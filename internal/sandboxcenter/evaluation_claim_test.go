package sandboxcenter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func TestEvaluationDispatchFencesExpiredClaim(t *testing.T) {
	store := testStore(t)
	lease := operationLease(t, store)
	createEvaluationBinding(t, store, lease, "owner", "true")
	ctx := context.Background()
	if err := store.db.Model(&table.Run{}).Where("id = ?", lease.HolderRunID).
		Updates(map[string]any{
			"status": "Completed", "worker_id": "", "lease_until": nil,
			"evaluation_worker_id": lease.WorkerID, "evaluation_deadline": time.Now().Add(time.Minute),
		}).Error; err != nil {
		t.Fatal(err)
	}
	key := "eval-" + lease.SandboxID
	hash := payloadHashOf("true")
	if _, err := store.BeginOperation(ctx, lease.ID, key, "command", hash); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("unclaimed evaluation dispatched: %v", err)
	}
	first := EvaluationClaim{WorkerID: lease.WorkerID, Epoch: 1}
	op, err := store.BeginOperation(ctx, lease.ID, key, "command", hash, first)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.db.Model(&table.Run{}).Where("id = ?", lease.HolderRunID).
		Updates(map[string]any{"evaluation_claim_worker_id": "replacement", "evaluation_epoch": 2}).
		Error; err != nil {
		t.Fatal(err)
	}
	if err = store.MarkDispatchStarted(ctx, op.ID, "guard", first); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("old worker dispatched after takeover: %v", err)
	}
	if _, err = store.BeginOperation(ctx, lease.ID, key, "command", hash, first); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("old worker reused pending operation after takeover: %v", err)
	}
	second := EvaluationClaim{WorkerID: "replacement", Epoch: 2}
	if err = store.MarkDispatchStarted(ctx, op.ID, "guard", second); err != nil {
		t.Fatalf("new owner cannot dispatch original pending operation: %v", err)
	}
}
