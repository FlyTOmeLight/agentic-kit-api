package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/server-go/constants"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestResolveSkillAndReadSkillFileWithDownloadGrantAndCache(t *testing.T) {
	var s3Hits atomic.Int32
	var grantHits atomic.Int32
	s3Objects := map[string][]byte{
		"/tenant-bucket/skills/skill.md": []byte("# Go Review\nAlways check errors.\n"),
		"/tenant-bucket/skills/bin.dat":  {0x00, 0xff, 0xfe},
		"/tenant-bucket/skills/big.txt":  []byte(strings.Repeat("x", 128<<10+10)),
	}
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s3Hits.Add(1)
		if r.URL.Path == "/tenant-bucket/skills/redirect" {
			http.Redirect(w, r, "https://evil.example/secret", http.StatusFound)
			return
		}
		if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") ||
			r.Header.Get("X-Amz-Security-Token") != "secret-session-token" {
			http.Error(w, "missing sigv4 credentials", http.StatusForbidden)
			return
		}
		body, ok := s3Objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer s3Server.Close()

	var lastResolveResourceUser string
	var lastGrantResourceUser string
	artifactTypeToReturn := "agent-skill"
	versionStateToReturn := "ready"
	artifactServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("k", 32) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/resolve":
			lastResolveResourceUser = r.Header.Get("X-Resource-User-ID")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artifact": map[string]any{"id": "art-1", "type": artifactTypeToReturn},
				"version": map[string]any{
					"id":         "ver-1",
					"versionUri": "artifact://tenant/projects/proj/artifacts/agent-skill/art-1/versions/ver-1",
					"state":      versionStateToReturn,
					"payload":    map[string]any{"description": "Skill from payload"},
					"files": []map[string]any{
						{
							"fileID":      "file-md",
							"path":        "SKILL.md",
							"fileUri":     "artifact://tenant/projects/proj/artifacts/agent-skill/art-1/versions/ver-1/files/SKILL.md",
							"size":        32,
							"contentType": "text/markdown; charset=utf-8",
						},
					},
				},
			})
		case "/v1/storage/grants":
			grantHits.Add(1)
			lastGrantResourceUser = r.Header.Get("X-Resource-User-ID")
			if r.Header.Get(constants.HeaderImpersonateTenantID) != "tenant" ||
				r.Header.Get(constants.HeaderImpersonateUserID) != "user-1" ||
				r.Header.Get(constants.HeaderImpersonateProjectID) != "proj" {
				http.Error(w, "missing impersonation", http.StatusForbidden)
				return
			}
			var req storagev1.CreateGrantRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Scope.Preset != "download" || req.TTLSeconds == nil || *req.TTLSeconds != 60 ||
				len(req.FileIDs) != 1 {
				http.Error(w, "unexpected grant request", http.StatusBadRequest)
				return
			}
			keyMap := map[string]string{
				"file-md":       "skills/skill.md",
				"file-bin":      "skills/bin.dat",
				"file-big":      "skills/big.txt",
				"file-redirect": "skills/redirect",
			}
			objectKey, ok := keyMap[req.FileIDs[0]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(storagev1.GrantResponse{
				Endpoint:       s3Server.URL,
				Region:         "us-east-1",
				ForcePathStyle: true,
				ExpiresAt:      time.Now().Add(time.Minute),
				Actions:        []string{"s3:GetObject"},
				Credentials: storagev1.GrantCredentials{
					AccessKeyID:     "secret-access-key-id",
					SecretAccessKey: "secret-access-key-val",
					SessionToken:    "secret-session-token",
				},
				Files: []storagev1.GrantFile{{
					FileID:    req.FileIDs[0],
					Bucket:    "tenant-bucket",
					ObjectKey: objectKey,
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer artifactServer.Close()

	store, err := NewArtifact(artifactServer.URL, strings.Repeat("k", 32), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project := "proj"
	id := Identity{TenantID: "tenant", ProjectID: &project, PropagatedUserID: "user-1"}

	// Personal skill sets X-Resource-User-ID; shared skill omits it.
	resolved, err := store.ResolveSkill(
		t.Context(),
		id,
		"artifact://tenant/projects/proj/artifacts/agent-skill/art-1/aliases/latest",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if lastResolveResourceUser != "user-1" || resolved.Description != "Skill from payload" ||
		len(resolved.Files) != 1 {
		t.Fatalf("personal resolve=%+v header=%q", resolved, lastResolveResourceUser)
	}
	if _, err = store.ResolveSkill(
		t.Context(),
		id,
		"artifact://tenant/projects/proj/artifacts/agent-skill/art-1/aliases/latest",
		true,
	); err != nil {
		t.Fatal(err)
	}
	if lastResolveResourceUser != "" {
		t.Fatalf("shared resolve must omit X-Resource-User-ID, got %q", lastResolveResourceUser)
	}

	// Non-skill artifact types must be rejected with ErrForbidden.
	for _, badType := range []string{"agent", "agent-memory", "generic"} {
		artifactTypeToReturn = badType
		if _, err = store.ResolveSkill(
			t.Context(),
			id,
			"artifact://tenant/projects/proj/artifacts/agent/a/versions/v1",
			false,
		); !errors.Is(
			err,
			memory.ErrForbidden,
		) {
			t.Fatalf("type=%q got err=%v want ErrForbidden", badType, err)
		}
	}
	artifactTypeToReturn = "agent-skill"

	// Non-ready version must fail with ErrConflict.
	versionStateToReturn = "deleting"
	if _, err = store.ResolveSkill(
		t.Context(),
		id,
		"artifact://tenant/projects/proj/artifacts/agent-skill/art-1/versions/ver-1",
		false,
	); !errors.Is(
		err,
		agent.ErrConflict,
	) {
		t.Fatalf("non-ready version got err=%v want ErrConflict", err)
	}
	versionStateToReturn = "ready"

	// ReadSkillFile downloads via SigV4 grant and caches immutable file by scope+fileID.
	data, err := store.ReadSkillFile(t.Context(), id, true, "file-md", 128<<10)
	if err != nil || string(data) != "# Go Review\nAlways check errors.\n" {
		t.Fatalf("ReadSkillFile = %q, %v", data, err)
	}
	if lastGrantResourceUser != "" || grantHits.Load() != 1 || s3Hits.Load() != 1 {
		t.Fatalf(
			"shared grant header=%q grantHits=%d s3Hits=%d",
			lastGrantResourceUser,
			grantHits.Load(),
			s3Hits.Load(),
		)
	}
	// Second read hits cache without calling grant or S3 again.
	data2, err := store.ReadSkillFile(t.Context(), id, true, "file-md", 128<<10)
	if err != nil || string(data2) != string(data) || grantHits.Load() != 1 || s3Hits.Load() != 1 {
		t.Fatalf("expected cache hit: err=%v grantHits=%d s3Hits=%d", err, grantHits.Load(), s3Hits.Load())
	}
	// Personal scope uses a distinct cache key and sends X-Resource-User-ID.
	if _, err = store.ReadSkillFile(t.Context(), id, false, "file-md", 128<<10); err != nil {
		t.Fatal(err)
	}
	if lastGrantResourceUser != "user-1" || grantHits.Load() != 2 || s3Hits.Load() != 2 {
		t.Fatalf(
			"personal read did not isolate cache/header: header=%q grants=%d s3=%d",
			lastGrantResourceUser,
			grantHits.Load(),
			s3Hits.Load(),
		)
	}

	// Reject binary, oversize, and redirected objects without leaking credentials.
	if _, err = store.ReadSkillFile(t.Context(), id, true, "file-bin", 128<<10); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("binary file err=%v want ErrInvalid", err)
	}
	if _, err = store.ReadSkillFile(t.Context(), id, true, "file-big", 128<<10); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("oversize file err=%v want ErrInvalid", err)
	}
	if _, err = store.ReadSkillFile(
		t.Context(),
		id,
		true,
		"file-redirect",
		128<<10,
	); !errors.Is(
		err,
		agent.ErrUnavailable,
	) {
		t.Fatalf("redirect err=%v want ErrUnavailable", err)
	} else if strings.Contains(
		err.Error(),
		"secret-",
	) {
		t.Fatalf("error leaked credentials: %v", err)
	}
}
