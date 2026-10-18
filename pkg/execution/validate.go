package execution

import (
	"encoding/json"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

// Interaction reports the pause kind this boundary carries. It is empty for
// boundaries that do not wait for a human.
func (k BoundaryKind) Interaction() InteractionKind {
	switch k {
	case BoundaryAwaitClarification:
		return InteractionClarification
	case BoundaryAwaitReview:
		return InteractionReview
	default:
		return ""
	}
}

// Validate rejects any field combination the store cannot commit. It runs
// before a transaction is opened, so an invalid boundary writes nothing.
func (b Boundary) Validate() error {
	if len(b.State) == 0 || !json.Valid(b.State) {
		return ErrInvalid
	}
	switch b.Kind {
	case BoundaryModelStarted:
		if b.Output != "" || b.Detail != "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryComplete:
		if b.Detail != "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryFail:
		if b.Output != "" || b.Detail == "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryAwaitClarification, BoundaryAwaitReview:
		if b.Output != "" || b.Detail == "" || b.WaitingStep == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Validate rejects an intent the store cannot record.
func (i StepIntent) Validate() error {
	if i.ID == "" {
		return ErrInvalid
	}
	switch i.Policy {
	case agent.ReadOnly, agent.Idempotent, agent.Unsafe:
		return nil
	default:
		return ErrInvalid
	}
}
