package run

import (
	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

func (h *controller) trace(c *gin.Context) {
	after, err := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || after < 0 {
		httpx.Abort(c, 400, "InvalidCursor", "after must be a nonnegative event sequence")
		return
	}
	trace, err := h.service.Trace(c.Request.Context(), httpx.Owner(c), c.Param("id"), after)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, trace)
}
