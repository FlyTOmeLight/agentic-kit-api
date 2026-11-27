package contextbuilder

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRenderSkillDirectoryAndBudgetPrecheck(t *testing.T) {
	files := []string{"SKILL.md"}
	for i := range 10 {
		files = append(files, fmt.Sprintf("references/doc-%02d.md", i))
	}
	block, ok := RenderSkillDirectory([]SkillSummary{
		{Name: "go-review", Description: "Review Go code", EntryPath: "SKILL.md", Files: files},
		{
			Name:        "sql-audit",
			Description: "Check Postgres queries",
			EntryPath:   "SKILL.md",
			Files:       []string{"SKILL.md"},
		},
	})
	if !ok || block.Label != "skills" {
		t.Fatalf("block=%+v ok=%t", block, ok)
	}
	for _, want := range []string{
		"load_skill(name, path?)",
		"- go-review: Review Go code",
		`"references/doc-00.md"`,
		"+2 more",
		"- sql-audit: Check Postgres queries",
	} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("skill directory missing %q:\n%s", want, block.Content)
		}
	}
	if strings.Contains(block.Content, `"SKILL.md"`) {
		t.Fatalf("entry file should not be listed as helper: %s", block.Content)
	}

	counter, err := ResolveCounter(CJKWeightedCounterVersion)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Build(BuildInput{
		FrozenMaterials: FrozenMaterials{
			Skills: []SkillSummary{
				{
					Name:        "go-review",
					Description: "Review Go code",
					Files:       []string{"SKILL.md", "scripts/lint.sh"},
				},
			},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "check this"}},
		Policy:      Policy{Version: PolicyVersionV4},
		Budget:      Budget{MaxInputBytes: 4096},
		Counter:     counter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || !strings.Contains(got.Messages[0].Content, `label="skills"`) ||
		!strings.Contains(got.Messages[0].Content, "go-review") {
		t.Fatalf("messages = %+v", got.Messages)
	}

	_, err = Build(BuildInput{
		FrozenMaterials: FrozenMaterials{
			Skills: []SkillSummary{
				{Name: "go-review", Description: strings.Repeat("d", 512), Files: []string{"SKILL.md"}},
			},
		},
		CurrentTurn: []Message{{Role: RoleUser, Content: "check this"}},
		Policy:      Policy{Version: PolicyVersionV4},
		Budget:      Budget{MaxInputBytes: 256},
		Counter:     counter,
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded for tight skill directory budget, got %v", err)
	}

	// Oversized directory (> 8 KiB) must include an explicit omission marker and stay within cap.
	var many []SkillSummary
	for i := range 16 {
		many = append(many, SkillSummary{
			Name:        fmt.Sprintf("skill-%02d", i),
			Description: strings.Repeat("detailed guidance ", 50),
			Files:       []string{"SKILL.md"},
		})
	}
	capped, ok := RenderSkillDirectory(many)
	if !ok || len(capped.Content) > maxSkillDirectoryBytes ||
		!strings.Contains(capped.Content, "more skills omitted)") {
		t.Fatalf(
			"expected capped directory with omission marker: ok=%t len=%d content=%q",
			ok,
			len(capped.Content),
			capped.Content,
		)
	}
}
