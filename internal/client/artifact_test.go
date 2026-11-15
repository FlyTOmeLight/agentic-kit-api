package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/constants"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

func newTestStore(t *testing.T, handler http.Handler) ArtifactStore {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := NewArtifact(server.URL, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.(*artifactClient).Close() })
	return store
}

func testIdentity() Identity {
	project := "project-1"
	user := "user-1"
	return Identity{
		TenantID:       "tenant-1",
		ProjectID:      &project,
		ResourceUserID: &user,
		Session:        "session-token",
		CSRFToken:      "csrf-token",
		Origin:         "https://forge.example",
	}
}

func TestEnsureCollectionSendsStableRequestAndIdentity(t *testing.T) {
	calls := 0
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/artifacts" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertIdentityHeaders(t, r)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["type"] != "agent" || body["name"] != "agent-1" || body["versionScheme"] != "integer" {
			t.Fatalf("unexpected body %v", body)
		}
		if body["requestKey"] != collectionKey("agent-1") {
			t.Fatalf("request key changed: %v", body["requestKey"])
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": "artifact-1"})
	}))

	for range 2 {
		id, err := store.EnsureCollection(context.Background(), testIdentity(), "agent-1")
		if err != nil || id != "artifact-1" {
			t.Fatalf("ensure: id=%q err=%v", id, err)
		}
	}
	if calls != 2 {
		t.Fatalf("expected one call per invocation, got %d", calls)
	}
	if collectionKey("agent-1") == collectionKey("agent-2") {
		t.Fatal("collection key must distinguish agents")
	}
}

func TestPublishVersionProtectsAndReturnsFixedIdentity(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/artifacts/artifact-1/versions" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["requestKey"] != "key-1" || body["protected"] != true {
			t.Fatalf("unexpected body %v", body)
		}
		payload, _ := body["payload"].(map[string]any)
		if payload["systemPrompt"] != "hello" {
			t.Fatalf("payload changed: %v", body["payload"])
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         "version-1",
			"artifactID": "artifact-1",
			"versionUri": "artifact://tenant-1/x/versions/version-1",
		})
	}))

	ref, err := store.PublishVersion(
		context.Background(),
		testIdentity(),
		"artifact-1",
		"key-1",
		[]byte(`{"systemPrompt":"hello"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "version-1" || ref.URI == "" {
		t.Fatalf("unexpected reference %+v", ref)
	}
}

func TestGetPayloadReadsFixedVersion(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/artifacts/artifact-1/versions/version-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "version-1", "artifactID": "artifact-1", "state": "ready",
			"payload": map[string]any{"systemPrompt": "hello"},
		})
	}))

	payload, err := store.GetPayload(context.Background(), testIdentity(), "artifact-1", "version-1")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SystemPrompt string `json:"systemPrompt"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.SystemPrompt != "hello" {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
}

func TestArtifactErrorsMapToDomainSentinels(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{status: http.StatusBadRequest, want: agent.ErrInvalid},
		{status: http.StatusUnprocessableEntity, want: agent.ErrInvalid},
		{status: http.StatusNotFound, want: agent.ErrNotFound},
		{status: http.StatusGone, want: agent.ErrNotFound},
		{status: http.StatusConflict, want: agent.ErrConflict},
		{status: http.StatusUnauthorized, want: agent.ErrUnavailable},
		{status: http.StatusForbidden, want: agent.ErrUnavailable},
		{status: http.StatusServiceUnavailable, want: agent.ErrUnavailable},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tc.status, map[string]any{"code": "Failure", "message": "failed"})
			}))
			_, err := store.GetPayload(context.Background(), testIdentity(), "artifact-1", "version-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d mapped to %v, want %v", tc.status, err, tc.want)
			}
		})
	}
}

func TestMissingTenantFailsBeforeCallingArtifact(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("artifact must not be called without a verified tenant")
	}))
	identity := testIdentity()
	identity.TenantID = ""
	if _, err := store.EnsureCollection(
		context.Background(),
		identity,
		"agent-1",
	); !errors.Is(
		err,
		agent.ErrUnavailable,
	) {
		t.Fatalf("missing tenant accepted: %v", err)
	}
}

func assertIdentityHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get(constants.HeaderTenantID); got != "tenant-1" {
		t.Fatalf("tenant header %q", got)
	}
	if got := r.Header.Get(constants.HeaderProjectID); got != "project-1" {
		t.Fatalf("project header %q", got)
	}
	if got := r.Header.Get("X-Resource-User-ID"); got != "user-1" {
		t.Fatalf("resource user header %q", got)
	}
	if got := r.Header.Get("X-CSRF-Token"); got != "csrf-token" {
		t.Fatalf("csrf header %q", got)
	}
	if got := r.Header.Get("Origin"); got != "https://forge.example" {
		t.Fatalf("origin header %q", got)
	}
	if cookie, err := r.Cookie("forge_session"); err != nil || cookie.Value != "session-token" {
		t.Fatalf("session cookie missing: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
