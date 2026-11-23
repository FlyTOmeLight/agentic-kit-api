# 执行链路验证记录

本文件按日期保留历史检查结果，条目中的路径、版本和限制描述当时的实现。当前功能、布局和契约分别以 README、docs/layout.md 和 docs/api.md 为准；可复用经验见 docs/lessons/。

## 2026-11-23 上下文压缩 v4 与溢出自愈

在基于 `origin/main` 的隔离工作树 `feat/context-v4-compression`（API）与 `feat/context-v4-compression-worker`（Worker）完成移植与联合验证，保留上游 Memory 作用域隔离、PR #20 续跑快照与 PR #7 已计费摘要持久化及 `RecordDecision` 失败保护。

- API 侧收敛到 `context-v4` 与 `cjk-weighted-v1`（配置窗口时 `MaxToolResultBytes` 上限为输入 token 预算的一半，最低 128 字节），实现历史两级裁剪 + 当前轮参数/结果分级压缩 + 可选整轮淘汰与固定初始问题、仅在 `BoundaryComplete` 推进的 Session 级摘要/水位（`compacted_summary` / `compacted_through_seq`）、`ModelOverflowRejected` 审计与租约保护的 `ReadStepResult`。
- Worker 侧在 `boundaryModel` 统一经 `context-v4` 组装模型输入、保存 `UsageAnchor` 与连续 3 次摘要失败熔断计数、对明确零用量溢出错误做最多 5 次缩小重派发（耗尽进入 `Failed`，消费未知保持 `Blocked` 由 `Unblock` 恢复），并在启用工具或工具集时挂载只读 `read_step_result` 按 UTF-8 字节分页读取原始步骤结果。

验证连接本地 PostgreSQL（`127.0.0.1:55432`）和 Redis（`127.0.0.1:56379`）：

- API（`GOWORK=off`）：`go test -mod=readonly -race -count=1 -timeout=120s ./...`、`go vet -mod=readonly ./...`、`go build -mod=readonly ./...` 与 `make doc` 全部通过（退出码 `0`，`pkg/contextbuilder` 1.425s、`pkg/durable` 43.455s、`service` 13.567s、`internal/app/memory` 18.441s、`internal/sandboxcenter` 24.166s）；未设置 `SANDBOX_E2E_K8S_CONTEXT`、`ARTIFACT_TEST_PG_DSN` 或一次性探针开关的用例跳过。
- Worker（临时 `go.work` 指向上述 API 工作树）：`go test -race -count=1 -timeout=180s ./...` 与 `go vet ./...` 全部通过（退出码 `0`，`internal/engine` 83.120s、`internal/adapters` 1.862s、`internal/adapters/aio` 1.602s、`internal/adapters/sandbox` 6.956s、`internal/config` 1.662s、`internal/service` 1.685s），覆盖 HTTP 400 恢复与耗尽、502/断连人工 `Unblock`、滚动分块摘要、熔断接管、父上下文取消后已计费摘要持久化（`TestBilledSummaryPersistsAfterParentCancel`）、`read_step_result` 分页与跨进程强杀接管；未设置 `SANDBOX_E2E_K8S_CONTEXT`、`SANDBOX_CENTER_URL`、`TEST_AIO_URL` 的用例跳过。

未验证范围：本次使用受控 HTTP 模型与工具替身，未用真实在线 LLM 供应商检验实际 400 上下文溢出错误体覆盖度；API 合入前 Worker 通过未跟踪 `go.work` 联调，待 API 提交固定到 Worker `go.mod` 后仍需再执行一次 `GOWORK=off` 独立构建与集成测试。

## 2026-11-23 OrbStack Forge 网关归档撤权复测

**联合验收通过。** OrbStack 的 `forge-artifact` 换用本地镜像 `artifact-go:memory-archive-scope-20261123`，健康检查返回 `200`。Forge Tenant API 仍为 `v0.13.0`。`TestOrbStackMemoryGatewayRevocation` 以 `go test -mod=readonly -race` 启动当前 Memory HTTP 路由，连接真实 Artifact 与 Forge，并经 `forge-web` 执行项目角色变更。原有网关转发出现请求超时，复测使用同一 `forge-web` Service 的独立本地端口转发。

- `developer` 可以创建项目 Memory 并通过 Memory 读取固定版本。即使项目读写权限有效，普通用户也无法通过 Artifact 网关访问归档。降为 `reviewer` 后，Memory 正常读取，新键写入和同键重放都返回 `403`。
- 完全撤权后，Memory 列表及写入重放返回 `403`，详情、历史列表及固定版本均返回 `404`。在 Forge 网关的 `/artifact`、`/storage` 及 Artifact Service 直连入口，归档集合、版本列表、固定版本及 URI 解析均返回 `404`，按 `agent-memory` 过滤的集合列表为空。普通用户创建该类型返回 `403`，普通 `agent` 列表返回 `200`。用用户凭据尝试服务透传返回 `401`。
- 经过 Forge 认证的 `agentic-kit-api` 在撤权后仍能直连读取及解析同一固定版本，版本响应与撤权前一致。重新授权后，Memory 读取的条目修订和固定版本引用保持一致。
- 测试结束后已归档测试集合、删除独立 PostgreSQL schema，并恢复专用项目无读写的基线。临时用户及服务凭据均已撤销，复查返回 `401`。当前验收范围为 OrbStack 本地镜像。

Artifact 仓库的 `make check` 通过，覆盖 PostgreSQL、MySQL、竞态、构建、vet 和 OpenAPI 一致性。归档权限回归用例覆盖普通用户、另一已认证服务、可信归档服务和普通 Agent Artifact。

## 2026-11-23 OrbStack Memory 项目维护联合验收

在 `feat/memory-actor-project-maint` 上用临时探针 `TestOrbStackActorProjectMaintenance` 启动真实 Memory HTTP 路由，连接 OrbStack 的 Forge Tenant API `v0.13.0`、Artifact `v0.5.1` 和本机 PostgreSQL 的独立 schema。探针在 `go test -race` 下跑完两轮。第二轮增加了发布响应丢失后的 Pending 恢复，当时公开 Artifact 入口的撤权断言失败。修复后的结果见 [Forge 网关归档撤权复测](#2026-11-23-orbstack-forge-网关归档撤权复测)。

- Forge 从无项目权限切换为 `developer` 后，Memory 创建、更新、停用和恢复成功，依次应用 revision 1 至 4。创建、更新和恢复复用同一个真实 Artifact 集合，各自固定到一个版本。同键创建复用原条目和版本，旧版本正文保持不变。仅选租户时，Memory 项目条目返回 `404`。Artifact 拒绝了测试账号选择其他存储用户的请求。
- 降为 `reviewer` 后，详情和历史保留读权限，四类维护请求使用新键或原键都返回 `403`。完全撤权后，列表返回 `403`，详情、历史列表和单版本返回 `404`。重新授予 `developer` 后，先确认 revision 4 和固定版本未变。
- 第二轮由代理丢弃真实 Artifact 已经成功发布的更新响应，Memory 返回 `503` 并保留 Pending。撤销写权限后，`ResumePending` 拒绝恢复，期间没有向 Artifact 发出发布请求。重新授予写权限后，恢复复用原发布结果，采用同一个 Version ID 并推进至 revision 5，HTTP 同键重放返回这个生效结果。
- **部署隔离未通过。** 完全撤掉项目权限后，测试用户通过公开 Forge 网关的 Artifact 入口仍得到 `200` 和自己的 `agent-memory` 测试版本。直接请求 Artifact Service 也得到 `200`。现有 Artifact 用户接口按存储归属授权，公开网关放行了该接口。联合验收的通过条件是归档入口只接受受信服务，撤权后的普通用户请求无法读取归档正文。

两轮完成后均将专用项目权限恢复为无读写、删除独立 schema，并归档本轮创建的测试 Artifact 集合。受保护版本随归档集合保留。临时用户凭据和两个服务凭据均已撤销，复测返回 `401`。Pending 验证使用故障注入代理，公开入口的结果来自实际部署。

## 2026-11-23 Memory 项目身份与可见性

在隔离 PostgreSQL、Redis 和 artifact-go `v0.5.1` HTTP 测试服务上，启用 `ARTIFACT_TEST_PG_DSN`，以 `GOWORK=off` 执行全仓 `go test -mod=readonly -race -json -count=1 -timeout=120s ./...`。1,013 项通过、18 项沙箱或 Kubernetes 环境用例跳过、0 项失败，计数包含子测试。`go vet ./...`、`go build ./...`、`make doc`、改动 Go 文件的 `gofmt` 检查和 `git diff --check` 通过。

- Memory HTTP 与服务用例覆盖项目维护、本人及精确项目隔离、撤销读权限后的详情和历史、撤销写权限后的同键重放以及 Pending 恢复。
- 聊天和应用执行用例覆盖项目召回重放、撤权和受众过滤。Artifact 联合用例覆盖项目归档的固定版本、出站身份头和撤权后的 Memory 历史读取。

本轮 Forge 权限验证使用受控 HTTP 响应，Artifact 使用测试身份解析器。真实服务和公开入口的结果见 [OrbStack 联合验收](#2026-11-23-orbstack-memory-项目维护联合验收)。

## 2026-11-19 OrbStack Worker 冻结项目记忆与接管

临时探针 `TestOrbStackWorkerFrozenProjectRecall` 使用 API `7ff5db4` 的应用执行提交路径、OrbStack PostgreSQL 的独立 schema，以及从 Worker `313aeb5` 构建的两个 `cmd/worker` 进程。Worker 按该提交的 `go.mod` 使用 API 模块 `976d526`，构建时设置 `GOWORK=off` 和 `-mod=readonly`。本次使用受控项目授权、Artifact、模型和工具响应。真实 Forge 和 Artifact 的提交授权验证见前一天的记录。

- 首次模型请求收到冻结的 `Project` 记忆。第二次请求带上已保存的工具结果后，探针挂起模型响应，更新本地记忆投影并强制终止第一个 Worker。
- 第二个 Worker 接管后，Run 在 epoch `2` 进入 `Blocked`，模型请求仍为两次，工具仍为一次。探针通过 `Store.Unblock` 人工解除后，第三次请求的 `messages` 与中断前逐字节一致，仍使用冻结的 `Project` 内容。三次请求均未包含 `Private` 内容或更新后的投影。
- Run 在 epoch `4` 完成，快照正文与摘要保持一致。两个成功响应共入账 6 tokens，工具总计执行一次。持久事件按 `RunBlocked`、`RunUnblocked`、`RunCompleted` 的顺序出现。

探针在 `go test -race` 下首次通过，随后连续复跑三次均通过。测试进程退出时清理了临时 Worker 和隔离 schema。

## 2026-11-18 OrbStack ActorProject 授权联调

`TestOrbStackActorProjectRecall` 通过。用测试探针启动分支上的 API HTTP 路由，连接 OrbStack 上的 Forge Tenant API `v0.13.0`、Artifact `v0.5.1` 和本机 PostgreSQL 的独立 schema。探针向真实 Artifact 发布并读取测试 Agent 版本，在本地预置同一用户和项目下各一条 `Project` 与 `Private` 记忆投影。

- 测试用户在专用项目无读权限时，`POST /internal/v1/executions` 返回 `403 Forbidden`。
- Forge 临时 `reviewer` 授权生效后，用原请求键提交返回 `202`，同键重放返回 `200` 和同一 Run。数据库中的 `ContextSnapshot` 处于 Recall 模式，记忆材料仅包含 `Project` 条目。
- Forge 撤权生效后，原请求键和新请求键均返回 `403 Forbidden`，内部健康检查仍为 `200`，原 Run 的快照摘要保持一致。

验收后确认测试用户的项目读权限恢复为关闭，清理测试 schema 和 Agent Artifact，撤销临时服务凭据并复测返回 `401`。本次验证范围到 Run 接受与快照冻结。ActorProject 维护、Memory 远端归档与 Worker 消费快照需要各自的验收。

## 2026-11-18 ActorProject 执行召回授权

`GOWORK=off GOFLAGS=-mod=readonly make integration` 通过，包含 API 全仓竞态测试和 `go vet`。`TestApplicationActorProjectRecall` 使用本机 PostgreSQL 和受控 Forge HTTP 服务，验证项目读权限检查使用当前用户与已验范围、撤权后首次提交和重放都被拒绝、依赖故障返回 `MemoryUnavailable`。项目条目使用预置投影，验证只有标记为 `Project` 的条目进入 Run 快照，以及缺少项目授权的 `RecallTx` 调用被拒绝。ActorProject 维护入口和真实 Forge 项目授权联调留待后续验证。

## 2026-11-17 Agent 版本发布与 Run 快照

OrbStack 的 `forge` release revision 15 使用 Artifact `v0.5.1` 和 Forge Tenant API `v0.13.0`。Forge 的 `/internal/v1/service/self` 返回 `agentic-kit-api`，Artifact 的出站调用接收方白名单配置为 `artifact.forgeTenant.allowedPropagationServices=["agentic-kit-api"]`。API 入站的 `[Tenant].AllowedPropagationServices` 单独用于内部执行接口和 Goal 等路由的调用服务认证。

在本机独立启动 API，用真实 Forge 用户 API key 和已授权的租户创建 Agent、发布版本 1、读取正文、创建 Session 并提交 Run。修改草稿并发布版本 2 后，两个版本分别返回各自的正文，旧 Run 的 `configVersion` 仍为 `1`。

2026-11-18 复核工作树解析到 artifact-go `v0.5.1` 和 forge-tenant-api `v0.13.0`。`GOWORK=off` 下执行 `go build -mod=readonly ./...` 和 `go test -mod=readonly ./...` 均成功，共通过 30 个包的 377 项测试。上述 OrbStack 探针范围止于 Run 接受和版本固定。Worker 执行、Cookie 会话、项目范围、同 revision 重放和真实集群的越权拒绝还需各自的联调证据。

## 2026-11-17 提交时冻结计价快照

API 在 Chat/Application 接受前冻结价格版本、模型、BaseURL 和单价。非空价格表缺模型拒绝提交；空表明确冻结为仅 token 计量。幂等 replay 返回原 Run，Goal 续跑和 Child Run 继承原快照，部署改价不会改变已有任务的计费依据。Worker 只读快照，旧 Run 无冻结标记、快照非法或模型身份不匹配时均在调用前拒绝；主模型与摘要使用同一模型和价格，已持久化的 `PricingFailure` 接管后仍阻断。

本阶段 API 与 Worker 全量 `make integration` 均通过，包含 `go test -race` 与 `go vet ./...`。连接本机 PostgreSQL（55432），API 另连接 Redis（56379），使用 `GOCACHE=/private/tmp/agentic-kit-m0a-build` 和本地 go.work。API durable 包耗时 31.709 秒、service 包 10.290 秒、sandboxcenter 包 20.609 秒；Worker engine 包耗时 68.787 秒。原始临时日志为 `agentic-frozen-pricing-api.log`、`agentic-frozen-pricing-worker.log`，结论记录后清理；下节 84.771 秒和旧日志属于上一阶段。

覆盖价格版本生成、空表/缺模型、提交与改价后 replay、Goal/Child 继承、Worker 冻结快照校验、费用预算、接管与摘要。1 个子代理完成实际调用路径只读复核，未发现具体缺陷。文档收尾不改变 Go 逻辑，沿用本阶段全量结果。

以上为受控响应与本机存储验证，不含真实供应方报价、账单或摘要质量。价格表仍按模型名匹配，未区分不同供应方的同名模型；没有额度预占或严格硬预算。响应已产生但记账未提交时费用仍可能未知。真实 Kubernetes 本轮未重跑，存储仍为单节点 hostPath；本地组合验证不代表独立发布依赖通过。

## 2026-11-17 模型计价与缺价恢复

Worker 主模型与摘要根据可选价格表计算费用，统一通过 `RecordUsageWithCost` 提交。配置了非空价格表时，模型缺价或只有正数总 token 而无输入输出拆分，会先持久化已知 token、零费用和 `PricingFailure`，再返回确定性错误；接管继续阻断。空价格表允许仅 token 计量，但带费用预算的 Goal 在调用前失败。零费用不代表供应方免费，重复 usage 不会补价，配置变更不回填历史费用。

本阶段 API 与 Worker 均通过 `make integration`，包含全量 `go test -race` 与 `go vet ./...`；连接本机 PostgreSQL（55432），API 另连接 Redis（56379），使用可写 `GOCACHE=/private/tmp/agentic-kit-m0a-build`。Worker 最后一次全量包含摘要费用预算用例，engine 包耗时 84.771 秒。定向检查包括 engine 计价、usage 与恢复 61 项，config/service 31 项，以及 `TestCompactionUsageGovernance` 9 项，计数包含子测试。

API 最终复跑的 `make integration` 退出码为 0，日志为 `/private/tmp/agentic-pricing-api-close.log`。此前 `TestSubmissionRecallQueryAndFilterReplay` 因夹具未提供 verified user 失败；已补齐可信 tenant scope 与 `PropagatedUserID`，保留生产鉴权。定向测试及随后的全量均通过。收尾期间 Worker 只调整文档，未重复运行已通过的全量测试。

以上为受控模型响应与本机存储验证，不含真实供应方价格、账单或摘要质量核对。当前价格表未冻结版本，也未按供应方区分同名模型；没有额度预占或严格硬预算。真实 Kubernetes 本轮未重跑，检查点仍为单节点 `hostPath`；Worker 仍通过 `go.mod` 替换本地 API 仓库，独立发布依赖未验证。

## 2026-11-16 Forge 鉴权与租户隔离

用户请求通过 Forge 校验身份与租户，再把可信 tenant/project 放入请求 context；Agent、Session、Run、Goal 和 Memory 的访问及身份继承接入租户隔离。内部执行接口只允许白名单服务透传用户身份，沙箱接口使用独立服务鉴权；callback 默认关闭，启用时要求服务白名单、来源白名单和签名。Agent 发布继续把正文保存到 artifact-go，Run 保留冻结的配置快照。

在独立工作副本中使用 `GOWORK=off`、`GOCACHE=/private/tmp/agentic-kit-api-build`，连接本地隔离 PostgreSQL（55432）和 Redis（56379）执行：

- `go test -mod=readonly -race -json -count=1 -timeout=120s ./...`：659 项通过、18 项跳过、0 项失败，计数包含子测试。跳过项需要 Kubernetes、专用沙箱或 Worker 验收环境。
- `go vet -mod=readonly ./...`、`GOFLAGS=-mod=readonly make build`：通过。
- 本轮修改及新增 Go 文件的 `gofmt -l`：无输出。

测试覆盖 Forge 用户身份校验、伪造 Header、服务白名单、可信身份透传、跨租户读写拒绝及父子资源身份继承。框架生命周期用例使用真实 HTTP、PostgreSQL 和 Redis，Forge/artifact 为受控 HTTP 假服务；真实 Forge/artifact 联调尚未执行。本轮未修改 agentic-kit 或 agentic-kit-worker，未验证 Worker 与新鉴权接口的真实联调。

本轮只验证空库或空 schema 初始化和目标结构重复初始化；不迁移或回填旧的无 tenant 数据，不清理历史 Memory 唯一索引。旧库升级和真实发布链路仍需单独验收。

## 2026-11-13 Agent 资产接入 artifact-go

API 的草稿事务迁到 `internal/app/agent`，已发布版本正文由 artifact-go 保存，本地 `agent_version` 保留编号和远端引用；新任务提交在事务外预取配置，再冻结 Run 快照。修复了草稿 JSON 更新的 GORM 编码错误，并把应用提交的未发布版本和跨 owner 断言移到 HTTP/SDK 层。

本轮发现本地 `go.work` 掩盖了 `go.mod` 缺少 artifact-go 的问题，已显式固定到 v0.5.0 并运行 `go mod tidy`。使用 `GOWORK=off`、`GOCACHE=/private/tmp/agentic-kit-api-build`、本地隔离 PostgreSQL/Redis 执行：

- `go test -mod=readonly -race -json -count=1 -timeout=120s ./...`：394 项通过、18 项跳过、0 项失败，计数包含子测试；跳过项需要专用沙箱、Kubernetes 或 Worker 验收环境。
- `go vet -mod=readonly ./...`、`go build -mod=readonly ./...`：通过。
- `make doc`：完成 OpenAPI 重新生成；随后 `TestOpenAPIMatchesRoutes`、`TestSwaggerServesGeneratedContract` 两项契约测试通过。
- `go list -mod=readonly -deps`：`pkg/durable` 无 artifact SDK 依赖，`pkg/execution` 无 GORM、HTTP 或 artifact SDK 依赖。

Worker 测试夹具已适配 `Accept + PrepareChatSubmission`，进程测试使用本地 artifact HTTP 假服务与测试身份常量。在 Worker 当前 workspace 下运行 `go test -mod=readonly -race -count=1 -timeout=180s ./internal/engine ./internal/adapters/sandbox`，150 项通过，包含 `TestProcessKillResumesWithoutRepeatingTool` 与 `TestProcessShutdownCancelsInFlightHTTPTool`。本轮只修改 Worker 测试，生产执行路径继续读取冻结的 Run 配置；`agentic-kit` 未改动。Worker 普通及 `sandboxe2e` 标签编译检查、两包 vet 通过；未运行依赖专用集群的 sandboxe2e smoke。

artifact 请求使用本地 HTTP 假服务；以上结果不包含真实 artifact/Forge 联调，也不包含已有数据库的历史版本迁移。身份 middleware 尚未接入；Controller 到 artifact 的会话 Cookie、CSRF 与 Origin 透传尚未实现。

## 首期验证基线

最早验证日期：2026-10-02。使用本机 Go 1.27.1、隔离的 PostgreSQL 17.6 与 Redis 7.2.10 容器。原 agentic-kit 和现有业务数据库未修改。

两个仓库均完成 gofmt、go test -race -count=1 -timeout=60s ./...、go vet ./... 和命令入口构建。测试显式设置 TEST_DATABASE_URL；API 测试还设置 TEST_REDIS_URL，没有把缺少数据库时的跳过视为通过。

## 已通过的故障场景

- 12 个领取者争抢同一任务，只有一个成功。
- 执行权过期后接管，旧 epoch 的保存与续期被拒绝。
- 同一请求编号返回原任务，内容变化返回冲突；跨 owner 无法读取。
- 取消后拒绝旧执行者完成，不再自动领取。
- Unsafe 工具未确认结果时停止自动重试，人工提供结果后可以继续。
- 注入事件 INSERT 失败，完成状态和输出一并回滚。
- Redis 已发布、数据库未标记的情况下重试，不重复插入相同事件 ID。
- 删除 Redis 事件后，仍能从数据库读取已提交事件。
- 超过任务期限后进入失败状态。
- 同一会话拒绝并行活动任务，新一轮携带上一轮完整答案。
- 新建 Engine 后恢复澄清任务，不重新询问已经得到回答的问题。
- 幂等工具重试时沿用同一操作编号，不重复请求已保存的模型决策。
- AppForge MCP/OpenAPI 适配的路径、请求字段、业务错误判断符合本地 SDK 定义。

## 强杀进程验证

TestProcessKillResumesWithoutRepeatingTool 使用真实 HTTP API 提交任务、真实 PostgreSQL 保存状态、真实 worker 子进程执行。模型和工具是受控 HTTP 服务，使用实际 Eino OpenAI 适配器调用。

工具结果提交后，下一次模型调用被测试服务暂时阻塞。测试强制杀掉 worker 子进程，启动替代执行者，等待执行权过期后接管。断言任务完成、epoch 从 1 变为 2、工具只调用一次，并验证无需 Redis 的 SSE 完成事件回放。

这证明已提交步骤可以跨进程恢复；不等于真实模型效果、所有旧业务工具、部署升级或数据库灾备已经验证。

## 未执行的验证

未接入生产 Prism、AppForge、auth-center 和沙箱服务；未使用真实模型凭据；未做容量压测、数据库高可用切换、完整旧 SDK 兼容测试或依赖漏洞数据库扫描。本次重构范围以[框架设计](refactoring-design.md)为准，本节保留历史验证结果。

## Gin 改造验证

2026-10-03：API 改为 Gin v1.11.0、gin-contrib/sse v1.1.0。新增 HTTP 测试覆盖鉴权、DTO/header 校验、未知字段、多段 JSON、1 MiB 请求限制、404/405、游标校验、普通响应和 SSE 的 panic 处理，以及真实 PostgreSQL 上的提交幂等、越权访问、澄清回答、完成事件回放和取消。

两个仓库的 `go test -race -count=1 -timeout=60s ./...` 和 `go vet ./...` 均通过；worker 原有强杀进程接管测试通过 Gin API 提交并回放完成结果。API 和 migrate 入口构建通过。数据库调度和 worker 执行状态机未修改。

## 目录边界调整

2026-10-03：Go 私有实现迁入 internal，配置样例迁入 configs，跨仓库共享任务包迁入 pkg/durable。worker 的进程恢复测试改为构建并启动真实 API 可执行程序，通过 HTTP 调用，不再直接导入 API handler。

目录调整后，两个仓库均通过 `go test -race -count=1 -timeout=120s ./...` 与 `go vet ./...`，API/migrate 构建通过；跨进程恢复测试包含独立 API 程序和被强杀的 worker 子进程。

## GORM 模型与迁移

2026-10-04：移除独立 SQL 建表文件，三张表改为 GORM Model 与 AutoMigrate。API 在隔离的 PostgreSQL 17.6、Redis 7.2.10 上通过 `make integration`（含 race 和 vet）与 `make build`。新增测试直接写数据库，验证状态 CHECK、外键、复合主键、请求去重、四种活动状态的会话互斥、终态会话复用和数据库默认值；原有事件回滚与多发布者顺序测试继续通过。

升级验证使用 v0.1.3 的旧建表定义，在隔离 schema 中写入任务、步骤和事件，再连续执行两次新版迁移命令。迁移前后逐项对比字段、约束、索引和数据，结果一致；随后通过新版 Store 领取并完成旧任务，事件编号从 1 继续增长至 3。该验证覆盖当前旧版结构升级，不代表任意未来模型变更都能无损迁移。

## Agent、版本与会话

2026-10-04：新增 Agent 草稿、发布版本、Session 和 Message。自动化测试覆盖并发发布、revision 冲突、发布后重试提交、配置快照、多轮历史、消息分页、owner 隔离、配置写权限，以及消息写入失败时任务和事件全部回滚。worker 的真实子进程强杀测试增加消息断言：恢复后 User、Assistant 工具决策、Tool、Final 四条消息各一条，编号连续。

本地真实模型联调使用 Ollama `qwen3.8:27b-mlx`，通过 Eino OpenAI 适配器访问 `http://127.0.0.1:11434/v1`。第一轮提交后、worker 领取前发布版本二；旧任务仍使用版本一，调用本地只读求和工具得到 42。重启 worker 后第二轮正确回忆 17 和 25，并使用版本二的提示词。确认一次工具调用、六条有序消息、幂等重试返回原任务和 SSE 完成事件回放。此次真实模型验证在两轮之间重启；执行中强杀由上述自动化测试验证。

## 按业务模块拆分

2026-10-04：HTTP 路由、controller 和请求 DTO 按 Agent、Session、Run 拆包；共享事务实现按对应 service 文件组织。保持 v0.2 的路径、权限、参数、响应和持久化格式。继续通过完整路由的集成测试验证契约，未增加只断言目录或转发调用的测试。

## 公开 Go SDK

2026-10-04：对外请求、响应、错误和分页类型统一迁入根目录 sdk；controller 复用这些类型，参数校验仍由服务端执行。SDK 客户端覆盖 Agent、Session、Run 和原始 SSE 订阅，保持现有 HTTP 协议。

在独立 PostgreSQL 17.6、Redis 7.2.10 容器上通过 `make integration`（含 race、vet）和 `make build`。新增测试验证真实 SDK 创建与发布 Agent、会话、消息幂等、任务结果、消息分页、取消、owner 隔离、非法参数及 SSE 回放。客户端测试覆盖请求头、错误解码、context 取消、禁止重定向和响应体关闭责任。`go list -deps ./sdk` 确认除 SDK 自身外均为标准库；未增加生产依赖。

## Resty 与业务接口

2026-10-04：SDK 改用 Resty v2.17.2；Client 聚合 AgentAPI、SessionAPI、RunAPI，各自使用私有实现。三个 HTTP 模块改为依赖消费方定义的 Service 接口，装配层继续注入同一个 durable.Store，保留原有事务。

在隔离 PostgreSQL 17.6、Redis 7.2.10 上通过 `make integration`（race、vet）和 `make build`。新增验证覆盖未结束的 SSE 立即返回、关闭流后取消服务端请求、三个 Run 命令的 204 响应，以及并发调用的结果隔离。原有 SDK 错误、幂等、context 取消、重定向和响应体关闭测试继续通过。Go 调用入口变化记录于 sdk/README.md，HTTP 协议与持久化格式不变。

## server-go 生命周期重构

2026-10-13：API 固定引用 server-go v0.2.0，使用配置 Manager、Bootstrap、HTTPHost 和 Logger。连接及 Outbox、评测后台任务由 internal/service.API 拥有；生产入口统一走框架生命周期。保留现有业务 HTTP、SDK 和持久化契约，配置样例改用 HTTPServer.Listen，Helm 就绪探针改为 /healthz。

在本地 PostgreSQL 17.6、Redis 7.2.10 上完成 `make integration`（全仓库 race 测试与 vet）、`CGO_ENABLED=0 make build`、Helm lint、gofmt 和 diff 空白检查。数据库用例使用随机隔离 schema。新增框架入口测试覆盖未启动时的 503、启动后的探针、Bearer 鉴权、404/405、严格 JSON 校验、SDK 创建与发布 Agent、提交及取消任务、真实 HTTP SSE 事件回放，以及退出后后台任务已结束；配置测试覆盖环境变量优先级、占位符、应用与框架快照一致、SSE 写入超时及固定路由前缀。

通过临时 Go workspace 联合相邻 worker 验证 `TestProcessKillResumesWithoutRepeatingTool`（race），实际构建和启动本次 API，强杀 worker 后由替代执行者接管，已完成工具只调用一次，Redis 不可达时仍可回放已提交事件。worker 的测试配置同步为 HTTPServer.Listen；未改变 worker 的执行实现或持久化契约。

## server-go 项目与 SDK 布局

2026-10-13：入口迁至 cmd/server，迁移与评测统一由 cmd/cli 的 migrate／eval 子命令执行。业务模块迁入 internal/app，Tenant 与 Coding 外部协议及连接所有权迁入 internal/client，配置迁至框架默认 conf/server/config.toml，接口请求与评测套件位于 examples。SDK 迁入 sdk/go/agenticv1，采用领域文件布局并复用 server-go sdk.Client（Resty v3）；普通 JSON 默认 20 秒超时，SSE 继续由调用方 context 控制，客户端提供 Close。公开共享的 durable 和 evaluation 包继续供 worker／CLI 使用。

完成 API 与 worker 两仓库的 make integration，均包含全仓库 race 测试和 vet；worker 通过临时 workspace 使用本次本地 API，真实构建 cmd/server，覆盖强杀接管、工具不重复执行和 Redis 不可达时的事件回放。API 完成 CGO_ENABLED=0 make build，生成 bin/server 和 bin/cli；CLI 主命令、migrate、eval 的帮助入口正常。Helm lint、gofmt、diff 空白检查通过，部署镜像与初始化命令同步为 server／cli migrate。SDK 原有错误、非 JSON 响应、取消、重定向、并发与流关闭契约测试继续通过。

本轮开始时 Docker 引擎未运行；启动本地 OrbStack 与仓库 Compose 服务后完成上述外部依赖验证。没有把首次连接失败或缺少数据库时的跳过算作验证通过。

## 通用 HTTP 能力统一到 server-go

2026-10-13：删除 internal/httpx 与 internal/httpapi，总路由和契约测试归入 internal/service。JSON 绑定、请求大小限制、分页解析、Bearer 凭据解析与鉴权、身份读取、请求日志、Recovery、404/405 和错误响应统一使用 server-go。业务只保留 durable 错误到 HTTP 状态的映射。SDK 使用框架平铺错误，保留 requestId、statusCode 与实际 HTTP 状态。SSE 复用框架 stream 的事件 ID、心跳、编码与逐次写入期限，任务事件查询和回放仍由 Run 处理。

API 和 worker 均通过 make integration（全仓库 race 与 vet），使用本机隔离 PostgreSQL/Redis；worker 验证实际 API 进程、强杀接管和数据库事件回放。API 通过 CGO_ENABLED=0 make build。server-go 的 make check 覆盖根模块、四个示例、生成项目、HTTP 冒烟、格式、测试与 vet；额外对 auth、pkg/binder、sdk 和 stream 运行 race。SSE 回放测试改用真实 HTTP 连接，验证框架要求的写入期限支持。

本次新增框架能力尚未发布，以上验证使用本地 go.work 联合 API、server-go 与 worker。go.mod 仍记录已发布的 v0.2.0，独立下载或容器构建尚不能使用本次新增接口；需发布框架并升级依赖后再验证独立构建。

## server-go v0.3.0 发布版核对

已下载公开 v0.3.0 并更新 go.mod/go.sum。关闭 workspace 后执行 GOWORK=off CGO_ENABLED=0 make build，构建失败：发布版缺少 sdk.DecodeError。进一步核对发布包发现，SSE 的 SendWithID、Heartbeat 已包含；BearerTokens、ParseBearerTokens、UserID、BindJSON、QueryLimit、errors.Abort 与新增 HTTP 中间件尚未包含，仍是框架未提交的本地改动。独立与 Docker 构建需包含这些能力的新发布版；没有将 workspace 下的成功验证算作发布版验证通过。

## server-go v0.3.1 发布与独立验证

2026-10-13：通用 HTTP 能力随 server-go 提交 2da0895 发布为 v0.3.1，GitHub Release 为 https://github.com/FlyTOmeLight/server-go/releases/tag/v0.3.1。API 的 go.mod/go.sum 升级至公开 v0.3.1，不使用本地 replace；本地 go.work 移除 server-go，仅用于 API/worker 联合开发。

关闭 workspace（GOWORK=off）后通过 API make integration（全仓库 race、vet）和 CGO_ENABLED=0 make build。worker 使用仅含 API/worker 的 workspace 通过 make integration，涵盖真实跨进程恢复与 SSE 回放。Dockerfile 从公开模块独立构建镜像 agentic-kit-api:server-go-v0.3.1 成功，构建上下文排除 go.work；镜像内 /cli --help 正常，运行用户为 65532:65532。发布前框架 make check 与新增 auth、binder、sdk 的 race 测试通过，覆盖四个独立示例、生成项目和 HTTP 冒烟。

## API v0.5.0 发布整理

采用 server-go v0.3.1，发布 SDK/命令/配置目录迁移、平铺错误契约，以及当前工作区的持久评测和 Forge 项目/企业空间能力。README 升级说明与 Helm 元数据同步，chart 版本为 0.2.0、appVersion 为 0.5.0。Go 源码使用 gorgeous 整理，符合推送钩子。

格式整理后的代码通过 GOWORK=off make integration（race、vet）、CGO_ENABLED=0 make build；worker 联合测试通过完整 make integration，包括真实 API 进程、强杀接管与事件回放。Dockerfile 从公开依赖构建 agentic-kit-api:v0.5.0 成功，镜像内 CLI 帮助可运行；Helm lint 和 diff 空白检查通过。Docker 镜像在本机构建，未推送容器仓库。

## API v0.5.1 私有表模型归位

表结构按领域迁至 internal/table，模型集合由 table.Migrate 注册。pkg/durable 保留公开执行类型、Store 和业务事务，内部使用表模型并转换为领域对象；删除原 models.go，没有保留模型别名。公开 Spec 仍定义在 durable，持久化 ExecutionSnapshot 单独定义以避免循环依赖，JSON 格式不变。

通过 API 与 worker 的完整 make integration（race、vet）、GOWORK=off CGO_ENABLED=0 make build、gorgeous 格式检查。额外用已发布 v0.5.0 的模型创建隔离数据库并写入已完成任务、执行快照、checkpoint、事件和工具结果；新实现连续迁移两次后，145 项列/约束/索引定义逐项一致，全部已有数据保持一致。隔离 schema 在验证后清理。Docker 镜像 agentic-kit-api:v0.5.1 构建及镜像内 CLI 帮助通过，Helm lint 通过；首次 Docker Hub TLS 超时重试后成功。


## server-go v0.9.0 重构与 Coding 根模块升级

2026-10-14：采用 server-go v0.9.0、forge-coding-api 根模块 v0.17.0 与 Go 1.27.1。入口使用 app.Run，公开 service/ 装配与管理生命周期；配置使用框架快照和 TOML 占位符。数据库由 db.Module 接管，durable.New 借用连接并保留 UTC、唯一约束错误转换和静默 SQL 日志。专用通知缓存允许 Redis 不可用时启动并回放数据库事件。

HTTP 使用 core 类型、SDK 请求标签和 binder。幂等 Header 独立绑定，回归测试验证它不能被 query 替代、任意 Header 不能覆盖 URI 或 JSON。平台具体业务 Service 处理模板复制、数据集冻结、评测权限、比较、轨迹和 Coding 发布门槛，由装配层构造并注入。共享 durable 事务与持久化格式保留，普通单次操作不增加纯转发 Service。

SDK 全部操作改为 ctx 与操作请求指针；路径、查询和 Header 在请求对象里，幂等操作返回带 Created 的结果。CLI 领域命令移入 command/，通过 cli.CommandBuilder 装配。53 个业务 handler 的 Swagger 注释生成 76 项 OpenAPI 操作，测试逐项核对实际路由及路径参数。

API 在 GOWORK=off、无本地 replace 的正式发布依赖下通过 make integration（全仓库 race 与 vet）、CGO_ENABLED=0 make build 和 CLI 帮助入口；make doc、OpenAPI 路由检查、Swagger 实际 JSON 响应和 Helm lint 通过。入口显式安装 httpintegration.Install，使 SwaggerEnable、指标及其他可选 HTTP 配置生效。真实 PostgreSQL 测试验证借用连接不会被 Store 关闭、唯一约束错误保留，以及同一数据上的重复迁移。

worker 在 API/worker 联合 workspace 下通过完整 make integration（race 与 vet）。最终代码又通过 TestProcessKillResumesWithoutRepeatingTool，验证实际启动 API、强杀接管、不重复执行工具及 Redis 不可用时的 SSE 回放。仅更新 worker 跨进程测试生成的 API 配置，不修改执行实现。没有提交、推送或发布仓库。

## 平台业务删除与执行权截止校验

2026-10-18：删除平台业务、评测、CLI 与 Coding 客户端后，工具 kind 只接受空值或 HTTP；AppForgeMCP、AppForgeOpenAPI 及未知类型返回 ErrInvalid。CheckExecutionLease 同时检查 lease_until 与 deadline，超过期限的执行权不再通过（新增 TestCheckExecutionLeaseEnforcesDeadline，ToolKind 契约由 TestValidateAgentConfigToolKindContract 覆盖）。

在 deploy/compose.yaml 提供的隔离 PostgreSQL 17.6、Redis 7.2.10 上通过 GOWORK=off make integration（全仓库 race 与 vet）。`go test -json` 统计为 156 个用例通过、0 个用例跳过（无测试文件的包不计入）；GOWORK=off CGO_ENABLED=0 make build 通过。

worker 通过其 go.work 指向本地 API 工作树，完整 make integration（race 与 vet）通过，其中 TestProcessKillResumesWithoutRepeatingTool 确认通过，说明 deadline 收紧后的执行权检查不影响强杀接管。worker 仓库另有未提交改动，本次验证未修改或提交它。

## 框架重构阶段 1：纯值包与服务端依赖收敛

2026-10-18：阶段 1 拆分纯值包与领域层依赖方向。新增 `pkg/workspace`（Scope）、`pkg/agent`（Policy/ModelSpec/ToolSpec/AgentConfig/Agent/AgentVersion 及 `ValidateAgentConfig` 纯规则）与 `pkg/execution`（Status/Message/ToolCall/Spec/Run/Lease/Event/RunTrace/Session/SessionMessage/ExecutionSpec/FilesystemBinding 及错误集）。`pkg/durable` 公开类型改为上述纯值包的别名，导出名保持不变；`pkg/durable` 的直接依赖收缩为 `server-go/db`、`gorm.io/gorm` 及上述三个纯值包，不再导入 SDK、HTTP 框架或 Redis。SDK 改为依赖纯值包，不再 import `pkg/durable`。

阶段 1 之外：升级 server-go 到 v0.12.0，使用其 `db.NewDB`、`redis.NewRedis` / `NewRedisLazy`、`redis.XReadArgs` / `redis.Nil` / `redis.Redis` 别名。Outbox 拆为 `durable.ClaimOutbox`（返回 *gorm.DB 事务与是否首事件）与 `durable.MarkPublished`，Redis 发布与重试循环迁入 `service/outbox.go`（`publishOutbox`、`relayOutbox`）；事件相关测试随迁到 `service`。`pkg/durable` 不再 import go-redis/v9，`service` 装配改用 server-go 提供的 Redis 句柄，Redis 不可用时使用 `NewRedisLazy` 允许启动并通过数据库回放事件。Makefile 的 `swag` 搜索路径追加三个新包，让 SDK 别名能解析到目标类型。

依赖方向验证（`go list -deps`）：`pkg/agent`、`pkg/execution`、`pkg/workspace` 仅依赖标准库与彼此；`pkg/durable` 非测试直接 import 集合为 `context`、`crypto/*`、`encoding/*`、`errors`、`server-go/db`、`gorm.io/gorm`、`internal/table`、`pkg/agent`、`pkg/execution`、`pkg/workspace`、`net/url`、`strconv`、`strings`、`time`、`unicode/utf8`，不含 HTTP SDK、Gin、Resty；`service` 与 `internal/app/run` 不再直接 import go-redis/v9，仅依赖 `server-go/redis`。

副作用：阶段 1 让 SDK 公共类型变成领域包别名，swag 1.8.4 会跟随别名跳到目标包，OpenAPI 文档的 schema 名从 `agenticv1.AgentConfig` 等迁移到 `agent.AgentConfig`、`execution.Run` 等。API 处于 v0.x 阶段没有外部 SDK 消费者，记录此变化供后续阶段 4 处理；如需恢复 `agenticv1.*` schema 名称需把 SDK 类型改回 wrapper struct。

在 deploy/compose.yaml 提供的隔离 PostgreSQL 17.6、Redis 7.2.10 上通过 GOWORK=off make integration（全仓库 race 与 vet）。`go test -json` 统计 161 个用例通过、0 失败、9 跳过（DB 无关的子测试）；GOWORK=off CGO_ENABLED=0 make build 通过。`make doc` 重新生成的 OpenAPI 文档路径/操作与之前一致，只是 schema 名称随别名展开。worker 通过其 go.work 指向本地 API 工作树，完整 make integration（race 与 vet）通过，56 通过、0 失败、3 跳过，包含 TestProcessKillResumesWithoutRepeatingTool。

## 框架重构阶段 2：统一接受执行

2026-10-19：阶段 2 把 chat 与 application 两条接受路径合并为一个算法，并把版本解析与快照编译移出 durable 事务内核。

- `pkg/execution` 新增 `commands.go`（`SubmitCommand`、`SubmitKind`、`SubmissionFacts`、`PreparedSubmission`、`Submission`）与 `snapshot.go`（`CompileRunSpec` 纯编译）。该包仍不依赖 GORM、表模型或 HTTP。
- `pkg/durable` 新增 `tx.go`（`BindTransaction(tx *gorm.DB) *Tx`）与 `submission.go`（`Tx.Submit` 与 `Store.Accept`）。统一算法按顺序执行：请求 advisory lock、按 request_key 回放（**回放不调用 prepare**）、会话锁或按 scope 创建会话、活动任务检查、`prepare` 编译快照、写入 Run 与 User 消息与 RunQueued 事件。
- `pkg/durable/prepare.go` 提供 `PrepareChatSubmission`（当前发布版本，拒绝带 toolset 的配置）与 `PrepareApplicationSubmission`（显式版本、filesystem 绑定校验）；`loadConversationHistory` 随之迁移。
- `internal/app/execution` 新增提交编排服务，注入 `*durable.Store`，`session` 与 `run` 业务模块改为依赖它，不再断言 `*durable.Store` 实现 Service。
- 请求摘要保持与阶段 1 一致（chat 为 `{session,query}`，application 为 `{spec,ttl}`），因此既有幂等键与重放语义不变。`SubmitExecution` 的对外签名与 `MaxAttempts=5` 保持不变。
- 新增 `TestPublishSameRevisionReturnsExistingVersion`，确认同 revision 重复发布返回原版本且不新增版本行。
- 资产 Create/Update/Publish 仍由 `pkg/durable/agent_service.go` 提供；把资产事务迁入 `internal/app/agent` 与 `table/agent_query` 属于后续阶段，本轮未做。CreateDerivedVersion 未做。

行为测试无回归：`TestMessageAndRunRollbackTogether`、`TestConcurrentPublishAndSubmit`、`TestApplicationsReusePublishedAgentsAndTaskConversations`、`TestConcurrentApplicationRoundsShareOneConversation`、`TestPublishedSnapshotsAndConversationHistory`、`TestFilesystemBindingIsFrozenAndRequired`、`TestToolExecutionDoesNotRequireProject`、`TestApplicationExecutionSnapshotAndFencing` 全部通过。

在 deploy/compose.yaml 提供的隔离 PostgreSQL 17.6、Redis 7.2.10 上通过 GOWORK=off make integration（全仓库 race 与 vet）。`go test -json` 统计 162 个用例通过、0 失败、10 跳过；`GOWORK=off CGO_ENABLED=0 make build` 与 `make doc` 通过。worker 通过其 go.work 指向本地 API 工作树，完整 make integration（race 与 vet）通过，56 通过、0 失败、3 跳过，包含 TestProcessKillResumesWithoutRepeatingTool，说明统一接受路径未改变 worker 观察到的执行契约。

## 框架重构阶段 3：类型化执行命令与显式等待元数据

2026-10-19：阶段 3 撤掉 `Save`/`SaveDecision` 任意组合写入面，改为 `CommitBoundary`、`RecordDecision`、`BeginStep`/`FinishStep`、`SupplyResult` 等类型化命令；`durable_run` 新增 `waiting_step`/`waiting_kind` 列，API 与 worker 不再从 opaque state 解析 `waitingStep`。空库由 `internal/schema.Init`（advisory lock + AutoMigrate 七表）初始化，`service.Build` 在对外服务前调用 `durable.Store.InitSchema`；删除 `internal/table/migrate.go` 与 v0.1 legacy guard。`pkg/durable` 按 lease/boundary/step/interaction 等文件拆分；`pkg/execution` 新增 `Boundary`/`StepIntent` 与 `validate.go`。

新增测试覆盖非法 `CommitBoundary` 组合、`BeginStep` 三种 outcome、等待元数据生命周期与 schema 可重复初始化。静态检查：`rg` 确认 API 与 worker 源码无 `.Save(`/`SaveDecision` 调用，worker 无 `State.WaitingStep`；`go list -deps ./pkg/execution` 不含 gorm、server-go、SDK、Resty。

在 deploy/compose.yaml 提供的隔离 PostgreSQL 17.6、Redis 7.2.10 上通过 GOWORK=off `go test -race -count=1 -timeout=120s ./...`（`TEST_DATABASE_URL`、`TEST_REDIS_URL`）。`go test -json` 统计 **186** 个用例通过、**0** 失败、**0** 跳过；`GOWORK=off CGO_ENABLED=0 make build` 与 `make doc` 通过（OpenAPI Run 增加 `waitingKind`）。

worker 通过 go.work 指向本地 API 工作树，`TEST_DATABASE_URL` 下 `go test -race -count=1 -timeout=120s ./...`：**52** 通过、**0** 失败、**1** 跳过（需 AIO 环境的公开 SDK 测试）；包含 TestProcessKillResumesWithoutRepeatingTool、TestUnsafeErrorPausesInsteadOfRetrying（断言公开 Run 的 `waitingStep`/`waitingKind`）。worker `go.mod` 仍 pin 已发布 API 版本且无 replace；**GOWORK=off** 独立构建 worker 需等 API 发布含本阶段符号的版本，联合验证以 go.work 为准。

## 框架重构阶段 4：上下文构建与快照冻结

2026-11-09：新增 `pkg/contextbuilder` 首版，提供完整消息组校验、Memory 优先级、token/byte 预算裁剪、`ContextBudgetExceeded` 稳定错误，以及带格式版本、Policy 版本和 SHA-256 digest 的 `ContextSnapshot` 编解码。`pkg/durable/prepare.go` 改为按 Run 聚合历史：快照保留 User、Assistant（含 tool calls）、Tool、Final 的完整链，跳过缺少配对工具结果的消息组；`Spec.History` 仍只投影 User 与最终 Assistant 文本，避免改变当前 worker 的输入形状。系统指令、工具定义、历史组、Policy 和 Budget 与 Run 同事务写入 `durable_run.context_snapshot`。快照列使用 text 保存原始 envelope 字节，避免 jsonb 重排导致 digest 失配。

验证范围：`GOWORK=off go test -count=1 ./...`（`TEST_DATABASE_URL` 指向 deploy/compose.yaml 的隔离 PostgreSQL）通过；`GOWORK=off go test -race -count=1 ./pkg/contextbuilder/ ./pkg/durable/` 通过；`GOWORK=off go build ./...` 通过。新增用例覆盖快照往返、篡改与不一致 Memory 模式、工具链保留、失败但完整的上一轮进入历史、取消留下的不完整轮次被跳过，以及回放不改变快照。

未验证：worker 未接入每轮 `Build`，未验证动态工具结果超限后的 Fail 边界；Memory RecallTx 和模型窗口预算元数据尚未实现，因此当前冻结的 Budget 是未约束值。

## 上下文增强阶段一、阶段二：预算保护与历史两级裁剪

2026-11-09：在 API `feat/context-builder` 与 worker `feat/context-worker-adapter` 的隔离工作树完成，未改动两个根工作树。该记录更新上节的 worker 接入、预算元数据及超限边界状态。

- 阶段一：`ModelSpec.ContextWindow` 与 `MaxTokens` 校验；接受期冻结输入预算并预检，超限返回 HTTP 400 `ContextBudgetExceeded` 且不创建 Run、消息或事件。未配置窗口时使用 128 KiB 字节上限，配置窗口时使用 `utf8-byte-estimate-v1` 和输出预留；估算不是精确 tokenizer 计数。单条工具正文只对模型做 UTF-8 安全首尾预览。
- 阶段二：新 Run 冻结 `context-v2`，先移除低优先级 Memory，再从旧到新保留历史问答、折叠工具过程，仍超限才删除整轮。没有最终答复或包含额外 User 输入的历史不折叠。`context-v1` 保持旧的整轮裁剪规则。构建结果分别记录完整省略与保留但已折叠的来源。
- worker 在真实模型调用边界按实际工具 schema 构建输入，构建失败发生于 `ModelStarted` 之前，不自动重试；不通过 `BeforeModelRewriteState` 写回预览。原始步骤、消息与 checkpoint 保持完整。历史工具关联 ID 加 Run 前缀，避免与其他历史轮次或当前轮次冲突。

验证使用 README 指定的本机 PostgreSQL/Redis，测试分别创建独立 schema。API 在 `GOWORK=off` 下执行全仓 `go test -json -race -count=1 -timeout=120s ./...`：**256** 个测试及子测试通过、**0** 失败、**0** 测试跳过。随后补充 token cap 与预览下限边界用例，`go test -race -count=1 ./pkg/contextbuilder`：**56** 通过。API `go vet ./...`、`go build ./...` 和 `make doc` 通过，OpenAPI 新增 `contextWindow`。

worker 通过本地 `go.work` 指向 API 隔离树执行全仓 race：**63** 个测试及子测试通过、**0** 失败、**1** 跳过（`TestAIOPublicSDKAndEinoTools` 需要真实 AIO 环境）。`go vet ./...`、`go build ./...` 通过。新增用例覆盖实际工具定义超限、空实际工具集替换冻结定义、历史折叠与整轮淘汰，以及模型收到预览但下一工具边界 checkpoint 和持久消息保留完整正文。`TestProcessKillResumesWithoutRepeatingTool` 通过，验证强杀后接管且工具不重复调用。

独立只读审查由 1 个子代理完成，主代理核实并修复 checkpoint 写回和工具 ID 冲突。Memory 维护、召回与 artifact 接入仍未实现，提交固定 Off；没有实现 LLM 摘要。本次跨仓库结果是本地联合验证，worker 正式依赖版本尚未发布或升级，未进行生产部署或性能容量测试。

同日收口复验：API 在 `GOWORK=off` 下执行 `make integration`（全仓 race 与 vet）及 `CGO_ENABLED=0 make build` 通过；worker 使用隔离树 `go.work` 执行相同检查与构建通过，正式依赖独立构建仍待 API 发布后验证。两仓同时运行集成检查时，worker 的强杀恢复用例曾因 API 未在 5 秒内就绪而失败，日志包含预期离线 Redis 的连接重试；该用例单独复跑及 worker 全仓串行复跑均通过。该次就绪超时的原因尚未定位，未修改测试期限或宣称已修复。

## 上下文增强阶段三 A：当前轮工具链折叠

2026-11-09：新增 `context-v3`。它沿用 `context-v2` 的 Memory 与历史裁剪顺序；历史耗尽后仍超预算时，把当前轮较旧的工具结果替换为投影标记，保留最近 `Policy.KeepRecentToolResults` 条完整结果（默认 3 条）。投影只影响本次模型输入，不删除 assistant/tool 关联，不修改 Eino checkpoint、冻结材料、已存消息或工具步骤。`BuildResult.ProjectedToolResults` 记录实际投影条数，worker 将其写入 `ModelStarted` 事件的 `context.projectedToolResults`。新 Run 冻结 `context-v3`，`context-v1`/`context-v2` 快照保持原有规则。

验证：API `GOWORK=off make integration`（全仓 race 与 vet）通过；`GOWORK=off go test -count=1 ./...` 统计 **267** 个测试通过、0 失败。新增用例覆盖仅投影较旧结果、默认保留条数、投影后仍超限报 `ContextBudgetExceeded`、预算充足时不投影、负保留数拒绝、v3 快照往返以及原有 v1/v2 行为不变。`go build ./...` 与 `make doc` 通过且 OpenAPI 无变化。

worker 在隔离工作树通过本地 replace 指向 API 改动，`GOWORK=off make integration`（全仓 race 与 vet）通过；新增 `TestBuildContextProjectsOlderCurrentToolResults` 验证真实 Eino 状态消息中旧结果被投影、最近 2 条完整保留、用量下降且原消息未被修改。

未验证：当前轮折叠后的模型输出质量与摘要收益未做线上评估；投影标记的措辞可能影响模型行为，尚无对比实验。Memory 召回、artifact 接入与 LLM 摘要仍未实现。

## Memory 阶段四：人工维护、可选 artifact 归档与 RecallTx 冻结

2026-11-16：在 `feat/memory-artifact-publish` 隔离工作树完成，未改动根工作树。Memory 拥有身份、OwnerPrivate 权限、生效投影和操作回执；artifact-go `v0.5.0` 仅经 `internal/client` 适配，事务外做集合创建与版本发布，本地事务用 CAS 激活固定 VersionRef。`pkg/durable/prepare.go` 的 RecallTx 从同一事务读取生效投影并冻结进 ContextSnapshot；执行与恢复不访问 artifact。

- 维护闭环：Create/Get/List/Update/Retire/Restore，删除语义为 Retire，Restore 重新发布新版本并激活；HTTP、SDK、OpenAPI、配置、Helm 与文档同步。
- 故障语义：操作回执持久化发布意图；archive claim 2 分钟、远端调用总预算 1 分钟；原键重放恢复 Pending/Applied；激活提交失败按原键确认，结果不明返回 HTTP 503 `MemoryOperationOutcomeUnknown`；410 墓碑保留固定引用并拒绝激活；已有归档绑定不能关闭归档后静默退回本地写入。
- 稳定性修复：集合创建键按 entryID 稳定派生，版本发布键按操作 ID 派生；首次归档在 revision 竞争中失败后，显式 Restore 复用原集合键，避免同名集合冲突（`TestFirstArchiveLosesRevisionThenRestores`）；OwnerPrivate 的存储 user 固定为 scope.Owner，不随 actor 变化。

验证（全部 `GOWORK=off`，无本地 replace）：

- 第一轮定向检查包含无数据库环境的测试，不能据其通过数认定数据库用例已执行。补齐 `prepare` 拒绝路径的提交结果确认后，带隔离 PostgreSQL/Redis 的全仓 `GOWORK=off make integration`（race 与 vet）通过，覆盖归档、并发修订、墓碑恢复与 RecallTx。
- `go test -race -count=1 -v ./internal/app/memory -run '^TestCommit'`（隔离 PostgreSQL）：5 组、10 个故障注入场景全部通过，覆盖 Create 激活、Update 激活拒绝、Retire 生效/拒绝与 prepare 拒绝。分别模拟实际提交后报错及实际回滚后报错，核对持久回执、投影与原键重放；首次事务回滚时回执也不存在，不能当作已持久化 Pending。
- `CGO_ENABLED=0 go build ./...`、`git diff --check` 通过；`helm lint charts/agentic-kit-api` 1 chart、0 failed；`make doc` 重新生成 OpenAPI，三个写接口含 503 响应。首次推送被 gorgeous 格式检查阻止，已应用格式修复。
- 经验补充：[数据库租约时钟](lessons/database-lease-clock.md)：租约测试改用 `clock_timestamp()` 表达式后 `TestArchiveClaimExpiresAndCanResume` 通过。

未验证与未实现：真实 artifact-go 部署未联调（仅 fake archiver 与 httptest adapter）；版本历史 API、自动提炼、语义检索未实现；对外仅 OwnerPrivate，完整认证 middleware 与多主体共享授权未接入；本地模式（未配置 Artifact Endpoint）下无远端版本引用。

## Memory 阶段五：后台 Pending 恢复

2026-11-17：在 `feat/memory-artifact-publish` 隔离工作树完成，未改动根工作树；本阶段未修改 worker。API 在配置 archive 时启动后台恢复，复用持久操作回执、archive claim 和本地 CAS 激活，继续未完成的 Create/Update/Restore。

- 恢复范围：仅 Create/Update/Restore；Retire 是本地短事务，没有远端 Pending。后台不自动重试 Rejected，远端不可用或结果不明时保持 Pending。
- 调度与并发：启动后立即执行一轮，之后每 30 秒一轮；单轮最多 20 条、预算 2 分钟，单条操作 90 秒。未过期 claim 跳过，多实例依靠 claim 与本地 CAS 去重；停机时未完成项保持 Pending，由下次启动或其他实例继续。
- 队列推进：Archive 领取和释放 claim 都会刷新 `updated_at`；失败项移到队尾。损坏回执和无 archive binding 的 Pending 回执也会刷新 `updated_at`，避免长期占住批次头部。单项失败聚合返回，不中断整批。
- 生命周期：恢复循环与 Outbox 共用 `WaitGroup`，`Stop` 取消后等待两者退出；当前不提供常驻运维 Pending 列表接口。

验证（全部 `GOWORK=off`）：`gofmt`、`git diff --check` 通过；`go test ./internal/app/memory ./service -run '^$' -count=1` 完成编译检查；`go test ./service -run '^TestResumePendingMemory' -count=1` 通过 3 个用例，覆盖首轮立即执行、周期执行、期限与取消退出。

2026-11-17 数据库环境复验：接入本地 PostgreSQL 与 Redis 后，`go test ./internal/app/memory -run '^TestResumePending' -count=1` 通过 11 个用例，覆盖 Create/Update/Restore 恢复、集合创建失败恢复、未过期 claim 跳过、损坏回执与无 binding 回执推进，以及无 archive 和 `limit=0` 边界。全仓 `GOWORK=off make integration`（`go test -race` 与 `go vet`）通过，共 456 个用例。

同轮修复：全仓测试暴露生命周期用例在没有 archive 时仍启动恢复循环，typed nil 接口绕过 `resumer == nil` 检查并在 `ResumePending` 解引用 panic。`routes.memoryRecovery` 改为 `pendingResumer` 接口类型后，nil 判断恢复生效；`TestAPIFrameworkLifecycle` 覆盖该路径。

未完成验证：真实 artifact 部署与跨服务故障联调仍未执行，`Artifact` 不可用时的真实超时与恢复节奏只有单元/集成测试证据。

## Memory 生效版本历史

2026-11-17：新增 OwnerPrivate 版本列表与详情，列表基于本地已生效内容操作回执，详情先检查条目归属和生效回执，再读取固定归档版本。纯本地模式的历史列表为空；Retire 不产生历史版本，停用时不标记活跃项。

验证：设置本地 PostgreSQL 和 Redis、使用 `GOWORK=off make integration`，全仓 race 测试和 `go vet` 通过；`make doc` 生成 OpenAPI。新增测试覆盖分页、跨 owner 拒绝、未采用版本、Pending/Rejected 过滤、停用和恢复、正文缺失、本地模式、归档服务模拟与 SDK 请求。归档适配使用 HTTP fake，尚未与真实 artifact 服务联合验证。

## Memory 真实 artifact 联合验证

2026-11-17：在 `test/memory-artifact-integration` 分支及 `.worktrees/context` 隔离工作树完成本地联合验证，未修改 API 根工作树或 artifact-go 根工作树。本节更新前述真实服务联调尚未执行的状态；外部部署验收仍未完成。

测试直接使用正式依赖 artifact-go `v0.5.0` 的 `service` 与 `tests/fixture`，由 server-go HTTP host 启动真实 artifact HTTP 服务。artifact 与 Memory 分别使用本机 PostgreSQL 的随机 schema，并在测试后清理；未使用本地 replace。身份解析器使用测试授权表验证 Bearer 服务凭据，再核验 tenant、project、resource-user 三元作用域。

- 认证与配置：新增 `Artifact.BearerToken` 和 `ARTIFACT_BEARER_TOKEN`，启用 Endpoint 时拒绝缺失或空白凭据。集合创建、版本发布、固定版本读取均携带凭据；Helm 通过 `existingSecret` 注入。服务凭据不替代作用域授权。
- 幂等与隔离：`TestArtifactIntegrationScopeAndReplay` 覆盖集合及版本原键重放、同键异参冲突、固定版本读取、跨 tenant/project/user 隔离，以及空或无效 token 拒绝。
- 发布结果未知与恢复：`TestArtifactIntegrationPendingRecoveryAndHistory` 通过反向代理丢弃远端已经提交的发布响应，确认本地回执保持 Pending；随后注入 503，确认恢复失败期间不激活新版本、历史只展示已生效版本。恢复连接后以原键继续，核对 VersionID、Sequence、VersionURI 与远端已提交版本一致，并检查历史、详情、跨 owner 拒绝及重复恢复无新增操作。

验证结果（全部 `GOWORK=off`）：

- 设置 `TEST_DATABASE_URL`、`TEST_REDIS_URL`、`ARTIFACT_TEST_PG_DSN` 后，`make integration` 全仓 race 测试与 `go vet` 通过。
- 格式化后的定向复验 `go test -v -count=1 -run '^TestArtifactIntegration' ./internal/app/memory` 通过，2 个顶层测试及 3 个作用域子测试，共 5 个用例，无跳过。
- `CGO_ENABLED=0 make build`、`make doc`、`gorgeous ./...` 通过；OpenAPI 生成文件无变化。
- 1 个子代理完成配置与认证传递的只读审查，发现并修正 README 中入站 token 与出站服务凭据的文案歧义。

测试 fixture 引入 13 个间接模块依赖，其中 AWS SDK/S3/STS 链来自 artifact 服务的 storage 装配，`creasty/defaults` 来自 server-go binder。新增 artifact service/fixture 导入仅在测试文件，正式构建已通过；artifact-go 与 server-go 仍分别固定 `v0.5.0`、`v0.13.0`。

未验证：外部实际部署、真实凭据签发和身份解析器、独立进程强杀、真实网络超时与恢复节奏、线上性能容量。本次代理故障注入证明同键恢复与本地激活行为，不等同于上述外部故障演练。自动提炼与语义检索不在本次范围。

## 租户级 ActorPrivate 维护与召回

2026-11-18 在本地 PostgreSQL、Redis 和真实 artifact HTTP 测试服务上运行 `GOWORK=off make integration`，已启用 `ARTIFACT_TEST_PG_DSN`。全仓 race 测试与 `go vet` 通过。

- `TestArtifactIntegrationActorPrivate` 验证归档绑定固定到租户和本人、跨用户和跨租户隔离、项目请求无法读取条目或重放停用回执，并检查恢复后的版本列表及原版本正文。
- `TestApplicationActorPrivateRecall` 经应用执行服务提交 Run，验证本人能召回、其他用户召回为空、替换 actor 被拒绝。更新记忆后，新的独立任务读取新正文，重新加载原 Run 仍得到原快照。

artifact 测试使用本地身份解析器。Forge 凭据签发、项目成员授权和外部部署仍需单独验收。
