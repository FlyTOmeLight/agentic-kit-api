package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateAgentConfigModelBudget(t *testing.T) {
	maxTokens := func(n int) *int { return &n }
	base := AgentConfig{
		Model: ModelSpec{
			Name:    "test",
			BaseURL: "http://model.test/v1",
		},
		MaxTurns: 1,
	}

	tests := []struct {
		name          string
		contextWindow int64
		maxTokens     *int
		wantErr       error
	}{
		{name: "legacy config without window", wantErr: nil},
		{name: "legacy config with positive max tokens", maxTokens: maxTokens(128), wantErr: nil},
		{name: "zero max tokens", maxTokens: maxTokens(0), wantErr: ErrInvalidConfig},
		{name: "negative max tokens", maxTokens: maxTokens(-1), wantErr: ErrInvalidConfig},
		{name: "negative window", contextWindow: -1, wantErr: ErrInvalidConfig},
		{name: "positive window without max tokens", contextWindow: 1024, wantErr: ErrInvalidConfig},
		{
			name:          "positive window with zero max tokens",
			contextWindow: 1024,
			maxTokens:     maxTokens(0),
			wantErr:       ErrInvalidConfig,
		},
		{
			name:          "positive window with negative max tokens",
			contextWindow: 1024,
			maxTokens:     maxTokens(-1),
			wantErr:       ErrInvalidConfig,
		},
		{
			name:          "positive window equal to max tokens",
			contextWindow: 1024,
			maxTokens:     maxTokens(1024),
			wantErr:       ErrInvalidConfig,
		},
		{
			name:          "positive window below max tokens",
			contextWindow: 1024,
			maxTokens:     maxTokens(2048),
			wantErr:       ErrInvalidConfig,
		},
		{
			name:          "positive window above max tokens",
			contextWindow: 1024,
			maxTokens:     maxTokens(1023),
			wantErr:       nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.Model.ContextWindow = tt.contextWindow
			cfg.Model.MaxTokens = tt.maxTokens

			err := ValidateAgentConfig(cfg)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestModelSpecContextWindowJSON(t *testing.T) {
	data, err := json.Marshal(ModelSpec{ContextWindow: 32768})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if got := fields["contextWindow"]; got != float64(32768) {
		t.Fatalf("contextWindow = %v, want 32768; JSON: %s", got, data)
	}

	data, err = json.Marshal(ModelSpec{})
	if err != nil {
		t.Fatal(err)
	}
	var zeroFields map[string]any
	if err := json.Unmarshal(data, &zeroFields); err != nil {
		t.Fatal(err)
	}
	if _, ok := zeroFields["contextWindow"]; ok {
		t.Fatalf("zero contextWindow must be omitted; JSON: %s", data)
	}
	if _, ok := zeroFields["stream"]; ok {
		t.Fatalf("nil stream must be omitted; JSON: %s", data)
	}
}

func TestModelSpecStreamJSON(t *testing.T) {
	if !(ModelSpec{}).Streaming() {
		t.Fatal("nil Stream must default to true")
	}
	disabled := false
	spec := ModelSpec{Stream: &disabled}
	if spec.Streaming() {
		t.Fatal("explicit false Stream must return false")
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if got, ok := fields["stream"].(bool); !ok || got {
		t.Fatalf("stream = %#v, want false; JSON: %s", fields["stream"], data)
	}
	var decoded ModelSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Stream == nil || *decoded.Stream {
		t.Fatalf("decoded Stream = %v, want false", decoded.Stream)
	}
}
