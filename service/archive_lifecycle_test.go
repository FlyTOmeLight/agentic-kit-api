package service

import (
	"context"
	"errors"
	"testing"
)

type archiveCloser struct {
	calls int
	err   error
}

func (closer *archiveCloser) Close() error {
	closer.calls++
	return closer.err
}

func TestOwnedArchiveClosesOnce(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "before start", true: "after stop"}[started], func(t *testing.T) {
			closeErr := errors.New("close failed")
			closer := &archiveCloser{err: closeErr}
			api := &API{ownedArchive: closer}
			if started {
				_, api.cancel = context.WithCancel(context.Background())
				defer api.cancel()
				api.done = make(chan struct{})
				close(api.done)
			}
			for range 2 {
				if err := api.Stop(context.Background()); !errors.Is(err, closeErr) {
					t.Fatalf("Stop = %v, want close error", err)
				}
			}
			if closer.calls != 1 {
				t.Fatalf("Close called %d times", closer.calls)
			}
		})
	}
}

func TestStopTimeoutKeepsArchiveOpen(t *testing.T) {
	closer := &archiveCloser{}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &API{ownedArchive: closer, cancel: cancel, done: make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if err := api.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v", err)
	}
	if closer.calls != 0 {
		t.Fatal("archive closed before background work exited")
	}
	close(api.done)
	if err := api.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closer.calls != 1 {
		t.Fatalf("Close called %d times", closer.calls)
	}
}
