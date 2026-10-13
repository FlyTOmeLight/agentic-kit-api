package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// PublishCodingRelease freezes a project candidate under the business runtime
// owner. A stable ID makes retries reuse the same immutable version.
func (s *Store) PublishCodingRelease(
	ctx context.Context,
	runtimeOwner, projectOwner, agent string,
	version int64,
	gateway string,
) (*sdk.AgentVersion, error) {
	if runtimeOwner == "" || projectOwner == "" || version < 1 || !validEndpoint(gateway) {
		return nil, ErrInvalid
	}
	id := Hash([]byte(fmt.Sprintf("coding-release:%s:%s:%s:%d", runtimeOwner, projectOwner, agent, version)))[:32]
	var result versionRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
			"coding-release:"+id,
		).Error; err != nil {
			return err
		}
		var source agentRecord
		if err := tx.Where("owner_id = ? AND id = ?", projectOwner, agent).Take(&source).Error; err != nil {
			return recordError(err)
		}
		var candidate versionRecord
		if err := tx.Where("agent_id = ? AND number = ?", agent, version).Take(&candidate).Error; err != nil {
			return recordError(err)
		}
		config := candidate.Config
		if len(config.Toolsets) != 1 || config.Toolsets[0] != "filesystem" || len(config.Tools) != 2 {
			return ErrInvalid
		}
		for i := range config.Tools {
			tool := &config.Tools[i]
			if tool.Name != "finish" && tool.Name != "run_checks" {
				return ErrInvalid
			}
			tool.URL = strings.TrimRight(gateway, "/") + "/internal/v1/tools/" + tool.Name
			tool.TokenEnv = "CODING_TOOLS_TOKEN"
			if tool.Name == "finish" {
				tool.Description = "Submit the business task result and end this run. The platform independently verifies the patch and checks, then publishes changes as a draft merge request when verification succeeds."
			}
		}
		if err := ValidateAgentConfig(config); err != nil {
			return err
		}
		var existing agentRecord
		err := tx.Where("id = ?", id).Take(&existing).Error
		if err == nil {
			if existing.OwnerID != runtimeOwner {
				return ErrConflict
			}
			if err := tx.Where("agent_id = ? AND number = 1", id).Take(&result).Error; err != nil {
				return recordError(err)
			}
			a, _ := json.Marshal(config)
			b, _ := json.Marshal(result.Config)
			if string(a) != string(b) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		name := source.Name
		if len(name) > 180 {
			name = agent
		}
		record := agentRecord{
			ID:                id,
			OwnerID:           runtimeOwner,
			Name:              "项目 Coding · " + name,
			Draft:             config,
			Revision:          1,
			PublishedRevision: 1,
			PublishedVersion:  1,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		result = versionRecord{AgentID: id, Number: 1, Config: config}
		return tx.Create(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return result.version(), nil
}
