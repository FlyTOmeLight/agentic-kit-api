package apperror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestRespondDomainErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "memory invalid", err: memory.ErrInvalid, status: 400, code: "InvalidParameter"},
		{name: "memory not found", err: memory.ErrNotFound, status: 404, code: "NotFound"},
		{name: "memory conflict", err: memory.ErrConflict, status: 409, code: "Conflict"},
		{name: "memory forbidden", err: memory.ErrForbidden, status: 403, code: "Forbidden"},
		{name: "memory pending", err: memory.ErrPending, status: 503, code: "MemoryOperationPending"},
		{name: "memory outcome unknown", err: memory.ErrOperationOutcomeUnknown, status: 503, code: "MemoryOperationOutcomeUnknown"},
		{name: "memory unavailable", err: memory.ErrUnavailable, status: 503, code: "MemoryUnavailable"},
		{name: "memory version unavailable", err: memory.ErrVersionUnavailable, status: 503, code: "MemoryVersionUnavailable"},
		{name: "adapter version unavailable", err: &memory.VersionUnavailableError{
			Ref:   memory.VersionRef{VersionID: "original-version"},
			Cause: errors.New("source body deleted"),
		}, status: 503, code: "MemoryVersionUnavailable"},
		{name: "replayed version unavailable", err: &memory.VersionUnavailableError{
			Ref: memory.VersionRef{VersionID: "original-version"},
		}, status: 503, code: "MemoryVersionUnavailable"},
		{name: "durable invalid", err: durable.ErrInvalid, status: 400, code: "InvalidParameter"},
		{name: "durable not found", err: durable.ErrNotFound, status: 404, code: "NotFound"},
		{name: "durable conflict", err: durable.ErrConflict, status: 409, code: "Conflict"},
		{name: "context budget", err: contextbuilder.ErrBudgetExceeded, status: 400, code: "ContextBudgetExceeded"},
		{name: "unknown", err: errors.New("private database detail"), status: 500, code: "InternalError"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/v1/test", func(ctx *gin.Context) {
				Respond(ctx, fmt.Errorf("operation failed: %w", test.err))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/test", nil))
			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || body.Code != test.code {
				t.Fatalf(
					"response = %d %s, want %d %s",
					response.Code,
					response.Body.String(),
					test.status,
					test.code,
				)
			}
			if test.name == "unknown" && body.Message == "private database detail" {
				t.Fatal("internal error details leaked")
			}
		})
	}
}
