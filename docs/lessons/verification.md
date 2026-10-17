# 验证范围经验

## 明确外部依赖用例是否真的执行

本项目持久化测试需要 PostgreSQL，事件发布测试还需要 Redis。缺少环境变量时，部分测试会跳过；单独看到 `go test` 成功不能证明这些行为通过。

`make integration` 先检查 `TEST_DATABASE_URL` 和 `TEST_REDIS_URL`，再运行全仓库 race 测试与 vet。验证使用隔离数据库或 schema，避免修改已有业务数据。依赖升级还需关闭 workspace 验证正式模块，具体启动和命令以 [README](../../README.md) 与 [Makefile](../../Makefile) 为准。

## 生命周期与 SSE 使用真实 HTTP 连接验证

handler 测试可以覆盖绑定和响应，但无法完整验证 app.Run 装配、就绪状态、后台任务退出和 SSE 写入期限。SSE 依赖真实连接提供逐次写入期限，测试中的 ResponseRecorder 不能代替这项能力。

本项目通过真实 HTTP 服务验证入口生命周期、SDK 调用和已提交事件回放，并等待 app.Run 返回，验证入口完成退出。OpenAPI 则核对生成文档的操作和路径参数是否对应实际路由。

证据：[框架生命周期测试](../../service/api_test.go)、[HTTP 与 SSE 契约测试](../../service/router_test.go)、[OpenAPI 路由测试](../../service/openapi_test.go)。

## 恢复承诺需要跨进程故障验证

恢复行为涉及独立 API、数据库、worker 执行权和已提交工具结果。只重建内存对象，无法证明进程退出后这些信息还能被新执行者正确使用。

相邻 worker 仓库的 `TestProcessKillResumesWithoutRepeatingTool` 实际启动 API 与 worker 子进程，在工具结果已提交、下一次模型调用被阻塞时强杀 worker，再由替代执行者接管。断言覆盖任务完成、epoch 递增、工具只调用一次和 Redis 不可用时的 SSE 回放。修改共享 durable 契约或恢复行为时，应保留这项验证。

证据与测试范围见 [验证记录](../verification.md) 中的“强杀进程验证”和“server-go v0.9.0 重构与 Coding 根模块升级”。该证据针对已提交步骤的恢复；外部工具去重与 checkpoint 格式约束仍以 [恢复设计](../design.md) 为准。
