package agenticv1

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

type memoryClient struct{ transport *transport }

var _ MemoryAPI = (*memoryClient)(nil)

func (c *memoryClient) Create(ctx context.Context, req *CreateMemoryRequest) (*memory.Entry, error) {
	result, _, err := doJSON[memory.Entry](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/memories",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusCreated,
	)
	return result, err
}

func (c *memoryClient) ListVersions(
	ctx context.Context,
	id string,
	opts *ListMemoryVersionsOptions,
) (*memory.VersionPage, error) {
	query := url.Values{}
	if opts != nil {
		if opts.AfterRevision != 0 {
			query.Set("afterRevision", strconv.FormatInt(opts.AfterRevision, 10))
		}
		if opts.Limit != 0 {
			query.Set("limit", strconv.Itoa(opts.Limit))
		}
	}
	result, _, err := doJSON[memory.VersionPage](ctx, c.transport, http.MethodGet,
		withQuery("/v1/memories/"+url.PathEscape(id)+"/versions", query), nil, nil, http.StatusOK)
	return result, err
}

func (c *memoryClient) GetVersion(ctx context.Context, id, versionID string) (*memory.Version, error) {
	result, _, err := doJSON[memory.Version](ctx, c.transport, http.MethodGet,
		"/v1/memories/"+url.PathEscape(id)+"/versions/"+url.PathEscape(versionID), nil, nil, http.StatusOK)
	return result, err
}

func (c *memoryClient) Get(ctx context.Context, id string) (*memory.Entry, error) {
	result, _, err := doJSON[memory.Entry](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/memories/"+url.PathEscape(id),
		nil,
		nil,
		http.StatusOK,
	)
	return result, err
}

func (c *memoryClient) List(ctx context.Context, opts *ListMemoriesOptions) (*Page[memory.Entry], error) {
	result, _, err := doJSON[Page[memory.Entry]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/memories", opts.PageOptions.query()),
		nil,
		nil,
		http.StatusOK,
	)
	return result, err
}

func (c *memoryClient) Update(ctx context.Context, id string, req *UpdateMemoryRequest) (*memory.Entry, error) {
	result, _, err := doJSON[memory.Entry](
		ctx,
		c.transport,
		http.MethodPut,
		"/v1/memories/"+url.PathEscape(id),
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusOK,
	)
	return result, err
}

func (c *memoryClient) Retire(ctx context.Context, id string, req *RetireMemoryRequest) (*memory.Entry, error) {
	result, _, err := doJSON[memory.Entry](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/memories/"+url.PathEscape(id)+"/retire",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusOK,
	)
	return result, err
}
