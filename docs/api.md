# 任务接口

Go 请求与响应类型统一定义在 [sdk](../sdk/go/agenticv1)，调用方式见 [SDK 用法](../sdk/go/agenticv1/README.md)。SDK 不依赖 Gin；服务端负责校验。

用户业务接口已接入 forge-tenant-api v0.13.0 的 `MiddlewareAuthorization`，要求 `x-forge-tenant-id`，可选 `x-forge-project-id`；Scope 回调返回空 Scope、`RequireTenant=true`、Action 为空。本层不定义项目业务权限政策，也不维护静态 Bearer token 或 config owner 白名单。middleware 将已验证的 tenant/project 写入请求的 `access.Scope`，所有用户路由末端强制校验非空租户，缺失时拒绝请求。对象按 tenant 和已验证 owner 隔离；访问其他 tenant 或 owner 的对象返回 404。当前不支持跨 owner 共享 Agent。

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
| POST /v1/sessions/{id}/messages | 提交用户消息并创建任务 | query、可选 attachments；Idempotency-Key header 必填 |
| GET /v1/sessions/{id}/messages | 按序读取消息 | after（消息 seq）、limit |
| GET /v1/sessions/{id}/runs | 查询会话任务与状态 | after、limit |
| GET /v1/runs/{id} | 查询任务 | 无 |
| POST /v1/runs/{id}/cancel | 取消 | 无 |
| POST /v1/runs/{id}/answer | 回答澄清 | stepID、result |
| POST /v1/runs/{id}/resolve | 提交人工确认的工具结果 | stepID、result |
| POST /v1/runs/{id}/unblock | 接受未知消费并授权重新派发，返回 204 | epoch、reason |
| GET /v1/runs/{id}/events | SSE 回放及订阅 | `Last-Event-ID` header 或 `after` query，格式 `runID:seq`。`live` 默认 `true` |
| POST /v1/memories | 创建 OwnerPrivate、ActorPrivate 或 ActorProject 记忆，返回 201 | document；Idempotency-Key header 必填 |
| GET /v1/memories | 列出当前身份可访问的私人记忆 | after、limit |
| GET /v1/memories/{id} | 读取当前生效投影 | 无 |
| GET /v1/memories/{id}/versions | 分页列出已生效归档版本 | afterRevision、limit |
| GET /v1/memories/{id}/versions/{versionID} | 读取已生效版本正文 | 无 |
| PUT /v1/memories/{id} | 更新活跃记忆或恢复停用记忆 | expectedRevision、可选 restore、document；Idempotency-Key header 必填 |
| POST /v1/memories/{id}/retire | 停用记忆 | expectedRevision；Idempotency-Key header 必填 |

config 包括 systemPrompt、model、tools、mcpServers、toolsets、maxTurns、subAgents 和 selfDelegate，不接受 query/history/agentID/configVersion。模型配置与工具字段见 [创建请求样例](../examples/agents/agents.example.json)。模型地址必须是 HTTP(S)，maxTurns 为 1～100。静态工具名称不能重复或以 `mcp__` 开头，`delegate_subagent` 和 `read_step_result` 为系统保留名。parameters 必须为 object 类型 JSON Schema，policy 必须显式提供。工具可设置 `returnDirectly: true`，成功时 worker 直接返回工具结果并结束本轮，失败时将错误结果交给模型处理。此设置随 Agent 发布版本和 Run 快照冻结。静态工具 `kind` 仅接受空字符串或 `HTTP`，`AppForgeMCP`、`AppForgeOpenAPI` 及其他未知 kind 一律拒绝。`ask_for_clarification` 必须为 `ReadOnly`，kind 为空或 `HTTP`，URL 字段可不填。

`mcpServers` 是可选的标准 MCP Streamable HTTP 服务列表，最多 16 项。API 将服务配置随发布版本冻结到聊天和受信任应用的 Run 快照中。使用 MCP 不要求配置 `Filesystem`。

| 字段 | 约束与行为 |
| --- | --- |
| `name` | 服务名唯一，匹配 `^[a-z0-9][a-z0-9-]{0,15}$`。Worker 暴露的完整工具名为 `mcp__<server>__<tool>`。 |
| `url` | HTTP(S) 端点，不能包含用户信息或片段。 |
| `tokenEnv` | 可选的 Worker 凭据环境变量名，须匹配 `^[A-Za-z_][A-Za-z0-9_]{0,127}$` 且拒绝平台保留名（如 `DATABASE_URL`、`SANDBOX_CENTER_TOKEN` 等）。快照只保存变量名。 |
| `allowedTools` | 可选的远端原始工具名白名单，最多 64 项且不可重复。省略或为空时不按白名单筛选。 |
| `defaultPolicy` | 可设为 `ReadOnly`、`Idempotent` 或 `Unsafe`，省略或为空时按 `Unsafe` 处理。 |
| `toolPolicies` | 按远端原始工具名设置上述策略，优先于 `defaultPolicy`，不接受空策略。白名单非空时，每个键都必须在白名单中。 |
| `forwardScope` | 默认不向 MCP 服务发送 `X-Agentic-Scope`。显式设置为 `true` 时才转发。 |

白名单和策略映射中的原始工具名须匹配 `^[a-zA-Z0-9_-]{1,64}$`，拼接后的完整工具名最多 64 字符。工具重放策略来自本地配置，远端 `ToolAnnotations` 不参与决策。`Idempotent` 只用于自身天然幂等、可安全重复调用的工具。`model.apiKeyEnv` 与静态工具 `tokenEnv` 适用相同的环境变量名格式与平台保留名限制。

API 不访问 MCP 服务，提交期工具预算预检只计入静态工具。Worker 接入后在执行期发现 MCP 工具，将其纳入参数校验和 `durable_step` 保护，并按实际工具定义核算上下文预算。静态工具上限为 127 个（启用 `subAgents` 或 `selfDelegate` 时收紧至 126 个，为内置 `read_step_result` 与 `delegate_subagent` 预留槽位），静态工具与运行时装配的工具合计最多 128 个。升级说明：此前按旧上限已发布且恰好包含 128 个静态工具的历史 Agent 版本，其新 Run 会在 Worker 装配期因计入 `read_step_result` 后超出 128 上限而失败，需精简并重新发布。

`model.contextWindow`为可选非负整数。非零时必须同时配置正数`model.maxTokens`且小于窗口，输入预算为窗口减输出预留；`maxTokens`一旦提供就必须为正数。`model.stream` 为可选布尔值（省略时默认 `true`），随发布版本和 Run 快照冻结；设为 `false` 时 worker 直接使用非流式模型调用，兼容不支持流式或不支持 `stream_options.include_usage` 的 OpenAI 兼容网关。当前采用`cjk-weighted-v1`字符加权估算，并预留256 token协议开销，不是精确tokenizer计数。未配置窗口时冻结128 KiB输入字节上限。单条工具结果以有界首尾预览进入模型输入，完整结果仍持久保存。预算、预览上限和裁剪策略随Run冻结，发布新配置不改变已有Run。

提交消息及应用执行时，对已知必需材料预检；超限返回HTTP 400、错误码`ContextBudgetExceeded`，不创建Run、消息或事件，也不占用本次幂等键。worker调用模型前再核算动态工具schema与当前工具链；超限以Failed结束Run，错误详情包含`context budget exceeded`。裁剪顺序及限制见[上下文设计](refactoring-design.md#66-上下文管线与-memory-基础版)。

Agent 创建时 revision=1，编辑成功递增 revision。编辑或发布携带旧 revision 返回 409。同一 revision 重复发布返回同一个版本，版本 number 从 1 开始。仅已发布 Agent 可以创建会话。每次提交消息使用当时最新的发布版本，并把配置写入 Run 快照；恢复时不重新读取草稿或最新版本。Run 响应中的 agentID、configVersion 可用于定位版本。

已发布的 `AgentVersion.config` 来自 artifact-go 的不可变正文，本地只保存版本指针，响应形状不变。发布先完成远端写入，再复核本地 revision 并提交版本指针；远端成功而本地提交失败可能留下未引用版本，但不会占用公开版本号。远端依赖不可用时发布、版本读取和新任务提交返回 503；同一请求已接受后的幂等重放直接返回原 Run，不重新读取 artifact。

artifact 出站请求统一使用 API 自身的 `Tenant.ServiceAPIKey` 作为 Bearer。`RequestIdentity` 只读取 Forge middleware 已核验的 user/tenant/project 与 RequestID；适配器将 tenant/user/可选 project 写入 `x-impersonate-target-*`，并将同一用户写入 `X-Resource-User-ID`，保持个人归属。入站 Authorization、`forge_session` Cookie、CSRF 和 Origin 均不转发，也不混用 Cookie 与 Bearer；浏览器不得持有服务凭据。缺少有效 tenant 或 actor 时，适配器在请求远端前返回依赖不可用错误，不退化为共享范围。artifact 部署须启用服务传播，并将该 key 对应的 API 服务名列入允许名单；当前发布依赖的 Artifact SDK 包含 `PropagatedUserID`。真实 Forge/artifact 联调尚未完成；入站会话示例见 [README](../README.md#调用接口)。

提交消息返回 202 和 Run；同一 tenant、owner 的相同请求编号和相同输入重试返回 200 和原任务，不重复保存用户消息，也不重放其他租户的 Run。发布新版本不改变该行为。请求编号重复但输入不同、同一会话已有活动任务、无效状态转换返回 409。

列表返回 `{"items":[]}`。默认 limit=50；Agent、会话、任务列表最多 100 项，按 ID 升序，after 是上一页最后一项 ID。ID 排序不代表创建时间。消息列表最多 200 项，按会话内 seq 升序，after 默认 0，下一页使用上一页最后一项 seq。Agent、会话和消息时间以 UTC RFC3339 返回。

消息包含 sessionID、seq、runID、kind、role、content、可选 toolCalls/toolCallID 与 attachments/artifacts、createdAt。kind 包括 User、Assistant、Final、Tool、ClarificationAnswer、ReviewResult。助手工具决策保存为 Assistant，最终回复保存为 Final；澄清回答与人工确认属于工具结果，role 为 tool。通过 toolCallID 与助手 toolCalls 中的 ID 配对。失败和取消从 Run 状态及 Event 读取，不伪造成助手回复。

消息与对应任务状态在同一事务提交。客户端可以用会话消息列表展示历史，用 Run SSE 展示当前任务进度；Event 不作为聊天记录。Run 接受时冻结会话历史和上下文策略。已经压缩的消息仍可通过会话消息列表读取。

### 附件与文件索引

聊天提交在消息正文携带 `attachments`，应用执行在 `spec.attachments` 携带同样的数组。两种入口都要求非空 `query`。例如，提交一张远端图片：

```json
{
  "query": "描述这张图片",
  "attachments": [
    {"kind":"image","uri":"https://media.example/photo.jpg","name":"photo.jpg","mediaType":"image/jpeg"}
  ]
}
```

- `kind` 接受 `image`、`document`、`audio`、`video`、`file`。最多 10 个附件，单个 `sizeBytes` 可省略，非负且不超过 50 MiB，合计不超过 100 MiB。可选 `id` 最多 128 字节，非空 `name` 最多 255 字节，均不能含控制字符。
- `mediaType` 使用小写且无参数的 MIME 类型。图片、音频、视频分别要求 `image/`、`audio/`、`video/`。`uri` 最长 2048 字节，接受无凭据和片段的 HTTP(S) 或 `artifact://` URI，以及无路径穿越的干净绝对路径；其中 `image` 和 `video` 的外部 URL 仅允许公网 `https://`（拒绝 `http://`、单段主机名、`localhost`、`*.local`、`*.internal`、`*.svc`、`*.home.arpa` 及非公网/保留/转换前缀 IP 与非标准数字 IP，防止经模型服务拉取触发 SSRF；该校验不做 DNS 解析，依赖 DNS 指向内网的域名仍需出口网络策略兜底）。API 接收引用和元数据，不接收内嵌文件字节。凡 `uri` 使用 `artifact://` 必须同时提供 `artifactURI`；`artifactURI` 不可与外部 HTTP(S) 或工作区路径 `uri` 混用。
- 通过 `artifactURI` 提交 Artifact 文件引用时，API 在首次提交和幂等重放前核验 Forge 的用户、tenant、project 及项目 `read` 权限。聊天要求会话属于当前 project，应用要求 `spec.scope.actorID` 等于当前用户。Artifact 按同一身份和个人资源归属解析。返回的 `agent` 和 `agent-memory` 类型一律拒绝，解析须返回 ready 版本及文件清单项。
- 新请求解析后将 `artifactURI` 与 `uri` 冻结为固定版本的文件 URI。此时可省略 `uri`、`name`、`mediaType`，分别从文件引用、路径末段（缺省回退文件 ID）和清单（忽略 MIME 参数规范化为小写 `type/subtype`）补全。附件按原始顺序参与幂等比较。授权通过的同键重放直接返回已接受的 Run，即使 Artifact 别名已经移动或服务暂时不可用。
- `model.inputModalities` 随 Agent 发布版本和 Run 冻结，允许 `text`、`image`、`video`，每项至多出现一次。省略或传空数组时支持文本和图片。上下文构建器只在当前首问保留模型支持的公网 `https://` 图片或视频直传项。其他附件以及历史附件使用 `[Attachments]` 元数据。直传的 URL 计入输入字节，图片和视频分别额外计 1024 和 2048 token。文件大小不计入模型输入字节。
- 用户消息保存 `attachments`。工具写入或编辑文件时可随工具结果提交 `artifacts`，每项有 `path`、`action`（`written` 或 `edited`），可选 `name`、`mediaType`、`sizeBytes` 和 `provider`（`AIO` 或 `Sandbox`）。工具步骤、结果消息和 checkpoint 在同一事务提交。
- 上下文的 `files` 固定块合并当前轮和本会话已完成 Run 的文件记录，以来源和 URI 去重，新记录优先。索引不受会话历史压缩游标影响，最多展示 50 条和 4 KiB（按完整行截断），并受本轮上下文预算约束且不会挤掉首问。`[attachment]` 表示附件引用。`[workspace/accessible]` 表示当前 Run 使用同一 AIO 工作区或已确认的沙箱恢复链。`[workspace/historical]` 表示可查到路径记录，但当前工作区未确认可读。索引本身不授予读取权限。子 Run 默认不继承父会话附件与文件索引，需要交接的文件由父 Agent 在子任务描述中显式传递。

任务状态为 Queued、Running、WaitingClarification、NeedsReview、WaitingExternal、Blocked、Completed、Failed、Canceled。等待状态时，GET /v1/runs/{id} 与 Run 列表项从 `durable_run` 显式列返回 `waitingStep` 与 `waitingKind`（`Clarification` 或 `Review`），不再从 worker 私有 checkpoint JSON 解析。`POST /v1/runs/{id}/answer` 在 `WaitingClarification` 且 `waitingKind=Clarification` 时提交澄清；`POST /v1/runs/{id}/resolve` 在 `NeedsReview` 且 `waitingKind=Review` 时提交人工确认的工具结果。两处请求体仍为 `stepID` 与 `result`，`stepID` 必须等于当前 `waitingStep`；`result` 是提供给模型的用户回答或已确认工具结果。相关 SSE 事件 payload 在保留 `waitingStep` 的同时新增 `waitingKind`。

`POST /v1/runs/{id}/unblock` 只处理带有合法模型或摘要派发标记的 Blocked Run。请求示例为 `{"epoch":1,"reason":"已核查未知消费，接受重复计费风险并授权重新派发"}`。epoch 必须等于查询到的阻断版本，reason 去除首尾空白后非空且不超过 4096 字节。成功时清除派发标记、epoch 加一并重新排队；保留 checkpoint、已完成步骤、已记录 token 和费用，不补写未知 usage。此操作表示人工接受未知消费并授权新请求，不能证明旧请求未发送，可能重复计费。原调用账单核对和补账需另行处理。

恢复仍要求 Run 未过期、会话无活动 Run 且可取得会话锁；有关联 Goal 时还要求 Goal 为 Active 或 Blocked、预算未耗尽、总期限未过且 StallCount 小于 3。根 Run 须仍为 Goal.ActiveRunID。只有恢复当前根 Run 且同 Goal 无其他 Blocked Run 时，Blocked Goal 才恢复 Active；恢复 child 不会直接恢复 Goal。非法状态或旧 epoch、旧状态格式、损坏标记、PricingFailure 或已有 PendingModel 均返回 409。重复提交返回 409；响应丢失时先查询 Run 及事件，不自动更换 epoch 重试。鉴权沿用 tenant 与 owner 隔离，其他 owner 或 tenant 返回 404。

`RunUnblocked` 与重新排队同事务提交，记录 actorID、reason、blockedEpoch、新 epoch、原 inflightModel、previousError、goalID 和 goalResumed；审计失败时恢复全部回滚。

如果只恢复 child，父 Run 在 WaitingExternal 等待，child 完成后会按原子任务链路唤醒父 Run，但 Goal 仍保持 Blocked。完成相关人工核查后，还需调用 `POST /internal/v1/goals/{id}/resume`，才能继续 Goal 验收、收敛或自动续轮。父子均 Blocked 时，先恢复 child、再恢复当前根 Run，可由根 Run 的 unblock 自动恢复 Goal；顺序相反时仍需显式 ResumeGoal。调用方应查询 Goal 或审计事件的 goalResumed 确认结果。

`GET /v1/runs/{id}/events` 按 SSE `event:` 字段区分两种消息。

- PostgreSQL 权威事件带 `id: runID:seq`，`data` 为 `Event`，包括 `runID`、`seq`、`kind`、`payload` 和 `createdAt`。客户端按 `id` 去重和续订。`Last-Event-ID` 优先于 `after`，重新订阅不会启动新任务。Run 进入终态或 `Blocked` 后，API 发完权威事件就关闭连接。`Blocked` 恢复后可用原游标重新订阅。
- `ModelReasoningDelta` 和 `ModelContentDelta` 分别是模型生成时的思考和正文增量。帧没有 `id:` 行，`data` 为 `StreamDelta`，包括 `runID`、`kind`、`epoch`、`turn`、`offset`、`delta` 和 `createdAt`。`offset` 为该 `(epoch, turn, kind)` 内的 UTF-8 字节偏移。连接先回放权威事件，再补发 Redis 中仍保留的、尚未记账轮次的增量。客户端可按 `(runID, epoch, turn, kind, offset)` 去重，并通过比较 `offset` 是否衔接上一条结束位置发现丢弃缺口。传 `?live=false` 可只订阅权威事件。

权威事件包括 `RunQueued`、`RunStarted`、`ModelStarted`、`UsageRecorded`、`ModelCompleted`、`ModelOverflowRejected`、`RunCompacted`、`CompactionFailed`、`ToolStarted`、`ToolCompleted`、`ClarificationRequested`、`RunNeedsReview`、`ExternalWaitRequested`、`ChildRunSpawned`、`ChildRunCompleted`、`ChildOrphaned`、`RunResumed`、`RunRetry`、`RunBlocked`、`RunUnblocked`、`GoalBlocked`、`RunCompleted`、`RunFailed` 和 `RunCanceled`。`ModelStarted` 的 `payload.epoch` 由租约决定，调用方提供轮次时还有 `payload.turn`。`RunRetry` 的 `payload.status` 可能为 `Failed`。

以 `ModelCompleted.payload.output` 校准该轮生成的正文，以 `RunCompleted.payload.output` 为最终答案。思考仅在瞬态流中传输；Worker 发布队列满时中间增量可能被丢弃，且轮次结束前的 Flush 有时间上限，尾部增量也可能截断。Redis 瞬态流在每次写入后续期一小时，按约 10,000 条裁剪。建连补发按批次逆向扫描（`XREVRANGE`），遇旧 epoch 或已记账轮次立即截断。Redis 单节点和哨兵使用一次阻塞读取监听两个流。Redis Cluster 返回 `CROSSSLOT` 时分别读取两条流。Redis 不可用时，客户端仍能通过 PostgreSQL 轮询收到权威事件。

## 子 Agent 与子 Run

- `subAgents` 可配置最多 10 个专职子 Agent。每项包含 `name`、`description`、`agentID` 及可选 `version`。别名在同一 Agent 内唯一，匹配 `^[a-z0-9][a-z0-9_-]{0,31}$`，`self` 保留。描述去除首尾空白后须非空，原值最多 1024 字节。`agentID` 须非空、无首尾空白且最多 128 字节。`version` 为正数时指定发布版本，省略或为 0 时在提交 Run 时解析当时的最新发布版本。
- 编辑和发布拒绝直接自引用。聊天和应用提交按父 Agent 的 tenant 与 owner 查找每个引用，缺少 Agent 或指定版本返回 404，自动解析时尚未发布返回 409，历史版本配置不合法返回 400。API 将每个子 Agent 的版本、系统提示词、模型、静态工具、MCP、工具集、轮次上限和模型单价冻结在父 Run 快照中。已接受请求按原幂等键返回原快照。
- `selfDelegate` 默认为 `false`。开启后，根 Run 可按本轮冻结配置派生动态子任务，委派指令进入子任务的用户输入，父系统提示词原样继承。指定专职子 Agent 时使用该子 Agent 的冻结配置。两种子 Run 均有各自的会话和上下文，深度最多一层。
- 子 Run 排队与父 Run 进入 `WaitingExternal` 同事务提交。父 Run 的 `ChildRunSpawned` 事件提供 `stepID`、`childID`、`subAgent`、`agentID`。子 Run 到达终态时，匹配的等待步骤接收结果并重新排队。`child:` 引用由持久层保留，外部结果回调无法完成该步骤。子 Run 的查询、取消和 SSE 使用现有 `/v1/runs/{id}` 路由及子 Run ID，沿用 tenant 与 owner 鉴权。其他租户或 owner 访问返回 404。父 Run 终止会取消仍处于活动状态的子 Run。
- 父或专职子 Agent 使用文件工具集时，须从受信任应用执行入口提交合法 `filesystem`，普通聊天提交返回 400。双方均无文件工具集时，应用提交也拒绝 `filesystem`。专职子 Run 仅在自身使用文件工具集时接收绑定，动态子 Run 继承父绑定。`AIO` 使用绑定中相同的端点和工作目录，文件可见性取决于端点提供的工作区。`Sandbox` 按 Run 获取独立租约和工作区，子 Run 会清空父 `restoreFromSandboxID` 始终以干净工作区启动，委派本身不共享父沙箱的实时文件。
- 当子 Run 绑定了 Goal 并因未知模型消费等原因进入 `Blocked` 时，关联 Goal 会同步进入 `Blocked`，父 Run 保持 `WaitingExternal`。恢复需两步人工操作：先调用 `POST /v1/runs/{childID}/unblock` 解除子 Run 阻断，待无其他阻断 Run 后再显式恢复 Goal（`POST /internal/v1/goals/{id}/resume`）以继续后续评测与迭代。

## Memory

HTTP 维护入口开放 OwnerPrivate、租户级 ActorPrivate 和项目内的 ActorProject。服务从 Forge 验证结果确定租户、项目和 owner，写入文档的 `scope.tenant`、`scope.project` 必须与验证结果一致。ActorPrivate 要求请求处于租户级且 `scope.project` 为空。`scope.actor` 可省略或填写当前用户，服务保存核验后的用户 ID。`outputVisibility` 省略时默认 `Private`，ActorPrivate 只接受 `Private`。

ActorProject 要求请求处于项目级，`scope.actor` 可省略或填写当前用户，`outputVisibility` 可为默认的 `Private` 或显式的 `Project`。项目请求仅访问同一租户、同一项目下本人的 ActorProject 和 OwnerPrivate 条目。读取条目和历史检查 Forge 的项目 `read` 权限，缺少该权限时列表返回 403，详情和历史返回 404。创建、更新、恢复、停用及其同键重放检查 `write` 权限，缺少该权限返回 403；权限检查失败返回 503 `MemoryUnavailable`。后台恢复也检查当前写权限，失去写权限的 Pending 操作等授权恢复后才能继续。

`memory=Recall` 的输出受众由入口确定。聊天使用同一租户、项目和用户下的 OwnerPrivate，受众为 `Private`。租户级应用执行使用 ActorPrivate，受众为 `Private`。项目应用执行按 `Project` 受众处理，只召回同一租户、项目和用户下标记为 `Project` 的 ActorProject。应用执行的 `spec.scope.actorID` 必须等于 Forge 核验的当前用户。项目聊天和应用执行均在首次提交和重放前检查项目 `read` 权限。缺少权限返回 403，权限检查失败返回 503 `MemoryUnavailable`。成功接受的 Run 继续使用当时选中的冻结投影。

Create 返回 201，其余 Memory 维护操作成功返回 200。Entry 的 `revision` 表示当前投影修订，启用归档时还返回固定的 `activeVersionRef`。

`POST /v1/memories` 的 body 必须包含完整 `document`。`PUT /v1/memories/{id}` 的 body 必须包含 `expectedRevision` 和完整 `document`；`restore` 为 `false` 或省略时执行 Update，只允许修改活跃条目；`restore` 为 `true` 时执行 Restore，只允许恢复已停用条目。两种操作都要求 `expectedRevision` 等于操作开始时的当前 revision，成功后 revision 递增。Scope、agentID、kind、subjectKey 等身份字段不能通过更新改变。对活跃条目请求 Restore、对已停用条目执行普通 Update、revision 不匹配或同键不同参数均返回 409。`POST /v1/memories/{id}/retire` 只接受 `expectedRevision`。

三个写接口都必须通过 `Idempotency-Key` 请求 Header 传幂等键，长度不超过 128；该字段不属于 JSON body，缺失或放进 body 都返回 400 `InvalidParameter`。同一 owner 下相同键、相同操作意图和相同 actor 的重试返回第一次成功生效的结果，即使条目后来又被修改或停用，也不会重做发布或覆盖新状态。相同键对应不同操作意图或 actor 返回 409。客户端应持久保存并复用原键，不应在超时或 503 后自行换键重试。

Memory 写操作除通用的 400、401、403、404、409 外，还会返回以下稳定 503 错误码。`MemoryOperationPending` 表示操作已受理但远端归档或本地生效尚未完成，使用原键重试；`MemoryUnavailable` 表示归档或其他依赖暂不可用，修复依赖后继续原键；`MemoryOperationOutcomeUnknown` 表示本地生效提交结果不明，必须保留原幂等键、原 collection 和原版本身份，用原键继续处理，不能换键或重建集合，也不能当成确定的失败结果；`MemoryVersionUnavailable` 表示此前发布已发生但对应 artifact 版本正文不可用，可能来自源版本 410。遇到 `MemoryVersionUnavailable` 必须保留原幂等键、原 collection 和原版本身份，不能换键或重建集合并自动生效，应保留引用并交由受控修复。

版本历史列表按 `appliedRevision` 降序返回 `items`，每项包含 `versionRef`、`appliedRevision`、`actor`、`appliedAt` 和 `active`。`limit` 默认 50，范围 1–100；使用上一页的 `nextAfterRevision` 请求下一页，字段缺省表示没有下一页。列表另返回当前 `activeVersionRef` 和可选 `retiredAt`；停用后所有历史项的 `active` 为 false。列表只查本地已生效的 Create、Update、Restore 回执，不依赖 artifact；纯本地模式没有归档引用，列表为空。详情先验证条目归属和成功生效回执，再按固定引用读取 `document`；未采用的版本返回 404，正文缺失返回 503 `MemoryVersionUnavailable`，依赖故障返回 503 `MemoryUnavailable`。

## 进程探针

server-go HTTPHost 提供无需鉴权的 `GET /ping`（200，正文 `pong`）、`GET /healthz`（就绪时 200，正文 `healthz`；未就绪时 503）和 `GET /version`。探针只反映进程与服务生命周期，不持续检查外部依赖。本仓库不提供 Forge 项目工作台或企业资产业务入口。

## 沙箱控制面（阶段 2，默认关闭）

`Sandbox.AllowedServices` 非空并装配服务认证后才注册以下路由，空白名单不注册。`[Sandbox].K8sContext` 控制真实调度；未配置时申请只写入 `Provisioning` 元数据，exec/release 返回 409 `NotReady`。当前边界与验证范围见[阶段 2 验证](sandbox-center/phase2-verification.md)。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /v1/sandboxes | 申请沙箱，返回 sandbox 与首个 lease | tenantId、ownerId、requestKey、profileId、workerId、runId、runEpoch、可选 ttlSeconds、requiredSave、restoreFromSandboxId |
| GET /v1/sandboxes/{id} | 查询沙箱与当前 lease | 无 |
| POST /v1/sandboxes/{id}/renew | 续租 | revision、requestKey、可选 ttlSeconds |
| POST /v1/sandboxes/{id}/operations | 执行命令并写回执 | requestKey、script；验收派发另需 evaluationWorkerId、evaluationEpoch |
| GET /v1/sandboxes/{id}/operations/{opID} | 重查操作回执 | 无 |
| DELETE /v1/sandboxes/{id} | 关闭准入；必要保存未完成时停止实例并保留工作区 | 无 |

申请按 `(ownerId, requestKey)` 幂等；相同请求返回原 sandbox。续租按 lease revision 单调递增，相同 revision 与 requestKey 重放返回原事实，旧 revision 返回 409。执行按 `(leaseId, requestKey)` 幂等：控制面先持久化 intent 和 dispatch 回执再派发，派发结果不可知时操作保留 `Unknown` 并返回 502 `OutcomeUnknown`，客户端应通过回执查询确认，不能直接重试为第二次执行。释放是幂等的；已销毁的 sandbox 直接返回。

`requiredSave` 默认 `false`，申请后不可变，属于幂等摘要。设为 `true` 时，Kubernetes Provider 为工作区申请独立的 1 GiB PVC，记录 `workspaceUid`。默认使用集群默认 StorageClass 和 `ReadWriteOnce`。部署方可通过 `[Sandbox].WorkspaceStorageClass` 指定存储类，通过 `WorkspaceAccessMode` 请求 `ReadWriteMany`。释放或到期先关闭租约并进入 `SaveBlocked`，再停止原 Pod UID 的 Guard。确认容器已终止后记录 `stoppedAt`，保留 Pod、PVC 与未关闭的环境记录。DELETE 成功可以返回 `SaveBlocked`。`Destroyed` 表示物理回收已完成。停止结果不明确时返回错误，阻塞状态仍保存在数据库，由对账继续处理。

配置 `[Sandbox].CheckpointImage` 并在 `CheckpointStorePVC` 与绝对路径 `CheckpointStoreHostPath` 中选择一项。PVC 模式下，中心先登记保存尝试。Saver 打包 `/workspace` 并写入提交标记。独立的只读 Verifier 按保存尝试读取存储，校验标记、沙箱身份与检查点内容。校验通过后，中心写入 `savedCheckpoint`，再回收 Pod/PVC 并将检查点回填到 Run 环境记录。保存或校验失败时继续保持 `SaveBlocked` 并保留工作区 PVC。期限与普通释放不能解除阻塞。保存确认由中心负责，支持范围及验证环境见[保存与恢复验证](sandbox-center/layerfs-save-verification.md)。

`restoreFromSandboxId` 指定已保存的来源沙箱，并要求 `requiredSave=true`。中心验证来源已停止且为 `Destroyed`、`savedCheckpoint` 非空、tenant/owner/profile 及 profile revision 一致，并校验来源 ID 匹配当前 Run 冻结的 Filesystem 配置，不能省略或替换。客户端不能提供任意检查点路径；中心解析并冻结 `resumeCheckpoint`。恢复沿用保存的镜像和存储配置，成功后新沙箱才允许激活。响应同时返回 `restoreFromSandboxId` 和 `resumeCheckpoint`，Worker 验证绑定后才激活环境。单节点真实 Kubernetes 验证见[跨 Run 工作区恢复](lessons/long-task-boundaries.md#跨-run-续跑必须等待工作区保存确认)。

恢复使用 Pod init container，工作区通过校验和原子目录发布后 Guard 才能启动。恢复失败不会发放 lease；未激活实例经释放或超时对账清理为 `Lost`，不保存半成品，来源检查点仍然保留。调用方重新申请使用新 requestKey。配置未启用检查点能力时，运行时恢复请求返回 409 `NotReady`。

Completed Run 的原沙箱可为 command 验收继续续租。完成事务为满足条件的活跃 Goal 与环境保存 `evaluation_worker_id` 和 `evaluation_deadline`，窗口最长为数据库时间起算 10 分钟，且不超过 Run deadline。原沙箱租约继续匹配该 Worker；Goal 仍为 Active 且指向该 Run，环境、沙箱、租约的 epoch/generation/owner 绑定一致，尚无 Evaluation 回执，当前沙箱租约和验收窗口均未过期。续租期限取请求 TTL、验收窗口、Run deadline 与已配置 Goal 总时长预算的最早值，运行时还可进一步缩短期限。过期或已关闭租约不能复活。

当前验收执行者由独立的 `evaluation_claim_worker_id`、`evaluation_epoch`、`evaluation_lease_until` 表示，可与原沙箱 Worker 不同。新派发只允许固定的 `eval-<sandboxID>` 请求键与 Goal 配置的验收命令，另须提交匹配有效 claim 的 `evaluationWorkerId`、`evaluationEpoch`，派发前再次检查。普通命令仍要求有效 Running claim。操作创建后，响应通过 `X-Sandbox-Operation-ID` 返回操作 ID，包含后续失败响应；SDK 保留该 ID 供查询。网络响应丢失且没有 ID 时可同参重放原请求以获取去重回执，不得换请求键重新执行。

执行输出保存 guard/execd 的原始事件流，未解析为结构化结果。沙箱路由使用 Forge 服务认证和 `Sandbox.AllowedServices` 白名单；申请的 `tenantId`/`ownerId` 仍由可信调用方在请求体提供，不从 Forge 用户 scope 推导。

## Gin 请求与错误契约

对象接口返回 JSON，列表返回 items；取消与回答成功返回 204。错误统一返回：

```json
{"code":"InvalidParameter","message":"invalid request body","requestId":"request-id","statusCode":100400}
```

错误码包括 Unauthorized、Forbidden、InvalidParameter、PayloadTooLarge、InvalidCursor、APINotFound、NotFound、MethodNotAllowed、Conflict、InternalError。请求体超过 1 MiB 返回 413，未知字段或连续多个 JSON 文档返回 400；URI、Query、Header 与 JSON 在同一请求对象中完成绑定和校验，参数错误统一返回 InvalidParameter（包含消息 after 为负数）；SSE 的复合事件游标格式错误仍返回 InvalidCursor。绑定错误返回框架校验信息，未分类的内部错误不向客户端暴露数据库详情。`statusCode` 是框架错误编号，实际 HTTP 状态以响应状态行为准；`requestId` 用于关联日志。

请求日志只记录方法、路由模板、状态与耗时，不记录 token 或请求体。Recovery 在响应未提交时返回 JSON 500；SSE 已开始后只结束处理，不向事件流追加 JSON 错误。这些行为由 server-go HTTPHost 统一管理；身份鉴权由已接入的 Forge middleware 负责，调用方应用负责项目业务授权。SSE 不设整段响应的写入超时，调用方取消与进程退出会结束订阅。SSE 响应头、事件编码、心跳和逐次写入期限由 server-go stream 管理，底层使用 gin-contrib/sse 编码，字段冒号后是否有空格不影响标准 SSE 消费端。

## 受信任应用执行

内部 execution、goal、lease 路由仅在 `Tenant.AllowedPropagationServices` 非空时注册，使用 `MiddlewareInternalPropagation` 验证服务 Authorization 和服务白名单，并接收 `x-impersonate-target-tenant-id`、`x-impersonate-target-user-id`、可选 `x-impersonate-target-project-id`。已验证的 tenant/project 写入请求 context，路由末端同样拒绝空租户。`spec.scope` 的 tenant/project 必须匹配验证 scope，owner 取自验证身份；请求体不是授权证明。可信 worker 直连数据库、直接调用 Store 时仍保留无 HTTP scope 的内部契约。

`POST /internal/v1/executions` 请求为：

```json
{"spec":{"agentID":"已发布 Agent ID","agentVersion":1,"scope":{"tenantID":"t","projectID":"p","actorID":"u","taskID":"task","roundID":"round"},"query":"本轮要求"},"timeoutSeconds":1800}
```

Agent 定义先通过 `/v1/agents` 创建、编辑与发布。应用执行必须引用明确的发布版本，不能传入 systemPrompt、model、tools 或 maxTurns。运行时读取该版本并冻结完整执行快照；不创建临时 Agent。旧的内联定义请求返回 400，不保留第二条定义路径。

同一 owner、tenant、project、task 复用同一个 Session；不同项目的同名 task 不共享历史。后续轮次允许选择同一 Agent 的其他已发布版本，但不能在同一任务中更换 Agent 身份。同一会话已有活动 Run 时返回 409。上下文策略与普通聊天一致。

幂等键必填。同一 tenant、owner 内同键同输入返回原 Run，包括发布新版本之后的重试；同键不同输入返回 409。不同租户的相同 owner 和请求键不会重放另一租户的 Run。Agent、发布版本不存在或属于其他 tenant/owner 时返回 404。创建会话、Run、输入消息与事件在同一事务提交。应用服务凭据不能下发给浏览器；应用从已验证的用户与任务构造 scope，敏感操作仍动态检查业务权限。

`GET /internal/v1/executions/{id}/lease?epoch=N` 返回当前数据库时钟下的执行权；取消、旧 epoch、超过 deadline 或其他 owner 均无法通过。模型工具参数不能提供该执行身份，Worker 在 HTTP headers 中注入真实 run ID 和 epoch。运行时执行权不替代业务授权或外部进程停止确认。projectID 是可选的执行上下文；工具或文件系统绑定本身不要求选择项目，资源是否允许使用由调用方授权。

## Public run trace

`GET /v1/runs/:id/trace?after=0` (RunAPI.Trace) returns an owner-scoped `RunTrace`: the current public Run, up to 50 events in sequence order, `latestEvent`, `nextSeq`, and `hasMore`. Repeat with `after=nextSeq` while `hasMore`, then poll for new events. Each event includes `createdAt`. The snapshot uses a repeatable-read transaction; refreshing or reconnecting replays persisted events. `ModelStarted` is committed before each model call and includes a `context` field with observable facts about input construction: `inputTokens`, `inputBytes`, `tokenLimit`, `byteLimit`, `outputReserve`, `omittedSources`, `collapsedSources`, `projectedToolResults`, and optional `ladderPreviewed`, `ladderStubbed`, `droppedRounds`, `usedUsageAnchor`, `reactiveAttempt`, and `summaryBreakerOpen`. `ModelOverflowRejected` reports the recovery attempt and detail. This helps diagnose budget trimming, source omission, and current-turn tool result projection without parsing executor checkpoint state. Tool events include their public name; ToolStarted includes public arguments recovered from assistant messages, including historical runs. ToolCompleted includes the saved result. `arguments`, `result`, `output`, and `detail` previews are capped at 32 KiB UTF-8 and marked with `<field>Truncated: true`; full originals remain in storage. No checkpoint, hidden reasoning, system prompt, model/tool credentials or lease identity is returned. This endpoint is read-only and does not resume tasks. Invalid negative/non-numeric cursors return 400; another owner’s run returns 404. Responses use Cache-Control: no-store.

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

Goal 的根 Run 自身使用文件工具集时，Sandbox 工作区默认申请必要保存。验收 Failed 后，`QueueGoalIteration` 等待原环境退出 Preparing/Active，再检查当前 Run epoch、profile 对应唯一 Released 环境且检查点非空，将来源沙箱 ID 冻结到下一 Run 的 `Filesystem.restoreFromSandboxId`。保存期间保持 Goal Active；检查点缺失、来源不唯一、AIO 或 `/workspace` 外的目录均阻断续跑。仅子 Agent 使用文件工具集时，下一轮沿用冻结绑定，跳过根 Run 的工作区恢复检查。Worker 不依据 Release 响应自行确认持久工作区保存，由中心完成回填和环境关闭。

Steering 只接受 Active Goal 的 Running Run，成功返回 204。`content` 去除首尾空白后不能为空，单条和待消费合并内容均不得超过 4096 字节，多条按事务顺序以换行合并。其他 owner 返回 404；Goal/Run 状态不允许返回 409。提交会重置 StallCount，但不增加预算。Worker 下一次模型调用前，在持租事务中同时清空 `pending_steer`、保存含纠偏内容的 checkpoint；后续轮次和接管执行继续携带这些指令。当前工具不会被中止。

StallMark 基于已完成 Step ID 的排序集合、配置版本、Run 输出哈希计算；每个已确定结果的终态 Run 只累计一次。同一 Goal 连续三个不同 Run 的指纹相同会 Blocked；新指纹从 1 开始，验收通过清零。未获得验收结论或验收基础设施错误不计 Stall，其用量暂缓到结论确定时归集。指纹只反映这些字段，不代表实际文件内容或外部业务状态完全相同。

### 外部回调

`POST /internal/v1/executions/{id}/callback` 接受 JSON `{"stepID":"approval","externalRef":"approval-42","result":"approved"}`。成功或相同结果重放返回 204；Step 或 externalRef 不匹配、重放结果变化返回 409。JSON 字段不会被 Query 参数覆盖，请求体上限为 1 MiB。

callback 默认不注册。设置 `[Callback].Enabled=true` 时，必须同时配置 `SigningSecret`（示例配置从 `CALLBACK_SIGNING_SECRET` 读取）、`AllowedSources` 和 `AllowedServices`，否则启动校验失败。路由使用独立的 Forge 服务认证及服务白名单，并在 handler 中校验来源白名单和 HMAC-SHA256；三项检查同时生效。`AllowedSources` 可配置为 `["approval-system"]`，来源名不得为空或包含首尾空白。

发送端提供 `X-Agentic-Source` 和 `X-Agentic-Signature: sha256=<hex>`，签名内容按以下顺序拼接，换行均为单个 LF，正文后不额外添加换行：

```text
POST\n
<URL escaped path，不含 Query>\n
<去除首尾空白后的 X-Agentic-Source>\n
<原始 JSON 正文>
```

签名缺失、格式错误、正文/路径/来源被篡改返回 401；来源不在配置白名单返回 403。重试复用相同路径、正文与签名，由持久化幂等约束避免重复恢复；当前协议没有时间戳过期窗口。HMAC 不建立用户 owner 身份，callback 的调用服务身份由独立服务认证建立，不使用 execution/goal/lease 的 propagation 鉴权链。

### 沙箱工具绑定

AgentConfig 可设置 `toolsets:["filesystem"]`，并将平台结束工具设为 `endRun:true`。受信任的内部 ExecutionSpec 必须同时提供 `filesystem:{provider:"AIO",url:"http://coding/internal/v1/rounds/<round>/sandbox",tokenEnv:"CODING_TOOLS_TOKEN",workspace:"/workspace/repo"}`。绑定随执行配置冻结，幂等重试不能更换沙箱。普通会话提交不能使用需要可信绑定的文件工具集。凭据值不写入配置快照。

## 调用方职责

本 API 不提供评测实验、dataset、项目工作台、试运行产品入口或 Coding 发布审批。调用方负责这些业务及其持久化，只通过通用 Agent 和 Execution 接口发起执行。`spec.scope` 中的 tenantID、projectID、actorID、taskID、roundID 是执行上下文，不代表本 API 拥有项目成员或权限模型，也不作为授权证明。

本仓库不提供 eval/migrate CLI；数据库由部署方在接收业务请求前准备。已有业务表不随接口移除自动删除。
