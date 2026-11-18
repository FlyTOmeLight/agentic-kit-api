# 长任务边界

## 跨 Run 续跑必须等待工作区保存确认

`Release` 成功可以表示进入 `SaveBlocked`，不等于检查点已经保存。持久环境必须由中心在确认保存和销毁后，以同一次更新写入 `SavedCheckpoint` 与 `Released`。Worker 对必要保存环境不自行关闭记录；否则调度器可能提前续跑，拿到空工作区或把仍在保存的 Goal 错误阻断。

`QueueGoalIteration` 在存在 Preparing/Active 环境时等待；保存收尾后，只接受上一 Run 当前 epoch、相同 profile 下唯一的 Released 检查点。下一 Run 冻结 `RestoreFromSandboxID`，不改上一 Run 快照。中心校验来源 tenant/owner/profile、停止与销毁事实，以及当前 Run 的冻结绑定，再自行解析内容寻址检查点。恢复完成才启动新沙箱；不能接受调用方传入任意检查点路径。

证据：API `TestGoalIterationRequiresSavedWorkspace` 覆盖保存等待、保存完成续跑、缺失检查点、错误 epoch/profile、Lost 与多来源拒绝；Worker `TestGoalIterationAcquiresSavedWorkspace` 使用真库和受控 HTTP 中心，调用实际 adapters.Build，验证两轮 Goal 的 RequiredSave、来源传递与完成。`TestAcquireRejectsMismatchedWorkspace` 验证响应绑定异常拒绝；`TestClosePreservesUnconfirmedWorkspace` 验证保存未确认时环境保持 Active。

当前仅支持 Sandbox Provider 的 `/workspace` 及其子目录，AIO 不具备本链路。检查点 hostPath 依赖单节点，跨节点共享存储和容量/耗时压测须单独完成；两轮 Goal 的 HTTP 替身测试不代表真实模型与 Kubernetes 全链路已验证。

2026-11-09 主代理在本地 orbstack 实际运行 `TestCheckpointRestoreHTTPE2E`，验证新 Run 的 HTTP Acquire 恢复、内容/权限/软硬链接、幂等申请不覆盖文件，以及检查点缺失时不激活并清理 Pod/PVC。`TestRequiredSaveLayerfsE2E` 通过跨 Run Acquire 比较完整目录树摘要一致，用例总耗时 32.56 秒，不代表单次恢复延迟。两仓 `make integration`（全量 `-race` 与 `go vet`）通过；`make doc` 已同步新增字段。恢复需要配置 `Sandbox.CheckpointImage` 与 `CheckpointStoreHostPath`。

## 压缩只能改变模型投影

`durable_message` 是审计事实，Eino opaque checkpoint 是执行恢复状态；二者都不应为减少模型输入而裁剪。`CompactionContext` 在有效租约内读取当前 Run 的完整消息，不能把会话 Seq 当作模型输入下标，也不能把会话接口单页 200 条视作全部历史。`CompactRun` 检查完整工具批次切点，摘要、切点和事件同事务保存。

Worker 首次摘要包含冻结 History，后续合并旧摘要和新增覆盖批次。保留批次通过 ToolCall ID 核对后复用 Eino 原消息，保留 reasoning 和工具签名。普通摘要失败继续使用旧投影；没有旧摘要时保留原输入。usage 未知、记账失败和预算错误直接传播，见下节。独立摘要请求不携带主调用的 tools/tool_choice。压缩不延长 MaxTurns，也不缩小 opaque checkpoint。

证据：API `pkg/durable/compaction_test.go` 覆盖切点、幂等/冲突、事件回滚、200+ 消息与 Run 隔离；Worker `internal/engine/compaction_test.go` 覆盖新 Store/Engine 接管、失败重试、冻结 History、多轮合并及元数据保留，`internal/adapters/summarizer_test.go` 检查无工具请求与不完整摘要拒绝。压缩接管测试模拟取消与失租；触发压缩后的进程强杀与真实模型摘要质量不在该证据范围。

## 模型用量必须可恢复且未知不等于零

把缺失 usage 当作 0 会绕过预算并丢失计费事实。流式响应如果在 usage 确认前把消息交给工具，响应已经产生费用但平台尚未记账；PendingModel 如果只恢复消息不恢复用量，接管后可能重复请求或重复计费；摘要如果不走同一预算和记账链路，可以绕过 Goal 上限。

Worker 对 `ResponseMeta`/`Usage` 缺失返回 `ErrModelUsageUnavailable`，停止主执行或摘要，不记 0、不执行工具。provider 明确返回的 0 是已知 0，正常写入 usage 行。OpenAI 响应修饰器保留原始 usage 缺失，避免 SDK 合成零值后误判为已知用量。

主模型在调用前、上下文构建后和响应记账后各检查预算。`call="model"` 使用调用前捕获的 Turn；`call="summary"` 使用 `State.SummaryCalls`，递增计数与 usage/state 同事务持久化，写入失败还原计数并传播错误。摘要先检查预算，usage 未知、记账失败和预算错误都确定性传播；只有普通摘要失败才记录压缩失败并继续旧投影。摘要返回内容不完整但 usage 已知时，仍先记账再拒绝摘要。

成功响应暂存为 `PendingModel`，与 Turn、usage 和 Run state 同事务提交；接管直接应用该响应，不再请求模型。记账失败恢复 Turn 和 PendingModel 并传播错误，响应已经返回也不继续工具。流式响应完整缓冲、合并为单条消息后统一校验和记账，最后只返回合并后的消息流，合并元数据也随该消息保留。

API `RecordUsageWithCost` 先读取 Run 的 owner/goal 绑定，锁 Goal，再锁 Run。重复 `(run_id, turn, call)` 不重复计费；新用量与 Run/Goal 汇总、state 和 `UsageRecorded` 事件同事务提交。Worker 根据 Run 冻结的模型价格提交费用；提交时空价格表冻结为仅 token 计量、费用为 0；此时若 Goal 的 `MaxCostUSD > 0`，主模型和摘要调用前都会失败。

非空价格表缺模型在 API 提交时拒绝。有价格快照但 usage 只有正数总 token 而无法拆分计价时，主模型与摘要都将已知 token、零费用和带 `PricingFailure` 的 state 同事务提交，再返回确定性计价错误。这里的零费用表示未完成计价；计价失败与预算耗尽分别处理，摘要不能将计价失败当作普通压缩失败后继续主模型。接管会先检查持久化的 `PricingFailure`，在应用 PendingModel、继续摘要或请求模型前继续阻断；同一 usage 编号的重放会去重，不能补价。

预算只在调用前检查、响应后记账，没有预占或在途额度。并发调用和单次超预算响应可以超过上限；响应已产生、记账事务尚未提交时进程退出，费用事实未知，重试可能再次产生费用。当前保证已提交用量的幂等恢复，不承诺严格硬上限。

证据：API `pkg/durable/usage_test.go` 覆盖预算阈值、已知 0、幂等重放、旧租约、非 Active Goal 汇总、费用字段、Goal→Run 锁序与父子 Run 并发记账。真实 PostgreSQL 屏障测试曾复现反向锁序的 `SQLSTATE 40P01`，统一 Goal→Run 锁序后同场景通过。Worker `internal/engine/usage_test.go` 覆盖缺失/已知 usage、记账前工具阻断和 PendingModel 接管，`internal/engine/compaction_test.go` 覆盖摘要预算、未知 usage、计价失败与记录失败，`internal/adapters/summarizer_test.go` 覆盖受控 HTTP 响应的 usage 解析与“内容失败但 usage 已知仍记账”。`internal/engine/pricing_test.go`、`pricing_recovery_test.go` 覆盖缓存价继承与显式零及计价失败后的接管阻断。两仓全量集成的验证范围见 `docs/verification.md`；真实供应方账单与严格硬预算不在这些测试的保证范围。

## 派发中崩溃必须 fail-closed

模型或摘要请求已派发、usage 尚未持久化时 Worker 崩溃，消费事实未知。自动重放会重复产生费用并可能重复执行工具；把未知消费记为 0 会绕过预算。两者都不能接受。

Worker 在派发前用 `Store.SaveState` 把 `State.InflightModel{Kind, Sequence}` 持久化，usage 与清除 marker 同事务提交。接班人发现 marker 即提交 `BoundaryBlock`：Run 置 `Blocked`（非终态）、Goal 置 `GoalBlocked`、发出 `RunBlocked`，不 Build、不调用模型、不执行工具、不记 0。同进程内调用错误或摘要成功但 usage 未提交也保留 marker 并阻断；返回错误不证明请求未发送。usage 已提交但摘要调用失败时，marker 已被记账事务清除，Run 记录 CompactionFailure 后继续，不能重新标记、不能重复记账。

`SaveState` 走租户作用域，只认 lease 三元组且要求 Running；Blocked、过期、错 worker、错租户统一返回 `ErrLeaseLost`。SSE 在 Terminal 或 Blocked 后关闭流，保证调用方收到 `RunBlocked`。

证据：Worker `TestInflightModelBlocksReplacement`、`TestInflightSummaryBlocksReplacement`、`TestCompactionRecordedUsageSurvivesSummaryError`、`TestProcessKillResumesWithoutRepeatingTool`（真实进程 kill 后 Blocked、工具不重复、SSE 含 `RunBlocked`）；API `TestSaveState`、`TestSaveStateTenantIsolation`。Blocked 只接受下节的人工恢复，不自动解除。

## 人工恢复必须说明接受了什么

`unblock(epoch, reason)` 表示人工接受未知消费并授权重新派发，不能表述为“原请求未发送”或“补记零费用”。恢复只清除派发标记，不增加 usage，不改变 checkpoint 或已完成步骤。旧 epoch、到期、忙会话、预算耗尽、Stall 达阈值均拒绝；改变状态、递增 epoch 和 `RunUnblocked` 审计必须同事务，事件写入失败则回滚。Goal→Run 的锁顺序与记账保持一致，会话锁不可得立即冲突，避免反向等待。

API 解析的 `version/inflightModel/pricingFailure/pendingModel` 成为共享 execution 控制头，checkpoint 仍由 Worker 拥有。用 `map[string]json.RawMessage` 定点删除 marker，避免反序列化为 `map[string]any` 损失大整数精度。仅支持 version 3；损坏标记、计价失败或已有 PendingModel 都不猜测修复。恢复 child 不直接恢复 Goal；根 Run 仍为 ActiveRunID 且无其他 Blocked Run 时才恢复 Goal。

摘要“成功但未回报 usage”和“普通错误未回报 usage”也属于未知消费，不能降级成 CompactionFailure 后继续主模型。恢复测试须既检查模型重新派发，又检查此前完成的工具没有重复调用。已知 usage 的调用错误仍按记账后重试处理；测试 Stream 替身须实际产出响应及 usage，不能用 nil 回调产生的 panic 代替 provider 错误。

模型装饰器还须保留底层 callbacks 能力。Eino ADK 对未管理 callbacks 的模型自动套包装器，Generate 遇到错误会丢弃同时返回的 message；边界因而拿不到其中的 usage，只能按未知消费阻断。Worker 的 usageModel 通过 `components.IsCallbacksEnabled` 转发底层能力，WithTools 后重新检查绑定后的模型，不能无条件返回 true。已知 usage 错误的回归应模拟真实 callbacks 契约，断言记账后 Queued；未启用 callbacks、边界未取得响应的场景则仍应 Blocked，不能用后者替代前者验收。

证据：API `TestReleaseInflightModel`、`TestUnblockResumesAndAudits`、`TestUnblockRejectsWithoutMutation`、`TestUnblockBusySessionAndRollback`、`TestUnblockConcurrentSingleWinner`、`TestUnblockChildRequiresGoalResume`、`TestSDKUnblockRun`；Worker `TestUnblockResumesWithoutRepeatingCompletedTools`、`TestCompactionUsageGovernance`、`TestModelUsageStopsToolsAndRetriesKnownFailure`、`TestUsageModelForwardsCallbacks`、`TestBuildModelPreservesCallbacks`。这些检查使用本机 PostgreSQL 与受控模型，不验证真实供应方账单或人工界面。

## 价格必须随 Run 冻结

Worker 每次执行时读取部署价格，会让同一 Run 的接管、Goal 后续轮次或 Child Run 因部署改价而改变费用依据。API 在接受 Chat/Application 前冻结版本、模型、BaseURL 与单价，幂等 replay 先返回已接受的 Run；Goal/Child 复制持久化 Spec，不再次查询价格表。

`PricingFrozen=true`、`PricingSnapshot=nil` 明确表示提交时选择仅 token 计量。`PricingFrozen=false` 表示旧 Run 没有冻结事实，Worker 必须在模型调用前拒绝，不能回退当前价格；应经 API 重新提交。主模型和摘要共用同一模型及快照。空表、零价与计价失败是三个不同事实，不得都解释为供应方免费。

证据：API `pkg/execution/pricing_test.go` 检查稳定价格版本及空表/缺模型，`internal/app/execution/pricing_test.go` 覆盖提交、改价后 replay，`pkg/durable/pricing_inheritance_test.go` 覆盖 Goal/Child 继承及持久化往返；Worker `internal/engine/pricing_snapshot_test.go` 覆盖冻结标记、快照身份、非法价格及费用预算。Worker config 测试还覆盖拒绝遗留 `[Pricing]` 参数，防止运营误以为 Worker 配置仍能改价。

## Steering 消费必须保存可恢复状态

只原子清空 `pending_steer` 不够：若 Worker 在清空后、保存模型调用 checkpoint 前退出，下一次执行无法重建纠偏指令。`TakePendingSteer` 在持有 Run 租约与行锁期间调用本地 checkpoint 编码函数，将清空槽位和更新 state 同事务提交。编码函数只构造 JSON，不进行外部 I/O。

证据：`pkg/durable/goal_test.go` 的 `TestSteerGoalAndTakePendingSteer` 检查旧租约拒绝、非法 checkpoint 回滚、消费后 state 已持久化；Worker 的 `TestSteeringSurvivesWorkerTakeover` 在模型调用取消后让租约过期，由新 Store 和新 Engine 接管，验证指令仍只出现一次。后者模拟接管，不是新增的强杀进程测试。

## 验证签名后必须固定字段来源

`binder.All()` 会合并 JSON、Query、URI；JSON 已签名不代表最终绑定对象已签名。回调分别绑定 URI 与 JSON，避免 Query 替换 `StepID`、`ExternalRef`、`Result`。HMAC 同时覆盖方法、路径、来源与原始正文，避免签名改投另一个 Run。

证据：`service/router_test.go` 的 `TestSignedCallbackResumesRunWithoutQueryOverride` 通过真实存储验证回调恢复、相同结果重放与 Query 隔离；`TestCallbackSecurity` 和 `TestCallbackBodyLimitsAndJSONBinding` 覆盖签名、来源、正文上限及字段来源。

## Stall 按不同 Run 累计

Goal 推进可被 Worker 或接口重复调用。Run 的 `stall_mark` 与 Goal 计数同事务写入，标记已处理结果，避免同一失败 Run 被重复检查三次就阻断。非终态不推进，缺失验收或 Error 不计 Stall。

证据：`TestStallMarkDetectsNoProgress`、`TestCheckGoalProgressBudgetExceeded` 覆盖重复检查的计数和用量归集。

## 沙箱命令必须有确定退出码

execd 启动失败或流截断时，回执可能是 `Failed` 且退出码为空。Worker 的输出解析器默认值不能当作 exit 0 使用。API 从终结事件提取退出码并保存回执；Worker 的普通命令与 Evaluator 消费回执时，都把缺失退出码作为执行错误，保留错误原因。

证据：API `TestParseExecdOutput`、`TestCompletedRunAllowsPostRunEvaluationOperation`；Worker `TestExecuteRequiresDurableExitVerdict`、`TestHTTPSandboxRunnerVerdictMapping`。其中信号终止的负退出码归入执行错误，不作为验收失败计入 Stall。

## 验收后释放不等于验收可恢复

Worker 的 `TestWorkerEvaluatesGoalBeforeSandboxRelease` 使用真实 PostgreSQL、SDK HTTP 与沙箱 Backend，验证阻塞验收时不释放环境，验收回执与 Goal 状态提交后才调用释放。模型和沙箱中心均为替身，不覆盖真实 Guard 的期限执行。

2026-11-09 只读审查发现固定 100 秒续期晚于默认 30 秒 claim；当前按服务端 `HardExpiresAt` 剩余时间提前续期，相关行为由 Worker `renew_test.go` 验证。后续修复为 Completed 后的验收提供受绑定关系与截止时间约束的续租授权，并在验收未确定或持久化失败时保留环境，具体规则见下文。独立领取与强杀恢复已在后续阶段完成，证据见末节。

## 验收未可靠完成时保留环境并停止续租

Completed 会清空 Run 的 worker_id/lease_until。满足条件的完成事务保存 `evaluation_worker_id` 与 `evaluation_deadline = LEAST(deadline, clock_timestamp() + interval '10 minutes')`。这两个字段是验收授权边界，不能退化为只检查 Run deadline，也不能通过续租重新起算验收窗口。

API 的 `authorizeEvaluation` 为续租和验收派发复用相同条件：原 Worker 与未过期的验收窗口、Active Goal 指向 Completed Run、原环境和沙箱绑定一致、没有 Evaluation 回执、command 配置非空。授权有效期取持久化验收窗口、Run deadline、Goal 创建时间加总时长预算的最早值；续租再与请求期限取最早值。时间判断使用数据库时钟，普通命令与申请仍走原 Running claim 授权。

证据：API `TestCommitBoundaryCompleteStartsEvaluationWindow` 验证窗口写入与数据库时间；`TestCompletedEvaluationRenewsWithinDeadline` 验证续期同时遵守验收窗口、Run、Goal 和请求期限；`TestCompletedRunOnlyAllowsBoundEvaluation` 与 `TestCompletedRunEvaluationRenewal` 覆盖过期、错误 Worker/epoch、暂停 Goal、已记录验收及错误环境绑定时拒绝续租和派发；`TestEvaluationAuthorizationPreservesDatabaseError` 确认数据库故障不会被掩盖为授权失败。

`advanceGoal` 返回是否允许释放。Passed/Failed 回执和 Goal 推进都成功后释放；读取 Run/Goal/Evaluation 失败、回执或 Goal 推进提交失败、EvaluationError 时均保留现场。保留调用 `Backend.Detach`，取消并等待续租协程退出，同时拒绝该 Backend 的新命令，不发送 Release，也不将环境标记 Released。沙箱仍受已接受的 HardExpiresAt 约束，到期由 Guard 和 Reconciler 处理。

证据：Worker `TestWorkerEvaluatesGoalBeforeSandboxRelease` 使用真实 PostgreSQL 和 HTTP 替身，验收持续到原 Run claim 截止之后，确认续租仍进行；使用数据库约束注入回执/Goal 写入失败，验证环境仍 Active 且 Release 次数为零。`TestWorkerRetainsEnvironmentWhenGoalReadFails` 覆盖 Goal/Evaluation 查询失败；`TestDetachStopsRenewalWithoutReleasingSandbox` 验证正在进行的续租被取消、协程退出、重复 Detach 安全且新命令被拒绝。

保留现场本身不等于自动恢复。后续阶段为 Completed Run 增加独立验收领取，见下节；Error 回执仍不可被后续结果覆盖。禁止用新操作编号自动重跑不确定命令。上述验证使用本机数据库与受控 HTTP 替身，不包含真实 Kubernetes Guard 的期限接续。

## Completed 的验收执行权必须独立于 Run 租约

Run 完成会清空执行租约，不能依靠常规 Claim 恢复未完成的验收。`evaluation_worker_id` 保存原沙箱 Worker 绑定，新增 `evaluation_claim_worker_id`、`evaluation_epoch`、`evaluation_lease_until` 表示当前验收执行权；两者可能不同，接管不能改写原 Run epoch 或沙箱 holder。Claim 采用数据库行锁和 `SKIP LOCKED`，续期与回执写入都校验有效 claim，期限不超过原验收窗口、Run deadline 和 Goal 总时长预算。

证据：`pkg/durable/evaluation_lease_test.go` 的 `TestEvaluationClaimTakeoverFencesOldWorker` 验证 8 个并发领取只有一个成功、接管递增 epoch、旧 claim 续期/回执和无 claim 首次写入被拒绝。已有 Passed/Failed 回执但 Goal 推进或环境释放未完成时允许领取收尾；已释放环境不再领取。`TestEvaluationClaimEligibility` 覆盖暂停、过期和不可变 Error 排除。

Worker `ResumeEvaluation` 只挂接原沙箱，校验环境、Run epoch、generation、lease 与原 Worker，继承 revision 和期限，恢复续租；禁止该 Backend 调用普通业务工具。验收新派发另携带当前 claim，在 sandbox-center 的操作创建和派发前检查。Error 回执不自动覆盖或重跑。

已有 Passed/Failed 回执时，恢复必须先应用回执推进 Goal，再尝试挂接环境清理。否则在回执写入后崩溃、原沙箱已失效的情况下，挂接失败会挡住本已确定的结果。Worker `TestStoredEvaluationAdvancesWithoutSandbox` 两例先复现 Passed/Failed 均未推进，修复后验证挂接返回错误之前状态已推进且回执不变。

Destroyed/Lost 沙箱关联的残余环境现已纳入最终对账；中心根据可信保存事实回填检查点并关闭环境，见本页跨 Run 工作区恢复。原验收窗口过期后仍不会再次领取验收。

## 网络响应丢失必须查原操作

验收固定使用 `eval-<sandboxID>` 请求键。有 operation ID 则查询原操作；无 ID 则以完全相同参数重放原请求，依赖控制面去重，再读取原回执。操作创建后即写入 `X-Sandbox-Operation-ID`，SDK 在失败响应中保留它。重放请求不能更换请求键，也不能把 Unknown 当作允许再次派发的证明。

证据：Worker `TestHTTPSandboxRunnerSDKReconcilesLostReceipt` 覆盖断连与 OutcomeUnknown；`TestEvaluationRecoversAfterWorkerKilled` 在验收 HTTP 请求处理中强杀真实子进程，由新 Worker 领取并挂接原环境，完成 Passed → GoalComplete → 释放。受控 HTTP 服务端统计 1 次派发和 1 次请求重放；该统计不代表真实 Kubernetes Guard 的副作用去重已经验证。

该验收接管阶段的 API 与 Worker `make integration` 均通过，包含全量 `-race`、`go vet` 与强杀恢复；当时 `make doc` 再生成后的 Go/JSON/YAML SHA256 均不变。后续工作区恢复已在真实单节点 Kubernetes 验证，见本页对应记录。Worker/Evaluator 与真实 Kubernetes 的跨 Run 联合验收见下节；命令成功后回执丢失的失租接续见本页“真实沙箱验收接管要区分操作完成与回执提交”，独立发布依赖仍待验证。

## Goal 跨 Run 恢复要验证真实文件与自动状态推进

仅检查下一次 Acquire 的 `RestoreFromSandboxID` 不足以证明恢复成功；手动写入环境的 checkpoint 也不能覆盖 Saver、Reconciler 与 Worker 的协作。`service.TestGoalWorkspaceWorkerKubernetesE2E` 启动真实 Worker 进程，模型服务返回固定工具调用，工具和 command 验收通过实际 HTTP/SDK 在 OrbStack Guard 沙箱中执行。

首轮命令写入随机内容，验收因缺少修复文件而 Failed。暂不运行 Reconciler 时，实例已停止、环境仍未完成保存；测试在五个 Worker 轮询间隔内持续检查 `QueueGoalIteration` 返回 `ErrNoWork`、Session 只有首轮、Goal 仍指向首轮且没有第二次工具调用。随后强杀该 Worker，启动新进程，再启动 Reconciler；保存与 Goal 推进不再依赖原进程。Saver 保存、中心确认销毁并关闭环境后，新 Worker 自动创建下一 Run 并申请从来源恢复的新沙箱。第二轮命令先核对首轮随机文件，再写入修复文件，实际验收 Passed。最终核对恰有两轮 Run、两轮 `evaluationWorkerID` 不同、保存与恢复来源一致、两组 Pod/Saver/PVC 均消失。

2026-11-13 本机 PostgreSQL、OrbStack 上该用例在待保存屏障强杀 Worker 后以 `-race` 运行通过。测试使用 500ms 对账周期；生产默认一分钟，不能直接套用测试耗时。Worker 二进制通过当前本地 `go.work` 构建；模型质量、多节点存储、独立发布依赖、Saver 已启动后的强杀和恢复 initContainer 期间强杀仍不在本次证据范围。命令见[保存与恢复验证](../sandbox-center/layerfs-save-verification.md#复跑-goal-跨-run-验收)。

## 真实沙箱验收接管要区分操作完成与回执提交

Worker `internal/engine/evaluation_takeover_e2e_test.go` 的 `TestEvaluationTakeoverRealCluster` 通过子进程屏障固定故障窗口：原进程读取成功命令回执后等待，父进程确认中心操作为 Succeeded、屏障就绪且 Evaluation 尚不存在，再强杀并断言退出信号为 SIGKILL。替代 Worker 领取独立验收租约，通过 ResumeEvaluation 挂接原沙箱，取回原操作结果，完成 Passed → GoalComplete → 释放。

命令每执行一次追加一行；替代 Worker 调用真实 HTTPSandboxRunner 后、释放前读取文件，要求只有一行，同时核对只有一条 `sandbox_operation`。直接联表检查环境 Released、沙箱 Destroyed、双方检查点非空且相同；在 fixture 兜底删除之前等待 sandbox/saver/digest Pod 与 workspace PVC 全部消失。该证据覆盖这个接管场景的追加副作用未重复、检查点回填一致及产品自动清理，不能推广到任意外部副作用的恰好一次保证，也不验证检查点内容恢复。

直接 `kubectl exec` 读取 `/workspace` 时须明确 `-c guard`，并用 `setpriv --reuid=65534 --regid=65534 --clear-groups` 切换为工作区身份。Guard 的 root 身份不具备 `DAC_OVERRIDE`，直接读工作区会 Permission denied；未指定容器还会将默认容器提示混入命令输出。计数失败应使测试失败，不能改为跳过检查或放宽工作区权限。

API `testutil/sandboxe2e.StartOnSchema` 启动真实中心 HTTP 服务与 Reconciler，和 durable 使用同一隔离 schema。schema 不一致时中心无法读到 Run/环境，授权会失败；缺少 Reconciler 时必要保存不能自动收尾。释放返回后须等待中心确认检查点，不能由测试手工补写 Released。

2026-11-13 补强后的单文件用例在本机 PostgreSQL、OrbStack 以 `-race` 通过，用例耗时 105.26 秒；该文件 `go vet` 与格式检查通过。耗时包含资源准备、故障注入、接管及保存清理，不是接管延迟指标。夹具直接建立 durable 事实，不覆盖 Agent 发布、提交入口或真实模型。检查点仍为单节点 hostPath；命令执行中、Saver 保存中及恢复 initContainer 运行中强杀仍待验证。

本轮全量测试结果由主代理最终核实。以下单文件入口用于单独复跑本用例，不替代全仓回归；仍依赖本地 API checkout 与 go.work/replace。

```sh
cd /Users/light/Code/agentic-kit-worker
TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable' \
SANDBOX_E2E_K8S_CONTEXT=orbstack \
GOCACHE=/private/tmp/agentic-kit-m0a-build \
go test -race -count=1 -v -timeout 10m \
  -run '^TestEvaluationTakeoverRealCluster$' \
  internal/engine/evaluation_takeover_e2e_test.go
```

镜像与检查点目录沿用夹具配置，可通过 `SANDBOX_E2E_GUARD_IMAGE`、`SANDBOX_E2E_SAVER_IMAGE`、`SANDBOX_E2E_CHECKPOINT_HOST_PATH` 指定。缺少数据库或集群环境变量会跳过测试，必须看到该用例 PASS，不能只看命令退出码。

## 真实租户中间件下测试夹具必须同步契约

API 控制面启用 `internal/client/tenant.go` 的真实租户中间件后，Worker 测试夹具不能只改数据库或只补配置。2026-11-17 的 Worker 全量集成失败来自三处契约漂移：

- 直接 `INSERT INTO agent` 的测试夹具未提供 `tenant_id`，而表结构已要求非空。
- 进程级测试缺少 `[Tenant].Endpoint` 与 `ServiceAPIKey`，`service.Build` 的 `ValidateAPI` 直接拒绝。
- 跨进程请求未携带 `Authorization` 与 `x-forge-tenant-id`，中间件返回 `SCOPE_REQUIRED`。

修复方式是让测试替身同时满足三层契约：数据库夹具写入非空 `tenant_id`；进程配置指向 `newFakeTenantServer`；所有 API 请求携带 Bearer 凭证与租户选择头。fake 服务只需覆盖 `/internal/v1/scope/resolve` 与 `/internal/v1/service/self`，但 scope 响应必须让 `principal.id == user_id`，并按请求回显 `tenant_id` / `project_id`。

证据：修复后 `TestProcessKillResumesWithoutRepeatingTool`、`TestProcessShutdownCancelsInFlightHTTPTool` 及 Worker 全量 `make integration` 通过。该结论只证明测试夹具与当前中间件契约兼容，不证明 Forge 生产配置已验收。

## 提交与续跑的四类一致性边界

2026-11-18 审查发现并修复四类问题，均已合入 main（API PR #20、Worker PR #7）：

- **Goal 绑定竞态**：提交事务内 `active_run_id` 更新必须检查 `RowsAffected`；并发 `TransitionGoal(Paused)` 赢得竞态时整个提交回滚，不留 goal_id 已写入但 Goal 未绑定的孤儿 Run。`TestSubmitRejectsPausedGoalBind` 覆盖。
- **续跑上下文快照**：`QueueGoalIteration` 与聊天提交同样用 `freezeContextWithoutRecall` 冻结并校验快照，写入 `context_snapshot` 列；反馈查询需在冻结前拼入。`TestGoalIterationConcurrentAndFrozen` 断言快照合法且含历史材料。
- **已计费摘要落盘**：Worker 摘要 usage 提交后，`CompactRun` 用 `context.WithoutCancel` 独立上下文继续执行，父 ctx 取消不再丢弃已计费结果，接管侧不重跑摘要、不重复计费。`TestBilledSummaryPersistsAfterParentCancel` 覆盖。
- **决策写入失败保留响应**：`RecordDecision` 失败时内存 `PendingModel` 保留以便重试，但持久化 checkpoint 仍不含 PendingModel，恢复时上下文重建不受影响。注意两个方向都要成立：只清内存丢响应，只留持久化会让恢复失败。`TestRecordDecisionFailureKeepsBilledResponse` 与 `TestEinoCheckpointPrecedesToolAndUncertainOutcomeNeedsReview` 共同覆盖。

双仓全量 `make integration`（含 `-race`）通过。边界不变：本地测试通过不代表生产部署完成，仓库仍无 CI workflow，摘要与决策恢复只覆盖单 Worker 视角，多 Worker 并发接管同一 Run 仍依赖租约互斥。
