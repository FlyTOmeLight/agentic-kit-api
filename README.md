# Agentic Kit API

管理 Agent 草稿、不可变发布版本、会话、消息与持久化任务，并提供评测和 Forge 项目/企业空间入口。模型推理与工具执行由独立的 `agentic-kit-worker` 完成；API 与 worker 共用 PostgreSQL 和本仓库的 durable 契约，可分别部署和扩容。

当前执行链路能在worker重启后接管已提交任务；Memory 的[技术设计](docs/memory-design.md)已提出复用 artifact-go 版本服务，仍待实施。本次[通用框架重构](docs/refactoring-design.md)采用新结构，不包含旧版本兼容或数据迁移。

## 功能与代码分层

本节记录当前工作树的实际实现，包含尚未移交的业务模块；不是重构后的目标布局。通用框架目标和逐文件落点见 [重构设计第10节](docs/refactoring-design.md#10-文件级实施地图)，按设计实施后再更新本节，不能将目标当作已经实现。

| 功能 | 当前负责的内容 |
| --- | --- |
| Agent | 创建和编辑草稿、revision 冲突检查、发布不可变版本 |
| Session / Message | 多轮会话、有序消息、冻结提交时的配置与历史、消息幂等 |
| Run / Execution | 查询、取消、澄清回答、人工确认、执行权校验、trace 与 SSE 回放 |
| Evaluation | 持久实验、Trial / Run 提交、后台评分、取消与配对比较；本地 CLI 保留文件 checkpoint |
| Forge 平台 | Tenant 会话、CSRF、项目/企业权限、模板副本、数据集和评测计划、Coding 独立评测与发布门槛 |

当前代码按职责分层，持久化事务跨API与worker共享。现有业务代码、测试、配置、样例与部署文件逐文件展开如下；每个文件旁注明职责：

```text
agentic-kit-api/
├── cmd/  # 进程入口
│   ├── server/
│   │   └── main.go  # 服务进程入口：信号、配置路径、app.Run 与可选 HTTP 集成
│   └── cli/
│       └── main.go  # CLI 入口：装配命令、处理信号和退出码
├── command/  # CLI 领域命令
│   ├── migration/
│   │   └── migration.go  # migrate 命令：加载配置、初始化数据库、显式迁移
│   └── evaluation/
│       ├── evaluation.go  # eval 命令：固定套件执行、checkpoint 保存、评分与结果
│       └── evaluation_test.go  # CLI 中断后继续执行与提交幂等测试
├── service/  # 公开构造、装配和生命周期
│   ├── build.go  # 独立应用装配：配置校验、资源接管、API 注册和路径更新限制
│   ├── api.go  # 公开 API 构造、Controller 生命周期、Outbox 与评测后台任务
│   ├── routes.go  # 构造领域 Controller/Service，安装总路由与鉴权顺序
│   ├── notification_cache.go  # 通知 Redis 连接生命周期，允许离线启动和数据库回放
│   ├── api_test.go  # 真实 HTTP 启动、就绪、SDK 请求、SSE 与退出测试
│   ├── build_test.go  # 动态配置不能改变公开路径的回归测试
│   ├── router_test.go  # HTTP 测试装配、鉴权、绑定、Recovery、任务与 SSE 契约
│   ├── conversations_test.go  # Agent 编辑权限、会话与消息 HTTP 行为测试
│   ├── sdk_test.go  # SDK 创建 Agent、发布、会话和任务全流程测试
│   ├── execution_sdk_test.go  # 受信任应用执行与 SDK 租约校验测试
│   ├── evaluation_sdk_test.go  # 持久评测 SDK 生命周期与权限测试
│   ├── project_sdk_test.go  # 项目/企业授权、候选配置与 Coding 评测测试
│   ├── workspace_test.go  # 企业资产、无项目访问与数据集快照测试
│   ├── trace_test.go  # 公开执行轨迹、回放与 owner 隔离测试
│   └── openapi_test.go  # 文档与真实路由一致性、Swagger JSON 响应测试
├── internal/  # 私有实现
│   ├── app/  # HTTP 与平台业务编排
│   │   ├── agent/  # Agent
│   │   │   ├── controller.go  # Agent 创建、查询、编辑、发布和版本 HTTP 处理
│   │   │   ├── router.go  # Agent 路由、Controller 构造与配置编辑者权限
│   │   │   ├── service.go  # Agent Controller 所需的共享事务接口
│   │   │   └── doc.go  # OpenAPI 全局标题、版本和鉴权说明
│   │   ├── apperror/  # 领域错误映射
│   │   │   └── response.go  # durable 领域错误到框架 HTTP 错误的映射
│   │   ├── session/  # 会话与消息
│   │   │   ├── controller.go  # 会话、消息、历史任务与消息提交 HTTP 处理
│   │   │   ├── router.go  # Session 路由与 Controller 构造
│   │   │   ├── service.go  # 会话及消息提交所需的共享事务接口
│   │   │   └── controller_test.go  # 正文、URI、Header 来源隔离的真实 HTTP 回归测试
│   │   ├── run/  # 任务与执行
│   │   │   ├── controller.go  # Run 查询、取消、澄清回答和人工结果确认
│   │   │   ├── router.go  # Run 路由、Controller 构造与执行接口依赖
│   │   │   ├── service.go  # Run 状态、事件和结果操作的消费方接口
│   │   │   ├── events.go  # SSE 事件游标、数据库回放、Redis 唤醒和心跳
│   │   │   ├── executions.go  # 受信任执行提交、执行权/评测租约校验与内部健康接口
│   │   │   └── trace.go  # 公开 Run trace 查询与缓存策略
│   │   ├── evaluation/  # 持久评测
│   │   │   ├── controller.go  # 实验提交、列表、读取、取消与比较 HTTP 处理
│   │   │   ├── router.go  # 持久评测路由与受信任应用权限
│   │   │   └── service.go  # 持久实验操作的消费方接口
│   │   └── platform/  # Forge 项目与企业空间
│   │       ├── auth.go  # 平台配置、模板定义、Tenant/项目鉴权与平台路由
│   │       ├── controller.go  # 模板、候选 Agent、实验和计划的 HTTP 处理
│   │       ├── service.go  # 模板复制、数据集冻结、评测授权、比较和发布门槛编排
│   │       ├── store.go  # 平台业务所需的持久化消费方接口
│   │       ├── workspace.go  # 企业 owner/项目范围辅助与数据集 HTTP 接口
│   │       ├── trial.go  # 项目试运行提交、读取和取消 HTTP 接口
│   │       └── release.go  # 受信任 Coding 发布审批 HTTP 入口
│   ├── client/  # 外部协议和连接
│   │   ├── tenant.go  # Tenant 会话与授权协议、HTTP 连接所有权
│   │   ├── coding.go  # Coding SDK 适配、隔离环境绑定与评测证据转换
│   │   └── coding_test.go  # Coding 适配的正式 SDK 请求契约测试
│   ├── config/  # 类型化配置
│   │   ├── service.go  # 类型化配置、数据库/API 校验和平台配置解析
│   │   └── service_test.go  # 配置文件、环境占位符、SSE 与平台校验测试
│   └── table/  # 表模型与迁移
│       ├── agent.go  # Agent 草稿和不可变版本表模型
│       ├── session.go  # 会话和有序消息表模型
│       ├── run.go  # 任务、步骤、事件及执行快照表模型
│       ├── evaluation.go  # 实验、Trial、评测计划及相关表模型
│       ├── dataset.go  # 数据集草稿和已发布版本表模型
│       └── migrate.go  # 集中注册 GORM 迁移模型
├── pkg/  # 跨调用方共享实现
│   ├── durable/  # API/worker 共享执行事务
│   │   ├── store.go  # Store 创建/借用、连接关闭、迁移锁与旧数据检查
│   │   ├── types.go  # 公开状态、执行快照、租约、消息和任务类型
│   │   ├── record_conversion.go  # 私有 Run/Event 表记录到公开对象的转换
│   │   ├── agent_service.go  # Agent 配置校验、草稿更新、发布版本和列表事务
│   │   ├── session_service.go  # 会话查询、消息编号、历史冻结与消息/任务同事务提交
│   │   ├── run_service.go  # 领取、续期、epoch 校验、步骤存档、重试、取消和事件读取
│   │   ├── execution_service.go  # 应用执行快照提交、任务会话复用与执行权检查
│   │   ├── events.go  # Outbox 领取、按序发布、Redis 幂等写入与 Relay
│   │   ├── trace.go  # 从持久记录生成公开轨迹，过滤执行器私有信息
│   │   ├── dataset.go  # 数据集草稿、revision、发布与版本快照事务
│   │   ├── evaluation_service.go  # 持久实验提交、读取、取消与终态评分协调
│   │   ├── evaluation_plan.go  # 评测计划保存、revision 与项目范围查询
│   │   ├── evaluation_coding.go  # Coding 隔离环境准备、运行、证据读取与恢复协调
│   │   ├── coding_release.go  # 冻结可发布 Coding 候选与幂等发布事务
│   │   ├── store_test.go  # 数据库测试装配、领取竞争、隔离、步骤、回滚与期限测试
│   │   ├── models_test.go  # 表约束、唯一性和数据库默认值测试
│   │   ├── framework_store_test.go  # 框架数据库借用所有权、错误转换和重复迁移测试
│   │   ├── conversations_test.go  # 版本/历史冻结、并发发布、owner 隔离与事务回滚测试
│   │   ├── application_agent_test.go  # 应用复用发布版本与多轮任务会话的并发测试
│   │   ├── execution_service_test.go  # 应用快照和旧执行权隔离测试
│   │   ├── events_test.go  # Outbox 多发布者顺序、锁定头事件与幂等测试
│   │   ├── filesystem_test.go  # 文件系统绑定必需性与快照冻结测试
│   │   ├── evaluation_service_test.go  # 实验并发提交、全体 Run 事务、取消和评分完成条件测试
│   │   └── evaluation_coding_test.go  # Coding 独立证据、恢复与准备阶段取消测试
│   └── evaluation/  # 评分与比较
│       ├── evaluate.go  # 套件校验、摘要、确定性评分和覆盖率计算
│       ├── evaluate_test.go  # 评分、覆盖率、能力约束与证据完整性测试
│       ├── compare.go  # 基线与候选实验的配对比较
│       └── compare_test.go  # 配对比较规则测试
├── sdk/
│   └── go/
│       └── agenticv1/  # 公开 HTTP 契约与 Go SDK
│           ├── doc.go  # 公开 SDK 包说明
│           ├── README.md  # SDK 构造、调用、错误、分页与事件流用法
│           ├── agentic_client.go  # 普通客户端与 Forge 客户端构造、业务入口和 Close
│           ├── agentic_transport.go  # 框架 HTTP 传输、超时、JSON 完成判定与错误处理
│           ├── agentic_api_agent.go  # Agent 配置、模型/工具、请求和版本契约
│           ├── agentic_api_session.go  # 会话、消息、提交请求与查询参数契约
│           ├── agentic_api_run.go  # Run 状态、工具策略、事件、结果和 trace 契约
│           ├── agentic_api_execution.go  # 应用执行范围、快照、租约和文件系统绑定契约
│           ├── agentic_api_evaluation.go  # 评测套件、实验、观察、评分与 Coding 证据契约
│           ├── agentic_api_dataset.go  # 数据集草稿、版本与保存请求契约
│           ├── agentic_api_project.go  # Forge 项目/企业、候选、试运行和发布契约
│           ├── agentic_api_page.go  # 通用 ID 游标分页类型
│           ├── agentic_api_requests.go  # 操作请求对象、路径/查询/Header 参数与 Created 响应
│           ├── agentic_interface_agent.go  # Agent SDK 操作接口
│           ├── agentic_interface_session.go  # Session SDK 操作接口
│           ├── agentic_interface_run.go  # Run SDK 操作接口
│           ├── agentic_interface_execution.go  # 受信任 Execution SDK 操作接口
│           ├── agentic_interface_evaluation.go  # Evaluation SDK 操作接口
│           ├── agentic_client_agent.go  # Agent HTTP 调用实现
│           ├── agentic_client_session.go  # Session、消息和提交 HTTP 调用实现
│           ├── agentic_client_run.go  # Run、人工结果、trace 和原始 SSE 调用实现
│           ├── agentic_client_execution.go  # 受信任执行提交与租约校验调用实现
│           ├── agentic_client_evaluation.go  # 持久实验提交、读取、取消和比较调用实现
│           ├── agentic_client_project.go  # Forge 项目/企业资产、数据集、计划、试运行和发布调用
│           └── agentic_client_test.go  # 真实 HTTP 协议、错误、取消、分页、并发和流关闭测试
├── scripts/  # 管理与文档生成脚本
│   ├── publish-agent.py  # 创建/更新并发布应用 Agent 的脚本
│   └── normalize-openapi.py  # 规范化共用 handler 的各路由路径参数及生成文档
├── conf/
│   └── server/  # 服务配置
│       └── config.toml  # 默认服务、数据库、Redis、鉴权和平台配置
├── examples/  # 接口与评测样例
│   ├── agents/
│   │   ├── agents.example.json  # 通用 Agent 创建请求样例
│   │   └── writing-agent.example.json  # Writing Agent 创建请求样例
│   └── evaluation/
│       └── evaluation-writing.example.json  # 冻结的 Writing 评测套件样例
├── deploy/  # 本地依赖
│   └── compose.yaml  # 本地 PostgreSQL 与 Redis 容器配置
├── charts/
│   └── agentic-kit-api/  # Kubernetes 部署
│       ├── Chart.yaml  # Helm chart 元信息
│       ├── values.yaml  # Helm 默认部署参数
│       └── templates/
│           ├── application.yaml  # API Deployment、迁移 initContainer 与 Service
│           ├── database.yaml  # PostgreSQL 部署与存储模板
│           └── redis.yaml  # Redis 部署模板
├── doc/
│   └── openapi/  # 自动生成；由 make doc 更新
│       ├── Agentic_docs.go  # 生成的 Swagger 文档注册代码
│       ├── Agentic_swagger.json  # 生成的 JSON OpenAPI 契约
│       └── Agentic_swagger.yaml  # 生成的 YAML OpenAPI 契约
├── Dockerfile  # 无 CGO 的 server/CLI 镜像构建与默认配置打包
├── Makefile  # 测试、集成、静态检查、构建与文档生成入口
├── go.mod  # 模块路径、Go 最低版本与依赖版本
└── go.sum  # 模块依赖校验和
```

`internal/app/agent`、`session`、`run`、`evaluation` 处理对应 HTTP 接口；`platform` 承担 Forge 跨领域编排，`apperror` 映射 durable 领域错误。已有单次事务直接消费 durable 接口，不另加纯转发业务层。

```mermaid
flowchart LR
    Entry[cmd/server] --> Host[server-go app.Run]
    Host --> Assembly[service 装配与生命周期]
    Assembly --> HTTP[internal/app Controller]
    HTTP --> Platform[platform Service]
    HTTP --> Durable[pkg/durable 共享事务]
    Platform --> Durable
    Platform --> Client[internal/client 外部连接]
    Durable -. 复用模型 .-> Tables[internal/table 模型与迁移]
    Durable --> DB[(PostgreSQL)]
    Worker[独立 worker] --> Durable
    Durable --> Notify[Outbox / Redis 通知]
    CLI[command/evaluation] --> Score[pkg/evaluation]
    Durable --> Score
```

用户消息与任务同事务创建；任务完成时，状态、输出、执行消息和完成事件一起提交。Redis 只加速通知，已提交事件从数据库回放。框架提供请求日志、Recovery、标准错误、鉴权基础能力和 SSE 传输；领域权限、事件游标及恢复规则由本项目维护。具体依赖方向和资源所有权见 [项目布局与约束](docs/layout.md)。

## 本地运行

需要 Go 1.27.1 或更新版本、PostgreSQL 17 和 Redis 7.2。依赖版本以 [go.mod](go.mod) 为准，本地服务参数以 [Compose](deploy/compose.yaml) 和 [默认配置](conf/server/config.toml) 为准。

Coding SDK 来自私有 `forge-coding-api` 根模块，导入路径仍为 `sdk/go/codingv1`。首次获取仓库和下载私有模块需配置 GitHub 认证：

```bash
gh auth login
gh auth setup-git
export GOPRIVATE="${GOPRIVATE:+$GOPRIVATE,}github.com/FlyTOmeLight/*"
gh repo clone FlyTOmeLight/agentic-kit-api
gh repo clone FlyTOmeLight/agentic-kit-worker
```

在 API 仓库启动数据库、迁移表结构并运行服务：

```bash
docker compose -f deploy/compose.yaml up -d
cp conf/server/config.toml conf/server/config.local.toml
export API_TOKENS_JSON='{"replace-with-your-local-token-32chars":"local-user"}'
go run ./cmd/cli migrate --config conf/server/config.local.toml
go run ./cmd/server -config conf/server/config.local.toml
```

在相邻 worker 仓库配置同一数据库和模型/工具凭据，再启动执行器：

```bash
cd ../agentic-kit-worker
cp configs/config.example.toml configs/config.local.toml
go run ./cmd/worker -config configs/config.local.toml
```

worker 的配置与工具能力以其 README 为准。它默认使用自己的 go.mod 固定的 API 版本；同时修改两个仓库时，在共同父目录创建包含 API/worker 的 Go workspace，联合验证后按发布依赖再检查独立构建。workspace 文件不提交。

### 配置与启动行为

通过 `-config`、`CONFIG_FILE`、`SERVER_CONFIG_PATH` 依次选择配置路径，未指定时读取 `conf/server/config.toml`。环境变量只通过文件中的 `${env:NAME:default}` 占位符映射；写成普通常量的字段不会被同名环境变量覆盖。默认文件也支持框架的文件和密文占位符。

常用环境变量为 `DATABASE_URL`、`REDIS_URL`、`API_TOKENS_JSON`、`CONFIG_OWNERS` 和 `LISTEN_ADDR`。默认配置只将 `local-user` 列为配置编辑者；变更 token 的 owner 时，同步设置 `CONFIG_OWNERS`。模型和工具凭据属于 worker 环境。

数据库使用 `[DB] Driver="PostgreSQL"` 和 `DSN`；`DB.AutoMigrate` 必须为 false，迁移通过 CLI 显式执行。HTTP 的 `APIBasePath` 保持为空，`WriteTimeout` 为负数以支持长时间 SSE。完整字段和默认值直接查看 [配置文件](conf/server/config.toml)。

`app.Run` 先装配连接和业务对象，再启动 HTTP 与 Outbox/评测后台任务；退出时先排空 HTTP，再停止任务和释放资源。Redis 不可用时仍可启动并回放数据库事件。配置监听支持框架的可动态更新 HTTP 项；连接、业务配置、监听与超时变更需要重启。

`/ping`、`/healthz`、`/version` 无需鉴权；`/healthz` 表示后台任务已启动，不持续探测外部连接。开启 `HTTPServer.SwaggerEnable` 可查看 `/swagger/`。

## 调用接口

创建 Agent、发布、创建会话和发送消息（示例使用 curl 和 jq）：

```bash
export API_TOKEN='replace-with-your-local-token-32chars'
export API_URL='http://127.0.0.1:8080'
# 示例指向本机 Ollama qwen3.8:27b-mlx；可复制到 *.local.json 修改。
agent_id=$(curl --fail-with-body -sS "$API_URL/v1/agents" \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  --data-binary @examples/agents/agents.example.json | jq -r .id)
curl --fail-with-body -sS "$API_URL/v1/agents/$agent_id/publish" \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"revision":1}'
session_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions" \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg agentID "$agent_id" '{agentID:$agentID,title:"第一次对话"}')" | jq -r .id)
run_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions/$session_id/messages" \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: question-001' -d '{"query":"请介绍一下你自己。"}' | jq -r .id)
curl -N "$API_URL/v1/runs/$run_id/events" -H "Authorization: Bearer $API_TOKEN"
curl --fail-with-body -sS "$API_URL/v1/sessions/$session_id/messages" -H "Authorization: Bearer $API_TOKEN"
```

继续对话时使用相同 session_id、新的 Idempotency-Key 和 query。编辑 Agent 使用 `PUT /v1/agents/{id}`，传完整 name/config 和当前 revision；发布后新任务使用新版本，已有任务仍使用原版本。详情见 [接口契约](docs/api.md)。

### Go SDK

导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`。Client 提供 Agents、Sessions、Runs、Executions、Evaluations；ProjectClient 提供 Forge 会话入口。所有操作接收标准 context 和操作请求指针，路径、查询、Header 与 JSON 字段按协议分别编码。

```go
result, err := client.Sessions.SendMessage(ctx, &sdk.SendMessageRequest{
    SessionID: sessionID, IdempotencyKey: "question-001", Query: "请介绍一下你自己。",
})
```

`result.Created` 区分新建与幂等回放。调用方负责关闭客户端和事件流；SDK 不自动重试或重连。构造客户端、错误处理和分页示例见 [SDK README](sdk/go/agenticv1/README.md)，HTTP 字段、权限和状态码见 [接口契约](docs/api.md)。

### 发布与评测

`scripts/publish-agent.py` 创建或更新 Agent 并发布版本。先设置 `AGENTIC_URL`、`AGENTIC_TOKEN`，token 的 owner 必须有配置编辑权限：

```bash
python3 scripts/publish-agent.py examples/agents/writing-agent.example.json
python3 scripts/publish-agent.py examples/agents/writing-agent.example.json --agent-id <agentID>
```

本地评测修改固定套件中的 Agent ID/版本后运行：

```bash
go run ./cmd/cli eval --suite examples/evaluation/evaluation-writing.example.json --state .local/evaluation/state.json
```

同一 state 文件由单个进程写入；重跑复用已记录 Trial/Run，套件变化使用新文件。退出码 0 表示门槛通过，2 表示未通过，1 表示配置或执行错误。CLI 处理 single_turn 场景；Coding 场景通过平台持久评测接口使用独立环境。持久实验通过 `Client.Evaluations` 或平台入口提交，评分可在 API 重启后继续，原 Run 不重建。

Forge 入口使用 Tenant 会话、CSRF 和实时项目/企业权限。默认配置的 `PLATFORM_TENANT_*`、`PLATFORM_TEMPLATE_*` 启用受信任模板；Coding 再配置 `PLATFORM_CODING_*`。模板与模型连接由部署管理员维护，项目候选不会自动替换业务执行版本。完整接口见 [Forge 契约](docs/api.md#forge-项目-agent-与评测入口)。

## 验证与部署

常用命令以 [Makefile](Makefile) 为准：`make test`、`make vet`、`make build`；`make doc` 从 Controller/SDK 生成 [OpenAPI](doc/openapi/Agentic_swagger.json)，`make install-doc` 安装固定文档工具。

涉及持久化、任务状态、SSE 或跨进程行为时，配置隔离数据库和 Redis 后执行：

```bash
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export TEST_REDIS_URL='redis://127.0.0.1:56379/0'
GOWORK=off make integration
GOWORK=off CGO_ENABLED=0 make build
```

数据库测试创建随机 schema 并清理，账户需有建 schema 权限；Redis 测试使用本次生成的 run key。未配置数据库时的跳过不代表集成验证通过。变更表结构需验证已有数据上的重复迁移；变更共享 durable 契约需联合验证 worker，涉及接管时保留真实跨进程恢复测试。HTTP/SDK 改动同步契约、Swagger 与调用方。历史已执行检查见 [验证记录](docs/verification.md)。

[Dockerfile](Dockerfile) 构建 server/CLI 并打包默认配置；私有模块下载使用 BuildKit secret `github_netrc`。`charts/agentic-kit-api` 部署 API、PostgreSQL 和 Redis，通过 `existingSecret` / `database.existingSecret` 引用凭据，使用 `/healthz` 就绪探针。部署前执行 migrate，生产不使用默认示例连接和 token。

## 文档入口

- [领域术语](CONTEXT.md)：框架目标中的资产、身份、作用域、执行、Memory与上下文概念及区别。
- [通用 Agent 框架重构设计](docs/refactoring-design.md)：待实施的规则拆分、事务、文件目标与验收；评测和场景应用由各自任务设计。
- [核心流程与接口图解](docs/core-flows.html)：待实施方案的Agent创建、完整对话、API/worker协作及人工恢复，可离线交互查看。
- [项目布局与依赖约束](docs/layout.md)：改目录、装配、SDK 或共享包前查阅。
- [HTTP 契约](docs/api.md)与 [SDK 用法](sdk/go/agenticv1/README.md)：接口开发与接入。
- [恢复设计](docs/design.md)：执行权、事务、接管、步骤及事件回放。
- [项目经验](docs/lessons/README.md)：本项目已验证的做法、陷阱和证据。
- [验证记录](docs/verification.md)：按日期保留的历史结果。
