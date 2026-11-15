# 必要保存的回收保护

日期：2026-11-09。状态：Center 的 SaveBlocked 保留与停止机制已实现，并在本地 PostgreSQL 和 OrbStack 验证。自动保存尚未接入。

## 当前流程

申请时声明 `requiredSave: true`，该声明随幂等请求固定。当前 Provider 为 `/workspace` 单独申请 1 GiB、ReadWriteOnce PVC，使用集群默认 StorageClass；普通沙箱继续使用 emptyDir。PVC 不设置 Pod ownerReference。工作区 UID 与 Pod UID 在等待 Ready 和激活前分别入库，激活必须匹配已登记的 Pod 身份。

释放和到期使用同一个事务，先锁定 sandbox，再锁定 lease。到期处理在锁内重新检查数据库时间，避免旧扫描结果关闭已经续期的租约。必要保存尚未完成时，sandbox 进入 `SaveBlocked`、lease 进入 `Releasing`，停止接受新操作与续租。普通释放不能把 `SaveBlocked` 改成可销毁状态。

Guard 的私有 `/stop` 接收 Pod UID，和 Downward API 注入的自身 UID 比较。匹配后永久关闭准入、唤醒期限监视器并退出 PID 1。控制响应丢失不等于停止失败或成功；Provider 观察原 Pod UID 的 Guard 容器终止后才记录 `stoppedAt`。实例终止停止该 PID namespace 中的写者，独立 PVC 仍可被后续保存器挂载。工作区恢复等级仍取决于底层存储，本机 local-path 不提供跨节点灾备。

Pod 创建成功但身份登记或激活失败时，对账按本沙箱的资源标签补齐物理 UID，再进入阻塞和停止流程。已经登记的 UID 不允许替换。尚未确认创建结果、对象缺失或 UID 不符时保留阻塞，不将 NotFound 当作进程停止证据。失败停止更新对账时间，使其他记录有机会进入后续批次。

停止确认后的 SaveBlocked 不参与普通销毁扫描，不关闭关联的 durable 环境记录。当前没有独立的业务配额结算实现；这里的保护是保留环境事实与实际 PVC，不将其标记为回收完成。物理删除使用 Kubernetes UID precondition 只作用于已登记的 Pod 和 PVC；同名替代实例不会被删除。

内部 `confirmRequiredSave` 仅在 generation、version、停止证据和工作区身份满足条件时接受非空检查点引用，之后进入 `Releasing`，由原有对账回收 Pod 和 PVC。该方法没有 HTTP 入口，检查点上传及验证仍须由后续可信保存器提供。重复相同确认幂等；旧 generation/version、空引用、尚未停止的确认均被拒绝。

## 已执行验证

- PostgreSQL：主动释放、租约到期、关闭后拒绝续租和新执行、重复释放、Provider 与 Store 的销毁阻断、缺少停止证据、旧 generation/version、空引用、保存后恢复可回收状态。
- 真实 Kubernetes：主动释放和到期两种路径均进入 SaveBlocked，原 Guard 容器已终止；重复释放及对账保留 PVC；错误 UID 的停止请求被拒绝。
- 独立非 root 只读 Pod 重新挂载 PVC，读取原结果文件及心跳文件；停止后跨一秒观察心跳字节数不再变化。
- 真实 Kubernetes：模拟 Pod/PVC 已创建、数据库尚无物理 UID 且未激活；释放补齐两个 UID，确认原实例停止并保留 PVC。
- 测试经内部确认事务解除阻塞后，对账删除 Pod 和 PVC，状态变为 Destroyed。测试使用显式构造的检查点引用，**没有上传真实检查点**；它验证解除阻塞和清理顺序。

源码与可复现用例：

- `internal/sandboxcenter/required_save.go`
- `internal/sandboxcenter/provider_identity.go`
- `internal/sandboxcenter/required_save_test.go`
- `internal/sandboxcenter/required_save_e2e_test.go`
- `internal/sandboxcenter/provider_k8s.go`
- `docs/sandbox-center/probes/guard-execd/main_linux.go`

```sh
# 使用当前 Guard 源码构建本地测试镜像，不修改原 phase2 镜像
docs/sandbox-center/probes/guard-execd/build-image.sh saveblocked

TEST_DATABASE_URL='postgres://agentic:agentic_local@127.0.0.1:55432/agentic?sslmode=disable' \
SANDBOX_E2E_K8S_CONTEXT=orbstack \
SANDBOX_E2E_GUARD_IMAGE=agentic-guard-execd:saveblocked \
GOCACHE=/private/tmp/go-cache GOWORK=off \
  go test -count=1 -v -timeout=360s ./internal/sandboxcenter \
  -run 'TestRequiredSaveRetentionE2E|TestRequiredSaveProvisioningRecoveryE2E'
```

每个用例只清理自己创建的 Pod、PVC 与随机数据库 schema。当前 schema 初始化仍遵循项目的空库约定；本次未修改已有部署的表结构或切换运行镜像。

## 下一步

将已有 LayerFS 机制接到持久工作区与可信保存器：停止后读取对应工作区 UID，打包本次数据，上传并核实内容寻址检查点，再提交内部保存确认。保存意图、失败原因和重试进度须持久化，重试继续同一意图。拥有者放弃未保存数据需要真实身份及绑定本次数据范围的授权，当前不开放该动作。
