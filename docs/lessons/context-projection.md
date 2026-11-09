# 上下文投影与完整执行事实

## 模型输入裁剪不要改写 Eino checkpoint

适用范围：worker 用 Eino ADK v0.9.2 管理消息与 checkpoint，并在模型调用前执行上下文预算裁剪或工具预览。

`BeforeModelRewriteState` 返回的 `state.Messages` 会由 Eino 持久回执行状态。把精简后的消息放回该字段，会让下一次工具边界的 checkpoint 保存工具预览或裁剪结果，破坏“模型看预览、恢复保留完整事实”的约定。

当前处理：在 worker 的 `boundaryModel.Generate` 内用 builder 创建独立消息切片，读取本次模型调用 options 中的真实工具定义，再交给底层模型。该构建在 `ModelStarted` 提交前完成；超限不记录实际模型已开始，也不请求自动重试。Eino state 保留本轮原始消息。`ReturnDirectly` 的成功结果在构建前直接结束，不需要再经过模型预算。

验证：worker 的 `TestEinoPreviewsToolOutputAndStoresOriginal` 连续执行两次工具，断言模型只收到有界预览、公开存储结果完整，以及第二次工具边界的 checkpoint 仍包含第一次工具的完整结果；`TestModelBoundaryCountsActualToolDefinitionsBeforeCallingModel` 检查实际大工具定义在提交模型调用前被拒绝。

## 历史工具关联 ID 必须区分 Run

适用范围：worker 为每个 Run 生成 `turn_1_tool_0` 等局部稳定步骤 ID，并把多个 Run 的完整工具链送进同一次模型输入。

步骤 ID 在单个 Run 内唯一，不能直接作为整段会话的工具关联 ID。当前历史装载器先校验每组配对，再把 call 和 result 的关联 ID 同时投影为 `runID:stepID`；当前 Run 保留自身步骤 ID。原始消息和步骤不改写。

验证：[TestHistoryToolIDsAreScopedByRun](../../pkg/durable/prepare_test.go) 使用两段相同步骤 ID 的历史和一段当前工具链，检查整体构建成功、配对保持一致、存储输入未被修改。
