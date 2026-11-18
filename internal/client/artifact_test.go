package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/constants"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

const testArtifactServiceKey = "api-service-key-for-artifact-tests"

func newTestStore(t *testing.T, handler http.Handler) ArtifactStore {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := NewArtifact(server.URL, testArtifactServiceKey, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.(*artifactClient).Close() })
	return store
}

func testIdentity() Identity {
	project := "project-1"
	return Identity{
		TenantID:         "tenant-1",
		ProjectID:        &project,
		PropagatedUserID: "user-1",
		RequestID:        "request-1",
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
		assertIdentityHeaders(t, r)
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
		assertIdentityHeaders(t, r)
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

func TestInvalidIdentityFailsBeforeCallingArtifact(t *testing.T) {
	for _, test := range []struct {
		name, tenant, user string
	}{
		{name: "missing tenant", user: "user-1"},
		{name: "blank tenant", tenant: " ", user: "user-1"},
		{name: "padded tenant", tenant: " tenant-1", user: "user-1"},
		{name: "missing user", tenant: "tenant-1"},
		{name: "blank user", tenant: "tenant-1", user: " "},
		{name: "padded user", tenant: "tenant-1", user: " user-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			identity := testIdentity()
			identity.TenantID, identity.PropagatedUserID = test.tenant, test.user
			ctx := context.Background()
			_, ensureErr := store.EnsureCollection(ctx, identity, "agent-1")
			_, publishErr := store.PublishVersion(ctx, identity, "artifact-1", "key-1", []byte(`{}`))
			_, readErr := store.GetPayload(ctx, identity, "artifact-1", "version-1")
			for _, err := range []error{ensureErr, publishErr, readErr} {
				if !errors.Is(err, agent.ErrUnavailable) {
					t.Errorf("invalid identity accepted: %v", err)
				}
			}
			if calls.Load() != 0 {
				t.Fatalf("invalid identity reached artifact: %d calls", calls.Load())
			}
		})
	}
}

func TestNewArtifactRequiresServiceKey(t *testing.T) {
	for _, key := range []string{"", " \t"} {
		store, err := NewArtifact("http://127.0.0.1:18080", key, time.Second)
		if err == nil {
			_ = store.Close()
			t.Fatal("missing service key accepted")
		}
	}
}

func TestArtifactReplacesInheritedCredentials(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertIdentityHeaders(t, r)
		writeJSON(w, http.StatusCreated, map[string]any{"id": "artifact-1"})
	}))
	ctx := artifactv1.WithRequestIdentity(context.Background(), artifactv1.RequestIdentity{
		BearerToken: "untrusted-inbound-token",
		Session:     "untrusted-inbound-session",
		CSRFToken:   "untrusted-csrf",
		Origin:      "https://untrusted.example",
		TenantID:    "untrusted-tenant",
	})
	if _, err := store.EnsureCollection(ctx, testIdentity(), "agent-1"); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactIdentityIsPerRequest(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testArtifactServiceKey {
			t.Error("service credential lost")
		}
		user := r.Header.Get(constants.HeaderImpersonateUserID)
		if user == "" || r.Header.Get("X-Resource-User-ID") != user {
			t.Error("private owner must match propagated user")
		}
		switch user {
		case "user-1":
			assertIdentityHeaders(t, r)
		case "user-2":
			if r.Header.Get(constants.HeaderTenantID) != "tenant-2" ||
				r.Header.Get(constants.HeaderImpersonateTenantID) != "tenant-2" ||
				r.Header.Get(constants.HeaderProjectID) != "" ||
				r.Header.Get(constants.HeaderImpersonateProjectID) != "" {
				t.Error("scope leaked between requests")
			}
		default:
			t.Errorf("unexpected user %q", user)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": "artifact-1"})
	}))
	for _, identity := range []Identity{
		testIdentity(),
		{TenantID: "tenant-2", PropagatedUserID: "user-2"},
		testIdentity(),
	} {
		if _, err := store.EnsureCollection(context.Background(), identity, "agent-1"); err != nil {
			t.Fatal(err)
		}
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
	for header, want := range map[string]string{
		"Authorization":                      "Bearer " + testArtifactServiceKey,
		constants.HeaderImpersonateTenantID:  "tenant-1",
		constants.HeaderImpersonateUserID:    "user-1",
		constants.HeaderImpersonateProjectID: "project-1",
		"X-Request-ID":                       "request-1",
	} {
		if got := r.Header.Get(header); got != want {
			t.Errorf("%s header %q, want %q", header, got, want)
		}
	}
	for _, header := range []string{"Cookie", "X-CSRF-Token", "Origin"} {
		if got := strings.TrimSpace(r.Header.Get(header)); got != "" {
			t.Errorf("browser credential forwarded in %s", header)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
