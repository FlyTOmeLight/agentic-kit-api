package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateAgentConfigMCPServers(t *testing.T) {
	config := AgentConfig{
		Model:    ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
		MaxTurns: 1,
	}
	validServer := MCPServerSpec{
		Name:         "catalog",
		URL:          "https://mcp.test/tools",
		TokenEnv:     "CATALOG_TOKEN",
		AllowedTools: []string{"Find_Items", "update-item"},
		ToolPolicies: map[string]Policy{"Find_Items": ReadOnly, "update-item": Idempotent},
		ForwardScope: true,
	}

	t.Run("maximum server and allowlist counts", func(t *testing.T) {
		cfg := config
		for index := range 16 {
			server := MCPServerSpec{Name: fmt.Sprintf("server-%d", index), URL: validServer.URL}
			for toolIndex := range 64 {
				server.AllowedTools = append(server.AllowedTools, fmt.Sprintf("tool-%d", toolIndex))
			}
			cfg.MCPServers = append(cfg.MCPServers, server)
		}
		if err := ValidateAgentConfig(cfg); err != nil {
			t.Fatal(err)
		}
		cfg.MCPServers = append(cfg.MCPServers, validServer)
		if err := ValidateAgentConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("17 servers: got %v, want %v", err, ErrInvalidConfig)
		}
		cfg.MCPServers = cfg.MCPServers[:16]
		cfg.MCPServers[0].AllowedTools = append(cfg.MCPServers[0].AllowedTools, "tool-64")
		if err := ValidateAgentConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("65 allowed tools: got %v, want %v", err, ErrInvalidConfig)
		}
	})

	t.Run("duplicate server names", func(t *testing.T) {
		cfg := config
		cfg.MCPServers = []MCPServerSpec{validServer, validServer}
		if err := ValidateAgentConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("got %v, want %v", err, ErrInvalidConfig)
		}
	})

	maxTool := strings.Repeat("x", 64-len(FormatMCPToolName(validServer.Name, "")))
	tests := []struct {
		name    string
		edit    func(*MCPServerSpec)
		invalid bool
	}{
		{name: "valid with implicit unsafe"},
		{
			name: "valid with explicit policy",
			edit: func(server *MCPServerSpec) { server.DefaultPolicy = ReadOnly },
		},
		{
			name: "valid 64 character qualified name without allowlist",
			edit: func(server *MCPServerSpec) {
				server.AllowedTools = nil
				server.ToolPolicies = map[string]Policy{maxTool: Unsafe}
			},
		},
		{
			name:    "underscore server name",
			edit:    func(server *MCPServerSpec) { server.Name = "my_server" },
			invalid: true,
		},
		{
			name:    "uppercase server name",
			edit:    func(server *MCPServerSpec) { server.Name = "Catalog" },
			invalid: true,
		},
		{
			name:    "long server name",
			edit:    func(server *MCPServerSpec) { server.Name = strings.Repeat("a", 17) },
			invalid: true,
		},
		{name: "empty server name", edit: func(server *MCPServerSpec) { server.Name = "" }, invalid: true},
		{name: "leading hyphen", edit: func(server *MCPServerSpec) { server.Name = "-catalog" }, invalid: true},
		{
			name:    "non-http URL",
			edit:    func(server *MCPServerSpec) { server.URL = "ftp://mcp.test/tools" },
			invalid: true,
		},
		{name: "relative URL", edit: func(server *MCPServerSpec) { server.URL = "/tools" }, invalid: true},
		{
			name:    "URL userinfo",
			edit:    func(server *MCPServerSpec) { server.URL = "https://user@mcp.test/tools" },
			invalid: true,
		},
		{
			name:    "URL fragment",
			edit:    func(server *MCPServerSpec) { server.URL = "https://mcp.test/tools#list" },
			invalid: true,
		},
		{
			name:    "padded TokenEnv",
			edit:    func(server *MCPServerSpec) { server.TokenEnv = " TOKEN " },
			invalid: true,
		},
		{
			name:    "TokenEnv with interior whitespace",
			edit:    func(server *MCPServerSpec) { server.TokenEnv = "CATALOG\u00a0TOKEN" },
			invalid: true,
		},
		{
			name:    "invalid default policy",
			edit:    func(server *MCPServerSpec) { server.DefaultPolicy = "Unknown" },
			invalid: true,
		},
		{
			name:    "duplicate allowed tool",
			edit:    func(server *MCPServerSpec) { server.AllowedTools = []string{"Find_Items", "update-item", "Find_Items"} },
			invalid: true,
		},
		{
			name:    "empty allowed tool",
			edit:    func(server *MCPServerSpec) { server.AllowedTools = []string{"Find_Items", "update-item", ""} },
			invalid: true,
		},
		{
			name:    "dot in allowed tool",
			edit:    func(server *MCPServerSpec) { server.AllowedTools = []string{"Find_Items", "update-item", "list.all"} },
			invalid: true,
		},
		{
			name: "long qualified allowed tool",
			edit: func(server *MCPServerSpec) {
				server.AllowedTools = []string{"Find_Items", "update-item", maxTool + "x"}
			},
			invalid: true,
		},
		{
			name:    "policy for tool outside allowlist",
			edit:    func(server *MCPServerSpec) { server.ToolPolicies = map[string]Policy{"other": Unsafe} },
			invalid: true,
		},
		{
			name: "dot in policy key",
			edit: func(server *MCPServerSpec) {
				server.AllowedTools = nil
				server.ToolPolicies = map[string]Policy{"list.all": ReadOnly}
			},
			invalid: true,
		},
		{
			name: "long qualified policy key",
			edit: func(server *MCPServerSpec) {
				server.AllowedTools = nil
				server.ToolPolicies = map[string]Policy{maxTool + "x": ReadOnly}
			},
			invalid: true,
		},
		{
			name:    "empty policy override",
			edit:    func(server *MCPServerSpec) { server.ToolPolicies = map[string]Policy{"Find_Items": ""} },
			invalid: true,
		},
		{
			name:    "unknown policy override",
			edit:    func(server *MCPServerSpec) { server.ToolPolicies = map[string]Policy{"Find_Items": "Unknown"} },
			invalid: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := validServer
			if test.edit != nil {
				test.edit(&server)
			}
			cfg := config
			cfg.MCPServers = []MCPServerSpec{server}
			err := ValidateAgentConfig(cfg)
			if test.invalid && !errors.Is(err, ErrInvalidConfig) || !test.invalid && err != nil {
				t.Fatalf("expected invalid config: %v, got %v", test.invalid, err)
			}
		})
	}
}

func TestValidateAgentConfigRejectsReservedOrProprietaryTools(t *testing.T) {
	for _, test := range []struct{ name, kind string }{
		{name: "mcp__catalog__lookup", kind: "HTTP"},
		{name: DelegateSubAgentToolName, kind: "HTTP"},
		{name: ReadStepResultToolName, kind: "HTTP"},
		{name: "lookup", kind: "AppForgeMCP"},
		{name: "lookup", kind: "AppForgeOpenAPI"},
	} {
		t.Run(test.name+"/"+test.kind, func(t *testing.T) {
			cfg := AgentConfig{
				Model:    ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
				MaxTurns: 1,
				Tools: []ToolSpec{{
					Name:       test.name,
					Kind:       test.kind,
					URL:        "https://tools.test/lookup",
					Parameters: json.RawMessage(`{"type":"object"}`),
					Policy:     ReadOnly,
				}},
			}
			if err := ValidateAgentConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("got %v, want %v", err, ErrInvalidConfig)
			}
		})
	}
}

func TestValidateSubAgentSpecs(t *testing.T) {
	valid := SubAgentSpec{Name: "research-1", Description: "Research", AgentID: "agent-id"}
	for _, test := range []struct {
		name string
		spec []SubAgentSpec
		want error
	}{
		{name: "valid", spec: []SubAgentSpec{valid, {Name: "review_2", Description: "Review", AgentID: "agent-id", Version: 3}}},
		{name: "limit", spec: make([]SubAgentSpec, 11), want: ErrInvalidConfig},
		{name: "name", spec: []SubAgentSpec{{Name: "-review", Description: valid.Description, AgentID: valid.AgentID}}, want: ErrInvalidConfig},
		{name: "self", spec: []SubAgentSpec{{Name: "self", Description: valid.Description, AgentID: valid.AgentID}}, want: ErrInvalidConfig},
		{name: "duplicate", spec: []SubAgentSpec{valid, valid}, want: ErrInvalidConfig},
		{name: "empty description", spec: []SubAgentSpec{{Name: valid.Name, Description: " \t", AgentID: valid.AgentID}}, want: ErrInvalidConfig},
		{name: "long description", spec: []SubAgentSpec{{Name: valid.Name, Description: strings.Repeat("x", 1025), AgentID: valid.AgentID}}, want: ErrInvalidConfig},
		{name: "untrimmed ID", spec: []SubAgentSpec{{Name: valid.Name, Description: valid.Description, AgentID: " agent-id"}}, want: ErrInvalidConfig},
		{name: "empty ID", spec: []SubAgentSpec{{Name: valid.Name, Description: valid.Description}}, want: ErrInvalidConfig},
		{name: "long ID", spec: []SubAgentSpec{{Name: valid.Name, Description: valid.Description, AgentID: strings.Repeat("x", 129)}}, want: ErrInvalidConfig},
		{name: "negative version", spec: []SubAgentSpec{{Name: valid.Name, Description: valid.Description, AgentID: valid.AgentID, Version: -1}}, want: ErrInvalidConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := AgentConfig{
				Model:        ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
				MaxTurns:     1,
				SelfDelegate: true,
				SubAgents:    test.spec,
			}
			if err := ValidateAgentConfig(cfg); !errors.Is(err, test.want) {
				t.Fatalf("validation error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMCPToolNameAndPolicy(t *testing.T) {
	server := MCPServerSpec{Name: "catalog", ToolPolicies: map[string]Policy{"find__items": ReadOnly}}
	if got := FormatMCPToolName(server.Name, "find__items"); got != "mcp__catalog__find__items" {
		t.Fatalf("qualified name = %q", got)
	}
	if got := server.PolicyFor("find__items"); got != ReadOnly {
		t.Fatalf("tool override = %q, want ReadOnly", got)
	}
	if got := server.PolicyFor("other"); got != Unsafe {
		t.Fatalf("implicit default = %q, want Unsafe", got)
	}
	server.DefaultPolicy = Idempotent
	if got := server.PolicyFor("other"); got != Idempotent {
		t.Fatalf("explicit default = %q, want Idempotent", got)
	}
}

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

func TestValidateAgentConfigCredentialEnv(t *testing.T) {
	for _, field := range []struct {
		name string
		set  func(*AgentConfig, string)
	}{
		{name: "model.apiKeyEnv", set: func(c *AgentConfig, name string) { c.Model.APIKeyEnv = name }},
		{name: "tools.tokenEnv", set: func(c *AgentConfig, name string) { c.Tools[0].TokenEnv = name }},
		{name: "mcpServers.tokenEnv", set: func(c *AgentConfig, name string) { c.MCPServers[0].TokenEnv = name }},
	} {
		t.Run(field.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, value string
				want        error
			}{
				{name: "empty"},
				{name: "POSIX name", value: "_Provider2_TOKEN"},
				{name: "case-sensitive reserve", value: "database_url"},
				{name: "maximum length", value: strings.Repeat("x", 128)},
				{name: "space", value: "API KEY", want: ErrInvalidConfig},
				{name: "equal sign", value: "API=KEY", want: ErrInvalidConfig},
				{name: "leading digit", value: "1KEY", want: ErrInvalidConfig},
				{name: "too long", value: strings.Repeat("x", 129), want: ErrInvalidConfig},
				{name: "DATABASE_URL", value: "DATABASE_URL", want: ErrInvalidConfig},
				{name: "SANDBOX_CENTER_TOKEN", value: "SANDBOX_CENTER_TOKEN", want: ErrInvalidConfig},
				{name: "REDIS_PASSWORD", value: "REDIS_PASSWORD", want: ErrInvalidConfig},
				{name: "FORGE_SERVICE_API_KEY", value: "FORGE_SERVICE_API_KEY", want: ErrInvalidConfig},
				{name: "TENANT_SERVICE_API_KEY", value: "TENANT_SERVICE_API_KEY", want: ErrInvalidConfig},
				{name: "AGENT_ARTIFACT_SERVICE_API_KEY", value: "AGENT_ARTIFACT_SERVICE_API_KEY", want: ErrInvalidConfig},
				{name: "CALLBACK_SIGNING_SECRET", value: "CALLBACK_SIGNING_SECRET", want: ErrInvalidConfig},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := AgentConfig{
						Model:    ModelSpec{Name: "test", BaseURL: "https://model.example/v1"},
						MaxTurns: 1,
						Tools: []ToolSpec{{
							Name:       "lookup",
							URL:        "https://tools.example/lookup",
							Policy:     ReadOnly,
							Parameters: json.RawMessage(`{"type":"object"}`),
						}},
						MCPServers: []MCPServerSpec{
							{Name: "catalog", URL: "https://mcp.example/tools"},
						},
					}
					field.set(&cfg, tc.value)
					if err := ValidateAgentConfig(cfg); !errors.Is(err, tc.want) {
						t.Fatalf("validation error = %v, want %v", err, tc.want)
					}
				})
			}
		})
	}
}

func TestValidateAgentConfigStaticToolLimit(t *testing.T) {
	tools := make([]ToolSpec, 128)
	for i := range tools {
		tools[i] = ToolSpec{
			Name:       fmt.Sprintf("tool_%d", i),
			URL:        "https://tools.example/run",
			Policy:     ReadOnly,
			Parameters: json.RawMessage(`{"type":"object"}`),
		}
	}
	for _, tc := range []struct {
		name         string
		maxTools     int
		selfDelegate bool
		subAgents    []SubAgentSpec
	}{
		{name: "without delegation", maxTools: 127},
		{name: "self delegation", maxTools: 126, selfDelegate: true},
		{name: "named delegation", maxTools: 126, subAgents: []SubAgentSpec{{Name: "review", Description: "Review", AgentID: "agent-2"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AgentConfig{
				Model:        ModelSpec{Name: "test", BaseURL: "https://model.example/v1"},
				MaxTurns:     1,
				Tools:        tools[:tc.maxTools],
				SelfDelegate: tc.selfDelegate,
				SubAgents:    tc.subAgents,
			}
			if err := ValidateAgentConfig(cfg); err != nil {
				t.Fatalf("%d static tools: %v", tc.maxTools, err)
			}
			cfg.Tools = tools[:tc.maxTools+1]
			if err := ValidateAgentConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("%d static tools: got %v, want %v", len(cfg.Tools), err, ErrInvalidConfig)
			}
		})
	}
}

func TestModelInputModalities(t *testing.T) {
	for _, tc := range []struct {
		name               string
		modalities         []string
		text, image, video bool
		wantErr            error
	}{
		{name: "omitted", text: true, image: true},
		{name: "empty", modalities: []string{}, text: true, image: true},
		{name: "text only", modalities: []string{"text"}, text: true},
		{name: "video opt in", modalities: []string{"text", "video"}, text: true, video: true},
		{name: "duplicate", modalities: []string{"image", "image"}, wantErr: ErrInvalidConfig},
		{name: "unknown", modalities: []string{"audio"}, wantErr: ErrInvalidConfig},
		{name: "case sensitive", modalities: []string{"Image"}, wantErr: ErrInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := ModelSpec{Name: "test", BaseURL: "https://model.test", InputModalities: tc.modalities}
			if err := ValidateAgentConfig(
				AgentConfig{Model: model, MaxTurns: 1},
			); !errors.Is(
				err,
				tc.wantErr,
			) {
				t.Fatalf("validate = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if model.SupportsInputModality("text") != tc.text ||
				model.SupportsInputModality("image") != tc.image ||
				model.SupportsInputModality("video") != tc.video ||
				model.SupportsInputModality("audio") {
				t.Fatalf("modality support for %v is wrong", tc.modalities)
			}
		})
	}
}
