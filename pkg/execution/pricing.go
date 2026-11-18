package execution

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

type PricingSnapshot struct {
	Version               string   `json:"version"`
	Model                 string   `json:"model"`
	BaseURL               string   `json:"baseURL,omitempty"`
	InputPerMillion       float64  `json:"inputPerMillion"`
	CachedInputPerMillion *float64 `json:"cachedInputPerMillion,omitempty"`
	OutputPerMillion      float64  `json:"outputPerMillion"`
}

type ModelPrice struct {
	Name                  string
	InputPerMillion       float64
	CachedInputPerMillion *float64
	OutputPerMillion      float64
}

type PricingCatalog struct {
	Version string
	Models  []ModelPrice
}

func (catalog PricingCatalog) Validate() error {
	if strings.ContainsAny(catalog.Version, "\r\n") {
		return errors.New("pricing version must not contain line breaks")
	}
	names := make(map[string]bool, len(catalog.Models))
	for _, price := range catalog.Models {
		if price.Name == "" || strings.TrimSpace(price.Name) != price.Name || names[price.Name] {
			return errors.New("pricing requires unique nonempty trimmed model names")
		}
		names[price.Name] = true
		values := []float64{price.InputPerMillion, price.OutputPerMillion}
		if price.CachedInputPerMillion != nil {
			values = append(values, *price.CachedInputPerMillion)
		}
		for _, value := range values {
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("pricing for model %s must be finite and non-negative", price.Name)
			}
		}
	}
	return nil
}

func (catalog PricingCatalog) Freeze(model agent.ModelSpec) (*PricingSnapshot, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	if len(catalog.Models) == 0 {
		return nil, nil
	}
	version := strings.TrimSpace(catalog.Version)
	if version == "" {
		prices := slices.Clone(catalog.Models)
		slices.SortFunc(prices, func(left, right ModelPrice) int {
			return strings.Compare(left.Name, right.Name)
		})
		data, err := json.Marshal(prices)
		if err != nil {
			return nil, err
		}
		version = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	}
	for _, price := range catalog.Models {
		if price.Name != model.Name {
			continue
		}
		snapshot := &PricingSnapshot{
			Version: version, Model: model.Name, BaseURL: model.BaseURL,
			InputPerMillion: price.InputPerMillion, OutputPerMillion: price.OutputPerMillion,
		}
		if price.CachedInputPerMillion != nil {
			cached := *price.CachedInputPerMillion
			snapshot.CachedInputPerMillion = &cached
		}
		return snapshot, nil
	}
	return nil, fmt.Errorf("pricing is not configured for model %q", model.Name)
}
