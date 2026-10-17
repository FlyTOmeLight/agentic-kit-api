package platform

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	framesdk "github.com/FlyTOmeLight/server-go/sdk"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
)

type Config struct {
	Tenant    framesdk.Config
	CodingURL string
}

type Controller struct {
	store    Store
	business *Service
	cfg      Config
	tenant   *client.Tenant
}

func (h *Controller) check(ctx context.Context, path string, body, out any) int {
	return h.tenant.Check(ctx, path, body, out)
}

// Owner namespaces reuse the store's enforced owner isolation. Only the server
// derives this value, after checking the session and current project grant.
func projectOwner(tenant, project string) string {
	raw, _ := json.Marshal([]string{tenant, project})
	return fmt.Sprintf("forge-project:%x", sha256.Sum256(raw))
}

func (h *Controller) authorized(c *core.Context) {
	cookie, err := c.Cookie("forge_session")
	if err != nil || cookie == "" {
		serrors.Abort(c, 401, "AuthRequired", "请先登录")
		return
	}
	var user struct {
		ID string `json:"id"`
	}
	status := h.check(
		c.Request.Context(),
		"/internal/v1/session/check",
		map[string]any{
			"session": cookie,
			"method":  c.Request.Method,
			"origin":  c.GetHeader("Origin"),
			"csrf":    c.GetHeader("X-CSRF-Token"),
		},
		&user,
	)
	if status != 200 || user.ID == "" {
		if status == 200 {
			status = 401
		}
		serrors.Abort(c, status, "SessionRejected", "会话校验失败，请重新登录或刷新页面")
		return
	}
	if c.Param("pid") == "" {
		var access workspaceAccess
		status := h.check(
			c.Request.Context(),
			"/internal/v1/workspace/check",
			map[string]any{"user_id": user.ID, "tenant_id": c.Param("tid")},
			&access,
		)
		if status != 200 || !access.Allowed {
			if status == 200 {
				status = 403
			}
			serrors.Abort(c, status, "WorkspaceAccessRequired", "需要有效的企业成员身份")
			return
		}
		if c.Request.Method != "GET" && !access.CanEdit {
			serrors.Abort(c, 403, "WorkspaceWriteRequired", "当前成员不能编辑企业资产")
			return
		}
		c.Set("workspaceAccess", access)
		c.Set("projectOwner", tenantOwner(c.Param("tid")))
		c.Set("projectActor", user.ID)
		c.Set("projectCanEdit", access.CanEdit)
		c.Header("Cache-Control", "no-store")
		c.Next()
		return
	}
	action, minimum := "read", 20
	if c.Request.Method != "GET" {
		action, minimum = "write", 30
	}
	var grant struct {
		Allowed bool `json:"allowed"`
		Level   int  `json:"level"`
	}
	status = h.check(
		c.Request.Context(),
		"/internal/v1/access/check",
		map[string]any{
			"user_id":    user.ID,
			"tenant_id":  c.Param("tid"),
			"project_id": c.Param("pid"),
			"action":     action,
		},
		&grant,
	)
	if status != 200 {
		serrors.Abort(c, status, "AccessUnavailable", "无法校验项目权限")
		return
	}
	if !grant.Allowed || grant.Level < minimum {
		serrors.Abort(c, 403, "ProjectAccessRequired", "当前项目权限不足")
		return
	}
	c.Set("projectOwner", projectOwner(c.Param("tid"), c.Param("pid")))
	c.Set("projectActor", user.ID)
	c.Set("projectCanEdit", grant.Level >= 30)
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func NewController(store Store, business *Service, cfg Config, tenant *client.Tenant) *Controller {
	return &Controller{store: store, business: business, cfg: cfg, tenant: tenant}
}

func (h *Controller) RegisterRoutes(r core.Router) {
	register := func(g *core.RouterGroup) {
		g.GET("/agents", h.agents)
		g.GET("/agents/:agent", h.agent)
		g.PUT("/agents/:agent", h.updateAgent)
		g.POST("/agents/:agent/candidates", h.candidate)
		g.GET("/agents/:agent/versions/:version", h.version)
		g.POST("/agents/:agent/trials", h.startTrial)
		g.GET("/agents/:agent/trials/:run", h.trial)
		g.POST("/agents/:agent/trials/:run/cancel", h.cancelTrial)
		g.GET("/evaluation-plans", h.plans)
		g.GET("/evaluation-plans/:plan", h.plan)
		g.PUT("/evaluation-plans/:plan", h.savePlan)
		g.GET("/evaluations", h.evaluations)
		g.POST("/evaluations", h.start)
		g.GET("/evaluations/:experiment", h.experiment)
		g.POST("/evaluations/:experiment/cancel", h.cancel)
		g.GET("/evaluations/:experiment/compare", h.compare)
		g.GET("/evaluations/:experiment/trials/:trial/trace", h.trace)

		g.GET("/datasets", h.datasets)
		g.GET("/datasets/:dataset", h.dataset)
		g.PUT("/datasets/:dataset", h.saveDataset)
		g.POST("/datasets/:dataset/versions", h.publishDataset)
		g.GET("/datasets/:dataset/versions/:version", h.datasetVersion)
	}
	register(r.Group("/api/v1/tenants/:tid/projects/:pid", h.authorized))
	g := r.Group("/api/v1/tenants/:tid", h.authorized)
	register(g)
	g.GET("/agent-workspace", h.workspace)
}

// workspace 查询企业工作区权限。
// @Summary 查询企业工作区权限
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Success 200 {object} workspaceAccess "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agent-workspace [get]
func (h *Controller) workspace(c *core.Context) { c.JSON(200, c.MustGet("workspaceAccess")) }
