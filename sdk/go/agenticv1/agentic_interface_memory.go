package agenticv1

import (
	"context"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// MemoryAPI is the public behavior for memory management.
type MemoryAPI interface {
	Create(ctx context.Context, req *CreateMemoryRequest) (*memory.Entry, error)
	Get(ctx context.Context, id string) (*memory.Entry, error)
	List(ctx context.Context, opts *ListMemoriesOptions) (*Page[memory.Entry], error)
	ListVersions(ctx context.Context, id string, opts *ListMemoryVersionsOptions) (*memory.VersionPage, error)
	GetVersion(ctx context.Context, id, versionID string) (*memory.Version, error)
	Update(ctx context.Context, id string, req *UpdateMemoryRequest) (*memory.Entry, error)
	Retire(ctx context.Context, id string, req *RetireMemoryRequest) (*memory.Entry, error)
}
