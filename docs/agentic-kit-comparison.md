# agentic-kit-api 与 agentic-kit 功能对比

本文档维护当前仓库（含配套执行面 `agentic-kit-worker`）与一体化仓库 `~/Code/agentic-kit` 最新活跃开发分支的功能异同、架构取舍、优劣评估及待补齐能力进度。正文始终反映最近一次核对结论；上游分支更新或内核补齐能力落地后，按第七节流程执行对比并同步更新本文档与第六节进度表。

## 当前对比基线

| 仓库 | 分支 / 引用 | 提交 SHA | 提交时间 | 备注 |
| --- | --- | --- | --- | --- |
| `agentic-kit-api` | `main` | `54922f0` | `2026-11-24 00:36:23 +0800` | 已合入 PR #27（`feat/context-v4-compression`） |
| `agentic-kit-worker` | `main` | `0434172` | — | 已合入 PR #9（`feat/context-v4-compression-worker`） |
| `~/Code/agentic-kit` | `origin/dev-v2.0.0` | `c9ecfe80` | `2026-11-23 18:13:44 +0800` | 最新主开发线（`skill update`） |
| `~/Code/agentic-kit` | `origin/dev-2.0.0` | `18ab6c92` | `2026-11-23 07:16:03 +0000` | 同日分支，合入 `chrome-devtools-cli` 内置技能（与 `origin/dev-v2.0.0` 相差 1 vs 3 个提交） |

最近对比日期：`2026-11-24`（说明：`~/Code/agentic-kit` 本地工作区当前停留在 `release-v1.5.2` `c714dd3f`，对比全部以远端活跃分支 `origin/dev-v2.0.0` 及其 `origin/dev-2.0.0` 增量为准）。

---

## 一、定位结论与选型边界

1. **核心定位差异**：
   - **`~/Code/agentic-kit`（v2.0.0 开发线）是一体化智能体与工作流全栈业务平台**：在同一单体 Go 服务内集成了 Agent 对话、多智能体（Multi-Agent）、自研 `compose` DAG 工作流图引擎、技能市场与 13+ 内置技能包、MCP/OpenAPI 插件绑定、RAG 知识库与多源数据库/本体工具、定时自动化任务、飞书/钉钉/企微/蓝信/Claw 等 IM 渠道、沙箱 Playwright 浏览器以及 OpenSearch 会话全文检索，面向内部平台生态（AppForge / Prism / AuthCenter）提供开箱即用的产品能力。
   - **`agentic-kit-api`（含配套 `agentic-kit-worker`）是剥离了垂直产品语义的通用持久化 Agent 控制面与执行内核**：仓库已移除工作流、技能包、知识库、IM 渠道与场景模板等产品层代码（见 [框架重构设计](refactoring-design.md#1-目标与范围)），专注于跨进程崩溃接管、冻结计价与未知消费阻断、不可变版本与上下文快照校验、多租户与输出受众权限隔离、长程目标（Goal）自动验收续跑，以及 LayerFS / JuiceFS RWX 工作区跨轮次快照恢复。
2. **选型与组合建议**：
   - 需要快速交付企业门户、低代码工作流编排、多智能体协同、文档生成技能（Word/PDF/PPT/Excel）及飞书/钉钉机器人接入时，可直接基于 `~/Code/agentic-kit`（`dev-v2.0.0`）开展业务交付。
   - 需要构建高可靠长程自主任务（如软件工程 Agent、无人值守研发流水线、多租户 SaaS 底座），要求进程崩溃后不重复触发副作用工具、不丢失事件、严格核算每次模型成本且跨轮继承完整文件系统状态时，应选用 `agentic-kit-api` + `agentic-kit-worker` 内核，并将上层产品工作流、模板与渠道作为独立外部应用通过 [Go SDK](../sdk/go/agenticv1/README.md) 接入。

---

## 二、全景功能矩阵对比

| 对比维度 | 当前仓库 `agentic-kit-api`（`main`，含配套 `worker`） | `~/Code/agentic-kit`（`origin/dev-v2.0.0` / `origin/dev-2.0.0`） |
| --- | --- | --- |
| **架构形态** | **控制面 / 执行面物理分离**：API 负责鉴权、资产、提交、事件回放与沙箱控制；独立 `worker` 轮询 PostgreSQL 领取任务，可独立扩缩容（[README](../README.md)） | **单体一体化运行时**：API 与 Eino 对话/工作流执行同进程运行（另有可选独立入口 `cmd/automation-worker` 执行定时自动化任务） |
| **接口风格与规模** | 标准 RESTful（`/v1/*`、`/internal/v1/*`），共 36 条控制面与业务路由（[service/routes.go](../service/routes.go)），配零框架依赖 SDK `sdk/go/agenticv1` | RPC 风格内部接口（`POST /agentickit/internal/v1/<Action>`），90+ 条路由（`internal/app/{agent,workflow,skill,channel,sandboxmanager}`），配 `sdk/go/agentickitv1` 与 `internalsdk` |
| **多租户与鉴权** | 接入 `forge-tenant-api v0.13.0`：强制 `x-forge-tenant-id`、内部服务传播白名单、沙箱/回调独立服务鉴权、项目级实时读写鉴权（[布局与依赖约束](layout.md#http-与业务职责)） | 接入内部 `auth-center` 中间件与 `internal/pkg/authapikey`，绑定内部平台用户、组织（Org）与项目（ProjectID）模型 |
| **Agent 资产与版本** | 本地 `agent` 表存草稿与乐观锁 `revision`；发布正文存入外部 `artifact-go`（`protected=true` 不可变版本），Run 提交时冻结配置快照（[资产与 artifact 设计](agent-asset-artifact-design.md)） | 本地数据库表维护 Agent 基础信息、CoreFile（Agents/Identity/Soul 三合一）、模型配置、草稿与发布态配置 |
| **进程崩溃与容灾** | **跨进程无损接管**：DB 租约 + `epoch` 隔离僵尸写入 + Eino 原生 checkpoint + `durable_step` 幂等重放/副作用拦截（[恢复设计](design.md)） | **孤儿回收置失败**：后台 `runreaper` 扫描重启遗留的 `Running`/`Created` Run，结合 DB 消息活动防误杀后统一收口为 `Failed`（`service/service.go`） |
| **实时事件流（SSE）** | **PostgreSQL 权威事实 + 事务 Outbox 推 Redis Stream**：Redis 宕机可正常启动并自动降级为 1 秒 DB 轮询回放（[events.go](../internal/app/run/events.go)） | **Redis Stream 主通道（`HeadlessEmitter`）+ DB 历史**：支持会话级订阅、Run 代次隔离、`keepAlive` 与全局通知 SSE |
| **任务编排上限** | 单 Agent ReAct + **长程 Goal 状态机**（自动验收、预算控制、`StallMark` 死循环熔断、运行中 `Steer` 纠偏）+ 父子 `Child Run` + 外部 HMAC `Callback`（[api.md](api.md)） | 单 Agent ReAct + **Multi-Agent 多智能体编排**（主从成员管理、`calledAgentID` 召唤专家、共享沙箱与写权限收窄）+ **对话分支 Fork** + 会话级 `PlanList` |
| **可视化工作流引擎** | **明确不做**（交由上层场景应用实现，见 [框架重构设计](refactoring-design.md#31-场景与通用能力的边界)） | **内置完整 `compose` 图引擎**：支持 LLM、Agent、子工作流、循环（Break/Continue）、批处理、意图识别、Text2SQL、DB 查询、知识库、本体（Ontology）、图表、代码沙箱与断点续传 |
| **上下文构建与压缩** | API 侧 `pkg/contextbuilder` 实现纯函数 `context-v4` 与 `cjk-weighted-v1`（裁 Memory、折叠历史工具链、删旧轮、预览与先成功再失败存根化、可选整轮淘汰与固定初始提问），快照以 `text` 存储并校验 SHA-256；配套 `worker` 统一装配模型输入、支持滚动分块摘要计费与 3 次失败熔断，且仅在 `BoundaryComplete` 推进会话水位 `compacted_through_seq`（[验证记录](verification.md#2026-11-23-上下文压缩-v4-与溢出自愈)） | Eino 中间件 `compression`（v2 重构版）：工具结果三档降级（`full` -> `preview` -> `stub`）后实时调用 LLM 生成九节结构化滚动摘要（拼接最多 20 条会话文件索引及关键块），兜底丢弃最旧轮次；支持模型上下文超限时按 0.7 倍收缩重试（`docs/agent-context-compression-design.md`） |
| **记忆（Memory）体系** | **强治理显式记忆**：支持 `OwnerPrivate`、`ActorPrivate`、`ActorProject` 及受众隔离，每次项目读取/写入/召回实时校验 Forge 权限，显式 `Recall` 同事务词项召回，可选 `artifact-go` 归档与 30s Pending 恢复；`ProjectShared`、自动提炼与向量检索尚未实施（[Memory 设计](memory-design.md)） | **自动化个人记忆**：提供模型工具 `SaveUserMemory` 实时写入，后台 `usermemory.Curator` 每 24 小时调用 LLM 自动去重、消解冲突与剔除失效项（`internal/app/agent/usermemory/curator.go`）；另配 OpenSearch 跨会话历史消息检索工具 |
| **技能（Skill）体系** | **明确不做** | **完整技能平台**：支持技能 zip 导入导出、收藏、市场技能与 13/14 个嵌入式内置技能包（`docx`/`pdf`/`pptx`/`xlsx`/`data-visualization`/`ui-ux-design`/`browser-playbook`/`chrome-devtools-cli` 等），通过 Eino skill middleware 注入提示词与沙箱 |
| **工具与外部资源生态** | 仅支持通用 **HTTP POST 工具** 与 **Filesystem（AIO/Sandbox）** 工具集，明确拒绝专有平台协议（[api.md](api.md)） | 内置丰富工具目录：文件读写编排、产物自动上传 S3、澄清提问、计划清单、会话搜索、自动化管理、Prism 知识库/多源数据库查询/本体/词库工具、AppForge MCP 与 OpenAPI 插件绑定 |
| **沙箱与浏览器能力** | 自研 K8s `sandboxcenter`：零特权 `Guard` Pod（UDS 控制、命令回执去重 `OutcomeUnknown`）+ **JuiceFS RWX 与 LayerFS 跨 Run 增量保存、独立 `Verifier` 校验与 `initContainer` 原子恢复**（[LayerFS 保存与恢复](sandbox-center/layerfs-save-verification.md)）；无内置浏览器中间件 | 自研 K8s `sandboxmanager` + AIO SDK：支持实例池复用（`ReuseKey`）、显式唤醒/重启、同步/异步长命令（最长 24h）、S3 路径双向映射、公共只读卷，以及内置 **Playwright 沙箱浏览器中间件**（含本地 `file://` 访问与内网地址拦截） |
| **外部渠道与辅助场景** | **明确不做**（无 IM 渠道、无 Cron 调度、无写作模板） | 内置 **飞书、钉钉、企业微信、蓝信、Claw** 渠道连接器与事件总线（`internal/app/channel`、`internal/pkg/channel`）；内置定时自动化任务（`automation`）；内置写作助手（`WritingAgents`） |
| **计费治理与可观测性** | **严格计费闭环**：API 提交期冻结单价快照，`durable_usage` 按 `(run_id, turn, call)` 幂等记账，派发中崩溃触发 `InflightModel` 未知消费阻断（`Blocked`）与人工审计 `Unblock`（[pkg/durable/usage.go](../pkg/durable/usage.go)） | 侧重可观测与运营统计：集成 OpenTelemetry（Trace/Metric/Log）、CozeLoop 链路追踪、Run 级 Token/缓存命中统计日志、专家使用量统计与 `service-monitor` 上报；无 USD 账本与未知消费熔断 |

---

## 三、技术底座与设计共识（相同点）

两个仓库出自同一技术演进脉络，在底层运行契约与工程方法上保留了五项核心共识：

1. **统一的语言与推理内核**：均基于 Go 与各自版本的 `server-go` 框架构建，单 Agent 推理循环均采用 CloudWeGo **Eino `v0.9.2`** ADK（`ChatModelAgent` + `Runner` + `CheckPointStore`），不自研第二套 ReAct 循环。
2. **上下文分级降级与原文可恢复思想**：双方都放弃了早期粗暴截断导致的模型“失忆”或工具调用配对损坏问题，统一采用“会话摘要水位游标 + 工具结果分级降级（`full -> preview -> stub`）+ 数据库保留完整原文并提供按需回读句柄（`read_step_result` / `restore_tool`）”的模式。
3. **中文友好的窗口预算估算**：均针对中英文混排引入 CJK 加权估算逻辑（避免纯字节或纯字符计数在中文长文本下出现较大偏差），并在模型返回上下文超限时执行缩小重试。
4. **基于 Checkpoint 的人工澄清暂停恢复**：模型调用澄清工具向用户提问时，双方均持久化 Eino checkpoint 并释放当前执行协程，待用户回答到达后再从断点恢复执行。
5. **Kubernetes 原生沙箱隔离与租约管理**：均内置基于 `client-go` 的 Kubernetes 沙箱控制模块（[internal/sandboxcenter](../internal/sandboxcenter/handler.go) 与 `internal/app/sandboxmanager`），包含网络策略隔离、租约续期与异步/长耗时命令管控。

---

## 四、核心机制差异详解（7 大分水岭）

### 1. 执行可靠性：跨进程无缝接管 vs 孤儿任务回收失败
* **`agentic-kit-api`**：[pkg/durable/lease.go](../pkg/durable/lease.go) 通过 PostgreSQL `FOR UPDATE SKIP LOCKED` 与数据库时钟 `clock_timestamp()` 发放租约，所有写操作强制校验 `(run_id, worker_id, epoch)` 隔离僵尸 Worker。工具调用前由 [pkg/durable/step.go](../pkg/durable/step.go) 写入 `StepIntent`。进程被强杀后，替代 Worker 在租约到期后自动接管：已完成的工具直接回放结果（`StepReplay`），不重复触发外部请求；结果不明的非幂等 `Unsafe` 工具自动转入 `NeedsReview` 等待人工确认。
* **`~/Code/agentic-kit`（`dev-v2.0.0`）**：对话在 HTTP 服务副本内存的 `runmanager` 并发槽中运行。服务滚动发版或进程异常退出后，由后台 `runreaper` 扫描遗留的 `Running`/`Created` 任务（结合 DB 消息活动防误杀）并将主 Run 及其子 Run 收口为 `Failed`，不支持跨进程断点接管。

### 2. 事件投递一致性：事务性 Outbox vs Redis Stream 直写
* **`agentic-kit-api`**：Run 状态变更、会话消息与 `durable_event` 在同一 PostgreSQL 事务内原子提交（[pkg/durable/events.go](../pkg/durable/events.go)），再由后台 [service/outbox.go](../service/outbox.go) 按单 Run 严格保序中继到 Redis Stream。Redis 离线时 API 仍可启动，[internal/app/run/events.go](../internal/app/run/events.go) 自动降级为每秒轮询数据库按游标回放，做到零丢事件。
* **`~/Code/agentic-kit`（`dev-v2.0.0`）**：通过 `HeadlessEmitter` 直接向 Redis Stream 写入逐 token 输出、思考流与子智能体进度事件，终端交互实时性更佳，但实时推送强依赖 Redis 可用性。

### 3. 复杂任务范式：长程自治 Goal vs 多智能体协同 + DAG 工作流
* **`agentic-kit-api` 纵向深耕“单智能体长程自治闭环”**：[pkg/durable/goal.go](../pkg/durable/goal.go) 提供 `Goal` 控制面，支持多轮 Run 自动续跑直到沙箱命令验收通过（配独立 `evaluation_lease` 防旧 Worker 越权提交），内置 Token/USD/WallClock 三维预算、连续 3 轮同 `StallMark` 死循环自动熔断、运行中无损注入纠偏指令（`Steer` 消费与新 checkpoint 同事务落库），以及服务认证 + 来源白名单 + HMAC-SHA256 三重校验的外部异步回调（[callback.go](../internal/app/run/callback.go)）。
* **`~/Code/agentic-kit` 横向铺开“多智能体编排与低代码工作流”**：`internal/app/agent/controller.go` 提供完整的 Multi-Agent 主从架构（子成员独立配置模型、CoreFile、技能、插件与知识库，运行时复用主 Agent 沙箱并收窄写权限）、会话分支复制（`ForkAgentSession`）及会话级计划清单（`PlanList`）；同时在 `internal/pkg/compose` 自研了覆盖知识问答、Text2SQL、批处理与循环编排的 DAG 工作流图引擎。

### 4. 上下文构建：纯函数可验证快照 vs 在线 LLM 九节结构化摘要
* **`agentic-kit-api`**：[pkg/contextbuilder/build.go](../pkg/contextbuilder/build.go) 保持纯函数零 I/O，提交期即可确定性预检并在超限时返回 HTTP 400 `ContextBudgetExceeded`；编码后的 `ContextSnapshot` 以 `text` 列存储并校验 SHA-256 摘要（[snapshot.go](../pkg/contextbuilder/snapshot.go)），确保崩溃恢复与审计重放时的字节级一致性。配套 Worker 则负责模型侧滚动分块摘要、计费与 3 次连续摘要失败熔断。
* **`~/Code/agentic-kit`（`dev-v2.0.0`）**：将单个超长 Run 内部的实时压缩作为重点重构方向（`internal/app/agent/conversation/compression/`），支持在百次工具调用的同一轮 Run 内实时触发摘要模型生成九节结构化摘要，并由代码强制拼接最近文件索引与关键块，对超长交互对话的语义保留密度更高，但运行期会引入额外的摘要模型调用延迟。

### 5. 记忆治理：多级受众隔离与不可变归档 vs 零配置自动整理
* **`agentic-kit-api`**：[internal/app/memory/service.go](../internal/app/memory/service.go) 围绕企业合规与可追溯性设计，支持 `OwnerPrivate`、`ActorPrivate`、`ActorProject`（区分 `Private`/`Project` 输出可见性，避免私人偏好泄漏进项目共享任务），每次项目操作实时校验 Forge 权限，并通过 `MemoryOperation` 持久回执将变更归档至 `artifact-go` 形成不可变版本历史，支持按版本读取与显式 `Restore`。代价是目前仅支持人工维护与确定性词项召回，尚未实现模型自动提炼与语义检索。
* **`~/Code/agentic-kit`（`dev-v2.0.0`）**：`internal/app/agent/usermemory/curator.go` 聚焦终端用户零配置体验，由模型调用 `SaveUserMemory` 实时记录用户记忆，后台每 24 小时批量唤醒 LLM 自动去重、以最新时间覆盖冲突并删除已声明失效的旧条目；但缺少项目级可见性隔离、乐观锁版本树与外部不可变审计归档。

### 6. 沙箱工程：LayerFS 跨轮持久工作区 vs 沙箱池复用与浏览器一体化
* **`agentic-kit-api`**：针对跨多个 Run 继承代码工作区的需求，构建了零特权 `Guard`（`uid=65534`、`CapEff=0`、只读根、UDS 控制通道）+ `Saver` + 独立 `Verifier` + `initContainer` 的 **LayerFS + JuiceFS RWX** 流水线（[LayerFS 保存与恢复](sandbox-center/layerfs-save-verification.md)），确保恢复失败不暴露半成品工作区，并通过命令回执去重（`X-Sandbox-Operation-ID` / `502 OutcomeUnknown`）解决网络断连重查问题。
* **`~/Code/agentic-kit`（`dev-v2.0.0`）**：侧重会话交互效率与多模态工具体验，支持按 `ReuseKey` 复用热沙箱、显式唤醒/重启、将沙箱生成产物自动上传并映射为 S3 地址，同时内置 Playwright 沙箱浏览器中间件，支持模型直接访问沙箱本地 `file://` 页面并拦截内网地址。

### 7. 成本与计费安全：Fail-Closed 账本 vs 观测统计
* **`agentic-kit-api`**：[pkg/durable/usage.go](../pkg/durable/usage.go) 将每次模型/摘要调用视为不可逆外部消费。API 提交时冻结模型单价快照；Worker 派发模型请求前先落库 `InflightModel` 标记，若进程在响应记账前崩溃，接管进程直接提交 `BoundaryBlock` 将 Run 置为 `Blocked`（绝不盲目重试导致重复计费，也不把缺失 usage 当作 0 放行），必须通过 `POST /v1/runs/:id/unblock` 附人工审计理由显式解封。
* **`~/Code/agentic-kit`（`dev-v2.0.0`）**：记录 Run 级 Token 与缓存命中日志并对接 CozeLoop / OpenTelemetry 与 `service-monitor`，模型瞬时失败按统一重试预算自动恢复重试，优先保障终端对话连通率。

---

## 五、优劣势评估

### 当前仓库 `agentic-kit-api`（含配套 `worker`）

* **优势**：
  1. **故障恢复与状态一致性严密**：DB 租约 `epoch` 隔离、工具意图落库、事务性 Outbox 与 Redis 离线降级形成闭环，任意时刻杀进程均不会重复执行副作用工具或丢失事件。
  2. **长程任务与成本风控完备**：`Goal` 验收迭代、Stall 熔断、冻结单价、幂等账本与未知消费 `Blocked` 机制，能够满足高成本、长耗时、无人值守任务的生产容错要求。
  3. **架构解耦干净且安全边界明确**：零垂直场景耦合，Forge 租户/项目权限、Memory 受众隔离、Callback HMAC 验签与 LayerFS 内容寻址校验边界清晰，Go SDK 纯净易复用。
* **局限**：
  1. **产品层开箱能力较少**：不内置工作流图引擎、技能包市场、RAG 知识库接入、Playwright 浏览器工具与 IM 机器人渠道，业务方需在外部应用层自行组装。
  2. **实时交互与智能辅助能力偏克制**：当前 Worker 采用整轮 `Generate` 落库而非逐 token 流式推送；Memory 尚未实现自动提炼、语义向量检索与 `ProjectShared` 协同维护；也不内置 Multi-Agent 编排。
  3. **运行期外部依赖门槛较高**：完整运行依赖 `artifact-go` 与 `forge-tenant-api`，生产级跨节点沙箱保存恢复需配置 JuiceFS `ReadWriteMany` 存储类。

### `~/Code/agentic-kit`（`origin/dev-v2.0.0` / `origin/dev-2.0.0`）

* **优势**：
  1. **产品功能矩阵完备**：一站式覆盖单智能体、Multi-Agent、可视化 Workflow、13/14 个高质量内置技能、MCP/OpenAPI 插件、知识库/数据库/本体工具、定时任务与主流企业 IM 渠道。
  2. **终端交互体验成熟**：具备逐 token 流式 SSE、思考过程展示、对话分支 Fork、OpenSearch 跨会话全文高亮搜索、LLM 九节滚动摘要、每日记忆整理与内置 Playwright 浏览器。
* **局限**：
  1. **单体耦合重、独立移植成本高**：深度绑定内部十余个基础设施客户端（AppForge、Prism、AuthCenter、Compute、Storage 等），难以脱离现有内网底座独立部署或作为通用内核复用。
  2. **缺少跨进程断点接管与强一致成本账本**：进程重启后在途 Run 由 `runreaper` 统一置为 `Failed`，缺少 `durable_step` 级副作用防重放、Outbox 离线兜底与未知消费熔断。
  3. **维护与回归半径大**：近百个 RPC 接口与多种业务形态共处同一代码库与数据模型中，任何核心表或执行链改动均涉及较大的跨模块回归范围。

---

## 六、内核待补齐能力优先级与进度跟踪

评估 `agentic-kit-api + agentic-kit-worker` 待补齐能力的原则是**守住通用内核边界**（见 [框架重构设计](refactoring-design.md#31-场景与通用能力的边界)）：工作流图引擎、IM 渠道、定时任务与垂直业务模板保留在外部场景层，内核优先补齐直接影响交互体验、工具生态接入、多智能体任务拆解与生产可观测性的通用运行时原语。

### 进度跟踪表

状态约定：`待实施` / `部分就绪` / `进行中` / `已完成`

| 优先级 | 编号 | 待补齐能力 | 涉及端 | 当前状态 | 备注 / 验收证据 |
| --- | --- | --- | --- | --- | --- |
| **P0** | 1 | 逐 Token 与思考过程（Thinking）实时流式输出 | API + Worker | `已完成` | API `v0.7.1` + Worker `v0.2.1` 双仓落地双流 SSE（`ModelReasoningDelta`/`ModelContentDelta`、无 `id:` 帧、中途补发、`?live=false`）、`model.stream` 开关与首帧前 `stream_options` 400 自动回退、可配置空闲超时及可选 Redis 积压合批 |
| **P0** | 2 | 厂商中立标准 MCP 工具协议支持 | API + Worker | `待实施` | 支持 Streamable HTTP MCP 发现与步骤策略映射 |
| **P0** | 3 | Worker 侧子智能体（Sub-Agent / `ChildRun`）执行闭环 | API + Worker | `部分就绪` | API [pkg/durable/subtask.go](../pkg/durable/subtask.go) 已实现 `SpawnChildRun`、计价继承与级联收口；Worker 侧待接入委派工具与版本冻结 |
| **P0** | 4 | 多模态附件输入与产物文件索引契约 | API + Worker + SDK | `待实施` | 复用 `artifact-go` 附件引用，补齐上下文文件索引块 |
| **P1** | 5 | 按需加载的通用 Skill 挂载协议（不含技能市场） | API + Worker | `待实施` | 目录摘要注入与按需读取不可变技能包 |
| **P1** | 6 | 会话分支（Session Fork）与工作区检查点联动分叉 | API + SDK | `待实施` | 按历史消息序号分叉会话并关联 `restoreFromSandboxId` |
| **P1** | 7 | 沙箱会话级热复用（Idle TTL）与受限浏览器工具集 | API + Worker | `待实施` | 降低高频对话 Pod 冷启动耗时，补充内网拦截浏览器能力 |
| **P1** | 8 | Memory 演进：`ProjectShared`、受控记忆提议与混合检索 | API | `待实施` | 补齐项目共享读写授权、候选提议流程及 PostgreSQL 全文/向量召回 |
| **P2** | 9 | 明确零消费场景的模型限流/瞬时网络退避重试 | Worker | `待实施` | 与未知消费 `Blocked` 隔离，仅对明确未计费瞬态失败自动退避重试 |
| **P2** | 10 | 跨 API-DB-Worker 全链路 OpenTelemetry 与队列指标 | API + Worker | `待实施` | 透传 `traceparent`，暴露队列深度、租约续期与 Outbox 滞后 Prometheus 指标 |
| **P2** | 11 | 异常滞留态（`Blocked` / `SaveBlocked` / `Pending`）巡检与会话检索 | API | `待实施` | 补齐内部运维只读巡检列表与会话基础筛选接口 |

### 各项落地方向

* **P0（近期必补，打通核心交互与通用接入）**
  1. **逐 Token 与思考流输出（`P0-1`）**：当前 Worker 仅执行同步 `Generate`，SSE 仅推送步骤级事件。建议 Worker 切换为带 `include_usage` 的流式调用，将增量文本与思考过程直接推送到 Redis Stream 供 [events.go](../internal/app/run/events.go) 实时转发，不写入 PostgreSQL `durable_event` 表；单轮结束后再合并完整消息与 usage 走既有 `RecordUsageWithCost` 与事务 Outbox 落库，兼顾低首字延迟与计费恢复一致性。
  2. **标准 MCP 工具协议（`P0-2`）**：当前 [types.go](../pkg/agent/types.go) 仅允许空或 `HTTP` 单接口工具配置。在保持拒绝 `AppForgeMCP` 等专有协议的前提下，增加标准 Streamable HTTP MCP 源配置、TokenEnv 绑定、工具白名单及工具级 `ReadOnly`/`Idempotent`/`Unsafe` 策略映射，纳入现有参数校验与 `durable_step` 重放保护。
  3. **Worker 子智能体执行闭环（`P0-3`）**：API 持久化层已在 [subtask.go](../pkg/durable/subtask.go) 完整支持 `SpawnChildRun`、计价快照继承与父子唤醒/级联取消，但 Worker 尚无委派入口。需在 `AgentConfig` 支持子 Agent 引用列表、提交期冻结子版本，并在 Worker 内置委派工具调用 `SpawnChildRun`，使子任务天然具备独立上下文窗口与跨 Worker 并发能力。
  4. **多模态附件与产物索引（`P0-4`）**：在消息与执行请求中增加基于 `artifact-go` 的类型化附件引用（图片/文档 URI、媒体类型与大小校验），Worker 映射为 Eino 多模态消息；同时在沙箱工具与 [pkg/contextbuilder/build.go](../pkg/contextbuilder/build.go) 中维护关键产物文件索引块，避免长对话压缩后遗忘已生成文件路径。
* **P1（中期高优，强化长程协同与上下文效率）**
  5. **通用 Skill 挂载协议（`P1-5`）**：不引入技能市场与收藏夹产品接口，仅支持在 `AgentConfig` 引用 `artifact-go` 中的不可变技能包版本；上下文装配时注入简短目录摘要，提供只读工具按需加载完整 `SKILL.md` 与辅助脚本，并在上下文压缩中保护已加载技能正文。
  6. **会话分支与检查点联动分叉（`P1-6`）**：为 [session/router.go](../internal/app/session/router.go) 增加 `POST /v1/sessions/:id/fork`，按指定截止序号在单事务内复制 `ResultScope`、压缩水位摘要与历史消息；若该轮次存在 LayerFS 沙箱检查点，同步绑定 `restoreFromSandboxId`。
  7. **沙箱会话级热复用与受限浏览器工具（`P1-7`）**：在现有跨 Run 保存恢复流水线（[LayerFS 保存与恢复](sandbox-center/layerfs-save-verification.md)）之外，为高频交互会话提供短时 Idle TTL 热保活租约，空闲超时后再走 LayerFS 保存释放；并在 Worker `toolsets` 中扩展内置 SSRF 私网拦截的受限浏览器工具集。
  8. **Memory 体系演进（`P1-8`）**：按 [Memory 设计](memory-design.md) 补齐基于 Forge 读写权限的 `ProjectShared` 共享记忆维护与召回；引入受控的候选记忆提议（Memory Proposal）流程（个人可自动生效、项目共享需人工确认）；结合 PostgreSQL `pg_trgm` / 全文检索或可选 `pgvector` 增强语义召回，避免引入 OpenSearch 额外依赖。
* **P2（规模化生产运维护栏）**
  9. **明确零消费模型重试（`P2-9`）**：保持响应中断时 `InflightModel` fail-closed `Blocked` 保护不变；针对 TCP 建连失败、HTTP `429` 等明确未产生计费的瞬时错误，在 Worker 清除在途标记后执行指数退避重试，避免长程 `Goal` 因限流抖动直接终止。
  10. **全链路 OTel 与队列指标（`P2-10`）**：在 `durable_run` 透传 W3C `traceparent` 串联 API、PostgreSQL 队列、Worker 与 SSE，并暴露待领取队列深度、Claim 耗时、租约续期失败数、Outbox 积压及 LayerFS 保存耗时等 Prometheus 指标。
  11. **异常滞留态运维巡检与会话筛选（`P2-11`）**：补齐针对 `Blocked` Run、`SaveBlocked` 沙箱及长期 `Pending`/`Rejected` Memory 操作回执的内部只读巡检接口（填补 [布局与依赖约束](layout.md#共享持久化边界) 记录的运维空白），并为 `/v1/sessions` 增加基础关键词筛选。

### 明确不纳入补齐清单的项（保留在外部场景层）

以下 `agentic-kit` 能力属于产品应用层，继续通过 [Go SDK](../sdk/go/agenticv1/README.md) 在外部独立服务组装，不迁入通用内核：

* **可视化 DAG 工作流引擎（`compose`）与单节点调试器**：属于低代码编排产品，与持久化单/子 Agent 内核正交。
* **飞书 / 钉钉 / 企业微信 / 蓝信等 IM 渠道连接器与定时任务（Cron）调度器**：属于消息网关与外部触发器层，通过 `/v1/sessions` 或 `/internal/v1/executions` 对接即可。
* **固定业务场景模板（如写作助手、Coding 审批流）与离线评测数据集平台**：由各自垂直业务应用独立演进。

---

## 七、后续增量对比流程与历史对比记录

每次 `~/Code/agentic-kit` 发生更新需要核对或跟进时，按以下只读步骤执行增量对比（严禁在 `~/Code/agentic-kit` 工作区执行 `git checkout` / `git switch` 或产生未提交修改）：

1. **识别最新活跃开发分支**（离线或无法拉取远端时跳过 `fetch` 并在记录中注明本地引用刷新时间）：
   ```bash
   git -C ~/Code/agentic-kit fetch origin
   git -C ~/Code/agentic-kit for-each-ref --sort=-committerdate refs/remotes/origin/ \
     --format='%(committerdate:iso8601) %(refname:short) %(objectname:short) %(subject)' | head -n 15
   ```
   锁定最新活跃主线开发分支（注意区分主线 `dev-*` / `master` 与特定客户定制分支 `proj-*`）。
2. **提取双边增量提交与改动范围**：
   ```bash
   git -C ~/Code/agentic-kit log <上次基线SHA>..<新SHA> --oneline
   git -C ~/Code/agentic-kit diff --dirstat=files,0 <上次基线SHA>..<新SHA>
   git -C ~/Code/agentic-kit show <新SHA>:docs/agent-requirement-index.md | tail -n 60
   git -C /Users/light/Code/agentic-kit-api log <上次API基线SHA>..HEAD --oneline
   ```
   按需使用 `git -C ~/Code/agentic-kit show <新SHA>:<path>` 只读查看关键变更源码或需求记录文档。
3. **三类口径评估变更影响**：
   - **内核能力可吸收项**：涉及持久执行、上下文压缩、沙箱安全、工具协议或恢复边界的通用改进，评估是否纳入第六节待补齐能力列表或调整优先级。
   - **边界外产品项**：属于工作流节点、技能包、专有插件、IM 渠道、业务模板或评测的功能，确认继续保持在通用内核之外。
   - **契约或差距变化项**：双边已有对应能力但实现策略或优劣对比发生变化，就地更新本文档第一至五节对应条目；若本仓库或 `agentic-kit-worker` 已完成第六节中的某项能力，同步更新其 `当前状态` 与验收证据链接。
4. **回填基线与记录**：更新文首“当前对比基线”表的分支、提交 SHA 与对比日期，并在下方“历史对比记录”顶部按日期倒序追加一条新记录。

### 历史对比记录

* **2026-11-24 首版全量基线对比**：基于 `agentic-kit-api`（`main` @ `54922f0`）+ `agentic-kit-worker`（`main` @ `0434172`）与 `~/Code/agentic-kit`（`origin/dev-v2.0.0` @ `c9ecfe80` 及 `origin/dev-2.0.0` @ `18ab6c92`）完成全量架构、路由、持久执行、上下文压缩、记忆、沙箱、技能、渠道与工作流对比，并确立 P0/P1/P2 共 11 项内核待补齐能力进度基线（`P0-3` 为 `部分就绪`，其余 10 项为 `待实施`）。确认本仓库已完成 `context-v4` 与 `ActorProject` 记忆隔离等内核能力建设，后续重点按 P0 优先推进流式输出、标准 MCP、子智能体闭环与多模态附件契约，同时保持工作流、技能市场、渠道与专有插件继续归属外部场景层。
