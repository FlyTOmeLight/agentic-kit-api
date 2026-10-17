package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type submittedMessage struct {
	Service
	called              bool
	session, key, query string
}

func (s *submittedMessage) Submit(
	_ context.Context,
	_, session, key, query string,
	_ time.Duration,
	_ int,
) (*durable.Run, bool, error) {
	s.called = true
	s.session, s.key, s.query = session, key, query
	return &durable.Run{ID: "run", SessionID: session}, true, nil
}

func TestSubmitMessageSourcesRemainSeparate(t *testing.T) {
	for _, tc := range []struct {
		name, path, key string
		status          int
		called          bool
	}{
		{name: "headers cannot overwrite body or URI", path: "/v1/sessions/original/messages", key: "header-key", status: http.StatusAccepted, called: true},
		{name: "query cannot replace required header", path: "/v1/sessions/original/messages?IdempotencyKey=query-key", status: http.StatusBadRequest},
		{name: "header wins over forged query", path: "/v1/sessions/original/messages?IdempotencyKey=query-key", key: "header-key", status: http.StatusAccepted, called: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submitted := &submittedMessage{}
			router := gin.New()
			NewController(submitted).RegisterRoutes(router.Group("/v1"))
			req := httptest.NewRequest(
				http.MethodPost,
				tc.path,
				strings.NewReader(`{"query":"original input"}`),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Query", "injected input")
			req.Header.Set("Sessionid", "injected-session")
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			if submitted.called != tc.called {
				t.Fatalf("Submit called = %v, want %v", submitted.called, tc.called)
			}
			if submitted.called &&
				(submitted.session != "original" || submitted.query != "original input" || submitted.key != tc.key) {
				t.Fatalf(
					"Submit received session=%q query=%q key=%q",
					submitted.session,
					submitted.query,
					submitted.key,
				)
			}
		})
	}
}
