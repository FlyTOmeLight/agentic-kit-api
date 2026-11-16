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
		{"memory invalid", memory.ErrInvalid, 400, "InvalidParameter"},
		{"memory not found", memory.ErrNotFound, 404, "NotFound"},
		{"memory conflict", memory.ErrConflict, 409, "Conflict"},
		{"memory forbidden", memory.ErrForbidden, 403, "Forbidden"},
		{"memory pending", memory.ErrPending, 503, "MemoryOperationPending"},
		{"memory outcome unknown", memory.ErrOperationOutcomeUnknown, 503, "MemoryOperationOutcomeUnknown"},
		{"memory unavailable", memory.ErrUnavailable, 503, "MemoryUnavailable"},
		{"memory version unavailable", memory.ErrVersionUnavailable, 503, "MemoryVersionUnavailable"},
		{"adapter version unavailable", &memory.VersionUnavailableError{
			Ref:   memory.VersionRef{VersionID: "original-version"},
			Cause: errors.New("source body deleted"),
		}, 503, "MemoryVersionUnavailable"},
		{"replayed version unavailable", &memory.VersionUnavailableError{
			Ref: memory.VersionRef{VersionID: "original-version"},
		}, 503, "MemoryVersionUnavailable"},
		{"durable invalid", durable.ErrInvalid, 400, "InvalidParameter"},
		{"durable not found", durable.ErrNotFound, 404, "NotFound"},
		{"durable conflict", durable.ErrConflict, 409, "Conflict"},
		{"context budget", contextbuilder.ErrBudgetExceeded, 400, "ContextBudgetExceeded"},
		{"unknown", errors.New("private database detail"), 500, "InternalError"},
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
