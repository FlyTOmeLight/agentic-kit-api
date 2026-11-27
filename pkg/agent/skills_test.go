package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValidateAgentConfigSkillsAndReservedToolCap(t *testing.T) {
	base := func() AgentConfig {
		return AgentConfig{
			Model:    ModelSpec{Name: "model", BaseURL: "https://model.test/v1"},
			MaxTurns: 5,
			Skills: []SkillRef{{
				Name:        "go-review",
				ArtifactURI: "artifact://tenant/artifacts/agent-skill/s1/versions/v1",
				Description: "Review Go code",
			}},
		}
	}
	if err := ValidateAgentConfig(base()); err != nil {
		t.Fatalf("valid skill config rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*AgentConfig)
	}{
		{name: "17 skills", edit: func(c *AgentConfig) {
			c.Skills = make([]SkillRef, 17)
			for i := range c.Skills {
				c.Skills[i] = SkillRef{Name: fmt.Sprintf("skill-%d", i), ArtifactURI: "artifact://t/artifacts/agent-skill/s/versions/v1"}
			}
		}},
		{name: "duplicate skill name", edit: func(c *AgentConfig) {
			c.Skills = append(c.Skills, c.Skills[0])
		}},
		{name: "bad skill name", edit: func(c *AgentConfig) { c.Skills[0].Name = "Bad Name" }},
		{name: "non-artifact URI", edit: func(c *AgentConfig) { c.Skills[0].ArtifactURI = "https://example.com/skill" }},
		{name: "artifact URI fragment", edit: func(c *AgentConfig) { c.Skills[0].ArtifactURI = "artifact://t/a/v1#frag" }},
		{name: "artifact URI userinfo", edit: func(c *AgentConfig) { c.Skills[0].ArtifactURI = "artifact://u@t/a/v1" }},
		{name: "blank description", edit: func(c *AgentConfig) { c.Skills[0].Description = "   " }},
		{name: "control in description", edit: func(c *AgentConfig) { c.Skills[0].Description = "ok\nbad" }},
		{name: "oversize description", edit: func(c *AgentConfig) { c.Skills[0].Description = strings.Repeat("a", 1025) }},
		{name: "reserved load_skill static tool", edit: func(c *AgentConfig) {
			c.Skills = nil
			c.Tools = []ToolSpec{{
				Name: LoadSkillToolName, URL: "https://tool.test/x", Policy: ReadOnly, Parameters: json.RawMessage(`{"type":"object"}`),
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.edit(&cfg)
			if err := ValidateAgentConfig(cfg); err == nil {
				t.Fatalf("expected invalid config for %s", tc.name)
			}
		})
	}

	makeTools := func(n int) []ToolSpec {
		tools := make([]ToolSpec, n)
		for i := range n {
			tools[i] = ToolSpec{
				Name: fmt.Sprintf(
					"t_%03d",
					i,
				),
				URL:        "https://tool.test/x",
				Policy:     ReadOnly,
				Parameters: json.RawMessage(`{"type":"object"}`),
			}
		}
		return tools
	}
	for _, tc := range []struct {
		name     string
		skills   bool
		delegate bool
		count    int
		valid    bool
	}{
		{name: "skills only 126", skills: true, count: 126, valid: true},
		{name: "skills only 127", skills: true, count: 127, valid: false},
		{name: "skills+delegate 125", skills: true, delegate: true, count: 125, valid: true},
		{name: "skills+delegate 126", skills: true, delegate: true, count: 126, valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			if !tc.skills {
				cfg.Skills = nil
			}
			cfg.SelfDelegate = tc.delegate
			cfg.Tools = makeTools(tc.count)
			err := ValidateAgentConfig(cfg)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
}
