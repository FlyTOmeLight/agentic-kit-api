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
│   ├── config/               # 配置加载与校验，仅 API 使用
│   └── httpapi/              # Gin HTTP 层
│       ├── router.go         # 路由与依赖装配
│       ├── middleware.go     # 鉴权、日志、Recovery
│       ├── request.go        # 请求 DTO 与 binding
│       ├── response.go       # 错误响应
│       ├── runs.go           # 任务 handler
│       ├── events.go         # SSE
│       └── router_test.go
├── pkg/
│   └── durable/              # API 与 worker 共用的任务状态与事务契约
│       ├── types.go
│       ├── store.go
│       ├── schema.sql        # 与存储包一起 embed
│       ├── events.go         # Outbox 发布
│       └── store_test.go
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
│   ├── engine/              # 可恢复执行循环、领取、续期、故障测试
│   └── adapters/            # Eino 模型与 HTTP/AppForge 工具接入
├── go.mod
└── Makefile
```

cmd 可以导入本仓库 internal。worker 只从 API 仓库导入 pkg/durable，不允许导入 API 的 internal/config 或 internal/httpapi。跨仓库恢复测试构建并启动真实 API 程序，通过 HTTP 提交和读取任务，不突破 internal 边界。

pkg 是本项目对共享包的显式标记，不是 Go 编译器的特殊目录；internal 的导入限制由 Go 工具链执行。暂时由 API 仓库管理共享包版本，与现有两个仓库的约束一致。

## 分层规则

HTTP 参数、状态码和 Gin Context 留在 internal/httpapi；任务状态变化、数据库并发控制和事务边界集中在 pkg/durable；模型和工具执行在 worker/internal/engine。context.Context 沿调用链传递。

当前不添加仅转发一次调用的 service/repository 层，也不创建空的 models、utils、common 目录。未来新增业务职责时，在职责实际出现的位置拆包；schema.sql 与 embed 所属包保持在一起，避免目录调整破坏构建。
