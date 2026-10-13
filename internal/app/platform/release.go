package platform

import (
	"errors"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// RegisterReleaseRoutes runs after runtime bearer authentication. The public
// gateway rejects /internal; current project administration is checked again.
func RegisterReleaseRoutes(r gin.IRouter, service Service, cfg Config, tenant *client.Tenant) {
	h := &controller{service: service, cfg: cfg, tenant: tenant}
	r.POST("/internal/v1/coding-release-approval", func(c *gin.Context) {
		if auth.UserID(c) != cfg.TemplateOwner || cfg.CodingURL == "" {
			serrors.Abort(c, 403, "Forbidden", "trusted Coding application required")
			return
		}
		var req sdk.CodingReleaseRequest
		if !binder.BindJSON(c, &req) {
			return
		}
		if req.TenantID == "" || req.ProjectID == "" || req.ActorID == "" || req.ExperimentID == "" ||
			req.AgentID == "" ||
			req.AgentVersion < 1 {
			serrors.Abort(c, 400, "InvalidRelease", "启用请求不完整")
			return
		}
		var grant struct {
			Allowed bool `json:"allowed"`
			Level   int  `json:"level"`
		}
		status := h.check(
			c.Request.Context(),
			"/internal/v1/access/check",
			map[string]any{
				"user_id":    req.ActorID,
				"tenant_id":  req.TenantID,
				"project_id": req.ProjectID,
				"action":     "write",
			},
			&grant,
		)
		if status != 200 {
			serrors.Abort(c, status, "AccessUnavailable", "无法校验项目权限")
			return
		}
		if !grant.Allowed || grant.Level < 40 {
			serrors.Abort(c, 403, "ProjectAdminRequired", "需要项目管理员权限")
			return
		}
		owner := tenantOwner(req.TenantID)
		experiment, err := service.Evaluation(c.Request.Context(), owner, req.ExperimentID)
		if errors.Is(err, durable.ErrNotFound) { // Existing project-private evidence keeps its original owner.
			owner = projectOwner(req.TenantID, req.ProjectID)
			experiment, err = service.Evaluation(c.Request.Context(), owner, req.ExperimentID)
		}
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		suite := experiment.Suite
		if experiment.Scope.TenantID != req.TenantID || experiment.Scope.ProjectID != req.ProjectID ||
			suite.Target.AgentID != req.AgentID ||
			suite.Target.AgentVersion != req.AgentVersion ||
			suite.Scenario.Kind != "coding" ||
			suite.Scenario.Coding == nil ||
			experiment.Status != "Completed" ||
			experiment.Report == nil ||
			!experiment.Report.Passed ||
			experiment.Report.SuiteDigest != experiment.SuiteDigest {
			serrors.Abort(c, 409, "EvaluationGateFailed", "必须选择此项目、此候选已完成且通过的 Coding 实验")
			return
		}
		strict := false
		for _, metric := range suite.Metrics {
			for _, evaluator := range suite.Evaluators {
				if metric.Evaluator != evaluator.ID || evaluator.Kind != "checks_passed" ||
					evaluator.Scope != "checks" ||
					metric.MinimumScore != 1 ||
					metric.MinimumCoverage != 1 {
					continue
				}
				for _, result := range experiment.Report.Metrics {
					if result.ID == metric.ID && result.Passed && result.Value != nil &&
						*result.Value == 1 &&
						result.Coverage == 1 &&
						result.Scored == len(suite.Dataset.Cases) {
						strict = true
					}
				}
			}
		}
		if !strict {
			serrors.Abort(c, 409, "ChecksGateRequired", "项目启用要求独立检查通过率和覆盖率门槛均为 100%")
			return
		}
		candidate, err := service.AgentVersion(c.Request.Context(), owner, req.AgentID, req.AgentVersion)
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		if !coding(candidate.Config) {
			serrors.Abort(c, 409, "CodingRequired", "只有 Coding 候选可用于项目执行")
			return
		}
		version, err := service.PublishCodingRelease(
			c.Request.Context(),
			auth.UserID(c),
			owner,
			req.AgentID,
			req.AgentVersion,
			cfg.CodingURL,
		)
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		c.JSON(
			200,
			sdk.CodingReleaseApproval{
				ExperimentID:   experiment.ID,
				SuiteDigest:    experiment.SuiteDigest,
				AgentID:        req.AgentID,
				AgentVersion:   req.AgentVersion,
				RuntimeAgentID: version.AgentID,
				RuntimeVersion: version.Number,
				Model:          version.Config.Model.Name,
				Scenario:       *suite.Scenario.Coding,
			},
		)
	})
}
