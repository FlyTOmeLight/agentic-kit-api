# Go SDK

导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。SDK 与 API 同模块发布；私有仓库认证见根目录 README。

下面的函数完成创建 Agent、发布、创建会话和提交消息。token 对应的 owner 需要配置写权限。

```go
package example

import (
    "context"
    "time"

    sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func Start(ctx context.Context, baseURL, token string) (*sdk.Run, error) {
    client, err := sdk.NewClient(baseURL, token, nil)
    if err != nil {
        return nil, err
    }
    defer client.Close()
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()
    agent, err := client.Agents.Create(ctx, sdk.CreateAgentRequest{
        Name: "对话助手",
        Config: sdk.AgentConfig{
            SystemPrompt: "请用中文回答。",
            Model: sdk.ModelSpec{
                Name: "qwen3.8:27b-mlx",
                BaseURL: "http://127.0.0.1:11434/v1",
            },
            MaxTurns: 10,
        },
    })
    if err != nil {
        return nil, err
    }
    _, err = client.Agents.Publish(ctx, agent.ID, sdk.PublishAgentRequest{Revision: agent.Revision})
    if err != nil {
        return nil, err
    }
    session, err := client.Sessions.Create(ctx, sdk.CreateSessionRequest{AgentID: agent.ID})
    if err != nil {
        return nil, err
    }
    run, _, err := client.Sessions.SendMessage(ctx, session.ID, "question-001",
        sdk.SendMessageRequest{Query: "请介绍一下你自己。"})
    return run, err
}
```

实际应用保存 Agent 和 Session ID，后续消息直接调用 `client.Sessions.SendMessage`。`Idempotency-Key` 在同一 owner 下去重；应用应为每条新消息生成并保存唯一编号，网络失败重试时复用它。客户端不自动创建编号或重试。

`Sessions.SendMessage` 的第二个返回值表示是否创建新任务：202 为 true，200 幂等回放为 false。提交成功只代表服务接受任务；通过 `Runs.Get`、`Sessions.ListMessages` 或事件流获取执行结果。

`NewClient` 的第三个参数可以传自定义 `*http.Client`，用于连接池、TLS 和超时设置。SDK 复用 server-go 客户端，以 Resty v3 执行请求并禁用重定向。标准 HTTP Transport 被克隆，不修改原 client；普通 JSON 默认 20 秒超时，传入 HTTP Client 的正超时值会覆盖此默认值。调用方在请求与流结束后调用 client.Close()。Client 可以并发复用。

非 2xx 响应返回 `*github.com/FlyTOmeLight/server-go/errors.Error`，包含 HTTP 状态 `Status`、框架错误编号 `StatusCode`、`Code`、`Message` 和 `RequestID`，可用 `errors.As(err, &apiErr)` 判断。网络错误和 context 取消保留原错误链，可用 `errors.Is` 判断。

`client.Runs.OpenEvents(ctx, runID, after)` 返回原始 SSE `io.ReadCloser`。调用方使用 SSE 消费器读取事件，并负责调用 Close；提前结束也必须关闭。`sdk.Event` 定义每条事件 data 的 JSON 结构。断线后使用最后收到的 `runID:seq` 再次调用；SDK 不自动解析 SSE 或重连。事件订阅应使用独立的 context，按业务需要设置订阅时长。

列表方法返回 `sdk.Page[T]`。ID 列表用最后一项 ID 作为下一次的 `After`，消息列表用最后一项 `Seq`；`Limit=0` 使用服务端默认值。详细分页规则、权限和状态码见 [HTTP 契约](../../../docs/api.md)。

## 接口与依赖注入

`Client.Agents`、`Client.Sessions`、`Client.Runs` 分别是 `AgentAPI`、`SessionAPI`、`RunAPI` 接口。业务实现和 server-go SDK（Resty v3）客户端均不导出。调用方可以只接收所需的业务接口，例如：

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

## 从 v0.2.2 升级到 v0.3.0

HTTP 路径、JSON 类型和数据库格式不变。Go 方法改为按业务访问，旧的扁平方法已移除：

| v0.2.2 | v0.3.0 |
| --- | --- |
| CreateAgent / GetAgent / ListAgents / UpdateAgent / PublishAgent / GetAgentVersion | Agents.Create / Get / List / Update / Publish / GetVersion |
| CreateSession / GetSession / ListSessions | Sessions.Create / Get / List |
| SendMessage / ListMessages / ListSessionRuns | Sessions.SendMessage / ListMessages / ListRuns |
| GetRun / CancelRun / AnswerRun / ResolveRun / OpenRunEvents | Runs.Get / Cancel / Answer / Resolve / OpenEvents |

NewClient 的参数不变。请求、响应和分页类型位于 agenticv1 包；错误类型复用 server-go/errors.Error；构造函数不要求调用方了解 server-go SDK（Resty v3）。

## 受信任应用执行

`Client.Executions` 提供 Submit 与 CheckLease，使用 SubmitExecutionRequest / ExecutionSpec / Scope。ExecutionSpec 仅包含 agentID、agentVersion、scope 和 query。先通过 Agents 创建并发布定义，再调用 Executions.Submit；同一应用任务跨轮复用 Session。Submit 的幂等键与不可变快照重试语义和服务端一致。仅配置为 config_owners 的服务身份可调用；浏览器用户不得持有此令牌。Run.Scope 记录提交时的业务上下文，不替代每次敏感操作的动态授权。

## 持久实验

`Client.Evaluations` 实现 `EvaluationAPI`，提供 Start、Get、List、Cancel、Compare。Start 输入 `StartEvaluationRequest{Suite, Scope}`，显式传入并保存幂等键；202 返回 created=true，200 返回原实验。List 要求 tenantID / projectID，Limit=0 使用服务端默认值。Compare 参数顺序为 candidateID、baselineID。

客户端通过返回的 Trials 查到原 Run 与会话。轮询 Get 到 Completed 或 Canceled 后读取 Report；NeedsAttention 需要查看原 Run 并提供澄清或人工确认。Completed 与 Report.Passed 分别表示已完成判分和门槛通过。服务重启后继续读取同一个实验，不再次创建新键。模型和工具配置仍通过不可变 AgentVersion 装配，suite 不接受内联定义。

这些方法仅供受信任应用使用。网关必须从已验证身份构造 Scope，并核对项目权限及对象归属。SDK 不提供浏览器会话认证。字段、限制和失败语义见 [HTTP 契约](../../../docs/api.md#持久评测实验)。

## Forge 项目客户端

`NewProjectClient(baseURL, tenantID, projectID, session, csrf, httpClient)` 使用真实 Forge 会话，提供项目 Agent 草稿、候选、版本和实验操作。公开响应不含模型地址或凭据引用。它与使用 Bearer token 的 `NewClient` 分开；浏览器保持 Cookie/CSRF 流程，不能使用运行时密钥。详见 [HTTP 契约](../../../docs/api.md#forge-项目-agent-与评测入口)。

项目客户端还提供 `EvaluationPlans`、`EvaluationPlan`、`SaveEvaluationPlan`。新方案使用客户端生成的稳定 ID 和 revision=0；后续保存使用返回的 revision，409 时重新读取并核对修改。保存方案不会执行 Agent。

`StartTrial(ctx, agentID, key, request)`、`Trial`、`CancelTrial` 支持固定候选的独立试运行。创建返回结果及 created 标记；轮询同一个 Run 恢复输出，不通过重新提交来查询进度。试运行结果不代表评测通过。

Coding scenarios use `CodingEvaluationScenario` in the same saved plans and experiment APIs. During environment preparation a Trial has no Run yet; use `Trial.Coding.Status`. Independent check logs, exit codes and patches are available as `CodingEvaluationEvidence`. Treat `Stopped` as the environment cleanup confirmation, including canceled experiments.

## 布局迁移

旧导入路径 `github.com/FlyTOmeLight/agentic-kit-api/sdk` 已移除，改用 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。可用 `sdk` 别名保留调用代码的类型前缀；业务路径和成功响应字段保持原契约；错误采用框架的平铺格式，详见上文错误处理。框架客户端的传输资源由 Client.Close／ProjectClient.Close 释放，调用方须在全部请求与事件流结束后关闭。
