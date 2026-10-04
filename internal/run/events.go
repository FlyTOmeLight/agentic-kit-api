package run

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func (h *controller) events(c *gin.Context) {
	r, w := c.Request, c.Writer
	id := c.Param("id")
	after := int64(0)
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 || parts[0] != id {
			httpx.Abort(c, http.StatusBadRequest, "InvalidCursor", "invalid event cursor")
			return
		}
		var err error
		after, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || after < 0 {
			httpx.Abort(c, http.StatusBadRequest, "InvalidCursor", "invalid event cursor")
			return
		}
	}
	run, err := h.service.Get(r.Context(), httpx.Owner(c), id)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	if after > run.EventSeq {
		httpx.Abort(c, http.StatusBadRequest, "InvalidCursor", "cursor is ahead of run")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	for {
		events, err := h.service.Events(r.Context(), httpx.Owner(c), id, after)
		if err != nil {
			return
		}
		if err = rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		for _, event := range events {
			if err = sse.Encode(w, sse.Event{Id: fmt.Sprintf("%s:%d", id, event.Seq), Event: event.Kind, Data: event}); err != nil {
				return
			}
			after = event.Seq
		}
		if len(events) == 0 {
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
		}
		if err = rc.Flush(); err != nil {
			return
		}
		if len(events) == 200 {
			continue
		}
		run, err = h.service.Get(r.Context(), httpx.Owner(c), id)
		if err != nil {
			return
		}
		if run.Status.Terminal() && after >= run.EventSeq {
			return
		}
		// Redis is a wake-up accelerator. Database polling still serves committed events during a Redis outage.
		if h.redis != nil {
			_, err = h.redis.XRead(r.Context(), &redis.XReadArgs{Streams: []string{durable.StreamKey(id), fmt.Sprintf("%d-0", after)}, Count: 1, Block: time.Second}).Result()
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
