package httpx

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

func PageLimit(c *gin.Context, max int) (int, bool) {
	n, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || n < 1 || n > max {
		Abort(c, 400, "InvalidRequest", "invalid page size")
		return 0, false
	}
	return n, true
}
