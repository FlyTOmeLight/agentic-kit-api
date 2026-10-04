package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

const maxRequestBytes = 1 << 20

// A per-router binding keeps strict JSON semantics without changing Gin's package-level decoder flags.
type strictJSONBinding struct{}

func (strictJSONBinding) Name() string { return "json" }
func (strictJSONBinding) Bind(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("expected one JSON document")
	}
	return binding.Validator.ValidateStruct(dst)
}
func BindJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
	if err := c.ShouldBindWith(dst, strictJSONBinding{}); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			Abort(c, http.StatusRequestEntityTooLarge, "PayloadTooLarge", "request body exceeds 1 MiB")
		} else {
			Abort(c, http.StatusBadRequest, "InvalidRequest", "invalid request body")
		}
		return false
	}
	return true
}
