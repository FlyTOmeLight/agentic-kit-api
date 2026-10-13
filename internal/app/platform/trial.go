package platform

import (
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func (h *controller) startTrial(c *gin.Context) {
	var req sdk.StartProjectTrialRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 100 || strings.TrimSpace(req.Input) == "" ||
		req.TimeoutSeconds < 1 ||
		req.TimeoutSeconds > 3600 {
		serrors.Abort(c, 400, "InvalidTrial", "请输入试运行内容，超时应为 1–3600 秒")
		return
	}
	owner := c.GetString("projectOwner")
	v, err := h.service.AgentVersion(c.Request.Context(), owner, c.Param("agent"), req.AgentVersion)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !writing(v.Config) {
		serrors.Abort(c, 409, "ScenarioUnavailable", "该版本需要独立工具环境，暂不支持直接试运行")
		return
	}
	spec := sdk.ExecutionSpec{AgentID: v.AgentID, AgentVersion: v.Number, Query: req.Input, Scope: &sdk.Scope{
		TenantID: c.Param(
			"tid",
		),
		ProjectID: c.Param("pid"),
		ActorID:   c.GetString("projectActor"),
		TaskID:    "try:" + durable.Hash([]byte(key)),
		RoundID:   "1",
	}}
	run, created, err := h.service.SubmitExecution(
		c.Request.Context(),
		owner,
		"try:"+key,
		spec,
		time.Duration(req.TimeoutSeconds)*time.Second,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := 200
	if created {
		status = 202
	}
	c.JSON(status, sdk.ProjectTrial{Run: run.Response(), Input: run.Spec.Query})
}

func (h *controller) readTrial(c *gin.Context) (*durable.Run, bool) {
	run, err := h.service.Get(c.Request.Context(), c.GetString("projectOwner"), c.Param("run"))
	if err != nil {
		apperror.Respond(c, err)
		return nil, false
	}
	if run.Spec.AgentID != c.Param("agent") || run.Spec.Scope == nil ||
		!strings.HasPrefix(run.Spec.Scope.TaskID, "try:") {
		apperror.Respond(c, durable.ErrNotFound)
		return nil, false
	}
	return run, true
}

func (h *controller) trial(c *gin.Context) {
	run, ok := h.readTrial(c)
	if !ok {
		return
	}
	c.JSON(200, sdk.ProjectTrial{Run: run.Response(), Input: run.Spec.Query})
}

func (h *controller) cancelTrial(c *gin.Context) {
	run, ok := h.readTrial(c)
	if !ok {
		return
	}
	if err := h.service.Cancel(c.Request.Context(), c.GetString("projectOwner"), run.ID); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(204)
}
