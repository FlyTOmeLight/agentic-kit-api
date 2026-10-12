# 项目目录与依赖方向

本项目采用 Go 官方服务端项目布局建议，并结合 Gin 按资源组织路由的做法。Gin 没有为本项目提供一套必须照搬的完整目录模板；下面的命名和分包是本项目的工程选择。

参考：[Go 模块组织建议](https://go.dev/doc/modules/layout)、[Gin API 设计模式](https://gin-gonic.com/en/docs/routing/api-design/)。

## API 仓库

```text
agentic-kit-api/
├── cmd/
│   ├── api/main.go            # 启动、依赖装配、信号与退出
│   └── migrate/main.go        # 数据库初始化入口
├── internal/
│   ├── config/               # TOML 配置加载与校验
│   ├── httpapi/              # HTTP 总装配与全局中间件
│   │   ├── router.go         # 挂载业务模块
│   │   ├── middleware.go     # 鉴权、日志、Recovery
│   │   └── *_test.go         # 通过完整路由验证对外契约
│   ├── httpx/                # JSON 绑定、错误响应、身份读取、分页
│   ├── agent/
│   │   ├── router.go         # Agent 路由及配置写权限
│   │   ├── controller.go     # 草稿、发布、版本查询
│   │   └── service.go        # 消费方定义的业务接口
│   ├── session/
│   │   ├── router.go         # Session 路由
│   │   ├── controller.go     # 会话、发送消息、历史与运行记录
│   │   ├── headers.go        # HTTP header 绑定与校验
│   │   └── service.go        # 消费方定义的业务接口
│   └── run/
│       ├── router.go         # Run 路由
│       ├── controller.go     # 查询、取消、澄清与人工确认
│       ├── events.go         # SSE 回放及订阅
│       └── service.go        # 消费方定义的业务接口
├── sdk/                      # 对外 HTTP 契约与 Resty 客户端
│   ├── agent.go              # Agent 请求与响应类型
│   ├── session.go            # Session、消息、分页参数
│   ├── run.go                # Run、事件、澄清与确认参数
│   ├── client.go             # 构造并装配三个业务接口
│   ├── transport.go          # Resty 请求、响应和错误转换
│   ├── *_api.go              # AgentAPI、SessionAPI、RunAPI
│   └── *_client.go           # 私有业务实现
├── pkg/
│   └── durable/              # API 与 worker 共用的业务事务
│       ├── agent_service.go  # Agent 草稿校验、并发编辑与发布
│       ├── session_service.go # 会话、消息、任务提交与配置快照
│       ├── run_service.go    # 领取、续期、恢复与完成事务
│       ├── store.go          # GORM 连接、迁移与存储读取
│       ├── models.go         # GORM 表结构、约束与索引
│       ├── types.go          # 共享执行契约
│       ├── events.go         # Outbox 发布
│       └── *_test.go
├── configs/                  # 配置样例，与 Go 配置代码分开
├── deploy/                   # 本地部署配置
├── docs/
├── go.mod
└── Makefile
```

## Worker 仓库

```text
agentic-kit-worker/
├── cmd/worker/main.go        # 启动、配置与信号处理
├── internal/
│   ├── config/              # Viper TOML 配置加载与校验
│   ├── engine/              # Eino Runner、checkpoint 存储、工具边界、领取与续期
│   └── adapters/            # Eino 模型与 HTTP/AppForge 工具接入
├── configs/                 # TOML 配置样例
├── go.mod
└── Makefile
```

cmd 可以导入本仓库 internal。worker 只从 API 仓库导入 pkg/durable，不允许导入 API 的 internal/config 或 internal/httpapi。跨仓库恢复测试构建并启动真实 API 程序，通过 HTTP 提交和读取任务，不突破 internal 边界。

pkg 是本项目对共享包的显式标记，不是 Go 编译器的特殊目录；internal 的导入限制由 Go 工具链执行。暂时由 API 仓库管理共享包版本，与现有两个仓库的约束一致。

## 分层规则

装配方向为 `cmd/api → httpapi → agent/session/run`。三个 HTTP 模块各自定义 Service 接口，仅声明 controller 使用的方法；httpapi 将 durable.Store 注入这些接口。业务 HTTP 模块还依赖 httpx；httpx 不导入业务 HTTP 模块。httpapi 只装配路由与全局中间件，不保存所有业务共享的 controller。

每个业务模块维护自己的 router 和 controller。对外请求与响应统一定义在根目录 sdk，controller 直接使用；参数绑定和校验由服务端负责。sdk 的 Go 客户端复用这些类型，其他应用不需要导入 durable。controller 处理 HTTP 参数、状态码和 Gin Context。Session 负责发送消息，即使该请求会创建 Run；Run 负责执行状态与事件订阅。Agent 的配置写权限与 Agent 路由放在一起。

实际业务逻辑按 agent_service.go、session_service.go、run_service.go 组织在 durable 包中。它们直接维护 GORM 事务，复用同一个 Store；store.go 只保留连接与迁移等存储职责。用户消息、Run、执行消息和 Outbox 需要原子提交，不能按目录拆成多个各自提交的事务。worker 使用同一套执行规则。

Service 接口对应现有 durable.Store 的事务行为，不额外建立纯转发实现。跨模块事务有独立存储或独立发布需求时，再调整共享包边界。HTTP 模块之间不相互调用 controller，context.Context 沿调用链传递。

SDK 不依赖服务端实现。durable 通过类型别名复用公开配置、消息和状态类型，保留内部执行快照、租约、步骤与 GORM 模型；HTTP 返回 Run 时显式转换为 sdk.Run。

SDK 的 Client 是业务入口集合，Agents、Sessions、Runs 分别持有 AgentAPI、SessionAPI、RunAPI。私有实现共享一个 Resty 客户端；每次调用创建独立请求，context 和结果对象不共享。调用方可注入对应接口；只使用其中少数方法时，可在调用方定义更小的接口。Resty 类型不暴露在 SDK 的公开签名中。
