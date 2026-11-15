# 长任务边界

## 跨 Run 续跑必须等待工作区保存确认

`Release` 成功可以表示进入 `SaveBlocked`，不等于检查点已经保存。持久环境必须由中心在确认保存和销毁后，以同一次更新写入 `SavedCheckpoint` 与 `Released`。Worker 对必要保存环境不自行关闭记录；否则调度器可能提前续跑，拿到空工作区或把仍在保存的 Goal 错误阻断。

`QueueGoalIteration` 在存在 Preparing/Active 环境时等待；保存收尾后，只接受上一 Run 当前 epoch、相同 profile 下唯一的 Released 检查点。下一 Run 冻结 `RestoreFromSandboxID`，不改上一 Run 快照。中心校验来源 tenant/owner/profile、停止与销毁事实，以及当前 Run 的冻结绑定，再自行解析内容寻址检查点。恢复完成才启动新沙箱；不能接受调用方传入任意检查点路径。

证据：API `TestGoalIterationRequiresSavedWorkspace` 覆盖保存等待、保存完成续跑、缺失检查点、错误 epoch/profile、Lost 与多来源拒绝；Worker `TestGoalIterationAcquiresSavedWorkspace` 使用真库和受控 HTTP 中心，调用实际 adapters.Build，验证两轮 Goal 的 RequiredSave、来源传递与完成。`TestAcquireRejectsMismatchedWorkspace` 验证响应绑定异常拒绝；`TestClosePreservesUnconfirmedWorkspace` 验证保存未确认时环境保持 Active。

当前仅支持 Sandbox Provider 的 `/workspace` 及其子目录，AIO 不具备本链路。检查点 hostPath 依赖单节点，跨节点共享存储和容量/耗时压测须单独完成；两轮 Goal 的 HTTP 替身测试不代表真实模型与 Kubernetes 全链路已验证。

2026-11-09 主代理在本地 orbstack 实际运行 `TestCheckpointRestoreHTTPE2E`，验证新 Run 的 HTTP Acquire 恢复、内容/权限/软硬链接、幂等申请不覆盖文件，以及检查点缺失时不激活并清理 Pod/PVC。`TestRequiredSaveLayerfsE2E` 通过跨 Run Acquire 比较完整目录树摘要一致，用例总耗时 32.56 秒，不代表单次恢复延迟。两仓 `make integration`（全量 `-race` 与 `go vet`）通过；`make doc` 已同步新增字段。恢复需要配置 `Sandbox.CheckpointImage` 与 `CheckpointStoreHostPath`。

## 压缩只能改变模型投影

`durable_message` 是审计事实，Eino opaque checkpoint 是执行恢复状态；二者都不应为减少模型输入而裁剪。`CompactionContext` 在有效租约内读取当前 Run 的完整消息，不能把会话 Seq 当作模型输入下标，也不能把会话接口单页 200 条视作全部历史。`CompactRun` 检查完整工具批次切点，摘要、切点和事件同事务保存。

Worker 首次摘要包含冻结 History，后续合并旧摘要和新增覆盖批次。保留批次通过 ToolCall ID 核对后复用 Eino 原消息，保留 reasoning 和工具签名。摘要失败继续使用旧投影；没有旧摘要时保留原输入。独立摘要请求不携带主调用的 tools/tool_choice。压缩不延长 MaxTurns，也不缩小 opaque checkpoint。

证据：API `pkg/durable/compaction_test.go` 覆盖切点、幂等/冲突、事件回滚、200+ 消息与 Run 隔离；Worker `internal/engine/compaction_test.go` 覆盖新 Store/Engine 接管、失败重试、冻结 History、多轮合并及元数据保留，`internal/adapters/summarizer_test.go` 检查无工具请求与不完整摘要拒绝。2026-11-09 双仓 `make integration` 通过，包含全量 `-race` 与 `go vet`，使用本地 go.work 和测试 PostgreSQL/Redis。压缩接管测试模拟取消与失租；触发压缩后的进程强杀、真实模型摘要质量与用量仍待单独验证。

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

当前 Worker 其他 engine 测试仍调用 API 重构中已移除的 `CreateAgent`、`PublishAgent`、`Submit`，包级测试编译失败。以下单文件入口已单独编译通过，可复跑本用例；不将该入口替代全仓回归。仍依赖本地 API checkout 与 go.work/replace。

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
