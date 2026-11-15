---
status: accepted
---

# 确定 layerfs 首版落地方式

layerfs 只服务 `EphemeralCopy + Checkpointed`。`PersistentVolume` 继续直接挂载授权卷，不引入叠加层。以下六项为已确认的首版决策；变更需要新的 ADR，不能由实现细节悄悄改写。

## 挂载权限放在节点侧

叠加挂载由节点侧 Workspace Materializer 完成，Guard 保持 `SETUID/SETGID`，不新增 `CAP_SYS_ADMIN`。首版采用受控 DaemonSet：按 digest 准备节点只读基础层与实例可写层，组装 overlay 后经 `hostPath` 和 `mountPropagation` 暴露给 Pod。Guard 只校验 digest、只读性和挂载事实，不执行 mount syscall。

目标 RuntimeClass 落地前必须跑能力探针，确认 overlay 支持、`mountPropagation` 行为和 hostPath 策略；配置项打开不等于能力可用。若托管集群禁止节点侧挂载路径，退路是 Pod 内 `fuse-overlayfs`，仍需单独验证性能与 whiteout 语义。Pod 内内核 overlayfs 加 `CAP_SYS_ADMIN` 不作为首版方案。

## 基础层以只读 PVC 为真源

每个基础层按内容 digest 对应一个只读 PVC，由 Layer Materializer 从对象存储或仓库快照生成。节点侧把该 PVC 以只读方式挂到稳定缓存路径，同节点同 digest 的实例共享同一 lower；跨节点通过重新挂载 PVC 获得同一内容。

镜像卷和节点本地缓存是后续优化，必须复用同一 `BaseLayerRef` 契约并单独验收。基础层物化与挂载都记录为 Preparing 的独立阶段；digest 不符或只读校验失败时不得进入 Ready。

## 检查点使用 OCI layer 风格

检查点对象由 manifest 和 upper 增量层组成。manifest 记录基础层 digest、增量层 digest、覆盖范围、一致性级别、生成时间和工具版本；增量层使用 OCI layer 兼容的 tar 结构表达新增、修改、删除和 opaque 目录，不把 overlay 语义压平成普通 tar。

恢复时先物化新的只读基础层，再把增量层应用到新的可写层，不依赖原 Pod、原节点或原可写层。增量层和 manifest 都按内容寻址存储并校验 digest。

## 静止证明采用固定证据集合

`ApplicationConsistent` 检查点至少要求以下证据同时成立：

- 准入屏障已关闭该租约的新写入与新输入；
- 已受理的文件操作和命令全部排空；
- 沙箱 cgroup 中除 Guard/execd 固定集合外没有用户进程；
- 授权根目录下没有仍可写的文件描述符或在途写入；
- 对合并视图执行 `syncfs` 成功；
- 检查点期间基础层 digest 与挂载事实没有变化。

任一项无法确认时检查点明确失败。只有调用方在请求中显式声明 `CrashConsistent` 且 Profile 允许时，才走崩溃一致路径；不允许服务端静默降级。

## WarmPool 首版不按基础层 digest 分池

首版公共 WarmPool 只预热空工作区，认领后才物化基础层，因此 `T_ready` 包含基础层就位时间。`poolKey` 暂不加入基础层 digest，避免公共库存按仓库版本碎片化。

只有高频基准被证明值得专属库存时，才为少量 Profile 增加带 digest 的池键和预物化策略。池键格式变化必须版本化，新旧池不得互相认领。

## 公开 `LayeredWorkspace` 能力

公开 SDK 增加 `LayeredWorkspace` capability。Profile 声明非空 `LayeredWorkspace.Engine` 时必须同时声明该能力，实例就绪时还要核对实际挂载事实。调用方据此判断恢复耗时来源，并避免把未验证的分层工作区当成普通目录。
