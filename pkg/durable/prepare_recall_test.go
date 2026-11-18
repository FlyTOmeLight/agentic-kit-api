package durable

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	scope   memory.Scope
	agentID string
	filter  memory.RecallFilter
}

func (s *stubRecaller) RecallTx(
	_ context.Context,
	_ *gorm.DB,
	scope memory.Scope,
	agentID string,
	_ string,
	filter memory.RecallFilter,
) ([]memory.Entry, error) {
	s.called = true
	s.scope = scope
	s.agentID = agentID
	s.filter = filter
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
	cfg, agentID, version := sessionPublishedConfigFixture(t, s, session)
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
	}, PrepareChatSubmission(cfg, agentID, version, recaller))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return r
}

// sessionPublishedConfigFixture resolves the published config body for a
// fixture session, mirroring the production prefetch outside the accept
// transaction.
func sessionPublishedConfigFixture(t *testing.T, s *Store, sessionID string) (AgentConfig, string, int64) {
	t.Helper()
	agentID, version := sessionPublishedFixture(t, s, sessionID)
	return fixtureVersionConfig(t, agentID, version), agentID, version
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
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})

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
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})

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
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})

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
	session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
	agentID, version := sessionPublishedFixture(t, s, session.ID)

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
	}, PrepareChatSubmission(cfg, agentID, version, recaller))
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

func TestSubmitRecallPassesFilter(t *testing.T) {
	s := testStore(t)
	session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
	agentID, version := sessionPublishedFixture(t, s, session.ID)

	recaller := &stubRecaller{}
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session.ID, Query: "question"})
	filter := memory.RecallFilter{Terms: []string{"editor"}, MaxEntries: 2, MaxBytes: 512}
	_, _, err := s.Accept(context.Background(), execution.SubmitCommand{
		Owner:        "owner",
		RequestKey:   NewID(),
		RequestHash:  Hash(input),
		TTL:          time.Hour,
		MaxAttempts:  3,
		Kind:         execution.SubmitChat,
		SessionID:    session.ID,
		Query:        "question",
		Memory:       memory.SelectionRecall,
		MemoryFilter: filter,
	}, PrepareChatSubmission(cfg, agentID, version, recaller))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !recaller.called ||
		!slices.Equal(recaller.filter.Terms, filter.Terms) ||
		recaller.filter.MaxEntries != filter.MaxEntries ||
		recaller.filter.MaxBytes != filter.MaxBytes {
		t.Fatalf("recaller filter = %+v, want %+v", recaller.filter, filter)
	}
}

func TestSubmitRecallWithoutRecallerRejected(t *testing.T) {
	s := testStore(t)
	session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
	agentID, version := sessionPublishedFixture(t, s, session.ID)

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
	}, PrepareChatSubmission(cfg, agentID, version, nil))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Accept error = %v, want ErrInvalid", err)
	}
}

func submitApplicationWithMemory(
	t *testing.T,
	s *Store,
	owner string,
	spec execution.ExecutionSpec,
	selection memory.Selection,
	recaller MemoryRecaller,
) *Run {
	t.Helper()
	cfg := fixtureVersionConfig(t, spec.AgentID, spec.AgentVersion)
	input, _ := json.Marshal(struct {
		Spec   execution.ExecutionSpec
		Memory memory.Selection
	}{Spec: spec, Memory: selection})
	r, _, err := s.Accept(context.Background(), execution.SubmitCommand{
		Owner:         owner,
		RequestKey:    NewID(),
		RequestHash:   Hash(input),
		TTL:           time.Hour,
		MaxAttempts:   5,
		Kind:          execution.SubmitApplication,
		ExecutionSpec: spec,
		Memory:        selection,
	}, PrepareApplicationSubmission(cfg, spec.AgentID, spec.AgentVersion, recaller))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return r
}

func publishTestAgent(t *testing.T, s *Store, owner string) string {
	t.Helper()
	a := createAgentFixture(t, s, owner, "app", AgentConfig{
		SystemPrompt: "v1",
		Model:        ModelSpec{Name: "local", BaseURL: "http://model.test/v1"},
		MaxTurns:     5,
	})
	publishAgentFixture(t, s, owner, a.ID, a.Revision)
	return a.ID
}

func TestApplicationRecallDerivesScopeFromTrustedWorkspace(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		want  memory.Scope
	}{
		{
			name:  "tenant and actor select actor private",
			scope: Scope{TenantID: "t", ActorID: "u"},
			want:  memory.Scope{Kind: memory.ScopeActorPrivate, Owner: "app", Tenant: "t", Actor: "u"},
		},
		{
			name:  "tenant project and actor select actor project",
			scope: Scope{TenantID: "t", ProjectID: "p", ActorID: "u"},
			want: memory.Scope{
				Kind:    memory.ScopeActorProject,
				Owner:   "app",
				Tenant:  "t",
				Project: "p",
				Actor:   "u",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			agentID := publishTestAgent(t, s, "app")
			recaller := &stubRecaller{}
			scope := tc.scope
			scope.TaskID = "task"
			scope.RoundID = "round"
			submitApplicationWithMemory(t, s, "app", execution.ExecutionSpec{
				AgentID:      agentID,
				AgentVersion: 1,
				Query:        "request",
				Scope:        &scope,
			}, memory.SelectionRecall, recaller)
			if !recaller.called {
				t.Fatal("recaller was not called for a Recall submission")
			}
			if recaller.scope != tc.want {
				t.Fatalf("recall scope = %+v, want %+v", recaller.scope, tc.want)
			}
			if recaller.agentID != agentID {
				t.Fatalf("recall agentID = %q, want %q", recaller.agentID, agentID)
			}
		})
	}
}

func TestChatRecallStaysOwnerPrivate(t *testing.T) {
	s := testStore(t)
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})

	recaller := &stubRecaller{}
	submitWithMemory(t, s, session.ID, memory.SelectionRecall, recaller)
	want := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
	if recaller.scope != want {
		t.Fatalf("recall scope = %+v, want %+v", recaller.scope, want)
	}
}
