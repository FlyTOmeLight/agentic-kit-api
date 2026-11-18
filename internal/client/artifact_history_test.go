package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestArtifactReadVersion(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	doc := artifactTestDocument()
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertArtifactReadIdentity(t, request, ref)
		if request.URL.Path != "/v1/artifacts/artifact/versions/version" {
			t.Errorf("path = %q, want version read path", request.URL.Path)
		}
		if request.URL.RawQuery != "" {
			t.Errorf("raw query = %q, want none", request.URL.RawQuery)
		}
		writeArtifactJSON(t, writer, http.StatusOK, artifactTestResponse(doc))
	})
	got, err := store.ReadVersion(context.Background(), ref)
	if err != nil || !reflect.DeepEqual(got, doc) {
		t.Fatalf("ReadVersion = %+v, %v; want %+v", got, err, doc)
	}
}

func TestArtifactReadVersionDoesNotResolveVersionURI(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	ref.VersionURI = "https://untrusted.invalid/objects/secret"
	doc := artifactTestDocument()
	var calls atomic.Int32
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assertArtifactReadIdentity(t, request, ref)
		if request.URL.Path != "/v1/artifacts/artifact/versions/version" {
			t.Errorf("path = %q, want fixed version path", request.URL.Path)
		}
		response := artifactTestResponse(doc)
		response["versionUri"] = ref.VersionURI
		writeArtifactJSON(t, writer, http.StatusOK, response)
	})
	got, err := store.ReadVersion(context.Background(), ref)
	if err != nil || !reflect.DeepEqual(got, doc) {
		t.Fatalf("ReadVersion = %+v, %v; want %+v", got, err, doc)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want only the fixed version read", calls.Load())
	}
}

func TestArtifactReadVersionMissingOrDeleted(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	for _, test := range []struct {
		name    string
		status  int
		body    any
		version string
	}{
		{
			name:   "missing",
			status: http.StatusNotFound,
			body:   map[string]any{"code": "NotFound", "message": "version not found"},
		},
		{
			name:    "deleted tombstone",
			status:  http.StatusGone,
			body:    artifactTestTombstone(ref, "2"),
			version: "2",
		},
		{
			name:   "gone without tombstone",
			status: http.StatusGone,
			body:   map[string]any{"code": "Gone", "message": "version is gone"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				writeArtifactJSON(t, writer, test.status, test.body)
			})
			got, err := store.ReadVersion(context.Background(), ref)
			if got != nil {
				t.Fatalf("document = %+v, want none", got)
			}
			unavailable := requireVersionUnavailable(t, err, ref)
			if unavailable.Version != test.version {
				t.Fatalf("tombstone version = %q, want %q", unavailable.Version, test.version)
			}
			if errors.Unwrap(unavailable) == nil {
				t.Fatal("missing or deleted version must retain its remote cause")
			}
		})
	}
}

func TestArtifactReadVersionRejectsForeignTombstone(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	body := artifactTestTombstone(ref, "2")
	body["version"].(map[string]any)["artifactID"] = "other"
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writeArtifactJSON(t, writer, http.StatusGone, body)
	})
	got, err := store.ReadVersion(context.Background(), ref)
	if got != nil || !errors.Is(err, memory.ErrUnavailable) ||
		errors.Is(err, memory.ErrVersionUnavailable) {
		t.Fatalf("ReadVersion = %+v, %v; want only ErrUnavailable", got, err)
	}
}

func TestArtifactReadVersionUnreadyOrMissingPayload(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "missing payload", change: func(body map[string]any) { delete(body, "payload") }},
		{name: "null payload", change: func(body map[string]any) { body["payload"] = nil }},
		{name: "deleting", change: func(body map[string]any) { body["state"] = "deleting" }},
		{name: "deleted state", change: func(body map[string]any) { body["state"] = "deleted" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				body := artifactTestResponse(artifactTestDocument())
				test.change(body)
				writeArtifactJSON(t, writer, http.StatusOK, body)
			})
			got, err := store.ReadVersion(context.Background(), ref)
			if got != nil {
				t.Fatalf("document = %+v, want none", got)
			}
			unavailable := requireVersionUnavailable(t, err, ref)
			if unavailable.Version != "" {
				t.Fatalf("tombstone version = %q, want none", unavailable.Version)
			}
		})
	}
}

func TestArtifactReadVersionRejectsMalformedPayload(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "invalid response JSON",
			body: `{"id":"version","artifactID":"artifact","version":"2",` +
				`"sequence":2,"versionUri":"artifact://tenant/project/artifact/versions/2",` +
				`"state":"ready","payload":{`,
		},
		{
			name: "payload is not a document",
			body: `{"id":"version","artifactID":"artifact","version":"2",` +
				`"sequence":2,"versionUri":"artifact://tenant/project/artifact/versions/2",` +
				`"state":"ready","payload":[]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusOK)
				if _, err := writer.Write([]byte(test.body)); err != nil {
					t.Error(err)
				}
			})
			got, err := store.ReadVersion(context.Background(), ref)
			if got != nil || !errors.Is(err, memory.ErrUnavailable) ||
				errors.Is(err, memory.ErrVersionUnavailable) {
				t.Fatalf("ReadVersion = %+v, %v; want only ErrUnavailable", got, err)
			}
		})
	}
}

func TestArtifactReadVersionRejectsIdentityMismatch(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "version ID", change: func(body map[string]any) { body["id"] = "other" }},
		{name: "artifact ID", change: func(body map[string]any) { body["artifactID"] = "other" }},
		{name: "sequence", change: func(body map[string]any) { body["sequence"] = 3 }},
		{name: "version URI", change: func(body map[string]any) {
			body["versionUri"] = "artifact://tenant/project/artifact/versions/3"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				body := artifactTestResponse(artifactTestDocument())
				test.change(body)
				writeArtifactJSON(t, writer, http.StatusOK, body)
			})
			got, err := store.ReadVersion(context.Background(), ref)
			if got != nil || !errors.Is(err, memory.ErrUnavailable) ||
				errors.Is(err, memory.ErrVersionUnavailable) {
				t.Fatalf("ReadVersion = %+v, %v; want only ErrUnavailable", got, err)
			}
		})
	}
}

func TestArtifactReadVersionRejectsInvalidReference(t *testing.T) {
	var calls atomic.Int32
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	})
	base := *artifactTestRef(artifactTestBinding())
	for _, test := range []struct {
		name   string
		change func(*memory.VersionRef)
		want   error
	}{
		{name: "missing store", change: func(ref *memory.VersionRef) { ref.StoreID = "" }, want: memory.ErrInvalid},
		{name: "missing tenant", change: func(ref *memory.VersionRef) { ref.TenantID = "" }, want: memory.ErrInvalid},
		{name: "missing user", change: func(ref *memory.VersionRef) { ref.UserID = "" }, want: memory.ErrInvalid},
		{name: "missing artifact", change: func(ref *memory.VersionRef) { ref.ArtifactID = "" }, want: memory.ErrInvalid},
		{name: "missing version", change: func(ref *memory.VersionRef) { ref.VersionID = "" }, want: memory.ErrInvalid},
		{name: "missing sequence", change: func(ref *memory.VersionRef) { ref.Sequence = 0 }, want: memory.ErrInvalid},
		{name: "negative sequence", change: func(ref *memory.VersionRef) { ref.Sequence = -1 }, want: memory.ErrInvalid},
		{name: "missing URI", change: func(ref *memory.VersionRef) { ref.VersionURI = "" }, want: memory.ErrInvalid},
		{name: "different store", change: func(ref *memory.VersionRef) { ref.StoreID = "previous-deployment" }, want: memory.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := base
			test.change(&ref)
			got, err := store.ReadVersion(context.Background(), ref)
			if got != nil || !errors.Is(err, test.want) {
				t.Fatalf("ReadVersion = %+v, %v; want %v", got, err, test.want)
			}
			if test.want == memory.ErrInvalid && errors.Is(err, memory.ErrUnavailable) {
				t.Fatalf("invalid reference reported unavailable: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid references sent %d requests", calls.Load())
	}
}

func TestArtifactReadVersionDependencyFailure(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
			writeArtifactJSON(t, writer, status, map[string]any{"code": "Unavailable"})
		})
		got, err := store.ReadVersion(context.Background(), ref)
		if got != nil || !errors.Is(err, memory.ErrUnavailable) ||
			errors.Is(err, memory.ErrVersionUnavailable) {
			t.Fatalf("status %d: ReadVersion = %+v, %v; want only ErrUnavailable", status, got, err)
		}
	}
}

func TestArtifactReadVersionTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	server.Close()
	store, err := NewMemoryArchive(ArtifactConfig{
		Endpoint:        server.URL,
		StoreID:         "archive",
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
	got, err := store.ReadVersion(context.Background(), *artifactTestRef(artifactTestBinding()))
	if got != nil || !errors.Is(err, memory.ErrUnavailable) ||
		errors.Is(err, memory.ErrVersionUnavailable) {
		t.Fatalf("ReadVersion = %+v, %v; want only ErrUnavailable", got, err)
	}
}

func TestArtifactReadVersionBoundsPayload(t *testing.T) {
	ref := *artifactTestRef(artifactTestBinding())
	store := artifactTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writeArtifactJSON(t, writer, http.StatusOK, artifactTestResponse(artifactTestDocument()))
	})
	store.cfg.MaxPayloadBytes = 8
	got, err := store.ReadVersion(context.Background(), ref)
	if got != nil || !errors.Is(err, memory.ErrUnavailable) ||
		errors.Is(err, memory.ErrVersionUnavailable) {
		t.Fatalf("ReadVersion = %+v, %v; want only ErrUnavailable", got, err)
	}
}

func assertArtifactReadIdentity(t *testing.T, request *http.Request, ref memory.VersionRef) {
	t.Helper()
	if request.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", request.Method)
	}
	for header, want := range map[string]string{
		"Authorization":      "Bearer test-service-token",
		"x-forge-tenant-id":  ref.TenantID,
		"x-forge-project-id": ref.ProjectID,
		"X-Resource-User-ID": ref.UserID,
		"X-Request-ID":       ref.VersionID,
	} {
		if got := request.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func artifactTestTombstone(ref memory.VersionRef, version string) map[string]any {
	return map[string]any{
		"code": "VersionDeleted", "message": "version is deleted",
		"version": map[string]any{
			"id": ref.VersionID, "artifactID": ref.ArtifactID, "version": version,
			"sequence": ref.Sequence, "versionUri": ref.VersionURI,
		},
	}
}

func requireVersionUnavailable(
	t *testing.T,
	err error,
	ref memory.VersionRef,
) *memory.VersionUnavailableError {
	t.Helper()
	var unavailable *memory.VersionUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want VersionUnavailableError", err)
	}
	if unavailable.Ref != ref {
		t.Fatalf("unavailable ref = %+v, want %+v", unavailable.Ref, ref)
	}
	if unavailable.Code() != "MemoryVersionUnavailable" {
		t.Fatalf("code = %q, want MemoryVersionUnavailable", unavailable.Code())
	}
	if !errors.Is(err, memory.ErrVersionUnavailable) || !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("error = %v, want both version and dependency unavailable", err)
	}
	return unavailable
}
