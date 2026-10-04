# Agentic Kit API

Agent 的配置、发布、会话、消息与持久化执行 API。与相邻的 `agentic-kit-worker` 仓库一起运行，解决原 `agentic-kit` 中后台 goroutine 随服务重启消失、执行进度不能接管、数据库与完成事件双写不一致的问题。

当前版本为 v0.3，支持通过接口创建和发布 Agent、连续对话、查询历史及故障接管。使用独立 PostgreSQL 表，不会自动迁移原 agentic-kit 的 `agent_run` 数据。原产品的完整功能迁移状态见 [迁移范围](docs/migration.md)，不能直接替换旧服务流量。

## 项目结构

采用 `cmd/`、`internal/`、`sdk/`、`pkg/durable/`、`configs/` 分区；目录职责和跨仓库依赖见 [项目布局](docs/layout.md)。

## HTTP 结构

API 使用 Gin v1.11.0。`internal/httpapi` 装配总路由和鉴权、日志、Recovery 中间件；`internal/agent`、`internal/session`、`internal/run` 分别维护业务路由和 controller。公开请求与响应类型维护在根目录 `sdk/`。发送消息属于 Session，SSE 属于 Run。`internal/httpx` 提供共享的 JSON 绑定、错误响应、身份读取和分页处理。

业务事务在 `pkg/durable` 中按 agent_service.go、session_service.go、run_service.go 分开组织，API 与 worker 共用。controller 只处理 HTTP 语义，并依赖本业务模块的 Service 接口；总装配层注入 durable.Store。

使用 Gin binding 的校验器和局部严格 JSON binding，拒绝未知字段和多段 JSON，不修改 Gin 全局 decoder 标志。SSE 使用 gin-contrib/sse；Gin Context 不传入后台 worker，数据库操作继续传递 Request.Context。生产运行可设置 `GIN_MODE=release`。

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
cp configs/config.example.toml configs/config.local.toml
# 修改 config.local.toml 中的服务配置。
export API_TOKENS_JSON='{"replace-with-your-local-token-32chars":"local-user"}'
go run ./cmd/migrate -config configs/config.local.toml
go run ./cmd/api -config configs/config.local.toml
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
  --data-binary @configs/agents.example.json | jq -r .id)
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

其他 Go 应用导入 `github.com/FlyTOmeLight/agentic-kit-api/sdk`，即可复用请求、响应和客户端。SDK 与 API 同模块发布，使用 Resty 处理 HTTP 调用，无需导入 Gin、GORM 或 durable。Client 按 Agents、Sessions、Runs 暴露三个业务接口，可单独注入调用方。

```go
client, err := sdk.NewClient(baseURL, token, nil)
if err != nil {
    return err
}
ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()
run, created, err := client.Sessions.SendMessage(ctx, sessionID, "question-001",
    sdk.SendMessageRequest{Query: "请介绍一下你自己。"})
```

`created=false` 表示命中服务端幂等记录。重试同一条消息复用请求编号；下一条消息使用新编号。客户端不自动重试。完整示例、错误处理和事件流说明见 [SDK 用法](sdk/README.md)。

## 服务配置

API 和 migrate 使用独立 Viper 实例读取 TOML；通过 `-config` 指定路径，也可设置 `CONFIG_FILE`。未指定文件时使用环境变量和默认值；指定文件不存在、格式错误或包含未知字段时启动失败。配置只在启动时加载，修改后重启服务。

| TOML 字段 | 环境变量 | 默认值 |
| --- | --- | --- |
| http.listen_addr | LISTEN_ADDR | 127.0.0.1:8080 |
| database.url | DATABASE_URL | 必填 |
| redis.url | REDIS_URL | API 必填 |
| auth.config_owners | CONFIG_OWNERS（逗号分隔） | 空列表 |
| auth.tokens_json | API_TOKENS_JSON | API 必填 |

优先级为环境变量 > TOML > 默认值；已设置的空环境变量也会覆盖文件。Agent 定义由数据库管理，API 启动不读取 agents 文件。`configs/agents.example.json` 是创建接口的请求样例，可以直接导入。工具参数使用 JSON Schema；模型和工具凭据由 worker 环境变量提供。

`auth.config_owners` 指定可编辑模型地址、工具地址和凭据引用的受信任用户。这些配置会影响 worker 的外部访问，普通 token 不具备配置写权限。Agent、会话、消息和任务均按 owner 隔离，本轮未实现共享 Agent 或 project 级授权。

Viper v1.21.0 用于复用文件解析、配置覆盖和结构体解码，采用 MIT 许可，无需 CGO 或代码生成。依赖包含 mapstructure、fsnotify、afero、cast、pflag 等，固定版本见 go.mod/go.sum；本次未运行漏洞数据库扫描。

## 测试

```bash
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export TEST_REDIS_URL='redis://127.0.0.1:56379/0'
make integration
```

集成测试为每个测试创建随机 PostgreSQL schema 并在结束时清理，不复用业务表；Redis 测试只操作本次生成的 run key。测试账户需要创建 schema 的权限。未设置 TEST_DATABASE_URL 时，数据库用例会明确跳过，不能把这个结果当作集成验证通过。

## 数据库访问

连接、事务、查询和写入集中在 `pkg/durable` 的 GORM Store；API 与 worker 使用同一实现。读写使用 GORM Model、查询构造器和 Updates；领取与执行权校验使用 Locking 子句，更新结果使用 Returning 子句，Outbox 领取使用子查询。数据库时钟与原子递增使用 gorm.Expr；事务级 advisory lock 通过 GORM Exec 调用 PostgreSQL 函数。

`pkg/durable/models.go` 是表结构的唯一来源，包含复合主键、CHECK、外键、部分唯一索引和数据库默认值。`cmd/migrate` 在持有迁移锁的 GORM 事务中调用 AutoMigrate；API 和 worker 启动时不自动建表。AutoMigrate 会补齐缺失的表、列、约束和索引，也可能调整列类型或可空性；不会自动删除废弃列。变更模型后应先验证已有数据库上的迁移，再执行部署迁移命令。连接池每进程最多 20 个连接、5 个空闲连接；退出时关闭。SQL 日志关闭，避免输出模型上下文与工具结果。

## 依赖选择

SDK 使用 [Resty v2.17.2](https://github.com/go-resty/resty/releases/tag/v2.17.2) 复用请求构造、鉴权和 JSON 编解码。它采用 MIT 许可、要求 Go 1.23，符合本项目工具链；生产传递依赖为已有的 golang.org/x/net/publicsuffix，无需 CGO 或代码生成。已检查该版本源码与官方安全公告页面；客户端显式禁用 Resty 重试和 HTTP 重定向，SSE 使用不缓冲响应的模式。

Gin v1.11.0 与 gin-contrib/sse v1.1.0 沿用原仓库版本，分别用于 HTTP 框架与 SSE 编码，均为 MIT 许可；固定传递依赖在 go.sum 中。数据库统一通过 GORM v1.31.2 和官方 PostgreSQL 驱动 v1.6.3 管理；驱动底层使用 pgx v5.10.0，配置层仅使用 pgx 解析连接串。GORM 与驱动均为 MIT 许可，无需 CGO 或代码生成，新增依赖主要为 jinzhu/inflection、jinzhu/now。Redis 沿用 go-redis v9.16.0（BSD 2-Clause）。固定版本及传递依赖记录在 go.mod/go.sum；本次没有执行漏洞数据库扫描。

[设计与故障处理](docs/design.md) · [迁移范围](docs/migration.md) · [验证记录](docs/verification.md)

## 从 v0.1 升级

v0.2 改变任务提交入口，移除 `POST /v1/runs` 与静态 agents 文件加载。服务 TOML 中删除 `[agents]`，在 `[auth]` 中配置 `config_owners`；Agent 示例 JSON 也改为创建接口的请求格式。

v0.1 的任务只有 session 标签，无法完整还原 Agent 发布版本和消息记录。本轮不自动转换这些数据。空数据库可以直接执行 migrate；含 v0.1 任务的数据库会被迁移命令拒绝，原数据保持不变。已有任务应由原版本处理完成，v0.2 使用新数据库或独立 PostgreSQL schema。原项目数据迁移和旧客户端适配仍需单独实施。
