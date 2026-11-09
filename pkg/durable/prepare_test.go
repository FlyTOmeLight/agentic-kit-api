package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func TestFreezeContextDefaultAndModelWindowBudgets(t *testing.T) {
	defaultSnap, err := freezeContext(execution.Spec{
		SystemPrompt: "you are helpful",
		Query:        "hello",
	}, nil)
	if err != nil {
		t.Fatalf("default freezeContext: %v", err)
	}
	decodedDefault, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        defaultSnap.Format,
		PolicyVersion: defaultSnap.PolicyVersion,
		Digest:        defaultSnap.Digest,
		Payload:       defaultSnap.Payload,
	})
	if err != nil {
		t.Fatalf("decode default snapshot: %v", err)
	}
	if decodedDefault.Budget.MaxInputBytes != 128*1024 || decodedDefault.Budget.MaxToolResultBytes != 16*1024 {
		t.Fatalf("unexpected default budget: %+v", decodedDefault.Budget)
	}
	if decodedDefault.Policy.Version != contextbuilder.PolicyVersionV3 ||
		decodedDefault.Policy.KeepRecentToolResults != contextbuilder.DefaultKeepRecentToolResults {
		t.Fatalf("new run policy = %+v, want context-v3 with %d recent results",
			decodedDefault.Policy, contextbuilder.DefaultKeepRecentToolResults)
	}

	maxTokens := 1000
	windowSnap, err := freezeContext(execution.Spec{
		SystemPrompt: "you are helpful",
		Query:        "hello",
		Model: agent.ModelSpec{
			Name:          "gpt-test",
			ContextWindow: 5000,
			MaxTokens:     &maxTokens,
		},
	}, nil)
	if err != nil {
		t.Fatalf("window freezeContext: %v", err)
	}
	decodedWindow, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format:        windowSnap.Format,
		PolicyVersion: windowSnap.PolicyVersion,
		Digest:        windowSnap.Digest,
		Payload:       windowSnap.Payload,
	})
	if err != nil {
		t.Fatalf("decode window snapshot: %v", err)
	}
	if decodedWindow.Budget.ContextWindow != 5000 ||
		decodedWindow.Budget.OutputReserve != 1000 ||
		decodedWindow.Budget.CounterVersion != contextbuilder.ByteEstimateVersion ||
		decodedWindow.Budget.ProtocolOverheadTokens != 256 ||
		decodedWindow.Budget.MaxToolResultBytes != 1000 {
		t.Fatalf("unexpected model window budget: %+v", decodedWindow.Budget)
	}
}

func TestFreezeContextRejectsPreflightBudgetExceeded(t *testing.T) {
	maxTokens := 800
	_, err := freezeContext(execution.Spec{
		SystemPrompt: strings.Repeat("system instruction ", 30),
		Query:        strings.Repeat("user query ", 30),
		Model: agent.ModelSpec{
			Name:          "gpt-test",
			ContextWindow: 1200,
			MaxTokens:     &maxTokens,
		},
	}, nil)
	if !errors.Is(err, contextbuilder.ErrBudgetExceeded) {
		t.Fatalf("freezeContext error = %v, want ErrBudgetExceeded", err)
	}
}

func TestSubmitBudgetFailureLeavesNoRunOrMessages(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	maxTokens := 100
	session := testConversation(t, store, execution.Spec{
		Model: agent.ModelSpec{ContextWindow: 1000, MaxTokens: &maxTokens},
	})
	_, _, err := store.Submit(ctx, "owner", session.ID, "budget-key", strings.Repeat("large", 300), time.Hour, 3)
	if !errors.Is(err, contextbuilder.ErrBudgetExceeded) {
		t.Fatalf("submit: %v", err)
	}
	for _, model := range []any{&table.Run{}, &table.Message{}, &table.Event{}} {
		var count int64
		if err := store.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d error=%v", model, count, err)
		}
	}
	stored, err := store.Session(ctx, "owner", session.ID)
	if err != nil || stored.MessageSeq != 0 {
		t.Fatalf("session changed: %+v, %v", stored, err)
	}
	if _, created, err := store.Submit(
		ctx,
		"owner",
		session.ID,
		"budget-key",
		"small",
		time.Hour,
		3,
	); err != nil ||
		!created {
		t.Fatalf("budget rejection consumed idempotency key: created=%v error=%v", created, err)
	}
}

func TestHistoryToolIDsAreScopedByRun(t *testing.T) {
	messages := []execution.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", ToolCalls: []execution.ToolCall{{ID: "turn_1_tool_0", Name: "read"}}},
		{Role: "tool", ToolCallID: "turn_1_tool_0", Content: "result"},
		{Role: "assistant", Content: "answer"},
	}
	first, ok := completeHistoryGroup("first", messages)
	if !ok {
		t.Fatal("first group rejected")
	}
	second, ok := completeHistoryGroup("second", messages)
	if !ok {
		t.Fatal("second group rejected")
	}
	if first.Messages[1].ToolCalls[0].ID == second.Messages[1].ToolCalls[0].ID ||
		first.Messages[1].ToolCalls[0].ID != first.Messages[2].ToolCallID {
		t.Fatal("history tool IDs are not scoped and paired")
	}
	_, err := contextbuilder.Build(contextbuilder.BuildInput{
		FrozenMaterials: contextbuilder.FrozenMaterials{History: []contextbuilder.MessageGroup{first, second}},
		CurrentTurn: []contextbuilder.Message{
			{Role: contextbuilder.RoleUser, Content: "next"},
			{
				Role:      contextbuilder.RoleAssistant,
				ToolCalls: []contextbuilder.ToolCall{{ID: "turn_1_tool_0", Name: "read"}},
			},
			{Role: contextbuilder.RoleTool, ToolCallID: "turn_1_tool_0", Content: "current result"},
		},
		Policy: contextbuilder.Policy{Version: contextbuilder.PolicyVersionV2},
	})
	if err != nil {
		t.Fatalf("repeated step IDs across turns: %v", err)
	}
	if messages[1].ToolCalls[0].ID != "turn_1_tool_0" || messages[2].ToolCallID != "turn_1_tool_0" {
		t.Fatal("history mapping mutated stored messages")
	}
}
