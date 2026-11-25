package durable

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStreamDeltaCodec(t *testing.T) {
	delta := StreamDelta{
		RunID: "run", Kind: EventModelReasoningDelta, Epoch: 1, Turn: 0, Offset: 0,
		Delta: "思考", CreatedAt: time.Unix(123, 456).UTC(),
	}
	data, err := EncodeStreamDelta(delta)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"runID":"run","kind":"ModelReasoningDelta","epoch":1,"turn":0,"offset":0,"delta":"思考","createdAt":"1970-01-01T00:02:03.000000456Z"}`
	if string(data) != want {
		t.Fatalf("encoded: %s", data)
	}
	if decoded, err := DecodeStreamDelta(data); err != nil || decoded != delta {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for name, invalid := range map[string]func(*StreamDelta){
		"run":    func(d *StreamDelta) { d.RunID = "" },
		"kind":   func(d *StreamDelta) { d.Kind = "RunCompleted" },
		"epoch":  func(d *StreamDelta) { d.Epoch = 0 },
		"turn":   func(d *StreamDelta) { d.Turn = -1 },
		"offset": func(d *StreamDelta) { d.Offset = -1 },
		"empty":  func(d *StreamDelta) { d.Delta = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := delta
			invalid(&value)
			if _, err := EncodeStreamDelta(value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("encode: %v", err)
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeStreamDelta(data); !errors.Is(err, ErrInvalid) {
				t.Fatalf("decode: %v", err)
			}
		})
	}
}

func TestModelStartedRecordsLeaseEpochAndOptionalTurn(t *testing.T) {
	store := testStore(t)
	createRun(t, store)
	run := claim(t, store, "worker")
	for _, turn := range []*int64{nil, new(int64(0)), new(int64(2))} {
		if err := store.CommitBoundary(t.Context(), run.Lease(), Boundary{
			Kind: BoundaryModelStarted, State: []byte(`{}`), Turn: turn,
		}); err != nil {
			t.Fatal(err)
		}
		events, err := store.Events(t.Context(), "owner", run.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		last := events[len(events)-1]
		var payload struct {
			Epoch int64  `json:"epoch"`
			Turn  *int64 `json:"turn"`
		}
		if err := json.Unmarshal(last.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if last.Kind != "ModelStarted" || payload.Epoch != run.Epoch ||
			(payload.Turn == nil) != (turn == nil) || (turn != nil && *payload.Turn != *turn) {
			t.Fatalf("event=%+v payload=%s want epoch=%d turn=%v", last, last.Payload, run.Epoch, turn)
		}
	}
}

func TestLatestFinishedModelTurn(t *testing.T) {
	store := testStore(t)
	createRun(t, store)
	run := claim(t, store, "worker")
	checkTurn := func(want int64) {
		t.Helper()
		if turn, err := store.LatestFinishedModelTurn(
			t.Context(),
			"owner",
			run.ID,
		); err != nil ||
			turn != want {
			t.Fatalf("latest=%d err=%v want=%d", turn, err, want)
		}
	}
	record := func(turn int64, call string) {
		t.Helper()
		if err := store.RecordUsage(t.Context(), run.Lease(), turn, call, 0, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	checkTurn(-1)
	record(200, "summary")
	checkTurn(-1)
	record(0, "model")
	checkTurn(0)
	record(5, "model")
	record(2, "model")
	checkTurn(5)
	if _, err := store.LatestFinishedModelTurn(t.Context(), "other", run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner: %v", err)
	}
	other := createRun(t, store)
	if turn, err := store.LatestFinishedModelTurn(t.Context(), "owner", other.ID); err != nil || turn != -1 {
		t.Fatalf("other run: latest=%d err=%v", turn, err)
	}
}
