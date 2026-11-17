package agenticv1

import "github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"

type MemoryEntry = memory.Entry

type MemoryDocument = memory.Document

type MemoryVersion = memory.Version

type MemoryVersionPage = memory.VersionPage

type ListMemoryVersionsOptions struct {
	AfterRevision int64
	Limit         int
}

type CreateMemoryRequest struct {
	IdempotencyKey string         `json:"-" header:"Idempotency-Key" binding:"required,max=128"`
	Document       MemoryDocument `json:"document" binding:"required"`
}

type UpdateMemoryRequest struct {
	IdempotencyKey   string         `json:"-" header:"Idempotency-Key" binding:"required,max=128"`
	ExpectedRevision int64          `json:"expectedRevision" binding:"required,min=1"`
	Restore          bool           `json:"restore"`
	Document         MemoryDocument `json:"document" binding:"required"`
}

type RetireMemoryRequest struct {
	IdempotencyKey   string `json:"-" header:"Idempotency-Key" binding:"required,max=128"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"required,min=1"`
}

type ListMemoriesOptions struct {
	PageOptions
}
