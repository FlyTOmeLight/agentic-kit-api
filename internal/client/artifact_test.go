package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestArtifactConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		config ArtifactConfig
	}{
		{"missing endpoint", ArtifactConfig{MaxPayloadBytes: 4096}},
		{"invalid endpoint", ArtifactConfig{Endpoint: "relative", MaxPayloadBytes: 4096}},
		{"missing limit", ArtifactConfig{Endpoint: "http://localhost"}},
		{"negative limit", ArtifactConfig{Endpoint: "http://localhost", MaxPayloadBytes: -1}},
		{"negative timeout", ArtifactConfig{Endpoint: "http://localhost", MaxPayloadBytes: 4096, Timeout: -time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewArtifactStore(test.config)
			if err == nil || store != nil {
				t.Fatalf("NewArtifactStore = %v, %v; want configuration error", store, err)
			}
		})
	}
	store, err := NewArtifactStore(
		ArtifactConfig{Endpoint: "http://localhost", TenantID: "storage-tenant", MaxPayloadBytes: 4096},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	binding, err := store.ResolveBinding(memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}, "editor")
	if err != nil || binding.StoreID != "artifact" {
		t.Fatalf("default binding = %+v, %v", binding, err)
	}
}

func TestArtifactResolveBinding(t *testing.T) {
	store := &ArtifactStore{cfg: ArtifactConfig{StoreID: "archive", TenantID: "storage-tenant"}}
	for _, test := range []struct {
		name  string
		scope memory.Scope
		actor string
		want  *memory.ArtifactBinding
	}{
		{
			"owner mapped tenant",
			memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"},
			"editor",
			&memory.ArtifactBinding{StoreID: "archive", TenantID: "storage-tenant", UserID: "owner"},
		},
		{
			"owner business tenant",
			memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner", Tenant: "tenant"},
			"editor",
			&memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", UserID: "owner"},
		},
		{
			"actor private",
			memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: "tenant"},
			"actor",
			&memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", UserID: "actor"},
		},
		{
			"actor project",
			memory.Scope{Kind: memory.ScopeActorProject, Tenant: "tenant", Project: "project"},
			"actor",
			&memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", ProjectID: "project", UserID: "actor"},
		},
		{
			"project shared",
			memory.Scope{Kind: memory.ScopeProjectShared, Tenant: "tenant", Project: "project"},
			"creator",
			&memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", ProjectID: "project", UserID: "creator"},
		},
		{"owner missing", memory.Scope{Kind: memory.ScopeOwnerPrivate}, "editor", nil},
		{"owner with project", memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner", Project: "project"}, "editor", nil},
		{"actor missing tenant", memory.Scope{Kind: memory.ScopeActorPrivate}, "actor", nil},
		{"actor missing user", memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: "tenant"}, "", nil},
		{"private with project", memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: "tenant", Project: "project"}, "actor", nil},
		{"actor missing project", memory.Scope{Kind: memory.ScopeActorProject, Tenant: "tenant"}, "actor", nil},
		{"shared missing project", memory.Scope{Kind: memory.ScopeProjectShared, Tenant: "tenant"}, "actor", nil},
		{"unsupported scope", memory.Scope{Kind: "Unknown"}, "actor", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := store.ResolveBinding(test.scope, test.actor)
			if test.want == nil {
				if !errors.Is(err, memory.ErrInvalid) || got != nil {
					t.Fatalf("binding = %+v, %v; want ErrInvalid", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("binding = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	store.cfg.TenantID = ""
	if _, err := store.ResolveBinding(
		memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"},
		"editor",
	); !errors.Is(
		err,
		memory.ErrInvalid,
	) {
		t.Fatalf("missing tenant mapping error = %v", err)
	}
}

func TestArtifactSDKRequestsAndFixedReferences(t *testing.T) {
	for _, project := range []string{"", "project"} {
		t.Run("project="+project, func(t *testing.T) {
			binding := artifactTestBinding()
			binding.ProjectID = project
			doc := artifactTestDocument()
			var collections, versions atomic.Int32
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				assertArtifactIdentity(t, request, binding)
				var body map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				switch request.URL.Path {
				case "/v1/artifacts":
					collections.Add(1)
					want := map[string]json.RawMessage{
						"requestKey":    json.RawMessage(`"operation-collection"`),
						"type":          json.RawMessage(`"agent-memory"`),
						"name":          json.RawMessage(`"memory-entry"`),
						"versionScheme": json.RawMessage(`"integer"`),
					}
					if !reflect.DeepEqual(body, want) {
						t.Errorf(
							"collection body = %s, want %v",
							mustArtifactJSON(t, body),
							want,
						)
					}
					writeArtifactJSON(
						t,
						writer,
						201,
						map[string]any{"id": binding.ArtifactID, "state": "archived"},
					)
				case "/v1/artifacts/artifact/versions":
					versions.Add(1)
					if len(body) != 3 || string(body["requestKey"]) != `"operation"` ||
						string(body["protected"]) != "true" {
						t.Errorf("publish body = %s", mustArtifactJSON(t, body))
					}
					var got memory.Document
					if err := json.Unmarshal(
						body["payload"],
						&got,
					); err != nil ||
						!reflect.DeepEqual(&got, doc) {
						t.Errorf("payload = %+v, %v; want %+v", got, err, doc)
					}
					writeArtifactJSON(t, writer, 200, artifactTestResponse(doc))
				default:
					t.Errorf("unexpected path %s", request.URL.Path)
					writer.WriteHeader(404)
				}
			})
			for attempt := 0; attempt < 2; attempt++ {
				collectionID, err := store.EnsureCollection(
					context.Background(),
					binding,
					"entry",
					"operation-collection",
				)
				if err != nil || collectionID != binding.ArtifactID {
					t.Fatalf("EnsureCollection = %q, %v", collectionID, err)
				}
				ref, err := store.PublishVersion(
					context.Background(),
					binding,
					"entry",
					"operation",
					doc,
				)
				if err != nil || !reflect.DeepEqual(ref, artifactTestRef(binding)) {
					t.Fatalf(
						"PublishVersion = %+v, %v; want %+v",
						ref,
						err,
						artifactTestRef(binding),
					)
				}
			}
			if collections.Load() != 2 || versions.Load() != 2 {
				t.Fatalf("requests: collections=%d versions=%d", collections.Load(), versions.Load())
			}
		})
	}
}

func TestArtifactRejectsInvalidInputWithoutRequests(t *testing.T) {
	var calls atomic.Int32
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(500)
	})
	for _, test := range []struct {
		name    string
		binding *memory.ArtifactBinding
	}{
		{"nil binding", nil},
		{"missing store", &memory.ArtifactBinding{TenantID: "tenant", UserID: "user"}},
		{"missing tenant", &memory.ArtifactBinding{StoreID: "archive", UserID: "user"}},
		{"missing user", &memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant"}},
		{"different store", &memory.ArtifactBinding{StoreID: "other-deployment", TenantID: "tenant", UserID: "user", ArtifactID: "original"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.EnsureCollection(
				context.Background(),
				test.binding,
				"entry",
				"key",
			); !errors.Is(
				err,
				memory.ErrInvalid,
			) {
				t.Fatalf("EnsureCollection error = %v", err)
			}
			if _, err := store.PublishVersion(
				context.Background(),
				test.binding,
				"entry",
				"key",
				artifactTestDocument(),
			); !errors.Is(
				err,
				memory.ErrInvalid,
			) {
				t.Fatalf("PublishVersion error = %v", err)
			}
		})
	}
	for _, test := range []struct {
		name, entry, key string
		doc              *memory.Document
	}{
		{"missing entry", "", "key", artifactTestDocument()},
		{"missing key", "entry", "", artifactTestDocument()},
		{"nil document", "entry", "key", nil},
		{"invalid document JSON", "entry", "key", &memory.Document{Body: json.RawMessage(`{`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.PublishVersion(
				context.Background(),
				artifactTestBinding(),
				test.entry,
				test.key,
				test.doc,
			); !errors.Is(
				err,
				memory.ErrInvalid,
			) {
				t.Fatalf("PublishVersion error = %v", err)
			}
			if test.entry == "" || test.key == "" {
				if _, err := store.EnsureCollection(
					context.Background(),
					artifactTestBinding(),
					test.entry,
					test.key,
				); !errors.Is(
					err,
					memory.ErrInvalid,
				) {
					t.Fatalf("EnsureCollection error = %v", err)
				}
			}
		})
	}
	binding := artifactTestBinding()
	binding.ArtifactID = ""
	if _, err := store.PublishVersion(
		context.Background(),
		binding,
		"entry",
		"key",
		artifactTestDocument(),
	); !errors.Is(
		err,
		memory.ErrInvalid,
	) {
		t.Fatalf("missing artifact error = %v", err)
	}
	store.cfg.MaxPayloadBytes = 1
	if _, err := store.PublishVersion(
		context.Background(),
		artifactTestBinding(),
		"entry",
		"key",
		artifactTestDocument(),
	); !errors.Is(
		err,
		memory.ErrInvalid,
	) {
		t.Fatalf("payload limit error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs sent %d requests", calls.Load())
	}
}

func TestArtifactGonePreservesOriginalPublication(t *testing.T) {
	binding := artifactTestBinding()
	var calls atomic.Int32
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assertArtifactIdentity(t, request, binding)
		if request.URL.Path != "/v1/artifacts/artifact/versions" {
			t.Errorf("unexpected request (must not create replacement collection): %s", request.URL.Path)
		}
		var body struct {
			RequestKey string `json:"requestKey"`
		}
		if err := json.NewDecoder(request.Body).
			Decode(&body); err != nil ||
			body.RequestKey != "original-operation" {
			t.Errorf("publish key = %q, %v", body.RequestKey, err)
		}
		writeArtifactJSON(t, writer, 410, map[string]any{
			"code": "VersionDeleted", "message": "version is deleted",
			"version": map[string]any{
				"id": "version", "artifactID": "artifact", "version": "2", "sequence": 2,
				"versionUri": "artifact://tenant/project/artifact/versions/2",
			},
		})
	})
	original := *binding
	for attempt := 0; attempt < 2; attempt++ {
		ref, err := store.PublishVersion(
			context.Background(),
			binding,
			"entry",
			"original-operation",
			artifactTestDocument(),
		)
		var unavailable *memory.VersionUnavailableError
		if ref != nil || !errors.As(err, &unavailable) || !errors.Is(err, memory.ErrVersionUnavailable) ||
			!errors.Is(err, memory.ErrUnavailable) {
			t.Fatalf("PublishVersion = %+v, %v; want version unavailable", ref, err)
		}
		if unavailable.Code() != "MemoryVersionUnavailable" || unavailable.Version != "2" ||
			unavailable.Ref != *artifactTestRef(binding) ||
			errors.Unwrap(unavailable) == nil {
			t.Fatalf("tombstone identity = %+v", unavailable)
		}
	}
	if *binding != original || calls.Load() != 2 {
		t.Fatalf(
			"binding=%+v calls=%d; want original binding and two original-key requests",
			binding,
			calls.Load(),
		)
	}
}

func TestArtifactRemoteErrorMapping(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         int
		body           string
		want           error
		collectionWant error
	}{
		{"in progress", 409, `{"code":"RequestInProgress"}`, memory.ErrUnavailable, nil},
		{"expired attempt", 409, `{"code":"RequestAttemptExpired"}`, memory.ErrUnavailable, nil},
		{"parameter mismatch", 409, `{"code":"IdempotentParameterMismatch"}`, memory.ErrConflict, nil},
		{"name collision", 409, `{"code":"ArtifactAlreadyExists"}`, memory.ErrConflict, nil},
		{"archived collection", 409, `{"code":"ArtifactArchived"}`, memory.ErrConflict, nil},
		{"bad request", 400, `{"code":"InvalidParameter"}`, memory.ErrUnavailable, nil},
		{"forbidden storage identity", 403, `{"code":"Forbidden"}`, memory.ErrUnavailable, nil},
		{"missing collection", 404, `{"code":"NotFound"}`, memory.ErrUnavailable, nil},
		{"payload limit", 413, `{"code":"PayloadTooLarge"}`, memory.ErrUnavailable, nil},
		{"internal failure", 500, `{"code":"InternalError"}`, memory.ErrUnavailable, nil},
		{"unavailable", 503, `{"code":"ServiceUnavailable"}`, memory.ErrUnavailable, nil},
		{"incomplete tombstone", 410, `{"code":"VersionDeleted","version":{"artifactID":"artifact"}}`, memory.ErrUnavailable, nil},
		{"foreign tombstone", 410, `{"code":"VersionDeleted","version":{"id":"version","artifactID":"other"}}`, memory.ErrConflict, memory.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				if _, err := writer.Write([]byte(test.body)); err != nil {
					t.Error(err)
				}
			})
			for attempt := 0; attempt < 2; attempt++ {
				_, err := store.PublishVersion(
					context.Background(),
					artifactTestBinding(),
					"entry",
					"same-key",
					artifactTestDocument(),
				)
				if !errors.Is(err, test.want) || errors.Is(err, memory.ErrVersionUnavailable) {
					t.Fatalf("PublishVersion error = %v, want %v", err, test.want)
				}
				_, err = store.EnsureCollection(
					context.Background(),
					artifactTestBinding(),
					"entry",
					"same-collection-key",
				)
				collectionWant := test.want
				if test.collectionWant != nil {
					collectionWant = test.collectionWant
				}
				if !errors.Is(err, collectionWant) {
					t.Fatalf("EnsureCollection error = %v, want %v", err, collectionWant)
				}
			}
			if calls.Load() != 4 {
				t.Fatalf(
					"requests = %d, want four explicit calls without automatic retries",
					calls.Load(),
				)
			}
		})
	}
}

func TestArtifactPublishValidatesResponse(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
		want   error
	}{
		{"different artifact", func(body map[string]any) { body["artifactID"] = "other" }, memory.ErrConflict},
		{"missing version ID", func(body map[string]any) { delete(body, "id") }, memory.ErrUnavailable},
		{"missing URI", func(body map[string]any) { delete(body, "versionUri") }, memory.ErrUnavailable},
		{"missing sequence", func(body map[string]any) { delete(body, "sequence") }, memory.ErrUnavailable},
		{"not ready", func(body map[string]any) { body["state"] = "deleting" }, memory.ErrUnavailable},
		{"not protected", func(body map[string]any) { body["protected"] = false }, memory.ErrUnavailable},
		{"missing payload", func(body map[string]any) { delete(body, "payload") }, memory.ErrUnavailable},
		{"wrong entry", func(body map[string]any) { doc := artifactTestDocument(); doc.EntryID = "other"; body["payload"] = doc }, memory.ErrUnavailable},
		{"wrong scope", func(body map[string]any) {
			doc := artifactTestDocument()
			doc.Scope.Owner = "other"
			body["payload"] = doc
		}, memory.ErrUnavailable},
		{"wrong schema", func(body map[string]any) { doc := artifactTestDocument(); doc.SchemaVersion++; body["payload"] = doc }, memory.ErrUnavailable},
		{"wrong document", func(body map[string]any) {
			doc := artifactTestDocument()
			doc.Summary = "changed"
			body["payload"] = doc
		}, memory.ErrUnavailable},
		{"large integer changed", func(body map[string]any) {
			doc := artifactTestDocument()
			doc.Body = json.RawMessage(`{"count":9007199254740992}`)
			body["payload"] = doc
		}, memory.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				body := artifactTestResponse(artifactTestDocument())
				test.change(body)
				writeArtifactJSON(t, writer, 200, body)
			})
			if ref, err := store.PublishVersion(
				context.Background(),
				artifactTestBinding(),
				"entry",
				"key",
				artifactTestDocument(),
			); ref != nil ||
				!errors.Is(err, test.want) {
				t.Fatalf("PublishVersion = %+v, %v; want %v", ref, err, test.want)
			}
		})
	}
}

func TestArtifactCollectionValidatesResponse(t *testing.T) {
	for _, test := range []struct {
		body map[string]any
		want error
	}{
		{map[string]any{}, memory.ErrUnavailable},
		{map[string]any{"id": "other"}, memory.ErrConflict},
	} {
		store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
			writeArtifactJSON(t, writer, 200, test.body)
		})
		if _, err := store.EnsureCollection(
			context.Background(),
			artifactTestBinding(),
			"entry",
			"key",
		); !errors.Is(
			err,
			test.want,
		) {
			t.Fatalf("EnsureCollection error = %v, want %v", err, test.want)
		}
	}
}

func TestArtifactTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	server.Close()
	store, err := NewArtifactStore(
		ArtifactConfig{Endpoint: server.URL, StoreID: "archive", Timeout: time.Second, MaxPayloadBytes: 4096},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.PublishVersion(
		context.Background(),
		artifactTestBinding(),
		"entry",
		"key",
		artifactTestDocument(),
	); !errors.Is(
		err,
		memory.ErrUnavailable,
	) {
		t.Fatalf("transport error = %v", err)
	}
	if _, err := store.EnsureCollection(
		context.Background(),
		artifactTestBinding(),
		"entry",
		"key",
	); !errors.Is(
		err,
		memory.ErrUnavailable,
	) {
		t.Fatalf("transport error = %v", err)
	}
}

func artifactTestServer(t *testing.T, handler http.HandlerFunc) *ArtifactStore {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := NewArtifactStore(ArtifactConfig{
		Endpoint:        server.URL,
		StoreID:         "archive",
		TenantID:        "storage-tenant",
		Timeout:         time.Second,
		MaxPayloadBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func artifactTestBinding() *memory.ArtifactBinding {
	return &memory.ArtifactBinding{
		StoreID:    "archive",
		TenantID:   "tenant",
		ProjectID:  "project",
		UserID:     "original-user",
		ArtifactID: "artifact",
	}
}

func artifactTestDocument() *memory.Document {
	return &memory.Document{
		SchemaVersion: 1, EntryID: "entry", Scope: memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"},
		Kind: memory.KindFact, SubjectKey: "counter", Summary: "完整文档",
		Body:       json.RawMessage(`{"count":9007199254740993}`),
		Conditions: memory.Conditions{TaskTags: []string{"test"}}, OutputVisibility: memory.OutputPrivate,
		Source: memory.Source{Class: memory.SourceHumanConfirmed, Actor: "current-editor"},
	}
}

func artifactTestResponse(doc *memory.Document) map[string]any {
	return map[string]any{
		"id": "version", "artifactID": "artifact", "version": "2", "sequence": 2,
		"versionUri": "artifact://tenant/project/artifact/versions/2",
		"state":      "ready", "protected": true, "payload": doc,
	}
}

func artifactTestRef(binding *memory.ArtifactBinding) *memory.VersionRef {
	return &memory.VersionRef{
		StoreID:    binding.StoreID,
		TenantID:   binding.TenantID,
		ProjectID:  binding.ProjectID,
		UserID:     binding.UserID,
		ArtifactID: binding.ArtifactID,
		VersionID:  "version",
		Sequence:   2,
		VersionURI: "artifact://tenant/project/artifact/versions/2",
	}
}

func assertArtifactIdentity(t *testing.T, request *http.Request, binding *memory.ArtifactBinding) {
	t.Helper()
	if request.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", request.Method)
	}
	for header, want := range map[string]string{
		"x-forge-tenant-id":  binding.TenantID,
		"x-forge-project-id": binding.ProjectID,
		"X-Resource-User-ID": binding.UserID,
		"X-Request-ID":       "entry",
	} {
		if got := request.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func writeArtifactJSON(t *testing.T, writer http.ResponseWriter, status int, body any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		t.Error(err)
	}
}

func mustArtifactJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
