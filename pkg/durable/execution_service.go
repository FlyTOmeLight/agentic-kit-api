package durable

import (
	"context"
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// SubmitExecution freezes a published Agent version and business input.
// A task reuses its conversation; retries reuse the originally accepted run.
func (s *Store) SubmitExecution(
	ctx context.Context,
	owner, key string,
	inputSpec execution.ExecutionSpec,
	ttl time.Duration,
) (*Run, bool, error) {
	input, _ := json.Marshal(struct {
		Spec execution.ExecutionSpec
		TTL  time.Duration
	}{Spec: inputSpec, TTL: ttl})
	return s.accept(ctx, execution.SubmitCommand{
		Owner:         owner,
		RequestKey:    key,
		RequestHash:   Hash(input),
		TTL:           ttl,
		MaxAttempts:   5,
		Kind:          execution.SubmitApplication,
		ExecutionSpec: inputSpec,
	}, PrepareApplicationSubmission)
}

// CheckExecutionLease uses the database clock and rejects canceled, expired, or old attempts.
func (s *Store) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	var n int64
	e := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("owner_id = ? AND id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", owner, id, epoch, Running).
		Count(&n).
		Error
	return n == 1, e
}
