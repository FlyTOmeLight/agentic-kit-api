# Agentic Kit API 文档路由

全局协作与工程规则沿用 `~/.codex/AGENTS.md`。本文件只指定本项目修改前应读取的文档；当前行为以代码、配置与契约为准。

| 修改场景 | 先读取 |
| --- | --- |
| 了解当前功能、分层、启动、配置、部署或验证命令 | [README](README.md) |
| 调整目录、模块职责、装配、连接所有权、SDK 或跨仓库依赖 | [布局与依赖约束](docs/layout.md) |
| 修改领域术语或核对概念关系 | [领域术语](CONTEXT.md)，行为与取舍仍查对应设计及契约 |
| 评审或实施通用 Agent 框架重构 | [框架重构设计](docs/refactoring-design.md)，先核对范围和状态；当前契约仍查 api/design/layout |
| 维护现行 HTTP 请求/响应、鉴权及领域接口 | [接口契约](docs/api.md)、[SDK README](sdk/go/agenticv1/README.md)；同步对应契约与调用方 |
| 修改 durable 状态、领取、续期、步骤存档、完成事务或事件发布 | [恢复设计](docs/design.md)、[项目经验索引](docs/lessons/README.md)；按 README 验证 worker 与跨进程恢复 |
| 修改表模型、约束、索引或迁移 | [布局的持久化约束](docs/layout.md#共享持久化边界)、[恢复设计](docs/design.md)、[验证要求](README.md#验证与部署) |
| 升级依赖、调整 binder、配置加载或框架生命周期 | [项目经验索引](docs/lessons/README.md)，再读取相关框架版本的说明 |
| 实施首版 Memory 或上下文构建 | [框架设计](docs/refactoring-design.md#66-上下文管线与-memory-基础版)、[Memory设计](docs/memory-design.md)；涉及artifact接入先核对其中的权限、版本及跨服务提交前提 |
| 查找历史验证证据 | [验证记录](docs/verification.md)，其中旧路径与版本不作为当前约定 |

新增经验写入 `docs/lessons/`，附问题适用范围与代码/测试证据，并更新该目录索引。功能、契约和布局变化更新各自文档；本文件仅在文档职责或路由变化时更新。
