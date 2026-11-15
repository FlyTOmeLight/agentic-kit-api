package contextbuilder

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBuildHistoryReductionOrder(t *testing.T) {
	first := historyGroup("first",
		Message{Role: RoleUser, Content: "first question"},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "first-call", Name: "read"}}},
		Message{Role: RoleTool, ToolCallID: "first-call", Content: strings.Repeat("old result ", 100)},
		Message{Role: RoleAssistant, Content: "first answer"},
	)
	second := historyGroup("second",
		Message{Role: RoleUser, Content: "second question"},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "second-call", Name: "read"}}},
		Message{Role: RoleTool, ToolCallID: "second-call", Content: strings.Repeat("new result ", 100)},
		Message{Role: RoleAssistant, Content: "second answer"},
	)
	firstCollapsed := historyGroup("first", first.Messages[0], first.Messages[3])
	secondCollapsed := historyGroup("second", second.Messages[0], second.Messages[3])
	memory := []ContextBlock{{
		Source: Source{Kind: SourceMemory, ID: "memory"}, Content: "reference",
	}}
	tests := []struct {
		name      string
		history   []MessageGroup
		memory    []ContextBlock
		collapsed []Source
		omitted   []OmittedSource
	}{
		{name: "fits without changes", history: []MessageGroup{first, second}, memory: memory},
		{
			name: "memory before history", history: []MessageGroup{first, second},
			omitted: []OmittedSource{{Source: memory[0].Source, Reason: OmitBudget}},
		},
		{
			name: "oldest tool process first", history: []MessageGroup{firstCollapsed, second},
			collapsed: []Source{first.Source},
			omitted:   []OmittedSource{{Source: memory[0].Source, Reason: OmitBudget}},
		},
		{
			name:      "all tool processes before dropping turns",
			history:   []MessageGroup{firstCollapsed, secondCollapsed},
			collapsed: []Source{first.Source, second.Source},
			omitted:   []OmittedSource{{Source: memory[0].Source, Reason: OmitBudget}},
		},
		{
			name: "oldest turn dropped", history: []MessageGroup{secondCollapsed},
			collapsed: []Source{second.Source},
			omitted: []OmittedSource{
				{Source: memory[0].Source, Reason: OmitBudget},
				{Source: first.Source, Reason: OmitBudget},
			},
		},
		{
			name: "all history dropped",
			omitted: []OmittedSource{
				{Source: memory[0].Source, Reason: OmitBudget},
				{Source: first.Source, Reason: OmitBudget},
				{Source: second.Source, Reason: OmitBudget},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := BuildInput{
				FrozenMaterials: FrozenMaterials{
					SystemInstruction: "system",
					History:           []MessageGroup{first, second},
					Memory:            memory,
				},
				CurrentTurn: []Message{
					{Role: RoleUser, Content: "current question"},
					{
						Role:      RoleAssistant,
						ToolCalls: []ToolCall{{ID: "current-call", Name: "read"}},
					},
					{Role: RoleTool, ToolCallID: "current-call", Content: "current result"},
				},
				Policy:  Policy{Version: PolicyVersionV2},
				Budget:  Budget{CounterVersion: ByteEstimateVersion},
				Counter: byteTokenCounter{},
			}
			expected := in
			expected.FrozenMaterials.History = test.history
			expected.FrozenMaterials.Memory = test.memory
			want := mustBuild(t, expected)
			in.Budget.MaxInputBytes = want.Usage.InputBytes
			in.Budget.MaxInputTokens = want.Usage.InputTokens
			before, _ := json.Marshal(in)
			got := mustBuild(t, in)
			if !reflect.DeepEqual(got.Messages, want.Messages) ||
				!reflect.DeepEqual(got.CollapsedSources, test.collapsed) ||
				(len(got.OmittedSources) != 0 || len(test.omitted) != 0) &&
					!reflect.DeepEqual(got.OmittedSources, test.omitted) {
				t.Fatalf(
					"got %+v, want messages %+v, collapsed %+v, omitted %+v",
					got,
					want.Messages,
					test.collapsed,
					test.omitted,
				)
			}
			if got.Usage.InputBytes != want.Usage.InputBytes ||
				got.Usage.InputTokens != want.Usage.InputTokens {
				t.Fatalf("usage %+v, want %+v", got.Usage, want.Usage)
			}
			withoutSystem := got.Messages[1:]
			if err := ValidateGroup(withoutSystem); err != nil {
				t.Fatalf("reduction broke tool pairing: %v", err)
			}
			after, _ := json.Marshal(in)
			if string(before) != string(after) {
				t.Fatal("build mutated input")
			}
			if again := mustBuild(t, in); !reflect.DeepEqual(got, again) {
				t.Fatal("repeated build changed result")
			}
		})
	}
}

func TestBuildDoesNotCollapseUnfinishedOrMultiUserHistory(t *testing.T) {
	toolPair := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "read"}}},
		{Role: RoleTool, ToolCallID: "call", Content: "tool result"},
	}
	tests := []struct {
		name     string
		messages []Message
	}{
		{name: "unfinished", messages: append([]Message{{Role: RoleUser, Content: "question"}}, toolPair...)},
		{
			name: "multiple human inputs",
			messages: []Message{
				{Role: RoleUser, Content: "question"},
				toolPair[0], toolPair[1],
				{Role: RoleUser, Content: "human correction"},
				{Role: RoleAssistant, Content: "answer"},
			},
		},
		{
			name: "no starting user",
			messages: append(append([]Message(nil), toolPair...),
				Message{Role: RoleAssistant, Content: "answer"},
				Message{Role: RoleAssistant, Content: "more"}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := BuildInput{
				FrozenMaterials: FrozenMaterials{
					History: []MessageGroup{historyGroup("old", test.messages...)},
				},
				CurrentTurn: []Message{{Role: RoleUser, Content: "next"}},
				Policy:      Policy{Version: PolicyVersionV2},
			}
			all := mustBuild(t, in)
			in.Budget.MaxInputBytes = all.Usage.InputBytes - 1
			got := mustBuild(t, in)
			if len(got.CollapsedSources) != 0 || len(got.OmittedSources) != 1 ||
				!reflect.DeepEqual(got.Messages, in.CurrentTurn) {
				t.Fatalf("unexpected reduction: %+v", got)
			}
		})
	}
}

func TestBuildV1KeepsWholeGroupPolicy(t *testing.T) {
	group := historyGroup("old",
		Message{Role: RoleUser, Content: "question"},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "read"}}},
		Message{Role: RoleTool, ToolCallID: "call", Content: "result"},
		Message{Role: RoleAssistant, Content: "answer"},
	)
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{History: []MessageGroup{group}},
		CurrentTurn:     []Message{{Role: RoleUser, Content: "next"}},
		Policy:          Policy{Version: PolicyVersionV1},
	}
	in.Budget.MaxInputBytes = mustBuild(t, in).Usage.InputBytes - 1
	got := mustBuild(t, in)
	if len(got.CollapsedSources) != 0 || len(got.OmittedSources) != 1 {
		t.Fatalf("v1 behavior changed: %+v", got)
	}
	in.Policy.Version = PolicyVersionV2
	got = mustBuild(t, in)
	if len(got.CollapsedSources) != 1 || len(got.OmittedSources) != 0 {
		t.Fatalf("v2 did not retain question and answer: %+v", got)
	}
}

func TestBuildNeverCollapsesCurrentTurn(t *testing.T) {
	in := BuildInput{
		CurrentTurn: []Message{
			{Role: RoleUser, Content: "question"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "read"}}},
			{Role: RoleTool, ToolCallID: "call", Content: "result"},
			{Role: RoleAssistant, Content: "answer"},
		},
		Policy: Policy{Version: PolicyVersionV2},
	}
	in.Budget.MaxInputBytes = mustBuild(t, in).Usage.InputBytes - 1
	if _, err := Build(in); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("current turn must fail without reduction: %v", err)
	}
}

func currentToolTurn(count int, content string) []Message {
	messages := []Message{{Role: RoleUser, Content: "question"}}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("call-%d", index)
		messages = append(messages,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: "read"}}},
			Message{Role: RoleTool, ToolCallID: id, Content: content},
		)
	}
	return messages
}

func TestBuildV3ProjectsOlderCurrentToolResults(t *testing.T) {
	content := strings.Repeat("result ", 50)
	turn := currentToolTurn(5, content)
	in := BuildInput{
		CurrentTurn: turn,
		Policy:      Policy{Version: PolicyVersionV3, KeepRecentToolResults: 2},
		Budget:      Budget{CounterVersion: ByteEstimateVersion},
		Counter:     byteTokenCounter{},
	}
	all := mustBuild(t, in)
	if all.ProjectedToolResults != 0 || all.Usage.InputBytes == 0 {
		t.Fatalf("unlimited build projected results: %+v", all)
	}
	folded, projected := projectToolResults(turn, 2)
	if projected != 3 {
		t.Fatalf("projected %d, want 3", projected)
	}
	in.Budget.MaxInputBytes = messagesMeasure(folded).bytes
	result := mustBuild(t, in)
	if result.ProjectedToolResults != 3 {
		t.Fatalf("projected %d, want 3", result.ProjectedToolResults)
	}
	if len(result.OmittedSources) != 0 || len(result.CollapsedSources) != 0 {
		t.Fatalf("unexpected sources: %+v", result)
	}
	if result.Usage.InputBytes != in.Budget.MaxInputBytes {
		t.Fatalf("usage %+v, want %d bytes", result.Usage, in.Budget.MaxInputBytes)
	}
	var markers, intact int
	for _, message := range result.Messages {
		if message.Role != RoleTool {
			continue
		}
		switch {
		case strings.HasPrefix(message.Content, "[Tool result projected:"):
			markers++
			if !strings.Contains(message.Content, fmt.Sprintf("%d bytes", len(content))) {
				t.Fatalf("marker lost original size: %q", message.Content)
			}
		case message.Content == content:
			intact++
		default:
			t.Fatalf("unexpected tool content: %q", message.Content)
		}
	}
	if markers != 3 || intact != 2 {
		t.Fatalf("markers=%d intact=%d, want 3 and 2", markers, intact)
	}
	if err := ValidateGroup(result.Messages); err != nil {
		t.Fatalf("projection broke tool pairing: %v", err)
	}
	for _, message := range turn {
		if message.Role == RoleTool && message.Content != content {
			t.Fatal("projection mutated input messages")
		}
	}
	if again := mustBuild(t, in); !reflect.DeepEqual(result, again) {
		t.Fatal("repeated build changed result")
	}
}

func TestBuildV3DefaultKeepRecentToolResults(t *testing.T) {
	content := strings.Repeat("x", 400)
	turn := currentToolTurn(5, content)
	in := BuildInput{
		CurrentTurn: turn,
		Policy:      Policy{Version: PolicyVersionV3},
		Budget:      Budget{CounterVersion: ByteEstimateVersion},
		Counter:     byteTokenCounter{},
	}
	folded, _ := projectToolResults(turn, DefaultKeepRecentToolResults)
	in.Budget.MaxInputBytes = messagesMeasure(folded).bytes
	result := mustBuild(t, in)
	if result.ProjectedToolResults != 2 {
		t.Fatalf("projected %d, want 2", result.ProjectedToolResults)
	}
	var intact int
	for _, message := range result.Messages {
		if message.Role == RoleTool && message.Content == content {
			intact++
		}
	}
	if intact != DefaultKeepRecentToolResults {
		t.Fatalf("intact results %d, want %d", intact, DefaultKeepRecentToolResults)
	}
}

func TestBuildV3FailsWhenProjectionStillExceeds(t *testing.T) {
	turn := currentToolTurn(5, strings.Repeat("x", 400))
	in := BuildInput{
		CurrentTurn: turn,
		Policy:      Policy{Version: PolicyVersionV3, KeepRecentToolResults: 2},
		Budget:      Budget{CounterVersion: ByteEstimateVersion},
		Counter:     byteTokenCounter{},
	}
	folded, _ := projectToolResults(turn, 2)
	in.Budget.MaxInputBytes = messagesMeasure(folded).bytes - 1
	if _, err := Build(in); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("error %v, want ErrBudgetExceeded", err)
	}
}

func TestBuildV3LeavesCurrentTurnWhenItFits(t *testing.T) {
	turn := currentToolTurn(5, "small result")
	result := mustBuild(t, BuildInput{
		CurrentTurn: turn,
		Policy:      Policy{Version: PolicyVersionV3, KeepRecentToolResults: 2},
		Budget:      Budget{CounterVersion: ByteEstimateVersion},
		Counter:     byteTokenCounter{},
	})
	if result.ProjectedToolResults != 0 {
		t.Fatalf("projected %d results without budget pressure", result.ProjectedToolResults)
	}
	if !reflect.DeepEqual(result.Messages, turn) {
		t.Fatalf("messages %+v, want %+v", result.Messages, turn)
	}
}

func TestBuildV3RejectsNegativeKeepCount(t *testing.T) {
	_, err := Build(BuildInput{
		CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
		Policy:      Policy{Version: PolicyVersionV3, KeepRecentToolResults: -1},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error %v, want ErrInvalidInput", err)
	}
}
