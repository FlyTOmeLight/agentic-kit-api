package durable

import "github.com/FlyTOmeLight/agentic-kit-api/internal/table"

func runFromRow(r table.Run) *Run {
	return &Run{
		Scope:         r.Spec.Scope,
		AgentID:       r.Spec.AgentID,
		ConfigVersion: r.Spec.ConfigVersion,
		ID:            r.ID,
		OwnerID:       r.OwnerID,
		SessionID:     r.SessionID,
		Spec:          Spec(r.Spec),
		State:         r.State,
		Status:        r.Status,
		WorkerID:      r.WorkerID,
		Epoch:         r.Epoch,
		LeaseUntil:    r.LeaseUntil,
		Deadline:      r.Deadline,
		Attempt:       r.Attempt,
		MaxAttempts:   r.MaxAttempts,
		EventSeq:      r.EventSeq,
		Output:        r.Output,
		Error:         r.Error,
		WaitingStep:   waitingStep(r.State),
	}
}

func eventFromRow(e table.Event) Event {
	return Event{RunID: e.RunID, Seq: e.Seq, Kind: e.Kind, Payload: e.Payload, CreatedAt: e.CreatedAt.UTC()}
}
