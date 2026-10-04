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
│   │   └── request.go        # Agent 请求 DTO
│   ├── session/
│   │   ├── router.go         # Session 路由
│   │   ├── controller.go     # 会话、发送消息、历史与运行记录
│   │   └── request.go        # Session 请求 DTO
│   └── run/
│       ├── router.go         # Run 路由
│       ├── controller.go     # 查询、取消、澄清与人工确认
│       ├── request.go        # Run 请求 DTO
│       └── events.go         # SSE 回放及订阅
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
│   ├── engine/              # 可恢复执行循环、领取、续期、故障测试
│   └── adapters/            # Eino 模型与 HTTP/AppForge 工具接入
├── configs/                 # TOML 配置样例
├── go.mod
└── Makefile
```

cmd 可以导入本仓库 internal。worker 只从 API 仓库导入 pkg/durable，不允许导入 API 的 internal/config 或 internal/httpapi。跨仓库恢复测试构建并启动真实 API 程序，通过 HTTP 提交和读取任务，不突破 internal 边界。

pkg 是本项目对共享包的显式标记，不是 Go 编译器的特殊目录；internal 的导入限制由 Go 工具链执行。暂时由 API 仓库管理共享包版本，与现有两个仓库的约束一致。

## 分层规则

依赖方向为 `cmd/api → httpapi → agent/session/run → durable`。业务 HTTP 模块还依赖 httpx；httpx 不导入业务 HTTP 模块。httpapi 只装配路由与全局中间件，不保存所有业务共享的 controller。

每个业务模块维护自己的 router、controller 和请求 DTO。controller 处理 HTTP 参数、状态码和 Gin Context。Session 负责发送消息，即使该请求会创建 Run；Run 负责执行状态与事件订阅。Agent 的配置写权限与 Agent 路由放在一起。

实际业务逻辑按 agent_service.go、session_service.go、run_service.go 组织在 durable 包中。它们直接维护 GORM 事务，复用同一个 Store；store.go 只保留连接与迁移等存储职责。用户消息、Run、执行消息和 Outbox 需要原子提交，不能按目录拆成多个各自提交的事务。worker 使用同一套执行规则。

当前不在三个 HTTP 模块中再创建一层纯转发 service。跨模块事务有独立存储或独立发布需求时，再调整共享包边界。HTTP 模块之间不相互调用 controller，context.Context 沿调用链传递。
