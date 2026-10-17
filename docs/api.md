# 任务接口

Go 请求与响应类型统一定义在 [sdk](../sdk/go/agenticv1)，调用方式见 [SDK 用法](../sdk/go/agenticv1/README.md)。SDK 不依赖 Gin；服务端负责校验。

下表业务接口要求 `Authorization: Bearer <token>`。身份来自部署配置，不接受客户端指定 owner。所有对象按 owner 隔离；访问其他 owner 的对象返回 404。创建、编辑和发布 Agent 还要求 owner 在 `auth.config_owners` 中，否则返回 403。当前不支持跨 owner 共享 Agent。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /v1/agents | 创建 Agent 草稿，返回 201 | name、config |
| GET /v1/agents | Agent 列表 | after、limit |
| GET /v1/agents/{id} | 读取草稿及 revision、publishedVersion | 无 |
| PUT /v1/agents/{id} | 完整替换名称和草稿 | name、config、revision |
| POST /v1/agents/{id}/publish | 发布当前 revision | revision |
| GET /v1/agents/{id}/versions/{number} | 读取不可变版本 | 无 |
| POST /v1/sessions | 创建会话，返回 201 | agentID、可选 title |
| GET /v1/sessions | 会话列表 | 可选 agentID；after、limit |
| GET /v1/sessions/{id} | 读取会话及 messageSeq | 无 |
| POST /v1/sessions/{id}/messages | 提交用户消息并创建任务 | query；Idempotency-Key header 必填 |
| GET /v1/sessions/{id}/messages | 按序读取消息 | after（消息 seq）、limit |
| GET /v1/sessions/{id}/runs | 查询会话任务与状态 | after、limit |
| GET /v1/runs/{id} | 查询任务 | 无 |
| POST /v1/runs/{id}/cancel | 取消 | 无 |
| POST /v1/runs/{id}/answer | 回答澄清 | stepID、result |
| POST /v1/runs/{id}/resolve | 提交人工确认的工具结果 | stepID、result |
| GET /v1/runs/{id}/events | SSE 回放及订阅 | Last-Event-ID header 或 after query，格式 runID:seq |

config 包括 systemPrompt、model、tools、maxTurns，不接受 query/history/agentID/configVersion。模型配置与工具字段见 [创建请求样例](../examples/agents/agents.example.json)。模型地址必须是 HTTP(S)；maxTurns 为 1～100。工具名称不能重复，parameters 必须为 object 类型 JSON Schema，policy 必须显式提供。工具可设置 `returnDirectly: true`；该工具成功时，Eino 直接返回工具结果并结束本轮，不再调用模型。此设置随 Agent 发布版本和 Run 快照冻结。AppForge 尚无已验证的下游幂等契约，不能配置为 Idempotent。

Agent 创建时 revision=1，编辑成功递增 revision。编辑或发布携带旧 revision 返回 409。同一 revision 重复发布返回同一个版本，版本 number 从 1 开始。仅已发布 Agent 可以创建会话。每次提交消息使用当时最新的发布版本，并把配置写入 Run 快照；恢复时不重新读取草稿或最新版本。Run 响应中的 agentID、configVersion 可用于定位版本。

提交消息返回 202 和 Run；同一 owner 的相同请求编号、相同 session/query 重试返回 200 和原任务，不重复保存用户消息。发布新版本不改变该行为。请求编号重复但输入不同、同一会话已有活动任务、无效状态转换返回 409。

列表返回 `{"items":[]}`。默认 limit=50；Agent、会话、任务列表最多 100 项，按 ID 升序，after 是上一页最后一项 ID。ID 排序不代表创建时间。消息列表最多 200 项，按会话内 seq 升序，after 默认 0，下一页使用上一页最后一项 seq。Agent、会话和消息时间以 UTC RFC3339 返回。

消息包含 sessionID、seq、runID、kind、role、content、可选 toolCalls/toolCallID、createdAt。kind 包括 User、Assistant、Final、Tool、ClarificationAnswer、ReviewResult。助手工具决策保存为 Assistant，最终回复保存为 Final；澄清回答与人工确认属于工具结果，role 为 tool。通过 toolCallID 与助手 toolCalls 中的 ID 配对。失败和取消从 Run 状态及 Event 读取，不伪造成助手回复。

消息与对应任务状态在同一事务提交。客户端可以用会话消息列表展示历史，用 Run SSE 展示当前任务进度；Event 不作为聊天记录。当前模型上下文取最近 20 个已完成 Run 的 User/Final 消息，工具历史保存在消息表供查询，尚未实现跨轮工具历史与上下文压缩。

任务状态为 Queued、Running、WaitingClarification、NeedsReview、Completed、Failed、Canceled。等待状态查询结果包含 waitingStep。answer/resolve 使用这个值作为 stepID；result 是提供给模型的用户回答或已确认工具结果。

SSE 每条消息的 id 为 runID:seq，data 包含 runID、seq、kind 和 payload。客户端按该 id 去重。只在终态且已发送全部提交事件后关闭连接；WaitingClarification 和 NeedsReview 不是终态。重新连接不会启动新的任务。

重要事件包括 RunQueued、RunStarted、ModelCompleted、ToolStarted、ToolCompleted、ClarificationRequested、RunNeedsReview、RunResumed、RunRetry、RunCompleted、RunFailed、RunCanceled。RunRetry 的 payload.status 可能为 Failed，客户端以状态字段为准，不仅凭事件名称判断。

模型尚在生成时不会发逐 token delta；ModelCompleted 表示完整模型决策已经保存，RunCompleted 表示最终答案和完成事件已经同事务提交。

## 进程探针

server-go HTTPHost 提供无需鉴权的 `GET /ping`（200，正文 `pong`）、`GET /healthz`（就绪时 200，正文 `healthz`；未就绪时 503）和 `GET /version`。探针只反映进程与服务生命周期，不持续检查外部依赖。Forge 项目及企业资产接口继续使用下文的会话和权限校验。

## Gin 请求与错误契约

对象接口返回 JSON，列表返回 items；取消与回答成功返回 204。错误统一返回：

```json
{"code":"InvalidParameter","message":"invalid request body","requestId":"request-id","statusCode":100400}
```

错误码包括 Unauthorized、Forbidden、InvalidParameter、PayloadTooLarge、InvalidCursor、APINotFound、NotFound、MethodNotAllowed、Conflict、InternalError。请求体超过 1 MiB 返回 413，未知字段或连续多个 JSON 文档返回 400；URI、Query、Header 与 JSON 在同一请求对象中完成绑定和校验，参数错误统一返回 InvalidParameter（包含消息 after 为负数）；SSE 的复合事件游标格式错误仍返回 InvalidCursor。绑定错误返回框架校验信息，未分类的内部错误不向客户端暴露数据库详情。`statusCode` 是框架错误编号，实际 HTTP 状态以响应状态行为准；`requestId` 用于关联日志。

请求日志只记录方法、路由模板、状态与耗时，不记录 token 或请求体。Recovery 在响应未提交时返回 JSON 500；SSE 已开始后只结束处理，不向事件流追加 JSON 错误。这些行为由 server-go HTTPHost 统一管理；服务凭据采用框架 Bearer 鉴权，Forge 项目权限保留在业务层。SSE 不设整段响应的写入超时，调用方取消与进程退出会结束订阅。SSE 响应头、事件编码、心跳和逐次写入期限由 server-go stream 管理，底层使用 gin-contrib/sse 编码，字段冒号后是否有空格不影响标准 SSE 消费端。

## 受信任应用执行

`POST /internal/v1/executions` 仅允许 config_owners 身份。请求为：

```json
{"spec":{"agentID":"已发布 Agent ID","agentVersion":1,"scope":{"tenantID":"t","projectID":"p","actorID":"u","taskID":"task","roundID":"round"},"query":"本轮要求"},"timeoutSeconds":1800}
```

Agent 定义先通过 `/v1/agents` 创建、编辑与发布。应用执行必须引用明确的发布版本，不能传入 systemPrompt、model、tools 或 maxTurns。运行时读取该版本并冻结完整执行快照；不创建临时 Agent。旧的内联定义请求返回 400，不保留第二条定义路径。

同一 owner、tenant、project、task 复用同一个 Session；不同项目的同名 task 不共享历史。后续轮次允许选择同一 Agent 的其他已发布版本，但不能在同一任务中更换 Agent 身份。同一会话已有活动 Run 时返回 409。上下文策略与普通聊天一致：最近 20 个已完成轮次的用户输入与最终答案。

幂等键必填。同键同输入返回原 Run，包括发布新版本之后的重试；同键不同输入返回 409。Agent、发布版本不存在或属于其他 owner 时返回 404。创建会话、Run、输入消息与事件在同一事务提交。应用服务凭据不能下发给浏览器；应用从已验证的用户与任务构造 scope，敏感操作仍动态检查业务权限。

`GET /internal/v1/executions/{id}/lease?epoch=N` 返回当前数据库时钟下的执行权；取消、旧 epoch 或其他 owner 均无法通过。模型工具参数不能提供该执行身份，Worker 在 HTTP headers 中注入真实 run ID 和 epoch。运行时执行权不替代业务授权或外部进程停止确认。

## Public run trace

`GET /v1/runs/:id/trace?after=0` (RunAPI.Trace) returns an owner-scoped `RunTrace`: the current public Run, up to 50 events in sequence order, `latestEvent`, `nextSeq`, and `hasMore`. Repeat with `after=nextSeq` while `hasMore`, then poll for new events. Each event includes `createdAt`. The snapshot uses a repeatable-read transaction; refreshing or reconnecting replays persisted events. `ModelStarted` is committed before each model call. Tool events include their public name; ToolStarted includes public arguments recovered from assistant messages, including historical runs. ToolCompleted includes the saved result. `arguments`, `result`, `output`, and `detail` previews are capped at 32 KiB UTF-8 and marked with `<field>Truncated: true`; full originals remain in storage. No checkpoint, hidden reasoning, system prompt, model/tool credentials or lease identity is returned. This endpoint is read-only and does not resume tasks. Invalid negative/non-numeric cursors return 400; another owner’s run returns 404. Responses use Cache-Control: no-store.

### 沙箱工具绑定

AgentConfig 可设置 `toolsets:["filesystem"]`，并将平台结束工具设为 `endRun:true`。受信任的内部 ExecutionSpec 必须同时提供 `filesystem:{provider:"AIO",url:"http://coding/internal/v1/rounds/<round>/sandbox",tokenEnv:"CODING_TOOLS_TOKEN",workspace:"/workspace/repo"}`。绑定随执行配置冻结，幂等重试不能更换沙箱。普通会话提交不能使用需要可信绑定的文件工具集。凭据值不写入配置快照。

## 持久评测实验

以下接口仅供 `config_owners` 中的受信任应用调用。普通 owner 返回 403。浏览器身份、企业和项目授权由应用网关校验；本接口记录 scope，不把 scope 当作授权证明。网关不能向浏览器下发服务令牌，并须核对查询对象的企业、项目与当前用户权限。跨 owner 对象返回 404。

| 方法和路径 | 用途 | 请求 |
| --- | --- | --- |
| POST /v1/evaluations | 原子创建实验及全部 Trial / Run；首次返回 202，重试返回 200 | suite、scope；Idempotency-Key 必填 |
| GET /v1/evaluations | 按企业、项目分页 | tenantID、projectID 必填；after、limit |
| GET /v1/evaluations/{id} | 查询冻结套件、原 Run、当前评分与状态 | 无 |
| POST /v1/evaluations/{id}/cancel | 取消未结束 Run；成功返回 204 | 无 |
| GET /v1/evaluations/{id}/compare | 配对比较基线与当前实验 | baselineID 必填 |

请求类型为 `sdk.StartEvaluationRequest`。scope 包含 tenantID、projectID、actorID；suite 见 `examples/evaluation/evaluation-writing.example.json`。Agent 版本必须已发布且属于调用 owner。一次最多 100 个案例、20 个判分器和 20 个指标。请求体仍限制为 1 MiB。当前支持 Agentic / single_turn 和 contains_all、exact、json_valid 输出判分器，聚合为 pass_rate。文件系统套件暂限单案例；多案例需要每个 Trial 独立环境，不能共享可写 workspace。

实验、每题 Trial、独立 Session、固定版本 Run、用户消息和 RunQueued 事件在同一个 PostgreSQL 事务提交。创建失败整批回滚。幂等摘要覆盖完整 suite 与 scope；同键不同输入返回 409。候选再次编辑或发布不改变已接受的 Run。查询和恢复不创建新 Run。

状态为 Running、NeedsAttention、Completed、Canceled。澄清和未知工具结果显示 NeedsAttention，保留原 Run 的 waitingStep；仍通过既有 Run answer / resolve 接口提供明确的人类输入，不自动确认副作用。实验 Completed 表示所有 Run 已结束且最终评分事务已提交；实验报告 passed 独立表示门槛结果。失败 Run 的评分为 ExecutionError，未完成 Run 为 Incomplete，分母始终包含所有计划案例。实验活动期间报告 passed 为 false；单项指标可展示当前进度。评分所需的当前 evidenceComplete 仅覆盖完整 Run.output。

API 每秒按数据库中的待完成实验重算并保存报告；可运行多个 API 实例，实验行锁串行化提交。该过程不调用模型或工具。执行与恢复沿用现有 worker。Langfuse 是可重建的证据投影，不参与提交、取消或判分事务。

取消实验与未完成 Run 的状态、epoch 和取消事件同事务提交。已完成 Run 保留原结果；晚到取消遇到全部已结束的 Run 时保存 Completed。重复取消不重跑。取消继续遵守原执行契约，不表示外部副作用已撤回或外部进程停止已确认。

比较要求同一企业、项目，并保持 dataset（含版本及案例）、scenario、判分器版本、指标和门槛完全一致；Agent 身份、候选版本及套件显示名称可不同。不满足时返回 409 / IncompatibleEvaluation。返回按 caseID + evaluator 配对的状态，以及指标值、覆盖率和候选减基线的差值；缺少评分时差值为空。

公开证据继续通过 Trial.runID 对应的 Run trace 与 Session messages 查询。评测表不复制执行器 checkpoint，也不把截断的 trace 预览用于输出判分。部署新 API 前须先运行 `cmd/cli`；重复迁移保留既有 Run 与实验记录。


## Forge 项目 Agent 与评测入口

配置 `PLATFORM_TENANT_URL`、`PLATFORM_TENANT_TOKEN`、`PLATFORM_TEMPLATE_OWNER`、`PLATFORM_TEMPLATES_JSON` 后，在同一个 API 进程启用 `/api/v1/tenants/:tid/projects/:pid` 路由。浏览器通过同源反向代理访问，使用 `forge_session` Cookie；写请求透传 Origin 和 X-CSRF-Token 到 Tenant `/internal/v1/session/check` 校验。每次请求向 `/internal/v1/access/check` 查询当前项目授权：读至少 20，写至少 30。运行时 Bearer token 不能代替用户会话。

| 方法与项目内路径 | 行为 |
| --- | --- |
| GET /agents?after=&limit= | 返回项目 Agent、可用受信任模板与 canEdit；稳定 ID 分页 |
| POST /agents | `{templateID,name}`，复制已批准的不可变模板为项目独立草稿 |
| GET /agents/:id | 读取草稿；仅返回 Prompt、模型名称、轮数、revision、candidateVersion |
| PUT /agents/:id | `{name,revision,prompt,maxTurns}`；revision 冲突返回 409 |
| POST /agents/:id/candidates | `{revision}`；冻结当前草稿，重复调用返回同一候选 |
| GET /agents/:id/versions/:number | 读取不可变候选的公开配置 |
| POST /evaluations | `{suite}` + Idempotency-Key；scope 从会话和路由生成，创建返回 202，幂等重放 200 |
| GET /evaluations?after=&limit= | 项目实验列表，稳定 ID 分页 |
| GET /evaluations/:id | 实验、冻结套件、逐题输出与报告 |
| POST /evaluations/:id/cancel | 取消实验，204 |
| GET /evaluations/:id/compare?baselineID= | 同项目、同套件的配对比较 |
| GET /evaluations/:id/trials/:trial/trace?after= | 该实验内 Trial 的公开 RunTrace 页 |

模板列表是部署管理员批准的 `{id,name,agentID,version}` 数组，版本固定。用户不能通过这些接口设置连接地址、凭据引用、工具、scope 或 actor。项目 owner 由服务器按 tenantID/projectID 派生，所有查询和变更沿用数据库 owner 隔离。跨项目对象返回 404。项目副本不会出现在模板 owner 的普通会话入口，因此生成候选不会切换已有调用；原独立 `/v1/sessions` 的最新发布版本语义保持原契约。

项目入口支持 Writing `single_turn` 与 Coding `coding` 场景。浏览器不能提交 filesystem 连接或凭据。输出检查支持 contains_all、exact、json_valid；Coding 支持 checks_passed。指标聚合为 pass_rate，门槛可配置。案例和规则随实验冻结，可从历史实验复用。Skill/MCP 编辑尚未开放，评测通过不自动启用业务版本。

Go 调用使用 `sdk.NewProjectClient(publicBaseURL, tenantID, projectID, sessionCookie, csrfToken, httpClient)`；方法与响应类型见 sdk/go/agenticv1/agentic_api_project.go、sdk/go/agenticv1/agentic_client_project.go。连接必须指向公开同源 URL，客户端不跟随重定向，不能传入运行时服务密钥代替会话。

### 可保存的评测方案

项目入口新增 `GET /evaluation-plans`、`GET /evaluation-plans/:plan`、`PUT /evaluation-plans/:plan`。路径仍以前述 tenant/project 前缀开始，复用相同会话、CSRF 和项目权限。GET 使用稳定 ID 游标分页。

PUT 接受 `SaveEvaluationPlanRequest{revision,suite}`。客户端为新方案生成稳定 ID，首次 revision=0，成功返回 `EvaluationPlan{id,revision,suite,updatedAt}`。更新须传当前 revision；并发修改返回 409。丢失响应后重试同 ID、同 revision、同 suite 返回已接受的版本，不重复创建。其他项目读取返回 404，只读角色写入返回 403。方案必须引用本项目已有候选，并满足对应的 Writing 或 Coding 场景约束。

方案存储于 `evaluation_plans`。保存不创建 Run，不调用模型。运行时实验继续保存独立 suite 快照；修改方案不改变已有实验、评分或候选。方案修订号用于并发编辑，不能替代 Agent 的候选版本号。独立的数据集发布/版本管理尚未提供。

### 候选试运行

`POST /agents/:agent/trials` 接受 `{agentVersion,input,timeoutSeconds}`，要求稳定的 `Idempotency-Key`。输入非空，超时 1–3600 秒。首次返回 202，重试返回 200，响应为 `ProjectTrial{run,input}`。服务端校验候选属于本项目且无工具，构造真实 actor/project scope，复用 `SubmitExecution` 的事务、不可变版本和幂等语义。每次新键生成独立会话。

`GET /agents/:agent/trials/:run` 恢复结果；`POST /agents/:agent/trials/:run/cancel` 停止试运行。读取要求项目读权限，创建和取消要求写权限。Run 必须属于当前项目、路径中的 Agent 且来源为试运行，不能用这个入口取消评测 Run。响应不含模型地址、凭据引用或私有执行状态。试运行不产生评分，不计入实验门槛。


### Coding 仓库评测

`ProjectAgent.kind` 和模板的 `kind` 标识 Writing/Coding；`ProjectAgent.tools` 只返回工具名称。模板预览返回 `id,name,kind,model,version,prompt,maxTurns,tools`，字段来自已批准的不可变版本；工具仅包含名称，不返回连接地址或凭据引用。批准的 Coding 模板必须包含 filesystem、finish、run_checks。创建项目副本时，服务端将结果工具绑定到评测网关，保留模型、工具协议和 Prompt；名称、Prompt、轮数继续支持草稿修订与不可变候选。

Coding 使用同一套 `/evaluation-plans`、`/evaluations`、报告、比较和 trace 接口。`scenario` 示例：

```json
{"kind":"coding","timeoutSeconds":900,"coding":{"baseHead":"0123456789012345678901234567890123456789","allowedPaths":["src/calc.py"],"checks":["python3 -m unittest discover -s src"]}}
```

仓库由真实 tenant/project 权限解析，不接受任意仓库 URL。baseHead 必须为完整 commit。每条 Trial 创建独立环境；准备完成后才创建 Run。超时覆盖准备、执行与验证。目标声明 output、filesystem、checks。独立测试 evaluator 使用 `kind=checks_passed, scope=checks, requires=["checks"]`，读取验证容器中的实际退出码，工具 run_checks 的结果及 Agent 自报结论均不充当该证据。

Trial 的 `runID` 在准备期间为空，`coding` 返回 status、baseHead、patch、changed、checks、error、stopped。环境状态为 Preparing、Ready、Verifying、Completed、Failed、Stopped。实验完成须等待验证结果与环境清理；Canceled 实验仍可显示清理中，只有 stopped=true 表示回收已确认。失败或证据缺失不会增加通过率。配对比较要求 commit、路径、检查命令、案例和指标完全相同。

Coding 试运行使用单条案例的持久评测方案。`/agents/:agent/trials` 仍是 Writing 的直接单轮试运行接口。

受信任服务可调用 `GET /internal/v1/evaluation-trials/:trial/lease?runID=...&epoch=...`。仅返回匹配 Coding Trial 的活动执行权；不提供跨 owner 的 Run 内容。Coding 网关还核对冻结 scope 和实时项目权限。


### 评测候选转为 Coding 业务版本

`POST /internal/v1/coding-release-approval` 仅向批准模板所属的受信任 Coding 调用方开放。请求为 `CodingReleaseRequest`：`tenantID,projectID,actorID,experimentID,agentID,agentVersion`。服务端再次检查 actor 的当前项目管理权限，按项目 owner 读取精确实验和候选。

实验必须 Completed 且通过，目标候选与项目必须匹配。必须包含独立 `checks_passed` 指标，最低通过率和覆盖率均为 100%，实际结果也为 100%。响应 `CodingReleaseApproval` 返回实验摘要、固定场景及业务版本引用，不返回模型连接或工具凭据。

业务版本保留候选的 Prompt、模型、轮数、工具参数和工具策略。服务端将结果工具从评测接口绑定到业务接口，并调整 finish 的业务说明。固定候选稳定映射到一个不可变业务发布 v1，重试不会创建额外 Agent。项目候选及评测快照不变。Coding 服务负责核对当前仓库基线并保存生效绑定与回退历史。

## Enterprise Agent workspace

The browser workspace is rooted at `/api/v1/tenants/:tid`. It uses the existing Forge session, Origin and CSRF checks, and Tenant's trusted `/internal/v1/workspace/check` endpoint. Active enterprise members can read and edit shared assets without repository grants. Platform operator status alone does not grant membership. Agent data is stored under a server-derived enterprise owner.

- `GET /agent-workspace`: editing permission and currently readable projects (effective level, using the minimum of desired, observed and effective grants).
- `GET /agent-templates`: trusted, fixed-version template previews; no project required and no connection credentials returned.
- Agent, candidate, trial, evaluation-plan and evaluation routes have the same suffixes as the historical project API. `sdk.NewWorkspaceClient` addresses this enterprise root.
- Writing trials and experiments require no project. Execution without a project is restricted to configurations without tools or filesystem bindings.
- Coding plan save and experiment submission take `projectID` separately from the Agent identity. Writes require project level 30; reads require level 20. List queries filter inaccessible project-bound plans and reports before pagination. Read, cancel, compare and trace recheck access. Overwriting a plan checks its previous environment as well as the new one.
- Project activation still requires project administration and matching experiment scope. A shared candidate may have different experiments and activations in multiple projects; one project's evidence cannot enable another project.

Datasets are independently managed through `GET /datasets`, `GET /datasets/:id`, `PUT /datasets/:id` (`name`, `revision`, `cases`), `POST /datasets/:id/versions` (`revision`), and `GET /datasets/:id/versions/:number`. Draft saves detect concurrent edits. Publication is immutable and repeat publication of the same contents reuses the version. Enterprise plans and experiments must reference `suite.dataset.id` and its published numeric `version`; the server resolves the full snapshot, rather than trusting client-supplied cases. Existing experiments keep their original snapshot.

Historical project-private assets stay in their original owner namespace and remain accessible through the project API. Their IDs, published configurations, digests, experiment scope and project grants are unchanged. The central UI labels these as restricted access scopes. Existing project URLs lead to the central view with the original restricted scope. No automatic expansion to enterprise-wide access occurs. Historical inline case sets can be saved as an independent dataset within the same restricted scope.

The migration adds `evaluation_dataset`, `evaluation_dataset_version`, and an environment `project_id` column on `evaluation_plans` with an empty default. It does not rewrite existing Agent owners, runs, project bindings or reports. Migration can be repeated with existing data.
