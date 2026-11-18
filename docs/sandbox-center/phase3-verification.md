# 阶段 3 Agent 集成验证记录

日期：2026-11-09。状态：**阶段 3 最小集成在本地通过；生产发布门槛未到**。

验证目标：Run 与沙箱环境的持久关联、worker 通过 Sandbox Backend 执行命令、跨进程接管时绑定不失权、对账器收敛孤儿资源。数据库为本地 PostgreSQL 17（`127.0.0.1:55432`），集群为 OrbStack Kubernetes（context `orbstack`），镜像沿用阶段 2 的 `agentic-guard-execd:phase2`。

## 交付物

| 层 | 位置 | 内容 |
| --- | --- | --- |
| 环境关联存储 | `internal/table/environment.go`、`pkg/durable/environment.go` | `durable_run_environment` 表：Prepare/Activate/Get/Active/Released/Unfinished，Prepare/Activate 走 `lockLease`，绑定到领取 epoch |
| 对账器 | `internal/sandboxcenter/reconciler.go` | 周期收敛三类分歧：超时未激活的 Provisioning 孤儿 Pod、未确认销毁的 Releasing、租约过期的 Active |
| 存储查询 | `internal/sandboxcenter/service.go` | `PendingReconciliation` 按数据库时钟选出三类待对账记录；`markLost` 只关闭从未激活的 Provisioning |
| Provider | `internal/sandboxcenter/provider_k8s.go` | 新增 `PodExists` 供对账取集群事实 |
| 装配 | `service/api.go` | 配置 `[Sandbox].K8sContext` 后 reconciler 随 API 生命周期启动，默认 1 分钟间隔、10 分钟孤儿宽限 |
| SDK | `sdk/go/agenticv1/agentic_*_sandbox.go` | `SandboxAPI`：Acquire/Get/Renew/Exec/GetOperation/Release，全部按 request key 幂等 |
| 绑定校验 | `pkg/execution/types.go`、`pkg/durable/prepare.go` | `FilesystemBinding.Provider` 新增 `Sandbox`，可选 `ProfileID`；AIO 绑定拒绝 `ProfileID` |
| worker Backend | `agentic-kit-worker/internal/adapters/sandbox/backend.go` | 环境生命周期归属 Backend：意图先于网络调用落库，执行经 runID:callID 幂等键，Close 在中心确认销毁后才标记 Released |
| worker 装配 | `agentic-kit-worker/internal/adapters/eino.go`、`internal/engine/engine.go`、`internal/service/build.go` | `Provider=Sandbox` 时经 `BuildRunContext` 取得 run 领取身份装配 Backend；Evaluator 改用 sandbox-center SDK |

## 已执行检查与观察

| 检查 | 结果 |
| --- | --- |
| API 全仓测试 | 202 项通过（`GOWORK=off go test ./...`，含 PostgreSQL 集成） |
| worker 全仓测试 | 63 项通过（含 PostgreSQL 集成与引擎接管用例） |
| 环境关联集成 | `TestEnvironmentPrepareReplayAndActivate`、`TestEnvironmentBoundToClaimEpoch` 通过：同 key 重放返回原记录；新 epoch 不能激活旧绑定；Released 后不再计入未完成 |
| 对账范围 | `TestPendingReconciliationScopes` 通过：新 Provisioning 不误伤；超时孤儿、过期租约、未确认 Releasing 进入对账集；ConfirmDestroyed 后离开集合 |
| 接管 | `TestSandboxTakeoverKeepsEnvironmentBinding`（worker）通过：worker A 崩溃后 worker B 新 epoch 接管，`BuildRunContext` 携带新领取身份；旧 epoch 的环境记录可见但不可被新 epoch 变更；`UnfinishedEnvironments` 供对账回收 |
| 真实端到端冒烟 | `TestSandboxBackendAgainstRealCenter`（tag `sandboxe2e`）通过：真实 API + OrbStack 集群上完成 申请 Pod → `id -u; pwd` 执行（uid 65534、/workspace）→ 同 operation key 重放不重跑 → 写读 `smoke.txt` → Close 释放 Pod 并关闭环境记录；冒烟后 namespace 无残留 Pod |

冒烟复现（本机）：

```sh
# 1. 启动带 sandbox provider 的 API（前台保持运行）
LISTEN_ADDR=127.0.0.1:18084 SANDBOX_K8S_CONTEXT=orbstack \
SANDBOX_K8S_NAMESPACE=sandbox-center SANDBOX_GUARD_IMAGE=agentic-guard-execd:phase2 \
  GOWORK=off go run ./cmd/server

# 2. 在 agentic-kit-worker 仓执行冒烟
TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable' \
SANDBOX_CENTER_URL=http://127.0.0.1:18084 \
  go test -tags sandboxe2e -run TestSandboxBackendAgainstRealCenter -v ./internal/adapters/sandbox/
```

## 边界与未覆盖项

- **Backend 命令面**：`execute`、`write`、`read`、`ls` 已实现；`edit`、`glob`、`grep` 显式拒绝。`edit` 不能用 shell 部分替换冒充幂等，需要 execd 文件 API 后再开放。
- **输出解析**：`parseExecOutput` 把 execd 事件流（`stdout.text` 帧）还原为纯输出；`execution_complete` 帧当前不携带 exit code，非零退出依赖中心返回的 `Failed` 状态。
- **接管语义**：新 worker 认领后**重新申请**（acquire 按 request key 重放返回原沙箱）而不是直接续约旧租约；旧 worker 的 `BeginStep`/`PrepareEnvironment` 等 lease 校验路径全部失权。续期确认丢失时对账的补齐（阶段 2 遗留）仍只对账，不自动补 admit。
- **授权**：阶段 3 当时中心路由未接 Forge 租户身份。后续执行权校验和本地验证见[阶段 4 记录](phase4-lease-verification.md)；身份鉴权仍未接入。
- **释放时机**：~~Backend.Close 依赖装配方调用~~ engine 已在 Run 结束 defer 中经 `releaseEnvironments` 释放环境（2026-11-09）；worker 进程崩溃时环境由 reconciler 按租约过期回收，崩溃后 reconciler 同步终结环境记录（`CloseEnvironmentsForSandbox`），但真实 K8s 下的 kill -9 崩溃时序未实测。
- **Evaluator**：目标验收命令经 `HTTPSandboxRunner` 走中心 HTTP；`SANDBOX_CENTER_URL` 为空时保持 fail-closed `UnavailableEvaluator`。

## 下一步

1. ~~阶段 3 收尾：Backend.Close 接入 engine 的 Run 结束 defer、环境记录对账闭环~~（2026-11-09 已完成：engine 经 `releaseEnvironments` 在 Run 结束时调用环境 backend 的 `Close`；reconciler 在 sandbox Lost/Destroyed 收敛后同步把 `durable_run_environment` 标记 Lost/Released，见 `internal/sandboxcenter/environment.go` 与 `TestCloseEnvironmentsForSandbox`）。
2. 阶段 4：必要保存链路（检查点）、gVisor RuntimeClass、PVC/基础层物化与性能门槛。
3. 授权：Forge 租户身份接入中心路由；执行权校验的现状及验收期限限制见[阶段 4 记录](phase4-lease-verification.md)。
