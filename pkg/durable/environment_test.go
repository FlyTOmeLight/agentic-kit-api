package durable

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func TestEnvironmentPrepareReplayAndActivate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "worker")

	env, err := s.PrepareEnvironment(ctx, r.Lease(), "acquire-1", "coding", 1)
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != EnvironmentPreparing || env.RunEpoch != r.Epoch || env.SandboxID != "" {
		t.Fatalf("unexpected prepared environment: %+v", env)
	}
	// Replay with the same key and input returns the same record without a
	// second row, so a retried acquire reuses one sandbox request key.
	replay, err := s.PrepareEnvironment(ctx, r.Lease(), "acquire-1", "coding", 1)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != env.ID {
		t.Fatalf("replay created a new record: %s vs %s", replay.ID, env.ID)
	}
	if _, err = s.PrepareEnvironment(ctx, r.Lease(), "acquire-1", "coding", 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key with different input must conflict, got %v", err)
	}

	env, err = s.ActivateEnvironment(ctx, r.Lease(), "acquire-1", "sb-1", "lease-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != EnvironmentActive || env.SandboxID != "sb-1" || env.LeaseID != "lease-1" {
		t.Fatalf("unexpected active environment: %+v", env)
	}
	// Activating twice with the same facts is idempotent.
	if _, err = s.ActivateEnvironment(ctx, r.Lease(), "acquire-1", "sb-1", "lease-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActivateEnvironment(
		ctx,
		r.Lease(),
		"acquire-1",
		"sb-2",
		"lease-2",
		1,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("activate with different sandbox must conflict, got %v", err)
	}

	active, err := s.ActiveEnvironment(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != env.ID {
		t.Fatalf("active environment %s, want %s", active.ID, env.ID)
	}

	if err = s.MarkEnvironmentReleased(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkEnvironmentReleased(ctx, env.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second release must conflict, got %v", err)
	}
	if _, err = s.ActiveEnvironment(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("released environment must not be active, got %v", err)
	}
	unfinished, err := s.UnfinishedEnvironments(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfinished) != 0 {
		t.Fatalf("released environment must not be unfinished: %+v", unfinished)
	}
}

func TestEnvironmentBoundToClaimEpoch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	first := claim(t, s, "worker-a")
	if _, err := s.PrepareEnvironment(ctx, first.Lease(), "acquire-1", "coding", 1); err != nil {
		t.Fatal(err)
	}

	// Force the first claim to expire, then let another worker take over.
	if err := s.db.Model(&table.Run{}).Where("id = ?", first.ID).
		Update("lease_until", gorm.Expr("clock_timestamp()-interval '1 second'")).Error; err != nil {
		t.Fatal(err)
	}
	second := claim(t, s, "worker-b")
	if second.ID != first.ID || second.Epoch == first.Epoch {
		t.Fatalf("expected same run with a new epoch, got %s epoch %d", second.ID, second.Epoch)
	}

	// The new epoch may read the record for reattachment but must not mutate
	// the old epoch's binding through the lease-checked path.
	env, err := s.GetEnvironment(ctx, first.ID, "acquire-1")
	if err != nil {
		t.Fatal(err)
	}
	if env.RunEpoch != first.Epoch {
		t.Fatalf("environment epoch %d, want %d", env.RunEpoch, first.Epoch)
	}
	if _, err = s.ActivateEnvironment(
		ctx,
		second.Lease(),
		"acquire-1",
		"sb-1",
		"lease-1",
		1,
	); !errors.Is(
		err,
		ErrLeaseLost,
	) {
		t.Fatalf("new epoch must not activate the old binding, got %v", err)
	}
	// A stale worker holds no lease at all.
	if _, err = s.PrepareEnvironment(ctx, first.Lease(), "acquire-2", "coding", 1); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale worker must not prepare environments, got %v", err)
	}
	unfinished, err := s.UnfinishedEnvironments(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfinished) != 1 || unfinished[0].Status != EnvironmentPreparing {
		t.Fatalf("reconciler must see the preparing record: %+v", unfinished)
	}
}
