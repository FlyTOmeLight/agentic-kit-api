# 任务接口

Go 请求与响应类型统一定义在 [sdk](../sdk/go/agenticv1)，调用方式见 [SDK 用法](../sdk/go/agenticv1/README.md)。SDK 不依赖 Gin；服务端负责校验。

业务接口的身份与权限计划由 forge-tenant-api 提供的 middleware 统一处理。本服务不再维护静态 Bearer token 或 config owner 白名单。middleware 接入后，所有对象按它写入的 owner 隔离；访问其他 owner 的对象返回 404。当前不支持跨 owner 共享 Agent。在 middleware 接入前，本服务没有请求身份，不能视为已完成鉴权。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /v1/agents | 创建 Agent 草稿，返回 201 | name、config |
| GET /v1/agents | Agent 列表 | after、limit |
| GET /v1/agents/{id} | 读取草稿及 revision、publishedVersion | 无 |
| PUT /v1/agents/{id} | 完整替换名称和草稿 | name、config、revision |
| POST /v1/agents/{id}/publish | 发布当前 revision | revision |
| GET /v1/agents/{id}/versions/{number} | 读取不可变版本 | 无 |
| POST /v1/sessions | 创建会话，返回 201 | agentID、可选 title |
| GET /v1/sessions | 会话列表 | 可选 agentID；after、limit |
| GET /v1/sessions/{id} | 读取会话及 messageSeq | 无 |
| POST /v1/sessions/{id}/messages | 提交用户消息并创建任务 | query；Idempotency-Key header 必填 |
| GET /v1/sessions/{id}/messages | 按序读取消息 | after（消息 seq）、limit |
| GET /v1/sessions/{id}/runs | 查询会话任务与状态 | after、limit |
| GET /v1/runs/{id} | 查询任务 | 无 |
| POST /v1/runs/{id}/cancel | 取消 | 无 |
| POST /v1/runs/{id}/answer | 回答澄清 | stepID、result |
| POST /v1/runs/{id}/resolve | 提交人工确认的工具结果 | stepID、result |
| GET /v1/runs/{id}/events | SSE 回放及订阅 | Last-Event-ID header 或 after query，格式 runID:seq |

config 包括 systemPrompt、model、tools、maxTurns，不接受 query/history/agentID/configVersion。模型配置与工具字段见 [创建请求样例](../examples/agents/agents.example.json)。模型地址必须是 HTTP(S)；maxTurns 为 1～100。工具名称不能重复，parameters 必须为 object 类型 JSON Schema，policy 必须显式提供。工具可设置 `returnDirectly: true`；该工具成功时，worker 直接返回工具结果并结束本轮，不再调用模型；错误结果返回给模型处理。此设置随 Agent 发布版本和 Run 快照冻结。工具 `kind` 仅接受空字符串或 `HTTP`；`AppForgeMCP`、`AppForgeOpenAPI` 及其他未知 kind 一律拒绝；ask_for_clarification 必须 `ReadOnly` 且 kind 为空或 `HTTP`，URL 字段可不填。

Agent 创建时 revision=1，编辑成功递增 revision。编辑或发布携带旧 revision 返回 409。同一 revision 重复发布返回同一个版本，版本 number 从 1 开始。仅已发布 Agent 可以创建会话。每次提交消息使用当时最新的发布版本，并把配置写入 Run 快照；恢复时不重新读取草稿或最新版本。Run 响应中的 agentID、configVersion 可用于定位版本。

已发布的 `AgentVersion.config` 来自 artifact-go 的不可变正文，本地只保存版本指针，响应形状不变。发布先完成远端写入，再复核本地 revision 并提交版本指针；远端成功而本地提交失败可能留下未引用版本，但不会占用公开版本号。远端依赖不可用时发布、版本读取和新任务提交返回 503；同一请求已接受后的幂等重放直接返回原 Run，不重新读取 artifact。

提交消息返回 202 和 Run；同一 owner 的相同请求编号、相同 session/query 重试返回 200 和原任务，不重复保存用户消息。发布新版本不改变该行为。请求编号重复但输入不同、同一会话已有活动任务、无效状态转换返回 409。

列表返回 `{"items":[]}`。默认 limit=50；Agent、会话、任务列表最多 100 项，按 ID 升序，after 是上一页最后一项 ID。ID 排序不代表创建时间。消息列表最多 200 项，按会话内 seq 升序，after 默认 0，下一页使用上一页最后一项 seq。Agent、会话和消息时间以 UTC RFC3339 返回。

消息包含 sessionID、seq、runID、kind、role、content、可选 toolCalls/toolCallID、createdAt。kind 包括 User、Assistant、Final、Tool、ClarificationAnswer、ReviewResult。助手工具决策保存为 Assistant，最终回复保存为 Final；澄清回答与人工确认属于工具结果，role 为 tool。通过 toolCallID 与助手 toolCalls 中的 ID 配对。失败和取消从 Run 状态及 Event 读取，不伪造成助手回复。

消息与对应任务状态在同一事务提交。客户端可以用会话消息列表展示历史，用 Run SSE 展示当前任务进度；Event 不作为聊天记录。当前模型上下文取最近 20 个已完成 Run 的 User/Final 消息，工具历史保存在消息表供查询，尚未实现跨轮工具历史与上下文压缩。

任务状态为 Queued、Running、WaitingClarification、NeedsReview、Completed、Failed、Canceled。等待状态时，GET /v1/runs/{id} 与 Run 列表项从 `durable_run` 显式列返回 `waitingStep` 与 `waitingKind`（`Clarification` 或 `Review`），不再从 worker 私有 checkpoint JSON 解析。`POST /v1/runs/{id}/answer` 在 `WaitingClarification` 且 `waitingKind=Clarification` 时提交澄清；`POST /v1/runs/{id}/resolve` 在 `NeedsReview` 且 `waitingKind=Review` 时提交人工确认的工具结果。两处请求体仍为 `stepID` 与 `result`，`stepID` 必须等于当前 `waitingStep`；`result` 是提供给模型的用户回答或已确认工具结果。相关 SSE 事件 payload 在保留 `waitingStep` 的同时新增 `waitingKind`。

SSE 每条消息的 id 为 runID:seq，data 包含 runID、seq、kind 和 payload。客户端按该 id 去重。只在终态且已发送全部提交事件后关闭连接；WaitingClarification 和 NeedsReview 不是终态。重新连接不会启动新的任务。

重要事件包括 RunQueued、RunStarted、ModelCompleted、ToolStarted、ToolCompleted、ClarificationRequested、RunNeedsReview、RunResumed、RunRetry、RunCompleted、RunFailed、RunCanceled。RunRetry 的 payload.status 可能为 Failed，客户端以状态字段为准，不仅凭事件名称判断。

模型尚在生成时不会发逐 token delta；ModelCompleted 表示完整模型决策已经保存，RunCompleted 表示最终答案和完成事件已经同事务提交。

## 进程探针

server-go HTTPHost 提供无需鉴权的 `GET /ping`（200，正文 `pong`）、`GET /healthz`（就绪时 200，正文 `healthz`；未就绪时 503）和 `GET /version`。探针只反映进程与服务生命周期，不持续检查外部依赖。本仓库不提供 Forge 项目工作台或企业资产业务入口。

## 沙箱控制面（阶段 2，默认关闭）

配置 `[Sandbox].K8sContext` 后注册以下路由并启用真实调度；未配置时申请只写入 `Provisioning` 元数据，exec/release 返回 409 `NotReady`。当前边界与验证范围见[阶段 2 验证](sandbox-center/phase2-verification.md)。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /v1/sandboxes | 申请沙箱，返回 sandbox 与首个 lease | tenantId、ownerId、requestKey、profileId、workerId、runId、runEpoch、可选 ttlSeconds、requiredSave、restoreFromSandboxId |
| GET /v1/sandboxes/{id} | 查询沙箱与当前 lease | 无 |
| POST /v1/sandboxes/{id}/renew | 续租 | revision、requestKey、可选 ttlSeconds |
| POST /v1/sandboxes/{id}/operations | 执行命令并写回执 | requestKey、script；验收派发另需 evaluationWorkerId、evaluationEpoch |
| GET /v1/sandboxes/{id}/operations/{opID} | 重查操作回执 | 无 |
| DELETE /v1/sandboxes/{id} | 关闭准入；必要保存未完成时停止实例并保留工作区 | 无 |

申请按 `(ownerId, requestKey)` 幂等；相同请求返回原 sandbox。续租按 lease revision 单调递增，相同 revision 与 requestKey 重放返回原事实，旧 revision 返回 409。执行按 `(leaseId, requestKey)` 幂等：控制面先持久化 intent 和 dispatch 回执再派发，派发结果不可知时操作保留 `Unknown` 并返回 502 `OutcomeUnknown`，客户端应通过回执查询确认，不能直接重试为第二次执行。释放是幂等的；已销毁的 sandbox 直接返回。

`requiredSave` 默认 `false`，申请后不可变，属于幂等摘要。设为 `true` 时，当前 Kubernetes Provider 为工作区申请独立的 1 GiB、ReadWriteOnce PVC，使用集群默认 StorageClass，记录 `workspaceUid`。释放或到期先关闭租约并进入 `SaveBlocked`，再停止原 Pod UID 的 Guard；确认容器已终止后记录 `stoppedAt`，保留 Pod、PVC 与未关闭的环境记录。DELETE 成功可以返回 `SaveBlocked`；调用方必须检查 `status`，只有 `Destroyed` 才表示物理回收已完成。停止结果不明确时返回错误，阻塞状态仍保存在数据库，由对账继续处理。

配置 `[Sandbox].CheckpointImage` 与绝对路径 `CheckpointStoreHostPath` 后，对账循环在实例停止后打包 `/workspace`、核实内容寻址检查点，写入 `savedCheckpoint`，再回收 Pod/PVC 并将检查点回填到 Run 环境记录。保存失败保持 `SaveBlocked`；期限与普通释放不能解除阻塞。没有公开保存确认或放弃接口。当前检查点目录使用单节点 hostPath，尚未接入共享对象存储。

`restoreFromSandboxId` 指定已保存的来源沙箱，并要求 `requiredSave=true`。中心验证来源已停止且为 `Destroyed`、`savedCheckpoint` 非空、tenant/owner/profile 及 profile revision 一致，并校验来源 ID 匹配当前 Run 冻结的 Filesystem 配置，不能省略或替换。客户端不能提供任意检查点路径；中心解析并冻结 `resumeCheckpoint`。需配置 `Sandbox.CheckpointImage` 与 `CheckpointStoreHostPath`；恢复成功后新沙箱才允许激活。响应同时返回 `restoreFromSandboxId` 和 `resumeCheckpoint`，Worker 验证绑定后才激活环境。单节点真实 Kubernetes 验证见[跨 Run 工作区恢复](lessons/long-task-boundaries.md#跨-run-续跑必须等待工作区保存确认)。

恢复使用 Pod init container，工作区通过校验和原子目录发布后 Guard 才能启动。恢复失败不会发放 lease；未激活实例经释放或超时对账清理为 `Lost`，不保存半成品，来源检查点仍然保留。调用方重新申请使用新 requestKey。配置未启用检查点能力时，运行时恢复请求返回 409 `NotReady`。

Completed Run 的原沙箱可为 command 验收继续续租。完成事务为满足条件的活跃 Goal 与环境保存 `evaluation_worker_id` 和 `evaluation_deadline`，窗口最长为数据库时间起算 10 分钟，且不超过 Run deadline。原沙箱租约继续匹配该 Worker；Goal 仍为 Active 且指向该 Run，环境、沙箱、租约的 epoch/generation/owner 绑定一致，尚无 Evaluation 回执，当前沙箱租约和验收窗口均未过期。续租期限取请求 TTL、验收窗口、Run deadline 与已配置 Goal 总时长预算的最早值，运行时还可进一步缩短期限。过期或已关闭租约不能复活。

当前验收执行者由独立的 `evaluation_claim_worker_id`、`evaluation_epoch`、`evaluation_lease_until` 表示，可与原沙箱 Worker 不同。新派发只允许固定的 `eval-<sandboxID>` 请求键与 Goal 配置的验收命令，另须提交匹配有效 claim 的 `evaluationWorkerId`、`evaluationEpoch`，派发前再次检查。普通命令仍要求有效 Running claim。操作创建后，响应通过 `X-Sandbox-Operation-ID` 返回操作 ID，包含后续失败响应；SDK 保留该 ID 供查询。网络响应丢失且没有 ID 时可同参重放原请求以获取去重回执，不得换请求键重新执行。

执行输出保存 guard/execd 的原始事件流，未解析为结构化结果。当前路由未接入 Forge 租户身份，`tenantId`/`ownerId` 由调用方提供。

## Gin 请求与错误契约

对象接口返回 JSON，列表返回 items；取消与回答成功返回 204。错误统一返回：

```json
{"code":"InvalidParameter","message":"invalid request body","requestId":"request-id","statusCode":100400}
```

错误码包括 Unauthorized、Forbidden、InvalidParameter、PayloadTooLarge、InvalidCursor、APINotFound、NotFound、MethodNotAllowed、Conflict、InternalError。请求体超过 1 MiB 返回 413，未知字段或连续多个 JSON 文档返回 400；URI、Query、Header 与 JSON 在同一请求对象中完成绑定和校验，参数错误统一返回 InvalidParameter（包含消息 after 为负数）；SSE 的复合事件游标格式错误仍返回 InvalidCursor。绑定错误返回框架校验信息，未分类的内部错误不向客户端暴露数据库详情。`statusCode` 是框架错误编号，实际 HTTP 状态以响应状态行为准；`requestId` 用于关联日志。

请求日志只记录方法、路由模板、状态与耗时，不记录 token 或请求体。Recovery 在响应未提交时返回 JSON 500；SSE 已开始后只结束处理，不向事件流追加 JSON 错误。这些行为由 server-go HTTPHost 统一管理；身份鉴权由后续接入的 Forge middleware 负责，调用方应用负责项目业务授权。SSE 不设整段响应的写入超时，调用方取消与进程退出会结束订阅。SSE 响应头、事件编码、心跳和逐次写入期限由 server-go stream 管理，底层使用 gin-contrib/sse 编码，字段冒号后是否有空格不影响标准 SSE 消费端。

## 受信任应用执行

`POST /internal/v1/executions` 的服务身份与调用权限由 Forge middleware 校验。请求为：

```json
{"spec":{"agentID":"已发布 Agent ID","agentVersion":1,"scope":{"tenantID":"t","projectID":"p","actorID":"u","taskID":"task","roundID":"round"},"query":"本轮要求"},"timeoutSeconds":1800}
```

Agent 定义先通过 `/v1/agents` 创建、编辑与发布。应用执行必须引用明确的发布版本，不能传入 systemPrompt、model、tools 或 maxTurns。运行时读取该版本并冻结完整执行快照；不创建临时 Agent。旧的内联定义请求返回 400，不保留第二条定义路径。

同一 owner、tenant、project、task 复用同一个 Session；不同项目的同名 task 不共享历史。后续轮次允许选择同一 Agent 的其他已发布版本，但不能在同一任务中更换 Agent 身份。同一会话已有活动 Run 时返回 409。上下文策略与普通聊天一致：最近 20 个已完成轮次的用户输入与最终答案。

幂等键必填。同键同输入返回原 Run，包括发布新版本之后的重试；同键不同输入返回 409。Agent、发布版本不存在或属于其他 owner 时返回 404。创建会话、Run、输入消息与事件在同一事务提交。应用服务凭据不能下发给浏览器；应用从已验证的用户与任务构造 scope，敏感操作仍动态检查业务权限。

`GET /internal/v1/executions/{id}/lease?epoch=N` 返回当前数据库时钟下的执行权；取消、旧 epoch、超过 deadline 或其他 owner 均无法通过。模型工具参数不能提供该执行身份，Worker 在 HTTP headers 中注入真实 run ID 和 epoch。运行时执行权不替代业务授权或外部进程停止确认。projectID 是可选的执行上下文；工具或文件系统绑定本身不要求选择项目，资源是否允许使用由调用方授权。

## Public run trace

`GET /v1/runs/:id/trace?after=0` (RunAPI.Trace) returns an owner-scoped `RunTrace`: the current public Run, up to 50 events in sequence order, `latestEvent`, `nextSeq`, and `hasMore`. Repeat with `after=nextSeq` while `hasMore`, then poll for new events. Each event includes `createdAt`. The snapshot uses a repeatable-read transaction; refreshing or reconnecting replays persisted events. `ModelStarted` is committed before each model call. Tool events include their public name; ToolStarted includes public arguments recovered from assistant messages, including historical runs. ToolCompleted includes the saved result. `arguments`, `result`, `output`, and `detail` previews are capped at 32 KiB UTF-8 and marked with `<field>Truncated: true`; full originals remain in storage. No checkpoint, hidden reasoning, system prompt, model/tool credentials or lease identity is returned. This endpoint is read-only and does not resume tasks. Invalid negative/non-numeric cursors return 400; another owner’s run returns 404. Responses use Cache-Control: no-store.

## 内部 Goal 与验收

Goal 是跨多个 Run 的内部控制面对象，不进入公开 API。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /internal/v1/goals | 创建或按幂等键重放 Goal | sessionID、requestKey、requestHash、objective、可选 evaluator、budget |
| GET /internal/v1/goals | 按状态列出 Goal | 可选 status、limit |
| GET /internal/v1/goals/{id} | 查询 Goal | 无 |
| POST /internal/v1/goals/{id}/pause | 暂停 Goal | 无 |
| POST /internal/v1/goals/{id}/resume | 恢复 Goal | 无 |
| POST /internal/v1/goals/{id}/steer | 为运行中的 Goal 排队纠偏 | content |
| POST /internal/v1/goals/{id}/evaluations | 记录指定 Run 的验收结果并推进 Goal | runID、status（Passed/Failed/Error）、可选 output |
| GET /internal/v1/goals/{id}/evaluations/{runID} | 查询指定 Run 的验收结果 | 无 |

`evaluator.kind` 支持 `none`、`command`、`http`；`none` 时 Run 完成即视为 Goal 完成。`command`/`http` 需要 Worker 侧配置沙箱化 Evaluator 后端；未配置时记录 `Error` 验收，Goal 保持 Active 且不计入 Stall。同一 `(goalID, runID)` 重复提交相同结果幂等返回，不同结果返回 409。`output` 超过 64 KiB 返回 400，服务端持久化前截断到 16 KiB。Run 一旦进入独立验收领取，首次回执必须通过共享存储的 `RecordClaimedEvaluation` 校验有效 claim；上面的 HTTP 记录接口不携带 claim，不能用来绕过验收 fencing。

当前 SandboxEvaluator 只执行 `command`；`http` 仍返回 Error。Worker 使用 `[Evaluator].BaseURL` 与 `TokenEnv` 装配 sandbox-center SDK，验收命令经 HTTP 发往绑定沙箱。Passed/Failed 回执持久化且 Goal 推进成功后释放；验收错误、读取失败或提交失败时停止本地续租并保留环境，到已接受的 HardExpiresAt 后由既有清理机制回收。

Worker 通过共享存储 `ClaimEvaluation`/`RenewEvaluation` 领取并续期 Completed Run 的验收，失租后可由其他 Worker 在原窗口内接管，`evaluation_epoch` 拒绝旧 claim 的续期、回执与新派发。`ResumeEvaluation` 只挂接原环境，不重新申请沙箱；已有 Passed/Failed 回执时先推进 Goal，再尝试挂接环境清理，沙箱不可用不会阻挡已保存结果的推进，也不重跑命令。Error 回执不可变，排除自动领取；过期窗口不能恢复。真实子进程强杀后的接管已通过本机 PostgreSQL 与 HTTP 沙箱替身验证，真实 Kubernetes Guard 的期限接续仍待验证。

Goal 的 Sandbox 工作区默认申请必要保存。验收 Failed 后，`QueueGoalIteration` 等待原环境退出 Preparing/Active，再检查当前 Run epoch、profile 对应唯一 Released 环境且检查点非空，将来源沙箱 ID 冻结到下一 Run 的 `Filesystem.restoreFromSandboxId`。保存期间保持 Goal Active；检查点缺失、来源不唯一、AIO 或 `/workspace` 外的目录均阻断续跑。Worker 不依据 Release 响应自行确认持久工作区保存，由中心完成回填和环境关闭。

Steering 只接受 Active Goal 的 Running Run，成功返回 204。`content` 去除首尾空白后不能为空，单条和待消费合并内容均不得超过 4096 字节，多条按事务顺序以换行合并。其他 owner 返回 404；Goal/Run 状态不允许返回 409。提交会重置 StallCount，但不增加预算。Worker 下一次模型调用前，在持租事务中同时清空 `pending_steer`、保存含纠偏内容的 checkpoint；后续轮次和接管执行继续携带这些指令。当前工具不会被中止。

StallMark 基于已完成 Step ID 的排序集合、配置版本、Run 输出哈希计算；每个已确定结果的终态 Run 只累计一次。同一 Goal 连续三个不同 Run 的指纹相同会 Blocked；新指纹从 1 开始，验收通过清零。未获得验收结论或验收基础设施错误不计 Stall，其用量暂缓到结论确定时归集。指纹只反映这些字段，不代表实际文件内容或外部业务状态完全相同。

### 外部回调

`POST /internal/v1/executions/{id}/callback` 接受 JSON `{"stepID":"approval","externalRef":"approval-42","result":"approved"}`。成功或相同结果重放返回 204；Step 或 externalRef 不匹配、重放结果变化返回 409。JSON 字段不会被 Query 参数覆盖，请求体上限为 1 MiB。

配置 `[Callback].SigningSecret`（示例配置从 `CALLBACK_SIGNING_SECRET` 读取）启用 HMAC-SHA256。`AllowedSources` 可配置为 `["approval-system"]`；配置白名单时必须同时配置签名密钥，且来源名不得为空或包含首尾空白。空配置保留原有行为，不提供回调来源认证。

发送端提供 `X-Agentic-Source` 和 `X-Agentic-Signature: sha256=<hex>`，签名内容按以下顺序拼接，换行均为单个 LF，正文后不额外添加换行：

```text
POST\n
<URL escaped path，不含 Query>\n
<去除首尾空白后的 X-Agentic-Source>\n
<原始 JSON 正文>
```

签名缺失、格式错误、正文/路径/来源被篡改返回 401；来源不在配置白名单返回 403。重试复用相同路径、正文与签名，由持久化幂等约束避免重复恢复；当前协议没有时间戳过期窗口。此签名校验不建立 owner 身份，仍依赖前文所述的身份 middleware。

### 沙箱工具绑定

AgentConfig 可设置 `toolsets:["filesystem"]`，并将平台结束工具设为 `endRun:true`。受信任的内部 ExecutionSpec 必须同时提供 `filesystem:{provider:"AIO",url:"http://coding/internal/v1/rounds/<round>/sandbox",tokenEnv:"CODING_TOOLS_TOKEN",workspace:"/workspace/repo"}`。绑定随执行配置冻结，幂等重试不能更换沙箱。普通会话提交不能使用需要可信绑定的文件工具集。凭据值不写入配置快照。

## 调用方职责

本 API 不提供评测实验、dataset、项目工作台、试运行产品入口或 Coding 发布审批。调用方负责这些业务及其持久化，只通过通用 Agent 和 Execution 接口发起执行。`spec.scope` 中的 tenantID、projectID、actorID、taskID、roundID 是执行上下文，不代表本 API 拥有项目成员或权限模型，也不作为授权证明。

本仓库不提供 eval/migrate CLI；数据库由部署方在接收业务请求前准备。已有业务表不随接口移除自动删除。
