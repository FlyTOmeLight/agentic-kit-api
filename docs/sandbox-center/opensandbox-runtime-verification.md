# OpenSandbox Runtime 实测

日期：2026-10-22。**OpenSandbox 的 Docker backend 能在管理服务正常时到期销毁整环境并保留独立工作区，但换成它不能直接解决本项目的本地到期停写与执行恢复要求。** 本轮通过真实容器与 HTTP 请求注入故障，不把源码推断当作运行结论。

## 被测对象

固定官方 `release-1.1.1` [发布清单](https://raw.githubusercontent.com/opensandbox-group/OpenSandbox/release-1.1.1/docs/releases/1.1.1.yaml) 中的镜像：

| 组件 | Digest |
| --- | --- |
| server | `sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b` |
| execd | `sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded` |
| 只读卷观察器 BusyBox | `sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e` |

server 镜像内部 Python 包版本实际显示 `0.1.0.dev0`，与发布名不同。实验身份由上表 digest 固定，不宣称包版本就是 1.1.1，也不凭发布 tag 推定镜像精确源码提交。

Docker 29.8.2 / OrbStack，linux/arm64，Go 1.27.3。管理服务使用 Docker backend、SQLite 和独立 bridge 网络，HTTP 仅发布到 `127.0.0.1`；管理容器挂载 Docker socket。该网络不是 internal 网络，本实验不构成网络隔离验收。启动前确认没有已有 OpenSandbox 标签容器，避免上游恢复逻辑处理其他实例。

工作负载使用同一 server 镜像中的 Python/bash，入口为 `sleep infinity`，由官方服务注入 execd。每个用例使用独立 named volume，设置 `deleteOnSandboxTermination=false`。管理容器为 1 CPU / 512 MiB / 128 PID，工作负载为 1 CPU / 256 MiB / 128 PID。未配置 gVisor、Kata 或 Hardened 隔离。

探针及完整复现：[runtime_test.go](probes/opensandbox/runtime_test.go)、[README](probes/opensandbox/README.md)。

## 实测结果

以下数据来自修正流解析、最小 TTL 与重启端口刷新后的完整运行，测试主体耗时 151.62 秒，退出码 1。失败断言按本项目要求保留，不改写成“预期失败所以通过”。

| 场景 | 实际观察 | 本项目要求是否满足 |
| --- | --- | --- |
| kill 普通子进程 | 命令终态后，心跳 14→14 字节 | 此用例满足 |
| kill 脱离进程组的子进程 | 命令已 `running=false`，心跳 19→79 字节 | 不满足，仍在写入 |
| 3 秒 timeout，普通子进程 | 命令终态后，心跳 149→149 字节 | 此用例满足 |
| 3 秒 timeout，脱离进程组的子进程 | 命令已 `running=false`，心跳 154→219 字节 | 不满足，仍在写入 |
| 管理服务在线，环境 TTL=60 秒 | 截止后 352.697 ms 观察到容器删除；心跳稳定在 2939 字节，独立卷仍可读 | 此用例满足整环境停止与保留文件 |
| 管理服务被 SIGKILL，超过环境截止时间 | 管理容器退出码 137；超过截止后心跳 3004→3074 字节；新命令 HTTP 200，返回执行 ID | 不满足本地到期停写及关闭执行入口 |
| 管理服务恢复 | 重新读取动态端口后健康检查成功，已过期工作负载被删除，心跳停止且文件保留 | 恢复后能补回收；不能抹去过期后继续执行的窗口 |
| 已完成命令，重启工作负载容器及 execd | 重启前退出码 0 且日志可读；重启后原 ID 状态 HTTP 404、日志 HTTP 400，均为 `command not found`；卷中回执仍为 `once` | 不满足重启后的执行结果查询 |
| 接受命令后丢失响应，使用相同键重发 | 首次已写文件，客户端 `context canceled`；按调用方键查询 HTTP 404；相同请求和 `Idempotency-Key` 再次提交，副作用计数为 2 | 未提供本项目所需的稳定执行身份与去重 |

普通子进程停止的结论限于本用例观察窗口，不能推广为完整进程树清理保证。脱离进程两次非零心跳增长则直接证明该窗口内仍在写入。最后一项上游并未承诺支持 `Idempotency-Key`，属于能力缺口而非接口违约。

完整运行之后，命令停止断言补强为显式报告 Docker/文件读取错误，避免把读取失败误报为仍在写入；其余用例没有改动。该小范围用例单独复跑，普通子进程仍通过；脱离进程 kill 为 19→79 字节、timeout 为 154→214 字节，仍失败。`go vet` 与 gofmt 检查通过；未运行无关业务集成测试。测试容器、网络和工作区卷已清理，并用 Docker 列表复核无本轮资源残留；镜像保留在缓存中。

## 如何影响选型

**不能以“换成 OpenSandbox”代替补齐执行保护。** 命令管理、整环境生命周期和 Worker 接管是不同层面的能力：一个服务能够创建/删除容器，不表示它失联后，容器内部还会按租约截止时间停止用户代码。

建议保留当前设计的 Provider / Runtime Adapter 分工：

1. 普通命令、文件和 CLI 任务可继续评估 execd 作为较集中的数据面；这是职责适配建议，不是已验证生产 Profile。
2. 需要浏览器、桌面或更多开发工具时，AIO 仍有复用价值。本轮结果不足以支持全面替换 AIO。
3. 两种候选都需要在本项目接管边界补齐：控制面失联后的本地停写、所有用户写入入口受同一租约约束，以及稳定操作 ID 和可恢复回执。中心先存派发意图；结果不确定时保留 Unknown，不能因 404 自动重放。
4. 如果采用完整 OpenSandbox 管理服务，Provider 应适配其生命周期 API，由它独占底层资源管理；如果只采用 execd，则由现有设计中的 Kubernetes Provider 管理资源。避免两个控制器同时拥有同一个实例。

下一步应把已有 [supervisor 机制原型](supervisor-verification.md) 与一个实际数据面组合，在目标 Kubernetes 环境验证完整租约边界；先完成命令/文件场景，再扩大到浏览器等写入入口。本次没有把原型变成正式实现，也没有修改业务 API/worker。

## 解释与限制

- [固定版本 Docker 生命周期实现](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.1.1/server/opensandbox_server/services/docker/docker_service.py) 使用管理进程内的定时器调用 Docker kill/remove，并在启动时补处理已到期环境。实验区分“管理服务正常时回收”和“管理服务不在时本地执行截止”，不将二者等同。
- [固定执行协议](https://raw.githubusercontent.com/opensandbox-group/OpenSandbox/release-1.1.1/specs/execd-api.yaml) 未提供调用方执行 ID 或幂等键契约；其结果保留时间不能自动解释成进程重启后的持久性。探针验证的是本项目所需能力，不将上游未承诺能力的缺失认定为违约。
- 重启实验重启的是整个工作负载容器及其 execd；工作区独立卷保留。没有单独重启 execd，也没有测试节点/磁盘丢失。
- 仅验证此 Docker 组合及默认 execd 启动方式，不外推到 OpenSandbox Kubernetes/FastSandbox 或其他配置；没有实测 E2B、Daytona。
- 未覆盖租约续期与 epoch、网络分区、文件 API/PTY/浏览器入口、运行中 execd 崩溃和并发接管。因此不能将 `EnforcesLeaseDeadline` 或完整接管 Profile 标为已支持。

## 探针校准

初始运行的流事件解析、TTL 低于 60 秒以及重启后沿用旧动态端口导致过探针失败。已修正这些问题；它们不作为后端缺口证据。响应丢失用例必须先看到文件副作用，再检查客户端确实收到 `context.Canceled`。相关经验见 [故障探针经验](../lessons/sandbox-runtime-probes.md)。
