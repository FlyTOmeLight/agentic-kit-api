package contextbuilder

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type byteTokenCounter struct{}

func (byteTokenCounter) Count(text string) int64 { return int64(len(text)) }

func mustBuild(t *testing.T, in BuildInput) BuildResult {
	t.Helper()
	result, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return result
}

func historyGroup(id string, messages ...Message) MessageGroup {
	return MessageGroup{
		Source:   Source{Kind: SourceHistory, ID: id},
		Messages: messages,
	}
}

func messagesMeasure(messages []Message) measurement {
	var total measurement
	for _, message := range messages {
		total.add(measureText(byteTokenCounter{}, messageText(message)))
	}
	return total
}

func TestBuildTrimsOldestCompleteGroup(t *testing.T) {
	first := historyGroup(
		"run-1",
		Message{Role: RoleUser, Content: "question one"},
		Message{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup", Arguments: `{"q":"one"}`}},
		},
		Message{Role: RoleTool, ToolCallID: "call-1", Content: "result one"},
		Message{Role: RoleAssistant, Content: "answer one"},
	)
	second := historyGroup("run-2",
		Message{Role: RoleUser, Content: "question two"},
		Message{Role: RoleAssistant, Content: "answer two"},
	)
	third := historyGroup("run-3",
		Message{Role: RoleUser, Content: "question three"},
		Message{Role: RoleAssistant, Content: "answer three"},
	)
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			SystemInstruction: "system",
			History:           []MessageGroup{first, second, third},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "question four"}},
		Policy:      Policy{Version: PolicyVersionV1},
		Budget:      Budget{CounterVersion: "test-v1"},
		Counter:     byteTokenCounter{},
	}
	all := mustBuild(t, in)
	firstMeasure := messagesMeasure(first.Messages)

	in.Budget.MaxInputTokens = all.Usage.InputTokens - firstMeasure.tokens
	result := mustBuild(t, in)

	if len(result.OmittedSources) != 1 || result.OmittedSources[0].Source.ID != "run-1" {
		t.Fatalf("omitted %+v, want run-1", result.OmittedSources)
	}
	for _, message := range result.Messages {
		if message.ToolCallID == "call-1" || len(message.ToolCalls) > 0 {
			t.Fatalf("removed group leaked tool message %+v", message)
		}
	}
	if result.Messages[0].Role != RoleSystem || result.Messages[0].Content != "system" {
		t.Fatalf("system message %+v", result.Messages[0])
	}
	if result.Usage.InputTokens != in.Budget.MaxInputTokens {
		t.Fatalf("tokens %d, want %d", result.Usage.InputTokens, in.Budget.MaxInputTokens)
	}
}

func TestBuildTrimsLowestPriorityMemoryFirst(t *testing.T) {
	high := ContextBlock{
		Source:   Source{Kind: SourceMemory, ID: "mem-high", Revision: 3, Version: "v3"},
		Content:  "important preference",
		Priority: 10,
	}
	low := ContextBlock{
		Source:   Source{Kind: SourceMemory, ID: "mem-low", Revision: 1, Version: "v1"},
		Content:  "noise",
		Priority: 1,
	}
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			SystemInstruction: "system",
			Memory:            []ContextBlock{low, high},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
		Policy:      Policy{Version: PolicyVersionV1},
		Budget:      Budget{CounterVersion: "test-v1"},
		Counter:     byteTokenCounter{},
	}
	highOnly := in
	highOnly.FrozenMaterials.Memory = []ContextBlock{high}
	highUsage := mustBuild(t, highOnly)

	in.Budget.MaxInputTokens = highUsage.Usage.InputTokens
	result := mustBuild(t, in)

	if len(result.OmittedSources) != 1 || result.OmittedSources[0].Source.ID != "mem-low" {
		t.Fatalf("omitted %+v, want mem-low", result.OmittedSources)
	}
	if result.Messages[0].Role != RoleSystem {
		t.Fatalf("memory must be system material, got %+v", result.Messages[0])
	}
	if !strings.Contains(result.Messages[0].Content, "important preference") ||
		strings.Contains(result.Messages[0].Content, "noise") {
		t.Fatalf("system content %q", result.Messages[0].Content)
	}
}

func TestBuildRejectsRequiredInputOverBudget(t *testing.T) {
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			SystemInstruction: "system",
			Tools: []ToolDefinition{{
				Name:        "lookup",
				Description: "look up one record",
				Parameters:  []byte(`{"type":"object"}`),
			}},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
		Policy:      Policy{Version: PolicyVersionV1},
		Budget:      Budget{CounterVersion: "test-v1"},
		Counter:     byteTokenCounter{},
	}
	all := mustBuild(t, in)
	in.Budget.MaxInputTokens = all.Usage.InputTokens - 1

	_, err := Build(in)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("error %v, want ErrBudgetExceeded", err)
	}
	var exceeded *BudgetExceededError
	if !errors.As(err, &exceeded) || exceeded.Code() != "ContextBudgetExceeded" {
		t.Fatalf("error shape %#v", err)
	}
}

func TestBuildRejectsIncompleteToolPairing(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
	}{
		{
			name: "orphan tool result",
			messages: []Message{
				{Role: RoleTool, ToolCallID: "missing", Content: "result"},
			},
		},
		{
			name: "missing tool result",
			messages: []Message{
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}}},
			},
		},
		{
			name: "user message before tool result",
			messages: []Message{
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}}},
				{Role: RoleUser, Content: "interrupt"},
				{Role: RoleTool, ToolCallID: "call-1", Content: "result"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := BuildInput{
				CurrentTurn: test.messages,
				Policy:      Policy{Version: PolicyVersionV1},
				Budget:      Budget{CounterVersion: "test-v1"},
				Counter:     byteTokenCounter{},
			}
			if _, err := Build(in); !errors.Is(err, ErrIncompleteGroup) {
				t.Fatalf("error %v, want ErrIncompleteGroup", err)
			}
		})
	}
}

func TestBuildRejectsEmptyCurrentTurn(t *testing.T) {
	in := BuildInput{
		Policy: Policy{Version: PolicyVersionV1},
	}
	if _, err := Build(in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error %v, want ErrInvalidInput", err)
	}
}

func TestBuildRejectsDuplicateToolCallIDs(t *testing.T) {
	group := func(id string) MessageGroup {
		return historyGroup(id,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}}},
			Message{Role: RoleTool, ToolCallID: "call-1", Content: "result"},
		)
	}
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			History: []MessageGroup{group("run-1"), group("run-2")},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
		Policy:      Policy{Version: PolicyVersionV1},
		Budget:      Budget{CounterVersion: "test-v1"},
		Counter:     byteTokenCounter{},
	}
	if _, err := Build(in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error %v, want ErrInvalidInput", err)
	}
}

func TestBuildDoesNotMutateInputs(t *testing.T) {
	high := ContextBlock{Source: Source{Kind: SourceMemory, ID: "mem-high"}, Content: "high", Priority: 10}
	low := ContextBlock{Source: Source{Kind: SourceMemory, ID: "mem-low"}, Content: "low", Priority: 1}
	group := historyGroup("run-1",
		Message{Role: RoleUser, Content: "question"},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}}},
		Message{Role: RoleTool, ToolCallID: "call-1", Content: "result"},
	)
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{Memory: []ContextBlock{low, high}, History: []MessageGroup{group}},
		CurrentTurn:     []Message{{Role: RoleUser, Content: "next"}},
		Policy:          Policy{Version: PolicyVersionV1},
		Budget:          Budget{CounterVersion: "test-v1"},
		Counter:         byteTokenCounter{},
	}
	before := BuildInput{
		FrozenMaterials: FrozenMaterials{
			Memory:  append([]ContextBlock(nil), in.FrozenMaterials.Memory...),
			History: []MessageGroup{group},
		},
		CurrentTurn: append([]Message(nil), in.CurrentTurn...),
		Policy:      in.Policy,
		Budget:      in.Budget,
		Counter:     in.Counter,
	}
	result := mustBuild(t, in)

	for _, message := range result.Messages {
		if len(message.ToolCalls) > 0 {
			message.ToolCalls[0].Name = "changed"
		}
	}

	if !reflect.DeepEqual(in.FrozenMaterials.Memory, before.FrozenMaterials.Memory) {
		t.Fatalf("memory mutated: %+v", in.FrozenMaterials.Memory)
	}
	if in.FrozenMaterials.History[0].Messages[1].ToolCalls[0].Name != "lookup" {
		t.Fatalf("history tool call mutated: %+v", in.FrozenMaterials.History[0].Messages[1])
	}
}

func TestBuildHonorsByteLimitWithoutTokenCounter(t *testing.T) {
	first := historyGroup("run-1",
		Message{Role: RoleUser, Content: "old question"},
		Message{Role: RoleAssistant, Content: "old answer"},
	)
	second := historyGroup("run-2",
		Message{Role: RoleUser, Content: "new question"},
		Message{Role: RoleAssistant, Content: "new answer"},
	)
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{SystemInstruction: "system", History: []MessageGroup{first, second}},
		CurrentTurn:     []Message{{Role: RoleUser, Content: "next"}},
		Policy:          Policy{Version: PolicyVersionV1},
	}
	all := mustBuild(t, in)
	in.Budget.MaxInputBytes = all.Usage.InputBytes - messagesMeasure(first.Messages).bytes

	result := mustBuild(t, in)
	if len(result.OmittedSources) != 1 || result.OmittedSources[0].Source.ID != "run-1" {
		t.Fatalf("omitted %+v, want run-1", result.OmittedSources)
	}
	if result.Usage.ByteLimit != in.Budget.MaxInputBytes {
		t.Fatalf("byte limit %d, want %d", result.Usage.ByteLimit, in.Budget.MaxInputBytes)
	}
}

func TestBuildCounterRequirements(t *testing.T) {
	base := BuildInput{
		CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
		Policy:      Policy{Version: PolicyVersionV1},
		Budget:      Budget{MaxInputTokens: 100},
	}
	if _, err := Build(base); !errors.Is(err, ErrCounterRequired) {
		t.Fatalf("missing counter: %v", err)
	}
	base.Counter = byteTokenCounter{}
	if _, err := Build(base); !errors.Is(err, ErrCounterVersionRequired) {
		t.Fatalf("missing counter version: %v", err)
	}
}

func TestBuildRejectsUnsupportedPolicy(t *testing.T) {
	for _, policy := range []Policy{{}, {Version: "context-v9"}} {
		in := BuildInput{
			CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
			Policy:      policy,
		}
		if _, err := Build(in); !errors.Is(err, ErrUnsupportedPolicy) {
			t.Fatalf("policy %+v: error %v, want ErrUnsupportedPolicy", policy, err)
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	high := ContextBlock{Source: Source{Kind: SourceMemory, ID: "mem-b"}, Content: "b", Priority: 5}
	low := ContextBlock{Source: Source{Kind: SourceMemory, ID: "mem-a"}, Content: "a", Priority: 5}
	group := historyGroup("run-1",
		Message{Role: RoleUser, Content: "question"},
		Message{Role: RoleAssistant, Content: "answer"},
	)
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			SystemInstruction: "system",
			Memory:            []ContextBlock{high, low},
			History:           []MessageGroup{group},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "next"}},
		Policy:      Policy{Version: PolicyVersionV1},
		Budget:      Budget{MaxInputTokens: 200, CounterVersion: "test-v1"},
		Counter:     byteTokenCounter{},
	}
	first := mustBuild(t, in)
	second := mustBuild(t, in)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ:\n%+v\n%+v", first, second)
	}
}

func TestBuildPreviewsOversizedToolResults(t *testing.T) {
	largeOutput := strings.Repeat("结果片段αβγ-", 40)
	historyTurn := historyGroup("run-1",
		Message{Role: RoleUser, Content: "first question"},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup", Arguments: `{}`}}},
		Message{Role: RoleTool, ToolCallID: "call-1", Content: largeOutput},
		Message{Role: RoleAssistant, Content: "first answer"},
	)
	currentTurn := []Message{
		{Role: RoleUser, Content: "second question"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-2", Name: "lookup", Arguments: `{}`}}},
		{Role: RoleTool, ToolCallID: "call-2", Content: largeOutput},
	}
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			SystemInstruction: "system",
			History:           []MessageGroup{historyTurn},
		},
		CurrentTurn: currentTurn,
		Policy:      Policy{Version: PolicyVersionV1},
		Budget: Budget{
			MaxInputTokens:     900,
			MaxToolResultBytes: 200,
			CounterVersion:     ByteEstimateVersion,
		},
		Counter: byteTokenCounter{},
	}

	result := mustBuild(t, in)
	previewCount := 0
	for _, msg := range result.Messages {
		if msg.Role != RoleTool {
			continue
		}
		if int64(len(msg.Content)) > in.Budget.MaxToolResultBytes {
			t.Fatalf(
				"tool content length %d exceeds limit %d",
				len(msg.Content),
				in.Budget.MaxToolResultBytes,
			)
		}
		if !strings.Contains(msg.Content, "[Tool result preview: middle omitted;") {
			t.Fatalf("expected preview marker in %q", msg.Content)
		}
		if !utf8.ValidString(msg.Content) {
			t.Fatalf("preview split UTF-8 rune: %q", msg.Content)
		}
		previewCount++
	}
	if previewCount != 2 {
		t.Fatalf("previewed %d tool results, want 2", previewCount)
	}
	if historyTurn.Messages[2].Content != largeOutput || currentTurn[2].Content != largeOutput {
		t.Fatal("previewToolMessages mutated input slices")
	}
}

func TestResolveCounter(t *testing.T) {
	c, err := ResolveCounter("")
	if err != nil || c != nil {
		t.Fatalf("empty counter version: got (%v, %v), want (nil, nil)", c, err)
	}
	c, err = ResolveCounter(ByteEstimateVersion)
	if err != nil || c == nil || c.Count("hello") != 5 {
		t.Fatalf("ByteEstimateVersion: got (%v, %v)", c, err)
	}
	if _, err := ResolveCounter("unknown-v1"); !errors.Is(err, ErrUnsupportedCounter) {
		t.Fatalf("unknown counter version: got %v, want ErrUnsupportedCounter", err)
	}
}
