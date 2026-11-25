package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type liveDeltaKey struct {
	epoch int64
	turn  int64
	kind  string
}

type liveDeltaFilter struct {
	runID        string
	initialSeq   int64
	knownEpoch   int64
	minEpoch     int64
	finishedTurn int64
	offsets      map[liveDeltaKey]int64
}

func newLiveDeltaFilter(run *durable.Run, finishedTurn int64) *liveDeltaFilter {
	filter := &liveDeltaFilter{
		runID:        run.ID,
		initialSeq:   run.EventSeq,
		finishedTurn: finishedTurn,
		offsets:      make(map[liveDeltaKey]int64),
	}
	filter.observeRun(run)
	return filter
}

func (f *liveDeltaFilter) observeRun(run *durable.Run) {
	f.knownEpoch = max(f.knownEpoch, run.Epoch)
	minEpoch := max(int64(1), run.Epoch)
	if run.Status == durable.Queued && run.Epoch > 0 {
		minEpoch = run.Epoch + 1
	}
	f.advance(minEpoch, f.finishedTurn)
}

func (f *liveDeltaFilter) observe(event durable.Event) {
	switch event.Kind {
	case "RunStarted":
		var payload struct {
			Epoch int64 `json:"epoch"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil {
			f.knownEpoch = max(f.knownEpoch, payload.Epoch)
			f.advance(payload.Epoch, f.finishedTurn)
		}
	case "UsageRecorded":
		var payload struct {
			Call string `json:"call"`
			Turn *int64 `json:"turn"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Call == "model" && payload.Turn != nil {
			f.advance(f.minEpoch, *payload.Turn)
		}
	case "RunRetry":
		var payload struct {
			Status durable.Status `json:"status"`
		}
		if event.Seq > f.initialSeq && json.Unmarshal(event.Payload, &payload) == nil &&
			payload.Status == durable.Queued {
			f.advance(f.knownEpoch+1, f.finishedTurn)
		}
	}
}

func (f *liveDeltaFilter) advance(minEpoch, finishedTurn int64) {
	if minEpoch <= f.minEpoch && finishedTurn <= f.finishedTurn {
		return
	}
	f.minEpoch = max(f.minEpoch, minEpoch)
	f.finishedTurn = max(f.finishedTurn, finishedTurn)
	for key := range f.offsets {
		if key.epoch < f.minEpoch || key.turn <= f.finishedTurn {
			delete(f.offsets, key)
		}
	}
}

func (f *liveDeltaFilter) accept(delta durable.StreamDelta) bool {
	if delta.RunID != f.runID || delta.Epoch < f.minEpoch || delta.Turn <= f.finishedTurn {
		return false
	}
	key := liveDeltaKey{epoch: delta.Epoch, turn: delta.Turn, kind: delta.Kind}
	if offset, seen := f.offsets[key]; seen && delta.Offset <= offset {
		return false
	}
	f.offsets[key] = delta.Offset
	return true
}

func readRunStreams(
	ctx context.Context,
	client frameworkredis.Redis,
	args *frameworkredis.XReadArgs,
	split *bool,
) ([]redis.XStream, error) {
	if !*split {
		streams, err := client.XRead(ctx, args).Result()
		if len(args.Streams) != 4 || err == nil || !strings.Contains(err.Error(), "CROSSSLOT") {
			return streams, err
		}
		*split = true
	}
	events, eventsErr := client.XRead(ctx, &frameworkredis.XReadArgs{
		Streams: []string{args.Streams[0], args.Streams[2]}, Count: args.Count, Block: -1,
	}).Result()
	block := min(args.Block, 250*time.Millisecond)
	if len(events) > 0 {
		block = -1
	}
	live, liveErr := client.XRead(ctx, &frameworkredis.XReadArgs{
		Streams: []string{args.Streams[1], args.Streams[3]}, Count: args.Count, Block: block,
	}).Result()
	if errors.Is(eventsErr, frameworkredis.Nil) {
		eventsErr = nil
	}
	if errors.Is(liveErr, frameworkredis.Nil) {
		liveErr = nil
	}
	return append(events, live...), errors.Join(eventsErr, liveErr)
}
