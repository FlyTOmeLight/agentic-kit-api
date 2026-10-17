# 项目布局与依赖约束

当前功能和各层职责见 [README 的分层说明](../README.md#功能与代码分层)。本文维护修改目录、装配、HTTP/SDK 或共享包时需要遵守的项目约束。框架调用以 go.mod 选定的 server-go 版本为准。

## 装配与资源所有权

依赖装配方向为 `cmd/server → app.Run → service.Build → internal/app`。入口负责信号和退出码；独立 Build 初始化连接并交给 Runtime，组合应用通过公开 `service.New` 注入已有 Store 和 Redis，不重复调用子应用 Build。

- 构造时创建 Controller 和平台业务 Service，由装配层注入。RegisterRoutes 只安装既有 handler；配置更新不重新构造对象或启动后台任务。
- Start 使用框架提供的运行 context，Stop 取消并等待所属任务退出。初始化 context 只用于装配；未交给 Runtime 的部分资源在构造失败时清理。
- HTTP 先排空，再停止任务，最后关闭资源。Stop 失败或超时后，在任务真正退出前保留依赖。
- 框架 db.Module 拥有独立应用的数据库连接。durable.New 借用数据库句柄，Store.Close 不关闭借用的池；durable.Open 为独立 worker 创建并拥有连接。
- API 拥有 Tenant/Coding 客户端，在任务停止后关闭。通知缓存由 Runtime 接管，使用 server-go `redis.Config` 和 `redis.NewRedis`；Ping 失败时保留客户端重连，允许 Redis 不可用时启动和从数据库回放事件；原因及验证见 [资源经验](lessons/resource-and-recovery.md)。

类型化配置与业务校验在 `internal/config`，运行时配置加载由框架完成。HTTP 路径和 SSE 超时限制属于项目契约；配置字段和启动方式以 [默认配置](../conf/server/config.toml) 及 [README](../README.md#配置与启动行为) 为准。

## HTTP 与业务职责

Controller 负责请求绑定、校验、响应和 Swagger，复用 SDK 请求/响应类型。JSON、URI、Query 在同一请求中绑定；Header 使用独立对象再赋值，避免覆盖路径或正文。不要把包含正文、URI 的整个对象再次传给 Header 绑定，见 [来源隔离经验](lessons/server-go-migration.md)。

平台 Service 封装权限、数据集冻结、评测及发布门槛，使用 `core.Context` 获取身份和请求信息。其 Store 接口与 Agent、Session、Run、Evaluation 的消费方接口用标准 context 调用共享事务；已有单次事务直接调用，不增加纯转发 Service。领域包不调用其他领域的 Controller。

数据库与 SDK 调用使用 `ctx.Request.Context()`；长期任务、durable 和公开 SDK 使用标准 context，不持有会被 HTTP 框架复用的 Context。外部协议和连接适配属于 `internal/client`，不依赖 Controller。

通用请求日志、Recovery、404/405、错误渲染、Bearer 鉴权基础能力、binder 与 SSE 传输复用 server-go。本项目保留领域权限、错误到状态的映射、SSE 游标与数据库事件回放。

修改 HTTP 行为时同步 [接口契约](api.md)、SDK 和相关调用方；Controller 及 SDK 标签是文档来源，通过 `make doc` 重生成 `doc/openapi`，不直接编辑生成产物。

## 共享持久化边界

`internal/table` 是 GORM 表模型、表名、约束和索引的唯一来源。模型集合集中在 `internal/table/migrate.go`，由 durable.Store.Migrate 在持有迁移锁的事务中调用；API 和 worker 启动不自动迁移。变更模型后验证已有数据上的重复迁移。

表模型可以复用 SDK 的领域类型，不依赖 durable 或 HTTP。查询和业务事务位于 `pkg/durable`，使用 table 模型并显式转换为公开对象；不对外暴露私有表类型或保存旧模型副本。

`pkg/durable` 的任务、租约、步骤、事件、执行快照与 Store 供 API 和独立 worker 共用，不依赖 Gin 或 HTTP 模块。公开接口或持久化格式变更时检查 worker 的真实调用和恢复行为；状态、执行权、epoch 和一致提交约束见 [恢复设计](design.md)。

worker 只直接导入公开包，不能直接导入本仓库 internal。公开 durable 内部依赖 internal/table 符合 Go 的 internal 导入限制。`pkg/evaluation` 则供 CLI 和持久实验共用套件校验、评分与比较。

## SDK 与跨仓库依赖

公开路径为 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。请求、响应、接口和客户端按领域拆文件；SDK 不导入本项目 internal、durable、Gin 或 GORM。稳定 HTTP 类型集中维护在 SDK，持久化模型与其分别演进，通过显式映射转换。

普通操作接收 `context.Context` 和请求指针；路径、Query、Header 与 JSON 按协议分别编码。客户端所有者负责 Close，流所有者关闭响应体。普通 JSON 通过 sdk.Complete 完成错误处理，流式失败使用 sdk.DecodeError；调用示例与错误语义见 [SDK README](../sdk/go/agenticv1/README.md)。

Coding 适配引用 forge-coding-api 根模块中的 `sdk/go/codingv1`，不维护一份 Coding HTTP 协议。版本只在 go.mod/go.sum 固定。临时 workspace 用于 API/worker 联合开发；交付验证关闭 workspace 并使用正式依赖，具体经验见 [框架迁移经验](lessons/server-go-migration.md)。
