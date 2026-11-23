package contextbuilder

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildDropsCompleteRoundsAndKeepsPinnedQuery(t *testing.T) {
	query := "original question"
	turn := []Message{
		{Role: RoleUser, Content: query},
		{Role: RoleUser, Content: strings.Repeat("initial detail ", 50)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}}},
		{Role: RoleTool, ToolCallID: "b", Content: strings.Repeat("result ", 100)},
		{Role: RoleTool, ToolCallID: "a", Content: "another result"},
		{Role: RoleUser, Content: "earlier steer"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "read"}}},
		{Role: RoleTool, ToolCallID: "c", Content: "latest result"},
	}
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			SystemInstruction: "system",
			BaseSummary:       "older summary",
			History: []MessageGroup{historyGroup("old",
				Message{Role: RoleUser, Content: "old history"},
			)},
		},
		CurrentTurn:            turn,
		Policy:                 Policy{Version: PolicyVersionV4},
		CompactedSummary:       "new summary",
		PinnedBlocks:           []PinnedBlock{{Label: "query", Content: query}},
		Steers:                 []string{"", "latest steer"},
		AllowDropCurrentRounds: true,
	}
	want := in
	want.CurrentTurn = turn[6:]
	latest := mustBuild(t, want)
	in.Budget.MaxInputBytes = latest.Usage.InputBytes
	got := mustBuild(t, in)
	if got.DroppedRounds != 2 || !got.TargetMet || !reflect.DeepEqual(got.Messages, latest.Messages) {
		t.Fatalf("round reduction: %+v", got)
	}
	preamble := got.Messages[1]
	if got.Messages[0].Role != RoleSystem || preamble.Role != RoleUser ||
		!strings.Contains(preamble.Content, query) || !strings.Contains(preamble.Content, "new summary") ||
		strings.Contains(
			preamble.Content,
			"older summary",
		) || got.Messages[len(got.Messages)-1].Content != "latest steer" {
		t.Fatalf("system, preamble or steer missing: %+v", got.Messages)
	}
	if err := ValidateGroup(got.Messages[1:]); err != nil {
		t.Fatal(err)
	}
	in.Budget.MaxInputBytes--
	if _, err := Build(in); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("newest round, summary, pins and steer should remain required: %v", err)
	}
}

func TestBuildPreambleOrderingAndSoftLimits(t *testing.T) {
	in := BuildInput{
		FrozenMaterials: FrozenMaterials{
			BaseSummary: "summary",
			History: []MessageGroup{historyGroup("old",
				Message{Role: RoleUser, Content: "old question"},
				Message{Role: RoleAssistant, Content: "old answer"},
			)},
		},
		PinnedBlocks: []PinnedBlock{{Label: "query", Content: "pin"}},
		CurrentTurn:  []Message{{Role: RoleUser, Content: "current"}},
		Policy:       Policy{Version: PolicyVersionV4},
	}
	withSummary := mustBuild(t, in)
	if !strings.Contains(withSummary.Messages[0].Content, "summary") ||
		!strings.Contains(
			withSummary.Messages[0].Content,
			"pin",
		) || withSummary.Messages[1].Content != "old question" {
		t.Fatalf("summary/pin ordering: %+v", withSummary.Messages)
	}
	in.FrozenMaterials.BaseSummary = ""
	pinnedOnly := mustBuild(t, in)
	if pinnedOnly.Messages[0].Content != "old question" ||
		!strings.Contains(
			pinnedOnly.Messages[2].Content,
			"pin",
		) || pinnedOnly.Messages[3].Content != "current" {
		t.Fatalf("pin ordering: %+v", pinnedOnly.Messages)
	}
	in.Budget.MaxInputBytes = pinnedOnly.Usage.InputBytes
	in.TargetInputBytes = in.Budget.MaxInputBytes + 1
	in.Steers = []string{strings.Repeat("instruction ", 40)}
	if _, err := Build(in); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("hard limit must win over a looser target: %v", err)
	}
	in.Budget.MaxInputBytes = 1024
	in.TargetInputBytes = 1
	got := mustBuild(t, in)
	if got.TargetMet || len(got.OmittedSources) != 1 || len(got.Messages) != 3 ||
		!strings.Contains(got.Messages[0].Content, "pin") || got.Messages[2].Content != in.Steers[0] {
		t.Fatalf("unmet soft target should retain required input: %+v", got)
	}
}

func TestDropOrphanToolMessagesPreservesValidPairs(t *testing.T) {
	assistant := Message{
		Role:      RoleAssistant,
		ToolCalls: []ToolCall{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}},
	}
	toolA := Message{Role: RoleTool, ToolCallID: "a"}
	toolB := Message{Role: RoleTool, ToolCallID: "b"}
	orphan := Message{Role: RoleTool, ToolCallID: "orphan"}
	valid := []Message{
		{Role: RoleUser, Content: "question"},
		assistant, toolB, toolA,
		{Role: RoleUser, Content: "steer"},
		{Role: RoleAssistant, Content: "answer"},
	}
	rounds := groupRounds(valid)
	if len(rounds) != 3 || len(rounds[0]) != 1 || len(rounds[1]) != 4 || len(rounds[2]) != 1 {
		t.Fatalf("grouped rounds: %+v", rounds)
	}
	if got := dropOrphanToolMessages(valid); !reflect.DeepEqual(got, valid) {
		t.Fatalf("changed valid messages: %+v", got)
	}
	incomplete := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "missing", Name: "read"}}}
	damaged := []Message{orphan, valid[0], incomplete, assistant, toolB, orphan, toolA, toolA, valid[4], valid[5]}
	if got := dropOrphanToolMessages(damaged); !reflect.DeepEqual(got, valid) || ValidateGroup(got) != nil {
		t.Fatalf("orphan cleanup: %+v", got)
	}
}

func TestBuildAutoPinsDroppedInitialUserQuery(t *testing.T) {
	query := "keep this original task"
	turn := []Message{
		{Role: RoleUser, Content: query},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "read"}}},
		{Role: RoleTool, ToolCallID: "a", Content: strings.Repeat("old payload ", 100)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "b", Name: "read"}}},
		{Role: RoleTool, ToolCallID: "b", Content: "latest result"},
	}
	in := BuildInput{
		FrozenMaterials:        FrozenMaterials{SystemInstruction: "system"},
		CurrentTurn:            turn,
		Policy:                 Policy{Version: PolicyVersionV4},
		AllowDropCurrentRounds: true,
	}
	in.Budget.MaxInputBytes = 420
	got := mustBuild(t, in)
	if got.DroppedRounds != 2 || got.Messages[0].Role != RoleSystem ||
		got.Messages[1].Role != RoleUser || !strings.Contains(got.Messages[1].Content, query) {
		t.Fatalf("expected auto-pinned query after dropping initial round: %+v", got)
	}
	if err := ValidateGroup(got.Messages[1:]); err != nil {
		t.Fatalf("invalid turn after drop: %v", err)
	}
}
