# 执行链路验证记录

验证日期：2026-10-02。使用本机 Go 1.27.1、隔离的 PostgreSQL 17.6 与 Redis 7.2.10 容器。原 agentic-kit 和现有业务数据库未修改。

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

未接入生产 Prism、AppForge、auth-center 和沙箱服务；未使用真实模型凭据；未做容量压测、数据库高可用切换、完整旧 SDK 兼容测试或依赖漏洞数据库扫描。原业务功能迁移范围见 migration.md。

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
