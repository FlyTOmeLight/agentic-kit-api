package config

import (
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

func TestPricingConfigurationLoading(t *testing.T) {
	t.Setenv("PRICING_VERSION", "prices-2026")
	base := `[DB]
Driver = "PostgreSQL"
DSN = "postgres://localhost/test"
[HTTPServer]
Listen = "127.0.0.1:8080"
WriteTimeout = -1
[Redis]
Addrs = ["127.0.0.1:6379"]
DB = 0
[AgentArtifact]
Endpoint = "http://127.0.0.1:18080"
[Tenant]
Endpoint = "https://tenant.test"
ServiceAPIKey = "test-service-key-at-least-32-characters"
[Pricing]
Version = "${env:PRICING_VERSION:}"
`
	for _, test := range []struct {
		name, prices string
		cached       *float64
		invalid      bool
	}{
		{name: "empty", prices: "Models = []"},
		{name: "cached omitted", prices: "[[Pricing.Models]]\nName='model'\nInputPerMillion=2.5\nOutputPerMillion=8.0"},
		{name: "cached free", prices: "[[Pricing.Models]]\nName='model'\nInputPerMillion=2.5\nCachedInputPerMillion=0.0\nOutputPerMillion=8.0", cached: new(float64)},
		{name: "negative", prices: "[[Pricing.Models]]\nName='model'\nInputPerMillion=-2.5", invalid: true},
		{name: "duplicate", prices: "[[Pricing.Models]]\nName='model'\n[[Pricing.Models]]\nName='model'", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, base+test.prices))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.ValidateAPI(); (err != nil) != test.invalid {
				t.Fatalf("validation=%v want invalid=%v", err, test.invalid)
			}
			if test.invalid {
				return
			}
			snapshot, err := cfg.Pricing.Freeze(agent.ModelSpec{Name: "model"})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "empty" {
				if snapshot != nil {
					t.Fatal("empty catalog created a price")
				}
				return
			}
			if snapshot.Version != "prices-2026" || snapshot.InputPerMillion != 2.5 ||
				snapshot.OutputPerMillion != 8 {
				t.Fatalf("decoded prices=%+v", snapshot)
			}
			if (snapshot.CachedInputPerMillion == nil) != (test.cached == nil) ||
				(test.cached != nil && *snapshot.CachedInputPerMillion != *test.cached) {
				t.Fatalf("cached price=%v want=%v", snapshot.CachedInputPerMillion, test.cached)
			}
		})
	}
}
