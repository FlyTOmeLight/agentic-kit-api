package execution

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestReleaseInflightModel(t *testing.T) {
	for _, kind := range []string{"model", "summary"} {
		t.Run(kind, func(t *testing.T) {
			state := json.RawMessage(`{"version":3,"turn":5,"checkpoint":"YWJj","future":{"number":9007199254740993},"inflightModel":{"kind":"` + kind + `","sequence":5,"providerRef":"keep"}}`)
			next, marker, err := ReleaseInflightModel(state)
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]json.RawMessage
			if err = json.Unmarshal(state, &before); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(next, &after); err != nil {
				t.Fatal(err)
			}
			if _, present := after["inflightModel"]; present {
				t.Fatal("marker remains")
			}
			if string(marker) != string(before["inflightModel"]) {
				t.Fatalf("audit marker lost fields: %s", marker)
			}
			delete(before, "inflightModel")
			if len(before) != len(after) {
				t.Fatalf("fields changed: %s", next)
			}
			for key, value := range before {
				if string(value) != string(after[key]) {
					t.Fatalf("field %s changed: %s", key, after[key])
				}
			}
		})
	}
	for _, state := range []string{
		`null`, `[]`, `{}`, `{"version":4,"inflightModel":{"kind":"model","sequence":0}}`,
		`{"version":3}`, `{"version":3,"inflightModel":null}`,
		`{"version":3,"inflightModel":{"kind":"model"}}`,
		`{"version":3,"inflightModel":{"kind":"model","sequence":-1}}`,
		`{"version":3,"inflightModel":{"kind":"tool","sequence":0}}`,
		`{"version":3,"pricingFailure":"missing price","inflightModel":{"kind":"model","sequence":0}}`,
		`{"version":3,"pendingModel":{"content":"saved"},"inflightModel":{"kind":"model","sequence":0}}`,
	} {
		t.Run(state, func(t *testing.T) {
			if _, _, err := ReleaseInflightModel(json.RawMessage(state)); !errors.Is(err, ErrConflict) {
				t.Fatalf("got %v, want conflict", err)
			}
		})
	}
}
