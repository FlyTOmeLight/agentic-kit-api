package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type liveTestFrame struct {
	id, kind, data string
	hasID          bool
}

func nextLiveTestFrame(t *testing.T, frames *bufio.Scanner) liveTestFrame {
	t.Helper()
	var frame liveTestFrame
	for frames.Scan() {
		line := frames.Text()
		if line == "" && frame.kind != "" {
			return frame
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			frame.hasID = true
			frame.id = value
		case "event":
			frame.kind = value
		case "data":
			frame.data = value
		}
	}
	t.Fatalf("missing SSE frame: %v", frames.Err())
	return frame
}

func TestLiveRunEventsPostgreSQLRedis(t *testing.T) {
	cache := eventRedis(t)
	store := integrationStore(t)
	run := createDurableRun(t, store)
	liveKey, eventsKey := durable.LiveStreamKey(run.ID), durable.StreamKey(run.ID)
	t.Cleanup(func() {
		if err := cache.Del(context.Background(), liveKey, eventsKey).Err(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(NewRouter(t, store, cache, map[string]string{testToken: "owner"}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	client, err := sdk.NewClient(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	claim := func(worker string) durable.Lease {
		t.Helper()
		claimed, err := store.Claim(ctx, worker, time.Minute)
		if err != nil || claimed.ID != run.ID {
			t.Fatalf("claim=%+v err=%v", claimed, err)
		}
		return claimed.Lease()
	}
	commit := func(lease durable.Lease, boundary durable.Boundary) {
		t.Helper()
		boundary.State = []byte(`{}`)
		if err := store.CommitBoundary(ctx, lease, boundary); err != nil {
			t.Fatal(err)
		}
	}
	recordUsage := func(lease durable.Lease, turn int64) {
		t.Helper()
		if err := store.RecordUsage(ctx, lease, turn, "model", 1, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	publishOutboxEvents := func() {
		t.Helper()
		for {
			sent, err := publishOutbox(ctx, store, cache)
			if err != nil {
				t.Fatal(err)
			}
			if !sent {
				return
			}
		}
	}
	publishDelta := func(runID string, epoch, turn, offset int64, kind, text string) {
		t.Helper()
		data, err := durable.EncodeStreamDelta(durable.StreamDelta{
			RunID:     runID,
			Kind:      kind,
			Epoch:     epoch,
			Turn:      turn,
			Offset:    offset,
			Delta:     text,
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = cache.Eval(ctx, durable.PublishLiveDeltaLua, []string{liveKey}, durable.LiveStreamMaxLen,
			string(data), int64(durable.LiveStreamTTL/time.Second)).Err(); err != nil {
			t.Fatal(err)
		}
	}
	cursor := func() string {
		t.Helper()
		current, err := store.Get(ctx, "owner", run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s:%d", run.ID, current.EventSeq)
	}
	subscribe := func(after string, live *bool) (io.ReadCloser, *bufio.Scanner) {
		t.Helper()
		body, err := client.Runs.OpenEvents(
			ctx,
			&sdk.OpenRunEventsRequest{ID: run.ID, LastEventID: after, Live: live},
		)
		if err != nil {
			t.Fatal(err)
		}
		return body, bufio.NewScanner(body)
	}
	assertDelta := func(frames *bufio.Scanner, kind, text string, epoch, turn, offset int64) {
		t.Helper()
		frame := nextLiveTestFrame(t, frames)
		var delta sdk.StreamDelta
		err := json.Unmarshal([]byte(frame.data), &delta)
		if frame.hasID || frame.kind != kind || err != nil || delta.Kind != kind || delta.Delta != text ||
			delta.RunID != run.ID || delta.Epoch != epoch || delta.Turn != turn || delta.Offset != offset {
			t.Fatalf("frame=%+v delta=%+v err=%v", frame, delta, err)
		}
	}
	assertEvent := func(frames *bufio.Scanner, kind string) string {
		t.Helper()
		frame := nextLiveTestFrame(t, frames)
		var event sdk.Event
		err := json.Unmarshal([]byte(frame.data), &event)
		if frame.kind != kind || err != nil || event.Kind != kind || event.RunID != run.ID ||
			frame.id != fmt.Sprintf("%s:%d", event.RunID, event.Seq) || event.Seq <= 0 {
			t.Fatalf("frame=%+v event=%+v err=%v", frame, event, err)
		}
		return frame.id
	}

	reasoning, content := sdk.EventModelReasoningDelta, sdk.EventModelContentDelta
	firstLease := claim("live-worker")
	commit(firstLease, durable.Boundary{Kind: durable.BoundaryModelStarted, Turn: new(int64(0))})
	firstBody, firstFrames := subscribe(cursor(), nil)
	defer firstBody.Close()
	publishDelta("wrong-run", firstLease.Epoch, 0, 0, reasoning, "wrong run")
	publishDelta(run.ID, firstLease.Epoch, 0, 0, reasoning, "first thought")
	publishDelta(run.ID, firstLease.Epoch, 0, 0, content, "first content")
	assertDelta(firstFrames, reasoning, "first thought", firstLease.Epoch, 0, 0)
	assertDelta(firstFrames, content, "first content", firstLease.Epoch, 0, 0)
	if count, err := cache.XLen(ctx, eventsKey).Result(); count != 0 || err != nil {
		t.Fatalf("expected delayed outbox: count=%d err=%v", count, err)
	}
	if ttl, err := cache.TTL(ctx, liveKey).
		Result(); err != nil || ttl <= 59*time.Minute ||
		ttl > durable.LiveStreamTTL {
		t.Fatalf("live TTL=%s err=%v", ttl, err)
	}
	recordUsage(firstLease, 0)
	publishOutboxEvents()
	afterUsage := assertEvent(firstFrames, "UsageRecorded")
	firstBody.Close()

	if err := store.Retry(ctx, firstLease, []byte(`{}`), "retry", 0); err != nil {
		t.Fatal(err)
	}
	current := claim("replacement-worker")
	commit(current, durable.Boundary{Kind: durable.BoundaryModelStarted, Turn: new(int64(1))})
	publishDelta(run.ID, current.Epoch, 0, 1, content, "already accounted")
	publishDelta(run.ID, firstLease.Epoch, 1, 0, reasoning, "stale lease")
	publishDelta(run.ID, current.Epoch, 1, 0, reasoning, "replayed thought")
	replayBody, replayFrames := subscribe(afterUsage, nil)
	defer replayBody.Close()
	assertEvent(replayFrames, "RunRetry")
	assertEvent(replayFrames, "RunStarted")
	assertEvent(replayFrames, "ModelStarted")
	assertDelta(replayFrames, reasoning, "replayed thought", current.Epoch, 1, 0)
	publishDelta(run.ID, current.Epoch, 1, 0, reasoning, "duplicate")
	publishDelta(run.ID, current.Epoch, 1, 1, reasoning, "continuation")
	assertDelta(replayFrames, reasoning, "continuation", current.Epoch, 1, 1)

	recordUsage(current, 1)
	publishOutboxEvents()
	afterUsage = assertEvent(replayFrames, "UsageRecorded")
	publishDelta(run.ID, current.Epoch, 1, 2, reasoning, "late thought")
	publishDelta(run.ID, current.Epoch, 2, 0, content, "next content")
	assertDelta(replayFrames, content, "next content", current.Epoch, 2, 0)
	durableBody, durableFrames := subscribe(afterUsage, new(false))
	defer durableBody.Close()
	if err := store.RecordDecision(ctx, current, []byte(`{}`),
		durable.Message{
			Role:      "assistant",
			Content:   "final",
			ToolCalls: []durable.ToolCall{{ID: "lookup-result", Name: "lookup", Arguments: `{}`}},
		}); err != nil {
		t.Fatal(err)
	}
	commit(current, durable.Boundary{Kind: durable.BoundaryComplete, Output: "final"})
	publishOutboxEvents()
	for _, frames := range []*bufio.Scanner{replayFrames, durableFrames} {
		assertEvent(frames, "ModelCompleted")
		assertEvent(frames, "RunCompleted")
		if frames.Scan() || frames.Err() != nil {
			t.Fatalf("expected EOF after completion: %q %v", frames.Text(), frames.Err())
		}
	}
}
