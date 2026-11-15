package durable

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func compactionBatch(t *testing.T, store *Store, lease Lease, ids ...string) {
	t.Helper()
	ctx := context.Background()
	message := Message{Role: "assistant", Content: "inspect resources"}
	for _, id := range ids {
		message.ToolCalls = append(message.ToolCalls, ToolCall{ID: id, Name: "lookup", Arguments: `{}`})
	}
	if err := store.RecordDecision(ctx, lease, json.RawMessage(`{}`), message); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := store.BeginStep(ctx, lease, StepIntent{ID: id, Input: id, Policy: ReadOnly}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishStep(ctx, lease, id, "result:"+id, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompactionBoundaryAndAudit(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run := createRun(t, store)
	claimed := claim(t, store, "worker")
	compactionBatch(t, store, claimed.Lease(), "a", "b")
	compactionBatch(t, store, claimed.Lease(), "c")
	before, err := store.Messages(ctx, "owner", run.SessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []int64{3, 4, 6, 100} {
		if err = store.CompactRun(ctx, claimed.Lease(), "summary", boundary); !errors.Is(err, ErrConflict) {
			t.Fatalf("boundary %d: want Conflict, got %v", boundary, err)
		}
	}
	if err = store.CompactRun(ctx, claimed.Lease(), "summary", 5); err != nil {
		t.Fatal(err)
	}
	if err = store.CompactRun(ctx, claimed.Lease(), "summary", 5); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	for _, change := range []struct {
		summary  string
		boundary int64
	}{{summary: "different", boundary: 5}, {summary: "summary", boundary: 2}} {
		if err = store.CompactRun(
			ctx,
			claimed.Lease(),
			change.summary,
			change.boundary,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("changed or backward boundary accepted: %v", err)
		}
	}
	projection, err := store.CompactionContext(ctx, claimed.Lease())
	if err != nil || projection.FirstKeptSeq != 5 || projection.Summary != "summary" ||
		len(projection.Messages) != 2 || projection.Messages[0].Seq != 5 {
		t.Fatalf("projection = %+v, error %v", projection, err)
	}
	after, err := store.Messages(ctx, "owner", run.SessionID, 0, 200)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("audit messages changed: %v", err)
	}
	events, err := store.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "RunCompacted" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("compaction events = %d, want 1", count)
	}
	if err = store.RecordCompactionFailure(ctx, claimed.Lease(), "provider unavailable"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Get(ctx, "owner", run.ID)
	if err != nil || saved.CompactedSummary != "summary" || saved.FirstKeptSeq != 5 {
		t.Fatalf("failed summarization moved projection: %+v %v", saved, err)
	}
	if err = store.Cancel(ctx, "owner", run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompactionContext(ctx, claimed.Lease()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old context read: %v", err)
	}
	if err = store.CompactRun(ctx, claimed.Lease(), "new", 7); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old compaction write: %v", err)
	}
}

func TestCompactionRollsBackWithEvent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run := createRun(t, store)
	claimed := claim(t, store, "worker")
	compactionBatch(t, store, claimed.Lease(), "a")
	if err := store.DB().
		Exec(`ALTER TABLE durable_event ADD CONSTRAINT reject_compaction CHECK (kind <> 'RunCompacted')`).
		Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CompactRun(ctx, claimed.Lease(), "summary", 4); err == nil {
		t.Fatal("expected event failure")
	}
	var row table.Run
	if err := store.DB().Where("id = ?", run.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CompactedSummary != "" || row.FirstKeptSeq != 0 {
		t.Fatal("compaction was not rolled back")
	}
	if err := store.DB().Exec(`ALTER TABLE durable_event DROP CONSTRAINT reject_compaction`).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CompactRun(ctx, claimed.Lease(), "summary", 4); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionContextIncludesEntireRunOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	previous := createRun(t, store)
	oldClaim := claim(t, store, "worker")
	if err := store.CommitBoundary(ctx, oldClaim.Lease(), Boundary{
		Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "previous answer",
	}); err != nil {
		t.Fatal(err)
	}
	run, _, err := submitFixture(
		t,
		store,
		"owner",
		previous.SessionID,
		"next",
		"new question",
		time.Hour,
		3,
		AgentConfig{
			Model:    ModelSpec{Name: "test", BaseURL: "http://localhost:11434/v1"},
			MaxTurns: 5,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, store, "worker")
	var rows []table.Message
	for index := 0; index < 101; index++ {
		id := NewID()
		seq := int64(4 + index*2)
		rows = append(rows,
			table.Message{
				SessionID: run.SessionID,
				Seq:       seq,
				RunID:     run.ID,
				Kind:      "Assistant",
				Message: Message{
					Role:      "assistant",
					ToolCalls: []ToolCall{{ID: id, Name: "lookup", Arguments: `{}`}},
				},
			},
			table.Message{
				SessionID: run.SessionID, Seq: seq + 1, RunID: run.ID, Kind: "Tool",
				Message: Message{Role: "tool", Content: "result", ToolCallID: id},
			},
		)
	}
	if err = store.DB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.DB().
		Model(&table.Session{}).
		Where("id = ?", run.SessionID).
		Update("message_seq", 205).
		Error; err != nil {
		t.Fatal(err)
	}
	projection, err := store.CompactionContext(ctx, claimed.Lease())
	if err != nil || len(projection.Messages) != 203 || projection.Messages[0].Seq != 3 ||
		projection.Messages[202].Seq != 205 {
		t.Fatalf("projection truncated or crossed runs: count=%d error=%v", len(projection.Messages), err)
	}
	if err = store.CompactRun(ctx, claimed.Lease(), "large context summary", 204); err != nil {
		t.Fatal(err)
	}
	projection, err = store.CompactionContext(ctx, claimed.Lease())
	if err != nil || len(projection.Messages) != 2 || projection.Messages[0].Seq != 204 {
		t.Fatalf("incorrect session-relative boundary: %+v %v", projection, err)
	}
}
