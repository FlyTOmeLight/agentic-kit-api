package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

type skillTestArtifactStore struct {
	mu           sync.Mutex
	configs      map[string]agent.AgentConfig
	skills       map[string]client.ResolvedSkill
	fileContents map[string][]byte
	resolveCalls atomic.Int32
	readCalls    atomic.Int32
	resolveErr   error
	lastShared   bool
	lastIdentity client.Identity
}

func (*skillTestArtifactStore) EnsureCollection(context.Context, client.Identity, string) (string, error) {
	return "col", nil
}

func (*skillTestArtifactStore) PublishVersion(
	context.Context,
	client.Identity,
	string,
	string,
	[]byte,
) (client.VersionRef, error) {
	return client.VersionRef{ID: "v1", URI: "artifact://t/v1"}, nil
}

func (s *skillTestArtifactStore) GetPayload(
	_ context.Context,
	_ client.Identity,
	artifactID, versionID string,
) ([]byte, error) {
	cfg, ok := s.configs[artifactID+":"+versionID]
	if !ok {
		return nil, agent.ErrNotFound
	}
	return json.Marshal(cfg)
}

func (*skillTestArtifactStore) ResolveAttachment(
	context.Context,
	client.Identity,
	string,
) (client.ResolvedAttachment, error) {
	return client.ResolvedAttachment{}, agent.ErrUnavailable
}

func (s *skillTestArtifactStore) ResolveSkill(
	_ context.Context,
	id client.Identity,
	uri string,
	shared bool,
) (client.ResolvedSkill, error) {
	s.resolveCalls.Add(1)
	s.mu.Lock()
	s.lastIdentity = id
	s.lastShared = shared
	resolveErr := s.resolveErr
	skill, ok := s.skills[uri]
	s.mu.Unlock()
	if resolveErr != nil {
		return client.ResolvedSkill{}, resolveErr
	}
	if !ok {
		return client.ResolvedSkill{}, agent.ErrNotFound
	}
	return skill, nil
}

func (s *skillTestArtifactStore) ReadSkillFile(
	_ context.Context,
	id client.Identity,
	shared bool,
	fileID string,
	_ int64,
) ([]byte, error) {
	s.readCalls.Add(1)
	s.mu.Lock()
	s.lastIdentity = id
	s.lastShared = shared
	data, ok := s.fileContents[fileID]
	s.mu.Unlock()
	if !ok {
		return nil, agent.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (*skillTestArtifactStore) Close() error { return nil }

func TestSkillFreezeReplayLeaseReadAndSubAgentInheritance(t *testing.T) {
	store := tenantTestStore(t)
	const tenant, project, owner = "tenant-skill", "proj-skill", "owner-skill"
	scope := access.Scope{TenantID: tenant, ProjectID: project}
	ctx := access.WithScope(t.Context(), scope)
	projCopy := project
	identity := client.Identity{TenantID: tenant, ProjectID: &projCopy, PropagatedUserID: owner}

	const parentSkillAlias = "artifact://tenant-skill/projects/proj-skill/artifacts/agent-skill/s1/aliases/latest"
	const parentSkillFixed = "artifact://tenant-skill/projects/proj-skill/artifacts/agent-skill/s1/versions/v1"
	const subSkillAlias = "artifact://tenant-skill/projects/proj-skill/artifacts/agent-skill/s2/aliases/latest"
	const subSkillFixed = "artifact://tenant-skill/projects/proj-skill/artifacts/agent-skill/s2/versions/v3"

	subID := durable.NewID()
	parentID := durable.NewID()

	parentCfg := agent.AgentConfig{
		SystemPrompt: "parent",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     5,
		SelfDelegate: true,
		Skills: []agent.SkillRef{{
			Name:        "go-review",
			ArtifactURI: parentSkillAlias,
			Shared:      true,
		}},
		SubAgents: []agent.SubAgentSpec{{
			Name: "sql", Description: "SQL helper", AgentID: subID, Version: 1,
		}},
	}
	subCfg := agent.AgentConfig{
		SystemPrompt: "sub",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     3,
		Skills: []agent.SkillRef{{
			Name:        "sql-check",
			ArtifactURI: subSkillAlias,
			Description: "Override SQL description",
		}},
	}

	art := &skillTestArtifactStore{
		configs: map[string]agent.AgentConfig{
			"col:ver-parent": parentCfg,
			"col:ver-sub":    subCfg,
		},
		skills: map[string]client.ResolvedSkill{
			parentSkillAlias: {
				VersionURI:  parentSkillFixed,
				Description: "Review Go code carefully",
				Files: []client.ResolvedSkillFile{
					{
						Path:      "SKILL.md",
						FileID:    "f-parent-md",
						FileURI:   parentSkillFixed + "/files/SKILL.md",
						MediaType: "text/markdown",
						SizeBytes: 24,
					},
					{
						Path:      "references/rules.md",
						FileID:    "f-parent-ref",
						FileURI:   parentSkillFixed + "/files/references/rules.md",
						MediaType: "text/markdown",
						SizeBytes: 18,
					},
				},
			},
			subSkillAlias: {
				VersionURI: subSkillFixed,
				Files: []client.ResolvedSkillFile{
					{
						Path:      "SKILL.md",
						FileID:    "f-sub-md",
						FileURI:   subSkillFixed + "/files/SKILL.md",
						MediaType: "text/markdown",
						SizeBytes: 20,
					},
				},
			},
		},
		fileContents: map[string][]byte{
			"f-parent-md":  []byte("# Go Review\nCheck errs.\n"),
			"f-parent-ref": []byte("- No naked panic.\n"),
			"f-sub-md":     []byte("# SQL Check\nUse idx.\n"),
		},
	}

	db := store.DB()
	for _, item := range []struct {
		id    string
		verID string
		cfg   agent.AgentConfig
	}{
		{id: parentID, verID: "ver-parent", cfg: parentCfg},
		{id: subID, verID: "ver-sub", cfg: subCfg},
	} {
		if err := db.Create(&table.Agent{
			ID: item.id, TenantID: tenant, ProjectID: project, OwnerID: owner, Name: item.id,
			Draft: item.cfg, Revision: 1, PublishedRevision: 1, PublishedVersion: 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&table.AgentVersion{
			AgentID: item.id, Number: 1,
			ArtifactVersionID: item.verID, ArtifactVersionURI: "artifact://tenant/agent/" + item.verID,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sess, err := store.CreateSession(ctx, owner, parentID, "skills")
	if err != nil {
		t.Fatal(err)
	}

	// Denied project read must block before ResolveSkill.
	deniedSvc := NewService(
		store,
		art,
	).WithProjectReadCheck(func(context.Context, string, access.Scope) (bool, error) {
		return false, nil
	})
	if _, _, err := deniedSvc.SubmitChat(
		ctx,
		identity,
		owner,
		sess.ID,
		"req-denied",
		"hi",
		nil,
		time.Hour,
		3,
		memory.SelectionOff,
		memory.RecallFilter{},
	); !errors.Is(
		err,
		memory.ErrForbidden,
	) {
		t.Fatalf("expected ErrForbidden without project read, got %v", err)
	}
	if art.resolveCalls.Load() != 0 {
		t.Fatalf("ResolveSkill called despite denied project read: %d", art.resolveCalls.Load())
	}

	svc := NewService(store, art).WithProjectReadCheck(func(context.Context, string, access.Scope) (bool, error) {
		return true, nil
	})
	run, created, err := svc.SubmitChat(
		ctx,
		identity,
		owner,
		sess.ID,
		"req-1",
		"review code",
		nil,
		time.Hour,
		3,
		memory.SelectionOff,
		memory.RecallFilter{},
	)
	if err != nil || !created {
		t.Fatalf("SubmitChat err=%v created=%t", err, created)
	}
	if len(run.Spec.Skills) != 1 || run.Spec.Skills[0].VersionURI != parentSkillFixed ||
		run.Spec.Skills[0].Description != "Review Go code carefully" || !run.Spec.Skills[0].Shared ||
		len(run.Spec.Skills[0].Files) != 2 {
		t.Fatalf("frozen parent skills = %+v", run.Spec.Skills)
	}
	if len(run.Spec.SubAgents) != 1 || len(run.Spec.SubAgents[0].Skills) != 1 ||
		run.Spec.SubAgents[0].Skills[0].VersionURI != subSkillFixed ||
		run.Spec.SubAgents[0].Skills[0].Description != "Override SQL description" {
		t.Fatalf("frozen subagent skills = %+v", run.Spec.SubAgents)
	}
	// Context snapshot contains the skill directory summary.
	decoded, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format: run.ContextSnapshot.Format, PolicyVersion: run.ContextSnapshot.PolicyVersion,
		Digest: run.ContextSnapshot.Digest, Payload: run.ContextSnapshot.Payload,
	})
	if err != nil || len(decoded.Materials.Skills) != 1 || decoded.Materials.Skills[0].Name != "go-review" {
		t.Fatalf("context snapshot skills = %+v err=%v", decoded.Materials.Skills, err)
	}

	// Idempotent replay succeeds even if Artifact is unavailable, without re-calling ResolveSkill.
	callsBefore := art.resolveCalls.Load()
	art.resolveErr = agent.ErrUnavailable
	replayed, created, err := svc.SubmitChat(
		ctx,
		identity,
		owner,
		sess.ID,
		"req-1",
		"review code",
		nil,
		time.Hour,
		3,
		memory.SelectionOff,
		memory.RecallFilter{},
	)
	if err != nil || created || replayed.ID != run.ID || art.resolveCalls.Load() != callsBefore {
		t.Fatalf("replay err=%v created=%t calls=%d->%d", err, created, callsBefore, art.resolveCalls.Load())
	}
	art.resolveErr = nil

	// Unclaimed run cannot read skills via worker endpoint.
	if _, err = svc.ReadSkillFile(
		t.Context(),
		run.ID,
		"worker-1",
		1,
		"go-review",
		"",
	); !errors.Is(
		err,
		durable.ErrLeaseLost,
	) {
		t.Fatalf("unclaimed ReadSkillFile err=%v want ErrLeaseLost", err)
	}

	claimed, err := store.Claim(t.Context(), "worker-1", time.Minute)
	if err != nil || claimed.ID != run.ID {
		t.Fatalf("Claim err=%v run=%+v", err, claimed)
	}
	// Active lease reads default SKILL.md and helper file using authoritative run identity.
	md, err := svc.ReadSkillFile(t.Context(), claimed.ID, "worker-1", claimed.Epoch, "go-review", "")
	if err != nil || md.Path != "SKILL.md" || md.VersionURI != parentSkillFixed ||
		!strings.Contains(md.Content, "Check errs.") {
		t.Fatalf("ReadSkillFile SKILL.md = %+v err=%v", md, err)
	}
	if !art.lastShared || art.lastIdentity.TenantID != tenant || art.lastIdentity.PropagatedUserID != owner ||
		art.lastIdentity.ProjectID == nil || *art.lastIdentity.ProjectID != project {
		t.Fatalf(
			"unexpected identity passed to ReadSkillFile: id=%+v shared=%t",
			art.lastIdentity,
			art.lastShared,
		)
	}
	ref, err := svc.ReadSkillFile(
		t.Context(),
		claimed.ID,
		"worker-1",
		claimed.Epoch,
		"go-review",
		"references/rules.md",
	)
	if err != nil || !strings.Contains(ref.Content, "No naked panic.") {
		t.Fatalf("ReadSkillFile helper = %+v err=%v", ref, err)
	}
	// Wrong worker/epoch -> ErrLeaseLost; missing file -> ErrNotFound; path traversal -> ErrInvalid.
	if _, err = svc.ReadSkillFile(
		t.Context(),
		claimed.ID,
		"other-worker",
		claimed.Epoch,
		"go-review",
		"",
	); !errors.Is(
		err,
		durable.ErrLeaseLost,
	) {
		t.Fatalf("wrong worker err=%v want ErrLeaseLost", err)
	}
	if _, err = svc.ReadSkillFile(
		t.Context(),
		claimed.ID,
		"worker-1",
		claimed.Epoch,
		"go-review",
		"missing.md",
	); !errors.Is(
		err,
		durable.ErrNotFound,
	) {
		t.Fatalf("missing file err=%v want ErrNotFound", err)
	}
	if _, err = svc.ReadSkillFile(
		t.Context(),
		claimed.ID,
		"worker-1",
		claimed.Epoch,
		"go-review",
		"../secret",
	); !errors.Is(
		err,
		durable.ErrInvalid,
	) {
		t.Fatalf("traversal err=%v want ErrInvalid", err)
	}

	// Mode A child run inherits its own sub-agent skills; worker can read sub-agent skill via child lease.
	if _, err := store.BeginStep(t.Context(), claimed.Lease(), durable.StepIntent{
		ID: "turn_0_tool_0", Input: `{"subAgent":"sql","query":"check sql"}`, Policy: agent.ReadOnly,
	}); err != nil {
		t.Fatal(err)
	}
	child, err := store.SpawnChildAndAwait(t.Context(), claimed.Lease(), durable.ChildSpawnRequest{
		StepID: "turn_0_tool_0", SubAgent: "sql", Query: "check sql", TTL: time.Minute,
	}, []byte(`{}`), "waiting sql sub-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Spec.Skills) != 1 || child.Spec.Skills[0].Name != "sql-check" || child.Spec.Skills[0].Shared {
		t.Fatalf("Mode A child skills = %+v", child.Spec.Skills)
	}
	claimedChild, err := store.Claim(t.Context(), "worker-2", time.Minute)
	if err != nil || claimedChild.ID != child.ID {
		t.Fatalf("Claim child err=%v", err)
	}
	subMD, err := svc.ReadSkillFile(t.Context(), claimedChild.ID, "worker-2", claimedChild.Epoch, "sql-check", "")
	if err != nil || !strings.Contains(subMD.Content, "Use idx.") || art.lastShared {
		t.Fatalf("child ReadSkillFile = %+v shared=%t err=%v", subMD, art.lastShared, err)
	}

	// Complete Mode A child so parent can resume and spawn a Mode B (self-delegated) child inheriting parent skills.
	if err := store.CommitBoundary(t.Context(), claimedChild.Lease(), durable.Boundary{
		Kind: durable.BoundaryComplete, Output: "sql ok", State: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	resumedParent, err := store.Claim(t.Context(), "worker-1", time.Minute)
	if err != nil || resumedParent.ID != run.ID {
		t.Fatalf("re-claim parent err=%v run=%+v", err, resumedParent)
	}
	if _, err := store.BeginStep(t.Context(), resumedParent.Lease(), durable.StepIntent{
		ID: "turn_1_tool_0", Input: `{"query":"focus on error handling"}`, Policy: agent.ReadOnly,
	}); err != nil {
		t.Fatal(err)
	}
	selfChild, err := store.SpawnChildAndAwait(t.Context(), resumedParent.Lease(), durable.ChildSpawnRequest{
		StepID: "turn_1_tool_0", Query: "focus on error handling", TTL: time.Minute,
	}, []byte(`{}`), "waiting self-delegated child")
	if err != nil {
		t.Fatal(err)
	}
	if len(selfChild.Spec.Skills) != 1 || selfChild.Spec.Skills[0].Name != "go-review" ||
		!selfChild.Spec.Skills[0].Shared {
		t.Fatalf("Mode B self-delegated child skills = %+v", selfChild.Spec.Skills)
	}
}

func TestSkillSubmissionBudgetAndTotalLimit(t *testing.T) {
	store := tenantTestStore(t)
	const tenant, project, owner = "tenant-skill-limit", "proj-skill-limit", "owner-skill-limit"
	scope := access.Scope{TenantID: tenant, ProjectID: project}
	ctx := access.WithScope(t.Context(), scope)
	projCopy := project
	identity := client.Identity{TenantID: tenant, ProjectID: &projCopy, PropagatedUserID: owner}

	longDesc := strings.Repeat("detailed skill guideline ", 35)
	makeSkillRefs := func(prefix string, n int) []agent.SkillRef {
		refs := make([]agent.SkillRef, 0, n)
		for i := range n {
			name := prefix + string(rune('a'+i))
			refs = append(refs, agent.SkillRef{
				Name:        name,
				ArtifactURI: "artifact://tenant-skill-limit/projects/proj-skill-limit/artifacts/agent-skill/" + name + "/versions/v1",
				Description: longDesc,
			})
		}
		return refs
	}

	art := &skillTestArtifactStore{
		configs: map[string]agent.AgentConfig{},
		skills:  map[string]client.ResolvedSkill{},
	}
	registerSkillArtifacts := func(refs []agent.SkillRef) {
		for _, r := range refs {
			art.skills[r.ArtifactURI] = client.ResolvedSkill{
				VersionURI: r.ArtifactURI,
				Files: []client.ResolvedSkillFile{
					{
						Path:      "SKILL.md",
						FileID:    "f-" + r.Name,
						FileURI:   r.ArtifactURI + "/files/SKILL.md",
						MediaType: "text/markdown",
						SizeBytes: 12,
					},
				},
			}
		}
	}

	db := store.DB()
	createPublishedAgent := func(verID string, cfg agent.AgentConfig) string {
		id := durable.NewID()
		art.configs["col:"+verID] = cfg
		if err := db.Create(&table.Agent{
			ID: id, TenantID: tenant, ProjectID: project, OwnerID: owner, Name: id,
			Draft: cfg, Revision: 1, PublishedRevision: 1, PublishedVersion: 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&table.AgentVersion{
			AgentID:            id,
			Number:             1,
			ArtifactVersionID:  verID,
			ArtifactVersionURI: "artifact://tenant/agent/" + verID,
		}).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Case 1: Mode A sub-agent with tiny context window fails submission precheck when its skill directory exceeds budget.
	subTinyRefs := makeSkillRefs("tiny-", 4)
	registerSkillArtifacts(subTinyRefs)
	maxTok := 128
	subTinyID := createPublishedAgent("ver-sub-tiny", agent.AgentConfig{
		SystemPrompt: "sub",
		Model: agent.ModelSpec{
			Name:          "m",
			BaseURL:       "https://model.test/v1",
			ContextWindow: 512,
			MaxTokens:     &maxTok,
		},
		MaxTurns: 2,
		Skills:   subTinyRefs,
	})
	parentTightID := createPublishedAgent("ver-parent-tight", agent.AgentConfig{
		SystemPrompt: "parent",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     2,
		SubAgents:    []agent.SubAgentSpec{{Name: "tiny", Description: "tiny", AgentID: subTinyID, Version: 1}},
	})
	svc := NewService(store, art).WithProjectReadCheck(func(context.Context, string, access.Scope) (bool, error) {
		return true, nil
	})
	sess1, err := store.CreateSession(ctx, owner, parentTightID, "tight")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SubmitChat(
		ctx,
		identity,
		owner,
		sess1.ID,
		"req-tight",
		"go",
		nil,
		time.Hour,
		2,
		memory.SelectionOff,
		memory.RecallFilter{},
	); !errors.Is(
		err,
		contextbuilder.ErrBudgetExceeded,
	) {
		t.Fatalf("expected ErrBudgetExceeded for tight sub-agent skill directory, got %v", err)
	}

	// Case 2: Total parent + sub-agent skills exceeding 32 fails with agent.ErrInvalid.
	pRefs := makeSkillRefs("p-", 16)
	s1Refs := makeSkillRefs("sa-", 10)
	s2Refs := makeSkillRefs("sb-", 7)
	registerSkillArtifacts(pRefs)
	registerSkillArtifacts(s1Refs)
	registerSkillArtifacts(s2Refs)
	sub1ID := createPublishedAgent("ver-sub-1", agent.AgentConfig{
		SystemPrompt: "s1",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     2,
		Skills:       s1Refs,
	})
	sub2ID := createPublishedAgent("ver-sub-2", agent.AgentConfig{
		SystemPrompt: "s2",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     2,
		Skills:       s2Refs,
	})
	parentManyID := createPublishedAgent("ver-parent-many", agent.AgentConfig{
		SystemPrompt: "p",
		Model:        agent.ModelSpec{Name: "m", BaseURL: "https://model.test/v1"},
		MaxTurns:     2,
		Skills:       pRefs,
		SubAgents: []agent.SubAgentSpec{
			{Name: "s1", Description: "s1", AgentID: sub1ID, Version: 1},
			{Name: "s2", Description: "s2", AgentID: sub2ID, Version: 1},
		},
	})
	sess2, err := store.CreateSession(ctx, owner, parentManyID, "many")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SubmitChat(
		ctx,
		identity,
		owner,
		sess2.ID,
		"req-many",
		"go",
		nil,
		time.Hour,
		2,
		memory.SelectionOff,
		memory.RecallFilter{},
	); !errors.Is(
		err,
		agent.ErrInvalid,
	) {
		t.Fatalf("expected agent.ErrInvalid when total skills > 32, got %v", err)
	}
}
