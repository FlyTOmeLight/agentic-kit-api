package durable

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentConfig = sdk.AgentConfig
type Agent = sdk.Agent
type AgentVersion = sdk.AgentVersion

func (r agentRecord) agent() *Agent {
	return &Agent{ID: r.ID, Name: r.Name, Draft: r.Draft, Revision: r.Revision, PublishedVersion: r.PublishedVersion, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}
func (r versionRecord) version() *AgentVersion {
	return &AgentVersion{AgentID: r.AgentID, Number: r.Number, Config: r.Config, CreatedAt: r.CreatedAt.UTC()}
}
func runSpec(c AgentConfig, agent string, version int64, query string) Spec {
	return Spec{AgentID: agent, ConfigVersion: strconv.FormatInt(version, 10), Query: query, SystemPrompt: c.SystemPrompt, Model: c.Model, Tools: c.Tools, Toolsets: c.Toolsets, MaxTurns: c.MaxTurns}
}
func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.Fragment == ""
}
func ValidateAgentConfig(c AgentConfig) error {
	if strings.TrimSpace(c.Model.Name) == "" || !validEndpoint(c.Model.BaseURL) || c.MaxTurns < 1 || c.MaxTurns > 100 || len(c.Tools) > 128 {
		return ErrInvalid
	}
	seenSets := map[string]bool{}
	for _, set := range c.Toolsets {
		if set != "filesystem" || seenSets[set] {
			return ErrInvalid
		}
		seenSets[set] = true
	}
	seen := map[string]bool{}
	for _, t := range c.Tools {
		var params map[string]any
		if strings.TrimSpace(t.Name) == "" || seen[t.Name] || json.Unmarshal(t.Parameters, &params) != nil || params == nil || params["type"] != "object" {
			return ErrInvalid
		}
		seen[t.Name] = true
		if t.Policy != ReadOnly && t.Policy != Idempotent && t.Policy != Unsafe {
			return ErrInvalid
		}
		switch t.Kind {
		case "", "HTTP":
		case "AppForgeMCP", "AppForgeOpenAPI":
			if t.ResourceID == "" || t.ToolID == "" || t.Policy == Idempotent {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if t.Name == "ask_for_clarification" {
			if t.Policy != ReadOnly || (t.Kind != "" && t.Kind != "HTTP") {
				return ErrInvalid
			}
		} else if !validEndpoint(t.URL) {
			return ErrInvalid
		}
	}
	return nil
}
func recordError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}
func (s *Store) CreateAgent(ctx context.Context, owner, name string, config AgentConfig) (*Agent, error) {
	name = strings.TrimSpace(name)
	if owner == "" || name == "" || len(name) > 200 {
		return nil, ErrInvalid
	}
	if err := ValidateAgentConfig(config); err != nil {
		return nil, err
	}
	r := agentRecord{ID: NewID(), OwnerID: owner, Name: name, Draft: config, Revision: 1}
	if err := s.db.WithContext(ctx).Create(&r).Error; err != nil {
		return nil, err
	}
	return r.agent(), nil
}
func (s *Store) Agent(ctx context.Context, owner, id string) (*Agent, error) {
	var r agentRecord
	if err := s.db.WithContext(ctx).Where("owner_id = ? AND id = ?", owner, id).Take(&r).Error; err != nil {
		return nil, recordError(err)
	}
	return r.agent(), nil
}

// UpdateAgent replaces the draft only if the caller edited the current revision.
func (s *Store) UpdateAgent(ctx context.Context, owner, id, name string, revision int64, config AgentConfig) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || revision < 1 {
		return nil, ErrInvalid
	}
	if err := ValidateAgentConfig(config); err != nil {
		return nil, err
	}
	var r agentRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("owner_id = ? AND id = ?", owner, id).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&r).Error; err != nil {
			return recordError(err)
		}
		if r.Revision != revision {
			return ErrConflict
		}
		data, err := json.Marshal(config)
		if err != nil {
			return err
		}
		return tx.Model(&r).Clauses(clause.Returning{}).Updates(map[string]any{"name": name, "draft": string(data), "revision": revision + 1}).Error
	})
	if err != nil {
		return nil, err
	}
	return r.agent(), nil
}
func (s *Store) PublishAgent(ctx context.Context, owner, id string, revision int64) (*AgentVersion, error) {
	var v versionRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var a agentRecord
		if err := tx.Where("owner_id = ? AND id = ?", owner, id).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&a).Error; err != nil {
			return recordError(err)
		}
		if a.Revision != revision {
			return ErrConflict
		}
		if a.PublishedRevision == revision {
			return tx.Where("agent_id = ? AND number = ?", id, a.PublishedVersion).Take(&v).Error
		}
		if err := ValidateAgentConfig(a.Draft); err != nil {
			return err
		}
		v = versionRecord{AgentID: id, Number: a.PublishedVersion + 1, Config: a.Draft}
		if err := tx.Create(&v).Error; err != nil {
			return err
		}
		return tx.Model(&a).Updates(map[string]any{"published_revision": revision, "published_version": v.Number}).Error
	})
	if err != nil {
		return nil, err
	}
	return v.version(), nil
}
func (s *Store) AgentVersion(ctx context.Context, owner, id string, number int64) (*AgentVersion, error) {
	if _, err := s.Agent(ctx, owner, id); err != nil {
		return nil, err
	}
	var r versionRecord
	if err := s.db.WithContext(ctx).Where("agent_id = ? AND number = ?", id, number).Take(&r).Error; err != nil {
		return nil, recordError(err)
	}
	return r.version(), nil
}

// Lists use stable ID cursors and bounded pages; IDs are opaque, not chronological.
func (s *Store) Agents(ctx context.Context, owner, after string, limit int) ([]Agent, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	var records []agentRecord
	err := s.db.WithContext(ctx).Where("owner_id = ? AND id > ?", owner, after).Order("id").Limit(limit).Find(&records).Error
	out := make([]Agent, len(records))
	for i, r := range records {
		out[i] = *r.agent()
	}
	return out, err
}
