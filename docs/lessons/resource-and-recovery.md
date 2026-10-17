# 资源所有权与恢复经验

## 借用数据库时保留所有权与事务语义

将数据库初始化交给 server-go 后，`durable.New` 借用框架连接；`durable.Open` 则创建并拥有连接池。两种构造方式的关闭行为必须不同，借用方不能关闭共享连接。

连接能建立还不足以说明持久化行为一致。本项目的事务依赖 PostgreSQL 唯一约束错误转换，并要求 UTC 时间与静默 SQL 日志。框架数据库适配后，Store 在独立 Session 上设置这些行为，并使用底层 dialector 保留错误转换，避免修改共享句柄的配置。

证据：[Store 构造与关闭](../../pkg/durable/store.go)、[TestInjectedDatabaseOwnershipAndErrors](../../pkg/durable/framework_store_test.go)。该测试使用真实 PostgreSQL，覆盖共享配置未被修改、重复迁移、唯一约束错误，以及 `Store.Close` 后借用连接仍可查询。

## 通知连接不能成为数据库回放的前置条件

本项目的 Redis 负责 Outbox 通知，已提交事件仍保存在数据库。若使用要求 Redis 在线才能完成初始化的模块，会让通知故障阻止 API 启动，破坏数据库回放的用途。

当前专用 `notificationCache` 只解析连接配置并创建客户端，不在初始化时要求 Redis 可达；生命周期仍由框架接管。配置格式错误会返回错误，Redis 离线则由通知发布与事件读取路径处理。事件和状态的一致提交仍由数据库事务保证。

证据：[通知连接](../../service/notification_cache.go)、[装配与资源接管](../../service/build.go)、[事件发布测试](../../pkg/durable/events_test.go)。Redis 不可用时的真实 API 启动与 SSE 回放证据见 [验证记录](../verification.md) 中的“强杀进程验证”和“server-go v0.9.0 重构与 Coding 根模块升级”；事件权威来源与重试顺序见 [恢复设计](../design.md)。
