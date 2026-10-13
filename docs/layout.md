# 项目目录与依赖方向

API 采用 [server-go 项目组织约定](https://github.com/FlyTOmeLight/server-go/blob/main/docs/go-project-layout.md)。HTTP、SDK 和配置按框架布局组织，具有真实跨仓库用途的持久执行包保留为公开包。

```text
agentic-kit-api/
├── cmd/
│   ├── server/main.go          # Bootstrap、HTTPHost、配置与信号
│   └── cli/                    # server-go CLI：migrate、eval
├── internal/
│   ├── service/                # 资源、总路由、HTTP 契约测试与生命周期
│   ├── app/
│   │   ├── apperror/           # durable 领域错误到框架 HTTP 错误的映射
│   │   ├── agent/              # Agent Controller、路由与消费方 Service 接口
│   │   ├── session/            # Session、消息、历史
│   │   ├── run/                # 执行、取消、SSE、trace
│   │   ├── evaluation/         # 持久实验接口
│   │   └── platform/           # Forge 项目与企业资产接入、授权编排
│   ├── client/                 # Tenant 鉴权与 Coding 环境的外部协议、连接所有权
│   └── config/                 # 类型化配置、框架配置快照与校验
├── sdk/go/agenticv1/            # 公开 HTTP 契约与 server-go 客户端
│   ├── agentic_client.go       # 客户端构造、业务入口与 Close
│   ├── agentic_api_*.go        # 各领域请求、响应和公开数据
│   ├── agentic_interface_*.go  # 领域接口
│   ├── agentic_client_*.go     # 各领域 HTTP 调用
│   └── agentic_transport.go    # 框架传输、超时与标准错误解码
├── pkg/
│   ├── durable/                # API 与 worker 共用的状态、模型、存储与业务事务
│   └── evaluation/             # CLI 与持久实验共用的确定性评分、配对比较
├── conf/server/config.toml     # 框架默认配置路径
├── examples/
│   ├── agents/                 # Agent 创建、发布请求样例
│   └── evaluation/             # 冻结评测套件
├── deploy/                     # 本地数据库与 Redis
├── charts/                     # Kubernetes 部署
├── docs/
├── go.mod
└── Makefile
```

## 装配与职责

装配方向为 `cmd/server → internal/service → internal/app/<领域>`。入口构造配置 Manager 和 Logger，Bootstrap 初始化资源，HTTPHost 安装路由并运行服务。`service.API` 拥有 Store、Redis、Tenant 与 Coding 客户端，以及 Outbox、评测后台任务。RegisterRoutes 只安装路由；Start 启动任务；Stop 取消并等待任务；Close 在 HTTP 与后台任务结束后关闭连接。配置在启动时加载，修改后重启。

各领域 Controller 处理 HTTP 绑定、状态码与响应，调用消费方定义的 Service 接口。请求与响应复用 `sdk/go/agenticv1` 的公开类型；绑定和校验留在服务端。Session 负责提交消息，Run 负责执行状态与 SSE。领域包之间不互相调用 Controller，context 沿调用链传递。

Forge 接入位于 `internal/app/platform`，负责项目和企业资产的身份、权限及业务编排。Tenant 会话及授权协议、Coding 独立环境协议位于 `internal/client`，由装配层显式构造并管理连接；客户端不导入 Controller 或 Gin。

## 共享持久化边界

`pkg/durable` 是 API 与独立 worker 实际共用的执行模块。模型、查询与事务由该包集中维护，业务规则按 agent_service、session_service、run_service、execution_service、evaluation_service 等领域文件组织。Store 接收 context 并使用 GORM；事务由业务操作确定范围。用户消息与任务、执行消息与状态及 Outbox 都需要同事务提交。

该包继续公开，worker 可以导入；迁到 `internal/table` 会使 worker 无法访问。因此本项目的共享模型与存储职责放在 `pkg/durable`，不建立第二份模型或转发层。表结构仍以 models.go 为唯一来源，由 `cmd/cli migrate` 调用 AutoMigrate。`pkg/evaluation` 提供跨 CLI 与持久实验使用的纯函数评分逻辑。

Worker 仓库维持自己的 `cmd/worker`、`internal/engine`、`internal/adapters` 和 `configs`。它只引用公开共享包，不导入 API 的 internal。跨进程恢复测试实际构建 `cmd/server`，通过 HTTP 提交和读取任务；本地联合验证使用临时 Go workspace。

## SDK

公开导入路径为 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`，包名 `agenticv1`。文件按领域拆分请求、响应、接口和调用方法。SDK 不导入 API 的 internal、durable、Gin 或 GORM。durable 复用 SDK 的公开领域类型，保留私有执行快照、租约与 GORM 模型，HTTP 返回值显式转换为公开响应。

Client 提供 Agents、Sessions、Runs、Executions、Evaluations，ProjectClient 提供 Forge 会话接口。私有传输复用 server-go sdk.Client，每次调用创建独立请求，显式传递 context；身份只属于对应客户端或本次请求。普通 JSON 有明确超时，SSE 由调用方 context 控制，不缓冲完整事件流。SDK 使用框架的 sdk.DecodeError，向调用方返回 *server-go/errors.Error；Status 是 HTTP 状态，StatusCode 是框架错误编号。

客户端所有者在请求结束后调用 Close，流所有者关闭返回的响应体。构造时传入的标准 HTTP Transport 会被克隆，SDK 管理自己的连接池。旧 SDK 导入路径、旧命令目录均已移除，调用方需同步迁移；业务成功响应保持原契约；错误响应改为框架的平铺格式，见 docs/api.md。

## 通用 HTTP 能力

server-go HTTPHost 统一安装请求 ID、请求日志、Recovery、404/405 和错误处理。Controller 直接调用框架 binder.BindJSON、binder.QueryLimit、auth.UserID 与 errors.Abort；服务凭据解析和鉴权使用 auth.ParseBearerTokens、auth.BearerTokens。SSE 使用框架 stream.NewSSE、SendWithID 与 Heartbeat；事件查询和回放游标属于 Run 业务。服务只声明路由、权限和领域错误映射，不维护通用 HTTP 工具包。这些能力统一来自已发布的 server-go v0.3.1；API 独立构建无需本地框架源码。
