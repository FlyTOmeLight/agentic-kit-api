package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
)

type errorResponse struct {
	Error errorDetail `json:"error"`
}
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Abort(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}
func RespondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, durable.ErrInvalid):
		Abort(c, 400, "InvalidRequest", "invalid input")
	case errors.Is(err, durable.ErrNotFound):
		Abort(c, http.StatusNotFound, "NotFound", "resource not found")
	case errors.Is(err, durable.ErrConflict):
		Abort(c, http.StatusConflict, "Conflict", "state or idempotency conflict")
	default:
		slog.ErrorContext(c.Request.Context(), "request failed", "route", c.FullPath(), "error", err)
		Abort(c, http.StatusInternalServerError, "InternalError", "internal server error")
	}
}
