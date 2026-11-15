package apperror

import (
	"errors"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func Respond(c *core.Context, err error) {
	switch {
	case errors.Is(err, durable.ErrInvalid):
		serrors.Abort(c, 400, "InvalidParameter", "invalid input")
	case errors.Is(err, durable.ErrNotFound):
		serrors.Abort(c, http.StatusNotFound, "NotFound", "resource not found")
	case errors.Is(err, durable.ErrConflict):
		serrors.Abort(c, http.StatusConflict, "Conflict", "state or idempotency conflict")
	case errors.Is(err, agent.ErrInvalid):
		serrors.Abort(c, http.StatusBadRequest, "InvalidParameter", "invalid input")
	case errors.Is(err, agent.ErrNotFound):
		serrors.Abort(c, http.StatusNotFound, "NotFound", "resource not found")
	case errors.Is(err, agent.ErrConflict):
		serrors.Abort(c, http.StatusConflict, "Conflict", "state or idempotency conflict")
	case errors.Is(err, agent.ErrUnavailable):
		serrors.Abort(c, http.StatusServiceUnavailable, "DependencyUnavailable", "dependency unavailable")
	default:
		serrors.AbortWithError(c, err)
	}
}
