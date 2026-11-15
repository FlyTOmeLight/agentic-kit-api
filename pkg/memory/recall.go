package memory

import (
	"strings"
	"unicode"
)

// DefaultRecallMaxEntries bounds how many entries one recall freezes.
const DefaultRecallMaxEntries = 8

// DefaultRecallMaxBytes bounds the total frozen summary plus body bytes.
const DefaultRecallMaxBytes = 32 * 1024

// MatchTerms tokenizes text into lowercase terms. ASCII letters and digits
// split on any other byte; each Han character is one term so Chinese queries
// match per character without a segmentation dependency.
func MatchTerms(text string) []string {
	var (
		terms   []string
		current strings.Builder
	)
	flush := func() {
		if current.Len() > 0 {
			terms = append(terms, current.String())
			current.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			terms = append(terms, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return terms
}

// matchableText is the searchable text of one entry.
func (e *Entry) matchableText() string {
	return e.SubjectKey + "\n" + e.Summary + "\n" + string(e.Body)
}

// Matches reports whether every query term appears in the entry text and
// every requested tag is listed in Conditions.TaskTags.
func (e *Entry) Matches(terms, tags []string) bool {
	if len(tags) > 0 {
		owned := make(map[string]struct{}, len(e.Conditions.TaskTags))
		for _, tag := range e.Conditions.TaskTags {
			owned[strings.ToLower(tag)] = struct{}{}
		}
		for _, tag := range tags {
			if _, ok := owned[strings.ToLower(tag)]; !ok {
				return false
			}
		}
	}
	if len(terms) == 0 {
		return true
	}
	indexed := make(map[string]struct{})
	for _, term := range MatchTerms(e.matchableText()) {
		indexed[term] = struct{}{}
	}
	for _, term := range terms {
		if _, ok := indexed[strings.ToLower(term)]; !ok {
			return false
		}
	}
	return true
}

// MaterialBytes is the frozen size contribution of one entry.
func (e *Entry) MaterialBytes() int {
	return len(e.Summary) + len(e.Body)
}
