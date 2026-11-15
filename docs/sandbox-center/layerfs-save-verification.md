# LayerFS 自动保存与申请恢复

日期：2026-11-09。状态：必要保存和新沙箱申请恢复均已接入，验证环境为本地单节点 OrbStack、PostgreSQL 与实际 HTTP/SDK。

## 流程

申请时声明 `requiredSave: true` 的沙箱在释放或到期时进入 `SaveBlocked`：Guard `/stop` 停止 PID 1，工作区 PVC 保留。停止证据（`stoppedAt`）入库后，Reconciler 的 SaveBlocked 分支驱动 Saver：

1. 为每个沙箱创建一个 `saver-<sandboxID>` Pod，镜像 `agentic-sandbox-saver`（同一 digest 固定的 server 基础镜像 + LayerFS 检查点工具 + Go 包装器）。
2. Pod 只读挂载 `workspace-<sandboxID>`，读写挂载检查点存储。本地单节点使用宿主 `hostPath`（默认 `/private/tmp/sandbox-center-checkpoints`），生产替换为对象存储。
3. Pod 内运行 `layerfs_checkpoint.py pack`，以空基线常量 `sha256:<64 个 0>` 打包工作区为 OCI tar 层和 manifest，两个对象以内容寻址写入存储。
4. Saver 从 Pod 日志读取 `checkpoint: sha256:<manifestDigest>:<layerDigest>`，校验格式后调用 `confirmRequiredSave`：同一事务内核对 generation、version、停止证据，写入 `savedCheckpoint` 并转为 `Releasing`。
5. 检查点事务提交后删除 Saver Pod，再按 UID precondition 删除原 Pod、PVC，最终转为 `Destroyed`。确认后进程退出或删除失败时，`Releasing` 分支和同步 Release 都继续清理 Saver，再回收工作区。

检查点标识是内容寻址的：manifest 摘要与 layer 摘要拼合。恢复时按 manifest 摘要读 manifest，按其内嵌的 layer 摘要读层文件，工具逐项校验摘要、路径、whiteout、xattr 与硬链接，任何篡改或损坏都会失败。工作区树的摘要由 `layerfs_checkpoint.py digest` 另行计算，可与恢复结果比对。

Saver 需要 `DAC_OVERRIDE`：沙箱初始化把 `/workspace` 交给工作负载身份 65534、模式 0750，Saver 以 root 运行但丢弃全部能力后无法读取，只加回 `DAC_OVERRIDE`。Saver Pod 设 `activeDeadlineSeconds`，卡死的实例终态化并由控制面报告。

已停止的 `SaveBlocked` 记录继续进入对账队列。每轮 Save 使用 PostgreSQL advisory transaction try-lock 串行推进同一沙箱，锁内重读状态；打包 Pod 尚未完成时结束本轮，不等待整个打包过程。检查点确认随事务提交，之后才删除已观察的 Pod UID。迟到的旧快照不能在保存完成后重新创建 Saver；重启后的清理还核验 component 和 generation 标签。

## 新沙箱如何恢复

调用 `POST /v1/sandboxes` 或 `client.Sandboxes.Acquire`，设置 `requiredSave: true` 和 `restoreFromSandboxId`。Run 的冻结 `FilesystemBinding` 必须使用 `Sandbox`、`/workspace`，并匹配来源 ID 和 Profile。中心校验有效 Run claim、相同 tenant/owner/Profile/版本、来源停止与销毁事实及合法检查点，再持久化来源 ID 和 `resumeCheckpoint`；调用方不能选择任意存储路径。

恢复使用新沙箱的 init container。它只读挂载检查点目录，校验 manifest、layer 摘要与基础层，把完整工作区恢复到新 PVC 内的临时目录，再原子重命名为 `workspace`。Guard 通过 PVC subPath 使用恢复后的目录，不挂载检查点存储。初始化重试发现已完成的工作区时不重新覆盖文件，工作区根目录和内部文件的权限、属主、硬链接、软链接均保留。

恢复失败时 Guard 不启动，不发放 lease。尚未激活的恢复实例在释放或超出 provisioning 宽限期后，先关闭申请状态，再删除原 UID 的 Pod 与 PVC，记录为 `Lost`；不会把半恢复目录保存成新检查点。原来源检查点不变。调用方需要重新申请时使用新 requestKey。

服务配置需同时设置 `[Sandbox].CheckpointImage` 和绝对路径 `CheckpointStoreHostPath`，并启用 K8sContext。对应环境变量为 `SANDBOX_CHECKPOINT_IMAGE`、`SANDBOX_CHECKPOINT_STORE_HOST_PATH`；保存与恢复使用同一镜像和存储配置。默认留空，未启用时已有的必要保存保护继续保留。

## 已执行验证

- PostgreSQL：checkpoint 格式校验（`validateCheckpointID` 拒绝空、缺段、非 sha256、非小写 hex）、`Save` 拒绝未阻塞或未停止的沙箱、`confirmRequiredSave` 接受合法 LayerFS checkpoint 并转为 Releasing。
- 真实 Kubernetes 端到端 `TestRequiredSaveLayerfsE2E`：申请 RequiredSave 沙箱，写入目录树、权限、符号链接和不同属主文件；释放进入 SaveBlocked 并停止；Reconciler 驱动 Saver Pod 打包，确认 checkpoint 入库，销毁原 Pod 与 PVC；用确认的 manifest 摘要和空基线在新 Pod 中 `restore`，逐项核对恢复的目录、`main.go` 内容、`main.c`、`design.md` 与符号链接 `entry.go`。
- `TestCheckpointRestoreHTTPE2E` 使用真实 HTTP 路由和公开 SDK：沙箱 A 写入并保存、销毁；新 Run 冻结来源并申请 B；验证内容、目录、模式、属主和软硬链接；修改 B 后重复申请，确认未覆盖修改；B 再次保存并回收。另覆盖检查点对象缺失时不激活、对账清理到 `Lost`，以及迟到 Save 不重建 Saver。
- `service.TestGoalWorkspaceWorkerKubernetesE2E` 启动真实 Worker 二进制，通过 HTTP/SDK 与真实 Kubernetes 串联 Goal 的两轮执行：首轮写入随机内容并验收 Failed；保存未确认时 `QueueGoalIteration` 返回 `ErrNoWork`。测试在待保存屏障强杀该 Worker，启动新 Worker 后才启动 Reconciler；Reconciler 自动保存、销毁并关闭环境，新 Worker 自动创建下一 Run，恢复首轮文件后修复并验收 Passed。测试核对两轮 Run 的 `evaluationWorkerID` 非空且不同、冻结恢复来源、检查点继承、环境保存回执及两组 Pod/Saver/PVC 全部回收，未手动写入检查点或创建下一 Run。模型使用固定响应的 HTTP 测试服务，实际工具命令与验收命令均在 Guard 沙箱内执行。本机 OrbStack 上以 `-race` 运行通过；测试对账周期为 500ms，生产默认仍为一分钟。
- PostgreSQL 回归覆盖来源与冻结绑定校验、申请幂等、已停止记录继续调度保存、并发 Saver try-lock 和未完成恢复禁止发布检查点。
- 本轮最终检查：启用本机 PostgreSQL/Redis 的全仓 `go test -race -count=1 ./...`，385 个测试/子测试通过，16 个外部运行时用例因未启用对应开关而跳过；`go vet ./...`、服务构建与 `make doc` 通过。上述 HTTP 恢复 E2E 单独启用 OrbStack 执行通过，主用例 40.91 秒，其中新沙箱申请至 Active 7.86 秒、缺失检查点失败及清理子用例 12.18 秒。这是预置本地镜像、小文件工作区的一次观测。

## 源码

- `internal/sandboxcenter/saver.go`：Saver 的 pod 生命周期、checkpoint 确认与清理。
- `internal/sandboxcenter/reconciler.go`：SaveBlocked 分支在停止后驱动 `Save`。
- `docs/sandbox-center/probes/saver/main.go`：Pod 内包装器，调用打包工具并输出 checkpoint 标识。
- `docs/sandbox-center/probes/saver/build-image.sh`：构建 `agentic-sandbox-saver:<tag>`。
- `internal/sandboxcenter/saver_e2e_test.go`：端到端用例。
- `internal/sandboxcenter/restore.go`：来源授权、原子恢复与未激活实例清理。
- `internal/sandboxcenter/restore_e2e_test.go`：HTTP/SDK 跨 Run 恢复与失败清理。
- `service/goal_workspace_e2e_test.go`：真实 Worker 进程驱动的 Goal 跨 Run 工作区恢复验收。

## 复跑 Goal 跨 Run 验收

先准备前述 Guard、Saver 镜像、单节点检查点目录及本地 PostgreSQL，再从 API 仓库根目录执行。Worker 构建使用现有 `go.work` 联合当前两仓源码，API 测试关闭 workspace。以下命令为 API 测试进程启用 `-race`，Worker 子进程未启用竞态检测；这项验证不证明 Worker 固定的已发布 API 依赖包含新增契约。

```bash
GOCACHE=/private/tmp/go-cache GOWORK="$PWD/go.work" \
  go -C ../agentic-kit-worker build -o /private/tmp/agentic-sandbox-goal-worker ./cmd/worker

TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable' \
SANDBOX_E2E_K8S_CONTEXT=orbstack \
SANDBOX_E2E_WORKER_BINARY=/private/tmp/agentic-sandbox-goal-worker \
GOCACHE=/private/tmp/go-cache GOWORK=off \
  go test -race -count=1 -timeout=5m -run '^TestGoalWorkspaceWorkerKubernetesE2E$' -v ./service
```

测试使用隔离 schema，退出时停止 Worker、Reconciler 并清理本次 Pod/PVC。也可通过 `SANDBOX_E2E_GUARD_IMAGE`、`SANDBOX_E2E_SAVER_IMAGE`、`SANDBOX_E2E_CHECKPOINT_HOST_PATH` 指定已准备的镜像与单节点存储路径。未提供集群或 Worker 路径时跳过，不算通过。

## 未覆盖

- 多节点集群的共享检查点存储（当前是单节点 hostPath）。
- 非空基线的增量链：当前固定空基线，一次打包完整工作区。
- 恢复 initContainer 执行期间的 Worker 强杀；当前只覆盖待保存屏障强杀，恢复阶段强杀会留下待对账的 Provisioning 实例与环境记录。
- 生产规模的工作区大小、保存吞吐和恢复延迟验收。小文件样本耗时不能作为生产容量承诺。
