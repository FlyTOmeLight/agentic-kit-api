# AIO 首轮 Runtime 验证

日期：2026-10-22。结论：**当前固定 AIO 镜像不能直接满足 Worker 接管协议的本地到期停写与执行恢复要求。** 基础文件与 Bash 接口可用；脱离进程组的子进程漏停、没有租约级准入、服务重启丢失命令查询，是进入控制面实现前必须处理的缺口。

## 环境和证据

使用本机已有的 `ghcr.io/agent-infra/sandbox:1.11.0`，固定 digest 为 `sha256:6328d7fd2f0ff0b4c147c3d05b3df1ce331f4a482eb6e550ecd64ed1fcf906e7`，`linux/arm64`。容器实际 `/v1/openapi.json` 返回版本 `1.11.0`，包含 `/v1/bash/exec`、`output`、`kill`、文件读写接口。结论绑定此 digest，不推断其他同名 tag 或架构镜像的行为。

Docker 29.8.2 / OrbStack，2 CPU、3 GiB 内存，独立 internal 网络，无主机目录挂载、业务凭据或 Kubernetes 变更。HTTP 客户端和进程观察器运行在容器内，服务重启时测试回执与工作区保留。Go 1.27.3 交叉编译标准库探针，`go vet` 通过；运行结果如下。入口及复现命令见 [探针说明](probes/README.md)，源码见 [aio_runtime_test.go](probes/aio_runtime_test.go)。

## 实测结果

| 场景 | 观察 | 判断 |
| --- | --- | --- |
| SIGKILL，普通子进程 | 父子进程不再运行，心跳文件 5→5 字节，文件 API 可读 | 本用例通过 |
| SIGKILL，脱离进程组的子进程 | API 命令已终态，子进程仍存活，心跳文件 5→65 字节 | 停写失败 |
| hard_timeout=3 秒，普通子进程 | 无 HTTP 轮询期间自行到期；状态 timed_out，父子进程停止，文件 150→150 字节且可读 | 本用例通过 |
| hard_timeout=3 秒，脱离进程组的子进程 | 状态 timed_out，子进程仍存活，文件 205→265 字节 | 停写失败 |
| 命令超时后的入口 | file.write 成功；同 session 新 Exec 返回 completed / still-executable | 无租约级关闭入口 |
| 同步 Exec 已启动后断开 HTTP | 客户端取消后心跳继续；普通父子进程最终按 hard_timeout 停止 | 断连不等于取消；响应丢失不能重发 |
| 完成命令后重启 python-server | 文件回执仍可读；用原 session/command 查询返回 HTTP 404 / Session not found | 结果查询未跨服务重启保存 |

停止用例的两项失败和重启查询失败按原样保留，未将失败条件改成“预期通过”。服务重启由 supervisorctl 执行；首轮 PID 281→3029，补强探针后还核对 PID 与 `/proc` starttime，避免没有重启也通过。`kill` 用例实际 API status 为 completed，进一步说明不能只用状态名证明停止。

## 对实施的影响

1. **需要独立的执行隔离与到期控制。** 用户进程不能控制的 supervisor 必须覆盖所有用户进程及写入入口。只调用 AIO kill 或给单命令传 hard_timeout 不足以覆盖脱离进程和后续写入。下一步应在目标 Kubernetes/Linux 环境验证独立进程归属与强制停止机制（例如独立 cgroup），同时保留工作区；不能把同 UID 的 watchdog 当作可靠边界。
2. **需要可恢复的派发账本与 Runtime 回执。** 中心须在派发前保存稳定操作身份；Runtime 或受保护执行层需要按该身份查询原执行。只把 AIO 返回的 commandID 存入中心，仍无法解决初次响应丢失或 AIO 重启后 404。普通 NotFound 不得转换为“未执行，可以重跑”。
3. **暂不发布此组合为满足接管协议的 Profile。** `RuntimeInfo.EnforcesLeaseDeadline` 不能标记为已验证。现有 AIO 文件和工具能力可继续复用，但完整 Acquire→Execute→Save→Release 验收要等上述缺口补齐。

本轮没有实现 supervisor 或 Sandbox Center，也没有验证数据库并发、运行中 daemon 崩溃、Pod/节点重启、保存发布、认证或 Hardened 隔离。实验容器内的停止机制不等于生产集群验收。

后续已完成 [supervisor 最小机制验证](supervisor-verification.md)：独立 PID namespace 的整租约终止与独立卷回执可行，但尚未接入 AIO，本页记录的 AIO 缺口仍未修复。

## 官方协议核对

固定版本 [Bash 文档](https://github.com/agent-infra/sandbox/blob/v1.11.0/website/docs/en/guide/basic/bash.md) 区分等待超时与执行超时，并提供 session/command 查询；未承诺租约级 fencing 和持久幂等。[生命周期文档](https://github.com/agent-infra/sandbox/blob/v1.11.0/website/docs/en/guide/advanced/lifecycle.md) 与 [环境配置](https://github.com/agent-infra/sandbox/blob/v1.11.0/website/docs/en/guide/advanced/env-config.md) 未提供本项目所需的租约续期协议。上述实测结果优先于从接口名称推断能力。
