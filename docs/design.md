# Agent 长任务恢复设计

## 解决的问题

原代码的 `startBackgroundRun` 在服务进程内启动 goroutine，`context.WithoutCancel` 只移除了 HTTP 请求取消传播。旧 Runner 的 DB checkpoint 在工具中断、用户澄清时保存，不能据此承诺任意重启后的恢复。

当前实现将任务、模型决策、工具结果和用户交互状态持久化，由独立 worker 领取。Agent 执行使用 Eino ADK `ChatModelAgent + Runner`；平台通过 middleware 与 `CheckPointStore` 接入业务存储，不再自行维护 ReAct 循环。恢复单位是 Eino checkpoint 与已提交的工具结果。

## 数据与职责

```text
客户端 → API → PostgreSQL durable_run / durable_step / durable_event
                    ↑                           ↓
              worker 领取执行             Outbox relay → Redis Streams
                                                ↓
客户端 ← API SSE ← 数据库事件回放及 Redis 唤醒
```

- agent / agent_version：可编辑草稿与不可变发布版本。revision 检测并发编辑，发布在 Agent 行锁下递增版本号。
- agent_session / agent_message：独立会话和消息记录；消息 seq 在会话行锁下递增。
- durable_run：不可变执行配置、输入、Eino checkpoint、当前状态、期限、执行者和 epoch。
- durable_step：工具执行前的意图、输入摘要、重试策略、执行结果。操作编号是 runID:stepID，重试不改变。
- durable_event：与状态同事务写入的事件，也是 Outbox。Redis 只是通知与缓存，数据库是事件权威来源。

API 仓库拥有共享持久化包，worker 只调用其中的状态变更方法。持久化访问统一由 GORM 管理；领取使用 Locking 子句，事件计数用 Updates 与 Returning 子句递增并取回；事件写入仍在同一事务。表结构、约束与索引由 internal/table 中的领域模型定义，Store 的结构初始化方法在事务中执行 AutoMigrate，不再提供迁移 CLI。数据库时钟和数值递增通过 gorm.Expr 表达，事务级 advisory lock 通过 GORM Exec 调用。数据库时间决定执行权期限，避免多个 worker 的本地时钟差异影响领取判断。

## 会话提交与记录

提交消息先按 owner/request key 获取事务锁，再锁定会话。幂等摘要仅包含 session/query；先查已接受请求，再解析当前发布版本，保证重试不会受后续发布影响。会话已有活动任务时拒绝新提交；否则读取已完成对话，冻结配置和历史，再一起写入 Run、User 消息与 RunQueued 事件。

worker 在任务行锁与 epoch 校验之后保存执行结果。模型决策、工具结果、澄清回答和最终回复分别与其状态、事件同事务写入消息表。恢复命中已保存工具结果时不追加第二条 Tool 消息。客户端查看聊天记录不需要解析执行器私有 checkpoint。

Run 锁保护执行步骤，Session 锁保护消息编号。提交持有 Session 锁时只查询旧 Run 状态，不锁旧 Run；worker 持有 Run 锁后追加消息。避免两个方向互相等待。

## 自动接管

领取事务通过 FOR UPDATE SKIP LOCKED 找到待执行或执行权过期的任务，增加 epoch 并记录执行者。事务提交后再执行模型和工具，不在长事务里做业务工作。

所有保存状态、工具结果、续期操作都检查 runID、workerID、epoch、Running 状态和执行权有效期。旧实例恢复后无法覆盖新实例的状态。续期失败会取消本地 context，但外部请求可能已经到达下游，不能宣称网络副作用也被数据库隔离。

[PostgreSQL 对 SKIP LOCKED 的说明](https://www.postgresql.org/docs/16/sql-select.html)明确其可用于多个消费者访问队列表；这里选择 PostgreSQL 是新执行存储的实现决策，不表示已兼容原项目支持的全部数据库。

## Eino 执行边界

发布的 JSON 配置装配为单个 `ChatModelAgent`，由 `Runner` 执行 ReAct。Eino 管理消息上下文、工具分派、模型轮次上限和中断恢复。任务领取、租约、项目授权及外部副作用记录属于平台。

模型返回工具调用后，工具 middleware 先触发原生中断，不执行实际工具。Runner 调用平台的 `CheckPointStore.Set`；该适配器通过 `SaveDecision` 将 Eino checkpoint 与公开模型消息同事务提交。Runner 随后自动恢复此内部中断。

每个实际工具仍通过 `BeginStep` / `FinishStep` 记录意图与结果。结果提交时保存的 checkpoint 可以位于本批工具之前。进程崩溃后，Eino 恢复工具节点，middleware 用已提交结果完成该调用；Eino 自己重建后续上下文，无需平台保存第二份消息队列或工具游标。澄清或 Unsafe 结果不明时，再保存原生 checkpoint 和等待状态，并释放执行权。

## 恢复边界

| 中断位置 | 接管行为 |
| --- | --- |
| 任务已保存，尚未领取 | 新 worker 领取 |
| 模型调用中途 | 重新执行这次模型调用，可能再次产生模型费用 |
| 模型决策已保存，工具尚未开始 | 使用保存的工具名称、参数和调用编号 |
| 工具意图保存，结果尚未保存 | ReadOnly 重试；Idempotent 使用同一编号重试；Unsafe 待确认 |
| 工具结果与上下文已提交 | 跳过工具，继续下一步 |
| 等待澄清时重启 | 保持等待；用户回答提交后重新排队 |
| 完成状态提交，Redis 不可用 | Outbox 重试，API 仍从数据库返回完成状态和事件 |

工具执行与本地数据库事务不能跨服务原子提交。Idempotent 策略必须由下游真正实现操作编号去重；加 HTTP header 本身不构成幂等。AppForge 两种协议没有已验证的去重契约，因此配置校验拒绝将它们标为 Idempotent。

NeedsReview 接口要求用户提供已经确认的外部结果；它不是“无条件重跑”按钮。人工确认、取消同样写事件。取消阻止后续状态提交和自动领取，但不会撤销已经完成的外部操作。

## 事件一致性

状态、结果与事件在同一个 PostgreSQL 事务提交。每个任务的 event_seq 在该任务行的锁保护下递增，作为任务内的事件顺序。

Relay 只领取每个任务最早的未发布事件：NOT EXISTS 排除前面仍有未发布事件的记录，FOR UPDATE OF e SKIP LOCKED 锁定当前事件并跳过其他发布者已领取的记录。行锁一直持有到 Redis 返回成功、published 标记提交；未提交的标记对其他发布者仍不可见，因此同一任务的后续事件不能越过。事务失败会回滚标记并释放锁，后续重新发布同一事件；Redis 已收到时由显式事件 ID 去重。

不同任务可由多个 Relay 并行发布。每个 API 实例当前运行一个 Relay，多实例共享数据库进行协调；没有跨任务的全局发布锁。单个任务的首条待发布事件失败，会阻止该任务后续事件发布。Redis 调用在 Relay 中设有 3 秒 context 截止时间，同时受客户端网络超时约束，行锁和连接在事务结束时释放。尚未进行容量压测。

Redis Stream 使用事件 seq 作为显式 ID。Lua 在插入前查询该 ID，处理“Redis 已成功、数据库发布标记尚未提交”时的重试。事件不依赖 Redis 8.6 的新幂等命令，测试使用 Redis 7.2。数据库回放覆盖 Redis 丢失或过期；重复消费方仍应按 runID:seq 去重。[Redis XADD 文档](https://redis.io/docs/latest/commands/xadd/)

## 配置与版本

任务固定模型地址、模型名、系统提示词、工具元信息与配置版本。凭据保存环境变量引用，允许部署侧轮换凭据。管理接口的调用权限由 Forge middleware 校验；普通发送消息接口只接受 query。Agent 配置保存在数据库，任务保存发布版本的完整快照。

执行状态带版本号 2，保存 Eino 原生 checkpoint、操作编号使用的模型轮次及 waitingStep。旧 version 1 不转换、不从头重跑；新版 Worker 会明确拒绝。升级前由旧 Worker 完成或取消旧格式的所有活动、等待澄清和待确认运行，保留历史记录，再切换 Worker；需要继续的业务任务创建新的执行轮次。不要混跑两个版本的 Worker。后续 Eino 升级须验证 checkpoint 格式与工具语义兼容。

## 当前限制

- Eino checkpoint 以字节数据保存在 JSON 状态内；每批工具前有一次持久化中断／恢复。大附件应使用共享存储引用，不能依赖 worker 本地临时文件。
- 会话上下文目前只带最近 20 个已完成轮次的用户输入和最终答案，没有迁移原四层压缩与工具历史检索。
- 没有承诺外部工具 exactly once，也没有实现跨服务补偿事务。
- PostgreSQL 本身仍需按部署要求配置持久化、高可用和备份；进程重启测试不等于数据库灾备验证。

## 应用与 Agent 定义

Writing、Coding 等应用都引用平台的已发布 Agent。应用提交业务输入与 scope，运行时解析不可变版本并保存快照。通用 Worker 无应用类型分支；应用专属工具通过发布配置的 HTTP/MCP/OpenAPI 工具装配。

应用任务通过 owner/tenant/project/task 确定会话归属。首次创建会话前使用该身份的事务锁，之后锁定 Session 并拒绝活动 Run 重叠。幂等检查先于版本与活动任务检查。发布新版本和网络重试不改变已接受执行。

Worker 为 HTTP 工具注入来自冻结快照的 `X-Agentic-Scope`，并继续提供运行时 ID、epoch 与操作编号。模型参数不能覆盖这些 headers。业务工具结合当前运行时租约检查实际写入资格。
