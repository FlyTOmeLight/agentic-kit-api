# 与原 Agentic Kit 的迁移对应

当前交付是新 API/worker 的可运行执行主链路，目标是先证明重启接管与步骤恢复。它没有把原项目所有产品功能搬进新仓库，也没有切换原流量。

| 原代码 | 新实现 | 状态 |
| --- | --- | --- |
| conversation/service.go startBackgroundRun | API 入库；worker 轮询领取 | 已实现 |
| runner.Run 进程内 ReAct | engine.Execute 持久化 ReAct 步骤 | 已实现 |
| runner.Resume 澄清 checkpoint | durable_step 保存澄清结果，任务重新排队 | 已实现，协议不同 |
| Redis 心跳与异常任务识别 | PostgreSQL 执行权、epoch、接管与续期 | 已实现 |
| lifecycle.go 事务内 Redis emit | durable_event Outbox 与 Relay | 已实现 |
| HeadlessEmitter 与 SubscribeAgentSession | 按 Run 的数据库事件回放 SSE | 已实现，非旧 Session SSE 契约 |
| llm.Build 与 chatmodel | 同一 Eino 接口和 OpenAI 兼容模型接入 | 已实现基础模型参数；Prism endpoint 动态解析待接 |
| runtime/adapters.go MCP 调用 | AppForgeMCP 使用 /v1/RunMcpTool | 已实现协议适配，未连接真实业务环境 |
| runtime/adapters.go OpenAPI 调用 | AppForgeOpenAPI 使用 /v1/RunTool | 已实现协议适配，未连接真实业务环境 |
| Agent 配置与发布 | 数据库草稿、revision 与不可变发布版本 | 已实现基础模型/工具配置；原核心文件与资源目录尚未迁移 |
| Session / Message | 独立会话、按序消息、运行记录和分页接口 | 已实现基础多轮对话；原会话搜索、置顶等未迁移 |
| Resource/Tool 元信息与权限解析 | 数据库配置快照、owner 隔离和 config_owners 写权限 | 原资源目录与 project 授权尚未迁移 |
| ask_for_clarification | 持久化等待与回答接口 | 已实现 |
| Sandbox、Skill、artifact、知识资源工具、Memory | 依赖原业务服务的工具装配 | 尚未迁移 |
| 上下文压缩、模型 failover、逐 token 输出 | 原 middleware 和 streaming 路径 | 尚未迁移 |
| auth-center 与 project 级授权 | 本地 bearer token 到 owner 映射 | 生产鉴权尚未迁移 |
| 旧 SDK 与路由 | /v1/runs 新接口 | 不兼容旧 SDK，未实现流量切换 |

## 后续接入顺序

1. 从原授权与配置读取入口生成不可变任务快照，保持 Agent、用户、project、资源权限语义；把模型 endpoint 解析移到受控配置装配阶段。
2. 逐项迁移原工具注册。每个工具标注 ReadOnly、Idempotent 或 Unsafe，并修正忽略执行 context 的调用路径。原 externalRuntimeTool.InvokableRun 忽略传入 context，不能原样复制后就宣称支持 worker 取消。
3. 对 sandbox workspace、文件和 artifact 明确共享存储及所有权，避免新实例只能拿到旧实例本地路径。
4. 将压缩后的上下文和动态工具列表纳入恢复状态，验证恢复后工具名称、参数和消息配对不变。
5. 适配旧 SDK/Session SSE 或明确前端升级，再用真实模型、AppForge、权限服务做联调和容量测试。

以上未迁移项不能写成已经完成的生产重构成果。当前可用于面试演示的实证是：持久化调度、故障接管、步骤恢复、幂等边界与完成事件一致性。
