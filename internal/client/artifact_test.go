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
		{name: "missing endpoint", config: ArtifactConfig{MaxPayloadBytes: 4096}},
		{name: "invalid endpoint", config: ArtifactConfig{Endpoint: "relative", MaxPayloadBytes: 4096}},
		{name: "missing limit", config: ArtifactConfig{Endpoint: "http://localhost"}},
		{name: "negative limit", config: ArtifactConfig{Endpoint: "http://localhost", MaxPayloadBytes: -1}},
		{name: "negative timeout", config: ArtifactConfig{Endpoint: "http://localhost", MaxPayloadBytes: 4096, Timeout: -time.Second}},
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
			name:  "owner mapped tenant",
			scope: memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"},
			actor: "editor",
			want:  &memory.ArtifactBinding{StoreID: "archive", TenantID: "storage-tenant", UserID: "owner"},
		},
		{
			name:  "owner business tenant",
			scope: memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner", Tenant: "tenant"},
			actor: "editor",
			want:  &memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", UserID: "owner"},
		},
		{
			name:  "actor private",
			scope: memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: "tenant"},
			actor: "actor",
			want:  &memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", UserID: "actor"},
		},
		{
			name:  "actor project",
			scope: memory.Scope{Kind: memory.ScopeActorProject, Tenant: "tenant", Project: "project"},
			actor: "actor",
			want:  &memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", ProjectID: "project", UserID: "actor"},
		},
		{
			name:  "project shared",
			scope: memory.Scope{Kind: memory.ScopeProjectShared, Tenant: "tenant", Project: "project"},
			actor: "creator",
			want:  &memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant", ProjectID: "project", UserID: "creator"},
		},
		{name: "owner missing", scope: memory.Scope{Kind: memory.ScopeOwnerPrivate}, actor: "editor", want: nil},
		{name: "owner with project", scope: memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner", Project: "project"}, actor: "editor", want: nil},
		{name: "actor missing tenant", scope: memory.Scope{Kind: memory.ScopeActorPrivate}, actor: "actor", want: nil},
		{name: "actor missing user", scope: memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: "tenant"}, actor: "", want: nil},
		{name: "private with project", scope: memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: "tenant", Project: "project"}, actor: "actor", want: nil},
		{name: "actor missing project", scope: memory.Scope{Kind: memory.ScopeActorProject, Tenant: "tenant"}, actor: "actor", want: nil},
		{name: "shared missing project", scope: memory.Scope{Kind: memory.ScopeProjectShared, Tenant: "tenant"}, actor: "actor", want: nil},
		{name: "unsupported scope", scope: memory.Scope{Kind: "Unknown"}, actor: "actor", want: nil},
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
		{name: "nil binding", binding: nil},
		{name: "missing store", binding: &memory.ArtifactBinding{TenantID: "tenant", UserID: "user"}},
		{name: "missing tenant", binding: &memory.ArtifactBinding{StoreID: "archive", UserID: "user"}},
		{name: "missing user", binding: &memory.ArtifactBinding{StoreID: "archive", TenantID: "tenant"}},
		{name: "different store", binding: &memory.ArtifactBinding{StoreID: "other-deployment", TenantID: "tenant", UserID: "user", ArtifactID: "original"}},
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
		{name: "missing entry", entry: "", key: "key", doc: artifactTestDocument()},
		{name: "missing key", entry: "entry", key: "", doc: artifactTestDocument()},
		{name: "nil document", entry: "entry", key: "key", doc: nil},
		{name: "invalid document JSON", entry: "entry", key: "key", doc: &memory.Document{Body: json.RawMessage(`{`)}},
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
		{name: "in progress", status: 409, body: `{"code":"RequestInProgress"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "expired attempt", status: 409, body: `{"code":"RequestAttemptExpired"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "parameter mismatch", status: 409, body: `{"code":"IdempotentParameterMismatch"}`, want: memory.ErrConflict, collectionWant: nil},
		{name: "name collision", status: 409, body: `{"code":"ArtifactAlreadyExists"}`, want: memory.ErrConflict, collectionWant: nil},
		{name: "archived collection", status: 409, body: `{"code":"ArtifactArchived"}`, want: memory.ErrConflict, collectionWant: nil},
		{name: "bad request", status: 400, body: `{"code":"InvalidParameter"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "forbidden storage identity", status: 403, body: `{"code":"Forbidden"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "missing collection", status: 404, body: `{"code":"NotFound"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "payload limit", status: 413, body: `{"code":"PayloadTooLarge"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "internal failure", status: 500, body: `{"code":"InternalError"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "unavailable", status: 503, body: `{"code":"ServiceUnavailable"}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "incomplete tombstone", status: 410, body: `{"code":"VersionDeleted","version":{"artifactID":"artifact"}}`, want: memory.ErrUnavailable, collectionWant: nil},
		{name: "foreign tombstone", status: 410, body: `{"code":"VersionDeleted","version":{"id":"version","artifactID":"other"}}`, want: memory.ErrConflict, collectionWant: memory.ErrUnavailable},
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
		{name: "different artifact", change: func(body map[string]any) { body["artifactID"] = "other" }, want: memory.ErrConflict},
		{name: "missing version ID", change: func(body map[string]any) { delete(body, "id") }, want: memory.ErrUnavailable},
		{name: "missing URI", change: func(body map[string]any) { delete(body, "versionUri") }, want: memory.ErrUnavailable},
		{name: "missing sequence", change: func(body map[string]any) { delete(body, "sequence") }, want: memory.ErrUnavailable},
		{name: "not ready", change: func(body map[string]any) { body["state"] = "deleting" }, want: memory.ErrUnavailable},
		{name: "not protected", change: func(body map[string]any) { body["protected"] = false }, want: memory.ErrUnavailable},
		{name: "missing payload", change: func(body map[string]any) { delete(body, "payload") }, want: memory.ErrUnavailable},
		{name: "wrong entry", change: func(body map[string]any) { doc := artifactTestDocument(); doc.EntryID = "other"; body["payload"] = doc }, want: memory.ErrUnavailable},
		{name: "wrong scope", change: func(body map[string]any) {
			doc := artifactTestDocument()
			doc.Scope.Owner = "other"
			body["payload"] = doc
		}, want: memory.ErrUnavailable},
		{name: "wrong schema", change: func(body map[string]any) { doc := artifactTestDocument(); doc.SchemaVersion++; body["payload"] = doc }, want: memory.ErrUnavailable},
		{name: "wrong document", change: func(body map[string]any) {
			doc := artifactTestDocument()
			doc.Summary = "changed"
			body["payload"] = doc
		}, want: memory.ErrUnavailable},
		{name: "large integer changed", change: func(body map[string]any) {
			doc := artifactTestDocument()
			doc.Body = json.RawMessage(`{"count":9007199254740992}`)
			body["payload"] = doc
		}, want: memory.ErrUnavailable},
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
		{body: map[string]any{}, want: memory.ErrUnavailable},
		{body: map[string]any{"id": "other"}, want: memory.ErrConflict},
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
