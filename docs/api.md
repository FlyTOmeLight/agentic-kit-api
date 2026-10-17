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

config 包括 systemPrompt、model、tools、maxTurns，不接受 query/history/agentID/configVersion。模型配置与工具字段见 [创建请求样例](../examples/agents/agents.example.json)。模型地址必须是 HTTP(S)；maxTurns 为 1～100。工具名称不能重复，parameters 必须为 object 类型 JSON Schema，policy 必须显式提供。工具可设置 `returnDirectly: true`；该工具成功时，Eino 直接返回工具结果并结束本轮，不再调用模型。此设置随 Agent 发布版本和 Run 快照冻结。AppForge 尚无已验证的下游幂等契约，不能配置为 Idempotent。

Agent 创建时 revision=1，编辑成功递增 revision。编辑或发布携带旧 revision 返回 409。同一 revision 重复发布返回同一个版本，版本 number 从 1 开始。仅已发布 Agent 可以创建会话。每次提交消息使用当时最新的发布版本，并把配置写入 Run 快照；恢复时不重新读取草稿或最新版本。Run 响应中的 agentID、configVersion 可用于定位版本。

提交消息返回 202 和 Run；同一 owner 的相同请求编号、相同 session/query 重试返回 200 和原任务，不重复保存用户消息。发布新版本不改变该行为。请求编号重复但输入不同、同一会话已有活动任务、无效状态转换返回 409。

列表返回 `{"items":[]}`。默认 limit=50；Agent、会话、任务列表最多 100 项，按 ID 升序，after 是上一页最后一项 ID。ID 排序不代表创建时间。消息列表最多 200 项，按会话内 seq 升序，after 默认 0，下一页使用上一页最后一项 seq。Agent、会话和消息时间以 UTC RFC3339 返回。

消息包含 sessionID、seq、runID、kind、role、content、可选 toolCalls/toolCallID、createdAt。kind 包括 User、Assistant、Final、Tool、ClarificationAnswer、ReviewResult。助手工具决策保存为 Assistant，最终回复保存为 Final；澄清回答与人工确认属于工具结果，role 为 tool。通过 toolCallID 与助手 toolCalls 中的 ID 配对。失败和取消从 Run 状态及 Event 读取，不伪造成助手回复。

消息与对应任务状态在同一事务提交。客户端可以用会话消息列表展示历史，用 Run SSE 展示当前任务进度；Event 不作为聊天记录。当前模型上下文取最近 20 个已完成 Run 的 User/Final 消息，工具历史保存在消息表供查询，尚未实现跨轮工具历史与上下文压缩。

任务状态为 Queued、Running、WaitingClarification、NeedsReview、Completed、Failed、Canceled。等待状态查询结果包含 waitingStep。answer/resolve 使用这个值作为 stepID；result 是提供给模型的用户回答或已确认工具结果。

SSE 每条消息的 id 为 runID:seq，data 包含 runID、seq、kind 和 payload。客户端按该 id 去重。只在终态且已发送全部提交事件后关闭连接；WaitingClarification 和 NeedsReview 不是终态。重新连接不会启动新的任务。

重要事件包括 RunQueued、RunStarted、ModelCompleted、ToolStarted、ToolCompleted、ClarificationRequested、RunNeedsReview、RunResumed、RunRetry、RunCompleted、RunFailed、RunCanceled。RunRetry 的 payload.status 可能为 Failed，客户端以状态字段为准，不仅凭事件名称判断。

模型尚在生成时不会发逐 token delta；ModelCompleted 表示完整模型决策已经保存，RunCompleted 表示最终答案和完成事件已经同事务提交。

## 进程探针

server-go HTTPHost 提供无需鉴权的 `GET /ping`（200，正文 `pong`）、`GET /healthz`（就绪时 200，正文 `healthz`；未就绪时 503）和 `GET /version`。探针只反映进程与服务生命周期，不持续检查外部依赖。本仓库不提供 Forge 项目工作台或企业资产业务入口。

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

`GET /internal/v1/executions/{id}/lease?epoch=N` 返回当前数据库时钟下的执行权；取消、旧 epoch 或其他 owner 均无法通过。模型工具参数不能提供该执行身份，Worker 在 HTTP headers 中注入真实 run ID 和 epoch。运行时执行权不替代业务授权或外部进程停止确认。projectID 是可选的执行上下文；工具或文件系统绑定本身不要求选择项目，资源是否允许使用由调用方授权。

## Public run trace

`GET /v1/runs/:id/trace?after=0` (RunAPI.Trace) returns an owner-scoped `RunTrace`: the current public Run, up to 50 events in sequence order, `latestEvent`, `nextSeq`, and `hasMore`. Repeat with `after=nextSeq` while `hasMore`, then poll for new events. Each event includes `createdAt`. The snapshot uses a repeatable-read transaction; refreshing or reconnecting replays persisted events. `ModelStarted` is committed before each model call. Tool events include their public name; ToolStarted includes public arguments recovered from assistant messages, including historical runs. ToolCompleted includes the saved result. `arguments`, `result`, `output`, and `detail` previews are capped at 32 KiB UTF-8 and marked with `<field>Truncated: true`; full originals remain in storage. No checkpoint, hidden reasoning, system prompt, model/tool credentials or lease identity is returned. This endpoint is read-only and does not resume tasks. Invalid negative/non-numeric cursors return 400; another owner’s run returns 404. Responses use Cache-Control: no-store.

### 沙箱工具绑定

AgentConfig 可设置 `toolsets:["filesystem"]`，并将平台结束工具设为 `endRun:true`。受信任的内部 ExecutionSpec 必须同时提供 `filesystem:{provider:"AIO",url:"http://coding/internal/v1/rounds/<round>/sandbox",tokenEnv:"CODING_TOOLS_TOKEN",workspace:"/workspace/repo"}`。绑定随执行配置冻结，幂等重试不能更换沙箱。普通会话提交不能使用需要可信绑定的文件工具集。凭据值不写入配置快照。

## 调用方职责

本 API 不提供评测实验、dataset、项目工作台、试运行产品入口或 Coding 发布审批。调用方负责这些业务及其持久化，只通过通用 Agent 和 Execution 接口发起执行。`spec.scope` 中的 tenantID、projectID、actorID、taskID、roundID 是执行上下文，不代表本 API 拥有项目成员或权限模型，也不作为授权证明。

本仓库不提供 eval/migrate CLI；数据库由部署方在接收业务请求前准备。已有业务表不随接口移除自动删除。
