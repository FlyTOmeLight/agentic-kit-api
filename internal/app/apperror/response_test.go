package apperror

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestMemoryErrorStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", memory.ErrInvalid, http.StatusBadRequest},
		{"not found", memory.ErrNotFound, http.StatusNotFound},
		{"conflict", memory.ErrConflict, http.StatusConflict},
		{"forbidden", memory.ErrForbidden, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/", func(ctx *gin.Context) {
				Respond(ctx, fmt.Errorf("memory operation: %w", test.err))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}
