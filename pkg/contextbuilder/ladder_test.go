package contextbuilder

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildLadderReducesOneResultAtATime(t *testing.T) {
	content := "first\n" + strings.Repeat("x", 5990) + "last"
	turn := currentToolTurn(6, content)
	turn[2].ToolFailed = true
	turn[4].ToolName = "read_file"
	turn[6].Exempt = true
	in := BuildInput{
		CurrentTurn: turn,
		Policy:      Policy{Version: PolicyVersionV4},
		Budget:      Budget{CounterVersion: CJKWeightedCounterVersion},
		Counter:     cjkWeightedCounter{},
	}
	before := appendMessages(nil, turn)
	full := mustBuild(t, in)
	preview := appendMessages(nil, turn)
	preview[2].Content = previewToolResult(content, DefaultLadderPreviewBytes)
	in.TargetInputBytes = measureMessages(in.Counter, preview).bytes
	first := mustBuild(t, in)
	if first.LadderPreviewed != 1 || first.LadderStubbed != 0 || !first.TargetMet ||
		!reflect.DeepEqual(first.Messages, preview) {
		t.Fatalf(
			"first reduction = previews %d, stubs %d, met %v",
			first.LadderPreviewed,
			first.LadderStubbed,
			first.TargetMet,
		)
	}
	if full.LadderPreviewed != 0 || full.LadderStubbed != 0 || !full.TargetMet ||
		!reflect.DeepEqual(full.Messages, turn) {
		t.Fatal("build with enough budget changed the turn")
	}

	for _, i := range []int{4, 8, 10} {
		preview[i].Content = preview[2].Content
	}
	preview[4].Content = fmt.Sprintf(
		"[Tool result stub: tool=read_file status=ok original_bytes=%d head=%q restore=read_step_result(step_id=\"call-1\")]",
		len(content),
		content[:DefaultStubHeadRunes]+"...",
	)
	in.TargetInputBytes = measureMessages(in.Counter, preview).bytes
	ok := mustBuild(t, in)
	if ok.LadderPreviewed != 4 || ok.LadderStubbed != 1 || ok.ProjectedToolResults != 1 ||
		!ok.TargetMet || !reflect.DeepEqual(ok.Messages, preview) {
		t.Fatalf(
			"success pass = previews %d, stubs %d, met %v",
			ok.LadderPreviewed,
			ok.LadderStubbed,
			ok.TargetMet,
		)
	}
	if strings.Contains(ok.Messages[4].Content, "\n") {
		t.Fatal("stub contains a line break")
	}

	preview[2].Content = fmt.Sprintf(
		"[Tool result stub: tool=read status=error original_bytes=%d head=%q restore=read_step_result(step_id=\"call-0\")]",
		len(content),
		content[:DefaultStubHeadRunes]+"...",
	)
	in.TargetInputBytes--
	failed := mustBuild(t, in)
	if failed.LadderStubbed != 2 || failed.ProjectedToolResults != 2 ||
		!failed.TargetMet || !reflect.DeepEqual(failed.Messages, preview) {
		t.Fatalf("error pass = stubs %d, met %v", failed.LadderStubbed, failed.TargetMet)
	}
	if err := ValidateGroup(failed.Messages); err != nil {
		t.Fatal(err)
	}
	if measured := measureMessages(in.Counter, failed.Messages); measured.tokens != failed.Usage.InputTokens ||
		measured.bytes != failed.Usage.InputBytes {
		t.Fatalf("usage = %+v, measured = %+v", failed.Usage, measured)
	}
	if !reflect.DeepEqual(turn, before) {
		t.Fatal("ladder mutated original messages")
	}
	in.Budget.MaxInputBytes = failed.Usage.InputBytes - 1
	if _, err := Build(in); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("hard cap after all reductions: %v", err)
	}
}

func TestBuildLadderTruncatesOnlyEligibleJSONObjectArguments(t *testing.T) {
	content := strings.Repeat("结果", 1000)
	turn := currentToolTurn(6, content)
	object := `{"file":{"body":"` + strings.Repeat("界", 1000) + `"},"number":9007199254740993,"small":"keep"}`
	turn[1].ToolCalls[0].Arguments = object
	turn[3].ToolCalls[0].Arguments = `["` + strings.Repeat("a", 1500) + `"]`
	turn[5].ToolCalls[0].Arguments = object + "suffix"
	turn[7].ToolCalls[0].Arguments = object
	turn[8].Exempt = true
	turn[9].ToolCalls[0].Arguments = object
	turn[11].ToolCalls[0].Arguments = object
	in := BuildInput{
		CurrentTurn:       turn,
		Policy:            Policy{Version: PolicyVersionV4, KeepFullToolResults: 2, KeepRecentToolResults: 6},
		Budget:            Budget{CounterVersion: CJKWeightedCounterVersion, MaxToolResultBytes: 4500},
		Counter:           cjkWeightedCounter{},
		TargetInputTokens: 1,
	}
	got := mustBuild(t, in)
	if got.LadderPreviewed != 3 || got.LadderStubbed != 0 || got.TargetMet {
		t.Fatalf(
			"argument reduction = previews %d, stubs %d, met %v",
			got.LadderPreviewed,
			got.LadderStubbed,
			got.TargetMet,
		)
	}
	args := got.Messages[1].ToolCalls[0].Arguments
	var fields struct {
		File   struct{ Body string }
		Number json.Number
		Small  string
	}
	if len(args) >= len(object) || json.Unmarshal([]byte(args), &fields) != nil ||
		len(fields.File.Body) > DefaultArgFieldPreviewBytes || !strings.HasSuffix(fields.File.Body, "...") ||
		!utf8.ValidString(fields.File.Body) || fields.Number != "9007199254740993" || fields.Small != "keep" {
		t.Fatalf("unexpected argument projection %s", args)
	}
	for _, i := range []int{3, 5, 7, 9, 11} {
		if got.Messages[i].ToolCalls[0].Arguments != turn[i].ToolCalls[0].Arguments {
			t.Fatalf("changed ineligible arguments for call at %d", i)
		}
	}
	for _, i := range []int{2, 4, 6} {
		if len(got.Messages[i].Content) > DefaultLadderPreviewBytes ||
			!utf8.ValidString(got.Messages[i].Content) ||
			strings.Count(got.Messages[i].Content, "[Tool result preview:") != 1 {
			t.Fatalf("invalid preview at %d", i)
		}
	}
	if got.Messages[8].Content != content {
		t.Fatal("initial byte ceiling changed an exempt result")
	}
	for _, i := range []int{10, 12} {
		if len(got.Messages[i].Content) > 4500 || len(got.Messages[i].Content) <= DefaultLadderPreviewBytes {
			t.Fatal("recent result should get the hard per-result ceiling only")
		}
	}
	if turn[1].ToolCalls[0].Arguments != object {
		t.Fatal("argument reduction mutated the input")
	}
}

func TestBuildLadderMeasuresMultiToolArgumentReduction(t *testing.T) {
	args := `{"text":"` + strings.Repeat("x", 3000) + `"}`
	turn := []Message{
		{Role: RoleUser, Content: "question"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "a", Name: "one", Arguments: args},
			{ID: "b", Name: "two", Arguments: args},
		}},
		{Role: RoleTool, ToolCallID: "b", Content: "small"},
		{Role: RoleTool, ToolCallID: "a", Content: "small"},
	}
	in := BuildInput{
		CurrentTurn:       turn,
		Policy:            Policy{Version: PolicyVersionV4},
		Budget:            Budget{CounterVersion: CJKWeightedCounterVersion},
		Counter:           cjkWeightedCounter{},
		TargetInputTokens: 1,
	}
	got := mustBuild(t, in)
	calls := got.Messages[1].ToolCalls
	if calls[0].Arguments != args || len(calls[1].Arguments) >= len(args) ||
		got.Usage.InputTokens != measureMessages(in.Counter, got.Messages).tokens || got.LadderPreviewed != 0 {
		t.Fatal("multi-tool argument reduction did not follow result order or yield correct usage")
	}
}

func TestBuildAndSnapshotRejectUnsupportedPolicies(t *testing.T) {
	for _, version := range []string{"context-v1", "context-v2", "context-v3"} {
		policy := Policy{Version: version}
		if _, err := Build(BuildInput{Policy: policy}); !errors.Is(err, ErrUnsupportedPolicy) {
			t.Errorf("Build with %s: %v", version, err)
		}
		if _, err := EncodeSnapshot(Snapshot{Policy: policy}); !errors.Is(err, ErrUnsupportedPolicy) {
			t.Errorf("EncodeSnapshot with %s: %v", version, err)
		}
	}
	for _, policy := range []Policy{
		{Version: PolicyVersionV4, KeepFullToolResults: -1},
		{Version: PolicyVersionV4, KeepRecentToolResults: -1},
	} {
		if _, err := Build(BuildInput{Policy: policy}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Build with %+v: %v", policy, err)
		}
		if _, err := EncodeSnapshot(Snapshot{Policy: policy}); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("EncodeSnapshot with %+v: %v", policy, err)
		}
	}
}
