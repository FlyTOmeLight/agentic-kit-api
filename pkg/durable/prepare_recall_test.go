package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

type stubRecaller struct {
	entries []memory.Entry
	err     error
	called  bool
}

func (s *stubRecaller) RecallTx(
	_ context.Context,
	_ *gorm.DB,
	_ memory.Scope,
	_ string,
	_ string,
) ([]memory.Entry, error) {
	s.called = true
	return s.entries, s.err
}

func submitWithMemory(
	t *testing.T,
	s *Store,
	session string,
	selection memory.Selection,
	recaller MemoryRecaller,
) *Run {
	t.Helper()
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session, Query: "question"})
	r, _, err := s.Accept(context.Background(), execution.SubmitCommand{
		Owner:       "owner",
		RequestKey:  NewID(),
		RequestHash: Hash(input),
		TTL:         time.Hour,
		MaxAttempts: 3,
		Kind:        execution.SubmitChat,
		SessionID:   session,
		Query:       "question",
		Memory:      selection,
	}, PrepareChatSubmissionWith(recaller))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return r
}

func decodeRunSnapshot(t *testing.T, s *Store, runID string) contextbuilder.Snapshot {
	t.Helper()
	r, err := s.Get(context.Background(), "owner", runID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	snapshot, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        r.ContextSnapshot.Format,
		PolicyVersion: r.ContextSnapshot.PolicyVersion,
		Digest:        r.ContextSnapshot.Digest,
		Payload:       r.ContextSnapshot.Payload,
	})
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	return snapshot
}

func TestSubmitRecallFreezesMemoryMaterials(t *testing.T) {
	s := testStore(t)
	session := testConversation(t, s, Spec{MaxTurns: 5})

	recaller := &stubRecaller{entries: []memory.Entry{{
		ID:       "mem_1",
		Scope:    memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"},
		Kind:     memory.KindPreference,
		Summary:  "editor preference",
		Body:     json.RawMessage(`"use tabs"`),
		Revision: 1,
	}}}
	run := submitWithMemory(t, s, session.ID, memory.SelectionRecall, recaller)
	if !recaller.called {
		t.Fatal("recaller was not called for a Recall submission")
	}

	snapshot := decodeRunSnapshot(t, s, run.ID)
	if snapshot.MemoryMode != contextbuilder.MemoryRecall {
		t.Fatalf("MemoryMode = %q, want Recall", snapshot.MemoryMode)
	}
	if len(snapshot.Materials.Memory) != 1 {
		t.Fatalf("memory materials len = %d", len(snapshot.Materials.Memory))
	}
	block := snapshot.Materials.Memory[0]
	if block.Source.Kind != contextbuilder.SourceMemory || block.Source.ID != "mem_1" ||
		block.Source.Revision != 1 {
		t.Fatalf("unexpected block source: %+v", block.Source)
	}
	if block.Content == "" || block.Priority != 2 {
		t.Fatalf("unexpected block: %+v", block)
	}
}

func TestSubmitRecallEmptyIsDistinctFact(t *testing.T) {
	s := testStore(t)
	session := testConversation(t, s, Spec{MaxTurns: 5})

	recaller := &stubRecaller{}
	run := submitWithMemory(t, s, session.ID, memory.SelectionRecall, recaller)

	snapshot := decodeRunSnapshot(t, s, run.ID)
	if snapshot.MemoryMode != contextbuilder.MemoryEmpty {
		t.Fatalf("MemoryMode = %q, want Empty", snapshot.MemoryMode)
	}
	if len(snapshot.Materials.Memory) != 0 {
		t.Fatalf("Empty snapshot carries %d materials", len(snapshot.Materials.Memory))
	}
}

func TestSubmitOffNeverRecalls(t *testing.T) {
	s := testStore(t)
	session := testConversation(t, s, Spec{MaxTurns: 5})

	recaller := &stubRecaller{err: errors.New("must not be called")}
	run := submitWithMemory(t, s, session.ID, memory.SelectionOff, recaller)
	if recaller.called {
		t.Fatal("recaller was called for an Off submission")
	}

	snapshot := decodeRunSnapshot(t, s, run.ID)
	if snapshot.MemoryMode != contextbuilder.MemoryOff {
		t.Fatalf("MemoryMode = %q, want Off", snapshot.MemoryMode)
	}
}

func TestSubmitRecallFailureRollsBack(t *testing.T) {
	s := testStore(t)
	session := testConversation(t, s, Spec{MaxTurns: 5})

	recaller := &stubRecaller{err: memory.ErrUnavailable}
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session.ID, Query: "question"})
	_, _, err := s.Accept(context.Background(), execution.SubmitCommand{
		Owner:       "owner",
		RequestKey:  NewID(),
		RequestHash: Hash(input),
		TTL:         time.Hour,
		MaxAttempts: 3,
		Kind:        execution.SubmitChat,
		SessionID:   session.ID,
		Query:       "question",
		Memory:      memory.SelectionRecall,
	}, PrepareChatSubmissionWith(recaller))
	if !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Accept error = %v, want ErrUnavailable", err)
	}

	runs, err := s.SessionRuns(context.Background(), "owner", session.ID, "", 10)
	if err != nil {
		t.Fatalf("SessionRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("failed recall left %d runs behind", len(runs))
	}
}

func TestSubmitRecallWithoutRecallerRejected(t *testing.T) {
	s := testStore(t)
	session := testConversation(t, s, Spec{MaxTurns: 5})

	input, _ := json.Marshal(struct{ Session, Query string }{Session: session.ID, Query: "question"})
	_, _, err := s.Accept(context.Background(), execution.SubmitCommand{
		Owner:       "owner",
		RequestKey:  NewID(),
		RequestHash: Hash(input),
		TTL:         time.Hour,
		MaxAttempts: 3,
		Kind:        execution.SubmitChat,
		SessionID:   session.ID,
		Query:       "question",
		Memory:      memory.SelectionRecall,
	}, PrepareChatSubmission)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Accept error = %v, want ErrInvalid", err)
	}
}
