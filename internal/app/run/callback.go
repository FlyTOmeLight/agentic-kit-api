package run

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
)

type executionCallbackRequest struct {
	StepID      string `json:"stepID" binding:"required,max=128"`
	ExternalRef string `json:"externalRef" binding:"required,max=128"`
	Result      string `json:"result" binding:"required"`
}

// executionCallback receives a result from an external system (CI, approval
// flow, webhook, etc.) and requeues the waiting run. The externalRef must
// match the reference recorded when the run entered WaitingExternal; a
// replayed callback with an identical result is idempotent.
// @Summary 外部系统回调
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "run id"
// @Param request body executionCallbackRequest true "JSON 请求"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "来源不允许"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态或幂等冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/executions/{id}/callback [post]
func (h *Controller) executionCallback(c *core.Context) {
	source := strings.TrimSpace(c.GetHeader("X-Agentic-Source"))
	if len(h.callback.AllowedSources) > 0 {
		if source == "" || !slices.Contains(h.callback.AllowedSources, source) {
			serrors.Abort(c, http.StatusForbidden, "Forbidden", "callback source not allowed")
			return
		}
	}
	if h.callback.SigningSecret != "" {
		signature, prefixed := strings.CutPrefix(
			strings.TrimSpace(c.GetHeader("X-Agentic-Signature")),
			"sha256=",
		)
		decoded, err := hex.DecodeString(signature)
		if !prefixed || err != nil || len(decoded) != sha256.Size {
			serrors.Abort(c, http.StatusUnauthorized, "Unauthorized", "invalid callback signature")
			return
		}
		if c.Request.Body == nil {
			c.Request.Body = http.NoBody
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, binder.MaxJSONBytes)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var oversized *http.MaxBytesError
			if errors.As(err, &oversized) {
				serrors.Abort(
					c,
					http.StatusRequestEntityTooLarge,
					"PayloadTooLarge",
					"JSON body exceeds size limit",
				)
				return
			}
			serrors.AbortWithError(c, err)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		mac := hmac.New(sha256.New, []byte(h.callback.SigningSecret))
		_, _ = io.WriteString(mac, c.Request.Method+"\n"+c.Request.URL.EscapedPath()+"\n"+source+"\n")
		_, _ = mac.Write(body)
		if !hmac.Equal(decoded, mac.Sum(nil)) {
			serrors.Abort(c, http.StatusUnauthorized, "Unauthorized", "invalid callback signature")
			return
		}
	}
	var uri struct {
		ID string `uri:"id" binding:"required,max=128"`
	}
	if err := binder.Bind(c, &uri).Uri().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	var req executionCallbackRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	if err := h.service.SupplyExternalResult(
		c.Request.Context(),
		auth.UserID(c),
		uri.ID,
		req.StepID,
		req.ExternalRef,
		req.Result,
	); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
