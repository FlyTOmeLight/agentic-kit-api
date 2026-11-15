package memory

import (
	"encoding/json"
	"testing"
)

func TestMatchTermsASCIIAndHan(t *testing.T) {
	terms := MatchTerms("Use go-vet 与 缓存")
	want := []string{"use", "go", "vet", "与", "缓", "存"}
	if len(terms) != len(want) {
		t.Fatalf("MatchTerms len = %d, want %d: %v", len(terms), len(want), terms)
	}
	for i := range want {
		if terms[i] != want[i] {
			t.Fatalf("MatchTerms[%d] = %q, want %q (%v)", i, terms[i], want[i], terms)
		}
	}
}

func TestEntryMatchesTermsAndTags(t *testing.T) {
	entry := &Entry{
		SubjectKey: "editor",
		Summary:    "editor preference",
		Body:       json.RawMessage(`"use tabs and gofmt"`),
		Conditions: Conditions{TaskTags: []string{"Go", "Style"}},
	}

	if !entry.Matches([]string{"editor", "tabs"}, nil) {
		t.Fatal("terms present in text did not match")
	}
	if entry.Matches([]string{"nope"}, nil) {
		t.Fatal("missing terms must not match")
	}
	if !entry.Matches(nil, []string{"go", "style"}) {
		t.Fatal("tags are case-insensitive")
	}
	if entry.Matches(nil, []string{"go", "missing"}) {
		t.Fatal("unknown tag must not match")
	}
}
