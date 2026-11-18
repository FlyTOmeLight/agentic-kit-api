package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// maxPayloadBytes matches the artifact service default; oversized configs are
// rejected before any remote call.
const maxPayloadBytes = 262144

// Service owns the Agent asset lifecycle. Drafts live in the local database;
// published bodies live as immutable artifact versions.
type Service interface {
	CreateAgent(ctx context.Context, owner, name string, config durable.AgentConfig) (*durable.Agent, error)
	Agent(ctx context.Context, owner, id string) (*durable.Agent, error)
	Agents(ctx context.Context, owner, after string, limit int) ([]durable.Agent, error)
	UpdateAgent(
		ctx context.Context,
		owner, id, name string,
		revision int64,
		config durable.AgentConfig,
	) (*durable.Agent, error)
	PublishAgent(
		ctx context.Context,
		identity client.Identity,
		owner, id string,
		revision int64,
	) (*durable.AgentVersion, error)
	AgentVersion(
		ctx context.Context,
		identity client.Identity,
		owner, id string,
		number int64,
	) (*durable.AgentVersion, error)
}

type service struct {
	db          *gorm.DB
	artifact    client.ArtifactStore
	collections sync.Map // agentID → artifact collection ID
}

func NewService(db *gorm.DB, artifact client.ArtifactStore) Service {
	return &service{db: db, artifact: artifact}
}

func recordError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return agent.ErrConflict
	}
	return err
}

func agentFromRow(r table.Agent) *durable.Agent {
	return &durable.Agent{
		ID:               r.ID,
		Name:             r.Name,
		Draft:            r.Draft,
		Revision:         r.Revision,
		PublishedVersion: r.PublishedVersion,
		CreatedAt:        r.CreatedAt.UTC(),
		UpdatedAt:        r.UpdatedAt.UTC(),
	}
}

func validateConfig(c agent.AgentConfig) error {
	if err := agent.ValidateAgentConfig(c); err != nil {
		return agent.ErrInvalid
	}
	return nil
}

func (s *service) CreateAgent(
	ctx context.Context,
	owner, name string,
	config durable.AgentConfig,
) (*durable.Agent, error) {
	name = strings.TrimSpace(name)
	if owner == "" || name == "" || len(name) > 200 {
		return nil, agent.ErrInvalid
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	row := table.Agent{ID: durable.NewID(), OwnerID: owner, Name: name, Draft: config, Revision: 1}
	if scope, ok := access.FromContext(ctx); ok {
		if scope.TenantID == "" {
			return nil, agent.ErrInvalid
		}
		row.TenantID, row.ProjectID = scope.TenantID, scope.ProjectID
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return agentFromRow(row), nil
}

func (s *service) Agent(ctx context.Context, owner, id string) (*durable.Agent, error) {
	row, err := table.GetAgent(s.db.WithContext(ctx), owner, id)
	if err != nil {
		return nil, recordError(err)
	}
	return agentFromRow(row), nil
}

func (s *service) Agents(ctx context.Context, owner, after string, limit int) ([]durable.Agent, error) {
	if limit < 1 || limit > 100 {
		return nil, agent.ErrInvalid
	}
	rows, err := table.ListAgents(s.db.WithContext(ctx), owner, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]durable.Agent, len(rows))
	for i, row := range rows {
		out[i] = *agentFromRow(row)
	}
	return out, nil
}

// UpdateAgent replaces the draft only if the caller edited the current revision.
func (s *service) UpdateAgent(
	ctx context.Context,
	owner, id, name string,
	revision int64,
	config durable.AgentConfig,
) (*durable.Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || revision < 1 {
		return nil, agent.ErrInvalid
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	var row table.Agent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := table.LockAgent(tx, owner, id)
		if err != nil {
			return recordError(err)
		}
		if locked.Revision != revision {
			return agent.ErrConflict
		}
		row = locked
		return table.UpdateDraft(tx, &row, name, revision, config)
	})
	if err != nil {
		return nil, err
	}
	return agentFromRow(row), nil
}

// PublishAgent writes the current draft as an immutable artifact version, then
// commits the local pointer. The artifact request keys derive from the
// persisted agentID and revision, so a retry after an unknown outcome replays
// the same remote version.
func (s *service) PublishAgent(
	ctx context.Context,
	identity client.Identity,
	owner, id string,
	revision int64,
) (*durable.AgentVersion, error) {
	var draft agent.AgentConfig
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := table.LockAgent(tx, owner, id)
		if err != nil {
			return recordError(err)
		}
		if row.Revision != revision {
			return agent.ErrConflict
		}
		if row.PublishedRevision == revision {
			version, err := table.GetVersion(tx, id, row.PublishedVersion)
			if err != nil {
				return recordError(err)
			}
			draft = row.Draft
			return &publishedExists{number: version.Number, createdAt: version.CreatedAt}
		}
		draft = row.Draft
		return nil
	})
	var exists *publishedExists
	if errors.As(err, &exists) {
		return &durable.AgentVersion{
			AgentID:   id,
			Number:    exists.number,
			Config:    draft,
			CreatedAt: exists.createdAt.UTC(),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if err = validateConfig(draft); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(draft)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxPayloadBytes {
		return nil, agent.ErrInvalid
	}
	artifactID, err := s.collection(ctx, identity, id)
	if err != nil {
		return nil, err
	}
	ref, err := s.artifact.PublishVersion(ctx, identity, artifactID, publishKey(id, revision), payload)
	if err != nil {
		return nil, err
	}
	version := table.AgentVersion{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := table.LockAgent(tx, owner, id)
		if err != nil {
			return recordError(err)
		}
		if row.PublishedRevision == revision {
			existing, err := table.GetVersion(tx, id, row.PublishedVersion)
			if err != nil {
				return recordError(err)
			}
			version = existing
			return nil
		}
		if row.Revision != revision {
			// The remote version just published becomes an orphan; the caller
			// retries the new revision.
			return agent.ErrConflict
		}
		version = table.AgentVersion{
			AgentID:            id,
			Number:             row.PublishedVersion + 1,
			ArtifactVersionID:  ref.ID,
			ArtifactVersionURI: ref.URI,
		}
		if err = table.InsertVersion(tx, &version); err != nil {
			return err
		}
		return table.AdvancePublished(tx, &row, revision, version.Number)
	})
	if err != nil {
		return nil, err
	}
	return &durable.AgentVersion{
		AgentID:   id,
		Number:    version.Number,
		Config:    draft,
		CreatedAt: version.CreatedAt.UTC(),
	}, nil
}

func (s *service) AgentVersion(
	ctx context.Context,
	identity client.Identity,
	owner, id string,
	number int64,
) (*durable.AgentVersion, error) {
	row, err := table.GetAgent(s.db.WithContext(ctx), owner, id)
	if err != nil {
		return nil, recordError(err)
	}
	version, err := table.GetVersion(s.db.WithContext(ctx), id, number)
	if err != nil {
		return nil, recordError(err)
	}
	artifactID, err := s.collection(ctx, identity, id)
	if err != nil {
		return nil, err
	}
	payload, err := s.artifact.GetPayload(ctx, identity, artifactID, version.ArtifactVersionID)
	if err != nil {
		return nil, err
	}
	var config agent.AgentConfig
	if err = json.Unmarshal(payload, &config); err != nil {
		return nil, fmt.Errorf("artifact version payload is not an agent config: %w", err)
	}
	return &durable.AgentVersion{
		AgentID:   row.ID,
		Number:    version.Number,
		Config:    config,
		CreatedAt: version.CreatedAt.UTC(),
	}, nil
}

// publishedExists aborts the lock transaction when the revision is already
// published; the handler returns the stored version without a remote call.
type publishedExists struct {
	number    int64
	createdAt time.Time
}

func (e *publishedExists) Error() string { return "agent revision already published" }

// collection resolves the artifact collection for an agent, caching the
// idempotent EnsureCollection result per process.
func (s *service) collection(ctx context.Context, identity client.Identity, agentID string) (string, error) {
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

func publishKey(agentID string, revision int64) string {
	sum := sha256.Sum256([]byte(agentID + ":" + strconv.FormatInt(revision, 10)))
	return hex.EncodeToString(sum[:])
}
