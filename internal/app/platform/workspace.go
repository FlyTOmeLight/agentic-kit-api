package platform

import (
	"crypto/sha256"
	"fmt"
	"strconv"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type workspaceAccess struct {
	Allowed  bool               `json:"allowed"`
	CanEdit  bool               `json:"canEdit"`
	Projects []workspaceProject `json:"projects"`
}

type workspaceProject struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Level int    `json:"level"`
}

func tenantOwner(tenant string) string {
	return fmt.Sprintf("forge-tenant:%x", sha256.Sum256([]byte(tenant)))
}

func allowedProjects(c *gin.Context) []string {
	value, ok := c.Get("workspaceAccess")
	if !ok {
		return nil
	}
	ids := []string{}
	for _, p := range value.(workspaceAccess).Projects {
		if p.Level >= 20 {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func (h *controller) environmentProject(c *gin.Context, requested string) string {
	if c.Param("pid") != "" {
		return c.Param("pid")
	}
	return requested
}

func (h *controller) environmentAccess(c *gin.Context, requested, kind string, write bool) bool {
	legacy := c.Param("pid")
	if legacy != "" {
		if requested != "" && requested != legacy {
			serrors.Abort(c, 403, "ProjectMismatch", "历史受限资产只能用于原项目")
			return false
		}
		return true
	}
	if kind == "single_turn" && requested == "" {
		return true
	}
	if requested == "" {
		serrors.Abort(c, 400, "ProjectRequired", "Coding 评测需要选择有权限的仓库项目")
		return false
	}
	value, ok := c.Get("workspaceAccess")
	if ok {
		minimum := 20
		if write {
			minimum = 30
		}
		for _, p := range value.(workspaceAccess).Projects {
			if p.ID == requested && p.Level >= minimum {
				return true
			}
		}
	}
	serrors.Abort(c, 403, "EnvironmentAccessRequired", "没有所选项目的评测权限")
	return false
}

func (h *controller) freezeDataset(c *gin.Context, s *sdk.EvaluationSuite) bool {
	if s.Dataset.ID == "" {
		if c.Param("pid") != "" {
			return true
		}
		serrors.Abort(c, 400, "DatasetVersionRequired", "请选择已发布的数据集版本")
		return false
	}
	n, err := strconv.ParseInt(s.Dataset.Version, 10, 64)
	if err != nil || n < 1 {
		serrors.Abort(c, 400, "InvalidDatasetVersion", "数据集版本无效")
		return false
	}
	version, err := h.service.DatasetVersion(c.Request.Context(), c.GetString("projectOwner"), s.Dataset.ID, n)
	if err != nil {
		apperror.Respond(c, err)
		return false
	}
	s.Dataset = version.Snapshot
	return true
}

func (h *controller) datasets(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	if !ok {
		return
	}
	rows, err := h.service.Datasets(c.Request.Context(), c.GetString("projectOwner"), c.Query("after"), limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Dataset]{Items: rows})
}

func (h *controller) dataset(c *gin.Context) {
	row, err := h.service.Dataset(c.Request.Context(), c.GetString("projectOwner"), c.Param("dataset"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

func (h *controller) saveDataset(c *gin.Context) {
	var req sdk.SaveDatasetRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	row, err := h.service.SaveDataset(c.Request.Context(), c.GetString("projectOwner"), c.Param("dataset"), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

func (h *controller) publishDataset(c *gin.Context) {
	var req sdk.PublishAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	row, err := h.service.PublishDataset(
		c.Request.Context(),
		c.GetString("projectOwner"),
		c.Param("dataset"),
		req.Revision,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

func (h *controller) datasetVersion(c *gin.Context) {
	n, err := strconv.ParseInt(c.Param("version"), 10, 64)
	if err != nil || n < 1 {
		serrors.Abort(c, 400, "InvalidVersion", "版本无效")
		return
	}
	row, err := h.service.DatasetVersion(c.Request.Context(), c.GetString("projectOwner"), c.Param("dataset"), n)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}
