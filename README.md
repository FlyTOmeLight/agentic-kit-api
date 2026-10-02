# Agentic Kit API

Agent 对话执行重构的 API 与持久化部分。与相邻的 `agentic-kit-worker` 仓库一起运行，解决原 `agentic-kit` 中后台 goroutine 随服务重启消失、执行进度不能接管、数据库与完成事件双写不一致的问题。

这是一套可运行的新执行主链路，使用独立 PostgreSQL 表，不会自动修改或迁移旧 `agent_run` 数据。原产品的完整功能迁移状态见 [迁移范围](docs/migration.md)，不能直接替换旧服务流量。

## 项目结构

采用 `cmd/`、`internal/`、`pkg/durable/`、`configs/` 分区；目录职责和跨仓库依赖见 [项目布局](docs/layout.md)。

## HTTP 结构

API 使用 Gin v1.11.0，与原 agentic-kit 保持框架版本一致。路由装配在 `internal/httpapi/router.go`；鉴权、请求日志与 Recovery 在 `internal/httpapi/middleware.go`；DTO 和 binding 校验在 `internal/httpapi/request.go`；任务 handler 在 `internal/httpapi/runs.go`；SSE 在 `internal/httpapi/events.go`；JSON 错误契约在 `internal/httpapi/response.go`。handler 只处理 HTTP 语义，数据库事务仍由 durable 包负责。

使用 Gin binding 的校验器和局部严格 JSON binding，拒绝未知字段和多段 JSON，不修改 Gin 全局 decoder 标志。SSE 使用 gin-contrib/sse；Gin Context 不传入后台 worker，数据库操作继续传递 Request.Context。生产运行可设置 `GIN_MODE=release`。

## 职责

- 接收任务，按用户和请求编号幂等创建，冻结执行配置；同一会话只允许一个活动任务。
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

worker 默认使用 API 的 `v0.1.0` 版本，不依赖相邻源码。需要同时修改两个仓库时，在共同父目录执行 `go work init ./agentic-kit-api ./agentic-kit-worker`；该工作区文件只用于本地联合开发，不提交到仓库。

## 本地启动

两个仓库可以独立克隆和构建。需要 Go 1.25.1 或更新版本、PostgreSQL 17、Redis 7.2。当前在本机 Go 1.27.1 完成验证；Eino 的 Sonic 依赖会打印版本支持提示并使用其自带 JSON 回退路径。

```bash
cd agentic-kit-api
docker compose -f deploy/compose.yaml up -d
cp configs/config.example.toml configs/config.local.toml
# 修改 config.local.toml 中的服务配置。
export API_TOKENS_JSON='{"replace-with-your-local-token-32chars":"local-user"}'
cp configs/agents.example.json configs/agents.local.json
# 修改 agents.local.json 中的模型地址、模型名和可用工具。
go run ./cmd/migrate -config configs/config.local.toml
go run ./cmd/api -config configs/config.local.toml
```

另开终端启动 worker，在 TOML 中配置同一个数据库，并设置模型和工具所需的凭据环境变量：

```bash
cd agentic-kit-worker
cp configs/config.example.toml configs/config.local.toml
export MODEL_API_KEY='你的模型凭据'
go run ./cmd/worker -config configs/config.local.toml
```

API 默认监听 `127.0.0.1:8080`。`LISTEN_ADDR` 可修改监听地址。本地示例 token 仅用于演示，部署时自行配置。

```bash
curl -X POST http://127.0.0.1:8080/v1/runs \
  -H 'Authorization: Bearer replace-with-your-local-token-32chars' \
  -H 'Idempotency-Key: request-001' \
  -H 'Content-Type: application/json' \
  -d '{"agentID":"assistant","sessionID":"session-001","query":"请帮我分析这个问题"}'
```

返回任务 `id` 后，用 `GET /v1/runs/{id}` 查询；用 `GET /v1/runs/{id}/events` 接收 SSE。详细请求见 [接口契约](docs/api.md)。

## 服务配置

API 和 migrate 使用独立 Viper 实例读取 TOML；通过 `-config` 指定路径，也可设置 `CONFIG_FILE`。未指定文件时使用环境变量和默认值；指定文件不存在、格式错误或包含未知字段时启动失败。配置只在启动时加载，修改后重启服务。

| TOML 字段 | 环境变量 | 默认值 |
| --- | --- | --- |
| http.listen_addr | LISTEN_ADDR | 127.0.0.1:8080 |
| database.url | DATABASE_URL | 必填 |
| redis.url | REDIS_URL | API 必填 |
| agents.file | AGENTS_FILE | API 必填 |
| auth.tokens_json | API_TOKENS_JSON | API 必填 |

优先级为环境变量 > TOML > 默认值；已设置的空环境变量也会覆盖文件。指定 TOML 时，相对 agents.file（含环境变量覆盖值）以 TOML 所在目录为基准。Agent 定义仍使用 JSON，工具参数保持 JSON Schema 格式；模型和工具凭据由 worker 环境变量提供。

Viper v1.21.0 用于复用文件解析、配置覆盖和结构体解码，采用 MIT 许可，无需 CGO 或代码生成。依赖包含 mapstructure、fsnotify、afero、cast、pflag 等，固定版本见 go.mod/go.sum；本次未运行漏洞数据库扫描。

## 测试

```bash
export TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable'
export TEST_REDIS_URL='redis://127.0.0.1:56379/0'
make integration
```

集成测试为每个测试创建随机 PostgreSQL schema 并在结束时清理，不复用业务表；Redis 测试只操作本次生成的 run key。测试账户需要创建 schema 的权限。未设置 TEST_DATABASE_URL 时，数据库用例会明确跳过，不能把这个结果当作集成验证通过。

## 依赖选择

Gin v1.11.0 与 gin-contrib/sse v1.1.0 沿用原仓库版本，分别用于 HTTP 框架与 SSE 编码，均为 MIT 许可；固定传递依赖在 go.sum 中。沿用原仓库的 pgx v5.8.0、go-redis v9.16.0；直接使用 PostgreSQL 事务和行锁，不增加消息队列。pgx 为 MIT、go-redis 为 BSD 2-Clause，均不要求 CGO。固定版本及传递依赖记录在 go.mod/go.sum；本次没有执行漏洞数据库扫描。

[设计与故障处理](docs/design.md) · [迁移范围](docs/migration.md) · [验证记录](docs/verification.md)
