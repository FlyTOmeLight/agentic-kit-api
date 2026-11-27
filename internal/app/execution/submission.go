package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// Service accepts chat and application runs through the shared durable algorithm.
type Service struct {
	store            *durable.Store
	artifact         client.ArtifactStore
	recaller         durable.MemoryRecaller
	pricing          execpkg.PricingCatalog
	projectReadCheck access.ProjectReadCheck

	collections sync.Map // agentID → artifact collection ID
}

func NewService(store *durable.Store, artifact client.ArtifactStore) *Service {
	return &Service{store: store, artifact: artifact}
}

func (s *Service) WithPricing(catalog execpkg.PricingCatalog) *Service {
	s.pricing = catalog
	return s
}

// WithMemoryRecaller wires recall memory into the accept transaction when a
// submission selects Recall. Call it during construction, before serving.
func (s *Service) WithMemoryRecaller(recaller durable.MemoryRecaller) *Service {
	s.recaller = recaller
	return s
}

func (s *Service) WithProjectReadCheck(check access.ProjectReadCheck) *Service {
	s.projectReadCheck = check
	return s
}

func (s *Service) SubmitChat(
	ctx context.Context,
	identity client.Identity,
	owner, session, key, query string,
	attachments []execpkg.Attachment,
	ttl time.Duration,
	maxAttempts int,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (*execpkg.Run, bool, error) {
	ctx, cmd, prepare, replay, err := s.prepareChat(
		ctx, identity, owner, session, key, query, attachments, ttl, maxAttempts, memorySelection, filter,
	)
	if err != nil || replay != nil {
		return replay, false, err
	}
	return s.store.Accept(ctx, cmd, prepare)
}

func (s *Service) prepareChat(
	ctx context.Context,
	identity client.Identity,
	owner, session, key, query string,
	attachments []execpkg.Attachment,
	ttl time.Duration,
	maxAttempts int,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (context.Context, execpkg.SubmitCommand, durable.PrepareSubmission, *execpkg.Run, error) {
	var conversation table.Session
	needsArtifact := hasArtifactAttachments(attachments)
	projectAuthorized := false
	if needsArtifact || memorySelection == memory.SelectionRecall {
		scope, verified := access.FromContext(ctx)
		if !verified || scope.TenantID == "" || owner == "" ||
			(needsArtifact && !matchesArtifactIdentity(identity, owner, scope)) {
			return ctx, execpkg.SubmitCommand{}, nil, nil, memory.ErrForbidden
		}
		var err error
		ctx, err = s.authorizeProjectRecall(ctx, owner, scope)
		if err != nil {
			return ctx, execpkg.SubmitCommand{}, nil, nil, err
		}
		projectAuthorized = true
		conversation, err = table.GetSession(s.store.DB().WithContext(ctx), owner, session)
		if err != nil {
			return ctx, execpkg.SubmitCommand{}, nil, nil, recordError(err)
		}
		if conversation.ProjectID != scope.ProjectID {
			return ctx, execpkg.SubmitCommand{}, nil, nil, memory.ErrForbidden
		}
	}
	if err := execpkg.ValidateSubmittedAttachments(attachments); err != nil {
		return ctx, execpkg.SubmitCommand{}, nil, nil, err
	}
	hash := chatRequestHash(session, query, memorySelection, filter, attachments...)
	if run, err := s.replay(ctx, owner, key, hash); err != nil || run != nil {
		return ctx, execpkg.SubmitCommand{}, nil, run, err
	}
	db := s.store.DB().WithContext(ctx)
	if conversation.ID == "" {
		var err error
		conversation, err = table.GetSession(db, owner, session)
		if err != nil {
			return ctx, execpkg.SubmitCommand{}, nil, nil, recordError(err)
		}
	}
	attachments, err := s.resolveAttachments(ctx, identity, attachments)
	if err != nil {
		return ctx, execpkg.SubmitCommand{}, nil, nil, err
	}
	agentRow, err := table.GetAgent(db, owner, conversation.AgentID)
	if err != nil {
		return ctx, execpkg.SubmitCommand{}, nil, nil, recordError(err)
	}
	if agentRow.PublishedVersion == 0 {
		return ctx, execpkg.SubmitCommand{}, nil, nil, agent.ErrConflict
	}
	config, err := s.publishedConfig(ctx, identity, agentRow.ID, agentRow.PublishedVersion)
	if err != nil {
		return ctx, execpkg.SubmitCommand{}, nil, nil, err
	}
	cmd := execpkg.SubmitCommand{
		Owner:        owner,
		RequestKey:   key,
		RequestHash:  hash,
		TTL:          ttl,
		MaxAttempts:  maxAttempts,
		Kind:         execpkg.SubmitChat,
		Attachments:  attachments,
		SessionID:    session,
		Query:        query,
		Memory:       normalizeMemorySelection(memorySelection),
		MemoryFilter: filter,
	}
	cmd.PricingSnapshot, err = s.pricing.Freeze(config.Model)
	if err != nil {
		return ctx, execpkg.SubmitCommand{}, nil, nil, err
	}
	if len(config.Skills) > 0 {
		ctx, err = s.authorizeSkillAccess(ctx, identity, owner, projectAuthorized)
		if err != nil {
			return ctx, execpkg.SubmitCommand{}, nil, nil, err
		}
		if scope, ok := access.FromContext(ctx); ok && conversation.ProjectID != scope.ProjectID {
			return ctx, execpkg.SubmitCommand{}, nil, nil, memory.ErrForbidden
		}
		projectAuthorized = true
		cmd.Skills, err = s.resolveSkills(ctx, identity, config.Skills)
		if err != nil {
			return ctx, execpkg.SubmitCommand{}, nil, nil, err
		}
	}
	remainingSkillBudget := maxTotalRunSkills - len(cmd.Skills)
	cmd.SubAgents, err = s.freezeSubAgents(
		ctx, identity, owner, agentRow, config.SubAgents, projectAuthorized, remainingSkillBudget,
	)
	if err != nil {
		return ctx, execpkg.SubmitCommand{}, nil, nil, err
	}
	return ctx, cmd, durable.PrepareChatSubmission(
		config,
		agentRow.ID,
		agentRow.PublishedVersion,
		s.recaller,
	), nil, nil
}

func (s *Service) SubmitApplication(
	ctx context.Context,
	identity client.Identity,
	owner, key string,
	spec execpkg.ExecutionSpec,
	ttl time.Duration,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (*execpkg.Run, bool, error) {
	scope, verified := access.FromContext(ctx)
	needsArtifact := hasArtifactAttachments(spec.Attachments)
	if verified && (scope.TenantID == "" || spec.Scope == nil ||
		spec.Scope.TenantID != scope.TenantID || spec.Scope.ProjectID != scope.ProjectID) {
		if needsArtifact {
			return nil, false, memory.ErrForbidden
		}
		return nil, false, agent.ErrInvalid
	}
	projectAuthorized := false
	if needsArtifact || memorySelection == memory.SelectionRecall {
		if !verified || spec.Scope == nil || owner == "" || spec.Scope.ActorID != owner ||
			(needsArtifact && !matchesArtifactIdentity(identity, owner, scope)) {
			return nil, false, memory.ErrForbidden
		}
		var err error
		ctx, err = s.authorizeProjectRecall(ctx, owner, scope)
		if err != nil {
			return nil, false, err
		}
		projectAuthorized = true
	}
	if err := execpkg.ValidateSubmittedAttachments(spec.Attachments); err != nil {
		return nil, false, err
	}
	hash := applicationRequestHash(spec, ttl, memorySelection, filter)
	if run, err := s.replay(ctx, owner, key, hash); err != nil || run != nil {
		return run, false, err
	}
	attachments, err := s.resolveAttachments(ctx, identity, spec.Attachments)
	if err != nil {
		return nil, false, err
	}
	spec.Attachments = attachments
	db := s.store.DB().WithContext(ctx)
	agentRow, err := table.GetAgent(db, owner, spec.AgentID)
	if err != nil {
		return nil, false, recordError(err)
	}
	config, err := s.publishedConfig(ctx, identity, spec.AgentID, spec.AgentVersion)
	if err != nil {
		return nil, false, err
	}
	cmd := execpkg.SubmitCommand{
		Owner:         owner,
		RequestKey:    key,
		RequestHash:   hash,
		TTL:           ttl,
		MaxAttempts:   5,
		Kind:          execpkg.SubmitApplication,
		ExecutionSpec: spec,
		Attachments:   attachments,
		GoalID:        spec.GoalID,
		Query:         spec.Query,
		Memory:        normalizeMemorySelection(memorySelection),
		MemoryFilter:  filter,
	}
	cmd.PricingSnapshot, err = s.pricing.Freeze(config.Model)
	if err != nil {
		return nil, false, err
	}
	if len(config.Skills) > 0 {
		if spec.Scope == nil || spec.Scope.ActorID != owner {
			return nil, false, memory.ErrForbidden
		}
		ctx, err = s.authorizeSkillAccess(ctx, identity, owner, projectAuthorized)
		if err != nil {
			return nil, false, err
		}
		projectAuthorized = true
		cmd.Skills, err = s.resolveSkills(ctx, identity, config.Skills)
		if err != nil {
			return nil, false, err
		}
	}
	remainingSkillBudget := maxTotalRunSkills - len(cmd.Skills)
	cmd.SubAgents, err = s.freezeSubAgents(
		ctx, identity, owner, agentRow, config.SubAgents, projectAuthorized, remainingSkillBudget,
	)
	if err != nil {
		return nil, false, err
	}
	return s.store.Accept(
		ctx,
		cmd,
		durable.PrepareApplicationSubmission(config, agentRow.ID, spec.AgentVersion, s.recaller),
	)
}

func (s *Service) freezeSubAgents(
	ctx context.Context,
	identity client.Identity,
	owner string,
	parent table.Agent,
	subAgents []agent.SubAgentSpec,
	projectAuthorized bool,
	remainingSkillBudget int,
) ([]execpkg.SubAgentSnapshot, error) {
	var snapshots []execpkg.SubAgentSnapshot
	db := s.store.DB().WithContext(ctx)
	for _, sub := range subAgents {
		row, err := table.GetAgent(db.Where("tenant_id = ?", parent.TenantID), parent.OwnerID, sub.AgentID)
		if err != nil {
			return nil, recordError(err)
		}
		if row.ID == parent.ID {
			return nil, agent.ErrInvalid
		}
		version := sub.Version
		if version == 0 {
			if row.PublishedVersion <= 0 {
				return nil, agent.ErrConflict
			}
			version = row.PublishedVersion
		}
		config, err := s.publishedConfig(ctx, identity, row.ID, version)
		if err != nil {
			return nil, err
		}
		if err = agent.ValidateAgentConfig(config); err != nil {
			return nil, agent.ErrInvalid
		}
		pricing, err := s.pricing.Freeze(config.Model)
		if err != nil {
			return nil, err
		}
		var skills []execpkg.SkillSnapshot
		if len(config.Skills) > 0 {
			if len(config.Skills) > remainingSkillBudget {
				return nil, agent.ErrInvalid
			}
			ctx, err = s.authorizeSkillAccess(ctx, identity, owner, projectAuthorized)
			if err != nil {
				return nil, err
			}
			projectAuthorized = true
			skills, err = s.resolveSkills(ctx, identity, config.Skills)
			if err != nil {
				return nil, err
			}
			remainingSkillBudget -= len(skills)
		}
		spec := execpkg.CompileRunSpec(config, row.ID, version, "", nil, nil)
		snapshots = append(snapshots, execpkg.SubAgentSnapshot{
			Name:            sub.Name,
			Description:     sub.Description,
			AgentID:         spec.AgentID,
			ConfigVersion:   spec.ConfigVersion,
			PricingFrozen:   spec.PricingFrozen,
			PricingSnapshot: pricing,
			Toolsets:        spec.Toolsets,
			SystemPrompt:    spec.SystemPrompt,
			Model:           spec.Model,
			Tools:           spec.Tools,
			MCPServers:      spec.MCPServers,
			Skills:          skills,
			MaxTurns:        spec.MaxTurns,
		})
	}
	return snapshots, nil
}

func (s *Service) authorizeProjectRecall(
	ctx context.Context,
	owner string,
	scope access.Scope,
) (context.Context, error) {
	if scope.ProjectID == "" {
		return ctx, nil
	}
	if owner == "" || s.projectReadCheck == nil {
		return nil, memory.ErrForbidden
	}
	allowed, err := s.projectReadCheck(ctx, owner, scope)
	if err != nil {
		return nil, fmt.Errorf("%w: Forge project read check: %w", memory.ErrUnavailable, err)
	}
	if !allowed {
		return nil, memory.ErrForbidden
	}
	return access.WithProjectRead(ctx, owner, scope), nil
}

func normalizeMemorySelection(selection memory.Selection) memory.Selection {
	if selection == "" {
		return memory.SelectionOff
	}
	return selection
}

func emptyRecallFilter(filter memory.RecallFilter) bool {
	return len(filter.Terms) == 0 && len(filter.Tags) == 0 &&
		filter.MaxEntries == 0 && filter.MaxBytes == 0
}

func chatRequestHash(
	session, query string,
	selection memory.Selection,
	filter memory.RecallFilter,
	attachments ...execpkg.Attachment,
) string {
	selection = normalizeMemorySelection(selection)
	if selection == memory.SelectionOff && emptyRecallFilter(filter) {
		input, _ := json.Marshal(struct {
			Session, Query string
			Attachments    []execpkg.Attachment `json:"Attachments,omitempty"`
		}{Session: session, Query: query, Attachments: attachments})
		return durable.Hash(input)
	}
	input, _ := json.Marshal(struct {
		Session     string
		Query       string
		Memory      memory.Selection
		Filter      memory.RecallFilter
		Attachments []execpkg.Attachment `json:"Attachments,omitempty"`
	}{Session: session, Query: query, Memory: selection, Filter: filter, Attachments: attachments})
	return durable.Hash(input)
}

func applicationRequestHash(
	spec execpkg.ExecutionSpec,
	ttl time.Duration,
	selection memory.Selection,
	filter memory.RecallFilter,
) string {
	selection = normalizeMemorySelection(selection)
	if selection == memory.SelectionOff && emptyRecallFilter(filter) {
		input, _ := json.Marshal(struct {
			Spec execpkg.ExecutionSpec
			TTL  time.Duration
		}{Spec: spec, TTL: ttl})
		return durable.Hash(input)
	}
	input, _ := json.Marshal(struct {
		Spec   execpkg.ExecutionSpec
		TTL    time.Duration
		Memory memory.Selection
		Filter memory.RecallFilter
	}{Spec: spec, TTL: ttl, Memory: selection, Filter: filter})
	return durable.Hash(input)
}

// replay returns the accepted run for an idempotent retry without touching
// the artifact store; the authoritative in-transaction recheck still runs.
func (s *Service) replay(ctx context.Context, owner, key, hash string) (*execpkg.Run, error) {
	var row table.Run
	err := table.TenantQuery(ctx, s.store.DB()).
		Where("owner_id = ? AND request_key = ?", owner, key).
		Take(&row).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.RequestHash != hash {
		return nil, durable.ErrConflict
	}
	return durable.RunFromRow(row), nil
}

// publishedConfig resolves the immutable version body through the local
// pointer and the artifact store.
func (s *Service) publishedConfig(
	ctx context.Context,
	identity client.Identity,
	agentID string,
	number int64,
) (agent.AgentConfig, error) {
	version, err := table.GetVersion(s.store.DB().WithContext(ctx), agentID, number)
	if err != nil {
		return agent.AgentConfig{}, recordError(err)
	}
	artifactID, err := s.collection(ctx, identity, agentID)
	if err != nil {
		return agent.AgentConfig{}, err
	}
	payload, err := s.artifact.GetPayload(ctx, identity, artifactID, version.ArtifactVersionID)
	if err != nil {
		return agent.AgentConfig{}, err
	}
	var config agent.AgentConfig
	if err = json.Unmarshal(payload, &config); err != nil {
		return agent.AgentConfig{}, err
	}
	return config, nil
}

func (s *Service) collection(ctx context.Context, identity client.Identity, agentID string) (string, error) {
	if cached, ok := s.collections.Load(agentID); ok {
		return cached.(string), nil
	}
	id, err := s.artifact.EnsureCollection(ctx, identity, agentID)
	if err != nil {
		return "", err
	}
	cached, _ := s.collections.LoadOrStore(agentID, id)
	return cached.(string), nil
}

func recordError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.ErrNotFound
	}
	return err
}

func (s *Service) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	return s.store.CheckExecutionLease(ctx, owner, id, epoch)
}

func (s *Service) AuthorizeExecution(
	ctx context.Context,
	owner, id, workerID string,
	epoch int64,
) (*execpkg.RunAuthorization, error) {
	return s.store.AuthorizeExecution(ctx, owner, id, workerID, epoch)
}
