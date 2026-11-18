# 原生 Kubernetes 机制探针

## 固定截止

仅操作显式固定的本机 `orbstack` context。创建唯一 namespace、PVC、runtime Pod、只读 reader Pod；结束时删除整个测试 namespace。未安装 sandbox controller，不访问 dev/qa。

前提：本地节点 Ready，已有 `local-path` StorageClass，源码固定的三个镜像已缓存且对节点可见。`imagePullPolicy: Never`，不会临时拉取或替换镜像。需要 kubectl、Go。

```sh
KUBERNETES_DISPOSABLE_PROBE=orbstack GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -v -count=1 -timeout 150s ./docs/sandbox-center/probes/kubernetes
```

普通测试默认跳过。测试中先尝试延长 Pod deadline 并记录 API 结果，再启动 `setsid` writer，等待 kubelet 到期终止，确认终态禁止 exec、删除 Pod 后 PVC 文件仍在且停止增长。实验停止容差为 `[-1s,+5s]`，不是生产 lease SLA。

本轮实际结果见 [选型记录](../../backend-selection.md#新增实测原生-kubernetes-固定截止)。没有覆盖 kubelet 故障、API server/节点网络分区、续租、生产存储或 Hardened RuntimeClass。

## LayerFS 挂载与增量恢复

```sh
KUBERNETES_LAYERFS_PROBE=orbstack GOCACHE=/private/tmp/go-cache \
  GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -v -count=1 -run '^TestLayeredWorkspaceMounts$' -timeout 240s \
  ./docs/sandbox-center/probes/kubernetes
```

仅在显式固定的本机 `orbstack` context 运行，要求名为 `orbstack` 的节点 Ready、`local-path` StorageClass 及源码固定的 workload 镜像已缓存。测试创建唯一 namespace、基础层 PVC、一个特权 materializer Pod 和两个非特权 consumer Pod。不会部署长期 DaemonSet 或变更现有 Center 配置。

materializer 在节点 `/var/lib/rancher/k3s/storage/agentic-layerfs-probes/<本次 namespace>` 下组装 overlay，经 `Bidirectional` / `HostToContainer` 将合并视图动态传播到 consumer。该节点目录底层为已验证的 btrfs；不能换成同节点的任意 `/var/lib` 路径，详见[验证与文件系统限制](../../layerfs-verification.md)。

探针依次检查只读基础层、并行工作区隔离、consumer 权限、whiteout/opaque、原 upper 重挂载，再停止 consumer、卸载 overlay，将 upper 转为 OCI layer 风格 tar 和内容寻址 manifest。打包后删除原 upper/work，只用基础层及归档恢复到新的 upper，重新挂载并比较文件、权限、归属、软链接、hardlink 和 UTF-8 用户 xattr；中文及长路径的删除、opaque 目录也在真实挂载中验证。

`testdata/layerfs_checkpoint.py` 是 Linux 机制探针工具，复用固定镜像中的 Python 标准库，无新增生产依赖。配套 15 项测试在 materializer 内运行，临时目录放在 `/layers` 的 btrfs 上，检查摘要校验、恶意归档、扫描错误及失败清理。macOS Python 没有这组 Linux xattr API，不直接在宿主机运行该工具。

清理先删除 consumer，再确认无嵌套挂载、卸载并清理独占目录，最后删除 materializer、PVC 与 namespace。若 consumer 删除或卸载失败，测试报告失败并保留相关资源，避免递归清理仍在挂载的工作区。

本实验只有同一健康节点、同一基础层 PVC 下的文件恢复证据。归档使用显式 `CrashConsistent` 标记，未发布到对象存储，未完成应用一致性证明、跨节点恢复、Center 自动检查点和 SaveBlocked；通过该测试不等于完成生产 Profile。
