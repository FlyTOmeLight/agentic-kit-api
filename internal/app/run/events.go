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
	"github.com/FlyTOmeLight/server-go/stream"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
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
