# Agentic Kit API

管理 Agent 草稿、不可变发布版本、会话、消息与持久化执行任务。模型推理与工具执行由独立的 `agentic-kit-worker` 完成；API 与 worker 共用 PostgreSQL 和本仓库的 durable 契约，可分别部署和扩容。

Agent 草稿保存在本地，已发布配置正文存入 artifact-go，本地版本表仅保存公开版本号及远端 ID/URI。API 接受新任务前读取指定版本并冻结到 Run，worker 执行和恢复均使用该快照。

本仓库只提供通用 Agent 能力。项目工作台、项目权限政策、产品模板、数据集、评测实验和场景发布审批由调用方应用负责，不在 API 中实现。执行请求可以携带 tenant、project、actor、task 等上下文，核心只据此隔离任务和保存快照，不查询或管理项目业务。

当前执行链路支持 worker 重启后接管已提交任务。上下文构建已实现预算预检、工具结果有界预览、历史两级裁剪和当前轮工具链折叠：Run 接受时冻结完整材料、策略与预算，配套 worker 每次模型调用前重新构建输入。新 Run 使用 `context-v3`，超限先移除低优先级 Memory，再从旧到新折叠历史工具过程、保留问答，仍超限才删除整轮；历史耗尽后仍超限时，把当前轮较旧的工具结果替换为投影标记，只保留最近 3 条完整结果。旧 `context-v1`、`context-v2` 快照保持各自原有裁剪规则。原始消息、工具结果和快照继续完整保存。Memory 的[技术设计](docs/memory-design.md)提出复用 artifact-go，当前真实提交仍固定为 Off，维护与召回尚未实现。[通用框架重构设计](docs/refactoring-design.md)中的访问对象和新表结构仍待实施。

Agent 的 `model.contextWindow` 可配置模型窗口；设置后必须同时设置正数 `model.maxTokens`，且小于窗口。输入预算取两者之差，使用版本化 UTF-8 字节估算与 256 token 协议预留；这是偏保守的估算，不是模型 tokenizer 的精确计数。不配置窗口时使用 128 KiB 输入字节上限。单条工具结果默认最多向模型展示 16 KiB，配置窗口时再限制为输入预算的四分之一（最低 128 字节）。提交期对已知材料预检，超限返回 HTTP 400 `ContextBudgetExceeded`；worker 按实际工具 schema 和当前工具链复检，必需内容仍超限则结束为 Failed。

## 功能与代码分层

| 功能 | 当前负责的内容 |
| --- | --- |
| Agent | 创建和编辑草稿、revision 冲突检查、发布不可变版本 |
| Session / Message | 多轮会话、有序消息、冻结提交时的配置与历史、消息幂等 |
| Run / Execution | 提交、查询、取消、澄清回答、人工确认、执行权校验、trace 与 SSE 回放 |
| Durable / Outbox | API 与 worker 共享执行事务、租约、步骤存档、状态与事件一致提交、通知投递 |
| Context | 完整历史组与 Memory 材料的稳定选择、预算裁剪、版本化 `ContextSnapshot` 冻结 |

```text
agentic-kit-api/
├── cmd/server/main.go        # 服务入口：信号、配置路径、app.Run
├── service/                  # 公开构造、资源装配、HTTP 路由与 Outbox 生命周期
├── internal/
│   ├── app/
│   │   ├── agent/            # Agent 草稿事务、远端发布与 HTTP 接口
│   │   ├── execution/        # 版本预取与持久化提交
│   │   ├── session/          # 会话、消息与提交接口
│   │   ├── run/              # 执行、租约、取消、人工恢复、trace 与 SSE
│   │   └── apperror/         # 领域错误到 HTTP 错误的映射
│   ├── client/               # artifact 与 Forge 鉴权 SDK 适配、错误转换
│   ├── config/               # 类型化配置与启动校验
│   ├── schema/               # 空库目标表结构初始化
│   └── table/                # Agent、Session、Run、Step、Event、Message 表模型
├── pkg/durable/              # API/worker 共享持久化事务与执行契约
├── pkg/contextbuilder/       # 纯上下文构建、预算裁剪与快照编码
├── sdk/go/agenticv1/          # 通用 HTTP 请求/响应、Go SDK 与 SSE 消费
├── doc/openapi/              # 由 Controller 与 SDK 生成的 OpenAPI
├── conf/server/config.toml   # 默认配置与环境占位符
├── examples/agents/          # Agent 配置样例
├── scripts/publish-agent.py  # 调用通用 Agent API 的发布脚本
└── charts/agentic-kit-api/    # API、PostgreSQL 与 Redis 部署模板
```

Agent 资产事务由 `internal/app/agent` 负责，单表查询位于 `internal/table/agent_query.go`；执行一致性事务位于 `pkg/durable`。Controller 负责绑定和响应，表模型仅在 `internal/table` 维护。独立服务的 `service.Build` 创建连接与 artifact 客户端并交给 server-go Runtime；组合应用通过 `service.New` 注入已有资源，所有者在服务停止后关闭资源。

## 配置与启动

```bash
GOWORK=off go run ./cmd/server -config conf/server/config.toml
```

通过 `-config`、`CONFIG_FILE`、`SERVER_CONFIG_PATH` 依次选择配置路径，未指定时读取 `conf/server/config.toml`。环境变量通过 TOML 中的 `${env:NAME:default}` 占位符映射，普通常量不被同名环境变量覆盖。

启动需要 HTTP、数据库、Redis、`[Artifact]` 和 `[Tenant]` 配置。常用环境变量为 `DATABASE_URL`、`REDIS_ADDR`、`LISTEN_ADDR`、`ARTIFACT_ENDPOINT`、`TENANT_ENDPOINT`、`TENANT_SERVICE_API_KEY`；`Artifact.Timeout` 和 `Tenant.Timeout` 的单位为毫秒。Tenant endpoint 使用 HTTPS，本机回环地址允许 HTTP；service API key 是本 API 调用 Forge 的凭据，至少 32 个字符。发布 Agent、读取版本正文和接受新任务依赖 artifact 服务，已接受任务的执行与恢复不依赖它。`[Redis]` 使用 server-go `redis.Config`，以 `Addrs` 和 `DB` 配置连接；Redis 离线不阻止启动，事件仍从数据库回放。模型与工具凭据由 worker 环境提供，快照仅保存环境变量引用。

本地沙箱运行时通过 `[Sandbox]` 启用。自动保存与检查点恢复需设置 `SANDBOX_K8S_CONTEXT`、`SANDBOX_GUARD_IMAGE`、`SANDBOX_CHECKPOINT_IMAGE` 和绝对路径 `SANDBOX_CHECKPOINT_STORE_HOST_PATH`；后两项必须同时配置。当前存储适用于单节点 hostPath，申请恢复还须匹配 Run 冻结的文件系统绑定，详见[保存与恢复验证](docs/sandbox-center/layerfs-save-verification.md)。

用户业务路由使用 Forge SDK 的 `MiddlewareAuthorization` 校验身份与所选租户，要求 `x-forge-tenant-id`，可选 `x-forge-project-id`。URL 不提供鉴权 scope，Action 为空；本层不定义项目业务权限政策。middleware 将已验证的 tenant/project 写入请求的 `access.Scope`，路由末端再次校验租户非空；组合应用通过 `service.New` 显式注入 `Authentication`，缺 User 鉴权拒绝构造，注入的 middleware 未写入租户 scope 时请求仍被拒绝。Build 创建的 Forge 客户端由 Runtime 关闭。

内部 execution、goal、lease 路由仅在 `Tenant.AllowedPropagationServices` 非空时注册，使用 `MiddlewareInternalPropagation` 验证调用服务及白名单，并接收 `x-impersonate-target-tenant-id`、`x-impersonate-target-user-id` 和可选 `x-impersonate-target-project-id`；末端同样要求租户 scope。`Sandbox.AllowedServices` 为空时不注册沙箱路由；启用后使用独立的服务认证与白名单，申请的 `tenantId`/`ownerId` 仍来自可信调用方请求体，不从 Forge 用户 scope 推导。callback 默认不注册，仅在 `Callback.Enabled=true` 且已装配 callback 服务认证时注册；启动必须配置 `SigningSecret`、`AllowedSources`、`AllowedServices`，请求同时通过服务认证、来源白名单和 HMAC 校验。这些 HTTP 门槛不改变可信 worker 直接调用 Store 时无 HTTP scope 的内部契约。

artifact 出站使用 API 自身配置的 `Tenant.ServiceAPIKey`，只传播 middleware 已核验的 user/tenant/可选 project；`X-Resource-User-ID` 固定为同一用户，保留个人归属。入站 Authorization、Cookie、CSRF、Origin 均不转发；缺 tenant 或 actor 时适配器返回依赖不可用错误，不退化为共享范围。artifact 部署必须将该 key 对应的 API 服务名列入传播白名单，浏览器不得持有服务凭据。正式依赖仍为 artifact-go v0.5.0，尚缺 `PropagatedUserID`；本轮使用临时 `go.work` 联调本地 SDK，待 SDK 发版后须升级依赖并完成 `GOWORK=off` 验证，不加本地 replace。真实 Forge/artifact 联调尚未完成，假服务测试不代表发布路径已通过上线验收。

`service.Build` 在接收请求前调用 `durable.Store.InitSchema`，初始化只面向空库或空 schema，可在目标结构上重复执行。它不迁移旧的无 tenant 数据、不做数据回填，也不删除已有旧索引（如历史 `memory_operation_owner_key`）；旧库升级由部署方另行处理。组合部署直接调用 `service.New` 时由调用方负责初始化。本仓库不提供 `eval`、`migrate` 命令或 Helm 迁移任务。

server-go HTTPHost 提供 `/ping`、`/healthz`、`/version` 探针；开启 `HTTPServer.SwaggerEnable` 可查看 `/swagger/`。SSE 要求 `HTTPServer.WriteTimeout` 为负数，`APIBasePath` 为空。

## 调用接口

下面的请求创建并发布 Agent，再创建会话和发送消息。运行前设置有效 Forge 会话的 `FORGE_SESSION`、与该会话匹配的 `FORGE_CSRF_TOKEN`、部署允许的 `FORGE_ORIGIN` 和已授权的 `FORGE_TENANT_ID`；项目范围另加 `x-forge-project-id`。这些字段只供 API 入站鉴权，与服务启动使用的 `TENANT_SERVICE_API_KEY` 分开配置。也可使用有效用户 API key 通过 API 入站鉴权；artifact 出站始终使用 API 自身的服务凭据与已核验目标身份，不转发入站会话或用户 Bearer。此示例尚未经真实 Forge/artifact 联调。

```bash
export API_URL='http://127.0.0.1:8080'
agent_id=$(curl --fail-with-body -sS "$API_URL/v1/agents" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' \
  --data-binary @examples/agents/agents.example.json | jq -r .id)
curl --fail-with-body -sS "$API_URL/v1/agents/$agent_id/publish" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' -d '{"revision":1}'
session_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg agentID "$agent_id" '{agentID:$agentID,title:"第一次对话"}')" | jq -r .id)
run_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions/$session_id/messages" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: question-001' \
  -d '{"query":"请介绍一下你自己。"}' | jq -r .id)
curl -N "$API_URL/v1/runs/$run_id/events" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID"
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
