package table

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

// These helpers share the Agent persistence rules between the asset service and
// run submission. They never open a transaction and never check ownership
// beyond the owner predicates in the query itself.

func GetAgent(db *gorm.DB, owner, id string) (Agent, error) {
	var row Agent
	err := TenantQuery(db.Statement.Context, db).Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error
	return row, err
}

func ListAgents(db *gorm.DB, owner, after string, limit int) ([]Agent, error) {
	var rows []Agent
	err := TenantQuery(db.Statement.Context, db).
		Where("owner_id = ? AND id > ?", owner, after).
		Order("id").
		Limit(limit).
		Find(&rows).
		Error
	return rows, err
}

func LockAgent(db *gorm.DB, owner, id string) (Agent, error) {
	var row Agent
	err := TenantQuery(db.Statement.Context, db).Where("owner_id = ? AND id = ?", owner, id).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&row).
		Error
	return row, err
}

// UpdateDraft replaces the draft and bumps the revision; the caller must hold
// the agent row lock and have already compared the revision.
func UpdateDraft(db *gorm.DB, row *Agent, name string, revision int64, draft agent.AgentConfig) error {
	name = strings.TrimSpace(name)
	return db.Model(row).
		Clauses(clause.Returning{}).
		Select("name", "draft", "revision").
		Updates(&Agent{Name: name, Draft: draft, Revision: revision + 1}).
		Error
}

func InsertVersion(db *gorm.DB, version *AgentVersion) error {
	return db.Create(version).Error
}

// AdvancePublished points the agent at the version row inserted with it; the
// caller must hold the agent row lock.
func AdvancePublished(db *gorm.DB, row *Agent, revision, number int64) error {
	return db.Model(row).
		Updates(map[string]any{"published_revision": revision, "published_version": number}).
		Error
}

func GetVersion(db *gorm.DB, agentID string, number int64) (AgentVersion, error) {
	var row AgentVersion
	agents := TenantQuery(db.Statement.Context, db.Session(&gorm.Session{NewDB: true})).
		Model(&Agent{}).Select("id").Where("id = ?", agentID)
	err := db.Where("agent_id IN (?) AND number = ?", agents, number).Take(&row).Error
	return row, err
}

func GetSession(db *gorm.DB, owner, id string) (Session, error) {
	var row Session
	err := TenantQuery(db.Statement.Context, db).Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error
	return row, err
}
