package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const projectWriteTenant = "tenant-project-write"

func allowMemoryProject(context.Context, string, access.Scope) (bool, error) {
	return true, nil
}

func projectWriteContext() context.Context {
	return access.WithScope(context.Background(), access.Scope{
		TenantID: projectWriteTenant, ProjectID: "project-write",
	})
}

func projectWriteDocument(subject, summary string) memory.Document {
	data, _ := json.Marshal("body")
	return memory.Document{
		Scope: memory.Scope{
			Kind: memory.ScopeActorProject, Owner: "user", Tenant: projectWriteTenant,
			Project: "project-write", Actor: "user",
		},
		Kind:             memory.KindFact,
		SubjectKey:       subject,
		Summary:          summary,
		Body:             data,
		OutputVisibility: memory.OutputProject,
		Source:           memory.Source{Class: memory.SourceHumanConfirmed},
	}
}

func TestActorProjectMaintenanceRequiresWriteGrant(t *testing.T) {
	svc, _ := testService(t)
	ctx := projectWriteContext()
	doc := projectWriteDocument("write.grant", "requires project write")

	entry, err := svc.Create(ctx, "user", "user", "write-missing", doc)
	if !errors.Is(err, memory.ErrForbidden) || entry != nil {
		t.Fatalf("Create without check: err=%v entry=%v, want forbidden", err, entry)
	}

	svc.WithProjectWriteCheck(func(context.Context, string, access.Scope) (bool, error) {
		return false, nil
	})
	if _, err = svc.Create(ctx, "user", "user", "write-denied", doc); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("Create denied: err=%v, want forbidden", err)
	}

	svc.WithProjectWriteCheck(func(context.Context, string, access.Scope) (bool, error) {
		return false, errors.New("forge unavailable")
	})
	if _, err = svc.Create(ctx, "user", "user", "write-error", doc); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create check error: err=%v, want unavailable", err)
	}
}

func TestActorProjectMaintenanceLifecycle(t *testing.T) {
	svc, _ := testService(t)
	allowed := true
	svc.WithProjectWriteCheck(func(context.Context, string, access.Scope) (bool, error) {
		return allowed, nil
	})
	ctx := projectWriteContext()
	doc := projectWriteDocument("write.lifecycle", "project maintenance")

	created, err := svc.Create(ctx, "user", "user", "write-create", doc)
	if err != nil {
		t.Fatalf("Create allowed: %v", err)
	}
	if created.Scope.Kind != memory.ScopeActorProject ||
		created.OutputVisibility != memory.OutputProject {
		t.Fatalf("created scope/visibility=%s/%s", created.Scope.Kind, created.OutputVisibility)
	}

	doc.Summary = "updated by project member"
	updated, err := svc.Update(
		ctx, "user", "user", "write-update", created.ID, created.Revision, doc,
	)
	if err != nil {
		t.Fatalf("Update allowed: %v", err)
	}
	if updated.Summary != doc.Summary || updated.Revision != created.Revision+1 {
		t.Fatalf("updated summary/revision=%s/%d", updated.Summary, updated.Revision)
	}

	retired, err := svc.Retire(ctx, "user", "user", "write-retire", updated.ID, updated.Revision)
	if err != nil {
		t.Fatalf("Retire allowed: %v", err)
	}
	if retired.RetiredAt == nil {
		t.Fatal("retired entry has no retired time")
	}

	allowed = false
	doc.Summary = "must not apply after revoke"
	if _, err = svc.Restore(
		ctx, "user", "user", "write-restore", updated.ID, retired.Revision, doc,
	); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("Restore after revoke: err=%v, want forbidden", err)
	}
	if _, err = svc.Retire(
		ctx, "user", "user", "write-retire", updated.ID, updated.Revision,
	); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("Retire replay after revoke: err=%v, want forbidden", err)
	}
}

func TestActorProjectMaintenanceValidatesScopeAndVisibility(t *testing.T) {
	svc, _ := testService(t)
	svc.WithProjectWriteCheck(allowMemoryProject)
	ctx := projectWriteContext()

	private := projectWriteDocument("write.visibility", "private project entry")
	private.OutputVisibility = ""
	entry, err := svc.Create(ctx, "user", "user", "write-private", private)
	if err != nil || entry.OutputVisibility != memory.OutputPrivate {
		t.Fatalf("private ActorProject entry: entry=%+v err=%v", entry, err)
	}

	wrongProject := projectWriteDocument("write.scope", "other project")
	wrongProject.Scope.Project = "other-project"
	if _, err := svc.Create(
		ctx,
		"user",
		"user",
		"write-scope",
		wrongProject,
	); !errors.Is(
		err,
		memory.ErrForbidden,
	) {
		t.Fatalf("scope mismatch: err=%v, want forbidden", err)
	}

	otherActor := projectWriteDocument("write.actor", "another actor")
	otherActor.Scope.Actor = "someone-else"
	if _, err := svc.Create(ctx, "user", "user", "write-actor", otherActor); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("actor mismatch: err=%v, want forbidden", err)
	}
}

func TestActorProjectMaintenanceReadsVerifiedProjectEntries(t *testing.T) {
	archive := &historyArchiver{documents: make(map[string]memory.Document)}
	svc, _ := testServiceWithArchive(t, archive)
	readAllowed := true
	svc.WithProjectWriteCheck(allowMemoryProject).
		WithProjectReadCheck(func(context.Context, string, access.Scope) (bool, error) {
			return readAllowed, nil
		})
	ctx := projectWriteContext()
	doc := projectWriteDocument("write.read", "project read")
	entry, err := svc.Create(ctx, "user", "user", "write-read", doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Summary = "project read updated"
	entry, err = svc.Update(ctx, "user", "user", "write-read-update", entry.ID, entry.Revision, doc)
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Get(ctx, "user", entry.ID)
	if err != nil || got.Scope.Kind != memory.ScopeActorProject || got.Summary != doc.Summary {
		t.Fatalf("Get project entry: %+v, err=%v", got, err)
	}
	entries, err := svc.List(ctx, "user", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range entries {
		if item.ID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List project entry missing: %+v", entries)
	}
	page, err := svc.ListVersions(ctx, "user", entry.ID, 0, 10)
	if err != nil || len(page.Items) != 2 || page.Items[0].AppliedRevision != entry.Revision {
		t.Fatalf("ListVersions project entry: %+v, err=%v", page, err)
	}
	version, err := svc.GetVersion(ctx, "user", entry.ID, entry.ActiveVersionRef.VersionID)
	if err != nil || version.Document.Summary != doc.Summary {
		t.Fatalf("GetVersion project entry: %+v, err=%v", version, err)
	}

	otherProject := access.WithScope(ctx, access.Scope{
		TenantID: projectWriteTenant, ProjectID: "other-project",
	})
	if _, err = svc.Get(otherProject, "user", entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("other project Get: err=%v, want not found", err)
	}
	entries, err = svc.List(otherProject, "user", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range entries {
		if item.ID == entry.ID {
			t.Fatalf("other project List exposed entry: %+v", item)
		}
	}
	if _, err = svc.ListVersions(otherProject, "user", entry.ID, 0, 10); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("other project ListVersions: err=%v, want not found", err)
	}
	if _, err = svc.GetVersion(
		otherProject, "user", entry.ID, entry.ActiveVersionRef.VersionID,
	); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("other project GetVersion: err=%v, want not found", err)
	}

	otherActor := "someone-else"
	if _, err = svc.Get(ctx, otherActor, entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("other actor Get: err=%v, want not found", err)
	}
	if _, err = svc.GetVersion(
		ctx, otherActor, entry.ID, entry.ActiveVersionRef.VersionID,
	); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("other actor GetVersion: err=%v, want not found", err)
	}

	readAllowed = false
	if _, err = svc.Get(ctx, "user", entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Get after revocation: err=%v, want not found", err)
	}
	if _, err = svc.List(ctx, "user", "", 10); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("List after revocation: err=%v, want forbidden", err)
	}
	if _, err = svc.ListVersions(ctx, "user", entry.ID, 0, 10); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("ListVersions after revocation: err=%v, want not found", err)
	}
	if _, err = svc.GetVersion(
		ctx, "user", entry.ID, entry.ActiveVersionRef.VersionID,
	); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("GetVersion after revocation: err=%v, want not found", err)
	}
}

func TestProjectOwnerPrivateCannotCrossProject(t *testing.T) {
	svc, _ := testService(t)
	svc.WithProjectReadCheck(allowMemoryProject).WithProjectWriteCheck(allowMemoryProject)
	ctx := projectWriteContext()
	doc := projectWriteDocument("owner.isolation", "project owner's entry")
	doc.Scope.Kind, doc.Scope.Actor = memory.ScopeOwnerPrivate, ""
	entry, err := svc.Create(ctx, "user", "user", "owner-create", doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"", "other-project"} {
		other := access.WithScope(ctx, access.Scope{TenantID: projectWriteTenant, ProjectID: project})
		otherDoc := doc
		otherDoc.Scope.Project = project
		if _, err := svc.Get(other, "user", entry.ID); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("Get from %q: %v", project, err)
		}
		if listed, err := svc.List(other, "user", "", 10); err != nil || len(listed) != 0 {
			t.Fatalf("List from %q: got %d entries, err=%v", project, len(listed), err)
		}
		if _, err := svc.Update(
			other,
			"user",
			"user",
			"owner-update",
			entry.ID,
			entry.Revision,
			otherDoc,
		); !errors.Is(
			err,
			memory.ErrNotFound,
		) {
			t.Fatalf("Update from %q: %v", project, err)
		}
		if _, err := svc.Retire(
			other,
			"user",
			"user",
			"owner-retire",
			entry.ID,
			entry.Revision,
		); !errors.Is(
			err,
			memory.ErrNotFound,
		) {
			t.Fatalf("Retire from %q: %v", project, err)
		}
	}
}

func TestActorProjectRecallUsesOutputAudience(t *testing.T) {
	svc, store := testService(t)
	svc.WithProjectWriteCheck(allowMemoryProject)
	ctx := projectWriteContext()
	scope, _ := access.FromContext(ctx)
	ctx = access.WithProjectRead(ctx, "user", scope)
	projectDoc := projectWriteDocument("visibility.project", "editor project rule")
	projectEntry, err := svc.Create(ctx, "user", "user", "visibility-project", projectDoc)
	if err != nil {
		t.Fatal(err)
	}
	privateDoc := projectWriteDocument("visibility.private", "editor private rule")
	privateDoc.OutputVisibility = memory.OutputPrivate
	privateEntry, err := svc.Create(ctx, "user", "user", "visibility-private", privateDoc)
	if err != nil {
		t.Fatal(err)
	}

	err = store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, test := range []struct {
			audience memory.OutputAudience
			want     int
		}{
			{audience: memory.AudienceProject, want: 1},
			{audience: memory.AudiencePrivate, want: 2},
		} {
			entries, err := svc.RecallTx(
				ctx,
				tx,
				projectEntry.Scope,
				test.audience,
				"",
				"editor",
				memory.RecallFilter{},
			)
			if err != nil {
				return err
			}
			if len(entries) != test.want || (test.want == 1 && entries[0].ID != projectEntry.ID) {
				t.Errorf("%s recall: got %d entries, want %d", test.audience, len(entries), test.want)
			}
			if test.want == 2 && len(entries) == 2 && entries[0].ID != privateEntry.ID &&
				entries[1].ID != privateEntry.ID {
				t.Error("private recall omitted the private entry")
			}
		}
		if _, err := svc.RecallTx(
			ctx,
			tx,
			projectEntry.Scope,
			"",
			"",
			"editor",
			memory.RecallFilter{},
		); !errors.Is(
			err,
			memory.ErrForbidden,
		) {
			t.Errorf("missing audience: err=%v, want forbidden", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestActorProjectRecoveryChecksCurrentWriteGrant(t *testing.T) {
	archive := &fakeArchiver{failPublish: memory.ErrUnavailable}
	svc, _ := testServiceWithArchive(t, archive)
	allowed := true
	svc.WithProjectReadCheck(allowMemoryProject).
		WithProjectWriteCheck(func(context.Context, string, access.Scope) (bool, error) {
			return allowed, nil
		})
	ctx := projectWriteContext()
	doc := projectWriteDocument("write.recovery", "project recovery")
	if _, err := svc.Create(ctx, "user", "user", "write-pending", doc); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create with unavailable archive: %v", err)
	}
	archive.failPublish = nil
	allowed = false
	if count, err := svc.ResumePending(
		context.Background(),
		10,
	); count != 0 ||
		!errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("ResumePending after revocation: count=%d err=%v", count, err)
	}
	if entries, err := svc.List(ctx, "user", "", 10); err != nil || len(entries) != 0 {
		t.Fatalf("List after denied recovery: count=%d err=%v", len(entries), err)
	}
	allowed = true
	if count, err := svc.ResumePending(context.Background(), 10); count != 1 || err != nil {
		t.Fatalf("ResumePending after restoring grant: count=%d err=%v", count, err)
	}
	if entries, err := svc.List(ctx, "user", "", 10); err != nil || len(entries) != 1 {
		t.Fatalf("List after recovery: count=%d err=%v", len(entries), err)
	}
}
