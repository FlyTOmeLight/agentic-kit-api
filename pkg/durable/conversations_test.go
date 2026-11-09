package durable

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
)

func TestPublishedSnapshotsAndConversationHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session := testConversation(t, s, Spec{SystemPrompt: "version one", MaxTurns: 5})
	first, _, err := s.Submit(ctx, "owner", session.ID, "first", "first question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.Agent(ctx, "owner", session.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	changed := agent.Draft
	changed.SystemPrompt = "version two"
	changed.Model.Name = "new-model"
	updated, err := s.UpdateAgent(ctx, "owner", agent.ID, "new name", agent.Revision, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := updated.UpdatedAt.Zone(); offset != 0 {
		t.Fatal("timestamps must use UTC")
	}
	if updated.Revision != 2 || updated.Draft.SystemPrompt != "version two" {
		t.Fatalf("updated: %+v", updated)
	}
	if _, err = s.UpdateAgent(
		ctx,
		"owner",
		agent.ID,
		"stale",
		agent.Revision,
		agent.Draft,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale draft accepted: %v", err)
	}
	if _, err = s.PublishAgent(ctx, "owner", agent.ID, agent.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publish accepted: %v", err)
	}
	version, err := s.PublishAgent(ctx, "owner", agent.ID, updated.Revision)
	if err != nil || version.Number != 2 {
		t.Fatalf("publish: %+v %v", version, err)
	}
	repeat, created, err := s.Submit(ctx, "owner", session.ID, "first", "first question", time.Hour, 3)
	if err != nil || created || repeat.ID != first.ID || repeat.Spec.SystemPrompt != "version one" ||
		repeat.ConfigVersion != "1" {
		t.Fatalf("replay changed snapshot: %+v %v", repeat, err)
	}
	if repeat.ContextSnapshot.Digest != first.ContextSnapshot.Digest ||
		string(repeat.ContextSnapshot.Payload) != string(first.ContextSnapshot.Payload) {
		t.Fatal("replay changed context snapshot")
	}
	if _, _, err = s.Submit(
		ctx,
		"owner",
		session.ID,
		"first",
		"changed input",
		time.Hour,
		3,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("changed retry accepted: %v", err)
	}
	running := claim(t, s, "worker")
	if running.Spec.SystemPrompt != "version one" {
		t.Fatal("queued run adopted draft")
	}
	decision := Message{
		Role:      "assistant",
		Content:   "searching",
		ToolCalls: []ToolCall{{ID: "search", Name: "lookup", Arguments: `{}`}},
	}
	if err = s.RecordDecision(ctx, running.Lease(), json.RawMessage(`{"version":1}`), decision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginStep(
		ctx,
		running.Lease(),
		StepIntent{ID: "search", Input: "input", Policy: ReadOnly},
	); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishStep(
		ctx,
		running.Lease(),
		"search",
		"tool result",
		json.RawMessage(`{"version":1}`),
	); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitBoundary(
		ctx,
		running.Lease(),
		Boundary{Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "first answer"},
	); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Submit(ctx, "owner", session.ID, "second", "follow up", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if second.Spec.SystemPrompt != "version two" || second.ConfigVersion != "2" || len(second.Spec.History) != 2 ||
		second.Spec.History[0].Content != "first question" ||
		second.Spec.History[1].Content != "first answer" {
		t.Fatalf("bad second turn: %+v", second.Spec)
	}
	snapshot, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        second.ContextSnapshot.Format,
		PolicyVersion: second.ContextSnapshot.PolicyVersion,
		Digest:        second.ContextSnapshot.Digest,
		Payload:       second.ContextSnapshot.Payload,
	})
	if err != nil {
		t.Fatalf("decode context snapshot: %v", err)
	}
	if snapshot.MemoryMode != contextbuilder.MemoryOff ||
		snapshot.Materials.SystemInstruction != "version two" ||
		len(snapshot.Materials.History) != 1 ||
		len(snapshot.Materials.History[0].Messages) != 4 {
		t.Fatalf("bad context snapshot: %+v", snapshot)
	}
	messages, err := s.Messages(ctx, "owner", session.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 {
		t.Fatalf("messages=%+v", messages)
	}
	for i, kind := range []string{"User", "Assistant", "Tool", "Final", "User"} {
		if messages[i].Seq != int64(i+1) || messages[i].Kind != kind {
			t.Fatalf("message %d: %+v", i, messages[i])
		}
	}
	if messages[1].ToolCalls[0].ID != messages[2].ToolCallID {
		t.Fatal("tool pairing lost")
	}
	page, err := s.Messages(ctx, "owner", session.ID, 2, 2)
	if err != nil || len(page) != 2 || page[0].Seq != 3 || page[1].Seq != 4 {
		t.Fatalf("page: %+v %v", page, err)
	}
	old, err := s.AgentVersion(ctx, "owner", agent.ID, 1)
	if err != nil || old.Config.SystemPrompt != "version one" {
		t.Fatalf("published version changed: %+v %v", old, err)
	}
	if err = s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	read, err := s.Session(ctx, "owner", session.ID)
	if err != nil || read.MessageSeq != 5 {
		t.Fatalf("migration lost session: %+v %v", read, err)
	}
}

func TestConcurrentPublishAndSubmit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session := testConversation(t, s, Spec{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.PublishAgent(ctx, "owner", session.AgentID, 1)
			if err != nil || v.Number != 1 {
				t.Errorf("publish: %+v %v", v, err)
			}
		}()
	}
	wg.Wait()
	var versions int64
	if err := s.db.Model(&table.AgentVersion{}).Count(&versions).Error; err != nil || versions != 1 {
		t.Fatalf("versions=%d err=%v", versions, err)
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.Submit(ctx, "owner", session.ID, "same-key", "same message", time.Hour, 3)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	messages, err := s.Messages(ctx, "owner", session.ID, 0, 100)
	if err != nil || len(messages) != 1 {
		t.Fatalf("duplicate messages: %+v %v", messages, err)
	}
	runs, err := s.SessionRuns(ctx, "owner", session.ID, "", 100)
	if err != nil || len(runs) != 1 {
		t.Fatalf("duplicate runs: %+v %v", runs, err)
	}
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.Submit(ctx, "owner", session.ID, NewID(), "another message", time.Hour, 3)
			if !errors.Is(err, ErrConflict) {
				t.Errorf("active session accepted: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestConversationHistoryKeepsCompleteTurnsOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session := testConversation(t, s, Spec{SystemPrompt: "system"})

	complete, _, err := s.Submit(ctx, "owner", session.ID, "complete", "first question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	running := claim(t, s, "worker")
	if err = s.RecordDecision(ctx, running.Lease(), json.RawMessage(`{"version":1}`), Message{
		Role:      "assistant",
		Content:   "searching",
		ToolCalls: []ToolCall{{ID: "call-complete", Name: "lookup", Arguments: `{}`}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginStep(ctx, running.Lease(), StepIntent{
		ID: "call-complete", Input: "input", Policy: ReadOnly,
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishStep(
		ctx,
		running.Lease(),
		"call-complete",
		"complete result",
		json.RawMessage(`{"version":2}`),
	); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitBoundary(
		ctx,
		running.Lease(),
		Boundary{Kind: BoundaryFail, State: json.RawMessage(`{"version":3}`), Detail: "stopped"},
	); err != nil {
		t.Fatal(err)
	}

	incomplete, _, err := s.Submit(ctx, "owner", session.ID, "incomplete", "second question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	running = claim(t, s, "worker")
	if err = s.RecordDecision(ctx, running.Lease(), json.RawMessage(`{"version":4}`), Message{
		Role:      "assistant",
		Content:   "searching",
		ToolCalls: []ToolCall{{ID: "call-incomplete", Name: "lookup", Arguments: `{}`}},
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(ctx, "owner", incomplete.ID); err != nil {
		t.Fatal(err)
	}

	third, _, err := s.Submit(ctx, "owner", session.ID, "third", "third question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Spec.History) != 0 {
		t.Fatalf("history kept the wrong turns: %+v", third.Spec.History)
	}
	snapshot, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        third.ContextSnapshot.Format,
		PolicyVersion: third.ContextSnapshot.PolicyVersion,
		Digest:        third.ContextSnapshot.Digest,
		Payload:       third.ContextSnapshot.Payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Materials.History) != 1 ||
		snapshot.Materials.History[0].Source.ID != complete.ID ||
		len(snapshot.Materials.History[0].Messages) != 3 {
		t.Fatalf("snapshot groups: %+v", snapshot.Materials.History)
	}
}

func TestConversationOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session := testConversation(t, s, Spec{})
	tests := []struct {
		name string
		read func() error
	}{
		{name: "agent", read: func() error { _, e := s.Agent(ctx, "other", session.AgentID); return e }},
		{
			name: "version",
			read: func() error { _, e := s.AgentVersion(ctx, "other", session.AgentID, 1); return e },
		},
		{
			name: "publish",
			read: func() error { _, e := s.PublishAgent(ctx, "other", session.AgentID, 1); return e },
		},
		{name: "session", read: func() error { _, e := s.Session(ctx, "other", session.ID); return e }},
		{
			name: "create session",
			read: func() error { _, e := s.CreateSession(ctx, "other", session.AgentID, ""); return e },
		},
		{
			name: "messages",
			read: func() error { _, e := s.Messages(ctx, "other", session.ID, 0, 50); return e },
		},
		{
			name: "runs",
			read: func() error { _, e := s.SessionRuns(ctx, "other", session.ID, "", 50); return e },
		},
		{
			name: "submit",
			read: func() error { _, _, e := s.Submit(ctx, "other", session.ID, "key", "question", time.Hour, 3); return e },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.read(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("got %v", err)
			}
		})
	}
	agents, err := s.Agents(ctx, "other", "", 50)
	if err != nil || len(agents) != 0 {
		t.Fatal("agent list leaked", err)
	}
	sessions, err := s.Sessions(ctx, "other", "", "", 50)
	if err != nil || len(sessions) != 0 {
		t.Fatal("session list leaked", err)
	}
}

func TestMessageAndRunRollbackTogether(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session := testConversation(t, s, Spec{})
	err := s.db.Exec(
		`CREATE FUNCTION reject_message() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$; CREATE TRIGGER reject_message BEFORE INSERT ON agent_message FOR EACH ROW EXECUTE FUNCTION reject_message()`,
	).Error
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Submit(ctx, "owner", session.ID, "key", "question", time.Hour, 3); err == nil {
		t.Fatal("expected failure")
	}
	var runs, events int64
	if err = s.db.Model(&table.Run{}).Count(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.Model(&table.Event{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	read, err := s.Session(ctx, "owner", session.ID)
	if err != nil || read.MessageSeq != 0 || runs != 0 || events != 0 {
		t.Fatalf("partial submit: %+v runs=%d events=%d err=%v", read, runs, events, err)
	}
	if err = s.db.Exec(`DROP TRIGGER reject_message ON agent_message`).Error; err != nil {
		t.Fatal(err)
	}
	run, _, err := s.Submit(ctx, "owner", session.ID, "key", "question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	active := claim(t, s, "worker")
	if err = s.db.Exec(
		`CREATE TRIGGER reject_message BEFORE INSERT ON agent_message FOR EACH ROW EXECUTE FUNCTION reject_message()`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.CommitBoundary(
		ctx,
		active.Lease(),
		Boundary{Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "answer"},
	); err == nil {
		t.Fatal("expected final message failure")
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil || got.Status != Running || got.Output != "" || got.EventSeq != 2 {
		t.Fatalf("partial completion: %+v %v", got, err)
	}
	read, err = s.Session(ctx, "owner", session.ID)
	if err != nil || read.MessageSeq != 1 {
		t.Fatalf("message seq changed on rollback: %+v %v", read, err)
	}
}
