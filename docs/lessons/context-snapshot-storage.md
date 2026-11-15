# 上下文快照存储经验

## 带 digest 的不透明 payload 不要存 jsonb

`ContextSnapshot` 的 digest 按冻结 payload 的原始字节计算。PostgreSQL `jsonb` 会规范化对象键顺序和空白，读回的字节与写入时不同，按原文重算 digest 就会失败；该问题在持久化往返后的集成测试中复现，纯函数往返测试无法发现。

当前 `durable_run.context_snapshot` 使用 `text` 列，并通过表级 `Scanner`/`Valuer` 保存 envelope 的完整 JSON 文本，使 payload 字节和 digest 在存取后保持一致。只有需要对字段做 SQL 查询或索引的结构化 JSON 才使用 `jsonb`。

证据：[ContextSnapshot 列与扫描](../../internal/table/run.go)、[信封校验](../../pkg/durable/context.go)、[TestPublishedSnapshotsAndConversationHistory](../../pkg/durable/conversations_test.go)、[TestConversationHistoryKeepsCompleteTurnsOnly](../../pkg/durable/conversations_test.go)。

若未来需要直接查询快照内容，不能把列直接换回 `jsonb`；应先改为对规范化 JSON（例如解码后按键重排）计算 digest，并补持久化往返测试。
