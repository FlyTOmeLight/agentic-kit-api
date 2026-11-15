# LayerFS 挂载与 OCI 增量恢复验证

日期：2026-11-09。状态：节点挂载与新 upper 恢复的机制探针已通过；尚未接入 Center 生命周期。

本轮验证 [ADR-0005](sandbox-design-contracts/docs/adr/0005-layerfs-implementation-decisions.md) 的两个前提：挂载权限留在节点侧，工作负载仅使用合并视图；检查点必须保留删除和 opaque 目录语义，能够脱离原可写层恢复。

## 环境

| 项目 | 本轮配置 |
| --- | --- |
| 集群 | 显式固定 `orbstack` context，节点 `orbstack`，linux/arm64 |
| Kubernetes / 内核 | `v1.36.5+orb1` / `7.2.12-orbstack-00394-gbecdd6130068` |
| 基础层 | 64 MiB `local-path` PVC，写入 fixture 后在 materializer 中只读挂载 |
| upper / work / exports | `/var/lib/rancher/k3s/storage/agentic-layerfs-probes/<唯一 namespace>`，底层 btrfs `/dev/vdb1` |
| 固定镜像 | `opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b`，`imagePullPolicy: Never` |
| 挂载参数 | `index=off,metacopy=off,redirect_dir=off` |
| materializer | 独立特权 Pod，节点挂载 `Bidirectional` |
| consumer | UID/GID 65534、drop ALL、禁提权、只读根文件系统、默认 seccomp、无 ServiceAccount token；只挂载自身 exports，使用 `HostToContainer` |

探针 consumer 没有运行 Guard/execd，验证的是同等工作负载权限下的文件视图及挂载传播。正式节点 DaemonSet、Guard 的挂载事实检查与目标 RuntimeClass 需要另外接入和验收。

## 已验证行为

| 检查 | 实际结果 |
| --- | --- |
| PVC 基础层只读 | materializer 对 `/base` 写入失败，错误为 `EROFS` |
| 动态传播 | consumer 先启动，节点随后挂载；consumer 能看到 overlay 合并视图 |
| 权限与目录边界 | UID 65534，`CapEff=0`、`NoNewPrivs=1`、`Seccomp=2`；不能经软链接读取私有 upper，不能访问基础层路径 |
| 实例隔离 | alpha 修改后，基础层与 beta 的摘要不变 |
| 原 upper 重挂载 | 删除并重建 alpha consumer、卸载并重挂 overlay，文件状态保留 |
| 增量语义 | 原生 whiteout 转为 `.wh.<name>`，opaque 转为 `.wh..wh..opq`；tar 不携带内核控制 xattr 或设备节点 |
| 增量范围 | 基础层中未修改的 1 MiB 文件不进入归档，归档大小小于该文件 |
| 摘要与绑定 | manifest 和 layer 分别按 SHA-256 寻址；恢复前校验二者摘要、layer 字节数及调用方提供的基础层 digest |
| 脱离原可写层 | 打包后删除 alpha 原 upper/work，恢复到 `private/restored/upper`，再创建 consumer；合并视图摘要与保存前相同 |
| 文件恢复 | 普通/二进制文件、删除、opaque、文件与目录互换、中文及长路径、mode、uid/gid、软链接、upper 内 hardlink 和 UTF-8 用户 xattr 保留 |
| hardlink 实际行为 | 恢复后修改一个别名，另一个别名同步变化 |
| 失败关闭 | 摘要损坏、基础层不符、危险路径、越界链接、whiteout 冲突、目录扫描错误、超限均失败；恢复失败不暴露目标目录，清理 staging |

比较摘要覆盖内容、文件类型、mode、uid/gid、软链接目标、hardlink 分组及普通 xattr，也包含工作区根目录元数据；mtime 用单独的文件恢复测试检查，不计入合并视图摘要。

检查点 manifest 记录基础层 digest、增量层 media type/digest/size、覆盖范围、显式 `CrashConsistent`、生成时间及工具版本。它是探针自己的 manifest，增量 tar 采用 OCI layer 语义；没有宣称生成完整 OCI image，也没有对第三方镜像工具做互操作验收。

最终一轮 `TestLayeredWorkspaceMounts` 通过，主体耗时 50.18 秒，包含资源准备和清理；Linux 镜像内 15 项归档回归通过。增量 tar 为 40,960 字节，基础层未修改大文件为 1,048,576 字节。单次打包加 kubectl 调用耗时 165.60 ms，恢复、挂载并等待新 Pod Ready 耗时 1,254.83 ms。这些是镜像已缓存的本机单样本观测，不是生产延迟、吞吐或容量承诺；CPU、内存和并发开销未测量。

该轮 manifest 为 `sha256:6039f8dd40dd5beea0973d6f71f9330d0d565445c116e2d0f9f6ed3dd9c60754`，layer 为 `sha256:055bdb9bfe971fd592f73fecb429ac7989da5791b122aa2feb5f520b6b1dd5b3`。新 upper 挂载后的工作区摘要与保存前同为 `sha256:ec8ac715094118c563d1359cb46bcfc17afc9f924c3991cc37acb60869236db7`。fixture 的时间、文件元数据会影响对象摘要，不要求不同测试轮次产生相同值。

## 本轮遇到的问题

### upper 必须位于支持的底层文件系统

首次在节点另一处 `/var/lib` 路径创建 upper 时，挂载报：

```text
fsconfig() failed: overlay: filesystem on .../upper not supported as upperdir
```

该路径底层是 overlay。改用 K3s local-path 存储目录下的 btrfs 后挂载通过，whiteout 与 `trusted.overlay.opaque` 均可读取。选择路径必须核实底层文件系统与 xattr 支持；路径前缀本身不能证明兼容。此处记录当前内核与配置的实测限制，不扩大为所有 OverlayFS 叠加组合的通则。

### 内核控制 xattr 不能直接作为文件属性回放

实测 upper 根目录带有 `trusted.overlay.uuid`，目录中还有 `trusted.overlay.impure`。对照 Linux `v6.18` 的 `ovl_init_uuid_xattr`，uuid 表示 overlay 实例身份；恢复到新实例不沿用原值。探针将已识别的 uuid、origin、impure 排除出归档，将 whiteout/opaque 转为标准 marker；未知 overlay 属性仍拒绝，避免把 metacopy 或 redirect 的依赖丢掉。

Linux 源码只用于解释机制，不作为正在运行的 OrbStack 内核源码身份。新 upper 的实际挂载和合并视图比较才是本轮恢复证据。

### whiteout 的 PAX path 不能被当成异常属性

中文名称及长路径会让 Python tarfile 自动生成 PAX `path`。whiteout 校验若要求 PAX 字段全空，会出现“打包成功、自己的恢复器拒绝”。修正为允许 marker 的路径表示字段，继续拒绝数据、xattr 与异常类型，并增加真实 tar 序列化及挂载恢复回归。

### 扫描失败必须终止保存

`os.walk` 默认忽略扫描错误。探针显式将扫描错误抛出，防止目录部分不可读时仍发布缺文件的 manifest；打包和比较摘要共用的遍历都受该规则约束。故障注入验证扫描失败时不发布对象。

## 适用边界与下一步

探针输入是停止 consumer 并卸载后、仅供 materializer 操作的 upper；store 与 staging 的父目录也由它独占。它不是不可信用户可直接调用的归档服务。工具只接受本轮明确覆盖的文件类型，拒绝 `.wh.*` 用户文件、FIFO/socket/普通设备、setuid/setgid、不支持的安全或 overlay xattr，以及非 UTF-8 的用户 xattr 值。

探针限制单份归档及展开数据不超过 64 MiB、条目不超过 4,096、manifest 不超过 64 KiB。这些值用于约束实验资源，不是正式工作区配额。

基础层在本轮以文件树摘要校准；正式实现仍须对接不可变 `BaseLayerRef` 的内容身份，并由 materializer 验证实际基础层，而非相信调用方传入的字符串。此次使用同一基础层 PVC 和同一健康节点，未验证重新物化基础层或跨节点恢复。

manifest 与 tar 保存在本次独占节点目录，使用 rename 完成可见性切换；尚无对象存储发布、fsync/断电持久性、内容寻址对象 GC 或恢复授权协议。测试正常完成后归档也随 fixture 一并删除。

关闭消费者与卸载只是本轮 fixture 的停写手段。尚未收集 ADR-0005 要求的准入屏障、在途操作排空、cgroup、可写 FD、syncfs 及挂载事实固定证据，因此不宣称 `ApplicationConsistent`。

后续先把“必要保存意图 → 停写并保留工作区 → 发布检查点 → 确认保存 → 解除删除阻塞”接入 Center，使保存失败进入 SaveBlocked，普通释放、到期和对账清理都不能删除唯一待保存副本。再接节点 materializer 的真实编排与 `LayeredWorkspace` 能力协商。当前 `/workspace` 仍是 emptyDir，生产 Profile 尚未完成。

## 复现与证据

- [探针与运行命令](probes/kubernetes/README.md#layerfs-挂载与增量恢复)
- [Kubernetes 用例](probes/kubernetes/layerfs_test.go)
- [打包与恢复工具](probes/kubernetes/testdata/layerfs_checkpoint.py)
- [归档回归测试](probes/kubernetes/testdata/layerfs_checkpoint_test.py)
- [OCI image-spec v1.1.1 layer](https://github.com/opencontainers/image-spec/blob/v1.1.1/layer.md)
- [Linux v6.18 overlayfs uuid 与 xattr](https://github.com/torvalds/linux/blob/v6.18/fs/overlayfs/util.c)
