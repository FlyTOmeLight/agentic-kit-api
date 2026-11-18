package apperror

import (
	"errors"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func Respond(c *core.Context, err error) {
	switch {
	case errors.Is(err, contextbuilder.ErrBudgetExceeded):
		serrors.Abort(c, http.StatusBadRequest, "ContextBudgetExceeded", err.Error())
	case errors.Is(err, memory.ErrOperationOutcomeUnknown):
		serrors.Abort(c, http.StatusServiceUnavailable, "MemoryOperationOutcomeUnknown",
			"memory operation outcome unknown; retry with the same request key")
	case errors.Is(err, memory.ErrVersionUnavailable):
		serrors.Abort(
			c,
			http.StatusServiceUnavailable,
			"MemoryVersionUnavailable",
			"archived memory version unavailable",
		)
	case errors.Is(err, memory.ErrPending):
		serrors.Abort(
			c,
			http.StatusServiceUnavailable,
			"MemoryOperationPending",
			"memory operation pending; retry with the same request key",
		)
	case errors.Is(err, memory.ErrUnavailable):
		serrors.Abort(c, http.StatusServiceUnavailable, "MemoryUnavailable", "memory dependency unavailable")
	case errors.Is(err, durable.ErrInvalid), errors.Is(err, memory.ErrInvalid):
		serrors.Abort(c, 400, "InvalidParameter", "invalid input")
	case errors.Is(err, durable.ErrNotFound), errors.Is(err, memory.ErrNotFound):
		serrors.Abort(c, http.StatusNotFound, "NotFound", "resource not found")
	case errors.Is(err, durable.ErrConflict), errors.Is(err, memory.ErrConflict):
		serrors.Abort(c, http.StatusConflict, "Conflict", "state or idempotency conflict")
	case errors.Is(err, memory.ErrForbidden):
		serrors.Abort(c, http.StatusForbidden, "Forbidden", "access denied")
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
