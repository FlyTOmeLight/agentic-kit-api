package httpapi

import (
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const ownerKey = "authenticatedOwner"

func authenticate(tokens map[string]string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			abort(c, http.StatusUnauthorized, "Unauthorized", "bearer token required")
			return
		}
		token := strings.TrimPrefix(raw, "Bearer ")
		for candidate, owner := range tokens {
			if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
				c.Set(ownerKey, owner)
				c.Next()
				return
			}
		}
		abort(c, http.StatusUnauthorized, "Unauthorized", "invalid bearer token")
	}
}

func requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.InfoContext(c.Request.Context(), "http request", "method", c.Request.Method, "route", c.FullPath(), "status", c.Writer.Status(), "duration", time.Since(start))
	}
}

func recoverJSON() gin.HandlerFunc {
	// Gin's default recovery dumps request headers. Keep credentials and request bodies out of logs.
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		slog.ErrorContext(c.Request.Context(), "http handler panic", "route", c.FullPath(), "stack", string(debug.Stack()))
		if c.Writer.Written() {
			c.Abort()
			return
		}
		abort(c, http.StatusInternalServerError, "InternalError", "internal server error")
	})
}
