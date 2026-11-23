package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	artifactservice "github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
	"github.com/FlyTOmeLight/server-go/constants"
	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestArtifactIntegrationScopeAndReplay(t *testing.T) {
	binding := memory.ArtifactBinding{
		StoreID: "artifact-test", TenantID: "tenant", ProjectID: "project", UserID: "owner",
	}
	grants := map[string]memory.ArtifactBinding{"authorized": binding}
	for _, dimension := range []string{"tenant", "project", "user"} {
		other := binding
		switch dimension {
		case "tenant":
			other.TenantID = "other-tenant"
		case "project":
			other.ProjectID = "other-project"
		case "user":
			other.UserID = "other-owner"
		}
		grants[dimension] = other
	}
	server := realArtifactServer(t, grants)
	archive := realArtifactStore(t, server.URL, "authorized")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	collectionID, err := archive.EnsureCollection(ctx, &binding, "entry", "collection-key")
	if err != nil {
		t.Fatal(err)
	}
	binding.ArtifactID = collectionID
	replayedID, err := archive.EnsureCollection(ctx, &binding, "entry", "collection-key")
	if err != nil || replayedID != collectionID {
		t.Fatalf("collection replay = %q, %v", replayedID, err)
	}
	doc := factDocument(memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}, "editor", "first", "tabs")
	doc.EntryID = "entry"
	doc.SchemaVersion = 1
	first, err := archive.PublishVersion(ctx, &binding, "entry", "publish-key", &doc)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := archive.PublishVersion(ctx, &binding, "entry", "publish-key", &doc)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("version replay = %+v, %v; want %+v", replayed, err, first)
	}
	read, err := archive.ReadVersion(ctx, *first)
	if err != nil || !reflect.DeepEqual(read, &doc) {
		t.Fatalf("fixed version = %+v, %v; want %+v", read, err, doc)
	}
	doc.Summary = "different parameters"
	if _, err := archive.PublishVersion(
		ctx,
		&binding,
		"entry",
		"publish-key",
		&doc,
	); !errors.Is(
		err,
		memory.ErrConflict,
	) {
		t.Fatalf("same key with changed document = %v", err)
	}
	for _, dimension := range []string{"tenant", "project", "user"} {
		t.Run(dimension, func(t *testing.T) {
			grant := grants[dimension]
			foreign := *first
			foreign.TenantID, foreign.ProjectID, foreign.UserID = grant.TenantID, grant.ProjectID, grant.UserID
			if got, err := archive.ReadVersion(
				ctx,
				foreign,
			); got != nil ||
				!errors.Is(err, memory.ErrUnavailable) ||
				errors.Is(err, memory.ErrVersionUnavailable) {
				t.Fatalf("unauthorized scope = %+v, %v", got, err)
			}
			otherArchive := realArtifactStore(t, server.URL, dimension)
			if got, err := otherArchive.ReadVersion(
				ctx,
				foreign,
			); got != nil ||
				!errors.Is(err, memory.ErrVersionUnavailable) {
				t.Fatalf("authorized foreign scope exposed version = %+v, %v", got, err)
			}
		})
	}
	for _, token := range []string{"", "invalid"} {
		unauthorized := realArtifactStore(t, server.URL, token)
		if got, err := unauthorized.ReadVersion(
			ctx,
			*first,
		); got != nil ||
			!errors.Is(err, memory.ErrUnavailable) ||
			errors.Is(err, memory.ErrVersionUnavailable) {
			t.Fatalf("unauthenticated read = %+v, %v", got, err)
		}
	}
}

func TestArtifactIntegrationPendingRecoveryAndHistory(t *testing.T) {
	binding := memory.ArtifactBinding{TenantID: "tenant", UserID: "owner"}
	server := realArtifactServer(t, map[string]memory.ArtifactBinding{"authorized": binding})
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var dropPublish, offline atomic.Bool
	type publishedVersion struct {
		ID         string `json:"id"`
		Sequence   int64  `json:"sequence"`
		VersionURI string `json:"versionUri"`
	}
	lost := make(chan publishedVersion, 1)
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method != http.MethodPost ||
			!strings.HasSuffix(response.Request.URL.Path, "/versions") ||
			response.StatusCode >= 300 || !dropPublish.CompareAndSwap(true, false) {
			return nil
		}
		payload, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return err
		}
		var version publishedVersion
		if err := json.Unmarshal(payload, &version); err != nil {
			return err
		}
		lost <- version
		return errors.New("injected loss of committed publish response")
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if offline.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(writer, request)
	}))
	t.Cleanup(gateway.Close)
	archive := realArtifactStore(t, gateway.URL, "authorized")
	svc, store := testServiceWithArchive(t, archive)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = access.WithScope(ctx, access.Scope{TenantID: binding.TenantID})
	doc := factDocument(
		memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner", Tenant: binding.TenantID},
		"editor",
		"first",
		"tabs",
	)
	entry, err := svc.Create(ctx, "owner", "editor", "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ActiveVersionRef == nil || entry.ActiveVersionRef.Sequence != 1 {
		t.Fatalf("first version = %+v", entry.ActiveVersionRef)
	}
	first := *entry.ActiveVersionRef
	doc.Summary = "second"
	dropPublish.Store(true)
	if _, err := svc.Update(
		ctx,
		"owner",
		"editor",
		"update",
		entry.ID,
		entry.Revision,
		doc,
	); !errors.Is(
		err,
		memory.ErrUnavailable,
	) {
		t.Fatalf("lost response = %v", err)
	}
	var committed publishedVersion
	select {
	case committed = <-lost:
	case <-ctx.Done():
		t.Fatal("no committed publish response captured")
	}
	if committed.ID == "" || committed.Sequence != 2 {
		t.Fatalf("committed version = %+v", committed)
	}
	var pending table.MemoryOperation
	if err := store.DB().
		Where("owner_id = ? AND request_key = ?", "owner", "update").
		Take(&pending).
		Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != string(memory.OperationPending) || pending.ToDomain().VersionRef != nil {
		t.Fatalf("unknown publication must stay pending without a confirmed reference: %+v", pending.ToDomain())
	}
	offline.Store(true)
	page, err := svc.ListVersions(ctx, "owner", entry.ID, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].VersionRef != first || !page.Items[0].Active {
		t.Fatalf("history while pending and offline = %+v, %v", page, err)
	}
	resumed, err := svc.ResumePending(ctx, ResumeBatch)
	if resumed != 0 || !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("offline recovery = %d, %v", resumed, err)
	}
	var offlineOperation table.MemoryOperation
	if err := store.DB().WithContext(ctx).
		Where("id = ?", pending.ID).Take(&offlineOperation).Error; err != nil {
		t.Fatal(err)
	}
	if offlineOperation.Status != string(memory.OperationPending) {
		t.Fatalf("offline operation = %+v", offlineOperation.ToDomain())
	}
	offline.Store(false)
	resumed, err = NewService(store.DB(), archive).ResumePending(ctx, ResumeBatch)
	if err != nil || resumed != 1 {
		t.Fatalf("recovery = %d, %v", resumed, err)
	}
	entry, err = svc.Get(ctx, "owner", entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Revision != 2 || entry.Summary != "second" || entry.ActiveVersionRef == nil ||
		entry.ActiveVersionRef.VersionID != committed.ID || entry.ActiveVersionRef.Sequence != committed.Sequence ||
		entry.ActiveVersionRef.VersionURI != committed.VersionURI {
		t.Fatalf("recovered entry = %+v; committed = %+v", entry, committed)
	}
	page, err = svc.ListVersions(ctx, "owner", entry.ID, 0, 10)
	if err != nil || len(page.Items) != 2 || !page.Items[0].Active || page.Items[1].Active {
		t.Fatalf("recovered history = %+v, %v", page, err)
	}
	version, err := svc.GetVersion(ctx, "owner", entry.ID, first.VersionID)
	if err != nil || version.Document.Summary != "first" || version.Active {
		t.Fatalf("original fixed version = %+v, %v", version, err)
	}
	version, err = svc.GetVersion(ctx, "owner", entry.ID, committed.ID)
	if err != nil || version.Document.Summary != "second" || !version.Active {
		t.Fatalf("recovered version = %+v, %v", version, err)
	}
	if _, err := svc.GetVersion(ctx, "other-owner", entry.ID, committed.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("foreign owner read = %v", err)
	}
	if resumed, err := svc.ResumePending(ctx, ResumeBatch); err != nil || resumed != 0 {
		t.Fatalf("repeated recovery = %d, %v", resumed, err)
	}
}

func TestArtifactIntegrationActorPrivate(t *testing.T) {
	const tenant, owner, other = "tenant", "owner", "other"
	server := realArtifactServer(t, map[string]memory.ArtifactBinding{
		"authorized": {TenantID: tenant, UserID: owner},
	})
	svc, _ := testServiceWithArchive(t, realArtifactStore(t, server.URL, "authorized"))
	svc.WithProjectReadCheck(allowMemoryProject)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: tenant})
	project := access.WithScope(ctx, access.Scope{TenantID: tenant, ProjectID: "project"})
	doc := factDocument(memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: tenant}, "editor", "first", "tabs")

	wrongActor := doc
	wrongActor.Scope.Actor = other
	if _, err := svc.Create(ctx, owner, owner, "bad-actor", wrongActor); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("foreign actor: %v", err)
	}
	projectOutput := doc
	projectOutput.OutputVisibility = memory.OutputProject
	if _, err := svc.Create(ctx, owner, owner, "bad-output", projectOutput); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("project output: %v", err)
	}
	entry, err := svc.Create(ctx, owner, owner, "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Scope.Owner != owner || entry.Scope.Actor != owner || entry.Binding == nil ||
		entry.Binding.TenantID != tenant || entry.Binding.ProjectID != "" || entry.Binding.UserID != owner {
		t.Fatalf("private scope or archive binding: %+v", entry)
	}

	for _, test := range []struct {
		name string
		ctx  context.Context
		user string
	}{
		{name: "other user", ctx: ctx, user: other},
		{name: "project scope", ctx: project, user: owner},
		{name: "other tenant", ctx: access.WithScope(ctx, access.Scope{TenantID: "other-tenant"}), user: owner},
	} {
		t.Run(test.name, func(t *testing.T) {
			if entries, err := svc.List(test.ctx, test.user, "", 50); err != nil || len(entries) != 0 {
				t.Fatalf("list: %+v, %v", entries, err)
			}
			if _, err := svc.Get(test.ctx, test.user, entry.ID); !errors.Is(err, memory.ErrNotFound) {
				t.Fatalf("get: %v", err)
			}
			if _, err := svc.ListVersions(
				test.ctx,
				test.user,
				entry.ID,
				0,
				50,
			); !errors.Is(
				err,
				memory.ErrNotFound,
			) {
				t.Fatalf("history: %v", err)
			}
			if _, err := svc.GetVersion(
				test.ctx,
				test.user,
				entry.ID,
				entry.ActiveVersionRef.VersionID,
			); !errors.Is(
				err,
				memory.ErrNotFound,
			) {
				t.Fatalf("get version: %v", err)
			}
		})
	}

	doc.Summary = "second"
	if _, err := svc.Update(
		ctx,
		other,
		other,
		"foreign-update",
		entry.ID,
		1,
		doc,
	); !errors.Is(
		err,
		memory.ErrNotFound,
	) {
		t.Fatalf("foreign update: %v", err)
	}
	updated, err := svc.Update(ctx, owner, owner, "update", entry.ID, 1, doc)
	if err != nil || updated.Binding == nil || *updated.Binding != *entry.Binding {
		t.Fatalf("update changed binding: %+v, %v", updated, err)
	}
	retired, err := svc.Retire(ctx, owner, owner, "retire", entry.ID, 2)
	if err != nil || retired.RetiredAt == nil {
		t.Fatalf("retire: %+v, %v", retired, err)
	}
	if _, err := svc.Retire(project, owner, owner, "retire", entry.ID, 2); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("project scope retirement replay: %v", err)
	}
	restored, err := svc.Restore(ctx, owner, owner, "restore", entry.ID, 3, doc)
	if err != nil || restored.RetiredAt != nil || restored.Revision != 4 {
		t.Fatalf("restore: %+v, %v", restored, err)
	}
	page, err := svc.ListVersions(ctx, owner, entry.ID, 0, 50)
	if err != nil || len(page.Items) != 3 || page.Items[0].VersionRef != *restored.ActiveVersionRef {
		t.Fatalf("history: %+v, %v", page, err)
	}
	version, err := svc.GetVersion(ctx, owner, entry.ID, entry.ActiveVersionRef.VersionID)
	if err != nil || version.Document.Scope != entry.Scope || version.Document.Summary != "first" {
		t.Fatalf("fixed version: %+v, %v", version, err)
	}
}

func TestArtifactIntegrationActorProject(t *testing.T) {
	const tenant, project, owner = "tenant", "project", "owner"
	binding := memory.ArtifactBinding{TenantID: tenant, ProjectID: project, UserID: owner}
	server := realArtifactServer(t, map[string]memory.ArtifactBinding{"authorized": binding})
	svc, _ := testServiceWithArchive(t, realArtifactStore(t, server.URL, "authorized"))
	readAllowed := true
	svc.WithProjectReadCheck(func(_ context.Context, user string, scope access.Scope) (bool, error) {
		return readAllowed && user == owner && scope.TenantID == tenant && scope.ProjectID == project, nil
	}).WithProjectWriteCheck(allowMemoryProject)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: tenant, ProjectID: project})
	doc := factDocument(memory.Scope{
		Kind: memory.ScopeActorProject, Tenant: tenant, Project: project,
	}, "editor", "project preference", "tabs")
	doc.OutputVisibility = memory.OutputProject
	entry, err := svc.Create(ctx, owner, owner, "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Binding == nil || entry.Binding.TenantID != tenant ||
		entry.Binding.ProjectID != project || entry.Binding.UserID != owner ||
		entry.ActiveVersionRef == nil {
		t.Fatalf("project archive binding: %+v", entry)
	}
	versionID := entry.ActiveVersionRef.VersionID
	version, err := svc.GetVersion(ctx, owner, entry.ID, versionID)
	if err != nil || version.Document.Scope != entry.Scope ||
		version.Document.OutputVisibility != memory.OutputProject {
		t.Fatalf("project archive version: %+v, %v", version, err)
	}
	readAllowed = false
	if _, err := svc.GetVersion(ctx, owner, entry.ID, versionID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("revoked project history access: %v", err)
	}
}

func realArtifactServer(t *testing.T, grants map[string]memory.ArtifactBinding) *httptest.Server {
	t.Helper()
	database := fixture.Open(t, db.DriverPostgreSQL)
	return fixture.HTTP(t, database, func(request *http.Request) (artifactservice.RequestIdentity, error) {
		token, present := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		grant, authorized := grants[token]
		if !present || !authorized {
			return artifactservice.RequestIdentity{}, serrors.NewError(
				http.StatusUnauthorized,
				"Unauthorized",
				"test credential required",
			)
		}
		if request.Header.Get("x-forge-tenant-id") != grant.TenantID ||
			request.Header.Get("x-forge-project-id") != grant.ProjectID ||
			request.Header.Get("X-Resource-User-ID") != grant.UserID ||
			request.Header.Get(constants.HeaderImpersonateTenantID) != grant.TenantID ||
			request.Header.Get(constants.HeaderImpersonateProjectID) != grant.ProjectID ||
			request.Header.Get(constants.HeaderImpersonateUserID) != grant.UserID {
			return artifactservice.RequestIdentity{}, serrors.NewError(
				http.StatusForbidden,
				"Forbidden",
				"scope not granted",
			)
		}
		identity := artifactservice.RequestIdentity{
			UserID: grant.UserID, TenantID: grant.TenantID,
			ResourceUserID: &grant.UserID, OwnershipState: artifactservice.OwnershipActive,
		}
		if grant.ProjectID != "" {
			identity.ProjectID = &grant.ProjectID
		}
		return identity, nil
	})
}

func realArtifactStore(t *testing.T, endpoint, token string) *client.MemoryArchive {
	t.Helper()
	archive, err := client.NewMemoryArchive(client.ArtifactConfig{
		Endpoint: endpoint, BearerToken: token, StoreID: "artifact-test", TenantID: "tenant",
		Timeout: 3 * time.Second, MaxPayloadBytes: 256 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := archive.Close(); err != nil {
			t.Error(err)
		}
	})
	return archive
}
