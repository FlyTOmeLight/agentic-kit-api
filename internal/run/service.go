package run

import (
	"context"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// Service is the transactional behavior required by this HTTP module.
type Service interface {
	Get(ctx context.Context, owner, id string) (*durable.Run, error)
	Cancel(ctx context.Context, owner, id string) error
	SupplyResult(ctx context.Context, owner, id, step, result string, expected durable.Status) error
	Events(ctx context.Context, owner, id string, after int64) ([]durable.Event, error)
}

var _ Service = (*durable.Store)(nil)
