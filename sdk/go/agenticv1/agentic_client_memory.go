package agenticv1

import (
	"context"
	"net/http"
	"net/url"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

type memoryClient struct{ transport *transport }

var _ MemoryAPI = (*memoryClient)(nil)

func (c *memoryClient) Create(ctx context.Context, req *CreateMemoryRequest) (*memory.Entry, error) {
	return jsonResult[memory.Entry](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/memories",
		req.Document,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusCreated,
	)
}

func (c *memoryClient) Get(ctx context.Context, id string) (*memory.Entry, error) {
	return jsonResult[memory.Entry](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/memories/"+url.PathEscape(id),
		nil,
		nil,
		http.StatusOK,
	)
}

func (c *memoryClient) List(ctx context.Context, opts *ListMemoriesOptions) (*Page[memory.Entry], error) {
	return jsonResult[Page[memory.Entry]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/memories", opts.PageOptions.query()),
		nil,
		nil,
		http.StatusOK,
	)
}

func (c *memoryClient) Update(ctx context.Context, id string, req *UpdateMemoryRequest) (*memory.Entry, error) {
	return jsonResult[memory.Entry](
		ctx,
		c.transport,
		http.MethodPut,
		"/v1/memories/"+url.PathEscape(id),
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusOK,
	)
}

func (c *memoryClient) Retire(ctx context.Context, id string, req *RetireMemoryRequest) (*memory.Entry, error) {
	return jsonResult[memory.Entry](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/memories/"+url.PathEscape(id)+"/retire",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusOK,
	)
}
