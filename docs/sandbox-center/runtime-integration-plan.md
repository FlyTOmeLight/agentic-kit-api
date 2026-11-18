# 首版 Runtime 接入规格

日期：2026-10-22。状态：阶段 0 规格已确认；阶段 1a 控制边界探针已通过；阶段 1b 可续租准入与回执探针已通过（Docker）；阶段 2 最小 Center 已在本地通过（见[阶段 2 验证](phase2-verification.md)）；阶段 3 最小 Agent 集成已在本地通过（见[阶段 3 验证](phase3-verification.md)）。

采用 **原生 Kubernetes Provider + 受保护 Guard + OpenSandbox execd + 独立工作区 PVC**。先验证运行时控制边界，再实现 Sandbox Center 和 Agent worker 接管。已新增 execd UDS 补丁与 Guard 集成探针；Go 契约、SQL 和业务 worker 尚未改动。

本文细化 [选型结论](backend-selection.md) 和 [worker 接管协议](worker-handoff.md)。完整目标仍以 [主方案](sandbox-control-plane-technical-design.md) 为准；下文列出的接口差异是待实现工作，不代表现有声明已经包含这些能力。

## 1. 第一阶段的交付范围

首版只开放一个版本固定的命令/文件 Profile：一个实例、一个工作区、一个活动租约。支持命令提交、状态和输出查询、必要文件读写、租约续期及整实例停止。文件写入和命令使用相同的授权、准入与操作查询原则。

浏览器、PTY、交互 stdin、任意 session 配置、预热池和多 Backend 不进入第一阶段。内部验证可使用持久 PVC；正式 automaticCoding 仍遵守 `EphemeralCopy + Checkpointed`，不能把实验配置当成默认恢复策略。必要保存失败继续进入 SaveBlocked，见 [ADR-0003](sandbox-design-contracts/docs/adr/0003-scenario-recovery-guarantees.md) 和 [ADR-0004](sandbox-design-contracts/docs/adr/0004-block-deletion-on-required-save-failure.md)。

阶段完成的判断是：用户不能绕过 Guard；租约到期或保护进程崩溃后全部用户写者停止；文件保留；查询不会把不确定执行变成自动重跑。仅能正常执行命令不算完成。

## 2. 固定版本检查与必须修改的边界

已测 execd 镜像为 `opensandbox/execd@sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded`。二进制自报提交为 `b7d7697750b45be2ccc876da645c8880a0cf69fc`。以下结论针对该提交，不以发布标签或最新 main 替代。

| 检查结果 | 对首版的约束 |
| --- | --- |
| [main.go](https://github.com/opensandbox-group/OpenSandbox/blob/b7d7697750b45be2ccc876da645c8880a0cf69fc/components/execd/main.go#L232) 使用 TCP 监听，没有受保护 Unix socket 选项 | 给 execd 增加 Unix socket 模式；该模式不创建 TCP listener |
| [router.go](https://github.com/opensandbox-group/OpenSandbox/blob/b7d7697750b45be2ccc876da645c8880a0cf69fc/components/execd/pkg/web/router.go#L174) 将 `/internal/init` 排除在 token 校验之外 | 所有 execd 控制接口仅可信进程可达，不能只隐藏公开路由 |
| [init_runtime.go](https://github.com/opensandbox-group/OpenSandbox/blob/b7d7697750b45be2ccc876da645c8880a0cf69fc/components/execd/pkg/web/controller/init_runtime.go#L175) 应用初始化绑定 | Guard 不转发调用方提供的初始化请求 |
| [command.go](https://github.com/opensandbox-group/OpenSandbox/blob/b7d7697750b45be2ccc876da645c8880a0cf69fc/components/execd/pkg/runtime/command.go#L104) 未指定身份时继承 daemon 身份 | Guard 构造请求并强制 UID/GID，不能透传调用方身份 |

这是固定版本源码检查发现的接入边界。增加 UDS 补丁后的用户直连拒绝、身份注入拒绝与私有状态保护已通过[阶段 1a 实测](guard-execd-verification.md)；任意文件路径和完整攻击面尚未验收。普通 HTTP 代理、loopback 或 Pod 级 NetworkPolicy 都不能单独阻止同网络 namespace 的用户进程直连控制口。

execd 补丁首版只负责可选 Unix socket listener、安全创建和关闭 socket、禁用该模式下 TCP，并补对应验证。固定身份、公开入口白名单、租约和账本由 Guard 负责。升级时固定新的镜像 digest，并重新检查路由、身份和文件接口；上游是否接受补丁不作为首版成立前提，但补丁维护成本属于本选型成本。

本阶段不修改 execd 来保证每个完成结果都可恢复。若后续要求消除“执行完成但回执未保存”窗口，再单独设计可信 launch/exit 回执机制。

## 3. 进程、网络与存储拓扑

```text
Agent worker → Sandbox Center → Guard 的受认证入口
                                  │
                    独占容器 / 独立 PID namespace
                    Guard：真正的 root PID 1
                                  │ root-only Unix socket
                    execd：可信子进程
                                  │ 固定非 root UID/GID
                    用户命令与文件操作子进程

  /workspace：独立工作区 PVC，用户可写
  /state：独立受保护回执卷，用户不能访问
```

- 首版 coding Profile 的工作区采用 EphemeralCopy 分层组装：节点侧 Workspace Materializer 将只读基础层（按内容 digest 物化的只读 PVC）与实例级可写层组装为合并视图，经 `hostPath` 和 `mountPropagation` 暴露给 Pod。基础层可节点级只读共享；可写层随实例销毁。execd 与用户进程只看到合并视图，不能读写底层目录或请求替换挂载源。
- Guard 保持 `SETUID/SETGID`，不执行需要 `CAP_SYS_ADMIN` 的 mount；它校验并记录当前基础层 digest 与挂载事实，不匹配时拒绝 Ready。boot 变化后必须按已发布检查点重新组装或明确报告原可写层不可恢复，禁止用新环境冒充原工作区。
- 基础层以只读 PVC 为真源，检查点保存 OCI layer 风格的 upper 增量与 manifest；首版 WarmPool 只预热空工作区，认领后物化基础层，`T_ready` 包含这部分耗时。
- Guard 不能放在普通 sidecar，也不启用 Pod 共享 PID namespace；不使用 shell 或 tini 占据 PID 1。Guard 负责回收孤儿进程，execd 异常退出时终止整实例。
- execd 的 socket 位于 root-only 目录，权限仅允许可信进程访问。execd 不持有对外 TCP 入口。Guard 不提供任意反向代理，也不转发 init、proxy、PTY 或 session 管理接口。
- Center 到 Guard 使用双向 TLS 的服务身份，校验目标实例代次与 runtimeBootID。凭据由平台控制路径提供，放在受保护位置，不进入用户环境或工作区。用户进程即使连接 Guard 端口，也不能取得调用身份。
- 命令强制固定非 root UID/GID、清空额外组，用户 capability 为零，禁止提权。Guard 仅保留降权、进程管理所需能力；具体最小集合在目标 RuntimeClass 验证，不能照搬隔离会话实验的 SYS_ADMIN/unconfined。
- 文件使用 execd 的 identity-scoped 接口并固定身份。Guard 验证工作区范围；路径遍历、符号链接及并发替换不能使文件操作越过授权范围。不能用 root 文件接口加字符串前缀判断代替。
- 根文件系统只读；用户不能修改 Guard、execd、配置、凭据或回执。不给用户主机挂载、容器运行时 socket 或 Kubernetes ServiceAccount token。
- 工作负载采用 `restartPolicy: Never`。Guard 崩溃意味着该 boot 的活动租约关闭，不能从磁盘期限复活。旧回执由单独授权的只读对账路径读取，恢复 Pod 不启动旧命令。

首版可靠取消的兜底粒度是整个实例，可能停止同实例全部命令。单个主命令 Exited 不证明后代退出。没有全部写者停止和在途文件操作排空证据，不授予下一代工作区写权。

Guard/root execd 属于可信计算基础。上述拓扑只解决用户进程与控制层的边界；Hardened Profile 仍须通过目标 RuntimeClass 的隔离验收。若 Unix socket 与身份边界不能成立，停止该组合实施，重新选择执行层；不自动退回未验证的特权隔离会话方案。

## 4. 授权、续期与确认协议

### 4.1 授权来源

Agent API 必须验证已认证 caller 与 worker 领取身份，返回 RunID、WorkerID、epoch、scope、Running 状态、数据库检查时间和权威有效期限。期限不超过 `min(run.lease_until, run.deadline)`。沿用项目身份 middleware 方向，不新建静态 token 用户系统。

当前 [CheckExecutionLease](../../pkg/durable/execution_service.go) 只返回布尔值，查询也不绑定 WorkerID；它不能直接充当完整 RunAuthorization。失联时拒绝新副作用和续租，旧执行由本地有限期限结束。读历史结果和授权清理不要求旧执行权继续有效。

每次新命令或文件写入，在 Center 受理及实际派发前都重新检查当前 RunAuthorization。不能把 BindLease 时的检查缓存到整个本地租约期限。已在途请求仍由 Runtime 的目标身份、有限授权和准入屏障约束；上游领取失效不代表已经发生的副作用能撤销。

### 4.2 绑定与期限

每份本地授权冻结：资源 scope、RunID/WorkerID/epoch、leaseID/fencingToken、admissionEpoch、实例 ID/generation、runtimeBootID。新 epoch 不续旧租约。

同一绑定使用单调递增 `renewalRevision`。每次新续租有新的 operation requestKey；重试同一次续租沿用 key、revision 和全部参数。同 revision 参数不同返回冲突，旧 revision 不能覆盖较新授权。

授权包含绝对截止时间，不采用“收到后重新加 N 秒”。接受期限不得超过上游授权、Profile 上限和实例 hardExpiresAt。Pod activeDeadlineSeconds 仅作固定硬上限，不能充当可续租期限。

时钟策略是实施前置配置：给出允许的最大时钟误差和停止耗时预算，从上游截止时间扣除相应余量，再转换为本地单调 deadline。已接受的单调 deadline 不随 wall clock 回拨延长。无法满足时钟误差约束时拒绝激活/续期。暂停、节点冻结和时钟异常需单独验收，不能承诺未测的实时停止上限。

### 4.3 状态与响应丢失

| 当前状态或请求 | 处理 |
| --- | --- |
| 未绑定 boot，收到首次有效授权 | 安装有限本地期限，确认完整绑定和 revision 后才准入 |
| Active，收到同绑定新 revision | 重新核验授权上界，与到期关闭串行决定是否接受 |
| 同 revision、相同参数重试 | 返回原接受事实及当前状态，不延长原期限 |
| 旧 revision 或任意绑定不匹配 | 拒绝；查询可返回实际最新 revision |
| 已过期或 Closed | 拒绝激活和续期；关闭不可逆 |
| Guard 重启或 boot 不匹配 | 旧授权关闭；仅对账，不恢复活动执行 |

Center 复用 OperationStore 保存续租意图，再调用 Runtime。Runtime 确认至少包含完整目标身份、接受的 revision、接受期限、Active/Closed 状态及观察时间。增加按目标和 revision 读回实际接受事实的能力；当前 `RuntimeAdmission.BindLease(...) error` 不足以表达该确认。

续租响应丢失时，将操作保留为待对账，查询原 revision。不能把 Center 数据库的期望期限当成运行时已生效期限，也不能用旧内存状态回滚可能已生效的新授权。确认成功后再发布续租成功；进程重启使所有旧 revision 关闭。

截止计时独立于网络、日志和账本 fsync，不在全局请求锁内等待存储。新期限尚未完整安装时仍按旧期限停止。存储失败不得返回成功；若无法确定本地状态，则关闭整个绑定。到期检查同时阻断新命令、文件写入和已接受请求的后续输入，并停止实际写者。

## 5. 操作身份、回执与 Unknown

沿用 worker 已有的 `runID:callID` 作为稳定业务操作标识，接管不按 epoch 生成新标识。映射成安全内部 executionID/operationID，业务 ID 不直接作为文件名。原型只接受特定字符且长度不超过 64 字符，不能直接接受该业务 ID。

同一键的语义校验覆盖操作类型、argv/script、env、CWD、身份、超时、输出限制和冻结目标。派发前写入受保护 Pending；由单一持有派发权的执行者推进状态。HTTP 错误、超时、execd 404 或断流不能授权重发。

Pending 仅表示意图已保存，不能单凭名称判定未执行。调用 execd 前，Guard 先持久化 DispatchStarted 或等价的“已越过派发边界”事实，写入失败则不调用。若在记录该事实后、实际发送前崩溃，也保守保留 Unknown。只有明确处于未派发阶段且已排除旧派发者的记录，才能继续首次派发；Center 与 Guard 各自的状态不得互相冒充对方的证据。

| 事实 | 对账行为 |
| --- | --- |
| Center/Guard 持久状态明确未派发，且旧派发者已失去派发权 | 可以继续首次派发 |
| 已受理且仍运行 | 查询原命令、继续观察或授权取消 |
| 已完成，退出和输出证据完整 | 返回原结果，供 Agent 补交 FinishStep |
| execd 已执行，但 Guard 尚未保存终态/输出就崩溃 | Unknown，不自动再次执行 |
| 仅主命令结束，后代或文件操作未排空 | 命令状态可查询，工作区仍不得移交 |

回执卷与工作区分开，用户不能伪造结果。终态记录输出持久范围、截断和写入错误，不能把不完整输出标为完整。回执先落盘再对外确认；卷故障的恢复范围由真实存储保证决定。

文件写入使用 operationID 与原 requestKey 对账，不假装全部文件操作都是命令 executionID。文件写/edit 丢响应同样保留不确定状态。有限期限停止进程不能撤销已发生的外部网络副作用，本方案不承诺任意副作用 exactly-once。

## 6. API 与 worker 的最小接入

| 位置 | 待实施变更 |
| --- | --- |
| API `pkg/execution` 与 `internal/table` | Run 环境关联记录；Step 的版本化外部操作关联 |
| API `pkg/durable` | 在现有领取校验下原子准备外部身份；查询关联；复用 FinishStep 提交可信结果 |
| API 授权入口与 SDK | 提供绑定真实 worker 的 RunAuthorization 与有效期限 |
| API `pkg/durable/prepare.go` | 调整当前仅接受 AIO 的冻结输入校验 |
| worker `internal/engine` | 先恢复旧环境；为有外部关联的 Uncertain 增加只读对账分支 |
| worker `internal/adapters` | 用 Sandbox SDK Backend 替换首版 Profile 的直接 AIO 装配 |

Run 环境记录按 Run 与稳定申请键唯一，保存原申请 epoch、Profile/工作区要求、acquisitionID、leaseID、代次以及释放/保存操作。保留历史，不能只用可覆盖的 currentLeaseID。Run 结束后，独立清理流程仍能查询未完成记录。

Sandbox Step 的首次派发准备与外部关联应在同一 durable 事务提交。至少保证原 leaseID、requestKey、操作类型先持久化，才能调用网络；远程返回后再补 executionID/operationID 和输出游标。已有 Started 且无关联的历史 Unsafe 记录不自动补键并重跑。

复用 BeginStep、FinishStep 和稳定操作 ID，不把 execute 改为 Idempotent，不把生命周期账本塞入 Eino checkpoint。新 worker 先只读查旧操作；可信完成结果可在新 Run 领取下提交。需要继续执行时，先停止旧写者、完成必要保存和释放，再申请新 epoch 环境。

worker 停机或失去领取只终止本地等待。用户取消、命令超时和实例关闭走各自经过授权的路径。新 Backend 不复制 AIO 的通用 `ctx.Err() → Kill` 行为。

Sandbox Center 单独拥有资源生命周期数据库；Agent API 保持 PostgreSQL durable 边界，不能直接复制设计目录的 MySQL 全量表。正式服务仓库和依赖版本在编码阶段按布局约定确定。已有库需要显式迁移方案；临时 go.work 联调后仍须以正式模块版本、`GOWORK=off` 验证。

## 7. 实施次序与验收

| 阶段 | 交付物 | 通过门槛 |
| --- | --- | --- |
| 0：接入规格（本文） | 拓扑、补丁范围、协议差异与验收项 | 方案可检查；明确已验证与待验证边界 |
| 1a：控制边界（已通过探针） | execd UDS 补丁、PID 1 Guard、固定身份、有限期限 | [控制访问与整实例停止实验](guard-execd-verification.md)通过；未完成阶段 1 全部验收 |
| 1b：完整受保护 Runtime（待实施） | 可续租准入、受保护回执、完整命令/文件路径与远程认证 | 下表运行时故障场景通过 |
| 2：最小 Center（本地已通过，待授权） | 单 Profile 申请/查询/续期/操作/释放；对账器已落地，可信授权仍缺 | 响应丢失仍定位原资源，不重复派发；见[阶段 2 验证](phase2-verification.md) |
| 3：Agent 集成（本地已通过最小闭环） | API 持久关联、SDK、worker Backend、接管与对账器 | 见[阶段 3 验证](phase3-verification.md) |
| 4：生产 Profile | 必要保存链路、RuntimeClass、存储与性能验证 | 所有发布门槛通过后才允许业务选择 |

阶段 1 先验证控制入口不可绕过，再验证正常命令和续租。验收必须记录固定镜像、权限、RuntimeClass、实际进程与文件变化：

| 场景 | 必须观察到的结果 |
| --- | --- |
| 用户访问 execd/init/Guard、选择 UID 或操作回执 | 控制访问被拒绝，可信状态不变 |
| 文件路径遍历、符号链接及替换竞争 | 无越权读取或写入 |
| 正常续租、响应丢失、乱序、到期竞争 | 实际 revision/期限可查；旧请求不能复活租约 |
| Guard 或 execd 崩溃、账本存储阻塞 | 普通及 setsid 写者停止；文件保留；旧 boot 不恢复 |
| 到期时仍在上传或后台文件操作 | 新写入被拒，旧操作停止或排空后才交出写权 |
| 命令/文件请求执行后丢响应 | 查询原操作，副作用不因重试重复 |
| 叠加层组装与检查点 | 用户/execd 不能读写底层目录；基础层 digest 与内容一致；Guard 无 `CAP_SYS_ADMIN`；静止证据集合完整时才固化可写层；恢复结果与基础层+OCI 增量重放一致 |
| 完成结果已存、worker 在 FinishStep 前退出 | 新 worker 补交一次，不重跑 |
| 执行后回执未存、Guard 崩溃 | 保留 Unknown，不伪造完整结果 |
| 新 worker 领取后旧 worker 恢复 | 旧身份不能派发、续租或提交 |
| 节点失联、仅 Pod API 对象消失 | 没有停止或物理隔离证据，不发放重叠写权 |
| 必要保存失败、Run 终态 | 文件仍受保护；只读查询与独立清理仍可工作 |

复用现有 DrainEvidence 的在途命令、文件操作、访问会话、停止证据和必要保存检查，不另建只看 Start 的简化释放判断。

阶段 1 报告停止时延、续租延迟、回执 fsync 延迟、启动耗时、CPU/内存及输出/并发限制。生产发布前为目标 Profile 确定数值上限，实测不满足则不发布。当前约 2.161 秒停写记录仅是单次 Pod 实验，不能作为 SLA；节点冻结、内核或存储故障也不属于该样本保证。

## 8. 当前证据与剩余工作

已验证的底层机制分别见 [supervisor 原型](supervisor-verification.md)、[Kubernetes 与隔离会话探针](backend-selection.md)、[OpenSandbox 故障探针](opensandbox-runtime-verification.md)。新增 [Guard + execd 实测](guard-execd-verification.md) 已在 Docker 联合验证 PID 1、受保护 UDS、固定身份与整实例停止；尚未联合 Kubernetes，也没有实现续租确认、可信 Agent 授权或完整接管。

当前完成阶段 0、阶段 1a 与阶段 1b 探针（Docker）。阶段 2 最小 Center 已落地到代码：`internal/sandboxcenter/` 提供 PostgreSQL 生命周期存储（sandbox/lease/operation 三表，Acquire/Activate/Renew/BeginOperation/CompleteOperation/Release/ConfirmDestroyed 事务，4 项集成测试通过）、K8s Provider（kubectl 显式 context，单容器 guard Pod，/state root-only emptyDir 与 /workspace 65534 emptyDir，drop ALL+SETUID/SETGID，readOnly 之外的探针等价边界）与 HTTP API（`POST/GET /v1/sandboxes`、`POST /v1/sandboxes/:id/renew`、`POST /v1/sandboxes/:id/operations`、`GET /v1/sandboxes/:id/operations/:opID`、`DELETE /v1/sandboxes/:id`）。未配置 `Sandbox.K8sContext` 时仅注册元数据路由，provision/exec/release 返回 NotReady。控制面到 guard 的唯一通道是 `kubectl exec` 调用 Pod 内 `/guard call`，不新增 TCP 监听。下一项工程交付是构建合并 guard+patched execd 的固定镜像并在本地 orbstack 集群跑通端到端闭环（申请→激活→续期→执行→丢响应重查→释放），不先铺开全量 50 个 SDK 方法。
