# 沙箱 Runtime 故障探针

适用范围：在本机 Docker 或显式固定的 OrbStack Kubernetes 中验证沙箱的停止、到期与文件恢复能力；各探针结论仅覆盖对应配置，不替代目标生产环境验收。

## 先校准探针，再判断后端失败

OpenSandbox `release-1.1.1` 固定镜像实验中遇到以下已确认的探针问题：

- execd 初始流事件实际为 `{"type":"init","text":"执行ID"}`，不能假定 ID 字段叫 `id`，或每行必有 `data:` 前缀。
- 环境创建的 `timeout=15` 返回 HTTP 422，实际最小值为 60 秒；命令执行的 timeout 使用毫秒。探针须区分两种时限。
- Docker 动态宿主端口在容器 stop/start 或 restart 后可能变化。原端口连接失败不代表服务没有恢复；每次重启后重新读取 `docker port`，再检查就绪。
- 响应丢失实验必须先看到服务端副作用，再取消客户端请求，并检查返回确为 `context.Canceled`。若请求已完成，该轮不能证明丢响应恢复行为。
- 管理服务会在启动时恢复带 OpenSandbox 标签的已有容器；实验应拒绝与已有实例混用。创建响应可能丢失，清理不能仅依赖响应中的 sandbox ID，应按本次独占卷定位资源。

证据和处理见 [探针源码](../sandbox-center/probes/opensandbox/runtime_test.go)、[复现说明](../sandbox-center/probes/opensandbox/README.md) 和 [实际结果](../sandbox-center/opensandbox-runtime-verification.md)。镜像 digest 固定在源码及验证记录；镜像内部包版本不能单独作为发布身份。

## 接管要求应通过副作用验证

命令终态不证明所有子进程已停止；TTL 字段不证明控制面失联后仍按时停止。探针分别观察脱离进程的心跳文件、控制面退出后的新执行、重启后原执行 ID 的状态和输出。只有明确的去重契约或可靠回执才能支持恢复重试，普通 404 不能证明命令未执行。

结果记录为具体版本及配置下的能力边界；不把本项目额外要求缺失描述成上游违反其未承诺的契约，也不把 Docker 结论套到其他 backend。

## Pod deadline 不是可续租期限

本地 Kubernetes `v1.36.5+orb1` 实测拒绝把 Pod `activeDeadlineSeconds` 从 30 延长到 45；该字段可作固定寿命兜底，不能直接用于不断延长的 lease。没有 sandbox manager 时 kubelet 仍终止了 `setsid` writer，删除 Pod 后独立 PVC 文件保留，但本轮末次写入比截止晚约 2.161 秒。不能把实验的 5 秒容差写成严格截止承诺，也不能从本轮推断节点/控制平面网络分区下的行为。

证据：[探针](../sandbox-center/probes/kubernetes/deadline_test.go)、[结果与限制](../sandbox-center/backend-selection.md#新增实测原生-kubernetes-固定截止)。

## 配置启用不等于能力生效

固定 execd 镜像启用 hardening 和 Landlock 后，drop ALL 配置仍报告 `available=false`，cap_drop 为 degraded。增加 namespace 所需权限后才实际创建隔离会话，删除会话能够停止脱离进程并保留 `rw` 工作区。必须检查 capabilities 和真实行为；不能把 TOML enabled 或能力自报当成完整安全验收。额外 SYS_ADMIN 与 unconfined 设置只属于该机制实验，不直接成为生产 Hardened 配置。

证据：[隔离会话探针](../sandbox-center/probes/isolated/session_test.go)、[配置与结果](../sandbox-center/backend-selection.md#新增实测execd-隔离会话)。

## 同网络 namespace 的控制端口不能只靠代理隐藏

适用范围：将 root execd 与不可信用户进程放在同一网络 namespace，并准备在前面增加 Guard 的部署。固定二进制提交 `b7d7697750b45be2ccc876da645c8880a0cf69fc` 的 [路由源码](https://github.com/opensandbox-group/OpenSandbox/blob/b7d7697750b45be2ccc876da645c8880a0cf69fc/components/execd/pkg/web/router.go#L174) 明确让 `/internal/init` 跳过 token 验证。只给代理鉴权不能阻止用户进程直连内部端口。

源码检查后，已用固定提交加 UDS 补丁、Guard 入口白名单与非 root 身份做了[容器验证](../sandbox-center/guard-execd-verification.md)：用户访问两个控制 socket 被拒、TCP 控制口未监听、身份注入被拒，文件接口不能覆盖私有启动标记。该结果覆盖探针控制边界，完整续租、回执及文件路径仍按 [接入规格](../sandbox-center/runtime-integration-plan.md) 验收。检查新版本时应同时核对 listener、初始化路由、命令身份和文件接口，而非只核对公开 API token。

## 整实例故障注入不能只看注入客户端退出码

适用范围：真正 PID 1 Guard 在 execd 崩溃时主动退出的独占 PID namespace。容器内的 Python 发出 SIGKILL 后，也可能被 namespace 终止，因此 docker exec 返回 137 不等于信号没有发出。验证应限定允许的注入返回状态，并检查 Guard 记录的 execd 被杀原因、容器 exit 1、独立卷心跳停止和文件保留。不能泛化成忽略所有注入错误。

证据：[execd_crash_stops_detached_writer](../sandbox-center/probes/guard-execd/integration_test.go)。探针的 PID 1 尚未实现完整孤儿回收，不能把故障停止通过等同于长时间运行进程管理已完成。

## Kubernetes 内不能从容器内部杀死 Guard 来验证整实例停止

适用范围：用 `kubectl exec` 向沙箱 Pod 的 PID 1 发送信号，试图模拟 Guard 崩溃。Linux 对 PID namespace init 有特殊处理，从同一 namespace 内发送 SIGKILL 不会终止 Guard；实测 Pod 保持 Running。整实例停止要在容器运行时之外（Docker 探针的 `docker kill`）或通过删除 Pod/Kubernetes API 完成，不能把容器内 `kill 1` 当作失败证据。Guard 的硬期限上限由 `serve SECONDS` 控制；阶段 2 放宽到 86400 秒，但只有 20 秒到期的停止行为经过实测，长期限没有新证据。

证据：[Guard 探针](../../docs/sandbox-center/probes/guard-execd/main_linux.go)、[阶段 2 验证记录](../sandbox-center/phase2-verification.md)。

## 沙箱续租需要使用中心确认的到期时间

适用范围：Center 将沙箱租约钳制到 Run 当前领取期限。Worker 即使请求 300 秒，默认 30 秒的 Run 租约也会缩短实际期限；按请求期限计算出的 100 秒续租间隔会让沙箱先过期。Worker 按返回的 `hardExpiresAt` 调度续租，并用当前期限限制请求时间。真实 Kubernetes 测试跨过初始 30 秒租约，续租 revision 从 1 增至 5；详见[阶段 4 验证](../sandbox-center/phase4-lease-verification.md)。

## 租约能续不代表单条命令能一直等待

适用范围：当前 Guard + execd 的同步命令链。真实 Kubernetes 测试中，沙箱续租并获 Guard 确认后，`sleep 22` 仍在约 15 秒返回 Unknown；Guard 的 execd HTTP 客户端设置为 15 秒，服务写超时为 20 秒，命令请求配置了 10 秒 timeout。排查长命令须同时核对各层截止时间和原操作回执，不能把 Unknown 当作未执行后重跑。续租窗口和本次试验见[阶段 4 验证](../sandbox-center/phase4-lease-verification.md)。

## LayerFS 挂载要核对底层文件系统

适用范围：本机 OrbStack 内核及本轮 OverlayFS 参数。节点某处 `/var/lib` 路径本身位于 overlay 上，作为 upperdir 时挂载失败；使用 `/var/lib/rancher/k3s/storage/agentic-layerfs-probes` 下的 btrfs 后通过。检查 `stat -f`、`mountinfo`、xattr 与真实挂载，不根据 hostPath 前缀判断可用。挂载权限留在独立 materializer，非特权 consumer 只接收自己合并视图的传播。

## 原 upper 重挂载不能证明检查点恢复

适用范围：验证 LayerFS `Checkpointed` 文件恢复。只删除 Pod 再挂载原 upper，只能证明文件还在原节点。检查点探针必须先保存独立的合并视图基准，打包 upper 为 OCI 增量，再删除原 upper/work，在新目录恢复并挂载。删除项、opaque 目录、hardlink、软链接、权限及 xattr 都要比较；同时确认基础层与并行实例未变化。

实际 upper 还有 uuid、origin、impure 等内核控制属性，不能笼统回放；未知 overlay 属性应明确失败。中文和长路径 marker 会产生 PAX `path`，校验不能要求 PAX 全空。扫描错误也必须终止保存，`os.walk` 默认忽略错误可能导致缺文件的成功归档。

证据：[挂载与新 upper 恢复记录](../sandbox-center/layerfs-verification.md)、[真实 Kubernetes 用例](../sandbox-center/probes/kubernetes/layerfs_test.go)、[归档负例及故障注入](../sandbox-center/probes/kubernetes/testdata/layerfs_checkpoint_test.py)。这些是同一健康节点的 `CrashConsistent` 机制证据，未覆盖 Center 自动保存、应用一致性或跨节点恢复。

## 停止实例不能靠 emptyDir 保留待保存文件

适用范围：ADR-0004 必要保存失败后停止用户进程，并等待稍后保存或授权处置。Pod API 对象保留不代表 emptyDir 保留，kubelet 在运行时终止后会卸载并清理该卷。RequiredSave 工作区使用没有 Pod ownerReference 的独立 PVC；保存解阻塞前不能删除 PVC。

停止请求确认也不能单独作为冻结证据。当前 Guard 绑定 Pod UID 关闭准入并退出 PID 1，Center 观察原实例容器终止后才记录停止。创建结果的 UID 应在等待 Ready 和激活前登记，否则等待超时可能留下找不到原实例的阻塞记录；对账补齐身份时必须验证标签和已有 UID。

证据：[Center 保留与停止验证](../sandbox-center/saveblocked-verification.md)、[真实保留与激活前恢复用例](../../internal/sandboxcenter/required_save_e2e_test.go)。实测覆盖独立非 root Pod 读回 PVC、停止后心跳稳定及保存确认后的回收，不包含真实检查点上传。

## LayerFS 保存恢复需要的最小能力

适用范围：Saver 或恢复器以 root 运行、但按沙箱基线丢弃全部能力时。沙箱初始化把 `/workspace` 交给工作负载身份 65534、模式 0750；root 没有 `DAC_OVERRIDE` 时连目录遍历都会被拒绝，`os.walk` 报 `PermissionError`。恢复侧 `os.chown`/`os.chmod` 需要 `CHOWN`/`FOWNER`。正确做法是只加回这三个能力，保持 `drop: ALL` 基线。

证据：[LayerFS 保存验证](../sandbox-center/layerfs-save-verification.md)、[端到端用例](../../internal/sandboxcenter/saver_e2e_test.go)。

## 停止记录仍需调度保存，确认后仍需清理

适用范围：SaveBlocked 接入自动 Saver 后。旧查询只选 `stopped_at IS NULL`，停止证据一旦写入，后续 Sweep 就无法创建或轮询 Saver。查询必须继续包含已停止的 SaveBlocked，物理删除仍由保存确认保护；进入对账队列不代表允许删除工作区。

保存确认会先提交 Releasing，随后才删除 Saver。若中间崩溃，后续 Releasing 对账及同步 Release 必须先清理 Saver，再删原 Pod/PVC。多实例下旧 Save 快照可能迟到；同一沙箱的 Save 轮次用数据库 try-lock 串行推进，并在锁内重读状态，防止终态后重建 Saver。

证据：`TestPendingReconciliationIncludesStoppedSave`、`TestSaveWaitsForConcurrentSaver` 与 `TestCheckpointRestoreHTTPE2E` 的迟到保存重放和资源清理断言。

## 恢复必须在用户进程启动前完成

适用范围：从检查点创建新沙箱。独立恢复 Pod 成功后立即删除，重试申请可能再次复制文件并覆盖修改。当前恢复作为沙箱 init container，先在新 PVC 内完成校验和原子重命名，再启动 Guard。恢复侧和 Saver 挂载同一个 workspace 子目录，避免下次保存把目录多嵌套一层。

初始化失败或取消后，未激活实例没有用户写入，清理须先关闭申请状态、删除 Pod，再删除 PVC；不能把半恢复目录作为新检查点发布。已激活实例仍遵守正常必要保存流程。

证据：[HTTP 恢复测试](../../internal/sandboxcenter/restore_e2e_test.go)、[恢复授权与挂载测试](../../internal/sandboxcenter/restore_admission_test.go)。
