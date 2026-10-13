package platform

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
)

type Template struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AgentID string `json:"agentID"`
	Version int64  `json:"version"`
}

type Config struct {
	CodingURL     string
	CodingToken   string
	TenantURL     string
	TenantToken   string
	TemplateOwner string
	Templates     []Template
}

func (cfg Config) Validate() error {
	u, err := url.Parse(cfg.TenantURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		len(cfg.TenantToken) < 32 ||
		strings.ContainsAny(cfg.TenantToken, "\r\n") ||
		cfg.TemplateOwner == "" {
		return errors.New("invalid platform tenant connection or template owner")
	}
	if cfg.CodingURL != "" || cfg.CodingToken != "" {
		cu, e := url.Parse(cfg.CodingURL)
		if e != nil || cu.Host == "" || (cu.Scheme != "http" && cu.Scheme != "https") || cu.User != nil ||
			cu.RawQuery != "" ||
			cu.Fragment != "" ||
			len(cfg.CodingToken) < 32 ||
			strings.ContainsAny(cfg.CodingToken, "\r\n") {
			return errors.New("invalid Coding environment connection")
		}
	}
	seen := map[string]bool{}
	for _, t := range cfg.Templates {
		if t.ID == "" || seen[t.ID] || t.Name == "" || t.AgentID == "" || t.Version < 1 {
			return errors.New("invalid platform template")
		}
		seen[t.ID] = true
	}
	return nil
}

type controller struct {
	service Service
	cfg     Config
	tenant  *client.Tenant
}

func (h *controller) check(ctx context.Context, path string, body, out any) int {
	return h.tenant.Check(ctx, path, body, out)
}

// Owner namespaces reuse the store's enforced owner isolation. Only the server
// derives this value, after checking the session and current project grant.
func projectOwner(tenant, project string) string {
	raw, _ := json.Marshal([]string{tenant, project})
	return fmt.Sprintf("forge-project:%x", sha256.Sum256(raw))
}

func (h *controller) authorized(c *gin.Context) {
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

func RegisterRoutes(r gin.IRouter, service Service, cfg Config, tenant *client.Tenant) {
	h := &controller{service: service, cfg: cfg, tenant: tenant}
	register := func(g *gin.RouterGroup) {
		g.GET("/agents", h.agents)
		g.POST("/agents", h.createAgent)
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
	g.GET("/agent-templates", h.templates)
	g.GET("/agent-workspace", func(c *gin.Context) { c.JSON(200, c.MustGet("workspaceAccess")) })
}
