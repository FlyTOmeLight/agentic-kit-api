# Go SDK

导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk`。SDK 与 API 同模块发布；私有仓库认证见根目录 README。

下面的函数完成创建 Agent、发布、创建会话和提交消息。token 对应的 owner 需要配置写权限。

```go
package example

import (
    "context"
    "time"

    "github.com/FlyTOmeLight/agentic-kit-api/sdk"
)

func Start(ctx context.Context, baseURL, token string) (*sdk.Run, error) {
    client, err := sdk.NewClient(baseURL, token, nil)
    if err != nil {
        return nil, err
    }
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

`NewClient` 的第三个参数可以传自定义 `*http.Client`，用于连接池、TLS 和超时设置。客户端复制该配置，以 Resty 执行请求并禁用重定向，避免鉴权请求改变目标；不修改原 client，也不关闭调用方共享的 transport。默认没有总超时，调用方应给 context 设置截止时间。Client 可以并发复用。

非 2xx 响应返回 `*sdk.APIError`，包含 `StatusCode`、`Code` 和 `Message`，可用 `errors.As(err, &apiErr)` 判断。网络错误和 context 取消保留原错误链，可用 `errors.Is` 判断。

`client.Runs.OpenEvents(ctx, runID, after)` 返回原始 SSE `io.ReadCloser`。调用方使用 SSE 消费器读取事件，并负责调用 Close；提前结束也必须关闭。`sdk.Event` 定义每条事件 data 的 JSON 结构。断线后使用最后收到的 `runID:seq` 再次调用；SDK 不自动解析 SSE 或重连。事件订阅应使用独立的 context，按业务需要设置订阅时长。

列表方法返回 `sdk.Page[T]`。ID 列表用最后一项 ID 作为下一次的 `After`，消息列表用最后一项 `Seq`；`Limit=0` 使用服务端默认值。详细分页规则、权限和状态码见 [HTTP 契约](../docs/api.md)。

## 接口与依赖注入

`Client.Agents`、`Client.Sessions`、`Client.Runs` 分别是 `AgentAPI`、`SessionAPI`、`RunAPI` 接口。业务实现和 Resty 客户端均不导出。调用方可以只接收所需的业务接口，例如：

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

调用方若只使用一两个方法，可以在自己的包内定义更小的接口。Client 的三个业务实现可以并发复用，开始并发调用后不要替换 Client 字段。

## 从 v0.2.2 升级到 v0.3.0

HTTP 路径、JSON 类型和数据库格式不变。Go 方法改为按业务访问，旧的扁平方法已移除：

| v0.2.2 | v0.3.0 |
| --- | --- |
| CreateAgent / GetAgent / ListAgents / UpdateAgent / PublishAgent / GetAgentVersion | Agents.Create / Get / List / Update / Publish / GetVersion |
| CreateSession / GetSession / ListSessions | Sessions.Create / Get / List |
| SendMessage / ListMessages / ListSessionRuns | Sessions.SendMessage / ListMessages / ListRuns |
| GetRun / CancelRun / AnswerRun / ResolveRun / OpenRunEvents | Runs.Get / Cancel / Answer / Resolve / OpenEvents |

NewClient 的参数不变。请求、响应、错误和分页类型仍在 sdk 包；构造函数不要求调用方了解 Resty。
