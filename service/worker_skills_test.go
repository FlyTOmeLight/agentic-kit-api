package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/gin-gonic/gin"

	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type stubWorkerSkillArtifact struct {
	cfg   agent.AgentConfig
	skill client.ResolvedSkill
	files map[string][]byte
}

func (*stubWorkerSkillArtifact) EnsureCollection(context.Context, client.Identity, string) (string, error) {
	return "col", nil
}

func (*stubWorkerSkillArtifact) PublishVersion(
	context.Context,
	client.Identity,
	string,
	string,
	[]byte,
) (client.VersionRef, error) {
	return client.VersionRef{}, nil
}

func (s *stubWorkerSkillArtifact) GetPayload(context.Context, client.Identity, string, string) ([]byte, error) {
	return json.Marshal(s.cfg)
}

func (*stubWorkerSkillArtifact) ResolveAttachment(
	context.Context,
	client.Identity,
	string,
) (client.ResolvedAttachment, error) {
	return client.ResolvedAttachment{}, nil
}

func (s *stubWorkerSkillArtifact) ResolveSkill(
	context.Context,
	client.Identity,
	string,
	bool,
) (client.ResolvedSkill, error) {
	return s.skill, nil
}

func (s *stubWorkerSkillArtifact) ReadSkillFile(
	_ context.Context,
	_ client.Identity,
	_ bool,
	fileID string,
	_ int64,
) ([]byte, error) {
	data, ok := s.files[fileID]
	if !ok {
		return nil, durable.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (*stubWorkerSkillArtifact) Close() error { return nil }

func TestWorkerSkillReadRoute(t *testing.T) {
	// Route must not be mounted when Authentication.Worker is empty.
	disabled := NewRouter(t, nil, nil, nil)
	assertError(
		t,
		request(
			disabled,
			http.MethodPost,
			"/internal/v1/worker/runs/run-1/skills/read",
			`{"workerID":"w","epoch":1,"skill":"s"}`,
			"",
			"",
		),
		http.StatusNotFound,
		"APINotFound",
	)

	store := durableTestStore(t)
	const tenant, project, owner = "tenant", "proj-1", "owner-1"
	ctx := access.WithScope(t.Context(), access.Scope{TenantID: tenant, ProjectID: project})
	projCopy := project
	identity := client.Identity{TenantID: tenant, ProjectID: &projCopy, PropagatedUserID: owner}

	const versionURI = "artifact://tenant/projects/proj-1/artifacts/agent-skill/s1/versions/v1"
	cfg := agent.AgentConfig{
		SystemPrompt: "sys",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     3,
		Skills: []agent.SkillRef{{
			Name:        "go-review",
			ArtifactURI: versionURI,
			Description: "Review Go code",
		}},
	}
	art := &stubWorkerSkillArtifact{
		cfg: cfg,
		skill: client.ResolvedSkill{
			VersionURI: versionURI,
			Files: []client.ResolvedSkillFile{{
				Path:      "SKILL.md",
				FileID:    "file-md",
				FileURI:   versionURI + "/files/SKILL.md",
				MediaType: "text/markdown",
				SizeBytes: 17,
			}},
		},
		files: map[string][]byte{"file-md": []byte("# Go Review Rules")},
	}

	agentID := durable.NewID()
	if err := store.DB().Create(&table.Agent{
		ID: agentID, TenantID: tenant, ProjectID: project, OwnerID: owner, Name: "skill-agent",
		Draft: cfg, Revision: 1, PublishedRevision: 1, PublishedVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB().Create(&table.AgentVersion{
		AgentID: agentID, Number: 1, ArtifactVersionID: "v1", ArtifactVersionURI: "artifact://tenant/agent/v1",
	}).Error; err != nil {
		t.Fatal(err)
	}
	sess, err := store.CreateSession(ctx, owner, agentID, "skill-route")
	if err != nil {
		t.Fatal(err)
	}
	execSvc := appexec.NewService(store, art).
		WithProjectReadCheck(func(context.Context, string, access.Scope) (bool, error) {
			return true, nil
		})
	run, created, err := execSvc.SubmitChat(
		ctx,
		identity,
		owner,
		sess.ID,
		"req-route-skill",
		"hello",
		nil,
		time.Hour,
		3,
		memory.SelectionOff,
		memory.RecallFilter{},
	)
	if err != nil || !created {
		t.Fatalf("SubmitChat err=%v created=%t", err, created)
	}

	const workerToken = "worker-service-token-at-least-24-chars"
	auth := newTestAuthentication(nil)
	auth.Worker = []gin.HandlerFunc{func(c *gin.Context) {
		if strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ") != workerToken {
			serrors.Abort(c, http.StatusUnauthorized, "Unauthorized", "invalid worker token")
			return
		}
		c.Next()
	}}

	settings, err := conf.FromSettings(map[string]any{"AppName": "agentic", "AppCode": 100, "RunMode": "release"})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	log, err := logger.NewLogger(&logger.Config{Outputs: []logger.Output{{Type: "Console", Level: "Error"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	r := newRoutes(store, nil, art, nil, nil, nil, config.CallbackConfig{}, auth, execution.PricingCatalog{})
	h, err := host.NewHTTPHost(manager, log, nil, testRoutes{register: r.register})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)

	sdkClient, err := sdk.NewClient(srv.URL, workerToken, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	// Wrong token -> 401 Unauthorized.
	badClient, err := sdk.NewClient(srv.URL, "wrong-worker-token-at-least-24-chars", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = badClient.Close() })
	_, err = badClient.Executions.ReadSkillFile(t.Context(), &sdk.ReadSkillFileRequest{
		ID: run.ID, WorkerID: "worker-1", Epoch: 1, Skill: "go-review",
	})
	var apiErr *serrors.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("bad token err=%v want 401", err)
	}

	// Unclaimed run -> 409 Conflict (LeaseLost).
	_, err = sdkClient.Executions.ReadSkillFile(t.Context(), &sdk.ReadSkillFileRequest{
		ID: run.ID, WorkerID: "worker-1", Epoch: 1, Skill: "go-review",
	})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "LeaseLost" {
		t.Fatalf("unclaimed status err=%v want 409 LeaseLost", err)
	}

	claimed, err := store.Claim(t.Context(), "worker-1", time.Minute)
	if err != nil || claimed.ID != run.ID {
		t.Fatalf("Claim err=%v", err)
	}

	// Active lease -> 200 OK with frozen file content.
	got, err := sdkClient.Executions.ReadSkillFile(t.Context(), &sdk.ReadSkillFileRequest{
		ID: claimed.ID, WorkerID: "worker-1", Epoch: claimed.Epoch, Skill: "go-review",
	})
	if err != nil || got.Path != "SKILL.md" || got.Content != "# Go Review Rules" || got.VersionURI != versionURI {
		t.Fatalf("ReadSkillFile = %+v err=%v", got, err)
	}

	// Missing skill file -> 404 Not Found.
	_, err = sdkClient.Executions.ReadSkillFile(t.Context(), &sdk.ReadSkillFileRequest{
		ID: claimed.ID, WorkerID: "worker-1", Epoch: claimed.Epoch, Skill: "go-review", Path: "missing.md",
	})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("missing file err=%v want 404", err)
	}

	// Invalid traversal path -> 400 Bad Request.
	_, err = sdkClient.Executions.ReadSkillFile(t.Context(), &sdk.ReadSkillFileRequest{
		ID: claimed.ID, WorkerID: "worker-1", Epoch: claimed.Epoch, Skill: "go-review", Path: "../etc/passwd",
	})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("traversal err=%v want 400", err)
	}
}
