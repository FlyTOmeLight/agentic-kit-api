# Supervisor 最小机制验证

日期：2026-10-22。**整租约使用独立 PID namespace、到期终止全部进程并保留独立卷，这条最小路径已通过本地实测。** 稳定操作 ID 的回执也能跨 supervisor 重启查询。结论属于实验原型，不代表 AIO 已修复或 Sandbox Center 已具备生产能力。

代码与复现入口：[supervisor-prototype](probes/supervisor-prototype/README.md)。上一阶段的 [AIO 失败记录](aio-runtime-verification.md) 继续有效。

## 实测组合

- Go 1.27.3，交叉编译 Linux arm64 静态二进制；Docker 29.8.2 / OrbStack。
- 固定 BusyBox digest：`sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e`。本轮使用最小执行环境，没有复用 AIO 作为运行镜像。
- supervisor 为真正的 root PID 1，无 `--init`、共享 PID namespace 或 shell 包装。
- 执行容器没有网络、主机挂载或 Docker socket；只读根文件系统、no-new-privileges、drop all capabilities 后仅给 supervisor SETUID/SETGID。用户子进程的 CapEff 实测为 0，UID/GID 65534，没有额外组。
- 独立工作区卷与 root-only 回执卷；1 CPU、128 MiB 内存、64 PID 配额。Docker 内 cgroup 挂载为只读，本轮不创建容器内 cgroup 子树。

## 验证结果

| 场景 | 观察与断言 | 结果 |
| --- | --- | --- |
| 保护 supervisor 与回执 | 用户不能 STOP PID 1、读取 lease、创建回执；查询已经存在的操作时，socket 连接明确返回 permission denied | 通过 |
| 重复请求 | 首次受理后不使用响应，通过原 ID 查结果；相同 ID/脚本不追加第二次副作用，不同脚本返回 409 | 通过 |
| 脱离进程的本地到期 | setsid 后再派生子进程，父进程退出；无控制请求/续期期间，15 秒期限到达，容器自行退出码 0 | 通过 |
| 停写与文件保留 | 确认容器已退出，再独立只读挂载卷，两次读取相隔 1.2 秒，心跳不增长、原文件可读 | 通过 |
| 完成结果跨重启 | 原 ID 的 Exited、退出码和输出完整相同；重复副作用文件仍只有一次 | 通过 |
| 旧租约不能复活 | 到期后启动原容器，Bind、原 Start 重试、新 Start 均返回 409 | 通过 |
| supervisor 在运行中崩溃 | SIGKILL PID 1 后容器退出码 137，writer 停止；期限尚未到达时重启，原记录为 Unknown，拒绝重新 Bind/Start | 通过 |
| 输出证据错误路径 | 日志写入失败后即使 fsync 成功仍保留错误；刚好达到输出上限不算截断，超额内容被排空并标记 | 通过 |

最终 Go 集成用例与两个 Linux 输出单元用例通过，Linux 目标 `go vet` 通过。测试产生的专用容器、卷、临时镜像均由用例清理，本地二进制与缓存随后删除。没有执行全仓业务集成测试，也没有改动现行 API/worker 运行代码。

## 由此确定的实施方向

1. **保留“整租约独立执行边界”作为首版候选。** 停止进程与保存文件可以分开：终止执行容器之后，由单独授权的保存流程只读访问工作区。需要确认原容器已停止才交出工作区写权；Exited 只能证明主命令退出，不能证明文件静止。
2. **Runtime 必须在执行前接受稳定操作 ID。** 受保护执行层先持久化 Pending，再启动，完成时保存退出与输出。响应丢失先查原 ID；崩溃留下的 Unknown 不自动重放。中心只保存 AIO commandID 仍不够。
3. **正式接入先完成全部入口保护。** 本原型没有控制 AIO 的文件、PTY、浏览器或 Jupyter。后续选型已收敛为首版 execd，见 [Runtime 接入规格](runtime-integration-plan.md)；需要受保护控制通道、固定执行身份和本地租约。转发 execd 新增的执行/回执崩溃窗口保留 Unknown，不能沿用直接启动原型的完整结果恢复结论。完成接入验收前，不发布宣称满足完整接管协议的 Profile。

该原型没有续租、持有者 fencing、通用 orphan reaper 和正式存储实现，也没有验证 Kubernetes、Hardened 隔离或主机/存储故障。它验证的是两个底层机制，不能直接搬作生产服务。

## 机制依据

- [Linux PID namespaces](https://man7.org/linux/man-pages/man7/pid_namespaces.7.html)：namespace 的 init 终止会使内核终止其中的进程；因此脱离 session/process group 不能绕过整个 namespace 的终止。
- [Linux fsync](https://man7.org/linux/man-pages/man2/fsync.2.html)：文件 fsync 不代替目录项同步；本原型保存回执后同步父目录。
- [Go os/exec](https://pkg.go.dev/os/exec)：进程退出、输出复制与后代存活是不同事实。原型独立记录输出写入错误，不将非零退出错误当成日志完整的证明。
- [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)：需要保持 supervisor 在线并停止独立执行子树时，可进一步验证受保护 cgroup 子树及 cgroup.kill，本轮未实现。
