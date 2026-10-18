package durable

import (
	"context"
	"net/url"
	"strings"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil &&
		u.Fragment == ""
}

// PrepareChatSubmission resolves the published agent version and conversation history.
func PrepareChatSubmission(
	ctx context.Context,
	tx *gorm.DB,
	facts execution.SubmissionFacts,
) (execution.PreparedSubmission, error) {
	var agentRow table.Agent
	if err := tx.WithContext(ctx).Where("owner_id = ? AND id = ?", facts.Owner, facts.SessionAgentID).
		Take(&agentRow).Error; err != nil {
		return execution.PreparedSubmission{}, recordError(err)
	}
	if agentRow.PublishedVersion == 0 {
		return execution.PreparedSubmission{}, ErrConflict
	}
	var version table.AgentVersion
	if err := tx.WithContext(ctx).Where("agent_id = ? AND number = ?", agentRow.ID, agentRow.PublishedVersion).
		Take(&version).Error; err != nil {
		return execution.PreparedSubmission{}, err
	}
	if len(version.Config.Toolsets) > 0 {
		return execution.PreparedSubmission{}, ErrInvalid
	}
	spec := execution.CompileRunSpec(version.Config, agentRow.ID, version.Number, facts.Query, nil, nil)
	if err := loadConversationHistory(tx, facts.Owner, facts.SessionID, &spec); err != nil {
		return execution.PreparedSubmission{}, err
	}
	return execution.PreparedSubmission{
		Spec:        spec,
		UserMessage: execution.Message{Role: "user", Content: facts.Query},
	}, nil
}

// PrepareApplicationSubmission resolves an explicit published version and optional filesystem binding.
func PrepareApplicationSubmission(
	ctx context.Context,
	tx *gorm.DB,
	facts execution.SubmissionFacts,
) (execution.PreparedSubmission, error) {
	input := facts.ExecutionSpec
	var agentRow table.Agent
	if err := tx.WithContext(ctx).Where("owner_id = ? AND id = ?", facts.Owner, input.AgentID).
		Take(&agentRow).Error; err != nil {
		return execution.PreparedSubmission{}, recordError(err)
	}
	var version table.AgentVersion
	if err := tx.WithContext(ctx).Where("agent_id = ? AND number = ?", agentRow.ID, input.AgentVersion).
		Take(&version).Error; err != nil {
		return execution.PreparedSubmission{}, recordError(err)
	}
	spec := execution.CompileRunSpec(
		version.Config,
		agentRow.ID,
		version.Number,
		input.Query,
		input.Scope,
		input.Filesystem,
	)
	if len(spec.Toolsets) > 0 {
		if spec.Filesystem == nil || spec.Filesystem.Provider != "AIO" ||
			!validEndpoint(spec.Filesystem.URL) ||
			spec.Filesystem.TokenEnv == "" ||
			!strings.HasPrefix(spec.Filesystem.Workspace, "/") {
			return execution.PreparedSubmission{}, ErrInvalid
		}
	} else if spec.Filesystem != nil {
		return execution.PreparedSubmission{}, ErrInvalid
	}
	if err := loadConversationHistory(tx, facts.Owner, facts.SessionID, &spec); err != nil {
		return execution.PreparedSubmission{}, err
	}
	return execution.PreparedSubmission{
		Spec:        spec,
		UserMessage: execution.Message{Role: "user", Content: input.Query},
	}, nil
}

func loadConversationHistory(tx *gorm.DB, owner, session string, spec *execution.Spec) error {
	prior := tx.Model(&table.Run{}).
		Select("id").
		Where("owner_id = ? AND session_id = ? AND status = ?", owner, session, execution.Completed).
		Order("created_at DESC, id DESC").
		Limit(20)
	var history []table.Message
	if err := tx.Where("session_id = ? AND run_id IN (?) AND kind IN ?", session, prior, []string{"User", "Final"}).
		Order("seq").
		Find(&history).
		Error; err != nil {
		return err
	}
	for _, m := range history {
		spec.History = append(spec.History, m.Message)
	}
	return nil
}
