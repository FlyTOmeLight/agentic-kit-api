# Go SDK

导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。SDK 与 API 同模块发布；私有仓库认证见根目录 README。

身份校验已接入 Forge middleware。用户业务请求必须携带 `x-forge-tenant-id`，可选 `x-forge-project-id`；服务端将验证后的 tenant/project 写入 `access.Scope`，路由末端拒绝空租户。

`NewClient(baseURL, token, httpClient)` 当前要求非空 Bearer token。用户调用应传入 Forge 可验证的用户 API key，并在 HTTP 传输层补齐 `x-forge-tenant-id` 与可选 `x-forge-project-id`。发布、读取版本正文和新任务提交会访问 artifact。API 使用自身配置的 `Tenant.ServiceAPIKey` 传播已核验的 user/tenant/project，并将个人资源归属固定为该用户。入站用户 Bearer、Cookie、CSRF 和 Origin 均不转发。缺 tenant 或 actor 时服务端拒绝远端调用。会话认证示例见 [根 README](../../../README.md#调用接口)。

下面的函数读取草稿，接收调用方已完成身份和租户 Header 装配的客户端；客户端的创建和关闭由调用方负责。

```go
package example

import (
    "context"
    "time"

    sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func ReadAgent(ctx context.Context, client *sdk.Client, agentID string) (*sdk.Agent, error) {
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()
    return client.Agents.Get(ctx, &sdk.GetAgentRequest{ID: agentID})
}
```

实际应用保存 Agent 和 Session ID，后续消息调用 `client.Sessions.SendMessage`。`Idempotency-Key` 在同一 tenant、owner 下去重，不重放其他租户的 Run；应用应为每条新消息生成并保存唯一编号，网络失败重试时复用它。客户端不自动创建编号或重试。

`Sessions.SendMessage` 的响应 `Created` 表示是否创建新任务：202 为 true，200 幂等回放为 false。提交成功只代表服务接受任务；通过 `Runs.Get`、`Sessions.ListMessages` 或事件流获取执行结果。

Run 因模型或摘要消费未知进入 Blocked 后，人工可调用 `client.Runs.Unblock(ctx, &sdk.UnblockRunRequest{ID: runID, Epoch: blockedRun.Epoch, Reason: "已核查并接受未知消费，授权重新派发"})`。成功返回 nil，服务端递增 epoch 并重新排队，保留 checkpoint 和已记录用量。该动作可能重复计费，不补写未知 usage。状态、epoch、期限、会话或预算不允许恢复时返回 409；计价失败也不能用此入口解除。重复请求返回 409，响应丢失时先查 Run 与事件，不自动读取新 epoch 后再次批准。恢复 child 不直接恢复 Goal；完成相关核查后还需调用 `POST /internal/v1/goals/{id}/resume` 显式恢复 Goal，完整准入与审计字段见 HTTP 契约。

## Memory

`Client.Memories` 提供 Memory 的 Create、Get、List、Update、Retire、ListVersions 和 GetVersion，支持 OwnerPrivate、租户级 ActorPrivate 和项目内 ActorProject。ActorPrivate 使用租户级身份，ActorProject 使用项目级身份（`x-forge-project-id`）。文档的 actor 可留空或填写当前用户。项目级读取需要项目 `read` 权限，变更需要项目 `write` 权限。ActorProject 默认 `Private`，项目应用执行只召回标为 `Project` 的条目。`UpdateMemoryRequest.Restore=false` 或省略字段时更新活跃条目，`Restore=true` 时恢复停用条目；两种操作都需要当前 `ExpectedRevision` 和完整 `Document`。

```go
restored, err := client.Memories.Update(ctx, entry.ID, &sdk.UpdateMemoryRequest{
    IdempotencyKey:   "memory-restore-001",
    ExpectedRevision: entry.Revision,
    Restore:          true,
    Document:         sdk.MemoryDocument{
        // 填写完整的 scope、kind、subjectKey、summary、body 和 source。
    },
})
if err != nil {
    return err
}
```

`IdempotencyKey` 由 SDK 写入必填的 `Idempotency-Key` Header。同一租户和 owner 下，同键同参重试返回首次生效结果，同键不同参或 actor 返回 409。网络超时或 503 后复用原键。

Update 和 Restore 的 revision 不匹配、恢复活跃条目、更新停用条目或改变身份字段都会返回 409。Memory 写操作的 503 状态包括：`MemoryOperationPending`（操作已受理但尚未完成，使用原键重试）、`MemoryUnavailable`（依赖暂不可用，修复后继续原键）、`MemoryOperationOutcomeUnknown`（本地生效提交结果不明，保留原键、原 collection 和原版本身份继续原键处理，不能换键或重建集合）和 `MemoryVersionUnavailable`（归档版本正文不可用，可能来自源版本 410；同样保留原版本身份并交由受控修复）。

`ListVersions` 使用 `ListMemoryVersionsOptions` 的 `AfterRevision` 和 `Limit` 分页；下一页游标取响应的 `NextAfterRevision`，为零表示结束。`GetVersion` 只读取该条目曾生效的归档版本。列表不访问 artifact；未配置归档时列表为空。详情的正文缺失返回 `MemoryVersionUnavailable`。

`NewClient` 的第三个参数可以传自定义 `*http.Client`，用于连接池、TLS 和超时设置。SDK 复用 server-go 客户端，以 Resty v3 执行请求并禁用重定向。标准 HTTP Transport 被克隆，不修改原 client；普通 JSON 默认 20 秒超时，传入 HTTP Client 的正超时值会覆盖此默认值。调用方在请求与流结束后调用 client.Close()。Client 可以并发复用。

非 2xx 响应返回 `*github.com/FlyTOmeLight/server-go/errors.Error`，包含 HTTP 状态 `Status`、框架错误编号 `StatusCode`、`Code`、`Message` 和 `RequestID`，可用 `errors.As(err, &apiErr)` 判断。网络错误和 context 取消保留原错误链，可用 `errors.Is` 判断。

`sdk.ModelSpec.ContextWindow`配置模型窗口；大于零时同时设置正数且小于窗口的`MaxTokens`，作为输出预留。未配置窗口时使用128 KiB输入字节上限，配置窗口时当前仍采用保守字节估算而非精确token计数。提交期已知输入超限返回HTTP 400、`Code == "ContextBudgetExceeded"`，不会创建Run；worker仍会按实际工具定义和当前消息再次检查。

`client.Runs.OpenEvents(ctx, &sdk.OpenRunEventsRequest{ID: runID, LastEventID: after})` 返回原始 SSE `io.ReadCloser`。调用方使用 SSE 消费器读取事件，并负责调用 Close；提前结束也必须关闭。`sdk.Event` 定义每条事件 data 的 JSON 结构。断线后使用最后收到的 `runID:seq` 再次调用；SDK 不自动解析 SSE 或重连。事件订阅应使用独立的 context，按业务需要设置订阅时长。

列表方法返回 `sdk.Page[T]`。ID 列表用最后一项 ID 作为下一次的 `After`，消息列表用最后一项 `Seq`；`Limit=0` 使用服务端默认值。详细分页规则、权限和状态码见 [HTTP 契约](../../../docs/api.md)。

## 接口与依赖注入

`Client.Agents`、`Client.Sessions`、`Client.Runs`、`Client.Memories` 分别是 `AgentAPI`、`SessionAPI`、`RunAPI`、`MemoryAPI` 接口。业务实现和 server-go SDK（Resty v3）客户端均不导出。调用方可以只接收所需的业务接口，例如：

```go
type Conversation struct {
    sessions sdk.SessionAPI
}

func NewConversation(sessions sdk.SessionAPI) *Conversation {
    return &Conversation{sessions: sessions}
}

// 装配时：conversation := NewConversation(client.Sessions)
// 测试时：传入实现 SessionAPI 的测试替身。
```

调用方若只使用一两个方法，可以在自己的包内定义更小的接口。Client 的业务实现可以并发复用，开始并发调用后不要替换 Client 字段。

## 请求与响应

所有操作接收标准 `context.Context` 和一个操作请求指针；带目标 ID 的方法同时接收单独的 ID 参数。请求对象可以承载路径、query、Header 和 JSON 字段，SDK 按协议分别编码，不将路径和 Header 字段写入 JSON。比如 `Runs.Get(ctx, &sdk.GetRunRequest{ID: runID})`、`Sessions.ListMessages(ctx, &sdk.ListMessagesRequest{SessionID: sessionID, ListMessagesOptions: sdk.ListMessagesOptions{After: 10, Limit: 50}})`、`Memories.Update(ctx, memoryID, &sdk.UpdateMemoryRequest{IdempotencyKey: key, ExpectedRevision: revision, Document: document})`。

普通查询返回领域对象或分页结果；没有响应 body 的操作返回 `error`。提交消息和执行返回包含领域结果与 `Created` 的操作响应。事件流使用 `OpenEvents(ctx, &sdk.OpenRunEventsRequest{ID: runID, LastEventID: "runID:seq"})`，调用方负责关闭返回的 `io.ReadCloser`。

错误类型复用 `server-go/errors.Error`，普通 JSON 请求直接使用框架 `sdk.Complete` 处理网络、取消、解码和 HTTP 失败。客户端额外拒绝重定向，避免把认证信息带到其他地址。

## 受信任应用执行

`Client.Executions` 提供 Submit 与 CheckLease，使用 SubmitExecutionRequest / ExecutionSpec / Scope。先通过 Agents 创建并发布定义，再调用 Executions.Submit；同一应用任务跨轮复用 Session。Submit 的幂等键与不可变快照重试语义和服务端一致。内部 execution/goal/lease 路由仅在配置 propagation 服务白名单时注册，由 Forge `MiddlewareInternalPropagation` 校验服务 Authorization 及目标身份；调用方需携带 `x-impersonate-target-tenant-id`、`x-impersonate-target-user-id` 和可选 `x-impersonate-target-project-id`，并使 ExecutionSpec 的 tenant/project 与已验证 scope 一致。浏览器用户不得持有服务凭据。Run.Scope 记录提交时的业务上下文，不替代每次敏感操作的动态授权。

## 沙箱检查点恢复

沙箱路由仅在配置 `Sandbox.AllowedServices` 并装配服务认证时注册；申请的 tenant/owner 来自可信调用方请求体，不从 Forge 用户 scope 推导。`Sandboxes.Acquire` 的 `RequiredSave` 开启必要保存保护；`RestoreFromSandboxID` 指定已保存、销毁的来源沙箱。服务端依据新 Run 冻结的 Sandbox 文件系统配置校验来源和 Profile；SDK 不接受检查点目录路径。恢复成功后才返回 Active 和 lease，`Sandbox.ResumeCheckpoint` 返回实际恢复的检查点。

释放返回 SaveBlocked 表示保存尚未完成，需查询到 Destroyed 和非空 SavedCheckpoint 后才能作为恢复来源。同一申请重试保留 requestKey；修改恢复来源必须使用新请求键。当前实现与本地验证范围见仓库 `docs/sandbox-center/layerfs-save-verification.md`。

## 布局迁移

旧导入路径 `github.com/FlyTOmeLight/agentic-kit-api/sdk` 已移除，改用 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。可用 `sdk` 别名保留调用代码的类型前缀；业务路径和成功响应字段保持原契约；错误采用框架的平铺格式，详见上文错误处理。框架客户端的传输资源由 Client.Close 释放，调用方须在全部请求与事件流结束后关闭。
