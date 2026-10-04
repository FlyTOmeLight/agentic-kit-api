# 任务接口

Go 请求与响应类型统一定义在 [sdk](../sdk)，调用方式见 [SDK 用法](../sdk/README.md)。SDK 不依赖 Gin；服务端负责校验。

所有接口要求 `Authorization: Bearer <token>`。身份来自部署配置，不接受客户端指定 owner。所有对象按 owner 隔离；访问其他 owner 的对象返回 404。创建、编辑和发布 Agent 还要求 owner 在 `auth.config_owners` 中，否则返回 403。当前不支持跨 owner 共享 Agent。

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

config 包括 systemPrompt、model、tools、maxTurns，不接受 query/history/agentID/configVersion。模型配置与工具字段见 [创建请求样例](../configs/agents.example.json)。模型地址必须是 HTTP(S)；maxTurns 为 1～100。工具名称不能重复，parameters 必须为 object 类型 JSON Schema，policy 必须显式提供。AppForge 尚无已验证的下游幂等契约，不能配置为 Idempotent。

Agent 创建时 revision=1，编辑成功递增 revision。编辑或发布携带旧 revision 返回 409。同一 revision 重复发布返回同一个版本，版本 number 从 1 开始。仅已发布 Agent 可以创建会话。每次提交消息使用当时最新的发布版本，并把配置写入 Run 快照；恢复时不重新读取草稿或最新版本。Run 响应中的 agentID、configVersion 可用于定位版本。

提交消息返回 202 和 Run；同一 owner 的相同请求编号、相同 session/query 重试返回 200 和原任务，不重复保存用户消息。发布新版本不改变该行为。请求编号重复但输入不同、同一会话已有活动任务、无效状态转换返回 409。

列表返回 `{"items":[]}`。默认 limit=50；Agent、会话、任务列表最多 100 项，按 ID 升序，after 是上一页最后一项 ID。ID 排序不代表创建时间。消息列表最多 200 项，按会话内 seq 升序，after 默认 0，下一页使用上一页最后一项 seq。Agent、会话和消息时间以 UTC RFC3339 返回。

消息包含 sessionID、seq、runID、kind、role、content、可选 toolCalls/toolCallID、createdAt。kind 包括 User、Assistant、Final、Tool、ClarificationAnswer、ReviewResult。助手工具决策保存为 Assistant，最终回复保存为 Final；澄清回答与人工确认属于工具结果，role 为 tool。通过 toolCallID 与助手 toolCalls 中的 ID 配对。失败和取消从 Run 状态及 Event 读取，不伪造成助手回复。

消息与对应任务状态在同一事务提交。客户端可以用会话消息列表展示历史，用 Run SSE 展示当前任务进度；Event 不作为聊天记录。当前模型上下文取最近 20 个已完成 Run 的 User/Final 消息，工具历史保存在消息表供查询，尚未实现跨轮工具历史与上下文压缩。

任务状态为 Queued、Running、WaitingClarification、NeedsReview、Completed、Failed、Canceled。等待状态查询结果包含 waitingStep。answer/resolve 使用这个值作为 stepID；result 是提供给模型的用户回答或已确认工具结果。

SSE 每条消息的 id 为 runID:seq，data 包含 runID、seq、kind 和 payload。客户端按该 id 去重。只在终态且已发送全部提交事件后关闭连接；WaitingClarification 和 NeedsReview 不是终态。重新连接不会启动新的任务。

重要事件包括 RunQueued、RunStarted、ModelCompleted、ToolStarted、ToolCompleted、ClarificationRequested、RunNeedsReview、RunResumed、RunRetry、RunCompleted、RunFailed、RunCanceled。RunRetry 的 payload.status 可能为 Failed，客户端以状态字段为准，不仅凭事件名称判断。

模型尚在生成时不会发逐 token delta；ModelCompleted 表示完整模型决策已经保存，RunCompleted 表示最终答案和完成事件已经同事务提交。

## Gin 请求与错误契约

对象接口返回 JSON，列表返回 items；取消与回答成功返回 204。错误统一返回：

```json
{"error":{"code":"InvalidRequest","message":"invalid request body"}}
```

错误码包括 Unauthorized、Forbidden、InvalidRequest、PayloadTooLarge、InvalidCursor、NotFound、MethodNotAllowed、Conflict、InternalError。请求体超过 1 MiB 返回 413，未知字段或连续多个 JSON 文档返回 400；不向客户端输出校验器内部错误和数据库错误。

请求日志只记录方法、路由模板、状态与耗时，不记录 token 或请求体。Recovery 在响应未提交时返回 JSON 500；SSE 已开始后只结束处理，不向事件流追加 JSON 错误。SSE 线格式由 gin-contrib/sse 编码，字段冒号后是否有空格不影响标准 SSE 消费端。

## 受信任应用执行

`POST /internal/v1/executions` 仅允许 config_owners 身份。请求包含不可变 spec、scope（tenantID/projectID/actorID/taskID/roundID）和 timeoutSeconds，幂等键必填。接口原子创建 Agent 版本、Session、Run、输入消息和事件；同键不同快照返回冲突。此服务身份不能下发给浏览器。业务应用负责从真实用户会话和任务责任链构造 scope，并在敏感操作前重新查询业务授权。

`GET /internal/v1/executions/{id}/lease?epoch=N` 返回当前数据库时钟下的执行权；取消、旧 epoch 或其他 owner 均无法通过。模型工具参数不能提供该执行身份，Worker 在 HTTP headers 中注入真实 run ID 和 epoch。运行时执行权不替代业务授权或外部进程停止确认。
