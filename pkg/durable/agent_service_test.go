package durable

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateAgentConfigToolKindContract(t *testing.T) {
	baseTools := []ToolSpec{
		{
			Name:       "lookup",
			URL:        "http://tool.test/lookup",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Policy:     ReadOnly,
		},
	}
	base := AgentConfig{
		Model:    ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
		MaxTurns: 5,
		Tools:    cloneTools(baseTools),
	}
	httpKind := cloneConfig(base)
	httpKind.Tools[0].Kind = "HTTP"
	appForgeMCP := cloneConfig(base)
	appForgeMCP.Tools[0] = ToolSpec{
		Name:       "lookup",
		URL:        "http://tool.test/lookup",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Policy:     ReadOnly,
		Kind:       "AppForgeMCP",
		ResourceID: "resource",
		ToolID:     "tool",
	}
	appForgeOpenAPI := cloneConfig(base)
	appForgeOpenAPI.Tools[0] = ToolSpec{
		Name:       "lookup",
		URL:        "http://tool.test/lookup",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Policy:     ReadOnly,
		Kind:       "AppForgeOpenAPI",
		ResourceID: "resource",
		ToolID:     "tool",
	}
	unknown := cloneConfig(base)
	unknown.Tools[0].Kind = "Unknown"
	appForgeUnsafe := cloneConfig(appForgeMCP)
	appForgeUnsafe.Tools[0].Policy = Unsafe

	cases := []struct {
		name    string
		cfg     AgentConfig
		wantErr error
	}{
		{name: "empty kind accepted", cfg: base, wantErr: nil},
		{name: "HTTP kind accepted", cfg: httpKind, wantErr: nil},
		{name: "AppForgeMCP rejected", cfg: appForgeMCP, wantErr: ErrInvalid},
		{name: "AppForgeOpenAPI rejected", cfg: appForgeOpenAPI, wantErr: ErrInvalid},
		{name: "AppForgeMCP unsafe rejected", cfg: appForgeUnsafe, wantErr: ErrInvalid},
		{name: "unknown kind rejected", cfg: unknown, wantErr: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgentConfig(tc.cfg)
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func cloneTools(in []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, len(in))
	copy(out, in)
	return out
}

func cloneConfig(in AgentConfig) AgentConfig {
	out := in
	out.Tools = cloneTools(in.Tools)
	return out
}
