package durable

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
)

func TestRecordModelOverflow(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run := createRun(t, store)
	lease := claim(t, store, "worker").Lease()
	state := json.RawMessage(`{"phase":"recover"}`)
	for _, invalid := range []struct {
		attempt int
		detail  string
		state   json.RawMessage
	}{
		{attempt: 0, detail: "overflow", state: state},
		{attempt: 1, detail: " \n", state: state},
		{attempt: 1, detail: "overflow", state: json.RawMessage(`bad json`)},
	} {
		if err := store.RecordModelOverflow(
			ctx,
			lease,
			invalid.attempt,
			invalid.detail,
			invalid.state,
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Fatalf("invalid model overflow: %v", err)
		}
	}
	stale := lease
	stale.Epoch--
	wrongTenant := access.WithScope(ctx, access.Scope{TenantID: "another-tenant"})
	if err := store.RecordModelOverflow(ctx, stale, 1, "overflow", state); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale lease: %v", err)
	}
	if err := store.RecordModelOverflow(wrongTenant, lease, 1, "overflow", state); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("wrong tenant: %v", err)
	}
	if err := store.RecordModelOverflow(ctx, lease, 1, "overflow", state); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(ctx, run.OwnerID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint struct{ Phase string }
	if err := json.Unmarshal(before.State, &checkpoint); err != nil || checkpoint.Phase != "recover" ||
		before.Status != Running || before.Lease() != lease {
		t.Fatalf(
			"state or lease after overflow: state %s, status %s, lease %+v, error %v",
			before.State,
			before.Status,
			before.Lease(),
			err,
		)
	}
	events, err := store.Events(ctx, run.OwnerID, run.ID, before.EventSeq-1)
	if err != nil || len(events) != 1 || events[0].Kind != "ModelOverflowRejected" {
		t.Fatalf("overflow event: %+v, %v", events, err)
	}
	var payload struct {
		Attempt int
		Detail  string
	}
	if err := json.Unmarshal(
		events[0].Payload,
		&payload,
	); err != nil || payload.Attempt != 1 ||
		payload.Detail != "overflow" {
		t.Fatalf("overflow payload: %s, %v", events[0].Payload, err)
	}

	if err := store.DB().
		Exec(`ALTER TABLE durable_event ADD CONSTRAINT reject_overflow CHECK (kind <> 'ModelOverflowRejected') NOT VALID`).
		Error; err != nil {
		t.Fatal(err)
	}
	if err := store.RecordModelOverflow(
		ctx,
		lease,
		2,
		"still too large",
		json.RawMessage(`{"phase":"next"}`),
	); err == nil {
		t.Fatal("expected overflow event insertion failure")
	}
	after, err := store.Get(ctx, run.OwnerID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.State) != string(before.State) || after.EventSeq != before.EventSeq {
		t.Fatalf("failed overflow write changed run: state %s, event seq %d", after.State, after.EventSeq)
	}
}

func TestReadStepResult(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	createRun(t, store)
	lease := claim(t, store, "worker").Lease()
	compactionBatch(t, store, lease, "done")
	if _, err := store.BeginStep(
		ctx,
		lease,
		StepIntent{ID: "started", Input: "query", Policy: ReadOnly},
	); err != nil {
		t.Fatal(err)
	}
	if result, err := store.ReadStepResult(ctx, lease, "done"); result != "result:done" || err != nil {
		t.Fatalf("completed step = %q, %v", result, err)
	}
	for _, id := range []string{"started", "missing"} {
		if _, err := store.ReadStepResult(ctx, lease, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("step %q: %v", id, err)
		}
	}
	if _, err := store.ReadStepResult(ctx, lease, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty step: %v", err)
	}
	badLease := lease
	badLease.WorkerID = "another-worker"
	if _, err := store.ReadStepResult(ctx, badLease, "done"); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("wrong worker: %v", err)
	}
	wrongTenant := access.WithScope(ctx, access.Scope{TenantID: "another-tenant"})
	if _, err := store.ReadStepResult(wrongTenant, lease, "done"); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("wrong tenant: %v", err)
	}
}

func TestSessionCompactionPromotesOnlyAtSuccessfulCompletion(t *testing.T) {
	tests := []struct {
		name        string
		boundary    BoundaryKind
		existing    int64
		wantThrough int64
		wantSummary string
	}{
		{name: "complete advances", boundary: BoundaryComplete, wantThrough: 3, wantSummary: "new summary"},
		{
			name:        "complete replaces at same watermark",
			boundary:    BoundaryComplete,
			existing:    3,
			wantThrough: 3,
			wantSummary: "new summary",
		},
		{
			name:        "complete cannot move backward",
			boundary:    BoundaryComplete,
			existing:    4,
			wantThrough: 4,
			wantSummary: "existing summary",
		},
		{name: "failure", boundary: BoundaryFail, existing: 1, wantThrough: 1, wantSummary: "existing summary"},
		{name: "cancel", existing: 1, wantThrough: 1, wantSummary: "existing summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			run := createRun(t, store)
			lease := claim(t, store, "worker").Lease()
			compactionBatch(t, store, lease, "a")
			compactionBatch(t, store, lease, "b")
			if tt.existing > 0 {
				if err := store.DB().
					Model(&table.Session{}).
					Where("id = ?", run.SessionID).
					Updates(map[string]any{
						"compacted_summary":     "existing summary",
						"compacted_through_seq": tt.existing,
					}).
					Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := store.CompactRun(ctx, lease, "new summary", 4); err != nil {
				t.Fatal(err)
			}
			check := func(through int64, summary string) {
				t.Helper()
				var session table.Session
				if err := store.DB().Where("id = ?", run.SessionID).Take(&session).Error; err != nil {
					t.Fatal(err)
				}
				if session.CompactedThroughSeq != through || session.CompactedSummary != summary {
					t.Fatalf(
						"session summary = %q at %d, want %q at %d",
						session.CompactedSummary,
						session.CompactedThroughSeq,
						summary,
						through,
					)
				}
			}
			before := ""
			if tt.existing > 0 {
				before = "existing summary"
			}
			check(tt.existing, before)
			if tt.boundary == "" {
				if err := store.Cancel(ctx, run.OwnerID, run.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				boundary := Boundary{Kind: tt.boundary, State: json.RawMessage(`{}`)}
				if tt.boundary == BoundaryComplete {
					boundary.Output = "final answer"
				} else {
					boundary.Detail = "failure"
				}
				if err := store.CommitBoundary(ctx, lease, boundary); err != nil {
					t.Fatal(err)
				}
			}
			check(tt.wantThrough, tt.wantSummary)
		})
	}
}

func TestNextChatSnapshotFreezesSessionSummaryAndPairedTail(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, cfg := testConversation(t, store, Spec{MaxTurns: 5})
	submit := func(key string) *Run {
		t.Helper()
		run, _, err := submitFixture(t, store, "owner", session.ID, key, key, time.Hour, 3, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	complete := func(lease Lease, output string) {
		t.Helper()
		if err := store.CommitBoundary(ctx, lease, Boundary{
			Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: output,
		}); err != nil {
			t.Fatal(err)
		}
	}

	submit("fully compacted turn")
	complete(claim(t, store, "worker").Lease(), "older answer")
	prev := submit("partially compacted turn")
	lease := claim(t, store, "worker").Lease()
	compactionBatch(t, store, lease, "compact-away")
	compactionBatch(t, store, lease, "keep-a", "keep-b")
	if err := store.CompactRun(ctx, lease, "progress summary", 6); err != nil {
		t.Fatal(err)
	}
	complete(lease, "answer after retained tool results")
	after := submit("later full turn")
	complete(claim(t, store, "worker").Lease(), "later answer")

	next := submit("next question")
	var stored table.Run
	if err := store.DB().Where("id = ?", next.ID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(table.ContextSnapshot(next.ContextSnapshot), stored.ContextSnapshot) {
		t.Fatal("snapshot payload bytes or digest changed during storage")
	}
	snapshot := next.ContextSnapshot
	decoded, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        snapshot.Format,
		PolicyVersion: snapshot.PolicyVersion,
		Digest:        snapshot.Digest,
		Payload:       snapshot.Payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	history := decoded.Materials.History
	if decoded.Materials.BaseSummary != "progress summary" ||
		len(history) != 2 || history[0].Source.ID != prev.ID || history[1].Source.ID != after.ID {
		t.Fatalf("wrong frozen summary or history: %+v", decoded.Materials)
	}
	tail := history[0].Messages
	if len(tail) != 4 || tail[0].Role != contextbuilder.RoleAssistant ||
		len(
			tail[0].ToolCalls,
		) != 2 || tail[1].ToolCallID != prev.ID+":keep-a" || tail[2].ToolCallID != prev.ID+":keep-b" ||
		tail[3].Content != "answer after retained tool results" || contextbuilder.ValidateGroup(tail) != nil ||
		history[1].Messages[0].Role != contextbuilder.RoleUser {
		t.Fatalf("wrong retained messages: %+v", history)
	}
	audit, err := store.Messages(ctx, "owner", session.ID, 0, 200)
	if err != nil || len(audit) == 0 || audit[0].Message.Content != "fully compacted turn" {
		t.Fatalf("summary promotion changed audit history: %+v, %v", audit, err)
	}
}

func TestGoalIterationFreezesCompactedHistory(t *testing.T) {
	store := testStore(t)
	_, previous := failedGoalIteration(t, store)
	if err := store.DB().Model(&table.Session{}).Where("id = ?", previous.SessionID).Updates(map[string]any{
		"compacted_summary": "prior progress", "compacted_through_seq": 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	next, err := store.QueueGoalIteration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := next.ContextSnapshot
	decoded, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        snapshot.Format,
		PolicyVersion: snapshot.PolicyVersion,
		Digest:        snapshot.Digest,
		Payload:       snapshot.Payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Materials.BaseSummary != "prior progress" || len(decoded.Materials.History) != 1 ||
		len(decoded.Materials.History[0].Messages) != 1 ||
		decoded.Materials.History[0].Messages[0].Content != "previous answer" {
		t.Fatalf("unexpected frozen context for goal iteration: %+v", decoded.Materials)
	}
}
