# Agent 长任务恢复设计

## 解决的问题

原代码的 `startBackgroundRun` 在服务进程内启动 goroutine，`context.WithoutCancel` 只移除了 HTTP 请求取消传播。旧 Runner 的 DB checkpoint 在工具中断、用户澄清时保存，不能据此承诺任意重启后的恢复。

新实现将任务、模型决策、工具结果和用户交互状态持久化，由独立 worker 领取。恢复单位是已经保存的模型或工具步骤，不是 goroutine 指令位置，也不是已经流出的下一个 token。

## 数据与职责

```text
客户端 → API → PostgreSQL durable_run / durable_step / durable_event
                    ↑                           ↓
              worker 领取执行             Outbox relay → Redis Streams
                                                ↓
客户端 ← API SSE ← 数据库事件回放及 Redis 唤醒
```

- durable_run：不可变执行配置、输入、上下文快照、当前状态、期限、执行者和 epoch。
- durable_step：工具执行前的意图、输入摘要、重试策略、执行结果。操作编号是 runID:stepID，重试不改变。
- durable_event：与状态同事务写入的事件，也是 Outbox。Redis 只是通知与缓存，数据库是事件权威来源。

API 仓库拥有共享持久化包，worker 只调用其中的状态变更方法。持久化访问统一由 GORM 管理；领取使用 Locking 子句，数据库时钟和原子事件计数通过 GORM 参数化 SQL 执行。schema.sql 由 GORM 事务执行，保留 PostgreSQL 特有约束和索引。数据库时间决定执行权期限，避免多个 worker 的本地时钟差异影响领取判断。

## 自动接管

领取事务通过 FOR UPDATE SKIP LOCKED 找到待执行或执行权过期的任务，增加 epoch 并记录执行者。事务提交后再执行模型和工具，不在长事务里做业务工作。

所有保存状态、工具结果、续期操作都检查 runID、workerID、epoch、Running 状态和执行权有效期。旧实例恢复后无法覆盖新实例的状态。续期失败会取消本地 context，但外部请求可能已经到达下游，不能宣称网络副作用也被数据库隔离。

[PostgreSQL 对 SKIP LOCKED 的说明](https://www.postgresql.org/docs/16/sql-select.html)明确其可用于多个消费者访问队列表；这里选择 PostgreSQL 是新执行存储的实现决策，不表示已兼容原项目支持的全部数据库。

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

状态、结果与事件在同一个 PostgreSQL 事务提交。Relay 通过全局事务级 advisory lock 保持发布顺序；Redis 网络调用有 3 秒截止时间，网络阻塞不会无限占住事务。该串行 Relay 是明确的吞吐量取舍，尚未进行容量压测。

Redis Stream 使用事件 seq 作为显式 ID。Lua 在插入前查询该 ID，处理“Redis 已成功、数据库发布标记尚未提交”时的重试。事件不依赖 Redis 8.6 的新幂等命令，测试使用 Redis 7.2。数据库回放覆盖 Redis 丢失或过期；重复消费方仍应按 runID:seq 去重。[Redis XADD 文档](https://redis.io/docs/latest/commands/xadd/)

## 配置与版本

任务固定模型地址、模型名、系统提示词、工具元信息与配置版本。凭据保存环境变量引用，允许部署侧轮换凭据。配置文件由服务端维护，客户端不能传任意模型地址或工具 URL。

执行状态带版本号 1；不支持的版本会报错，不会静默当成新任务。滚动升级必须保持已有状态格式及工具语义兼容，或等待旧任务结束后切换。

## 当前限制

- 全量上下文 JSON 按步骤保存；大附件应使用共享存储引用，不能依赖 worker 本地临时文件。
- 会话上下文目前只带最近 20 个已完成轮次的用户输入和最终答案，没有迁移原四层压缩与工具历史检索。
- 没有承诺外部工具 exactly once，也没有实现跨服务补偿事务。
- PostgreSQL 本身仍需按部署要求配置持久化、高可用和备份；进程重启测试不等于数据库灾备验证。
