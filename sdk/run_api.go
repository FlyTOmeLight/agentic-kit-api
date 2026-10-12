package sdk

import (
	"context"
	"io"
)

// RunAPI contains the run operations exposed over HTTP.
type RunAPI interface {
	Get(ctx context.Context, id string) (*Run, error)
	Trace(ctx context.Context, id string, after int64) (*RunTrace, error)
	Cancel(ctx context.Context, id string) error
	Answer(ctx context.Context, id string, req SupplyResultRequest) error
	Resolve(ctx context.Context, id string, req SupplyResultRequest) error
	// OpenEvents returns raw SSE. The caller closes the body and supplies the last runID:seq to resume.
	OpenEvents(ctx context.Context, id, after string) (io.ReadCloser, error)
}
