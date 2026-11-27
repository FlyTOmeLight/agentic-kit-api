# Agentic Kit API

通用持久化 Agent 控制面与执行契约服务，负责管理 Agent 草稿、不可变发布版本、会话、消息、持久化执行任务、上下文预算冻结、受控 Memory、模型计价快照以及可选 Kubernetes 沙箱控制面。模型推理、工具调用、摘要压缩与沙箱命令验收由独立的配套执行面 `agentic-kit-worker` 完成；API 与 Worker 共用同一个 PostgreSQL 数据库和本仓库的 `pkg/durable` 契约，可分别部署与独立扩缩容。

> **新接手开发必读**：修改代码前先查阅 [AGENTS.md](AGENTS.md) 的文档路由表与 [项目经验索引](docs/lessons/README.md)；了解与原一体化平台 `~/Code/agentic-kit` 的边界取舍与待补齐能力进度，见 [功能对比与进度跟踪](docs/agentic-kit-comparison.md)。

---

## 系统架构与执行流

### 定位与能力边界

本仓库定位为**无产品语义的通用 Agent 内核**，能力边界划分如下：

| 归属 | 负责内容 |
| --- | --- |
| **本内核负责**（API + Worker） | Agent 草稿与不可变版本（`artifact-go`）、多轮会话与消息事实、持久化任务（Run / Goal / Child Run）、运行中 Steer 与外部 HMAC 回调、基于数据库租约与 `epoch` 的跨进程崩溃接管、上下文预算与压缩投影（`context-v4`）、受控 Memory 维护与本地召回、冻结计价账本（Token/USD）与未知消费阻断、可选 K8s Guard + LayerFS 沙箱控制面 |
| **外部调用方负责**（通过 [Go SDK](sdk/go/agenticv1/README.md) 接入） | 项目工作台、企业成员与业务 RBAC 权限等级、垂直场景模板（Writing / Coding / Personal）、可视化 DAG 工作流、技能包与插件市场、IM 机器人渠道、定时触发器（Cron）、离线评测集与数据集管理 |

执行请求可以携带 `tenant`、`project`、`actor`、`task` 等上下文，核心只据此隔离会话、任务与记忆快照，不在核心内部维护业务权限等级或工作流审批状态。

### 双进程拓扑图

```mermaid
flowchart LR
    Client["调用方应用 / 用户 (HTTP + Go SDK)"] -->|REST / SSE| API["agentic-kit-api (:8080)"]
    API <-->|鉴权与项目读写权限| Forge["forge-tenant-api (v0.13.0)"]
    API <-->|Agent 版本 / 附件解析 / 可选 Memory 归档| Artifact["artifact-go (v0.5.2)"]
    API -->|事务写入 & Outbox| PG[("PostgreSQL (16 张权威表)")]
    API -->|异步中继权威流 & 订阅瞬态流| Redis[("Redis (加速通知与实时增量，允许离线)")]
    Worker["agentic-kit-worker (:8081)"] <-->|SKIP LOCKED 领取 / Checkpoint / Step / Usage| PG
    Worker -.->|可选瞬态增量推送| Redis
    Worker -->|调用模型 / HTTP & MCP & AIO 工具| External["LLM Provider / MCP & 外部工具"]
    API -.->|可选 K8s Guard + Saver + Verifier| K8s["Kubernetes / LayerFS + JuiceFS"]
    Worker <-->|执行沙箱命令与验收| K8s
```

### 一次任务的端到端生命周期

1. **资产发布**：开发者通过 `/v1/agents` 创建或更新草稿（乐观锁 `revision` 防并发覆盖），调用 `/publish` 将不可变配置正文存入 `artifact-go`，本地 `agent_version` 仅保存公开版本号与远端 ID/URI。
2. **幂等提交与快照冻结**：客户端调用 `POST /v1/sessions/{id}/messages`（普通聊天）或 `POST /internal/v1/executions`（受信任应用任务）。API 在事务外校验权限并预取已发布配置，对已知材料执行预算预检（超限直接返回 HTTP 400 `ContextBudgetExceeded`，不建 Run 且不占用幂等键）；随后在单事务内冻结目标 Agent 版本、`PricingSnapshot`、`context-v4` 策略与 SHA-256 `ContextSnapshot`（含会话水位摘要、水位数之后的完整历史与显式 `Recall` 召回的本地 Memory），原子写入 `durable_run`、`User` 消息与 `RunQueued` 事件。
3. **Worker 租约领取与执行**：独立 `agentic-kit-worker` 轮询 PostgreSQL，通过 `FOR UPDATE SKIP LOCKED` 与数据库时钟 `clock_timestamp()` 领取任务并每 1/3 租约周期续期，按冻结配置装配 Eino `v0.9.2` `ChatModelAgent + Runner`。调用模型或摘要前先落库 `InflightModel` 标记；收到模型工具决策后同事务写入 Eino checkpoint 与工具意图 `StepIntent`（标注 `ReadOnly` / `Idempotent` / `Unsafe`）。模型及工具真实凭据通过快照中的 `apiKeyEnv` / `tokenEnv` 从 Worker 环境变量读取，不入库。
4. **崩溃接管与人工介入**：Worker 崩溃后，新实例在租约到期后自动接管并递增 `epoch` 隔离僵尸写入：已完成工具直接复用结果（`StepReplay`）而不重调外部接口；非幂等 `Unsafe` 工具中断进入 `NeedsReview`（`/resolve`），模型主动提问进入 `WaitingClarification`（`/answer`），外部异步步骤进入 `WaitingExternal`（`/callback`），派发模型期间进程崩溃进入非终态 `Blocked`（不自动重放，由人工审计调用 `/unblock` 接受重复计费风险后继续）。
5. **事件投递与 SSE 回放**：任务状态、消息与 `durable_event` 同事务提交；API 后台 `Outbox Relay` 按单 Run 严格保序推送到 Redis Stream。客户端订阅 `GET /v1/runs/{id}/events` 时，API 始终以 PostgreSQL 为权威数据源按游标回放，Redis 离线时自动退化为每秒轮询数据库。
6. **长程 Goal 续跑与工作区继承**：内部 `Goal` 支持多轮 Run 自动迭代直到沙箱 `command` 验收通过，内置 Token / USD / WallClock 预算、连续 3 轮同 `StallMark` 阻断、运行中无损注入 `Steer` 纠偏指令、Completed Run 独立验收租约，以及基于 LayerFS / JuiceFS `ReadWriteMany` 的跨 Run 工作区打包校验与原子恢复。

---

## 功能与代码分层

### 核心模块职责

| 模块 | 当前实现范围与约束 |
| --- | --- |
| **Agent** | 创建和编辑草稿、`revision` 乐观锁冲突检测、写入 `artifact-go` 发布不可变版本；配置校验要求 `maxTurns` 为 1～100，支持 `model.stream`、`model.inputModalities`、通用 HTTP 工具、标准 Streamable HTTP `mcpServers`、按需挂载的 `skills`（`artifact-go` `agent-skill` 版本与文件清单冻结）、双模式 `subAgents`/`selfDelegate` 及 `returnDirectly`/`endRun`（拒绝专有协议），并校验 `apiKeyEnv`/`tokenEnv` 格式与平台保留名 |
| **Session / Message** | 多轮会话、会话行锁下的单调 `seq` 消息、同会话单活动 Run 限制、按 `(owner, tenant, project, taskID)` 复用的应用会话、幂等提交与历史分页 |
| **Run / Execution / Goal** | 提交、查询、取消（级联取消子 Run）、`WaitingClarification` / `NeedsReview` / `Blocked` 人工恢复、外部 HMAC 回调、Goal 预算/Stall/Steer/验收闭环、脱敏 `RunTrace` 与 SSE 回放 |
| **Durable / Outbox** | API 与 Worker 共用的执行事务、数据库时钟租约与 `epoch` 隔离、步骤意图与重放、状态/消息/事件同事务提交、单 Run 保序 Outbox 投递 |
| **Context（`context-v4`）** | 配置 `model.contextWindow` 时必配正数且小于窗口的 `model.maxTokens`，输入预算取两者之差，按 `cjk-weighted-v1`（CJK 字符权重 2、其余权重 1，总权重除以 3 向上取整）并扣减 256 token 协议预留；未配窗口时默认 128 KiB 输入字节上限。单条工具结果默认最多向模型展示 16 KiB（配窗口时再上限为输入预算的一半，最低 128 B）。裁剪顺序：移除低优先级 Memory -> 折叠历史工具链 -> 移除最旧完整历史组 -> 预览较旧当前结果与大参数 -> 先成功再失败压为带 `read_step_result` 句柄的可恢复存根 -> 可选当前轮淘汰并固定原始提问。完整消息留存 PostgreSQL，冻结快照存 `text` 列并校验 SHA-256，仅在 `BoundaryComplete` 推进会话摘要水位 |
| **Memory** | 支持 `OwnerPrivate`、租户级 `ActorPrivate` 与项目内 `ActorProject` 的人工创建、读取、纠正、停用、显式 Restore 与已生效版本历史查询；项目读取、召回与写入按同租户、项目和用户隔离并实时核验 Forge `read`/`write` 权限。`ActorProject` 默认 `Private`，标为 `Project` 的条目可进入项目应用执行。聊天与应用执行默认 `Off`，显式 `Recall` 在接受事务内按输出受众冻结本地投影。可选 `artifact-go` 归档通过持久回执衔接远端与本地生效，后台在核验写权限后继续未完成操作。`ProjectShared`、自动提炼、语义检索和版本物理删除仍待实施（详见 [Memory 设计](docs/memory-design.md)） |
| **Pricing / Usage** | API 提交期冻结单价快照，`durable_usage` 按 `(run_id, turn, call)` 幂等累加主模型与摘要消费；缺失 usage 或拆分明细按故障阻断，派发中崩溃保留 `InflightModel` 置为 `Blocked` |
| **Sandbox（可选）** | `internal/sandboxcenter` 管理无特权 `Guard` Pod（UDS 控制通道）、命令回执去重（`X-Sandbox-Operation-ID` / `502 OutcomeUnknown`）以及 `Saver` + `Verifier` + `initContainer` 跨 Run 工作区保存与恢复 |

### 仓库目录结构（API 与配套 Worker）

```text
agentic-kit-api/
├── cmd/server/main.go        # API 进程入口：信号处理、配置路径加载、app.Run
├── service/                  # 公开构造（Build / New / NewWithSandbox）、路由总装配、Outbox Relay 与后台任务生命周期
├── internal/
│   ├── app/
│   │   ├── agent/            # Agent 草稿事务、远端 artifact-go 发布与 /v1/agents 接口
│   │   ├── execution/        # 发布版本预取、定价冻结与统一提交协调
│   │   ├── session/          # 会话、消息分页与提交接口
│   │   ├── run/              # Run 控制、Goal/Execution 内部入口、Callback、Trace 与 SSE
│   │   ├── memory/           # Memory 人工维护、发布编排、后台 Pending 恢复与本地召回
│   │   └── apperror/         # 领域错误到 HTTP 状态码与错误码映射
│   ├── sandboxcenter/        # 可选 K8s Guard 沙箱控制面、租约、命令回执与 LayerFS 保存恢复编排
│   ├── client/               # artifact-go 与 Forge 鉴权 SDK 适配（外部 SDK 类型边界止步于此）
│   ├── config/               # 类型化配置与启动期参数校验
│   ├── schema/               # 空库/空 schema 初始化（advisory lock + AutoMigrate 16 张表）
│   └── table/                # Agent、Execution、Memory、Sandbox 全部 GORM 表模型与查询唯一来源
├── pkg/
│   ├── access/               # 已验证请求身份与 Tenant/Project Scope
│   ├── agent/                # AgentConfig、ModelSpec、ToolSpec 与纯校验规则
│   ├── contextbuilder/       # 零 I/O 上下文裁剪、cjk-weighted-v1 预算估算与 SHA-256 快照编解码
│   ├── durable/              # API 与 Worker 共享的执行事务、租约、步骤存档、Outbox、Goal 与 Compaction
│   ├── execution/            # 执行快照、Spec、定价快照、Usage 与状态领域类型
│   ├── memory/               # Memory 领域值、校验与确定性词项匹配
│   └── workspace/            # AssetScope、VersionRef 与 ResultScope 引用类型
├── sdk/go/agenticv1/         # 公开 Go SDK（零 Gin/GORM 依赖，基于 server-go SDK / Resty v3）
├── doc/openapi/              # 由 Controller 注释与 SDK 标签生成的 Swagger/OpenAPI（禁止手改）
├── conf/server/config.toml   # API 默认配置与环境变量占位符
├── deploy/compose.yaml       # 本地联调 PostgreSQL (:55432) 与 Redis (:56379)
├── examples/agents/          # Agent 配置 JSON 样例
├── scripts/publish-agent.py  # 调用通用 Agent API 的发布脚本样例
├── charts/agentic-kit-api/   # Helm 部署模板（API、PostgreSQL、Redis）
└── testutil/sandboxe2e/      # K8s 沙箱端到端测试夹具

../agentic-kit-worker/        # 独立执行面配套仓库（需在同级目录配合联调或独立部署）
├── cmd/worker/main.go        # Worker 入口（默认监听 127.0.0.1:8081，可选连 Redis 推送实时增量）
├── internal/service/         # 生命周期装配、可选 Redis 瞬态流发布与 Worker 协程池
├── internal/engine/          # Claim 轮询、Eino Runner 执行、流式采集、Checkpoint、摘要压缩、溢出重派、子代理委派、Goal 验收与 read_step_result
├── internal/adapters/        # Eino Model/Tool 适配、标准 MCP 适配、凭据白名单、usage 防伪守卫、AIO 与 Sandbox 后端接入
└── conf/server/config.toml   # Worker 配置（禁止设置 [Pricing]，支持 DB / 可选 Redis / Worker / Credentials / Compaction / Evaluator）
```

### 新人常见修改导航

| 你想改什么 | 先读哪里 | 修改链路 | 改完验哪里 |
| --- | --- | --- | --- |
| **新增或调整 HTTP 接口 / SDK 字段** | [docs/api.md](docs/api.md)、[SDK README](sdk/go/agenticv1/README.md) | `pkg/*` 与 `sdk/go/agenticv1` -> `internal/app/*`（Controller 绑定 + Service）-> `service/routes.go` | `make doc` 重新生成 OpenAPI + HTTP 测试 |
| **修改表结构、Run 状态或租约恢复** | [docs/design.md](docs/design.md)、[docs/layout.md](docs/layout.md#共享持久化边界) | `internal/table` -> `internal/schema/init.go` -> `pkg/durable` -> 同步评估 Worker 调用方 | 本仓 `make integration` + Worker 跨进程恢复测试 |
| **调整上下文裁剪、预算或摘要策略** | [框架设计 §6.6](docs/refactoring-design.md#66-上下文管线与-memory-基础版)、[上下文投影经验](docs/lessons/context-projection.md) | `pkg/contextbuilder`（纯函数）+ `pkg/durable/compaction.go` -> Worker `internal/engine/{context,compaction,overflow}.go` | `pkg/contextbuilder` 单元测试 + Worker 溢出与摘要恢复测试 |
| **扩展 Memory 范围、权限或归档** | [docs/memory-design.md](docs/memory-design.md) | `pkg/memory` -> `internal/app/memory` -> `internal/client/{artifact,tenant}.go` | `internal/app/memory` 用例 + `TestArtifactIntegration*` |
| **修改沙箱控制面或跨 Run 检查点** | [LayerFS 保存与恢复](docs/sandbox-center/layerfs-save-verification.md)、[沙箱探针经验](docs/lessons/sandbox-runtime-probes.md) | `internal/sandboxcenter` + `pkg/durable/goal_iteration.go` + Worker `internal/adapters/sandbox` | `internal/sandboxcenter` 集成测试 + K8s E2E |

### 新人必守的核心工程约束

1. **资源所有权严禁越界**：`service.Build` 将数据库、Redis、Agent artifact 与 Forge 客户端交给 server-go Runtime 管理关闭，API 停止后台工作后关闭自持有的 Memory 归档客户端；组合应用调用 `service.New` 注入外部资源，由调用方在 `Stop` 后关闭。`durable.New` 借用连接池不负责关闭；`durable.Open` 专供独立 Worker 自建并拥有池。详见 [布局与依赖约束](docs/layout.md#装配与资源所有权)。
2. **单向依赖禁止反穿**：`pkg/durable`、`pkg/contextbuilder` 与公开 `pkg/*` 不允许导入 Gin、HTTP Controller 或外部业务模块；`artifact-go` 与 Forge SDK 类型仅允许封装在 `internal/client` 内。
3. **请求绑定保持来源隔离**：Controller 中必须先绑定 JSON/URI/Query，再用独立结构体绑定 Header 后手动赋值，防止不可信 Header 静默覆盖路径 ID 或已签名正文（见 [来源隔离经验](docs/lessons/server-go-migration.md#请求绑定要保留字段来源)）。
4. **快照存储与时间基准**：带 SHA-256 摘要的 `ContextSnapshot` 必须使用 `text` 列存储（禁止使用会重排 JSON 键和空白的 `jsonb`）；所有租约过期与截止判断统一使用 PostgreSQL `clock_timestamp()`，禁止依赖宿主机时钟。
5. **计费与副作用 Fail-Closed**：模型响应缺 `Usage` 或无法拆分输入/输出单价时拒绝记为 `0`；派发中崩溃保留 `InflightModel` 进入 `Blocked`，`Unsafe` 工具中断进入 `NeedsReview`，绝不静默重试。
6. **本地联调与独立交付分离**：同时改动 API 共享包与 Worker 时可在本地临时启用 `go.work` 联合调试；提交或发布验收前必须带 `GOWORK=off` 通过独立构建与测试（见 [server-go 迁移经验](docs/lessons/server-go-migration.md#发布依赖必须独立验证)）。

---

## 配置与启动

### 本地快速启动（API + Worker 完整闭环）

#### 1. 启动本地 PostgreSQL 与 Redis

```bash
docker compose -f deploy/compose.yaml up -d
```

容器就绪后提供 `PostgreSQL`（`127.0.0.1:55432`，库名 `agentic`，账号密码 `agentic` / `agentic_local`）与 `Redis`（`127.0.0.1:56379`），与 [conf/server/config.toml](conf/server/config.toml) 默认占位符一致。

#### 2. 配置必需环境变量并启动 API

启动需要 HTTP、PostgreSQL、Redis、`[AgentArtifact]` 与 `[Tenant]` 配置（参考 [conf/server/config.toml](conf/server/config.toml)）：

```bash
export DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export REDIS_ADDR='127.0.0.1:56379'
export LISTEN_ADDR='127.0.0.1:8080'
export AGENT_ARTIFACT_ENDPOINT='http://127.0.0.1:18080'
export TENANT_ENDPOINT='http://127.0.0.1:18081'         # 本机回环允许 HTTP，非回环必须 HTTPS
export TENANT_SERVICE_API_KEY='<至少32字符的API调用Forge凭据>'

GOWORK=off go run ./cmd/server -config conf/server/config.toml
```

* **配置加载规则**：依次按 `-config` 参数、`CONFIG_FILE`、`SERVER_CONFIG_PATH` 选择路径，未指定时回退 `conf/server/config.toml`。配置支持 TOML 中的 `${env:NAME:default}` 占位符映射，普通常量不会被同名环境变量覆盖。`Tenant.Timeout` 与 `AgentArtifact.Timeout` 单位均为毫秒。
* **数据库表初始化**：`service.Build` 在接收请求前自动调用 `durable.Store.InitSchema`（在 advisory lock 下初始化 16 张目标表，支持空库或空 schema 重复执行）。它不迁移旧无 tenant 数据、不回填正文，也不删除已有旧索引（如历史 `memory_operation_owner_key`）；旧库升级由部署方处理。组合部署调用 `service.New` 时由调用方负责初始化。本仓库不提供 `eval`、`migrate` 命令或 Helm 迁移任务。
* **离线韧性**：发布 Agent、读取版本正文和接受新任务依赖 `artifact-go`；已接受任务的执行与恢复只读取持久化快照。Redis 离线时 API 仍可启动，事件通过数据库正常回放。

#### 3. 启动配套 `agentic-kit-worker`

在同级目录 `../agentic-kit-worker` 终端中指向同一 PostgreSQL，并注入模型/工具凭据环境变量（名称须与 Agent 配置里的 `apiKeyEnv` / `tokenEnv` 一致）：

```bash
cd ../agentic-kit-worker
export DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export LISTEN_ADDR='127.0.0.1:8081'
# 按 Agent 配置注入模型与工具密钥，例如：export OPENAI_API_KEY='...'

# 独立验证或常规启动加 GOWORK=off；联调本地未发布共享包改动时去掉 GOWORK=off
GOWORK=off go run ./cmd/worker -config conf/server/config.toml -log-config conf/server/logger.toml
```

Worker 不访问 `artifact-go`，`[Redis]` 为可选配置（配置后推送逐 token / 思考过程瞬态增量，未配置或 Redis 不可用时仍正常运行权威事件链路）；配置中禁止出现带参数的 `[Pricing]`（价格由 API 冻结，在 Worker 配置会直接拒绝启动）。多租户生产环境建议同时在 Worker 配置 `[Credentials]` 环境变量白名单。

### 核心配置与鉴权契约

#### 入站鉴权与四类路由门槛

* **用户业务路由（`/v1/*`）**：使用 Forge SDK `MiddlewareAuthorization` 校验身份与所选租户，要求 `x-forge-tenant-id`（可选 `x-forge-project-id`），URL 不提供鉴权 scope，Action 为空（本层不定义项目业务权限政策）。middleware 将已核验 tenant/project 写入 `access.Scope`，路由末端再次校验租户非空；组合应用通过 `service.New` 注入 `Authentication` 时，缺 User 鉴权拒绝构造，middleware 未写入租户 scope 同样拒绝请求。`service.Build` 创建的 Forge 客户端由 Runtime 关闭。
* **内部执行 / Goal / Lease 路由（`/internal/v1/*`）**：仅在 `Tenant.AllowedPropagationServices` 非空时注册，使用 `MiddlewareInternalPropagation` 校验调用服务及白名单，并接收 `x-impersonate-target-tenant-id`、`x-impersonate-target-user-id` 与可选 `x-impersonate-target-project-id`；末端同样要求租户 scope。
* **沙箱控制面路由（`/v1/sandboxes/*`）**：`Sandbox.AllowedServices` 为空时不注册；启用后使用独立服务认证与白名单，申请的 `tenantId`/`ownerId` 来自可信调用方请求体，不从 Forge 用户 scope 推导。
* **外部回调路由（`/internal/v1/executions/{id}/callback`）**：默认不注册，仅在 `Callback.Enabled=true` 且已装配 callback 服务认证时注册；启动必须配置 `SigningSecret`、`AllowedSources`、`AllowedServices`，请求需同时通过服务认证、来源白名单与 HMAC-SHA256 校验。上述 HTTP 门槛不影响可信 Worker 直接调用 Store 时无 HTTP scope 的内部契约。
* **Worker 受控读取路由（`/internal/v1/worker/runs/{id}/skills/read`）**：默认不注册，仅在 `[Worker].AllowedServices` 非空并装配服务认证时注册；供 Worker 凭当前有效任务租约（`workerID` + `epoch` + 未过期租约与截止时间）按需读取该 Run 冻结清单内的 Skill 正文，API 使用 Run 自身冻结的权威 `tenant/project/owner` 通过 `artifact-go` 短期下载凭证读取。
* **内置探针与 SSE 要求**：server-go HTTPHost 自动提供免鉴权的 `/ping`、`/healthz`、`/livez`、`/readyz`、`/version` 探针；开启 `HTTPServer.SwaggerEnable` 可访问 `/swagger/`。`HTTPServer.WriteTimeout` 必须非负（[conf/server/config.toml](conf/server/config.toml#L9) 默认 `60000` 毫秒），SSE 端点通过 `stream.NewSSE` 清除单连接总写超时并按事件控制写期限，`APIBasePath` 为空。

#### Artifact 出站与可选 `[Artifact]`（Memory 归档）

Agent 发布与版本读取使用 API 自身的 `Tenant.ServiceAPIKey` 出站访问 `artifact-go v0.5.2`，只传播 middleware 已核验的 `user/tenant/project`，并将 `X-Resource-User-ID` 固定为同一用户以保留个人归属；入站 Authorization、Cookie、CSRF、Origin 均不转发，缺 tenant 或 actor 时拒绝远端调用。`artifact-go` 部署须将该 key 对应的服务名加入其传播白名单（用户 API Key 联调范围见 [验证记录](docs/verification.md#2026-11-17-agent-版本发布与-run-快照)）。

`[Artifact]` 用于可选的 `agent-memory` 远端归档，配置与 Helm values 对应如下：

| TOML 字段 | 环境变量 | Helm value | 默认值 |
| --- | --- | --- | --- |
| `Endpoint` | `ARTIFACT_ENDPOINT` | `artifact.endpoint` | 空 |
| `StoreID` | `ARTIFACT_STORE_ID` | `artifact.storeID` | `artifact` |
| `TenantID` | `ARTIFACT_TENANT_ID` | `artifact.tenantID` | 空，无公共租户兜底 |
| `BearerToken` | `ARTIFACT_BEARER_TOKEN` | `existingSecret` 中的 `ARTIFACT_BEARER_TOKEN` | 空 |
| `TimeoutSeconds` | `ARTIFACT_TIMEOUT_SECONDS` | `artifact.timeoutSeconds` | `30` |
| `MaxPayloadBytes` | `ARTIFACT_MAX_PAYLOAD_BYTES` | `artifact.maxPayloadBytes` | `262144` |

* `Endpoint` 为空时保持本地模式，Memory 写入本地投影且不生成远端版本引用，其余字段不参与校验。
* 配置 `Endpoint` 后，启动要求绝对 HTTP(S) URL、非空 `StoreID` / `TenantID` / `BearerToken`、1～60 秒超时及 1～262144 字节 payload 上限。`TimeoutSeconds` 是单次客户端请求超时，Archive 的集合创建与发布另受 1 分钟总预算约束。已有绑定不会随配置修改自动迁移，本地条目也不会自动补归档。
* `BearerToken` 证明调用 artifact 服务的部署身份（须在 Artifact 的 `forgeTenant.allowedPropagationServices` 白名单中）；`agent-memory` 归档仅通过受信服务入口访问，项目读权限由 Memory API 核验。Helm 将非敏感 values 渲染为显式 env（同名 `envFrom` Secret 不会覆盖），`BearerToken` 通过 `existingSecret` 注入而不写入 values。

#### 模型计价 `[Pricing]`

* 价格表配置在 API 的 `[Pricing]`（示例见 [conf/server/config.toml](conf/server/config.toml#L32-L43)）：`Models` 按模型名精确匹配，单价单位为 USD / 1M tokens；`CachedInputPerMillion` 省略时继承普通输入价，显式 `0` 表示免费；`Version` 留空时按排序后的价格表生成稳定摘要。
* API 在接受 Chat / Application Run 时冻结版本、模型、BaseURL 与单价：非空表缺目标模型时拒绝提交；空表冻结为仅 token 计量、费用记录为 `0`（不代表供应方免费，此时带 `MaxCostUSD > 0` 的 Goal 在模型或摘要调用前失败）。
* Worker 只读冻结快照，主模型与摘要共用同一模型及价格；幂等重放、Goal 续跑与 Child Run 继承原快照，API 改价只影响新提交。无冻结标记的旧 Run 会被 Worker 拒绝。费用预算无额度预占，不承诺在途并发下的严格硬上限，详见 [模型用量与恢复](docs/design.md#模型用量与恢复)。

#### 沙箱运行时 `[Sandbox]`

* 通过 `[Sandbox]` 启用，生产部署使用预建 JuiceFS CSI `ReadWriteMany` 检查点 PVC，并配置 `SANDBOX_WORKSPACE_STORAGE_CLASS` 与 `SANDBOX_WORKSPACE_ACCESS_MODE=ReadWriteMany`，使 Guard 与 Saver 可在不同节点挂载同一工作区。
* Helm 默认关闭沙箱；启用时需配置 API、Guard 和 Saver 镜像、kubeconfig 与完整 TOML 的 Secret、目标 `K8sContext` 与检查点 PVC。单节点环境可改用 `SANDBOX_CHECKPOINT_STORE_HOST_PATH`。部署、验收与回退步骤见 [LayerFS 保存与恢复](docs/sandbox-center/layerfs-save-verification.md#helm-部署与生产验收)。

---

## 调用接口

下面演示通过 Forge 会话创建并发布 Agent、建立会话、提交消息并订阅 SSE 事件流。运行前先设置有效 Forge 会话的 `FORGE_SESSION`、匹配的 `FORGE_CSRF_TOKEN`、允许的 `FORGE_ORIGIN` 与已授权的 `FORGE_TENANT_ID`（项目范围再加 `x-forge-project-id`；也可改用用户 API Key 鉴权）。

```bash
export API_URL='http://127.0.0.1:8080'

# 1. 创建 Agent 草稿
agent_id=$(curl --fail-with-body -sS "$API_URL/v1/agents" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' \
  --data-binary @examples/agents/agents.example.json | jq -r .id)

# 2. 发布草稿 revision=1 为不可变版本
curl --fail-with-body -sS "$API_URL/v1/agents/$agent_id/publish" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' -d '{"revision":1}'

# 3. 基于已发布 Agent 创建会话
session_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg agentID "$agent_id" '{agentID:$agentID,title:"第一次对话"}')" | jq -r .id)

# 4. 幂等提交用户消息并订阅 SSE 执行流
run_id=$(curl --fail-with-body -sS "$API_URL/v1/sessions/$session_id/messages" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: question-001' \
  -d '{"query":"请介绍一下你自己。"}' | jq -r .id)

curl -N "$API_URL/v1/runs/$run_id/events" \
  -H "Cookie: forge_session=$FORGE_SESSION" -H "X-CSRF-Token: $FORGE_CSRF_TOKEN" \
  -H "Origin: $FORGE_ORIGIN" -H "x-forge-tenant-id: $FORGE_TENANT_ID"
```

* **继续对话与版本编辑**：同会话续聊传入相同 `session_id` 与新的 `Idempotency-Key`；编辑草稿调用 `PUT /v1/agents/{id}`（带完整 `name`/`config` 与当前 `revision`），发布后新 Run 使用新版本，已提交任务保留原冻结快照。批处理发布可参考 [scripts/publish-agent.py](scripts/publish-agent.py)。
* **人工介入与恢复**：任务处于 `WaitingClarification` 时调用 `POST /v1/runs/{id}/answer`，`NeedsReview` 调用 `/resolve`，未知消费 `Blocked` 调用 `/unblock`（需传当前 `epoch` 与审计 `reason`）。完整 HTTP 契约见 [docs/api.md](docs/api.md)。
* **Go SDK 接入**：外部服务导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`，覆盖 Agent、Session、Run、Execution、Memory 与 Sandbox 全部能力组；所有接口接收标准 `context.Context` 与请求结构体，调用方负责关闭客户端和 SSE 响应体。详见 [SDK README](sdk/go/agenticv1/README.md)。

---

## 验证与部署

### 1. 基础检查与 OpenAPI 文档生成

```bash
make test         # 单元测试（无外部依赖）
make vet          # 静态检查
make build        # 构建二进制 bin/server
make install-doc  # 安装 swag v1.8.4
make doc          # 从 Controller 注释与 SDK/pkg 标签生成 doc/openapi（禁止手改产物）
```

### 2. API 集成验证（PostgreSQL + Redis + Artifact）

涉及持久化、SSE、Memory 或生命周期变更时，必须连接隔离的 PostgreSQL 与 Redis 运行全量竞态测试：

```bash
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export TEST_REDIS_URL='redis://127.0.0.1:56379/0'
export ARTIFACT_TEST_PG_DSN="$TEST_DATABASE_URL"

GOWORK=off make integration
GOWORK=off CGO_ENABLED=0 make build
```

* 数据库测试会自动创建随机 schema 并在结束时清理（账户需具备建 schema 权限）；Redis 测试使用独立 run key。未配置外部依赖导致用例跳过，不代表集成验证通过。
* 设置 `ARTIFACT_TEST_PG_DSN` 会启用 `TestArtifactIntegration*`（启动真实 artifact HTTP 服务，验证 Bearer 认证、版本固定、幂等、跨作用域隔离与代理丢响应后的 Pending 恢复）；单独复验可运行：
  ```bash
  GOWORK=off go test -v -count=1 -run '^TestArtifactIntegration' ./internal/app/memory
  ```

### 3. API + Worker 联合跨进程恢复验证

每当修改 `pkg/durable`、`pkg/contextbuilder`、`pkg/agent`、`pkg/execution` 共享契约或恢复状态机时，必须进入 `../agentic-kit-worker` 执行真实 Worker 集成测试（涵盖 `cmd/worker` 进程强杀恢复、`Unblock` 人工解封、上下文溢出缩小重试及跨 Run 沙箱验收）：

```bash
cd ../agentic-kit-worker
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
make integration
```

> 本地修改尚未发布到 `go.mod` 时可用 `go.work` 联合跑通，但交付前必须先把依赖版本固定并关闭 workspace（`GOWORK=off`）独立验证通过。

### 4. 容器构建与 Helm 部署

* [Dockerfile](Dockerfile) 仅构建并打包 `server` 二进制与默认配置，拉取私有依赖使用 BuildKit secret `github_netrc`。
* [Helm Chart](charts/agentic-kit-api) 部署 API、PostgreSQL 与 Redis，通过 `existingSecret` 与 `database.existingSecret` 注入配置，不运行迁移 `initContainer`。数据库初始化与历史结构升级由部署方负责，生产环境严禁使用示例数据库连接串。

---

## 文档入口

* **开发导航与经验**
  * [文档路由表](AGENTS.md)：按修改场景索引开工前必读文档。
  * [领域术语](CONTEXT.md)：资产、身份、作用域、执行、Memory 与上下文核心术语。
  * [项目经验索引](docs/lessons/README.md)：已验证的踩坑记录与定位指南。
  * [历史验证记录](docs/verification.md)：历次联合验收范围与测试证据（旧路径不作为当前约定）。
* **架构、契约与设计**
  * [项目布局与依赖约束](docs/layout.md)：目录职责、资源装配所有权、持久化边界与 SDK 规范。
  * [HTTP 契约](docs/api.md) 与 [Go SDK 用法](sdk/go/agenticv1/README.md)：对外接口与客户端调用说明。
  * [恢复设计](docs/design.md)：执行权、`epoch` 隔离、步骤重放、事件回放与模型用量/恢复规则。
  * [资产与 artifact 设计](docs/agent-asset-artifact-design.md)：Agent 不可变版本发布与 Run 预取冻结机制。
  * [Memory 设计](docs/memory-design.md)：人工维护、作用域与受众隔离、artifact 归档编排及后续规划。
  * [LayerFS 保存与恢复](docs/sandbox-center/layerfs-save-verification.md)：沙箱跨 Run 工作区保存校验与 Helm 生产验收。
  * [框架重构设计](docs/refactoring-design.md) 与 [核心流程图解](docs/core-flows.html)：通用框架目标结构与上下文/恢复流程。
  * [与 agentic-kit 功能对比](docs/agentic-kit-comparison.md)：与原一体化仓库最新活跃分支的异同、优劣与待补齐能力进度基线。
