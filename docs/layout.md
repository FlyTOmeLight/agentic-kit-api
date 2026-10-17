# 项目布局与依赖约束

当前功能和各层职责见 [README](../README.md#功能与代码分层)。本文件维护目录、装配、HTTP/SDK 和共享持久化的现行约束；后续重构目标见 [框架重构设计](refactoring-design.md)，不能将目标当作已经实现。

## 装配与资源所有权

依赖方向为 `cmd/server → app.Run → service.Build → internal/app`。入口负责信号和退出码；Build 初始化数据库与通知缓存并交给 Runtime。组合应用通过 `service.New` 注入 Store、Redis 和 logger，不调用 Build 重复建立连接。

- 构造时创建 Controller，RegisterRoutes 只安装已有 handler；配置更新不创建资源或后台任务。
- Start 使用运行 context，只启动通用 Outbox Relay。Stop 取消并等待任务退出，调用方在停止成功后关闭借用资源。
- HTTP 先排空，再停止任务，最后关闭资源。任务停止超时时，在其真正退出前保留依赖。
- db.Module 拥有独立应用的数据库连接。durable.New 借用数据库句柄，Store.Close 不关闭借用池；durable.Open 为独立 worker 创建并拥有连接。
- 通知缓存使用 server-go `redis.Config`、`redis.NewRedis` 和 UniversalClient，生命周期由 Runtime 管理。Ping 失败保留客户端重连，允许 Redis 离线启动和数据库事件回放；初始化 context 取消时返回错误。

类型化配置与启动校验在 `internal/config`，不依赖 HTTP 业务模块。配置不装配场景服务客户端、不维护模板或账号凭据清单。默认配置见 [config.toml](../conf/server/config.toml)。

## HTTP 与业务职责

Controller 负责绑定、校验、响应和 Swagger，复用 SDK 请求/响应类型。JSON、URI、Query 一起绑定；Header 使用独立对象绑定后赋值，避免覆盖路径或正文，见 [来源隔离经验](lessons/server-go-migration.md#请求绑定要保留字段来源)。

Agent、Session、Run 通过消费方接口调用共享事务。数据库使用 `ctx.Request.Context()`；durable、后台任务和公开 SDK 使用标准 context，不持有会被 HTTP 框架复用的 Context。现有单次事务直接调用，不增加纯转发 Service。

项目工作台、成员权限等级、模板、dataset、评测和 Coding 发布审批属于调用方业务。本仓库不实现这些路由、类型、表模型或出站检查。Execution Scope 中的 tenant/project/actor/task 是调用方提供的执行上下文，用于会话隔离和快照，不触发项目业务查询。

请求日志、Recovery、404/405、错误渲染、binder 与 SSE 传输复用 server-go。身份和鉴权后续由 forge-tenant-api middleware 提供，当前未接入；不在本仓库维护第二套静态 token、会话或 CSRF 校验。

修改 HTTP 行为时同步 [接口契约](api.md)、SDK 和调用方。Controller 注释与 SDK 标签是文档来源，通过 `make doc` 生成 `doc/openapi`，不直接编辑生成文件。

## 共享持久化边界

`internal/table` 是 GORM 表模型、表名、约束和索引的唯一来源，当前仅包含 Agent、AgentVersion、Session、Run、Step、Event、Message。模型集合集中在 `internal/table/migrate.go`，Store.Migrate 在持有锁的事务中调用，供初始化及测试使用。API 与 worker 启动不自动迁移；本仓库不提供迁移 CLI 或 Helm 迁移任务。已有平台表不在本次清理中执行 DROP 或改写。

表模型可以复用 SDK 领域类型，不依赖 durable 或 HTTP。查询和业务事务位于 `pkg/durable`，使用 table 模型并显式转换公开对象；不对外暴露私有表类型或保留旧模型副本。

`pkg/durable` 的任务、租约、步骤、事件、执行快照与 Store 供 API 和 worker 共用，不依赖 Gin 或 HTTP 模块。公开契约或持久化格式变化时核对 worker 的真实调用与恢复行为。执行权、epoch 和一致提交见 [恢复设计](design.md)。

## SDK 与跨仓库依赖

公开路径为 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。SDK 提供通用 Agent、Session、Run、Execution，不包含 ProjectClient、workspace、dataset、评测及 Coding 专用发布类型，也不导入 internal、durable、Gin 或 GORM。

普通操作接收标准 context 和请求指针；路径、Query、Header、JSON 分别编码。客户端所有者负责 Close，流所有者关闭响应体。业务应用通过公开 API/SDK 组合通用能力；本项目不反向依赖 forge-tenant-api 或 forge-coding-api 的业务客户端。

临时 workspace 只用于联合开发；交付验证关闭 workspace 并使用 go.mod 中的正式依赖。共享包修改后按实际受影响契约验证 worker，不能把本地 workspace 测试当作公开依赖已经交付。
