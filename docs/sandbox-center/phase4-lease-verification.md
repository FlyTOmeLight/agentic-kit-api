# 阶段 4：执行权与续租验证

日期：2026-11-09。状态：完成当前执行权校验和运行中续租验证；阶段 4 尚未完成。

Center 使用 PostgreSQL 时钟检查 Run 的 owner、worker、epoch、状态与有效期。申请和激活需要有效领取；普通操作在受理和派发前分别检查当前执行权。沙箱期限不得超过 Run 租约和 Run deadline，重复派发由数据库状态转换阻止。

Run 完成时，若当前 epoch 有活动沙箱环境，且绑定的活动 Goal 尚未验收并使用命令验收器，事务会记录原 worker 和验收截止时间，截止时间取 Run deadline 与数据库当前时间加 10 分钟中的较早值。完成状态和该窗口一起提交或回滚。

窗口内，Center 允许对应沙箱续租，期限不得超过窗口截止时间；验收操作只接受对应环境的 `eval-<sandboxID>` 命令，并检查 Goal、Run、worker、epoch、环境、租约、验收记录及命令摘要。普通操作仍要求有效的 Running 领取。Goal 不再活动、绑定变化、已有验收记录或窗口到期后不能新增续租和验收派发。

这是有界的完成后授权窗口，不是独立的验收任务领取：worker 崩溃后没有接管及恢复协议，窗口本身也没有独立心跳。续租会延长整个沙箱实例的寿命，并不证明之前启动的后台进程已停止。

Worker 根据 Center 返回的 `hardExpiresAt` 安排续租：剩余时间的三分之一，最长间隔 10 秒；请求受当前到期时间约束，停止 Backend 时取消进行中的续租。失败后不继续续租，沙箱按既有期限关闭。

## 本地验证

- API：PostgreSQL 和 Redis 集成环境下，`GOWORK=off go test -race -count=1 -timeout=120s ./...` 通过；`go vet ./...` 和 `go build ./cmd/server` 通过。
- Worker：使用当前 API 本地源码，PostgreSQL 集成环境下，`GOWORK=off go test -race -count=1 -timeout=120s ./...` 通过；`go vet ./...` 通过。`renew_test.go` 覆盖短租约、续租失败、过期和取消进行中请求。
- 真实链路：隔离 PostgreSQL schema，API 和 OrbStack Kubernetes `sandbox-center` 命名空间，运行 `TestSandboxBackendAgainstRealCenter`。Run 租约 30 秒，心跳按三分之一续期，连续四条各执行 9 秒的命令成功；沙箱 revision 从 1 增至 5，最后释放 Pod。该测试验证持续运行超过初始租约，不验证单个长命令超过 SDK 超时的行为。
- 本轮 PostgreSQL 集成及竞态测试：`./pkg/durable ./internal/sandboxcenter` 通过，覆盖完成事务、回滚、窗口上限、错误 worker/epoch、Goal 状态、已有验收记录及普通命令拒绝。
- 本轮真实 Guard 验证：`TestCompletedEvaluationRenewalE2E` 在独立 PostgreSQL schema 和 OrbStack Pod 中，模拟 Run 完成后续租并更新 Guard，等原租约过期后成功执行验收命令。该用例直接构造完成状态，未覆盖 Worker 到 Center 的完整 HTTP 链路；测试 Pod 已清理。
- 长命令试验未通过：验收命令 `sleep 22; echo accepted` 约 15 秒后返回 `Unknown`。当前 Guard 到 execd 的 HTTP 客户端为 15 秒，Guard 服务写超时为 20 秒，命令请求还配置了 10 秒 timeout；不能把本轮续租验证当成长命令验收通过。

## 尚未完成

- 长命令需要统一 Guard、execd、Center、SDK 和 Worker 的超时与回执查询，真实运行及响应丢失恢复尚未完成；完成后 worker 崩溃的验收接管也尚未实现。
- Forge 租户身份鉴权尚未接入 Center；数据库执行权校验不能替代请求身份验证。
- LayerFS 节点挂载、OCI 增量及删除原 upper 后的新 upper 恢复已通过[独立机制探针](layerfs-verification.md)，尚未接入 Center。必要保存的 [SaveBlocked 保留与停止保护](saveblocked-verification.md)已接入 Center 并通过本地验证；检查点上传、自动保存重试、拥有者授权放弃、gVisor RuntimeClass 和生产性能验收仍待完成。续租丢响应后的 runtime 对账仍是待办。
