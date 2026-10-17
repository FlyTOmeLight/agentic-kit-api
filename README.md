# Agentic Kit API

管理 Agent 草稿、不可变发布版本、会话、消息与持久化执行任务。模型推理与工具执行由独立的 `agentic-kit-worker` 完成；API 与 worker 共用 PostgreSQL 和本仓库的 durable 契约，可分别部署和扩容。

本仓库只提供通用 Agent 能力。项目工作台、项目权限政策、产品模板、数据集、评测实验和场景发布审批由调用方应用负责，不在 API 中实现。执行请求可以携带 tenant、project、actor、task 等上下文，核心只据此隔离任务和保存快照，不查询或管理项目业务。

当前执行链路支持 worker 重启后接管已提交任务。Memory 的[技术设计](docs/memory-design.md)已提出复用 artifact-go 版本服务，仍待实施。[通用框架重构设计](docs/refactoring-design.md)中的访问对象、新表结构和上下文管线也仍待实施；本次删除平台业务不代表整套重构已经完成。

## 功能与代码分层

| 功能 | 当前负责的内容 |
| --- | --- |
| Agent | 创建和编辑草稿、revision 冲突检查、发布不可变版本 |
| Session / Message | 多轮会话、有序消息、冻结提交时的配置与历史、消息幂等 |
| Run / Execution | 提交、查询、取消、澄清回答、人工确认、执行权校验、trace 与 SSE 回放 |
| Durable / Outbox | API 与 worker 共享执行事务、租约、步骤存档、状态与事件一致提交、通知投递 |

```text
agentic-kit-api/
├── cmd/server/main.go        # 服务入口：信号、配置路径、app.Run
├── service/                  # 公开构造、资源装配、HTTP 路由与 Outbox 生命周期
├── internal/
│   ├── app/
│   │   ├── agent/            # Agent HTTP 接口与消费方事务接口
│   │   ├── session/          # 会话、消息与提交接口
│   │   ├── run/              # 执行、租约、取消、人工恢复、trace 与 SSE
│   │   └── apperror/         # 领域错误到 HTTP 错误的映射
│   ├── config/               # 类型化配置与启动校验
│   └── table/                # Agent、Session、Run、Step、Event、Message 表模型
├── pkg/durable/              # API/worker 共享持久化事务与执行契约
├── sdk/go/agenticv1/          # 通用 HTTP 请求/响应、Go SDK 与 SSE 消费
├── doc/openapi/              # 由 Controller 与 SDK 生成的 OpenAPI
├── conf/server/config.toml   # 默认配置与环境占位符
├── examples/agents/          # Agent 配置样例
├── scripts/publish-agent.py  # 调用通用 Agent API 的发布脚本
└── charts/agentic-kit-api/    # API、PostgreSQL 与 Redis 部署模板
```

数据库操作与跨域事务位于 `pkg/durable`，Controller 负责绑定和响应；表模型仅在 `internal/table` 维护。独立服务的 `service.Build` 创建连接并交给 server-go Runtime；组合应用通过 `service.New` 注入已有资源，所有者在服务停止后关闭连接。

## 配置与启动

```bash
GOWORK=off go run ./cmd/server -config conf/server/config.toml
```

通过 `-config`、`CONFIG_FILE`、`SERVER_CONFIG_PATH` 依次选择配置路径，未指定时读取 `conf/server/config.toml`。环境变量通过 TOML 中的 `${env:NAME:default}` 占位符映射，普通常量不被同名环境变量覆盖。

当前只需 HTTP、数据库和 Redis 配置。常用环境变量为 `DATABASE_URL`、`REDIS_ADDR`、`LISTEN_ADDR`。`[Redis]` 使用 server-go `redis.Config`，以 `Addrs` 和 `DB` 配置连接；Redis 离线不阻止启动，事件仍从数据库回放。模型与工具凭据由 worker 环境提供，快照仅保存环境变量引用。

API 不维护静态 token、config owner 白名单、Tenant/Coding 业务客户端或对应连接配置。请求身份与鉴权后续通过 forge-tenant-api middleware 接入；当前尚未接入，业务路由没有身份校验。`auth.UserID(c)` 是现有身份读取点，持久化对象按 owner 隔离。

API 启动不自动建表，也不提供 `eval`、`migrate` 命令。部署方须在接收业务请求前准备数据库；共享 `Store.Migrate` 仍供表结构初始化和集成测试调用，不是独立迁移产品或部署命令。新结构自动初始化属于后续框架重构，本次不改变已有数据库内容。

server-go HTTPHost 提供 `/ping`、`/healthz`、`/version` 探针；开启 `HTTPServer.SwaggerEnable` 可查看 `/swagger/`。SSE 要求 `HTTPServer.WriteTimeout` 为负数，`APIBasePath` 为空。

## 调用接口

下面的请求创建并发布 Agent，再创建会话和发送消息。身份 middleware 接入后的凭据由接入方配置；本服务不解析示例之外的本地 token 清单。

```bash
export API_URL='http://127.0.0.1:8080'
agent_id=$(curl --fail-with-body -sS "$API_URL/v1/agents" \
  -H 'Content-Type: application/json' \
  --data-binary @examples/agents/agents.example.json | jq -r .id)
curl --fail-with-body -sS "$API_URL/v1/agents/$agent_id/publish" \
  -H 'Content-Type: application/json' -d '{"revision":1}'
session_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg agentID "$agent_id" '{agentID:$agentID,title:"第一次对话"}')" | jq -r .id)
run_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions/$session_id/messages" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: question-001' \
  -d '{"query":"请介绍一下你自己。"}' | jq -r .id)
curl -N "$API_URL/v1/runs/$run_id/events"
```

继续对话时使用相同 session_id、新的 Idempotency-Key 和 query。编辑 Agent 使用 `PUT /v1/agents/{id}`，传完整 name/config 和当前 revision；发布后新任务使用新版本，已有任务仍使用冻结的原版本。完整请求、幂等、执行上下文与人工恢复见 [HTTP 契约](docs/api.md)。

Go SDK 导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`，提供 Agents、Sessions、Runs、Executions。所有操作接收标准 context 和请求指针，路径、查询、Header 与 JSON 分别编码；调用方负责关闭客户端和事件流。使用示例见 [SDK README](sdk/go/agenticv1/README.md)。

## 验证与部署

常用命令为 `make test`、`make vet`、`make build`；`make doc` 从 Controller/SDK 生成 OpenAPI，`make install-doc` 安装文档工具。涉及持久化、SSE 或生命周期时，使用隔离 PostgreSQL/Redis：

```bash
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export TEST_REDIS_URL='redis://127.0.0.1:56379/0'
GOWORK=off make integration
GOWORK=off CGO_ENABLED=0 make build
```

数据库测试创建随机 schema 并清理，账户需有建 schema 权限；Redis 测试使用独立 run key。未配置外部依赖导致用例跳过，不代表集成验证通过。共享执行契约变化仍须联合验证 worker；涉及接管时保留真实跨进程恢复测试。

Dockerfile 只构建并打包 server 和默认配置，依赖下载使用 BuildKit secret `github_netrc`。Helm 部署 API、PostgreSQL 和 Redis，通过 `existingSecret` / `database.existingSecret` 引用配置，不再运行迁移 initContainer。部署方负责数据库准备，生产不使用示例连接。

## 文档入口

- [领域术语](CONTEXT.md)：通用资产、身份、作用域、执行、Memory 与上下文概念。
- [项目布局与依赖约束](docs/layout.md)：目录、装配、共享资源与 SDK 边界。
- [HTTP 契约](docs/api.md)及 [SDK 用法](sdk/go/agenticv1/README.md)：当前接口。
- [恢复设计](docs/design.md)：执行权、事务、接管、步骤及事件回放。
- [框架重构设计](docs/refactoring-design.md)：后续目标结构和实施约束。
- [Memory 设计](docs/memory-design.md)：尚未实现的版本、维护和召回契约。
- [核心流程图解](docs/core-flows.html)：设计中的上下文及恢复流程。
- [项目经验](docs/lessons/README.md)与 [历史验证记录](docs/verification.md)。
