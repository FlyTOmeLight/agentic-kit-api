package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
)

func TestSaveState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := createRun(t, s)
	claimed := claim(t, s, "w1")

	// Valid state persists.
	state := json.RawMessage(`{"version":3,"inflightModel":{"kind":"model","sequence":0}}`)
	if err := s.SaveState(ctx, claimed.Lease(), state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := s.Get(ctx, r.OwnerID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.State, &decoded); err != nil {
		t.Fatal(err)
	}
	marker, ok := decoded["inflightModel"].(map[string]any)
	if !ok || marker["kind"] != "model" {
		t.Fatalf("inflightModel not persisted: %s", got.State)
	}

	// Invalid JSON rejected.
	if err := s.SaveState(ctx, claimed.Lease(), json.RawMessage(`not json`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}

	// Stale lease rejected.
	stale := Lease{RunID: r.ID, WorkerID: "w1", Epoch: claimed.Epoch - 1}
	if err := s.SaveState(ctx, stale, state); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}

	// Wrong worker rejected.
	wrong := Lease{RunID: r.ID, WorkerID: "w2", Epoch: claimed.Epoch}
	if err := s.SaveState(ctx, wrong, state); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}

	// Cleared state persists (marker removed).
	cleared := json.RawMessage(`{"version":3}`)
	if err := s.SaveState(ctx, claimed.Lease(), cleared); err != nil {
		t.Fatalf("SaveState clear: %v", err)
	}
	got, err = s.Get(ctx, r.OwnerID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded = nil
	if err := json.Unmarshal(got.State, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["inflightModel"]; exists {
		t.Fatalf("inflightModel should be cleared: %s", got.State)
	}
}

func TestSaveStateTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := createRun(t, s)
	claimed := claim(t, s, "w1")
	setRunTenant(t, s, r, "tenant-a", "project-a")
	state := json.RawMessage(`{"version":3,"inflightModel":{"kind":"model","sequence":0}}`)
	ctxB := access.WithScope(ctx, access.Scope{TenantID: "tenant-b", ProjectID: "project-b"})
	if err := s.SaveState(ctxB, claimed.Lease(), state); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("got %v, want ErrLeaseLost", err)
	}
	got, err := s.Get(ctx, r.OwnerID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.State) != string(r.State) {
		t.Fatalf("cross-tenant SaveState wrote state: %s", got.State)
	}
}
