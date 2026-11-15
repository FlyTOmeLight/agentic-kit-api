# 阶段 2 最小 Center 验证记录

日期：2026-11-09。状态：**阶段 2 最小控制面在本地通过；生产发布门槛未到**。

验证目标：申请 → 调度 Pod → 激活租约 → 续期 → 执行命令并持久化回执 → 丢响应后重查幂等 → 主动释放回收。数据库为本地 PostgreSQL 17（`deploy/compose.yaml`，`127.0.0.1:55432`），集群为 OrbStack 内置 Kubernetes v1.36.5（context `orbstack`，namespace `sandbox-center`），运行镜像为固定提交加补丁构建的 `agentic-guard-execd:phase2`。

## 交付物

| 层 | 位置 | 内容 |
| --- | --- | --- |
| 生命周期存储 | `internal/sandboxcenter/{types,service}.go` | sandbox/lease/operation 三表事务：Acquire、Activate、Renew、BeginOperation、MarkDispatchStarted、CompleteOperation、MarkOperationUnknown、Release、ConfirmDestroyed |
| 运行时 Provider | `internal/sandboxcenter/provider_k8s.go` | 显式 `--context` 的 kubectl 调用；单容器 guard Pod；initContainer 准备 `/state` 与 `/workspace`；控制通道为 `kubectl exec … /guard call` |
| HTTP 接口 | `internal/sandboxcenter/handler.go` | `POST/GET /v1/sandboxes`、`POST /v1/sandboxes/:id/renew`、`POST /v1/sandboxes/:id/operations`、`GET /v1/sandboxes/:id/operations/:opID`、`DELETE /v1/sandboxes/:id` |
| 装配 | `service/{api,build,routes}.go`、`internal/config/service.go`、`conf/server/config.toml` | `Sandbox.K8sContext` 为空时只提供元数据路由；配置后启用真实调度 |
| 镜像构建 | `docs/sandbox-center/probes/guard-execd/build-image.sh` | 固定提交 `b7d7697…`、UDS 补丁、Linux arm64 静态二进制、digest 固定的基底镜像 |

镜像标识（本地）：`agentic-guard-execd@sha256:2058bb58aae4c7de1a7b20beccecb34f00fc370d5f88fa16bfd1f07186a56604`。该散列标识本次构建，未启用可复现构建参数。

## 已执行检查与观察

| 检查 | 结果 |
| --- | --- |
| 构建与静态检查 | `GOWORK=off go build ./...`、`go vet ./...` 通过 |
| 存储集成测试 | `sandbox/lease/operation` 4 项 PostgreSQL 用例通过（既有） |
| 端到端测试 | `TestSandboxLifecycleE2E` 通过，4.81s 内完成申请、激活、续期、执行、幂等重放、释放 |
| HTTP 申请 | `POST /v1/sandboxes` 返回 `Active`，真实 `podUid`，耗时 1.76s |
| HTTP 续期 | revision 1→2，`POST …/renew` 200（363ms），guard 接受同一 binding 的新 revision |
| HTTP 执行 | `POST …/operations` 返回 `Succeeded`，`receiptStoredAt` 非空；输出含 `uid=65534` 与 `/workspace/http.txt` 写入成功 |
| 幂等重放 | 同一 `requestKey` 重发返回相同 operation id（2.4ms），未第二次派发 |
| 回执重查 | `GET …/operations/:opID` 返回同一终结回执 |
| 释放 | `DELETE /v1/sandboxes/:id` 返回 `Destroyed`，Pod 被删除，namespace 无残留 Pod |
| 身份与能力 | 命令身份 `uid=65534:65534`，`CapEff=0`，`NoNewPrivs=1`，`Seccomp=2` |
| 私有状态 | `/state` 为 `root:root 0700`；工作负载连接 `guard.sock` 返回 `EACCES` |
| 工作区 | `/workspace` 属主 65534，可写；`/root` 写入被拒 |
| PID 1 与根文件系统 | `/proc/1/cmdline` 为 `/guard serve 86400`；`/` 为只读 overlay |

复现命令（本机）：

```sh
# 镜像（首次需要网络下载上游 tarball）
docs/sandbox-center/probes/guard-execd/build-image.sh phase2
kubectl create namespace sandbox-center
kubectl -n sandbox-center apply -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: deny-all}
spec: {podSelector: {}, policyTypes: ["Ingress", "Egress"]}
EOF

# 存储 + 端到端（需要 PostgreSQL）
TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable' \
SANDBOX_E2E_K8S_CONTEXT=orbstack \
  GOWORK=off go test -count=1 -v -timeout 300s -run TestSandboxLifecycleE2E ./internal/sandboxcenter/

# HTTP 闭环
SANDBOX_K8S_CONTEXT=orbstack SANDBOX_K8S_NAMESPACE=sandbox-center \
SANDBOX_GUARD_IMAGE=agentic-guard-execd:phase2 LISTEN_ADDR=127.0.0.1:18082 \
  GOWORK=off go run ./cmd/server
```

## 边界与未覆盖项

- **隔离等级**：本地集群没有 `runsc`/`kata`，`RuntimeClassName` 为空，未验证 gVisor。Pod 靠 drop ALL、只读根文件系统、RuntimeDefault seccomp、独立 PID namespace 和固定身份限制，不等于生产 Hardened RuntimeClass。
- **网络**：Pod 未配置 `hostNetwork`，但 Kubernetes 没有等价于 Docker `--network none` 的开关；隔离依赖 namespace 级 `deny-all` NetworkPolicy。OrbStack 对该策略的实际执行未单独验证，生产需要在目标 CNI 上验收。
- **存储**：`/state` 与 `/workspace` 都是 emptyDir，Pod 删除即丢；没有 PVC、基础层物化或检查点。阶段 2 只验证生命周期，不代表工作区持久化。
- **失败恢复**：Provision 成功后如果 Activate 落库失败，Pod 会残留，当前没有对账器（阶段 3）。执行派发失败会写 `Unknown`，但没有自动补齐机制。
- **续期语义**：租约期限被钳制到 guard 实例硬期限（`MaxLifetime`，默认 24h，减 1 分钟余量）；超过实例寿命的续期不会成功，也没有跨实例迁移。续期事实已落库但 guard 确认失败时，后续同 revision 重放会跳过确认，需要阶段 3 对账器补齐。
- **命令输出**：存储的是 execd 的原始事件流，包含 init/ping/stdout/execution_complete 帧；没有解析成结构化结果，也没有输出上限之外的截断标记。
- **认证授权**：路由沿用当前 API 的中间件现状，尚未接入 Forge 租户身份；`tenantId`/`ownerId` 由调用方提供。
- **整实例停止注入**：从 Pod 内部对 PID 1 发 SIGKILL 不会终止 Guard（PID namespace init 语义），所以本轮停止证据仍是删除 Pod 与 Docker 探针；没有在 K8s 上复现 guard/execd 崩溃注入。
- **Schema**：阶段 2 只 AutoMigrate 三张核心表；设计目录的 18 表 PostgreSQL 契约是目标结构，不是当前服务已建表范围。

## 下一步

1. 阶段 3：Agent API 持久关联 sandbox/lease、worker Backend 接入、双 worker 接管与对账器。
2. 失败注入：guard/execd 崩溃、节点失联、存储阻塞下的 Unknown 与回收路径。
3. 生产 Profile：gVisor 节点池、PVC/基础层物化、检查点与性能门槛。
