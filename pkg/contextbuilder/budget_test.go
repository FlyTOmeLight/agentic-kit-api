package contextbuilder

import (
	"errors"
	"testing"
)

func TestBudgetInputTokenLimit(t *testing.T) {
	tests := []struct {
		name   string
		budget Budget
		want   int64
	}{
		{
			name:   "explicit cap wins",
			budget: Budget{MaxInputTokens: 100, ContextWindow: 1000, OutputReserve: 900},
			want:   100,
		},
		{
			name:   "explicit cap below window",
			budget: Budget{MaxInputTokens: 100, ContextWindow: 1000, OutputReserve: 200},
			want:   100,
		},
		{
			name:   "explicit cap cannot exceed window",
			budget: Budget{MaxInputTokens: 900, ContextWindow: 1000, OutputReserve: 200},
			want:   800,
		},
		{name: "explicit cap without window", budget: Budget{MaxInputTokens: 100}, want: 100},
		{name: "window minus reserve", budget: Budget{ContextWindow: 1000, OutputReserve: 200}, want: 800},
		{name: "unlimited", budget: Budget{}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.budget.InputTokenLimit(); got != test.want {
				t.Fatalf("limit %d, want %d", got, test.want)
			}
		})
	}
}

func TestBuildRejectsInvalidBudget(t *testing.T) {
	tests := []struct {
		name   string
		budget Budget
	}{
		{name: "negative limit", budget: Budget{MaxInputTokens: -1}},
		{name: "negative window", budget: Budget{ContextWindow: -1}},
		{name: "reserve consumes window", budget: Budget{ContextWindow: 100, OutputReserve: 100}},
		{name: "negative overhead", budget: Budget{ProtocolOverheadTokens: -1}},
		{name: "negative preview limit", budget: Budget{MaxToolResultBytes: -1}},
		{name: "preview cannot hold omission marker", budget: Budget{MaxToolResultBytes: 127}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := BuildInput{
				CurrentTurn: []Message{{Role: RoleUser, Content: "question"}},
				Policy:      Policy{Version: PolicyVersionV1},
				Budget:      test.budget,
			}
			if _, err := Build(in); !errors.Is(err, ErrInvalidBudget) {
				t.Fatalf("error %v, want ErrInvalidBudget", err)
			}
		})
	}
}

func TestBudgetExceededErrorShape(t *testing.T) {
	err := (&BudgetExceededError{InputTokens: 11, InputBytes: 22, TokenLimit: 10, ByteLimit: 20}).Error()
	if err != "context budget exceeded: tokens 11 > 10, bytes 22 > 20" {
		t.Fatalf("message %q", err)
	}
}
