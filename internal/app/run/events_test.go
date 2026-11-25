package run

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type eventService struct {
	Service
	run           durable.Run
	events        []durable.Event
	eventPolls    int
	getCalls      int
	finishedCalls int
	finishOnPoll  int
}

func (s *eventService) Get(context.Context, string, string) (*durable.Run, error) {
	s.getCalls++
	run := s.run
	return &run, nil
}

func (s *eventService) LatestFinishedModelTurn(context.Context, string, string) (int64, error) {
	s.finishedCalls++
	return -1, nil
}

func (s *eventService) Events(_ context.Context, _, _ string, after int64) ([]durable.Event, error) {
	s.eventPolls++
	if s.eventPolls == s.finishOnPoll {
		s.complete()
	}
	return s.events[after:], nil
}

func (s *eventService) complete() {
	s.run.EventSeq++
	s.run.Status = durable.Completed
	s.events = append(s.events, durable.Event{
		RunID: s.run.ID, Seq: s.run.EventSeq, Kind: "RunCompleted", Payload: []byte(`{}`),
	})
}

type eventRedis struct {
	frameworkredis.Redis
	rangeCalls int
	read       func(*frameworkredis.XReadArgs) ([]redis.XStream, error)
}

func (r *eventRedis) XRange(context.Context, string, string, string) *redis.XMessageSliceCmd {
	r.rangeCalls++
	return redis.NewXMessageSliceCmdResult(nil, nil)
}

func (r *eventRedis) XRead(_ context.Context, args *frameworkredis.XReadArgs) *redis.XStreamSliceCmd {
	streams, err := r.read(args)
	return redis.NewXStreamSliceCmdResult(streams, err)
}

type eventRecorder struct{ *httptest.ResponseRecorder }

func (eventRecorder) SetWriteDeadline(time.Time) error { return nil }

func readEventResponse(t *testing.T, source Service, cache frameworkredis.Redis, suffix string) string {
	t.Helper()
	handler := gin.New()
	controller := Controller{service: source, redis: cache}
	handler.GET("/runs/:id/events", controller.events)
	recorder := eventRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/run/events"+suffix, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	return recorder.Body.String()
}

func testDeltaMessage(t *testing.T, offset int64) redis.XMessage {
	t.Helper()
	data, err := durable.EncodeStreamDelta(durable.StreamDelta{
		RunID: "run", Kind: durable.EventModelContentDelta, Epoch: 1, Offset: offset,
		Delta: "part", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return redis.XMessage{
		ID: fmt.Sprintf("123-%d", offset), Values: map[string]any{durable.LiveStreamField: string(data)},
	}
}

func TestLiveEventsDoNotAmplifyPollingOrHeartbeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &eventService{run: durable.Run{ID: "run", Status: durable.Running, Epoch: 1}}
		eventKey, liveKey := durable.StreamKey("run"), durable.LiveStreamKey("run")
		liveCursor := "0-0"
		var offset int64
		cache := &eventRedis{read: func(args *frameworkredis.XReadArgs) ([]redis.XStream, error) {
			if args.Count != 128 ||
				strings.Join(
					args.Streams,
					",",
				) != strings.Join(
					[]string{eventKey, liveKey, "0-0", liveCursor},
					",",
				) {
				t.Fatalf("streams=%+v", args)
			}
			time.Sleep(10 * time.Millisecond)
			message := testDeltaMessage(t, offset)
			liveCursor = message.ID
			offset++
			live := redis.XStream{Stream: liveKey, Messages: []redis.XMessage{message}}
			if offset == 160 {
				source.complete()
				return []redis.XStream{
					{Stream: eventKey, Messages: []redis.XMessage{{ID: "1-0"}}},
					live,
				}, nil
			}
			return []redis.XStream{live}, nil
		}}
		body := readEventResponse(t, source, cache, "")
		if count := strings.Count(body, "event:ModelContentDelta\n"); count != 160 {
			t.Fatalf("delta frames=%d", count)
		}
		if strings.Contains(body, ": heartbeat") || strings.Count(body, "id:") != 1 ||
			strings.LastIndex(
				body,
				"event:ModelContentDelta",
			) > strings.Index(
				body,
				"id:run:1\nevent:RunCompleted",
			) ||
			!strings.Contains(body, "id:run:1\nevent:RunCompleted") {
			t.Fatalf("unexpected SSE tail: %s", body[len(body)-350:])
		}
		if source.eventPolls != 3 || source.getCalls != 4 || source.finishedCalls != 1 ||
			cache.rangeCalls != 1 {
			t.Fatalf(
				"Events=%d Get=%d LatestFinishedModelTurn=%d XRANGE=%d",
				source.eventPolls,
				source.getCalls,
				source.finishedCalls,
				cache.rangeCalls,
			)
		}
	})
}

func TestRunEventsDBFallbackAndIdleHeartbeat(t *testing.T) {
	for _, test := range []struct {
		name  string
		cache *eventRedis
		query string
	}{
		{name: "nil redis"},
		{name: "redis offline", cache: &eventRedis{read: func(*frameworkredis.XReadArgs) ([]redis.XStream, error) {
			return nil, errors.New("connection refused")
		}}},
		{name: "live disabled", query: "?live=false", cache: &eventRedis{read: func(args *frameworkredis.XReadArgs) ([]redis.XStream, error) {
			if len(args.Streams) != 2 || args.Streams[0] != durable.StreamKey("run") {
				return nil, fmt.Errorf("unexpected streams: %v", args.Streams)
			}
			time.Sleep(args.Block)
			return nil, frameworkredis.Nil
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := &eventService{
					run:          durable.Run{ID: "run", Status: durable.Queued},
					finishOnPoll: 4,
				}
				var cache frameworkredis.Redis
				if test.cache != nil {
					cache = test.cache
				}
				start := time.Now()
				body := readEventResponse(t, source, cache, test.query)
				if time.Since(start) != 3*time.Second || strings.Count(body, ": heartbeat") != 2 ||
					!strings.Contains(body, "id:run:1\nevent:RunCompleted") {
					t.Fatalf("elapsed=%s body=%q", time.Since(start), body)
				}
				if test.query != "" && (source.finishedCalls != 0 || test.cache.rangeCalls != 0) {
					t.Fatalf(
						"disabled live: usage=%d range=%d",
						source.finishedCalls,
						test.cache.rangeCalls,
					)
				}
			})
		})
	}
}

func TestRunEventsCrossSlotFallsBackToSeparateReads(t *testing.T) {
	source := &eventService{run: durable.Run{ID: "run", Status: durable.Running, Epoch: 1}}
	eventKey, liveKey := durable.StreamKey("run"), durable.LiveStreamKey("run")
	var reads []string
	cache := &eventRedis{read: func(args *frameworkredis.XReadArgs) ([]redis.XStream, error) {
		reads = append(reads, strings.Join(args.Streams, ","))
		switch {
		case len(args.Streams) == 4:
			return nil, errors.New("CROSSSLOT Keys in request don't hash to the same slot")
		case args.Streams[0] == liveKey:
			return []redis.XStream{
				{Stream: liveKey, Messages: []redis.XMessage{testDeltaMessage(t, int64(len(reads)))}},
			}, nil
		case len(reads) == 2:
			return nil, frameworkredis.Nil
		default:
			source.complete()
			return []redis.XStream{{Stream: eventKey, Messages: []redis.XMessage{{ID: "1-0"}}}}, nil
		}
	}}
	body := readEventResponse(t, source, cache, "")
	wantReads := []string{
		eventKey + "," + liveKey + ",0-0,0-0",
		eventKey + ",0-0", liveKey + ",0-0",
		eventKey + ",0-0", liveKey + ",123-3",
	}
	if strings.Join(reads, "|") != strings.Join(wantReads, "|") ||
		strings.Count(body, "event:ModelContentDelta") != 2 ||
		!strings.Contains(body, "id:run:1\nevent:RunCompleted") {
		t.Fatalf("reads=%v body=%q", reads, body)
	}
}

func TestTerminalAndBlockedRunEventsDoNotReadLive(t *testing.T) {
	for _, status := range []durable.Status{durable.Completed, durable.Blocked} {
		t.Run(string(status), func(t *testing.T) {
			source := &eventService{
				run: durable.Run{ID: "run", Status: status, EventSeq: 1, Epoch: 1},
				events: []durable.Event{
					{RunID: "run", Kind: "Run" + string(status), Seq: 1, Payload: []byte(`{}`)},
				},
			}
			cache := &eventRedis{read: func(*frameworkredis.XReadArgs) ([]redis.XStream, error) {
				t.Fatal("terminal or blocked replay read Redis")
				return nil, nil
			}}
			body := readEventResponse(t, source, cache, "")
			if !strings.Contains(body, "id:run:1") || cache.rangeCalls != 0 || source.finishedCalls != 0 {
				t.Fatalf("body=%q XRANGE=%d usage=%d", body, cache.rangeCalls, source.finishedCalls)
			}
		})
	}
}

func TestLiveDeltaFilterMonotonicAcrossReplayAndRetry(t *testing.T) {
	filter := newLiveDeltaFilter(&durable.Run{ID: "run", Epoch: 3, Status: durable.Running, EventSeq: 10}, 0)
	observe := func(seq int64, kind, payload string) {
		filter.observe(durable.Event{Seq: seq, Kind: kind, Payload: []byte(payload)})
	}
	check := func(want bool, epoch, turn, offset int64, kind string) {
		t.Helper()
		delta := durable.StreamDelta{
			RunID:  "run",
			Epoch:  epoch,
			Turn:   turn,
			Offset: offset,
			Kind:   kind,
			Delta:  "value",
		}
		if filter.accept(delta) != want {
			t.Fatalf("accept epoch=%d turn=%d offset=%d kind=%s want=%t", epoch, turn, offset, kind, want)
		}
	}
	reasoning, content := durable.EventModelReasoningDelta, durable.EventModelContentDelta
	observe(8, "RunStarted", `{"epoch":2}`)
	observe(9, "RunRetry", `{"status":"Queued"}`)
	check(true, 3, 1, 0, reasoning)
	check(true, 3, 1, 0, content)
	check(false, 3, 1, 0, content)
	check(false, 2, 1, 1, content)
	check(false, 3, 0, 1, content)
	observe(11, "UsageRecorded", `{"call":"summary","turn":30}`)
	check(true, 3, 1, 1, content)
	observe(12, "RunRetry", `{"status":"Failed"}`)
	check(true, 3, 1, 2, content)
	observe(13, "RunRetry", `{"status":"Queued"}`)
	check(false, 3, 1, 3, content)
	check(true, 4, 1, 0, content)
	observe(14, "RunStarted", `{"epoch":4}`)
	observe(15, "UsageRecorded", `{"call":"model","turn":1}`)
	check(false, 4, 1, 1, content)
	check(true, 4, 2, 0, reasoning)
	observe(16, "RunStarted", `{"epoch":5}`)
	check(false, 4, 2, 1, reasoning)
	check(true, 5, 2, 0, reasoning)
}
