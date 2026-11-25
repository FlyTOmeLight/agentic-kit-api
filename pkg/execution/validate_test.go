package execution

import (
	"errors"
	"testing"
)

func TestBoundaryModelTurn(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    BoundaryKind
		turn    *int64
		invalid bool
	}{
		{name: "legacy", kind: BoundaryModelStarted},
		{name: "zero", kind: BoundaryModelStarted, turn: new(int64(0))},
		{name: "positive", kind: BoundaryModelStarted, turn: new(int64(7))},
		{name: "negative", kind: BoundaryModelStarted, turn: new(int64(-1)), invalid: true},
		{name: "completion", kind: BoundaryComplete, turn: new(int64(0)), invalid: true},
		{name: "failure", kind: BoundaryFail, turn: new(int64(0)), invalid: true},
		{name: "clarification", kind: BoundaryAwaitClarification, turn: new(int64(0)), invalid: true},
		{name: "review", kind: BoundaryAwaitReview, turn: new(int64(0)), invalid: true},
		{name: "external", kind: BoundaryAwaitExternal, turn: new(int64(0)), invalid: true},
		{name: "blocked", kind: BoundaryBlock, turn: new(int64(0)), invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary := Boundary{Kind: test.kind, State: []byte(`{}`)}
			switch test.kind {
			case BoundaryFail, BoundaryBlock:
				boundary.Detail = "reason"
			case BoundaryAwaitClarification, BoundaryAwaitReview, BoundaryAwaitExternal:
				boundary.Detail, boundary.WaitingStep = "reason", "step"
				if test.kind == BoundaryAwaitExternal {
					boundary.ExternalRef = "reference"
				}
			}
			if err := boundary.Validate(); err != nil {
				t.Fatalf("boundary without turn: %v", err)
			}
			boundary.Turn = test.turn
			err := boundary.Validate()
			if (test.invalid && !errors.Is(err, ErrInvalid)) || (!test.invalid && err != nil) {
				t.Fatalf("invalid=%t err=%v", test.invalid, err)
			}
		})
	}
}
