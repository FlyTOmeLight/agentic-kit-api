# Agentic Kit API 项目约定

## 目录与依赖

- 调整目录、包边界或跨仓库依赖前，读取 [项目布局](docs/layout.md)。`cmd/` 负责入口与依赖装配，`internal/config/` 负责配置加载，`internal/httpapi/` 负责总路由与中间件，`internal/agent/`、`internal/session/`、`internal/run/` 分别维护业务 HTTP 接口，`configs/` 保存配置样例。
- 对外 HTTP 请求、响应和 Go 客户端维护在根目录 `sdk/`；修改 HTTP 契约时同步 SDK 和契约测试。SDK 按 AgentAPI、SessionAPI、RunAPI 提供接口，私有实现使用 Resty；不导入 internal、durable、Gin 或 GORM。各 HTTP 模块在消费方定义 Service 接口，由装配层注入实现；绑定与服务端校验留在 internal。
- `pkg/durable/` 是供本仓库和相邻 worker 仓库共用的任务状态与存储契约。变更其公开接口或持久化格式时，同时检查 worker 的调用与恢复行为；避免让该包依赖 Gin 或 API 的 internal 包。
- 表结构、约束和索引统一维护在 `pkg/durable/models.go`，由 `cmd/migrate` 调用 GORM AutoMigrate；修改模型时验证已有数据上的重复迁移。项目目录说明集中在 docs/layout.md，具体启动方式以 README.md 为准。

## 执行与接口契约

- 修改任务状态、领取、续期、步骤存档或事件发布前，读取 [恢复设计](docs/design.md) 并核对实现；修改 HTTP 行为时同步 [接口契约](docs/api.md) 和相关测试。
- Agent 发布版本不可变；任务使用提交时的配置快照。修改配置、会话或消息行为时同步 docs/api.md，并验证发布后重试、多轮历史与 owner 隔离。
- 用户消息与任务同事务创建；执行消息、状态、结果和完成事件在同一数据库事务中提交；worker 的状态写入必须受执行权和 epoch 校验保护。Redis 是通知加速层，已提交事件从数据库回放。
- 原系统功能迁移情况以 [迁移清单](docs/migration.md) 为准，不能把新执行主链路视为原服务的完整替代。

## 验证

- 检查命令以 Makefile 为准。涉及持久化、任务状态、SSE 或跨进程行为时，配置隔离测试数据库和 Redis，执行 `make integration`；具体环境变量和准备方式见 README.md。
- `pkg/durable/` 的变更影响 worker 时，同时在相邻 worker 仓库验证相关测试；涉及故障接管时保留跨进程恢复验证。
