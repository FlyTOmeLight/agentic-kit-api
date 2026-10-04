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
