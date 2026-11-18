# Agent 资产接入 artifact-go 技术设计

API 已接入 Forge 鉴权、tenant 隔离和 artifact 出站服务身份传播。API 使用自身服务凭据传播已核验的目标身份。当前使用已发布的 artifact-go `v0.5.1`，独立构建和 OrbStack 联调范围见 [验证记录](verification.md#2026-11-17-agent-版本发布与-run-快照)。下文保留 2026-10-20 的设计背景和实施方案，正文存储决策见 [ADR-0009](adr/0009-agent-versions-in-artifact.md)。

## 1. 背景

Agent 资产的六个操作在 `pkg/durable/agent_service.go`：`CreateAgent`、`Agent`、`UpdateAgent`、`PublishAgent`、`AgentVersion`、`Agents`。HTTP 的 `internal/app/agent/service.go:24` 用 `var _ Service = (*durable.Store)(nil)` 直接断言 store 实现接口。[框架重构设计 §11](refactoring-design.md) 要求把资产事务迁到 `internal/app/agent` 和 `internal/table/agent_query.go`，`pkg/durable` 只保留执行一致性算法。这是阶段 2 遗留的尾巴。

版本正文目前存在本地 `agent_version.config`，发布是「推进指针 + 插一行」。用户决定：已发布版本的正文交给 artifact-go。

## 2. 设计决策

### 2.1 正文只存一份

| 位置 | 保存什么 | 为什么 |
| --- | --- | --- |
| `agent` 表 | 身份、名称、可编辑草稿、revision、发布指针 | 草稿是可变工作副本，编辑语义是本地 revision 比较交换 |
| artifact-go | 已发布版本的不可变正文 | 编号、不可变、保护、历史是版本服务本来的职责 |
| Run 快照 | 接受任务时冻结的完整配置 | 一次执行的事实，重试和进程接管都读它 |

上一版设计让本地 `agent_version` 继续存一份 `config` 投影，理由是「执行提交要在事务内读配置」。这个理由站不住：执行提交时读远端和读本地都是等待一次外部调用，而真正的约束是**接受 Run 的事务里不能发起网络请求**（它会拉长持锁时间，并在远端故障时让接受失败）。这个约束可以通过「先取正文、再开事务」解决，不需要存两份正文。所以本地 `agent_version` 删除 `config` 列。

草稿不是第二份正文。草稿是用户正在编辑的工作副本，发布只读取它一次；发布后用户继续编辑，草稿与已发布版本自然分叉。因此草稿留在本地，并继续由本地 revision 保护。

### 2.2 公开版本号本地分配，发布指针留在本地

公开版本号 `number = published_version + 1`，在 agent 行锁内分配，与今天一致。artifact 自己也有版本号和序列号，但那属于远端实现细节，不进入公开契约：如果直接采用远端编号，一旦本地提交失败，公开版本号就会出现空洞。

发布指针 `published_revision`、`published_version` 留在 `agent` 行。API 以本地指针为准，artifact 只回答「这个版本 ID 的正文是什么」。

### 2.3 跨服务提交：远端先建，本地后认；重试靠固定键收敛

发布一次要落两个系统。顺序固定为「远端先建不可变版本，本地再提交指针」，这样不会出现「本地已发布、远端没有正文」的坏状态。剩下的问题是第二种失败：

```text
1. 本地读草稿           → revision 3 的配置
2. artifact 建版本      → 成功，得到版本 V
3. 本地提交指针         → 进程崩溃 / 连接断开 / 数据库错误，未提交
```

此时远端多了一个没有任何本地版本指向的版本 V。处理办法不是回滚（跨服务无法回滚），而是保证**这个孤儿不会变成第二个**：

- 远端发布键 = `sha256(agentID + ":" + revision)`，由已持久化的数据推导。同一个 revision 的任何重试都命中同一个键，artifact 返回原来的版本 V，不会新建。
- 本地提交在 agent 行锁内完成，重试时把第 1–3 步整体重跑，最终收敛到「本地指针指向 V」。
- 本地提交前在锁内复核 revision；复核失败说明草稿已被改到别的 revision，此时 V 成为永久孤儿。

所以准确性来自「固定键 + 整体重试」，不是「远端成功即视为完成」。这一点上一版写成「自然幂等、不需要回执表」，说得过满：固定键能防止重复版本，但不能保证第 3 步一定成功。设计上接受一个极小窗口内的孤儿版本，代价和理由写在 §5.3。

### 2.4 身份：只使用已核验身份，不代为选择任意归属

向 artifact 发请求涉及三件事，必须分开：

- **谁在操作**（actor）：当前调用者。
- **访问哪个数据范围**：租户、可选项目、可选个人归属。artifact 要求租户必填。
- **凭什么允许**：artifact 部署中的 `IdentityResolver` 负责核验，它只接受已验证结果，不信任客户端自报 Header。

本服务已接入 Forge middleware，`owner` 取自验证后的 `auth.UserID(c)`；tenant/project 通过 `access.WithScope` 写入请求 context，HTTP 用户与 propagation 路由末端均拒绝空租户。artifact 调用契约如下：

- `Build` 将 API 自身的 `Tenant.ServiceAPIKey` 注入 `NewArtifact`，用于出站 Bearer。`RequestIdentity` 只读取 middleware 已核验的 user/tenant/project 和 RequestID，忽略入站 Authorization、Cookie、CSRF 和 Origin，不混用会话与 Bearer。
- `TenantID` 取自框架的已核验租户键，`ProjectID` 取自已核验的项目键（可为空）。
- `PropagatedUserID` = 本次操作的已验证用户；SDK 通过 `x-impersonate-target-*` 传播用户、租户及可选项目。`ResourceUserID` 固定为同一用户，保留个人归属。空项目用 nil；缺少有效 tenant 或 actor 时，适配器在远端调用前拒绝，不能省略 owner 并退化为共享范围。

artifact 部署必须启用身份传播，并将该 key 对应的 API 服务名加入传播白名单。artifact 校验服务身份和目标范围，`X-Resource-User-ID` 与传播用户相同，保持每个用户只访问自己的 Agent。系统预置或共享 Agent 仍需要新的 AssetScope 与授权契约，另行设计。浏览器不得持有服务凭据。

**验证边界**：本地 SDK 与假服务测试验证出站凭据、传播头及拒绝路径；真实 Forge/artifact 环境仍需验证服务白名单、目标身份和个人资源授权。缺少已验证租户或用户时，直接调用适配器返回依赖不可用错误，不设默认身份。

## 3. 数据模型

### 3.1 `agent` 表

当前字段：`id`、`tenant_id`、`project_id`、`owner_id`、`name`、`draft`、`revision`、`published_revision`、`published_version`、`created_at`、`updated_at`。tenant 来自已验证 scope，查询同时约束 tenant 与 owner。

不新增 `artifact_id` 列：集合名固定为 `agentID`，发布时用固定请求键幂等创建集合即可，不需要本地缓存远端 ID。

### 3.2 `agent_version` 表

| 列 | 说明 |
| --- | --- |
| `agent_id`、`number` | 复合主键；`number` 是本地分配的公开版本号 |
| `artifact_version_id` | 远端版本 ID；读取正文时使用 |
| `artifact_version_uri` | 远端固定版本 URI；追溯与 Resolve 使用 |
| `created_at` | 本地发布时间 |

**删除 `config` 列。** 本地不保存版本正文。

### 3.3 初始化

`internal/schema/init.go` 已对 `table.Agent`、`table.AgentVersion` 执行 AutoMigrate。按 [布局约束](layout.md#共享持久化边界)，初始化只面向空库或空 schema，不迁移或回填旧的无 tenant 数据，也不删除已有旧索引（如历史 `memory_operation_owner_key`）；含旧 `config` 列的库及旧索引升级需另行处理。

### 3.4 共享类型

本地表格中的 `agent.Agent`、`agent.AgentVersion` 是 API 与 SDK 的公开形状，保持不变；`AgentVersion.Config` 在读取时由远端正文填充。`pkg/durable` 的别名（`AgentConfig`、`Agent`、`AgentVersion` 等）保留，worker 依赖它们。

## 4. artifact 接入契约

### 4.1 依赖版本

`go.mod` 固定 artifact-go `v0.5.1`，所用 SDK 支持 `BearerToken` 和 `PropagatedUserID`。独立构建使用 `GOWORK=off` 和 `-mod=readonly`。

只 import `sdk/go/artifactv1`，不 import 服务端 `internal` 包。

### 4.2 消费方接口

`internal/client/artifact.go` 定义的小接口；artifactv1 类型限制在 `internal/client`：

```go
// ArtifactStore 保存 Agent 版本的不可变正文。
type ArtifactStore interface {
    // EnsureCollection 幂等创建并返回集合 ID（type=agent，name=agentID，整数编号）。
    EnsureCollection(ctx context.Context, identity Identity, agentID string) (string, error)
    // PublishVersion 用固定 requestKey 发布不可变版本；重放返回原版本。
    PublishVersion(ctx context.Context, identity Identity, artifactID, requestKey string, payload []byte) (VersionRef, error)
    // GetPayload 按固定版本 ID 读取正文。
    GetPayload(ctx context.Context, identity Identity, artifactID, versionID string) ([]byte, error)
    Close() error
}

type VersionRef struct{ ID, URI string }
```

接口中的 `Identity` 即 `client.Identity`，由 Controller 为每次请求构造，包含已验证的租户、可选项目、目标用户及 RequestID。目标身份随本次调用传递；共享客户端仅持有 API 自身的服务凭据。每次调用覆盖继承的 SDK 身份，避免带入其他凭据或范围。

### 4.3 幂等键

| 键 | 取值 | 作用 |
| --- | --- | --- |
| 集合创建 | `sha256(agentID + ":collection")` | 首次发布建集合；重试返回同一集合 |
| 版本发布 | `sha256(agentID + ":" + revision)` | 同一 revision 重试重放同一版本 |

64 位十六进制，满足 artifact 的 64 字符上限。键由已持久化的值推导，不需要新增回执表。

注意 artifact 的版本键空间是 `(artifact_id, actor_id, request_key)`：同名集合在**个人范围**与**共享范围**是不同集合，actor 不同也是不同键空间。因此固定键只保证同一 owner、同一集合内的重试收敛；跨 owner 的集合互不可见，这正是隔离的预期行为。

### 4.4 大小限制

artifact 默认 `MaxPayloadBytes = 262144`（256 KiB），按 `json.Compact` 后的字节数计算。发布前用 `json.Marshal` 得到的配置长度校验，超限返回 `ErrInvalid`（400），不发起远端调用。

## 5. 发布流程

### 5.1 时序

```text
1. 短事务：FOR UPDATE 锁 agent 行，读 draft / revision / published_revision / published_version
     revision != 请求 revision              → ErrConflict
     published_revision == 请求 revision    → 返回既有版本（本地幂等，不调远端）
2. EnsureCollection（固定集合键，重放安全）
3. PublishVersion（固定版本键，payload = 第 1 步锁内读到的 draft）
4. 本地事务：FOR UPDATE 再锁 agent 行
     published_revision == revision          → 返回既有版本（并发幂等）
     revision != 请求 revision               → ErrConflict（远端版本成为孤儿）
     number = published_version + 1
     插入 agent_version(agent_id, number, artifact_version_id, artifact_version_uri)
     更新 published_revision / published_version
5. 返回本地版本；正文直接用第 1 步读到的 draft，不再读远端
```

第 1 步的锁保证发布出去的内容与 revision 对应；第 4 步在锁内复核，才是权威判断。两步之间草稿可能被编辑，第 4 步负责拒绝。

### 5.2 并发

- 同一 revision 并发发布：远端键相同，得到同一个远端版本；本地锁内第二个请求返回既有版本，不报冲突。
- 不同 revision 并发发布：本地锁内只有一个成功，另一个返回 409。

### 5.3 失败处理

| 场景 | 结果 |
| --- | --- |
| 远端超时、5xx、`OperationOutcomeUnknown` | 503；本地不写；客户端用同一 revision 重试 |
| 远端 401/403、`ScopeMismatch`、`ScopeInactive` | 503；属于部署或 resolver 配置问题，记录日志 |
| 集合已归档（`ArtifactArchived`） | 409，需要人工处理 |
| `IdempotentParameterMismatch` | 409；键推导有 bug |
| 配置超过 256 KiB（`PayloadTooLarge`） | 400 |
| 第 4 步 revision 复核失败 | 409；远端版本成为孤儿 |
| 第 4 步未提交（进程退出、连接断开） | 远端版本成为孤儿；同一 revision 重试后收敛到它 |

孤儿版本不进入本地版本表，因此不影响公开版本号、版本读取、发布指针或 Run 快照；它只占远端存储，并在人工查询 artifact 时可见。这是「不引入跨库两阶段提交」的已知代价，记录为可接受，不新增对账任务。

## 6. 读取与执行提交

### 6.1 读取版本

`GET /v1/agents/{id}/versions/{number}`：

1. 本地校验 agent 属于 owner，否则 404。
2. 本地 `agent_version` 取 `artifact_version_id` 与 `created_at`。
3. 远端读取正文，组装 `AgentVersion{AgentID, Number, Config, CreatedAt}`。

版本读取因此依赖 artifact 可用，不可用返回 503。Agent 详情、草稿读取和列表仍只走本地。

### 6.2 Run 提交

今天的 `pkg/durable/prepare.go` 在接受事务内读 `table.Agent` 和 `table.AgentVersion.Config`。正文移到远端后，改为「事务外取正文，事务内编译快照」：

```text
1. 回放预检：按 (owner, requestKey) 读已有 Run；命中且 hash 一致 → 直接返回（不读远端）
2. 解析版本指针：
     聊天：session → agent → published_version
     应用：请求中的 AgentID + AgentVersion
   再读本地 agent_version，取 artifact_version_id
3. 远端读取正文 → agent.AgentConfig
4. durable.Accept(cmd, prepare)：事务内重检回放、锁会话、编译快照、写 Run/消息/事件
```

第 4 步不调用 artifact。第 2–3 步与第 4 步之间发布指针可能前进，但 Run 使用的是第 3 步取到的不可变版本，语义与今天读本地行相同。

`durable.Accept` 签名不变；`prepare.go` 改为闭包工厂：

```go
// 正文由调用方预取；闭包只在事务内读取会话历史并编译快照。
func PrepareChat(config agent.AgentConfig, agentID string, version int64) PrepareSubmission
func PrepareApplication(config agent.AgentConfig, agentID string, version int64) PrepareSubmission
```

注意两个行为差异：

- 聊天提交的「agent 未发布」检查（今天在 `prepare.go` 里判断 `PublishedVersion == 0` → 409）提前到第 2 步，仍在事务外。
- 应用提交的 toolset/filesystem 一致性校验保留在 `PrepareApplication` 内；它只依赖配置值，不需要查表。

`submission.go` 的 `lockSubmitSession` 不再读 `table.Agent`：聊天分支本来只读 session，应用分支读 agent 是为了取 `agentRow.ID` 建会话，可改为直接使用请求里的 `AgentID`（第 2 步已确认它存在且属于 owner）。`session_service.go` 的 `CreateSession` 内联 agent 存在性与已发布检查。

## 7. 职责划分与代码改动

### 7.1 `pkg/agent`

- 用 `ErrInvalid`、`ErrNotFound`、`ErrConflict`、`ErrUnavailable` 四个哨兵替代现有 `ErrInvalidConfig`；`ValidateAgentConfig` 的校验规则不变，失败改为返回 `ErrInvalid`。`ErrInvalidConfig` 目前只被 `pkg/durable.ValidateAgentConfig` 包装，而该包装在本轮删除，因此可以整体替换，不保留别名。
- 不引入 HTTP、GORM 或 artifact 类型。

### 7.2 `internal/client/artifact.go`（新增）

§4.2 接口的实现。构造时接收 endpoint、API 自身 service key 和超时，拒绝空 service key；每次调用用 `artifactv1.WithRequestIdentity` 注入 `BearerToken`、`TenantID`、`ProjectID`、`PropagatedUserID` 及同用户的 `ResourceUserID`。错误集中转换为 `pkg/agent` 错误族（`*serrors.Error` 的 HTTP 状态映射到 `ErrInvalid`/`ErrNotFound`/`ErrConflict`/`ErrUnavailable`）。不自动重试；`Close` 由装配层负责。

### 7.3 `internal/app/apperror`

现在的 `Respond` 只识别 `durable.ErrInvalid/ErrNotFound/ErrConflict`，而 `durable` 的这三个别名实际来自 `pkg/execution`。资产域改用 `pkg/agent` 错误族后：

- `durable.Err*` 继续对应执行域，执行相关 handler 不变；
- 新增 `agent.ErrInvalid/ErrNotFound/ErrConflict/ErrUnavailable` 的映射，其中 `ErrUnavailable` → 503 `DependencyUnavailable`；
- 两套错误族互不依赖，`durable` 不 import `pkg/agent` 的错误（`pkg/durable/types.go` 已从 `pkg/agent` 引入类型别名，仅限值类型）。

### 7.4 `internal/table`

- `agent.go`：`AgentVersion` 删除 `Config`，新增 `ArtifactVersionID`、`ArtifactVersionURI` 两列。
- `agent_query.go`（新增）：`GetAgent`、`ListAgents`、`LockAgent`、`UpdateDraft`、`InsertVersion`、`AdvancePublished`、`GetVersion`。接收传入的 `*gorm.DB`，不开事务、不判权限。

### 7.5 `internal/app/agent`

`service.go` 改为结构体，持有 `*gorm.DB` 与 `client.ArtifactStore`，实现六个方法；`controller.go`、`router.go` 不变；删除对 `*durable.Store` 的断言。领域校验用 `pkg/agent.ValidateAgentConfig`。

### 7.6 `pkg/durable`

- 删除 `agent_service.go`（六个资产方法、`ValidateAgentConfig` 包装、`agentFromRow`/`versionFromRow`）。
- `recordError` 移到 `records.go`，`prepare.go`、`submission.go`、`session_service.go` 继续使用。
- 保留 `types.go` 中的类型别名，worker 依赖。
- `prepare.go` 改为 §6.2 的闭包工厂；`session_service.go` 的 `CreateSession` 内联 agent 查询。
- **删除 `Store.Submit` 便捷包装**（`session_service.go:120`）：它把「读当前已发布版本」写死在 `PrepareChatSubmission` 里，与新的预取流程冲突。生产调用方只有测试；`internal/app/session` 已经通过 `appexec.SubmitChat` 走 `Accept`。

### 7.7 装配

- `internal/config/service.go`：新增 `Artifact` 配置段与校验（endpoint、超时；不含租户与令牌，见 §8）。
- `service/build.go`：构造 artifact 客户端，传入 `New`；`Close` 在 `Build` 返回前由调用方生命周期管理。
- `service.New` 与 `newRoutes` 增加 artifact 依赖；`routes.go` 用 `agent.NewService(store.DB(), artifactClient)`。

## 8. 配置

```toml
[Artifact]
Endpoint = "${env:ARTIFACT_ENDPOINT:http://127.0.0.1:18080}"
Timeout = 5000          # 毫秒
```

校验：`Endpoint` 为绝对 HTTP(S) URL，且无 userinfo、query、fragment；`Timeout` 非负（0 用 SDK 默认）。

`Artifact` 段不增加 `TenantID` 或 `BearerToken`：租户和目标用户来自已核验请求身份；出站服务凭据复用 `Tenant.ServiceAPIKey`，不接受入站 Bearer 替代。部署需配置 artifact 传播白名单。缺少租户或用户时返回 503，不使用默认租户或共享归属。

启动只校验配置格式，不探测远端；远端不可用不影响启动，只影响发布和版本读取。

## 9. 错误映射

| 本地错误 | HTTP |
| --- | --- |
| `agent.ErrInvalid` | 400 `InvalidParameter` |
| `agent.ErrNotFound` | 404 `NotFound` |
| `agent.ErrConflict` | 409 `Conflict` |
| `agent.ErrUnavailable` | 503 `DependencyUnavailable`（新增） |

执行域继续使用 `execution.Err*`（经 `durable.Err*` 别名暴露），两套映射并列存在。

## 10. 测试设计

### 10.1 `internal/app/agent`（假 ArtifactStore）

- revision 比较交换：过期 revision 更新/发布 → 409。
- 同 revision 重复发布：返回同一本地版本，远端只调用一次。
- 同 revision 并发发布：只产生一个本地版本。
- 不同 revision 并发发布：一个成功、一个 409。
- 远端不可用：发布返回 `ErrUnavailable`，本地无新行；同一 revision 重试后只产生一个版本。
- 第 4 步复核失败：返回 409，本地无版本行（模拟远端已建）。
- owner 隔离：跨 owner 读写 → 404。
- payload 超过 256 KiB → 400，且未调用远端。

### 10.2 `internal/client/artifact`（httptest 假服务）

验证 API 自身 service Bearer、已核验目标 tenant/user/可选 project 的传播头、同用户的 resource-user；确认入站 Bearer、Cookie、CSRF、Origin 不被转发，继承的 SDK 身份被覆盖，各请求范围不串用，空 tenant/actor 阻止所有远端操作。另验证两个固定 requestKey、payload 原样、`*serrors.Error` 到错误族的映射、超时。假服务只验证传输与本地行为，不证明真实 Forge 授权通过。

### 10.3 `pkg/durable`

现有测试夹具大量使用 `s.CreateAgent`/`s.PublishAgent`/`s.UpdateAgent`（`store_test.go`、`conversations_test.go`、`application_agent_test.go`、`execution_service_test.go`、`filesystem_test.go`）和 `s.Submit`。改为测试 helper 直接插入 `table.Agent`/`table.AgentVersion`（`artifact_version_id` 用占位值），`Submit` 调用改为 `Accept` + 内联配置的 `PrepareChat`。夹具不依赖 artifact。

### 10.4 `service`

注入假 `ArtifactStore`；`TestAPIFrameworkLifecycle` 等用例启动 httptest 假 artifact 并设置 `ARTIFACT_ENDPOINT`。HTTP 契约断言（路径、状态码、响应体）保持原样。`service/conversations_test.go` 等直接读 `durable.Agent`/`AgentVersion` 的断言继续有效。

### 10.5 Forge 联调

配置 artifact endpoint 和 API 自身的 `Tenant.ServiceAPIKey`，在 artifact 部署的传播白名单加入对应服务名。用户请求选择已授权的 tenant 和可选 project。OrbStack 联调已覆盖用户 API key、发布两个版本、读取正文和 Run 版本固定。其余场景的验证范围见 [验证记录](verification.md#2026-11-17-agent-版本发布与-run-快照)。

### 10.6 worker（单独提交）

- 生产代码不变。
- 三处夹具（`engine_test.go`、`finish_test.go`、`terminal_tool_test.go`）改为直接插入表行；`process_test.go` 为真实 API 进程提供假 artifact 服务与 `ARTIFACT_ENDPOINT`。

## 11. 文档更新

- 新增 `docs/adr/0009-agent-versions-in-artifact.md`：偏离 ADR-0008、正文单一存储、本地指针、固定键与孤儿版本、身份前提。
- `docs/refactoring-design.md` §5、§9、§10：版本正文归 artifact，本地存指针；`pkg/durable` 不再含资产方法。
- `docs/layout.md`：新增 `internal/client/artifact.go`、`internal/table/agent_query.go`；共享持久化边界的查询位置描述更新。
- `docs/api.md`：发布与版本读取依赖 artifact，不可用返回 503；`AgentVersion.config` 的来源。
- `README.md`：目录树、`[Artifact]` 配置、身份 middleware 与 artifact 服务两个前置条件。
- `docs/verification.md`：本轮实测命令与计数。

## 12. 验证

1. `make test`、`make vet`、`make build`、`make doc`（`GOWORK=off`）。
2. `TEST_DATABASE_URL/TEST_REDIS_URL make integration`（race），记录通过/失败/跳过。
3. worker：`go.work` 联调下 `go vet`、`go build`、`go test -race`，含强杀接管。
4. 静态检查：
   - `pkg/durable` 不再有 `CreateAgent`/`PublishAgent`/`UpdateAgent`/`ValidateAgentConfig`/`Submit`；
   - `go list -deps ./pkg/durable` 不含 artifact SDK；
   - `artifactv1` 只出现在 `internal/client`；
   - `go list -deps ./pkg/execution` 不含 GORM、HTTP 或 artifact SDK。
5. 有 artifact 环境时跑真实联调。

## 13. 风险与限制

| 风险 | 说明 | 处理 |
| --- | --- | --- |
| 不能代其他 owner 发布 | 内置 Forge resolver 只允许本人归属 | 本轮保持「每人只看自己的 Agent」；系统级共享 AssetScope 后续单独设计 |
| 发布、版本读取和新任务提交依赖 artifact | artifact 不可用时 503 | 草稿操作、已接受 Run 的执行与幂等重放不依赖远端正文读取 |
| 孤儿远端版本 | 第 4 步失败时留下未关联版本 | 记录为可接受，不增加对账任务 |
| 版本读取变慢 | 每次一次远端调用 | 读取频率低；必要时后续加只读缓存 |
| worker 独立构建 | 仍等 API 发版 | 联合验证以 `go.work` 为准 |

## 14. 实施顺序

1. 固定依赖 server-go `v0.13.0`、forge-tenant-api `v0.13.0`、artifact-go `v0.5.1`，并验证独立构建。
2. 配置：`internal/config` 的 `[Artifact]` 段与校验。
3. `pkg/agent` 错误族；`internal/client/artifact.go` 与单测；`apperror` 新增映射。
4. 表模型与 `internal/table/agent_query.go`。
5. `internal/app/agent` 服务与单测。
6. `pkg/durable`：删除资产层与 `Store.Submit`，改写 `prepare.go`，迁移测试夹具。
7. `internal/app/execution` 预取流程；装配与 service 测试。
8. worker 测试夹具（单独提交）。
9. 文档与验证记录。
