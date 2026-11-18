package service

// In-memory artifact service double for HTTP-level tests. It implements the
// three endpoints the adapter uses with the same idempotent request-key
// semantics as artifact-go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
)

type fakeArtifact struct {
	t *testing.T

	mu       sync.Mutex
	artifact map[string]string              // requestKey → artifact ID
	version  map[string]fakeArtifactVersion // artifactID+"/"+requestKey → version
	byID     map[string][]byte              // versionID → payload
	next     int
}

type fakeArtifactVersion struct {
	id  string
	key string
}

func newFakeArtifact(t *testing.T) (*httptest.Server, *fakeArtifact) {
	t.Helper()
	if shared, ok := fakeArtifacts.Load(t); ok {
		entry := shared.(fakeArtifactEntry)
		return entry.server, entry.fake
	}
	fake := &fakeArtifact{
		t:        t,
		artifact: map[string]string{},
		version:  map[string]fakeArtifactVersion{},
		byID:     map[string][]byte{},
	}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	fakeArtifacts.Store(t, fakeArtifactEntry{server: server, fake: fake})
	t.Cleanup(func() {
		fakeArtifacts.Delete(t)
		server.Close()
	})
	return server, fake
}

// fakeArtifacts shares one double per test so rebuilt routers keep the
// version bodies published earlier in the same test, like a real service.
var fakeArtifacts sync.Map

type fakeArtifactEntry struct {
	server *httptest.Server
	fake   *fakeArtifact
}

func (f *fakeArtifact) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/artifacts":
		var body struct {
			RequestKey string `json:"requestKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RequestKey == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if id, ok := f.artifact[body.RequestKey]; ok {
			writeFakeJSON(w, http.StatusOK, map[string]any{"id": id})
			return
		}
		f.next++
		id := fakeID("artifact", f.next)
		f.artifact[body.RequestKey] = id
		writeFakeJSON(w, http.StatusCreated, map[string]any{"id": id})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/artifacts/") && strings.HasSuffix(r.URL.Path, "/versions"):
		artifactID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"), "/versions")
		var body struct {
			RequestKey string          `json:"requestKey"`
			Payload    json.RawMessage `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).
			Decode(&body); err != nil || body.RequestKey == "" ||
			len(body.Payload) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		key := artifactID + "/" + body.RequestKey
		if version, ok := f.version[key]; ok {
			writeFakeJSON(w, http.StatusOK, map[string]any{
				"id":         version.id,
				"artifactID": artifactID,
				"versionUri": "artifact://fake/" + version.id,
			})
			return
		}
		f.next++
		id := fakeID("version", f.next)
		f.version[key] = fakeArtifactVersion{id: id, key: body.RequestKey}
		f.byID[id] = append([]byte(nil), body.Payload...)
		writeFakeJSON(w, http.StatusCreated, map[string]any{
			"id": id, "artifactID": artifactID, "versionUri": "artifact://fake/" + id,
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/artifacts/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"), "/")
		if len(parts) != 3 || parts[1] != "versions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		payload, ok := f.byID[parts[2]]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"id": parts[2], "payload": json.RawMessage(payload)})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func fakeID(prefix string, n int) string {
	return fmt.Sprintf("%s-%06d", prefix, n)
}

func writeFakeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// newArtifactClient builds the adapter against the fake service.
func newArtifactClient(t *testing.T, server *httptest.Server) client.ArtifactStore {
	t.Helper()
	store, err := client.NewArtifact(server.URL, strings.Repeat("service-key-", 3), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
