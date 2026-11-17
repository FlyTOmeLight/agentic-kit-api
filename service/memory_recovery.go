package service

import (
	"context"
	"time"
)

const (
	memoryRecoveryBatch = 20
	memoryRecoveryPass  = 2 * time.Minute
)

var memoryRecoveryInterval = 30 * time.Second

// pendingResumer is the background slice of the Memory service. Keeping it
// small lets the loop be tested without a database.
type pendingResumer interface {
	ResumePending(ctx context.Context, limit int) (int, error)
}

// resumePendingMemory retries committed Memory writes until ctx is canceled.
// A bounded pass deadline keeps Stop responsive even when the archive endpoint
// hangs; unprocessed receipts stay Pending for the next pass.
func resumePendingMemory(ctx context.Context, resumer pendingResumer, onError func(error)) {
	if resumer == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		passCtx, cancel := context.WithTimeout(ctx, memoryRecoveryPass)
		_, err := resumer.ResumePending(passCtx, memoryRecoveryBatch)
		cancel()
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		timer.Reset(memoryRecoveryInterval)
	}
}
