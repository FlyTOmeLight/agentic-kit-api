package agenticv1

import (
	"context"
	"io"
)

// RunAPI contains the run operations exposed over HTTP.
type RunAPI interface {
	Get(ctx context.Context, req *GetRunRequest) (*Run, error)
	Trace(ctx context.Context, req *GetRunTraceRequest) (*RunTrace, error)
	Cancel(ctx context.Context, req *CancelRunRequest) error
	Answer(ctx context.Context, req *AnswerRunRequest) error
	Resolve(ctx context.Context, req *ResolveRunRequest) error

	// OpenEvents returns raw SSE. The caller closes the body and supplies the last runID:seq to resume.
	OpenEvents(ctx context.Context, req *OpenRunEventsRequest) (io.ReadCloser, error)
}
