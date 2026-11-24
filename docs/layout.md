# 项目布局与依赖约束

当前功能和各层职责见 [README](../README.md#功能与代码分层)。本文件维护目录、装配、HTTP/SDK 和共享持久化的现行约束；后续重构目标见 [框架重构设计](refactoring-design.md)，不能将目标当作已经实现。

## 装配与资源所有权

依赖方向为 `cmd/server → app.Run → service.Build → internal/app`。入口负责信号和退出码。Build 初始化数据库、通知缓存、Agent artifact 与 Forge 鉴权客户端，由 Runtime 管理关闭。组合应用通过 `service.New` 注入 Store、Redis、Agent artifact、Authentication、logger 与可选的 Memory 归档客户端。

- 构造时创建 Controller，RegisterRoutes 只安装已有 handler；配置更新不创建资源或后台任务。
- Start 使用运行 context 启动通用 Outbox Relay；配置 archive 时同时启动 Memory Pending 恢复。Stop 取消并等待这些任务退出，调用方在停止成功后关闭借用资源。
- HTTP 先排空，再停止任务，最后关闭资源。任务停止超时时，在其真正退出前保留依赖。
- db.Module 拥有独立应用的数据库连接。durable.New 借用数据库句柄，Store.Close 不关闭借用池；durable.Open 为独立 worker 创建并拥有连接。
- 通知缓存使用 server-go `redis.Config`、`redis.NewRedis` 和 UniversalClient，生命周期由 Runtime 管理。Ping 失败保留客户端重连，允许 Redis 离线启动和数据库事件回放；初始化 context 取消时返回错误。
- Build 在配置 Artifact Endpoint 时创建并拥有 archive 客户端；New 或 AddService 失败会关闭它，装配成功后由 API 在停止任务成功后关闭一次。Stop 超时时保留客户端，后续成功停止才关闭。`service.New` 借用调用方的 archive，不关闭该资源。

类型化配置与启动校验在 `internal/config`，不依赖 HTTP 业务模块。配置不装配场景服务客户端、不维护模板或账号凭据清单。默认配置见 [config.toml](../conf/server/config.toml)。

## HTTP 与业务职责

Controller 负责绑定、校验、响应和 Swagger，复用 SDK 请求/响应类型。JSON、URI、Query 一起绑定；Header 使用独立对象绑定后赋值，避免覆盖路径或正文，见 [来源隔离经验](lessons/server-go-migration.md#请求绑定要保留字段来源)。

Agent、Session、Run 通过消费方接口调用共享事务。Memory 的人工维护、操作回执、发布编排和本地召回位于 `internal/app/memory`；领域纯值、校验和词项匹配位于 `pkg/memory`，artifact 出站适配位于 `internal/client`。`pkg/durable/prepare.go` 通过注入的 `RecallTx` 在接受事务内读取本地投影并冻结，不执行远端 HTTP。数据库使用 `ctx.Request.Context()`；durable、后台任务和公开 SDK 使用标准 context，不持有会被 HTTP 框架复用的 Context。

项目工作台、成员权限等级、模板、dataset、评测和 Coding 发布审批属于调用方业务。本仓库不实现这些路由、类型、表模型或出站检查。Execution Scope 中的 tenant/project/actor/task 是调用方提供的执行上下文，用于会话隔离和快照，不触发项目业务查询。

请求日志、Recovery、404/405、错误渲染、binder 与 SSE 传输复用 server-go。身份和鉴权已接入 forge-tenant-api v0.13.0 middleware。用户路由要求已验证租户，middleware 将 tenant/project 写入请求的 `access.Scope`，路由末端再次拒绝空租户；内部 execution/goal/lease 使用服务白名单控制的 propagation，同样强制租户 scope。sandbox 使用独立服务白名单，申请的 tenant/owner 仍来自可信请求体；callback 默认不注册，启用需同时配置签名密钥、来源和服务白名单。不在本仓库维护第二套静态 token、会话或 CSRF 校验。

修改 HTTP 行为时同步 [接口契约](api.md)、SDK 和调用方。Controller 注释与 SDK 标签是文档来源，通过 `make doc` 生成 `doc/openapi`，不直接编辑生成文件。

## 共享持久化边界

`internal/table` 是 GORM 表模型、表名、约束和索引的唯一来源，包括 Run、Goal、Sandbox、MemoryEntry 和 MemoryOperation。`internal/schema.Init` 在持有 advisory lock 的事务中对这些模型执行 AutoMigrate，`service.Build` 在接收请求前调用。公开的 `durable.Store.InitSchema` 供测试与联合验证使用。初始化面向空库或空 schema。旧库的租户归属回填、版本正文迁移和索引调整由部署方另行处理。

Memory 的主题部分唯一索引为 `memory_entry_subject_identity`，约束完整 scope、agent、kind 和非空 subjectKey。空主题不参与唯一性检查。

表模型可以复用公开领域类型，不依赖 durable 或 HTTP。执行查询和事务位于 `pkg/durable`，Memory 查询和事务位于 `internal/app/memory`，均使用 table 模型并显式转换公开对象；不对外暴露私有表类型或保留旧模型副本。MemoryOperation 保存 2 分钟 archive claim；事务外远端调用总预算为 1 分钟，绑定和版本写回要求 claim token 仍有效，完成或出错时释放。配置归档时，API 启动后立即恢复一轮 Pending，之后每 30 秒一轮，单轮最多 20 条、预算 2 分钟；仅继续 Create/Update/Restore，不自动重试 Rejected，停机时未完成项保持 Pending。当前不提供常驻运维 Pending 列表。
HTTP 用户与 propagation 请求通过已验证的 `access.Scope` 限制 tenant，再结合 owner 校验对象归属。`table.TenantQuery` 在 context 有 scope 时约束 `tenant_id`；可信 worker 直接调用 Store 时无 HTTP scope 的既有契约仍保留，不能据此省略 HTTP 末端的租户校验。

表模型可以复用 SDK 领域类型，不依赖 durable 或 HTTP。Agent 的单表查询位于 `internal/table/agent_query.go`，业务事务位于 `internal/app/agent`；其已发布正文通过 `internal/client/artifact.go` 保存到 artifact-go，本地 `agent_version` 只保存版本号与远端引用。执行查询和一致性事务位于 `pkg/durable`，使用 table 模型并显式转换公开对象；不对外暴露私有表类型或保留旧模型副本。

`internal/app/execution` 在执行事务外校验 Agent 归属并预取发布配置，再调用 durable 的 `Accept` 与准备回调冻结 Run。回调在事务中只读取会话历史，不调用 artifact；远端 SDK 类型只允许出现在 `internal/client`。

`pkg/durable` 的任务、租约、步骤、事件、执行快照与 Store 供 API 和 worker 共用，不依赖 Gin 或 HTTP 模块。执行状态写入按职责拆分在 `lease.go`、`boundary.go`、`step.go`、`interaction.go`、`cancellation.go`、`retry.go`；等待交互由 Run 的 `waiting_step`/`waiting_kind` 列显式保存。公开契约或持久化格式变化时核对 worker 的真实调用与恢复行为。执行权、epoch 和一致提交见 [恢复设计](design.md)。

## SDK 与跨仓库依赖

公开路径为 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。SDK 提供通用 Agent、Session、Run、Execution、Memory 人工维护、版本历史与执行时显式召回选项，不包含 ProjectClient、workspace、dataset、评测及 Coding 专用发布类型，也不导入 internal、durable、Gin 或 GORM。

普通操作接收标准 context 和请求指针，分别编码路径、Query、Header、JSON。客户端所有者负责 Close，流所有者关闭响应体。业务应用通过公开 API/SDK 组合通用能力。Forge 鉴权 SDK 仅在 `internal/client` 适配。`Build` 将 API 自身的 `Tenant.ServiceAPIKey` 注入 artifact 客户端，适配器使用它传播已核验的 user/tenant/project，个人归属固定为目标用户。缺少 tenant 或 actor 时拒绝远端调用。当前使用 artifact-go `v0.5.2`，独立构建及联调范围见 [验证记录](verification.md#2026-11-17-agent-版本发布与-run-快照)。

临时 workspace 只用于联合开发；交付验证关闭 workspace 并使用 go.mod 中的正式依赖。共享包修改后按实际受影响契约验证 worker，不能把本地 workspace 测试当作公开依赖已经交付。

当前 archive 适配使用 artifact-go `v0.5.2`，框架依赖 server-go `v0.17.1`。artifact SDK 仅允许 `internal/client` 引用；`pkg/memory`、durable、contextbuilder、worker 和公开 SDK 不暴露其类型。Memory 集成测试通过 artifact 的 `service` 与 `tests/fixture` 启动真实 HTTP 服务及隔离数据库；这些导入只用于测试。本地联合验证已覆盖认证、隔离与发布结果未知后的恢复，外部部署验收尚未完成，范围见[验证记录](verification.md#memory-真实-artifact-联合验证)。
