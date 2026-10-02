# 任务接口

所有接口要求 `Authorization: Bearer <token>`。用户身份由部署配置中的 token 映射得到，不信任客户端传入的身份。Agent 的 owners 限制谁能提交；查询、事件、取消和回答都按 owner 隔离。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /v1/runs | 创建任务 | agentID、sessionID、query；Idempotency-Key header 必填 |
| GET /v1/runs/{id} | 查询任务 | 无 |
| POST /v1/runs/{id}/cancel | 取消 | 无 |
| POST /v1/runs/{id}/answer | 回答澄清 | stepID、result |
| POST /v1/runs/{id}/resolve | 提交人工确认的工具结果 | stepID、result |
| GET /v1/runs/{id}/events | SSE 回放及订阅 | Last-Event-ID header 或 after query，格式 runID:seq |

创建返回 202；同一请求编号、相同请求重试返回 200 和原任务；请求编号重复但内容不同、同一会话已有活动任务、无效状态转换返回 409。未授权返回 401，非当前 owner 的任务返回 404。

任务状态为 Queued、Running、WaitingClarification、NeedsReview、Completed、Failed、Canceled。等待状态查询结果包含 waitingStep。answer/resolve 使用这个值作为 stepID；result 是提供给模型的用户回答或已确认工具结果。

SSE 每条消息的 id 为 runID:seq，data 包含 runID、seq、kind 和 payload。客户端按该 id 去重。只在终态且已发送全部提交事件后关闭连接；WaitingClarification 和 NeedsReview 不是终态。重新连接不会启动新的任务。

重要事件包括 RunQueued、RunStarted、ModelCompleted、ToolStarted、ToolCompleted、ClarificationRequested、RunNeedsReview、RunResumed、RunRetry、RunCompleted、RunFailed、RunCanceled。RunRetry 的 payload.status 可能为 Failed，客户端以状态字段为准，不仅凭事件名称判断。

模型尚在生成时不会发逐 token delta；ModelCompleted 表示完整模型决策已经保存，RunCompleted 表示最终答案和完成事件已经同事务提交。

## Gin 请求与错误契约

成功响应保持原任务 JSON 或 204 无响应体。错误统一返回：

```json
{"error":{"code":"InvalidRequest","message":"invalid request body"}}
```

错误码包括 Unauthorized、InvalidRequest、PayloadTooLarge、InvalidCursor、NotFound、MethodNotAllowed、Conflict、InternalError。请求体超过 1 MiB 返回 413，未知字段或连续多个 JSON 文档返回 400；不向客户端输出校验器内部错误和数据库错误。

请求日志只记录方法、路由模板、状态与耗时，不记录 token 或请求体。Recovery 在响应未提交时返回 JSON 500；SSE 已开始后只结束处理，不向事件流追加 JSON 错误。SSE 线格式由 gin-contrib/sse 编码，字段冒号后是否有空格不影响标准 SSE 消费端。
