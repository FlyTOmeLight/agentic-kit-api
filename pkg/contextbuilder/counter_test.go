package contextbuilder

import (
	"errors"
	"testing"
)

func TestResolveCounter(t *testing.T) {
	if c, err := ResolveCounter(""); err != nil || c != nil {
		t.Fatalf("empty counter version: got (%v, %v), want (nil, nil)", c, err)
	}
	for _, version := range []string{"utf8-byte-estimate-v1", "unknown-v1"} {
		if _, err := ResolveCounter(version); !errors.Is(err, ErrUnsupportedCounter) {
			t.Fatalf("%s: got %v, want ErrUnsupportedCounter", version, err)
		}
	}
}

func TestCJKWeightedCounter(t *testing.T) {
	counter, err := ResolveCounter(CJKWeightedCounterVersion)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		text string
		want int64
	}{
		{text: "", want: 0},
		{text: "hello world", want: 4},
		{text: "汉字かなカナ한글", want: 6},
		{text: "。、Ａｶ", want: 3},
		{text: "a汉🙂é", want: 2},
		{text: "文a", want: 1},
		{text: "abcd", want: 2},
	}
	for _, tt := range tests {
		if got := counter.Count(tt.text); got != tt.want {
			t.Errorf("Count(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}
