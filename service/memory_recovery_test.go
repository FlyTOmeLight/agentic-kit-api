package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type pendingResumerFunc func(context.Context, int) (int, error)

func (f pendingResumerFunc) ResumePending(ctx context.Context, limit int) (int, error) {
	return f(ctx, limit)
}

func TestResumePendingMemoryRunsImmediatelyAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resumePendingMemory(
			ctx,
			pendingResumerFunc(func(_ context.Context, limit int) (int, error) {
				calls <- limit
				return 0, nil
			}),
			nil,
		)
	}()
	select {
	case limit := <-calls:
		if limit != memoryRecoveryBatch {
			t.Fatalf("limit = %d, want %d", limit, memoryRecoveryBatch)
		}
	case <-time.After(time.Second):
		t.Fatal("first recovery pass did not run immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not stop after cancellation")
	}
}

func TestResumePendingMemoryRepeatsAndReportsErrors(t *testing.T) {
	previous := memoryRecoveryInterval
	memoryRecoveryInterval = time.Millisecond
	defer func() { memoryRecoveryInterval = previous }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := errors.New("resume failed")
	calls := make(chan struct{}, 2)
	reported := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resumePendingMemory(
			ctx,
			pendingResumerFunc(func(passCtx context.Context, limit int) (int, error) {
				deadline, ok := passCtx.Deadline()
				if !ok || time.Until(deadline) > memoryRecoveryPass {
					t.Errorf("pass context deadline = %v, ok = %t", deadline, ok)
				}
				if limit != memoryRecoveryBatch {
					t.Errorf("limit = %d, want %d", limit, memoryRecoveryBatch)
				}
				calls <- struct{}{}
				return 0, failure
			}),
			func(err error) { reported <- err },
		)
	}()
	for range 2 {
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("recovery loop did not repeat")
		}
	}
	select {
	case err := <-reported:
		if !errors.Is(err, failure) {
			t.Fatalf("reported error = %v, want %v", err, failure)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery error was not reported")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not stop after cancellation")
	}
}

func TestResumePendingMemoryWithoutResumerReturns(t *testing.T) {
	resumePendingMemory(context.Background(), nil, nil)
}
