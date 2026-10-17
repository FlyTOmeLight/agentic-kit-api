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
	"github.com/FlyTOmeLight/server-go/stream"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// events 订阅任务事件。
// @Summary 订阅任务事件
// @Tags Run
// @Description SSE id 为 runID:seq，event 为事件 kind，data 为 JSON Event；空闲时发送心跳。输出开始后的读取或写入错误关闭连接，客户端使用最后收到的 id 重连；终态事件回放完成后关闭连接。
// @Produce text/event-stream
// @Security BearerAuth
// @Param id path string true "id"
// @Param Last-Event-ID header string false "断点游标 runID:seq，优先于 after"
// @Param after query string false "断点游标 runID:seq"
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
	run, err := h.service.Get(r.Context(), auth.UserID(c), id)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if after > run.EventSeq {
		serrors.Abort(c, http.StatusBadRequest, "InvalidCursor", "cursor is ahead of run")
		return
	}
	eventStream, err := stream.NewSSE(w, 10*time.Second)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	for {
		events, err := h.service.Events(r.Context(), auth.UserID(c), id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			if err = eventStream.SendWithID(
				fmt.Sprintf("%s:%d", id, event.Seq),
				event.Kind,
				event,
			); err != nil {
				return
			}
			after = event.Seq
		}
		if len(events) == 0 {
			if err = eventStream.Heartbeat(); err != nil {
				return
			}
		}
		if len(events) == 200 {
			continue
		}
		run, err = h.service.Get(r.Context(), auth.UserID(c), id)
		if err != nil {
			return
		}
		if run.Status.Terminal() && after >= run.EventSeq {
			return
		}
		// Redis is a wake-up accelerator. Database polling still serves committed events during a Redis outage.
		if h.redis != nil {
			_, err = h.redis.XRead(r.Context(), &redis.XReadArgs{Streams: []string{durable.StreamKey(id), fmt.Sprintf("%d-0", after)}, Count: 1, Block: time.Second}).
				Result()
			if err == nil || errors.Is(err, redis.Nil) {
				continue
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
