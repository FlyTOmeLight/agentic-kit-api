# Agent 版本正文存入 artifact-go，本地保留草稿与发布指针

2026-10-20。此决定偏离 [ADR-0008](0008-memory-artifact-versions.md) 末尾「本提案不迁移 Agent 资产版本或执行 checkpoint」的范围限定。实施细节见 [Agent 资产接入 artifact-go 技术设计](../agent-asset-artifact-design.md)。

**决定。** Agent 的已发布版本正文只保存一份，位于 artifact-go 的不可变版本中；本地 `agent` 表保留名称、可编辑草稿、revision 和发布指针，本地 `agent_version` 表只保留公开版本号与远端版本 ID/URI，不再保存 `config`。Run 在被接受时冻结完整配置快照，执行侧不访问 artifact。

**为何偏离 ADR-0008。** ADR-0008 只为 Memory 引入 artifact，并把 Agent 版本迁移列为范围外，是因为当时 Memory 已有确定的生效引用与召回需求，而 Agent 资产的重构尚未开始。现在资产事务归位已进入实施，若继续让 Agent 自建版本正文，就会出现两套版本存储、两套编号和两套不可变语义。已发布版本与 Memory 内容在存储语义上相同：不可变、按序列编号、需要保护与历史。复用同一服务比维护第二套更简单，也让「版本服务」真正承担版本职责。

与 Memory 的差异仍然存在，因此不照搬 Memory 的做法：

- Memory 编辑的是「生效内容」，本地需要完整投影供召回；Agent 编辑的是草稿，草稿本来就是可变的本地工作副本，已发布版本只被读取。
- Memory 的生效指针和本地投影必须同事务更新；Agent 的发布指针只需与本地版本行一致，正文的远端写入在前。
- Memory 的维护是持久意图加重试；Agent 发布是同步请求，失败直接返回给调用方，重试由调用方发起。

**跨服务提交。** 发布顺序固定为「远端建版本 → 本地提交指针」。远端请求键由已持久化的 `agentID + revision` 推导，同一 revision 的重试命中同一版本，不产生重复。本地提交失败（进程退出、连接断开、锁内复核不通过）会在远端留下孤立版本；它不进入本地版本表，不影响公开版本号、版本读取、指向或 Run 快照。接受这一代价，不引入跨库两阶段提交，也不增加对账任务。

**编号。** 公开版本号仍由本地在 agent 行锁内分配。远端编号与序列号只用于追溯，不进入 HTTP 契约，避免本地提交失败导致公开版本号出现空洞。

**身份。** Forge middleware 已核验用户身份及 tenant/project，并通过 `access.WithScope` 写入请求 context；HTTP 用户与 propagation 路由末端强制非空租户，本地对象按 tenant 与 owner 隔离。`Build` 向 artifact 客户端注入 API 自身的 `Tenant.ServiceAPIKey`；`RequestIdentity` 仅选择已核验的 user/tenant/project。适配器发送 service Bearer 和目标身份传播头，并将 `X-Resource-User-ID` 固定为同一用户，以保留个人范围。artifact 验证服务身份及传播白名单；入站 Authorization、Cookie、CSRF、Origin 均不转发。缺 tenant 或 actor 时拒绝远端调用，不设默认租户、不退化为共享范围。浏览器不持有服务凭据。系统预置或共享 Agent 需要新的 AssetScope 与对应授权契约，另行设计。

**前提。** 当前使用 Forge SDK `v0.13.0` 和支持 `BearerToken`、`PropagatedUserID` 的 artifact-go `v0.5.1`。artifact 部署须允许该 API 服务传播身份。初始化只面向空库或空 schema，旧库的数据迁移和旧索引升级由部署方另行处理。独立构建及 OrbStack 联调范围见 [验证记录](../verification.md#2026-11-17-agent-版本发布与-run-快照)。
