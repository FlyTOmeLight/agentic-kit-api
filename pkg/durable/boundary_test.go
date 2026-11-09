package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCommitBoundaryRejectsInvalidCombinations(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "a")
	valid := json.RawMessage(`{}`)
	cases := []struct {
		name string
		b    Boundary
	}{
		{name: "unknown kind", b: Boundary{Kind: "Progress", State: valid}},
		{name: "missing state", b: Boundary{Kind: BoundaryModelStarted}},
		{name: "invalid state", b: Boundary{Kind: BoundaryModelStarted, State: json.RawMessage(`{`)}},
		{name: "model started with output", b: Boundary{Kind: BoundaryModelStarted, State: valid, Output: "x"}},
		{name: "model started with detail", b: Boundary{Kind: BoundaryModelStarted, State: valid, Detail: "x"}},
		{
			name: "model started with step",
			b:    Boundary{Kind: BoundaryModelStarted, State: valid, WaitingStep: "s"},
		},
		{name: "complete with detail", b: Boundary{Kind: BoundaryComplete, State: valid, Detail: "x"}},
		{name: "complete with step", b: Boundary{Kind: BoundaryComplete, State: valid, WaitingStep: "s"}},
		{name: "fail with output", b: Boundary{Kind: BoundaryFail, State: valid, Output: "x", Detail: "d"}},
		{name: "fail without detail", b: Boundary{Kind: BoundaryFail, State: valid}},
		{name: "fail with step", b: Boundary{Kind: BoundaryFail, State: valid, Detail: "d", WaitingStep: "s"}},
		{
			name: "clarification with output",
			b: Boundary{
				Kind:        BoundaryAwaitClarification,
				State:       valid,
				Output:      "x",
				Detail:      "d",
				WaitingStep: "s",
			},
		},
		{
			name: "clarification without detail",
			b:    Boundary{Kind: BoundaryAwaitClarification, State: valid, WaitingStep: "s"},
		},
		{
			name: "clarification without step",
			b:    Boundary{Kind: BoundaryAwaitClarification, State: valid, Detail: "d"},
		},
		{name: "review without detail", b: Boundary{Kind: BoundaryAwaitReview, State: valid, WaitingStep: "s"}},
	}
	before, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CommitBoundary(ctx, r.Lease(), tc.b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v want ErrInvalid", err)
			}
		})
	}
	after, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.EventSeq != before.EventSeq ||
		string(after.State) != string(before.State) ||
		after.WaitingStep != "" ||
		after.WaitingKind != "" {
		t.Fatalf("invalid boundary wrote state: %+v", after)
	}
	events, err := s.Events(ctx, "owner", r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(events)) != before.EventSeq {
		t.Fatalf("events=%d want %d", len(events), before.EventSeq)
	}
}

func TestCommitBoundaryDerivesStatusMessageEvent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	steps := []struct {
		name        string
		boundary    Boundary
		status      Status
		messageKind string
		event       string
	}{
		{
			name:     "model started",
			boundary: Boundary{Kind: BoundaryModelStarted, State: json.RawMessage(`{}`)},
			status:   Running,
			event:    "ModelStarted",
		},
		{
			name: "clarification",
			boundary: Boundary{
				Kind:        BoundaryAwaitClarification,
				State:       json.RawMessage(`{}`),
				Detail:      "q",
				WaitingStep: "ask",
			},
			status: WaitingClarification,
			event:  "ClarificationRequested",
		},
		{
			name:     "complete",
			boundary: Boundary{Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "done"},
			status:   Completed,
			event:    "RunCompleted",

			messageKind: "Final",
		},
		{
			name:     "fail",
			boundary: Boundary{Kind: BoundaryFail, State: json.RawMessage(`{}`), Detail: "boom"},
			status:   Failed,
			event:    "RunFailed",
		},
	}
	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			session := testConversation(t, s, Spec{MaxTurns: 5})
			r, _, err := s.Submit(ctx, "owner", session.ID, NewID(), "question", time.Hour, 3)
			if err != nil {
				t.Fatal(err)
			}
			claimed := claim(t, s, "a")
			if _, err = s.BeginStep(
				ctx,
				claimed.Lease(),
				StepIntent{ID: "ask", Input: "q", Policy: ReadOnly},
			); err != nil {
				t.Fatal(err)
			}
			if err = s.CommitBoundary(ctx, claimed.Lease(), tc.boundary); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(ctx, "owner", r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.status || got.Output != tc.boundary.Output ||
				got.Error != tc.boundary.Detail ||
				got.WaitingStep != tc.boundary.WaitingStep ||
				got.WaitingKind != tc.boundary.Kind.Interaction() {
				t.Fatalf("run=%+v", got)
			}
			events, err := s.Events(ctx, "owner", r.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Kind != tc.event {
				t.Fatalf("event=%s want %s", last.Kind, tc.event)
			}
			var payload struct {
				Status      Status
				WaitingStep string
				WaitingKind InteractionKind
			}
			if err = json.Unmarshal(last.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Status != tc.status || payload.WaitingStep != tc.boundary.WaitingStep ||
				payload.WaitingKind != tc.boundary.Kind.Interaction() {
				t.Fatalf("payload=%+v", payload)
			}
			if tc.messageKind != "" {
				messages, err := s.Messages(ctx, "owner", got.SessionID, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				if messages[len(messages)-1].Kind != tc.messageKind {
					t.Fatalf(
						"message kind=%s want %s",
						messages[len(messages)-1].Kind,
						tc.messageKind,
					)
				}
			}
		})
	}
}

func TestRecordDecisionRejectsInvalidInput(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "a")
	valid := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "t", Name: "lookup", Arguments: `{}`}}}
	cases := []struct {
		name    string
		state   json.RawMessage
		message Message
	}{
		{name: "invalid state", state: json.RawMessage(`{`), message: valid},
		{
			name:    "wrong role",
			state:   json.RawMessage(`{}`),
			message: Message{Role: "tool", ToolCalls: valid.ToolCalls},
		},
		{name: "no tool calls", state: json.RawMessage(`{}`), message: Message{Role: "assistant"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.RecordDecision(ctx, r.Lease(), tc.state, tc.message); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v want ErrInvalid", err)
			}
		})
	}
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := s.Messages(ctx, "owner", got.SessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("invalid decision wrote messages: %+v", messages)
	}
}
