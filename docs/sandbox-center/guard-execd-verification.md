# Guard + execd 控制边界验证

日期：2026-10-22。状态：**阶段 1a 通过；完整受保护 Runtime 尚未完成**。

在固定 OpenSandbox 提交上增加 UDS listener，并用真正的 root PID 1 Guard 管理 execd。真实 Linux arm64 容器验证了用户无法直连控制入口、命令和文件按固定非 root 身份运行，以及期限到达或保护进程崩溃后 detached writer 停止。该结果支持继续实施当前选型。

## 已执行检查

| 检查 | 结果与观察 |
| --- | --- |
| UDS listener Linux root 单测 | 全部通过，容器 exit 0；含 HTTP 往返、0600、低权限连接拒绝、非法路径/不可信祖先/符号链接拒绝、已有路径保留、关闭清理及替换文件保留 |
| flag 单测与构建 | flag 单测通过；patched execd 与 Guard 的 Linux arm64 二进制构建通过 |
| 静态与竞态检查 | execd listener/flag/main 的 go vet、Linux Guard go vet 通过；宿主 listener/flag 的 go test -race 通过，需 root 的用例在宿主跳过，另由上述 Linux 单测覆盖 |
| 补丁与平台分支 | 子代理在原始 tarball 上 apply --check 通过；主代理在实际构建源码上反向 apply --check 通过；listener 的 Windows amd64 编译通过 |
| protected_control_and_identity | Guard 为 PID 1；用户 UID/GID 65534、CapEff=0、NoNewPrivs=1；两个 socket 返回 EACCES；用户不能 SIGSTOP Guard、读写私有状态；root 探测 TCP 44772 被拒；Guard init 入口 404，身份/环境等注入字段 400 |
| 文件身份与保护状态 | 正常上传成功，文件属主为 65534:65534；用户把目标改为指向 boot 标记的 symlink 后，上传失败，标记逐字节不变 |
| expiry_stops_detached_writer_restart_stays_closed | 20 秒有限期限到达后 exit 0；没有早于期限 1 秒以上退出；心跳文件在退出后 1.2 秒窗口内不变，独立卷保留；重启 closed=true，命令返回 409 |
| guard_crash_stops_detached_writer | SIGKILL Guard 后容器停止，脱离会话写入器不再写入，文件保留 |
| execd_crash_stops_detached_writer | SIGKILL execd 后 Guard 记录原因并 exit 1；容器停止，脱离会话写入器不再写入，文件保留 |

集成测试最终返回 `5 passed`，对应 1 个父测试与 4 个子场景。没有将无 Docker 条件下的 Skip 当作通过。故障注入客户端可能随 namespace 一起被杀，测试只允许 exit 0 或 137，并独立要求 Guard 的 execd 被杀日志、容器 exit 1 与停止写入证据。

构建与权限详情、固定基底 digest、命令及清理方式见 [探针 README](probes/guard-execd/README.md)。本次构建身份：

| 产物 | SHA-256 |
| --- | --- |
| execd-uds.patch | `23b17aa3b0afdc23a05abf9a71dc2d14d5117febd116cca6fd9e93a6caab95da` |
| patched execd Linux arm64 | `e462c1c267583ff45b371df71cc8b93cc5f25cd7aa149b731d5a52af07d630eb` |
| Guard Linux arm64 | `711aeb5b2b474cc0afde677064c36e95092bd64dc55e8212c76be2b9f22593f2` |

这些散列标识此次构建；未设置可复现构建参数，不要求另一目录构建产生相同二进制散列。

## 结论的范围

本轮通过的是单机 Docker 中的控制边界与整实例停止。没有部署 Sandbox Center，没有替换业务 AIO Backend，也没有在 Kubernetes 上联合运行这份补丁和 Guard。此前独立的 Kubernetes PVC/Pod deadline 实验不能替代该联合验收。

文件测试覆盖固定路径、降权写入和受保护状态访问；没有证明任意 workspace 路径、并发替换、在途上传或跨目录访问均已受限。用户补充组实测不超出 65534，不等于上游在任意镜像都清空额外组。

到期测试以声明期限检查提前停止，观察退出后 1.2 秒心跳稳定；该窗口和 Docker wait 的返回时间均不是最大停止延迟 SLA。没有测量续租、fsync 延迟分位数、容量、节点冻结、网络分区或存储损坏。CPU/内存参数是测试资源限制，不是实测资源消耗。

## 下一步

阶段 1b 先实现 Guard 的可续租准入：完整绑定、单调 deadline、revision 与到期关闭串行决定、接受事实读回、重启不复活，补齐 PID 1 孤儿回收与身份组约束。然后实现受保护的 Pending/DispatchStarted/终态回执与 Unknown 对账，并补完整文件路径、mTLS、在途操作停止和故障测试。通过这些条件后再进入最小 Center 与 API/worker 接管。

本轮由主代理集成并运行容器验证；2 个子代理分别交付 UDS 补丁、集成测试与只读审查，均已完成。未提交 Git。
