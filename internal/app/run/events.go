package run

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	"github.com/FlyTOmeLight/server-go/stream"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

const eventPollInterval = time.Second

// events 订阅任务事件。
// @Summary 订阅任务事件
// @Tags Run
// @Description 权威事件带 runID:seq 的 id，data 为 JSON Event；ModelReasoningDelta 和 ModelContentDelta 无 id，data 为 JSON StreamDelta。空闲时发送心跳，终态或 Blocked 的权威事件回放完成后关闭连接。
// @Produce text/event-stream
// @Security BearerAuth
// @Param id path string true "id"
// @Param Last-Event-ID header string false "断点游标 runID:seq，优先于 after"
// @Param after query string false "断点游标 runID:seq"
// @Param live query bool false "订阅瞬态模型增量" default(true)
// @Success 200 {string} string "SSE 事件流"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/runs/{id}/events [get]
func (h *Controller) events(c *core.Context) {
	var req sdk.OpenRunEventsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	r, w := c.Request, c.Writer
	id := req.ID
	after := int64(0)
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = req.After
	}
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 || parts[0] != id {
			serrors.Abort(c, http.StatusBadRequest, "InvalidCursor", "invalid event cursor")
			return
		}
		var err error
		after, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || after < 0 {
			serrors.Abort(c, http.StatusBadRequest, "InvalidCursor", "invalid event cursor")
			return
		}
	}
	ctx, owner := r.Context(), auth.UserID(c)
	run, err := h.service.Get(ctx, owner, id)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if after > run.EventSeq {
		serrors.Abort(c, http.StatusBadRequest, "InvalidCursor", "cursor is ahead of run")
		return
	}
	liveEnabled := req.Live == nil || *req.Live
	var filter *liveDeltaFilter
	if liveEnabled && h.redis != nil && !eventReplayOnly(run.Status) {
		finishedTurn, err := h.service.LatestFinishedModelTurn(ctx, owner, id)
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		filter = newLiveDeltaFilter(run, finishedTurn)
	}
	eventStream, err := stream.NewSSE(w, 10*time.Second)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if err = eventStream.FlushHeaders(); err != nil {
		return
	}
	lastWrite := time.Now()
	replay := func() (done bool, err error) {
		for {
			events, err := h.service.Events(ctx, owner, id, after)
			if err != nil {
				return false, err
			}
			for _, event := range events {
				if filter != nil {
					filter.observe(event)
				}
				if err = eventStream.SendWithID(
					fmt.Sprintf("%s:%d", id, event.Seq),
					event.Kind,
					event,
				); err != nil {
					return false, err
				}
				after = event.Seq
				lastWrite = time.Now()
			}
			if len(events) == 200 {
				continue
			}
			if !eventReplayOnly(run.Status) {
				run, err = h.service.Get(ctx, owner, id)
				if err != nil {
					return false, err
				}
			}
			if after >= run.EventSeq {
				if filter != nil {
					filter.observeRun(run)
				}
				return eventReplayOnly(run.Status), nil
			}
		}
	}
	if done, err := replay(); done || err != nil {
		return
	}
	lastDBPoll := time.Now()
	eventsKey, liveKey := durable.StreamKey(id), durable.LiveStreamKey(id)
	liveCursor := "0-0"
	sendDeltas := func(messages []redis.XMessage) error {
		for _, message := range messages {
			liveCursor = message.ID
			delta, err := decodeLiveDelta(message)
			if err != nil || !filter.accept(delta) {
				continue
			}
			if err = eventStream.Send(delta.Kind, delta); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
		return nil
	}
	if filter != nil {
		messages, latestID, err := catchUpLiveDeltas(ctx, h.redis, liveKey, filter)
		if err == nil {
			if sendDeltas(messages) != nil {
				return
			}
			liveCursor = latestID
		}
	}
	splitStreams := false
	for ctx.Err() == nil {
		writeAtStart := lastWrite
		pollAt := lastDBPoll.Add(eventPollInterval)
		heartbeatAt := lastWrite.Add(eventPollInterval)
		wakeAt := pollAt
		if heartbeatAt.Before(wakeAt) {
			wakeAt = heartbeatAt
		}
		var streams []redis.XStream
		var readErr error
		if h.redis != nil {
			block := time.Until(wakeAt)
			if block <= 0 {
				block = -1
			} else {
				block = max(block, time.Millisecond)
			}
			args := frameworkredis.XReadArgs{
				Streams: []string{eventsKey, fmt.Sprintf("%d-0", after)},
				Count:   128,
				Block:   block,
			}
			if filter != nil {
				args.Streams = []string{eventsKey, liveKey, args.Streams[1], liveCursor}
			}
			streams, readErr = readRunStreams(ctx, h.redis, &args, &splitStreams)
		}
		if h.redis == nil || (len(streams) == 0 && readErr != nil && !errors.Is(readErr, frameworkredis.Nil)) {
			timer := time.NewTimer(time.Until(wakeAt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		eventsReady := false
		for _, entries := range streams {
			switch entries.Stream {
			case liveKey:
				if filter != nil && sendDeltas(entries.Messages) != nil {
					return
				}
			case eventsKey:
				eventsReady = len(entries.Messages) > 0
			}
		}
		if eventsReady || !time.Now().Before(pollAt) {
			if done, err := replay(); done || err != nil {
				return
			}
			lastDBPoll = time.Now()
		}
		if lastWrite == writeAtStart && time.Since(lastWrite) >= eventPollInterval {
			if err = eventStream.Heartbeat(); err != nil {
				return
			}
			lastWrite = time.Now()
		}
	}
}

func eventReplayOnly(status durable.Status) bool {
	return status.Terminal() || status == durable.Blocked
}
