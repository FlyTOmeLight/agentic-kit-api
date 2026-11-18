# Worker 接管与沙箱租约

状态：首版实施协议，已同步声明契约；运行实现与故障验收尚未完成。

当前首版 Runtime 拓扑及新增契约差异见 [接入规格](runtime-integration-plan.md)。受保护控制通道、续租 revision 确认及 Agent 持久关联仍待实现，不包含在“已同步声明契约”的完成范围内。

本文补充主方案第 16 节，解决一个具体问题：Run 被另一个 worker 接管后，旧 worker 不能继续取得写权限，新 worker 也不能重复启动旧命令。

## 首版规则

沙箱租约固定绑定 `holderRunId + holderRunEpoch`。Run 的 epoch 变化后，新 worker 先只读对账，再完成旧租约的停止和必要保存，最后申请新租约。首版不移交活动租约，不把旧租约的 epoch、fencingToken 或已接受命令改写成新持有者。

这会增加接管时的等待和环境准备耗时，但沿用已有申请、释放和查询接口，不引入活进程移交协议。旧命令的执行事实保留；结束旧租约不表示命令失败，也不授权重复命令。`Retain` 仍可在清理完成后复用原环境，`Destroy` 仍按 Profile 保存并销毁，不能为接管修改策略。

## 三种版本不能混用

| 标识 | 谁维护 | 表达什么 |
| --- | --- | --- |
| Run epoch | Agent durable Store | 哪一次 worker 领取持有 Run 执行权 |
| leaseID / fencingToken | 沙箱中心 | 哪个租约持有工作区的写入权 |
| admissionEpoch | 沙箱中心与 Runtime | 哪一批延迟请求仍允许进入执行环境 |

沙箱控制循环使用的 `worker_id / claim_token` 是后台对账任务的领取信息，不是 Agent worker 身份，不能拿来保存上游 Run epoch。实例 generation 也只表达物理环境代次。

## 身份与授权

`AcquireRequest.HolderRunEpoch` 是期望绑定值，不是授权证据。认证适配器从已验证的 worker 身份或绑定本次领取的委托取得 `Principal.RunAttempt`，包含 RunID、WorkerID、Epoch。普通共享服务 token 加上请求体或 Header 中的 epoch 不足以建立此权限。

`Principal.CallerID` 是跨 worker 与 epoch 稳定的调用应用身份；具体 worker 放在 RunAttempt 中，认证委托同时绑定这两者。新 worker 用自己的凭据在同一个 caller 名下查询原请求，不冒用旧 worker token，也不接受请求体任意指定 caller。

`Authorizer.AuthorizeRun` 查询可信 Agent 平台，验证主体及 tenant/project 范围、当前 WorkerID、epoch、Running 状态和执行权截止时间。网络失败或无法确认时，拒绝新副作用及续租。读历史、输出和操作对账继续使用资源读权限；Run 已终止也不应使历史和清理记录不可查询。

现有 `/internal/v1/executions/{id}/lease?epoch=...` 只能作为执行权查询基础。实施时还要接通身份校验与 worker/领取委托绑定，不能把公开的 runID/epoch 当作凭据，也不能把一个布尔结果当成可信主体证明。

首次申请、Ready 激活、每次新写入与续租均校验当前执行权；排队后真正派发前重新检查。`MutationKey.RunAuthorization` 和 `ActivateIntent.RunAuthorization` 传递服务端产生的校验结果。事务检查主体、动作、资源、有效期和对应的冻结 epoch；受理证据不作为长期授权缓存，不进入请求语义哈希，也不保存明文 token。

`ExecutionStore.BeginDispatch` 显式接收重新核验的 `RunAuthorization`。控制器使用自身受限代理权限，依据受理时保存的可信主体与 RunAttempt 向权威来源复核，不复用 Admit 的旧结论或旧 bearer token。其他排队写操作在各自执行入口遵循同样规则；完成已受理的必要保存使用独立保存授权，不要求失效 Run 重新获得执行权。

申请受理时，将已验证的 `MutationKey.Principal` 冻结为 `StoredAcquireRequest.AuthorizedPrincipal`，沿用现有 JSON 存储，不增加 SQL 列。它保存原 SubjectID、WorkerID 及 RunAttempt，必须与申请的 RunID/epoch 一致；不含 token，不进入公开请求语义哈希。旧 Roles 仅作审计，不能当成当前权限。控制器重启后经租约的 acquisitionID 找回该身份，再查询可信来源核验；不能用“当前 worker”替换原身份，也不能复用旧授权期限。

租约和运行时本地授权的有效期不得越过此次确认的上游 Run 执行权期限、Profile 上限及 hardExpiresAt。续租顺序为先确认上游执行权仍有效，再续沙箱租约，并确认 Runtime 的本地期限已更新。Runtime 更新结果不明时继续对账，不能在本地假定续租成功。较短的 Run 租约需要与沙箱最小租约及网络预算一起验证；不适配时拒绝该配置，不能自行延长授权。

本首版可发布 Profile 必须验证 `RuntimeInfo.EnforcesLeaseDeadline`，由 Runtime 或用户进程不能控制的 supervisor 实现 `RuntimeAdmission`：本地到期拒绝新写入和输入、停止现有用户进程并保留受保护文件；过期绑定不能被迟到续租复活。仅声明方法、依赖控制面定时器或删除唯一文件副本的 Pod 都不满足条件。不具备该机制的组合不能发布供此接管流程使用。

Run 数据库与沙箱数据库之间没有跨库原子事务。已经通过检查、随后遇到 epoch 变化的在途请求，不能声称被瞬时撤销。关闭沙箱准入后，要按已有派发账本对账，阻止迟到请求，并取得进程、文件写入、访问会话和保存操作的清理证据，再交出工作区。隔离旧写者是允许新写者开始的前提。

## 接管流程

1. worker 领取 Run，取得新 epoch。从持久记录读取原申请键、acquisitionID、leaseID、实例/工作区代次，以及每个工具操作与 executionID 的关联。先续自己的 Run 执行权，等待环境期间也不能让它过期。
2. 申请响应曾丢失时，用原 caller 与 requestKey 查询原 Acquisition。不能换键重新申请，也不能把新 epoch 填入旧请求重试。原申请尚未 Ready 时，新 worker按同一 Run 的清理权限取消并等到清理确认；若与 Ready 竞争，则查询分配结果并转入释放流程。
3. 申请已 Ready 时，查询原 Lease 和 Execution。原工具响应丢失时，以原 `leaseID + requestKey` 查命令；保存过 executionID 时直接按 ID 查。Run epoch 变化不改变原命令的 requestKey、归属和输出序列。
4. 已终态且结果完整可信的命令，由新 worker 用当前 Run 执行权提交原 Step 的结果；仍运行的命令继续只读观察，受原租约和命令期限约束。需要结束时请求旧租约释放，不通过复制旧 LeaseProof 取得写入、stdin 或续租权限。Unknown 或输出缺口进入对账/待确认，不自动重放。
5. 新 worker 可以凭当前同一 Run 的可信权限请求 `ReleaseLease` 清理旧 epoch 的租约。这个例外只授予关闭入口及清理，不授予旧租约写入、续租、额外检查点创建或放弃文件的权限。Run 已终止后由明确授权的清理控制角色执行；普通旧 worker token 不获得该角色。
6. 释放流程冻结必要保存要求，停止或隔离全部写者，完成已受理保存及发布对账。保存未受理或失败时进入 SaveBlocked，由具有独立保存权限的执行者按冻结范围重试；新 worker 不能自行丢弃。没有停止证据、存在未知在途写入或未满足保存要求时，继续阻塞同一工作区的新写入。
7. 旧租约确认 Released 后，确有后续工作才以新 epoch 和新的申请键申请新租约。沿用冻结的 Profile revision；恢复使用明确确认的检查点或已安全释放的持久工作区。没有有效恢复点时，不把从仓库基准重建称作恢复。
8. 新租约获得 Ready 后才继续后续工具。原 Step 已成功就提交其原结果；原 Step 的结果仍未知就保持待确认。新 lease 下 requestKey 的去重范围不同，不能用“还是同一个工具编号”为理由重复启动原命令。

失联 worker 无法续上游租约后，沙箱本地执行期限最终关闭用户执行；独立控制循环继续停止、保存和回收。不能依赖 worker 的 defer 执行 Release，也不能用删除保存唯一文件副本的 Pod 代替停止进程。

## 请求和持久化约束

| 操作 | 上游 epoch 与沙箱绑定关系 |
| --- | --- |
| Acquire | 请求 epoch 必须匹配可信当前领取；包含在语义哈希中 |
| 激活 Ready | 重新检查原申请 epoch，不在旧申请上切换到新 epoch |
| 新命令、文件写入、检查点、访问写连接及其输入 | 当前可信 epoch 必须等于租约绑定 epoch；继续检查正常租约、代次和准入 |
| RenewLease | 同上，且不超过确认的上游执行权期限；新 epoch 不能续旧租约 |
| Get / 输出 / SSE / 原请求对账 | 资源读授权，无须旧执行权仍有效，不自动续租 |
| Release / CancelAcquisition | 当前同一 Run 的可信领取可清理旧 epoch；终态 Run 由独立清理授权处理 |
| RetryRequiredSave / DiscardBlockedSave | 沿用独立保存/拥有者确认授权，不因 Run 接管增加权限 |

`sandbox_acquire_request` 和 `sandbox_lease` 分别增加非空正整数 `holder_run_epoch`，SDK Acquisition、Lease 和 StoredAcquireRequest 同步保存。执行与操作通过原 leaseID 关联冻结 epoch，不增加另一套可变持有者。Acquire Ready 结果的 JSON 快照也包含它。现有 action、execution 去重命名空间不变；Acquire 的规范化输入增加该字段。

沙箱侧的申请即使已经 Ready，也不能仅因申请结果过期就清除身份快照：关联租约或排队副作用仍需复核时，GC 必须保留该申请的 `request_spec`，直到相关依赖终结。

Agent 侧需要保存两层记录，后续实现必须与 API/worker 一起完成：

- Run 的环境记录：申请前持久化申请键、Profile/工作区要求、申请 epoch；受理后补充 acquisitionID、leaseID、实例/工作区代次和释放/保存操作 ID。记录应可被清理流程查询，不能只放 worker 内存。
- Step 的外部执行记录：调用前固定原 leaseID 与 requestKey，受理后保存 executionID、读取游标和可信终态。新 epoch 对账时保留旧关联，使用当前 Run 执行权提交结果。

首版沿用 worker 已有 `runID:callID`，不随接管更换业务操作标识。Sandbox Step 的派发准备与外部关联在同一 durable 事务提交后才能调用网络；命令关联 executionID，文件写入关联 operationID。历史 Started 且无关联的 Unsafe Step 不自动补键并重跑。Run 环境记录保留历史 epoch，不能只覆盖一个 currentLeaseID。

这些记录仍待加入 Agent 的正式持久化实现。本次只同步沙箱设计契约，不把上述状态塞入现有 Eino checkpoint，也不将声明字段视为已实现恢复。

## 现有 worker 的接入改动

`internal/adapters/eino.go` 当前直接创建 AIO Backend，后续改为注入使用 Sandbox SDK 的文件/Shell Backend。已有 Eino 工具定义和参数校验可复用。`internal/adapters/aio/backend.go` 的 commandID 与输出游标当前仅在内存中，不能直接作为恢复适配器。

`BeginStep` 当前对未完成 Unsafe 步骤返回 Uncertain。后续增加有外部执行记录时的只读对账路径，再决定提交已知结果、继续观察或进入人工确认；不能把 execute 全部改成 Idempotent。只有实际明确未派发的事实才允许首次派发，普通 NotFound 不能证明命令从未执行。

原 AIO 适配器在 context 取消后尝试 Kill。新适配器应区分 worker 停机/失去执行权、用户明确取消和命令到期：关闭本地请求或输出流不意味着取消远程命令；显式取消由经过授权的控制面处理，失联由本地执行截止机制兜底。

## 必须验证的场景

| 场景 | 通过条件 |
| --- | --- |
| Acquire 已成功但响应丢失，随后换 WorkerID/epoch，CallerID 不变 | 用新凭据查回原申请；不重复创建或重复占用配额 |
| 准备期间 Run 被接管 | 旧申请不能向新 epoch 发放写权限；Ready/取消竞争可查可清理 |
| 申请受理后控制器重启，再激活或派发 | 从持久快照恢复原主体和领取，重新核验；原领取失效则拒绝，不替换为当前 worker |
| 新 worker 接管，旧 worker 恢复 | 旧 epoch 的新请求和续租被拒；新 worker 同样不能写旧租约 |
| 旧请求在检查后延迟到达 Runtime | 准入屏障或物理隔离生效；未清理完不发放新工作区写权 |
| 命令启动响应丢失 | 按原 leaseID/requestKey 查回；Unknown 不再次启动 |
| 命令结束、Step 提交前强杀 worker | 新 worker 查到原结果并只提交一次，不再次执行命令 |
| 保存失败后发生接管或租约到期 | 停止用户写入并保留文件与配额；明确授权重试或放弃才解除保存阻塞 |
| Agent 权限查询失联 | 新副作用和续租失败关闭；原命令受有限本地期限控制，读对账不被误判成功 |

验收同时观察两个数据库的记录、Runtime 中的实际进程、文件内容与用户看到的状态。2026-10-22 已完成 [独立 AIO 底层探针](aio-runtime-verification.md)：普通子进程停止后文件保留，但脱离进程漏停、命令超时不关闭新写入、服务重启丢失命令查询。必须先补齐受保护执行层，再实现控制面的分配与执行链；上述完整接管场景仍未完成实测。

后续 [supervisor 最小实验](supervisor-verification.md) 已验证整租约独立 PID namespace 终止、保留工作区、稳定 ID 回执和崩溃 Unknown。它尚未控制 AIO 的全部写入入口，也未实现续租与 fencing，因此不计作上述完整场景通过。
