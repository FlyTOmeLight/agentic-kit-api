// handler.go exposes the phase-2 sandbox control plane over HTTP:
// acquire, renew, exec and release with durable receipts in PostgreSQL.
package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
)

// Service couples the durable store with the Kubernetes provider.
type Service struct {
	store            *Store
	provider         *Provider
	checkpointConfig CheckpointConfig
}

func NewService(store *Store, provider *Provider) *Service {
	service := &Service{store: store, provider: provider}
	if provider != nil {
		service.checkpointConfig = provider.cfg.Checkpoints
	}
	return service
}

// WithCheckpoints configures the required-save loop. The returned service packs a
// content-addressed checkpoint before unblocking destruction.
func (s *Service) WithCheckpoints(cfg CheckpointConfig) *Service {
	s.checkpointConfig = cfg
	if s.provider != nil {
		provider := *s.provider
		provider.cfg.Checkpoints = cfg
		s.provider = &provider
	}
	return s
}

type Controller struct {
	service *Service
}

func NewController(service *Service) *Controller { return &Controller{service: service} }

// RegisterStoreRoutes installs metadata-only routes when no runtime provider
// is configured (default while the local K8s provider is not enabled).
func RegisterStoreRoutes(v1 *core.RouterGroup, store *Store) {
	h := &storeController{store: store}
	g := v1.Group("/sandboxes")
	g.POST("", h.acquire)
	g.GET("/:id", h.get)
	g.POST("/:id/renew", h.renew)
	g.POST("/:id/operations", h.exec)
	g.GET("/:id/operations/:opID", h.getOperation)
	g.DELETE("/:id", h.release)
}

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	g := v1.Group("/sandboxes")
	g.POST("", h.acquire)
	g.GET("/:id", h.get)
	g.POST("/:id/renew", h.renew)
	g.POST("/:id/operations", h.exec)
	g.GET("/:id/operations/:opID", h.getOperation)
	g.DELETE("/:id", h.release)
}

func respondErr(c *core.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		serrors.Abort(c, 400, "InvalidParameter", err.Error())
	case errors.Is(err, ErrNotFound):
		serrors.Abort(c, 404, "NotFound", "sandbox or operation not found")
	case errors.Is(err, ErrConflict):
		serrors.Abort(c, 409, "Conflict", err.Error())
	case errors.Is(err, ErrLeaseExpired):
		serrors.Abort(c, 409, "LeaseExpired", "lease expired; outcome is closed to new work")
	case errors.Is(err, ErrLeaseClosed):
		serrors.Abort(c, 409, "LeaseClosed", "lease is closed")
	case errors.Is(err, ErrNotReady):
		serrors.Abort(c, 409, "NotReady", "sandbox is not active yet")
	case errors.Is(err, ErrSaveBlocked):
		serrors.Abort(c, 409, "SaveBlocked", err.Error())
	case errors.Is(err, ErrOutcomeUnknown):
		serrors.Abort(c, 502, "OutcomeUnknown", "query the original operation receipt")
	default:
		serrors.AbortWithError(c, err)
	}
}

type acquireRequest struct {
	TenantID             string `json:"tenantId"`
	OwnerID              string `json:"ownerId"`
	RequestKey           string `json:"requestKey"`
	ProfileID            string `json:"profileId"`
	WorkerID             string `json:"workerId"`
	RunID                string `json:"runId"`
	RunEpoch             int64  `json:"runEpoch"`
	TTLSeconds           int64  `json:"ttlSeconds"`
	RequiredSave         bool   `json:"requiredSave"`
	RestoreFromSandboxID string `json:"restoreFromSandboxId"`
}

// acquire 申请沙箱。
// @Summary 申请沙箱
// @Description 按 (ownerId, requestKey) 幂等；首次申请会调度 Pod 并激活租约。
// @Tags Sandbox
// @Accept json
// @Produce json
// @Param request body sandboxcenter.acquireRequest true "申请参数"
// @Success 200 {object} sandboxcenter.sandboxView
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 409 {object} serrors.Error "状态或幂等冲突"
// @Router /v1/sandboxes [post]
func (h *Controller) acquire(c *core.Context) {
	var req acquireRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 3600
	}
	ctx := c.Request.Context()
	if req.RestoreFromSandboxID != "" {
		if err := h.service.checkpointConfig.valid(); err != nil {
			respondErr(c, ErrNotReady)
			return
		}
	}
	sb, _, err := h.service.store.Acquire(ctx, AcquireInput{
		TenantID:             req.TenantID,
		OwnerID:              req.OwnerID,
		RequestKey:           req.RequestKey,
		ProfileID:            req.ProfileID,
		WorkerID:             req.WorkerID,
		RunID:                req.RunID,
		RunEpoch:             req.RunEpoch,
		RequiredSave:         req.RequiredSave,
		RestoreFromSandboxID: req.RestoreFromSandboxID,
		HardExpiresAt:        time.Now().Add(time.Duration(req.TTLSeconds) * time.Second).UTC(),
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	if sb.Status == SandboxStatusProvisioning {
		if err := h.service.provision(ctx, sb, req); err != nil {
			respondErr(c, err)
			return
		}
		fresh, err := h.service.store.GetSandbox(ctx, sb.ID)
		if err != nil {
			respondErr(c, err)
			return
		}
		sb = fresh
	}
	lease, _ := h.service.store.GetLease(ctx, sb.LeaseID)
	c.JSON(http.StatusOK, sandboxView{Sandbox: sb, Lease: lease})
}

// provision schedules the pod, then durably activates the sandbox. If the pod
// comes up but Activate fails we leave the pod for reconciliation (phase 2
// keeps this synchronous; the reconciler is stage 3).
func (s *Service) provision(ctx context.Context, sb *Sandbox, req acquireRequest) error {
	if sb.ResumeCheckpoint != "" {
		if err := s.checkpointConfig.valid(); err != nil {
			return err
		}
	}
	auth, err := s.store.AuthorizeRun(ctx, sb.OwnerID, req.RunID, req.WorkerID, req.RunEpoch)
	if err != nil {
		return err
	}
	requested := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second).UTC()
	requested = clampToRunAuth(requested, auth)
	deadline := s.provider.LeaseDeadline(time.Now(), requested)
	runID := auth.RunID
	epoch := auth.Epoch
	admission := admissionRequest{
		SandboxID:      sb.ID,
		RunID:          runID,
		WorkerID:       req.WorkerID,
		Epoch:          epoch,
		LeaseID:        "pending",
		FencingToken:   epoch,
		AdmissionEpoch: 1,
		Generation:     sb.Generation + 1,
		RuntimeBootID:  sb.ID,
		Revision:       1,
		Deadline:       deadline,
		RequestKey:     req.RequestKey,
	}
	if sb.RequiredSave {
		workspaceUID, err := s.provider.ensureWorkspace(ctx, *sb)
		if err != nil {
			return fmt.Errorf("provision workspace: %w", err)
		}
		if err := s.store.recordPhysicalIdentity(ctx, sb.ID, sb.PodUID, workspaceUID); err != nil {
			return err
		}
		sb.WorkspaceUID = workspaceUID
	}
	podUID, err := s.provider.Provision(ctx, *sb, admission, s.checkpointConfig)
	if err != nil {
		return fmt.Errorf("provision pod: %w", err)
	}
	if err := s.store.recordPhysicalIdentity(ctx, sb.ID, podUID, sb.WorkspaceUID); err != nil {
		return err
	}
	sb.PodUID = podUID
	if err := s.provider.waitReady(ctx, *sb); err != nil {
		return err
	}
	lease, err := s.store.ActivateForRun(ctx, sb.ID, podUID, runID, req.WorkerID, req.RunEpoch, deadline)
	if err != nil {
		return err
	}
	// Now that the lease fact is durable, admit the real lease into the guard.
	admission.LeaseID = lease.ID
	admission.Deadline = lease.HardExpiresAt
	if err := s.provider.admit(ctx, *sb, admission); err != nil {
		return fmt.Errorf("admit lease into guard: %w", err)
	}
	return nil
}

type sandboxView struct {
	Sandbox *Sandbox `json:"sandbox"`
	Lease   *Lease   `json:"lease,omitempty"`
}

// get 查询沙箱与当前租约。
// @Summary 查询沙箱
// @Tags Sandbox
// @Produce json
// @Param id path string true "沙箱 ID"
// @Success 200 {object} sandboxcenter.sandboxView
// @Failure 404 {object} serrors.Error "资源不存在"
// @Router /v1/sandboxes/{id} [get]
func (h *Controller) get(c *core.Context) {
	var req struct {
		ID string `uri:"id"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	sb, err := h.service.store.GetSandbox(c.Request.Context(), req.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	var lease *Lease
	if sb.LeaseID != "" {
		lease, _ = h.service.store.GetLease(c.Request.Context(), sb.LeaseID)
	}
	c.JSON(http.StatusOK, sandboxView{Sandbox: sb, Lease: lease})
}

type renewRequest struct {
	ID         string `uri:"id"`
	Revision   int64  `json:"revision"`
	RequestKey string `json:"requestKey"`
	TTLSeconds int64  `json:"ttlSeconds"`
}

// renew 续租：先写控制面事实，再向 guard 确认。
// @Summary 续租
// @Description revision 单调递增；相同 revision 与 requestKey 重放返回原接受事实。
// @Tags Sandbox
// @Accept json
// @Produce json
// @Param id path string true "沙箱 ID"
// @Param request body sandboxcenter.renewRequest true "续期参数"
// @Success 200 {object} sandboxcenter.Lease
// @Failure 409 {object} serrors.Error "租约已关闭或过期"
// @Router /v1/sandboxes/{id}/renew [post]
func (h *Controller) renew(c *core.Context) {
	var req renewRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	ctx := c.Request.Context()
	sb, err := h.service.store.GetSandbox(ctx, req.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	if sb.LeaseID == "" {
		respondErr(c, ErrNotReady)
		return
	}
	lease, err := h.service.store.GetLease(ctx, sb.LeaseID)
	if err != nil {
		respondErr(c, err)
		return
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 3600
	}
	newDeadline := h.service.provider.LeaseDeadline(
		lease.CreatedAt,
		time.Now().Add(time.Duration(req.TTLSeconds)*time.Second).UTC(),
	)
	updated, replayed, err := h.service.store.Renew(
		ctx,
		lease.ID,
		req.Revision,
		req.RequestKey,
		hashRenew(*lease, req.Revision),
		newDeadline,
	)
	if err != nil {
		respondErr(c, err)
		return
	}
	if !replayed {
		admission := admissionRequest{
			SandboxID:      sb.ID,
			RunID:          lease.HolderRunID,
			WorkerID:       lease.WorkerID,
			Epoch:          lease.HolderRunEpoch,
			LeaseID:        lease.ID,
			FencingToken:   lease.HolderRunEpoch,
			AdmissionEpoch: 1,
			Generation:     sb.Generation,
			RuntimeBootID:  sb.ID,
			Revision:       updated.Revision,
			Deadline:       updated.HardExpiresAt,
			RequestKey:     req.RequestKey,
		}
		if err := h.service.provider.admit(ctx, *sb, admission); err != nil {
			// Control-plane fact is durable; guard will catch up on next renew
			// or the sandbox stops at its own hard deadline. Report Unknown.
			respondErr(
				c,
				fmt.Errorf("%w: renew fact stored but guard confirm failed: %v", ErrNotReady, err),
			)
			return
		}
	}
	c.JSON(http.StatusOK, updated)
}

type execRequest struct {
	ID                 string `uri:"id"`
	RequestKey         string `json:"requestKey"`
	Script             string `json:"script"`
	EvaluationWorkerID string `json:"evaluationWorkerId,omitempty"`
	EvaluationEpoch    int64  `json:"evaluationEpoch,omitempty"`
}

// exec 在沙箱中执行命令：先落回执，再派发，再写终结回执。
// @Summary 执行命令
// @Description 先持久化 intent 与 dispatch 回执再派发；派发结果不可知时保留 Unknown。
// @Tags Sandbox
// @Accept json
// @Produce json
// @Param id path string true "沙箱 ID"
// @Param request body sandboxcenter.execRequest true "执行参数"
// @Success 200 {object} sandboxcenter.Operation
// @Failure 409 {object} serrors.Error "沙箱未激活"
// @Failure 502 {object} serrors.Error "派发结果未知"
// @Router /v1/sandboxes/{id}/operations [post]
func (h *Controller) exec(c *core.Context) {
	var req execRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if strings.TrimSpace(req.Script) == "" || req.RequestKey == "" {
		respondErr(c, fmt.Errorf("%w: requestKey and script are required", ErrInvalidInput))
		return
	}
	ctx := c.Request.Context()
	sb, err := h.service.store.GetSandbox(ctx, req.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	if sb.Status != SandboxStatusActive {
		respondErr(c, ErrNotReady)
		return
	}
	claim := EvaluationClaim{WorkerID: req.EvaluationWorkerID, Epoch: req.EvaluationEpoch}
	op, err := h.service.store.BeginOperation(
		ctx,
		sb.LeaseID,
		req.RequestKey,
		"command",
		payloadHashOf(req.Script),
		claim,
	)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.Header("X-Sandbox-Operation-ID", op.ID)
	// Replay: terminal receipt already durable.
	if op.Status == OpStatusSucceeded || op.Status == OpStatusFailed {
		c.JSON(http.StatusOK, op)
		return
	}
	if op.Status != OpStatusPending {
		serrors.Abort(
			c,
			http.StatusBadGateway,
			"OutcomeUnknown",
			"operation outcome unknown; query the original operation",
		)
		return
	}

	if op.Status == OpStatusPending {
		if err := h.service.store.MarkDispatchStarted(ctx, op.ID, "guard:"+sb.PodUID, claim); err != nil {
			respondErr(c, err)
			return
		}
	}
	out, _, err := h.service.provider.guardCall(ctx, *sb, "/command", map[string]string{
		"operationId": op.ID,
		"script":      req.Script,
	})
	if err != nil {
		// Dispatch outcome is not knowable locally; the durable fact must say so.
		_ = h.service.store.MarkOperationUnknown(ctx, op.ID, "dispatch outcome unknown: "+err.Error())
		serrors.Abort(
			c,
			http.StatusBadGateway,
			"OutcomeUnknown",
			"operation outcome unknown; query the original operation",
		)
		return
	}
	exitCode, errStr := parseExecdOutput(string(out))
	if err := h.service.store.CompleteOperation(ctx, op.ID, exitCode, string(out), errStr); err != nil {
		_ = h.service.store.MarkOperationUnknown(ctx, op.ID, "terminal receipt not durable: "+err.Error())
		serrors.Abort(
			c,
			http.StatusInternalServerError,
			"ReceiptNotDurable",
			"terminal receipt is not durable; outcome unknown",
		)
		return
	}
	fresh, _ := h.service.store.GetOperation(ctx, op.ID)
	c.JSON(http.StatusOK, fresh)
}

// getOperation 查询操作回执。
// @Summary 查询操作回执
// @Tags Sandbox
// @Produce json
// @Param id path string true "沙箱 ID"
// @Param opID path string true "操作 ID"
// @Success 200 {object} sandboxcenter.Operation
// @Failure 404 {object} serrors.Error "资源不存在"
// @Router /v1/sandboxes/{id}/operations/{opID} [get]
func (h *Controller) getOperation(c *core.Context) {
	var req struct {
		ID   string `uri:"id"`
		OpID string `uri:"opID"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	op, err := h.service.store.GetOperation(c.Request.Context(), req.OpID)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, op)
}

// release 关闭 admission 并回收 Pod。
// @Summary 释放沙箱
// @Tags Sandbox
// @Produce json
// @Param id path string true "沙箱 ID"
// @Success 200 {object} sandboxcenter.Sandbox
// @Router /v1/sandboxes/{id} [delete]
func (h *Controller) release(c *core.Context) {
	var req struct {
		ID string `uri:"id"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	ctx := c.Request.Context()
	sb, err := h.service.Release(ctx, req.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, sb)
}

// storeController serves durable metadata without a runtime provider.
// Provisioning, exec and release report NotReady until a provider is wired.
type storeController struct {
	store *Store
}

func (h *storeController) acquire(c *core.Context) {
	var req acquireRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 3600
	}
	sb, _, err := h.store.Acquire(c.Request.Context(), AcquireInput{
		TenantID:             req.TenantID,
		OwnerID:              req.OwnerID,
		RequestKey:           req.RequestKey,
		ProfileID:            req.ProfileID,
		WorkerID:             req.WorkerID,
		RunID:                req.RunID,
		RunEpoch:             req.RunEpoch,
		RequiredSave:         req.RequiredSave,
		RestoreFromSandboxID: req.RestoreFromSandboxID,
		HardExpiresAt:        time.Now().Add(time.Duration(req.TTLSeconds) * time.Second).UTC(),
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, sandboxView{Sandbox: sb})
}

func (h *storeController) get(c *core.Context) {
	var req struct {
		ID string `uri:"id"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	sb, err := h.store.GetSandbox(c.Request.Context(), req.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	var lease *Lease
	if sb.LeaseID != "" {
		lease, _ = h.store.GetLease(c.Request.Context(), sb.LeaseID)
	}
	c.JSON(http.StatusOK, sandboxView{Sandbox: sb, Lease: lease})
}

func (h *storeController) renew(c *core.Context) {
	var req renewRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	ctx := c.Request.Context()
	sb, err := h.store.GetSandbox(ctx, req.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	if sb.LeaseID == "" {
		respondErr(c, ErrNotReady)
		return
	}
	lease, err := h.store.GetLease(ctx, sb.LeaseID)
	if err != nil {
		respondErr(c, err)
		return
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 3600
	}
	newDeadline := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second).UTC()
	updated, _, err := h.store.Renew(
		ctx,
		lease.ID,
		req.Revision,
		req.RequestKey,
		hashRenew(*lease, req.Revision),
		newDeadline,
	)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *storeController) getOperation(c *core.Context) {
	var req struct {
		ID   string `uri:"id"`
		OpID string `uri:"opID"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	op, err := h.store.GetOperation(c.Request.Context(), req.OpID)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, op)
}

// Without a runtime provider the durable metadata remains queryable, but no
// execution or destruction can be performed or confirmed.
func (h *storeController) exec(c *core.Context) {
	respondErr(c, ErrNotReady)
}

func (h *storeController) release(c *core.Context) {
	respondErr(c, ErrNotReady)
}
