package execution

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

func TestPricingCatalogFreeze(t *testing.T) {
	model := agent.ModelSpec{Name: "model", BaseURL: "https://model.test/v1"}
	snapshot, err := (PricingCatalog{}).Freeze(model)
	if err != nil || snapshot != nil {
		t.Fatalf("empty catalog: snapshot=%+v err=%v", snapshot, err)
	}
	cached := 0.0
	catalog := PricingCatalog{
		Version: "release-1",
		Models:  []ModelPrice{{Name: model.Name, InputPerMillion: 2, CachedInputPerMillion: &cached, OutputPerMillion: 8}},
	}
	snapshot, err = catalog.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	want := &PricingSnapshot{Version: "release-1", Model: model.Name, BaseURL: model.BaseURL,
		InputPerMillion: 2, CachedInputPerMillion: &cached, OutputPerMillion: 8}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot=%+v want=%+v", snapshot, want)
	}
	cached = 7
	catalog.Models[0].InputPerMillion = 9
	if *snapshot.CachedInputPerMillion != 0 || snapshot.InputPerMillion != 2 {
		t.Fatal("catalog mutation changed frozen prices")
	}
	catalog.Models[0].CachedInputPerMillion = nil
	snapshot, err = catalog.Freeze(model)
	if err != nil || snapshot.CachedInputPerMillion != nil {
		t.Fatalf("omitted cached price: snapshot=%+v err=%v", snapshot, err)
	}
	if _, err := catalog.Freeze(agent.ModelSpec{Name: "missing"}); err == nil {
		t.Fatal("nonempty catalog accepted missing model")
	}
}

func TestPricingCatalogDigest(t *testing.T) {
	model := agent.ModelSpec{Name: "a"}
	first := PricingCatalog{Models: []ModelPrice{{Name: "b", InputPerMillion: 2}, {Name: "a", InputPerMillion: 1}}}
	second := PricingCatalog{Models: []ModelPrice{first.Models[1], first.Models[0]}}
	left, err := first.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(left.Version, "sha256:") || len(left.Version) != 71 || left.Version != right.Version {
		t.Fatalf("unstable catalog digest: %q / %q", left.Version, right.Version)
	}
	if first.Models[0].Name != "b" {
		t.Fatal("freeze reordered configured catalog")
	}
	second.Models[0].InputPerMillion++
	changed, err := second.Freeze(model)
	if err != nil || changed.Version == left.Version {
		t.Fatalf("price change did not change digest: %+v err=%v", changed, err)
	}
}

func TestPricingCatalogRejectsInvalidConfiguration(t *testing.T) {
	negative, nan, infinite := -1.0, math.NaN(), math.Inf(1)
	for _, test := range []struct {
		name    string
		catalog PricingCatalog
	}{
		{"empty name", PricingCatalog{Models: []ModelPrice{{}}}},
		{"untrimmed name", PricingCatalog{Models: []ModelPrice{{Name: " model"}}}},
		{"duplicate", PricingCatalog{Models: []ModelPrice{{Name: "model"}, {Name: "model"}}}},
		{"negative input", PricingCatalog{Models: []ModelPrice{{Name: "model", InputPerMillion: -1}}}},
		{"nan output", PricingCatalog{Models: []ModelPrice{{Name: "model", OutputPerMillion: math.NaN()}}}},
		{"infinite input", PricingCatalog{Models: []ModelPrice{{Name: "model", InputPerMillion: math.Inf(-1)}}}},
		{"negative cached", PricingCatalog{Models: []ModelPrice{{Name: "model", CachedInputPerMillion: &negative}}}},
		{"nan cached", PricingCatalog{Models: []ModelPrice{{Name: "model", CachedInputPerMillion: &nan}}}},
		{"infinite cached", PricingCatalog{Models: []ModelPrice{{Name: "model", CachedInputPerMillion: &infinite}}}},
		{"newline version", PricingCatalog{Version: "version\n"}},
		{"carriage return version", PricingCatalog{Version: "version\r"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.catalog.Validate() == nil {
				t.Fatal("invalid catalog accepted")
			}
			if _, err := test.catalog.Freeze(agent.ModelSpec{Name: "model"}); err == nil {
				t.Fatal("invalid catalog frozen")
			}
		})
	}
}
