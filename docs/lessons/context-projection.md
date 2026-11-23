# 上下文投影与完整执行事实

## 模型输入裁剪不要改写 Eino checkpoint

适用范围：worker 用 Eino ADK v0.9.2 管理消息与 checkpoint，并在模型调用前执行上下文预算裁剪或工具预览。

`BeforeModelRewriteState` 返回的 `state.Messages` 会由 Eino 持久回执行状态。把精简后的消息放回该字段，会让下一次工具边界的 checkpoint 保存工具预览或裁剪结果，破坏“模型看预览、恢复保留完整事实”的约定。

当前处理：在 worker 的 `boundaryModel` 内为 Generate 和 Stream 构建独立消息切片，读取本次模型调用 options 中的真实工具定义，再交给底层模型。该构建在 `ModelStarted` 提交前完成；超限不记录实际模型已开始，也不请求自动重试。Eino state 保留本轮原始消息。`ReturnDirectly` 的成功结果在构建前直接结束，不需要再经过模型预算。

验证：worker 的 `TestEinoPreviewsToolOutputAndStoresOriginal` 连续执行两次工具，断言模型只收到有界预览、公开存储结果完整，以及第二次工具边界的 checkpoint 仍包含第一次工具的完整结果；`TestModelBoundaryCountsActualToolDefinitionsBeforeCallingModel` 检查实际大工具定义在提交模型调用前被拒绝。

## 历史工具关联 ID 必须区分 Run

适用范围：worker 为每个 Run 生成 `turn_1_tool_0` 等局部稳定步骤 ID，并把多个 Run 的完整工具链送进同一次模型输入。

步骤 ID 在单个 Run 内唯一，不能直接作为整段会话的工具关联 ID。当前历史装载器先校验每组配对，再把 call 和 result 的关联 ID 同时投影为 `runID:stepID`；当前 Run 保留自身步骤 ID。原始消息和步骤不改写。

验证：[TestHistoryToolIDsAreScopedByRun](../../pkg/durable/prepare_test.go) 使用两段相同步骤 ID 的历史和一段当前工具链，检查整体构建成功、配对保持一致、存储输入未被修改。

## 压缩、计费与溢出恢复共用模型边界

`context-v4` 用 `cjk-weighted-v1` 估算输入，计入实际绑定的工具和 steer。Worker 将上一轮真实 prompt token 和本地估算作为 `UsageAnchor` 随用量提交，为下一轮提前压缩提供依据。接近预算后，Worker 先预览或缩短旧工具内容，再尝试保留近期批次并分块滚动摘要，最后才从请求中移除完整旧轮次。原始查询和已被移出的澄清或人工审核结果由 Worker 固定保留。

每个摘要块都使用冻结计价，派发前保存 `InflightModel`，有用量后独立计费。三次连续可归因的失败会打开 Run 级摘要熔断器，接管后继续沿用。只有明确的零用量上下文溢出拒绝允许清除派发标记，并在写入 `ModelOverflowRejected` 后收紧目标重试。消耗未知时保留派发标记并进入 `Blocked`。连续的明确溢出拒绝在重试或缩减空间耗尽后进入 `Failed`。

`read_step_result` 在当前 Run 的租约下读取已完成步骤的原始内容。模型可用投影中的步骤 ID 和 `next_offset` 按 UTF-8 安全字节页继续读取。Worker 验证覆盖了真实 HTTP 400 拒绝与终止、网络中断后人工恢复、熔断接管和分页，见 `TestHTTPContextOverflowRecovery`、`TestMainModelUnknownConsumptionStillRequiresUnblock`、`TestSummaryBreakerSurvivesTakeoverAndDropsRounds` 和 `TestReadStepResultPaginatesStoredUTF8`。

## Session 只提升成功 Run 的压缩进度

Run 内滚动摘要与 `FirstKeptSeq` 只能改变模型投影。完成事务才将摘要提升到 Session，并将 Session 水位推进到 `FirstKeptSeq - 1`。下一次提交冻结 `BaseSummary` 与水位之后的成组消息。失败和取消仍保留各自的 Run 记录，Session 继续使用上次成功提交的摘要和水位。

API 测试覆盖完成、失败、取消和后续提交冻结。Worker 的 `TestRollingCompactionPreservesInputsAndPromotesSession` 从澄清和多轮工具调用走到下一次提交，并检查 Eino checkpoint 和步骤原始内容。[API Session 压缩测试](../../pkg/durable/context_recovery_test.go)
