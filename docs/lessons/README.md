# 项目经验索引

这里保存本项目已经遇到并验证过的问题、处理方式和证据，供后续修改时查阅。当前目录与职责以 [项目布局](../layout.md) 为准，执行契约以 [恢复设计](../design.md) 为准；具体历史版本、环境和验证结果保留在 [验证记录](../verification.md)。

| 遇到的问题 | 查阅 |
| --- | --- |
| 本地联合开发通过，独立构建却缺少框架或 SDK 接口 | [发布依赖与框架迁移](server-go-migration.md#发布依赖必须独立验证) |
| 请求绑定后，Header 覆盖正文或路径字段 | [请求来源隔离](server-go-migration.md#请求绑定要保留字段来源) |
| 配置启用了 Swagger，入口却没有安装相应能力 | [入口与配置配套](server-go-migration.md#入口迁移要核对配置和可选能力) |
| 框架接管数据库后，Store 关闭连接或丢失约束错误 | [数据库所有权](resource-and-recovery.md#借用数据库时保留所有权与事务语义) |
| 更新 `serializer:json` 字段报无法编码结构体 | [JSON 字段更新](resource-and-recovery.md#json-字段更新不能用-map) |
| Redis 断开，API 无法启动或无法回放事件 | [通知连接与数据库事件](resource-and-recovery.md#通知连接不能成为数据库回放的前置条件) |
| 带 digest 的 JSON payload 经 `jsonb` 往返后校验失败 | [上下文快照存储](context-snapshot-storage.md#带-digest-的不透明-payload-不要存-jsonb) |
| 上下文裁剪污染 checkpoint，或多个历史 Run 的工具 ID 冲突 | [上下文投影与完整事实](context-projection.md) |
| 测试通过，但外部依赖用例被跳过，或未覆盖真实恢复 | [验证范围](verification.md) |
| Steering 消费后进程退出丢指令，或签名正文被 Query 覆盖 | [长任务边界](long-task-boundaries.md) |
| Goal 续跑时文件丢失，或 Release 成功被误认为保存完成 | [跨 Run 工作区恢复](long-task-boundaries.md#跨-run-续跑必须等待工作区保存确认) |
| 只检查恢复参数或手动确认检查点，未覆盖真实 Worker 与文件继承 | [Goal 跨 Run 真实集群验收](long-task-boundaries.md#goal-跨-run-恢复要验证真实文件与自动状态推进) |
| 保存屏障期间 Worker 退出，下一轮无人推进 | [保存与迭代不依赖原 Worker 进程](long-task-boundaries.md#goal-跨-run-恢复要验证真实文件与自动状态推进) |
| 真实命令成功但验收回执未提交时 Worker 退出 | [真实沙箱验收接管](long-task-boundaries.md#真实沙箱验收接管要区分操作完成与回执提交) |
| 压缩丢历史、工具签名或恢复事实，或会话消息读取被分页截断 | [压缩投影边界](long-task-boundaries.md#压缩只能改变模型投影) |
| 缺失 usage 被当作 0、摘要绕过预算，或模型响应恢复后重复计费 | [模型用量与恢复](long-task-boundaries.md#模型用量必须可恢复且未知不等于零) |
| 沙箱缺失退出码被当成成功，或验收释放顺序正确但期限与恢复仍缺失 | [验收边界](long-task-boundaries.md#沙箱命令必须有确定退出码) |
| 沙箱命令终态仍写入、Pod 截止不能续长，或配置开启但隔离能力不可用 | [沙箱 Runtime 故障探针](sandbox-runtime-probes.md) |
| 上游 Run 缩短沙箱实际租约，固定续租间隔导致任务中断 | [沙箱续租期限](sandbox-runtime-probes.md#沙箱续租需要使用中心确认的到期时间) |
| 故障注入客户端被整实例停止杀死，退出码不能单独判定结果 | [整实例故障注入](sandbox-runtime-probes.md#整实例故障注入不能只看注入客户端退出码) |
| 用户进程与沙箱控制端口共享网络，代理鉴权不足以阻止绕过 | [控制端口边界](sandbox-runtime-probes.md#同网络-namespace-的控制端口不能只靠代理隐藏) |
| 在容器内向沙箱 PID 1 发信号无法模拟 Guard 崩溃 | [K8s 整实例停止注入](sandbox-runtime-probes.md#kubernetes-内不能从容器内部杀死-guard-来验证整实例停止) |
| 沙箱已续租但长命令返回 Unknown | [命令链超时](sandbox-runtime-probes.md#租约能续不代表单条命令能一直等待) |
| LayerFS 的 upper 挂载失败，或原 upper 重挂载被误当成检查点恢复 | [LayerFS 文件系统与恢复探针](sandbox-runtime-probes.md#layerfs-挂载要核对底层文件系统) |
| 停止实例后仍用 emptyDir 保留必要保存数据，或等待 Ready 失败后丢失实例身份 | [必要保存的持久工作区](sandbox-runtime-probes.md#停止实例不能靠-emptydir-保留待保存文件) |
| Saver 以 root 读取工作区却被拒绝，或恢复时 chown 报权限错误 | [LayerFS 保存与恢复能力边界](sandbox-runtime-probes.md#layerfs-保存恢复需要的最小能力) |
| 停止后不再保存、确认后遗留 Saver，或旧请求重建 Saver | [保存调度与清理](sandbox-runtime-probes.md#停止记录仍需调度保存确认后仍需清理) |
| 重试恢复覆盖工作区，或未激活的恢复实例发布半成品 | [恢复与启动顺序](sandbox-runtime-probes.md#恢复必须在用户进程启动前完成) |

新增经验时记录具体问题、已验证的处理和代码或测试链接。历史验证流水继续写入 `docs/verification.md`，避免在这里重复维护版本清单。
