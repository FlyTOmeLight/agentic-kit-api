# Agentic Kit API

Agent 的配置、发布、会话、消息与持久化执行 API。与相邻的 `agentic-kit-worker` 仓库一起运行，解决原 `agentic-kit` 中后台 goroutine 随服务重启消失、执行进度不能接管、数据库与完成事件双写不一致的问题。

当前版本为 v0.5.1，支持通过接口创建和发布 Agent、连续对话、查询历史及故障接管。使用独立 PostgreSQL 表，不会自动迁移原 agentic-kit 的 `agent_run` 数据。原产品的完整功能迁移状态见 [迁移范围](docs/migration.md)，不能直接替换旧服务流量。

## 项目结构

采用 `cmd/`、`internal/`、`sdk/go/agenticv1/`、`pkg/durable/`、`conf/server/` 分区；目录职责和跨仓库依赖见 [项目布局](docs/layout.md)。

## HTTP 结构

API 使用 [server-go](https://github.com/FlyTOmeLight/server-go) 管理配置、资源、HTTP 生命周期与通用 HTTP 行为。`cmd/server` 处理启动与信号，`internal/service` 装配资源、业务路由和后台任务；`internal/app` 按领域维护 Controller 与 Service 接口。JSON 绑定、请求大小限制、Bearer 鉴权、身份读取、分页参数、请求日志、Recovery 和错误响应统一使用框架能力。`internal/app/apperror` 只把 durable 的领域错误映射为 HTTP 错误。公开请求、响应与客户端维护在 `sdk/go/agenticv1/`。

持久化模型与迁移注册位于 `internal/table`；业务事务在 `pkg/durable` 中按 agent_service.go、session_service.go、run_service.go 分开组织，API 与 worker 共用其公开接口。controller 只处理 HTTP 语义，并依赖本业务模块的 Service 接口；总装配层注入 durable.Store。

使用 server-go binder 的严格 JSON 绑定和 Gin 校验器，拒绝未知字段和多段 JSON，不修改 Gin 全局 decoder 标志。SSE 传输使用 server-go stream；Gin Context 不传入后台 worker，数据库操作继续传递 Request.Context。生产运行可设置 `GIN_MODE=release`。

## 职责

- 管理 Agent 草稿和不可变发布版本；通过 revision 拒绝覆盖其他编辑者的修改。
- 管理独立会话和消息记录；提交消息时固定配置版本和历史，同一会话只允许一个活动任务。
- 重试相同请求时返回原任务，即使期间发布了新版本。
- 查询、取消、回答澄清、确认不确定的工具结果。
- 从数据库回放 SSE 事件；Redis Streams 加速通知，Redis 不可用时仍能回放已提交事件。
- 保存任务、工具执行记录和事件 Outbox。worker 引用本仓库的 `pkg/durable` 包，避免两份状态机。

worker 不经过 API 领取任务，而是直接访问同一个 PostgreSQL。两个程序可以分别扩容。

## 获取代码与私有模块

仓库位于 `github.com/FlyTOmeLight/agentic-kit-api` 和 `github.com/FlyTOmeLight/agentic-kit-worker`。首次获取代码前配置 GitHub 认证和 Go 私有模块：

```bash
gh auth login
gh auth setup-git
# 当前终端生效；已有 GOPRIVATE 时追加此模式。
export GOPRIVATE="${GOPRIVATE:+$GOPRIVATE,}github.com/FlyTOmeLight/*"
gh repo clone FlyTOmeLight/agentic-kit-api
gh repo clone FlyTOmeLight/agentic-kit-worker
```

worker 默认使用 go.mod 固定的 API 版本，不依赖相邻源码。需要同时修改两个仓库时，在共同父目录执行 `go work init ./agentic-kit-api ./agentic-kit-worker`；该工作区文件只用于本地联合开发，不提交到仓库。

## 本地启动

两个仓库可以独立克隆和构建。需要 Go 1.25.1 或更新版本、PostgreSQL 17、Redis 7.2。当前在本机 Go 1.27.1 完成验证；Eino 的 Sonic 依赖会打印版本支持提示并使用其自带 JSON 回退路径。

```bash
cd agentic-kit-api
docker compose -f deploy/compose.yaml up -d
cp conf/server/config.toml conf/server/config.local.toml
# 修改 config.local.toml 中的服务配置。
export API_TOKENS_JSON='{"replace-with-your-local-token-32chars":"local-user"}'
go run ./cmd/cli migrate --config conf/server/config.local.toml
go run ./cmd/server -config conf/server/config.local.toml
```

另开终端启动 worker，在 TOML 中配置同一个数据库，并设置模型和工具所需的凭据环境变量：

```bash
cd agentic-kit-worker
cp configs/config.example.toml configs/config.local.toml
# 本地 Ollama 不需要密钥。其他模型按 Agent 配置中的 apiKeyEnv 设置。
# export MODEL_API_KEY='你的模型凭据'
go run ./cmd/worker -config configs/config.local.toml
```

API 默认监听 `127.0.0.1:8080`。`LISTEN_ADDR` 可修改监听地址。本地示例 token 仅用于演示，部署时自行配置。

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

## Go SDK

其他 Go 应用导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`，即可复用请求、响应和客户端。SDK 与 API 同模块发布，使用 server-go SDK（Resty v3）处理 HTTP 调用，无需导入 Gin、GORM 或 durable。Client 按 Agents、Sessions、Runs、Executions、Evaluations 暴露业务接口，可单独注入调用方。

```go
client, err := sdk.NewClient(baseURL, token, nil)
if err != nil {
    return err
}
defer client.Close()
ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()
run, created, err := client.Sessions.SendMessage(ctx, sessionID, "question-001",
    sdk.SendMessageRequest{Query: "请介绍一下你自己。"})
```

`created=false` 表示命中服务端幂等记录。重试同一条消息复用请求编号；下一条消息使用新编号。客户端不自动重试。完整示例、错误处理和事件流说明见 [SDK 用法](sdk/go/agenticv1/README.md)。

## 服务配置

API 和 migrate 通过 `internal/config` 加载 server-go 配置快照。Viper 负责 TOML 解析、严格字段检查和现有环境变量覆盖；server-go 的 `conf` 负责占位符解析与框架配置校验，API 的 `conf.Manager` 持有独立快照。通过 `-config` 指定路径，也可设置 `CONFIG_FILE` 或 `SERVER_CONFIG_PATH`，路径优先级依此排列。未指定路径时读取 `conf/server/config.toml`，环境变量覆盖文件中的对应字段；指定文件不存在、格式错误或包含未知字段时启动失败。配置只在启动时加载，修改后重启服务。

| TOML 字段 | 环境变量 | 默认值 |
| --- | --- | --- |
| AppName | 无 | agentic-kit-api |
| AppCode | 无 | 100 |
| RunMode | GIN_MODE | release |
| HTTPServer.Listen | LISTEN_ADDR | 127.0.0.1:8080 |
| HTTPServer.ReadTimeout | 无 | 10000 毫秒 |
| HTTPServer.WriteTimeout | 无 | -1（禁用，供 SSE 使用） |
| HTTPServer.IdleTimeout | 无 | 60000 毫秒 |
| database.url | DATABASE_URL | 必填 |
| redis.url | REDIS_URL | API 必填 |
| auth.config_owners | CONFIG_OWNERS（逗号分隔） | 空列表 |
| auth.tokens_json | API_TOKENS_JSON | API 必填 |

配置中的字符串支持 server-go 的 `${env:NAME[:default]}`、`${file:/path}` 和 `${secret:密文}` 占位符；密文需要 `SERVER_SECRET_KEY`。`HTTPServer.APIBasePath` 必须为空，以保留业务路径；`WriteTimeout` 必须为负数，以免截断 SSE。服务凭据使用框架的 Bearer 鉴权；Forge 会话和项目权限由业务层校验。错误统一采用 server-go 的平铺格式，详见 [接口契约](docs/api.md)。

表中的默认值指代码默认值；默认配置文件另提供本地 PostgreSQL、Redis 地址和 `local-user` 配置编辑者。

优先级为环境变量 > TOML > 默认值；已设置的空环境变量也会覆盖文件。Agent 定义由数据库管理，API 启动不读取 agents 文件。`examples/agents/agents.example.json` 是创建接口的请求样例，可以直接导入。工具参数使用 JSON Schema；模型和工具凭据由 worker 环境变量提供。

`auth.config_owners` 指定可编辑模型地址、工具地址和凭据引用的受信任用户。这些配置会影响 worker 的外部访问，普通 token 不具备配置写权限。Agent、会话、消息和任务均按 owner 隔离，本轮未实现共享 Agent 或 project 级授权。

`server.NewBootstrap` 在启动前打开 PostgreSQL 与 Redis 资源，`host.HTTPHost` 先安装路由，再调用服务的 `Start` 启动 Outbox、评测评分和可选 Coding 环境协调任务。收到 SIGINT／SIGTERM 后，框架停止接收请求并排空 HTTP，随后取消并等待后台任务，最后由 Bootstrap 关闭连接。HTTP 排空有 5 秒期限，超时后取消请求（含 SSE）并等待 handler 退出；业务资源不会在 handler 尚未退出时关闭。监听失败或路由装配失败时，同样清理已初始化资源。API 启动不执行数据库迁移。

框架提供无需鉴权的 `GET /ping`、`GET /healthz` 和 `GET /version`；`/healthz` 仅在后台服务启动完成后返回 200，未就绪时返回 503。它表示进程已就绪，不持续探测数据库或 Redis。

## 测试

```bash
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export TEST_REDIS_URL='redis://127.0.0.1:56379/0'
make integration
```

集成测试为每个测试创建随机 PostgreSQL schema 并在结束时清理，不复用业务表；Redis 测试只操作本次生成的 run key。测试账户需要创建 schema 的权限。未设置 TEST_DATABASE_URL 时，数据库用例会明确跳过，不能把这个结果当作集成验证通过。

## 数据库访问

连接、事务、查询和写入集中在 `pkg/durable` 的 GORM Store；API 与 worker 使用同一实现。读写使用 GORM Model、查询构造器和 Updates；领取与执行权校验使用 Locking 子句，更新结果使用 Returning 子句，Outbox 领取使用子查询。数据库时钟与原子递增使用 gorm.Expr；事务级 advisory lock 通过 GORM Exec 调用 PostgreSQL 函数。

`internal/table/` 中的领域模型是表结构的唯一来源，迁移模型注册集中在 `internal/table/migrate.go`，包含复合主键、CHECK、外键、部分唯一索引和数据库默认值。`cmd/cli` 在持有迁移锁的 GORM 事务中调用 AutoMigrate；API 和 worker 启动时不自动建表。AutoMigrate 会补齐缺失的表、列、约束和索引，也可能调整列类型或可空性；不会自动删除废弃列。变更模型后应先验证已有数据库上的迁移，再执行部署迁移命令。连接池每进程最多 20 个连接、5 个空闲连接；退出时关闭。SQL 日志关闭，避免输出模型上下文与工具结果。

## 依赖选择

API 固定引用 [server-go v0.3.1](https://github.com/FlyTOmeLight/server-go/releases/tag/v0.3.1)，包含本项目使用的 Bearer 凭据、严格 JSON 绑定、分页解析、通用 HTTP 中间件、SSE 与 SDK 错误解码。框架通过公开 Go 模块下载，不需要相邻源码或本地 replace；go.work 只用于 API 与 worker 联合开发。框架日志复用 Zap 与 Lumberjack，SDK 复用 Resty v3。

SDK 复用 server-go 的 `sdk.Client`（Resty v3），不再直接依赖 Resty v2。普通 JSON 调用默认 20 秒超时，可通过传入的 HTTP Client 调整；SSE 使用调用方 context 控制时限。客户端禁用自动重试与重定向，每次请求独立传递身份和 context。调用方在所有请求与流结束后调用 Close；传入的标准 HTTP Transport 会被克隆，SDK 管理自己的连接池。

Gin v1.11.0 与 gin-contrib/sse v1.1.0 沿用原仓库版本，分别用于 HTTP 框架与 SSE 编码，均为 MIT 许可；固定传递依赖在 go.sum 中。数据库统一通过 GORM v1.31.2 和官方 PostgreSQL 驱动 v1.6.3 管理；驱动底层使用 pgx v5.10.0，配置层仅使用 pgx 解析连接串。GORM 与驱动均为 MIT 许可，无需 CGO 或代码生成，新增依赖主要为 jinzhu/inflection、jinzhu/now。Redis 沿用 go-redis v9.16.0（BSD 2-Clause）。固定版本及传递依赖记录在 go.mod/go.sum；本次没有执行漏洞数据库扫描。

[设计与故障处理](docs/design.md) · [迁移范围](docs/migration.md) · [验证记录](docs/verification.md)

## v0.5.0 升级：server-go 布局、配置与错误格式

服务入口为 `cmd/server`；管理命令为 `cmd/cli migrate` 和 `cmd/cli eval`。SDK 导入改为 `github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1`，调用方需在请求结束后关闭客户端；旧命令和 SDK 目录已移除。

已有 TOML 将 `[http] listen_addr` 改为 `[HTTPServer] Listen`，参考 `conf/server/config.toml`。旧字段会被严格校验拒绝。未指定文件的环境变量部署仍可启动，`LISTEN_ADDR`、`DATABASE_URL`、`REDIS_URL`、`CONFIG_OWNERS` 和 `API_TOKENS_JSON` 沿用原名称。配置文件迁到框架默认的 `conf/server/config.toml`；本地修改可复制为 `conf/server/config.local.toml` 后显式指定。Docker 镜像包含默认配置，部署 Secret 中的环境变量继续覆盖对应字段。

业务 HTTP 路径与成功响应保持原契约；错误响应改为 server-go 的平铺格式，旧的 error 外层对象已移除。SDK 的 APIError 改为框架 errors.Error：Status 是 HTTP 状态，StatusCode 是框架错误编号。新增评测表需在部署前执行 `go run ./cmd/cli migrate --config conf/server/config.toml`；迁移检查见 docs/verification.md。worker 的执行权与恢复契约保持原规则，但升级时需同步其引用的 API 模块版本。

## 从 v0.1 升级

v0.2 改变任务提交入口，移除 `POST /v1/runs` 与静态 agents 文件加载。服务 TOML 中删除 `[agents]`，在 `[auth]` 中配置 `config_owners`；Agent 示例 JSON 也改为创建接口的请求格式。

v0.1 的任务只有 session 标签，无法完整还原 Agent 发布版本和消息记录。本轮不自动转换这些数据。空数据库可以直接执行 migrate；含 v0.1 任务的数据库会被迁移命令拒绝，原数据保持不变。已有任务应由原版本处理完成，v0.2 使用新数据库或独立 PostgreSQL schema。原项目数据迁移和旧客户端适配仍需单独实施。

## Kubernetes

`charts/agentic-kit-api` 部署 API、独立 PostgreSQL 与 Redis。凭据引用 existingSecret / database.existingSecret；镜像由 Dockerfile 构建。部署时使用 `--set existingSecret=agentic-runtime-env,database.existingSecret=agentic-runtime-db`。就绪探针使用 server-go 的 `/healthz`。Coding 使用受信任应用执行接口，业务任务仍由 forge-coding-api 保存。

## 发布应用 Agent

Writing 与 Coding 使用同一个 Agent 发布接口和 Worker。`scripts/publish-agent.py` 接受标准创建请求 JSON；先设置 `AGENTIC_URL`、`AGENTIC_TOKEN`，凭据需具有 config_owners 权限。

```sh
python3 scripts/publish-agent.py examples/agents/writing-agent.example.json
# 后续修改同一个 Agent，保留其身份和发布历史
python3 scripts/publish-agent.py examples/agents/writing-agent.example.json --agent-id <agentID>
```

修改示例中的模型地址后再发布。Coding 的定义位于 `forge-coding-api/agents/coding.json`，同样由这个命令发布；先把工具 URL 配置为获授权项目的工具环境。应用提交返回的 agentID/agentVersion，不能在执行请求中覆盖 Agent 定义。绑定旧版本的项目不会自动升级，项目管理员显式选择新版本并复检。

## M0 评测 CLI

`cmd/cli eval` 通过现有受信任 ExecutionAPI 运行固定 Agent 版本，保存本地 checkpoint 并对完整输出判分。当前支持 single_turn 场景、可选 AIO 绑定、contains_all / exact / json_valid 判分器，以及 pass_rate 和覆盖率门槛。

修改 `examples/evaluation/evaluation-writing.example.json` 中的 Agent ID 与发布版本，设置 `AGENTIC_URL`、`AGENTIC_TOKEN` 后执行：

```sh
go run ./cmd/cli eval --suite examples/evaluation/evaluation-writing.example.json --state .local/evaluation/state.json
```

同一 state 文件仅由一个进程写入。重复运行复用已记录的试验和 Run；套件改变需要新实验文件。退出码 0 表示门槛通过，2 表示未达标，1 表示配置、执行或证据采集错误。CLI 不自动回答澄清或确认未知工具结果。

这是 M0 的受信任操作入口。用户权限、生产实验接口与项目启用流程尚未接入。SDK 配置与结果类型见 `sdk/go/agenticv1/agentic_api_evaluation.go`，判分实现见 `internal/app/evaluation`；Langfuse 记录导出与实际验收位于配套设计项目的 `pilot/evaluation/` 和 `docs/agent-evaluation-m0-verification.md`。

## M1 持久实验 API

`POST /v1/evaluations` 将实验及全部 Trial / Run 在同一事务提交。`Client.Evaluations` 可提交、查询、取消和比较固定版本实验。API 重启后从数据库继续完成评分，原 Run 不重新创建；Langfuse 不参与实验控制。接口契约见 [评测 API](docs/api.md#持久评测实验)。部署前执行迁移命令。

当前提供项目会话权限网关、配置编辑、Writing 输出判分及 Coding 独立环境判分。业务项目切换候选版本仍走现有执行配置流程。M0 CLI 继续用于本地冻结套件验证，其文件 checkpoint 与持久 API 实验各自有明确归属。


## Forge 项目入口

`internal/app/platform` 在同一 API 进程提供项目 Agent 和评测入口，使用 Forge 会话、CSRF 与实时项目授权。配置 `PLATFORM_TENANT_URL`、`PLATFORM_TENANT_TOKEN`、`PLATFORM_TEMPLATE_OWNER`、`PLATFORM_TEMPLATES_JSON` 后启用。模型连接与模板版本由部署管理员批准；浏览器只编辑项目副本的 Prompt、名称和轮数。

项目候选和实验复用现有持久执行链路，以服务端派生的项目 owner 隔离。项目候选不自动切换业务调用。支持 Writing 输出评测和 Coding 独立仓库评测。Coding 需配置 `PLATFORM_CODING_URL`、`PLATFORM_CODING_TOKEN` 并批准 Coding 模板；服务凭据只保存在部署环境。评测候选不会自动替换业务执行版本。完整接口与 SDK 见 [HTTP 契约](docs/api.md#forge-项目-agent-与评测入口) 和 sdk/go/agenticv1/agentic_client_project.go。
