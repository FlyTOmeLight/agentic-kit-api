# LayerFS 自动保存与申请恢复

必要保存和新沙箱申请恢复已接入。验证环境为本地单节点 OrbStack、PostgreSQL 与实际 HTTP/SDK。

## 流程

申请时声明 `requiredSave: true` 的沙箱在释放或到期时进入 `SaveBlocked`。Guard `/stop` 停止 PID 1，工作区 PVC 保留。`stoppedAt` 入库后，Reconciler 推进保存。

1. PVC 模式下，中心先登记保存尝试。Saver Pod 只读挂载工作区，在检查点 PVC 上独占创建 `saves/<sandboxID>/<generation>/<attempt>`，依次写入 LayerFS layer 和 manifest，最后原子重命名生成 `commit` 标记。打包使用空基线常量 `sha256:<64 个 0>`。
2. Saver 终止或缺失时，中心创建独立的 `verify-<sandboxID>` Pod，只读挂载检查点 PVC。Verifier 按登记的目录校验提交标记、manifest 与 layer 的 SHA-256、沙箱 ID、generation、保存尝试以及 tar 结构。Saver 已写完但 Pod 回执丢失时，中心仍可从该目录确认保存。
3. Verifier 校验通过后，`confirmRequiredSave` 核对 generation、version 与停止证据，在同一事务内写入 `savedCheckpoint` 并转为 `Releasing`。没有提交标记、校验失败或内容损坏时保留 `SaveBlocked` 与工作区 PVC。保存目录尚不存在且 Saver 已缺失时，中心可以启动 Saver。
4. 事务提交后，中心按 UID 删除 Saver、Verifier、原 Pod 与工作区 PVC，最后转为 `Destroyed`。进程退出或删除失败后，`Releasing` 分支与同步 Release 可继续清理。

单节点 `hostPath` 模式沿用 `layerfs_checkpoint.py pack`，从 Saver 日志读取并校验检查点标识格式。Saver 和 Verifier 使用同一个 `agentic-sandbox-saver` 镜像，包含 digest 固定的 server 基础镜像、LayerFS 工具和 Go 包装器。

检查点标识是内容寻址的：manifest 摘要与 layer 摘要拼合。恢复时按 manifest 摘要读 manifest，按其内嵌的 layer 摘要读层文件，工具逐项校验摘要、路径、whiteout、xattr 与硬链接，任何篡改或损坏都会失败。工作区树的摘要由 `layerfs_checkpoint.py digest` 另行计算，可与恢复结果比对。

Saver 需要 `DAC_OVERRIDE`：沙箱初始化把 `/workspace` 交给工作负载身份 65534、模式 0750，Saver 以 root 运行但丢弃全部能力后无法读取，只加回 `DAC_OVERRIDE`。Saver Pod 设 `activeDeadlineSeconds`，卡死的实例终态化并由控制面报告。

已停止的 `SaveBlocked` 记录继续进入对账队列。每轮 Save 使用 PostgreSQL advisory transaction try-lock 串行推进同一沙箱，锁内重读状态；打包 Pod 尚未完成时结束本轮，不等待整个打包过程。检查点确认随事务提交，之后才删除已观察的 Pod UID。迟到的旧快照不能在保存完成后重新创建 Saver；重启后的清理还核验 component 和 generation 标签。

Saver 退出后，若检查点目录缺失或只有半成品，中心改用新目录重试，最多追加两次保存尝试。超过上限后保持 `SaveBlocked`，保留原工作区。提交标记已存在但校验失败时，中心保留失败的 Verifier 及其日志，供运维判断存储故障或数据损坏。

## 新沙箱如何恢复

调用 `POST /v1/sandboxes` 或 `client.Sandboxes.Acquire`，设置 `requiredSave: true` 和 `restoreFromSandboxId`。Run 的冻结 `FilesystemBinding` 必须使用 `Sandbox`、`/workspace`，并匹配来源 ID 和 Profile。中心校验有效 Run claim、相同 tenant/owner/Profile/版本、来源停止与销毁事实，再从来源记录冻结 `resumeCheckpoint` 和 PVC 模式下的保存目录。存储路径由中心管理。

恢复使用新沙箱的 init container。它只读挂载检查点目录，校验 manifest、layer 摘要与基础层，把完整工作区恢复到新 PVC 内的临时目录，再原子重命名为 `workspace`。Guard 通过 PVC subPath 使用恢复后的目录，不挂载检查点存储。初始化重试发现已完成的工作区时不重新覆盖文件，工作区根目录和内部文件的权限、属主、硬链接、软链接均保留。

恢复失败时 Guard 不启动，不发放 lease。尚未激活的恢复实例在释放或超出 provisioning 宽限期后，先关闭申请状态，再删除原 UID 的 Pod 与 PVC，记录为 `Lost`；不会把半恢复目录保存成新检查点。原来源检查点不变。调用方需要重新申请时使用新 requestKey。

启用 `K8sContext` 并设置 `[Sandbox].CheckpointImage`（`SANDBOX_CHECKPOINT_IMAGE`）。存储配置必须二选一。

- `CheckpointStorePVC`（`SANDBOX_CHECKPOINT_STORE_PVC`）填写沙箱命名空间内已创建的 PVC 名称。Saver 需要写权限，恢复 init container 只读挂载。跨节点并行使用时，所选存储须支持多节点并发挂载。
- `CheckpointStoreHostPath`（`SANDBOX_CHECKPOINT_STORE_HOST_PATH`）填写单节点绝对路径。

保存与恢复需指向同一份检查点数据。切换存储前需迁移仍会被引用的检查点。默认留空，未启用时必要保存保护仍生效。

PVC 读写挂载、HTTP 恢复与跨 Run 工作区继承已在单节点 OrbStack 验证。检查点 PVC 引用 JuiceFS FUSE 挂载，工作区使用 `local-path`。生产使用目标集群的 JuiceFS CSI 验收。

## Helm 部署与生产验收

构建 API 时使用 `docker build --platform linux/<arch> --target sandbox`，并提供已有的 BuildKit secret `github_netrc`。该 target 按目标架构打包 API 和 `kubectl`。从同一份源码运行 `docs/sandbox-center/probes/guard-execd/build-image.sh <tag>` 和 `docs/sandbox-center/probes/saver/build-image.sh <tag>`。两份脚本通过 `TARGETARCH=amd64` 或 `TARGETARCH=arm64` 选择节点架构。发布后在 values 中引用三个镜像的 digest。确认对应节点能拉取镜像，以及 API 镜像中的 `kubectl` 与目标 API Server 版本兼容。使用私有镜像时，为 API 和沙箱两个命名空间中实际使用的 ServiceAccount 配置拉取凭据。Saver 需要包含 `verify` 子命令。

在沙箱命名空间预建 JuiceFS CSI `ReadWriteMany` 检查点 PVC，为它分配沙箱专用目录和保留策略。为工作区配置独立的 JuiceFS CSI StorageClass。工作区 PVC 需要 `ReadWriteMany`，因为 Saver 可以在 Guard 所在节点之外启动。由目标命名空间的准入策略允许当前 Guard、Saver 和恢复容器所需的 UID 与 capabilities。

在 API 命名空间创建两个 Secret。kubeconfig Secret 的键为 `kubeconfig`，包含目标 context、API Server 信任链和可在 API 容器内使用的认证配置。权限限于目标沙箱命名空间，允许 `pods` 和 `persistentvolumeclaims` 的 `get`、`list`、`watch`、`create`、`patch`、`delete`，以及 `pods/log` 的 `get` 和 `pods/exec` 的 `get`、`create`。用该 kubeconfig 通过 `kubectl auth can-i` 验证实际权限。配置 Secret 的键为 `config.toml`。以 `conf/server/config.toml` 为模板，保留环境变量绑定，并把 `[Sandbox].AllowedServices` 设为实际 Worker 的 Forge 服务身份。

在现有 API 部署 values 上加入以下配置，并将示例镜像和资源名替换为目标环境实际值。

```yaml
sandbox:
  enabled: true
  apiImage: <registry>/<api-image>@sha256:<digest>
  kubeconfigSecret: sandbox-kubeconfig
  existingConfigSecret: sandbox-config
  context: sandbox-runtime
  namespace: sandbox-center
  guardImage: <registry>/<guard-image>@sha256:<digest>
  checkpointImage: <registry>/<saver-image>@sha256:<digest>
  checkpointPVC: juicefs-checkpoints
  workspaceStorageClass: juicefs-workspace
  workspaceAccessMode: ReadWriteMany
  runtimeClass: <target-runtime-class>
```

升级前先在隔离的验收命名空间验证。使用与生产一致的镜像、CSI 配置及准入策略，为源和恢复准备两个 RuntimeClass。它们使用目标运行时，并分别通过 `scheduling.nodeSelector` 选择不同的节点。预建专用验收检查点 PVC。测试会创建和删除沙箱 Pod、工作区 PVC，并在验收检查点卷上留下归档。

```sh
SANDBOX_E2E_K8S_CONTEXT='<test-context>' \
SANDBOX_E2E_NAMESPACE='<test-namespace>' \
SANDBOX_E2E_GUARD_IMAGE='<guard-image>' \
SANDBOX_E2E_SAVER_IMAGE='<saver-image>' \
SANDBOX_E2E_CHECKPOINT_PVC='<test-checkpoint-pvc>' \
SANDBOX_E2E_SOURCE_RUNTIME_CLASS='<source-runtime-class>' \
SANDBOX_E2E_RESTORE_RUNTIME_CLASS='<restore-runtime-class>' \
SANDBOX_E2E_WORKSPACE_STORAGE_CLASS='<test-workspace-class>' \
SANDBOX_E2E_WORKSPACE_ACCESS_MODE=ReadWriteMany \
GOWORK=off go test -race -count=1 -timeout=8m \
  -run '^TestCheckpointRestoreHTTPE2E$' -v ./internal/sandboxcenter
```

测试使用 `TEST_DATABASE_URL` 指定的验收 PostgreSQL，断言两次申请落在不同节点，并检查文件内容、权限、属主、软硬链接、请求重放与再次保存。在隔离集群追加一次源节点不可用场景。检查点确认后，在源节点不可用期间从另一节点发起恢复。若要承诺检查点确认前的节点故障也可恢复，须验证原工作区仍可访问且控制面能确认原实例已停止。

用代表性工作区测量释放至 `Destroyed` 的时延、恢复至 `Active` 的时延和每分钟保存吞吐。按工作区大小和预期并发记录 p95，并核对 64 MiB 归档限制、PVC 容量和生产对账周期。灰度从指定 Worker 开始，监控 `SaveBlocked` 停留时间、Saver/Verifier 失败、PVC 挂载失败和检查点卷用量。需要回退时先停止新任务进入，保留检查点卷、原工作区和数据库记录，等在途保存与恢复处理完成后再关闭部署开关。切换卷前迁移仍被引用的检查点，清理策略须保留这些引用及在途保存目录。

## 已执行验证

- PostgreSQL：checkpoint 格式校验（`validateCheckpointID` 拒绝空、缺段、非 sha256、非小写 hex）、`Save` 拒绝未阻塞或未停止的沙箱、`confirmRequiredSave` 接受合法 LayerFS checkpoint 并转为 Releasing。
- 真实 Kubernetes 端到端 `TestRequiredSaveLayerfsE2E`：申请 RequiredSave 沙箱，写入目录树、权限、符号链接和不同属主文件；释放进入 SaveBlocked 并停止；Reconciler 驱动 Saver Pod 打包，确认 checkpoint 入库，销毁原 Pod 与 PVC；用确认的 manifest 摘要和空基线在新 Pod 中 `restore`，逐项核对恢复的目录、`main.go` 内容、`main.c`、`design.md` 与符号链接 `entry.go`。
- `TestCheckpointRestoreHTTPE2E` 使用真实 HTTP 路由和公开 SDK：沙箱 A 写入并保存、销毁；新 Run 冻结来源并申请 B；验证内容、目录、模式、属主和软硬链接；修改 B 后重复申请，确认未覆盖修改；B 再次保存并回收。另覆盖检查点对象缺失时不激活、对账清理到 `Lost`，以及迟到 Save 不重建 Saver。
- `service.TestGoalWorkspaceWorkerKubernetesE2E` 启动真实 Worker 二进制，通过 HTTP/SDK 与真实 Kubernetes 串联 Goal 的两轮执行：首轮写入随机内容并验收 Failed；保存未确认时 `QueueGoalIteration` 返回 `ErrNoWork`。测试在待保存屏障强杀该 Worker，启动新 Worker 后才启动 Reconciler；Reconciler 自动保存、销毁并关闭环境，新 Worker 自动创建下一 Run，恢复首轮文件后修复并验收 Passed。测试核对两轮 Run 的 `evaluationWorkerID` 非空且不同、冻结恢复来源、检查点继承、环境保存回执及两组 Pod/Saver/PVC 全部回收，未手动写入检查点或创建下一 Run。模型使用固定响应的 HTTP 测试服务，实际工具命令与验收命令均在 Guard 沙箱内执行。本机 OrbStack 上以 `-race` 运行通过；测试对账周期为 500ms，生产默认仍为一分钟。
- PostgreSQL 回归覆盖来源与冻结绑定校验、申请幂等、已停止记录继续调度保存、并发 Saver try-lock 和未完成恢复禁止发布检查点。
- JuiceFS FUSE 单节点复验覆盖 `TestRequiredSaveLayerfsE2E`（53.60 秒）、`TestCheckpointRestoreHTTPE2E`（62.84 秒）、`TestReconcileFailurePathsE2E`（59.01 秒）和 `TestGoalWorkspaceWorkerKubernetesE2E`（51.45 秒），均以 `-race` 通过。测试删除已写完的 Saver，再损坏 manifest，断言检查点未发布、原 PVC 仍保留。修复 manifest 后重新校验，中心在 Saver 缺失的情况下完成发布和回收。两轮 Goal 使用 Worker `4f46189bfcda` 的现有构建，接班 Worker 恢复首轮文件并验收通过。LayerFS Linux 容器回归 17 项通过，覆盖并发创建保存目录和拒绝半成品。
- 生产候选使用重新构建的 Saver `juicefs-261119`，同一组四项 Kubernetes E2E 再次以 `-race` 通过。HTTP 恢复最终复验耗时 65.82 秒，其中两个小工作区从释放至 `Destroyed` 分别为 20.09 和 15.41 秒，从恢复申请至 `Active` 为 7.03 秒。两轮 Goal 强杀接管复验耗时 54.00 秒。API 使用本机 PostgreSQL、Redis 通过 `make integration`（全仓 `-race` 和 `go vet`）。
- 启用与默认禁用两组配置通过 `helm lint --strict`。OrbStack 临时命名空间用新构建的 API sandbox 镜像部署 Chart，API 成功就绪。即使已有环境 Secret 中的 `CONFIG_FILE` 指向无效路径，部署仍读取指定的挂载 TOML。UID 65532 的 API 容器使用 kubeconfig 和可写 cache，通过 `kubectl` 读取到目标测试 PVC `Bound`。API sandbox 镜像通过 `linux/arm64` 和 `linux/amd64` 构建，Guard 和 Saver 均通过 `linux/amd64` 构建。

## 源码

- `internal/sandboxcenter/saver.go`：Saver 的 pod 生命周期、checkpoint 确认与清理。
- `internal/sandboxcenter/checkpoint_reconcile.go`：PVC 保存尝试与独立 Verifier 对账。
- `internal/sandboxcenter/reconciler.go`：SaveBlocked 分支在停止后驱动 `Save`。
- `docs/sandbox-center/probes/saver/main.go`：Pod 内包装器，调用打包工具并输出 checkpoint 标识。
- `docs/sandbox-center/probes/saver/build-image.sh`：构建 `agentic-sandbox-saver:<tag>`。
- `internal/sandboxcenter/saver_e2e_test.go`：端到端用例。
- `internal/sandboxcenter/restore.go`：来源授权、原子恢复与未激活实例清理。
- `internal/sandboxcenter/restore_e2e_test.go`：HTTP/SDK 跨 Run 恢复与失败清理。
- `service/goal_workspace_e2e_test.go`：真实 Worker 进程驱动的 Goal 跨 Run 工作区恢复验收。

## 复跑 Goal 跨 Run 验收

先准备前述 Guard、Saver 镜像、检查点存储及本地 PostgreSQL，再从 API 仓库根目录执行。可使用与当前恢复 HTTP 契约兼容的 Worker 二进制，也可在已有 `go.work` 时联合构建。API 测试关闭 workspace，并为测试进程启用 `-race`，Worker 子进程使用其构建时的竞态设置。

```bash
GOCACHE=/private/tmp/go-cache GOWORK="$PWD/go.work" \
  go -C ../agentic-kit-worker build -o /private/tmp/agentic-sandbox-goal-worker ./cmd/worker

TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable' \
SANDBOX_E2E_K8S_CONTEXT=orbstack \
SANDBOX_E2E_WORKER_BINARY=/private/tmp/agentic-sandbox-goal-worker \
GOCACHE=/private/tmp/go-cache GOWORK=off \
  go test -race -count=1 -timeout=5m -run '^TestGoalWorkspaceWorkerKubernetesE2E$' -v ./service
```

测试使用隔离 schema，退出时停止 Worker、Reconciler 并清理本次 Pod/PVC。可通过 `SANDBOX_E2E_GUARD_IMAGE` 和 `SANDBOX_E2E_SAVER_IMAGE` 指定镜像。用 `SANDBOX_E2E_CHECKPOINT_PVC` 指定已建测试 PVC，或用 `SANDBOX_E2E_CHECKPOINT_HOST_PATH` 指定单节点路径。未提供集群或 Worker 路径时跳过。

## 未覆盖

- JuiceFS CSI 多节点挂载、挂载节点故障恢复和对象网关接入。
- 非空基线的增量链：当前固定空基线，一次打包完整工作区。
- 恢复 initContainer 执行期间的 Worker 强杀；当前只覆盖待保存屏障强杀，恢复阶段强杀会留下待对账的 Provisioning 实例与环境记录。
- 生产规模的工作区大小、保存吞吐和恢复延迟验收。小文件样本耗时不能作为生产容量承诺。
