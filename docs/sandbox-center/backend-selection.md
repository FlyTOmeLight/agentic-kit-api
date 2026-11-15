# Sandbox 首版选型与验证结论

日期：2026-10-22。状态：**实施推荐，尚未通过生产 Profile 验收**。

## 选择

首版命令、文件和 CLI Agent 场景选择 **原生 Kubernetes Provider + OpenSandbox execd 数据面 + 独立工作区 PVC**。在执行边界补齐受保护的本地租约和持久执行回执。AIO 留给后续确实需要浏览器、桌面或开发工具组合的 Profile，不作为首版必接依赖。

这是对本仓库设计约束的选择，不是宣称 execd 已满足全部要求。当前业务 worker 的 AIO 调用尚未替换；本轮只增加探针与验证文档，没有引入生产依赖。

实施前置条件已收敛到 [首版 Runtime 接入规格](runtime-integration-plan.md)：execd 增加受保护 Unix socket 模式，Guard 强制执行身份并控制全部首版入口。固定版本的 `/internal/init` 跳过 token 校验，不能让同网络 namespace 的用户进程直达 execd。该限制来自源码检查；增加补丁后的控制边界与整实例停止已通过 [Guard + execd 容器探针](guard-execd-verification.md)，可续租与回执仍待实现。首版接受执行后回执未保存的 Unknown 窗口，不承诺所有结果均可恢复。

选择理由：本项目已经确定 Kubernetes 资源底座和 Provider / Runtime Adapter 分工，execd 的命令、文件与输出协议贴合首版范围。采用完整外部管理服务会增加生命周期对接，但目前没有证据表明它能消除本项目的租约与执行恢复缺口。用 Kubernetes 原生对象先打通单实例生命周期，无需首版同时引入 CRD、预热池及第二个沙箱控制器。

## 候选对照

| 组合 | 已有证据 | 对本项目的主要成本或缺口 | 本轮决定 |
| --- | --- | --- | --- |
| 原生 Kubernetes + execd | 本轮实测 Pod 固定截止、停止脱离进程、保留 PVC；另测 execd 隔离会话删除 | 需受保护 UDS 补丁与固定身份；可续租期限、回执、隔离与接管待集成 | **有前置条件的首版推荐** |
| AIO | 已测普通进程停止；脱离进程漏停，服务重启丢命令查询 | 同样需租约与回执保护；首版不需要其全部组合能力 | 保留浏览器 Profile 方向 |
| 完整 OpenSandbox Docker | 正常 TTL 可销毁环境；管理服务崩溃后过期仍可执行 | 依赖管理进程到期回收；不提供本项目需要的命令去重/持久回执 | 不选为当前 Kubernetes 首版资源控制面 |
| 完整 OpenSandbox Kubernetes | 固定版本源码仅提供 BatchSandbox、agent-sandbox 两种 Provider | 增加 CRD/controller；到期字段不等于 kubelet 本地租约，数据面仍是 execd | 暂不引入；未做完整部署实测 |
| FastSandbox | 固定源码的 Fastlet 协议未见本地 deadline；到期由控制器处理 | 持久存储与本机部署条件不匹配本轮最小路径 | 暂不引入；未做运行实测 |
| E2B 自托管 | 当前有 Apache-2.0 Runtime 和单机 Embed；支持 filesystem-only pause | 需 KVM 运行底座；控制面故障下的本地停止未验证，envd 退出事件不是持久回执 | 后续需要 microVM/快照时再评估 |
| Kubernetes SIG Apps agent-sandbox | Sandbox/PVC 对象、shutdownTime 生命周期 | 到期依赖 controller；资源持久化不等于执行结果持久化 | 目前没有替代本项目控制面的必要证据 |

E2B 不能沿用“只能重部署 GCP”这一旧判断：当前 Embed 支持 Linux x86-64/arm64。但硬件和虚拟化支持不等于本机 OrbStack 已满足运行前提，本轮未部署 E2B。Daytona、Runloop 未做足够运行验证，本轮不对其可靠性作否定结论，也不据此选为首版。

## 新增实测：原生 Kubernetes 固定截止

环境为本地 `orbstack`，节点 `v1.36.5+orb1`、linux/arm64。探针显式固定 context，未访问 dev/qa，未安装或暂停共享 controller。

创建独立 namespace、64 MiB local-path PVC 和原生 Pod。固定 execd 镜像提供二进制，固定 server 镜像只提供 Python/bash 工作负载环境。execd 作为 PID 1；Pod 不挂载 ServiceAccount token，容器 drop ALL capabilities，禁提权，采用默认 seccomp。**这仍不是生产 Hardened 隔离验收，也不是正式 Coding 镜像。**

| 动作 | 实际观察 |
| --- | --- |
| `activeDeadlineSeconds=30`，尝试延长到 45 | API 拒绝：新值必须小于等于旧值 |
| 经 execd 启动 fork + `setsid` 心跳写入器 | 取得执行 ID，心跳持续增长 |
| 不运行 sandbox manager，等待截止 | Pod 进入 Failed / DeadlineExceeded；容器结束时间比截止晚 2 秒 |
| Pod 终态后尝试 exec | 明确拒绝：不能 exec 到 completed Pod |
| 删除 Pod，另起只读 Pod 挂载原 PVC | 文件保留；5673 字节在 1.2 秒观察窗内不变 |
| 检查末次写入时间 | 比指定截止晚约 2.161 秒 |

`TestPodDeadline` 通过，主体耗时 48.68 秒。测试采用停止时间 `[-1s,+5s]` 的实验容差，**不表示已满足严格到期即停写的生产时限**。一次测试不能作为延迟分位数或最大延迟保证。

结论：Pod deadline 可作固定最大寿命的兜底，不能续长，不能取代可续租 `RuntimeAdmission`。本轮 kubelet、API server 均正常，未注入节点失联或控制平面网络分区；验证的是“不依赖 sandbox manager 的这一条停止路径”。local-path 文件保留只覆盖 Pod 删除，不承诺节点或磁盘故障恢复。

复现：[Kubernetes 探针](probes/kubernetes/README.md)、[源码](probes/kubernetes/deadline_test.go)。

## 新增实测：execd 隔离会话

本轮固定镜像中的 execd 自报二进制提交为 `b7d7697750b45be2ccc876da645c8880a0cf69fc`，构建时间为 `2026-10-09T13:36:16Z`。发布名、镜像 digest 和二进制提交分别记录，不混用。镜像包含 bwrap 0.11.2、native session gate 与 launcher，基于固定 server 镜像补齐 bash/Python 后实测。

开启 PID 1、hardening、Landlock，比较两种容器权限配置：

| 配置/操作 | 实际观察 |
| --- | --- |
| drop ALL、no-new-privileges、Docker 默认 seccomp | `available=false`；创建 namespace 被拒；cap_drop 报 degraded，seccomp/Landlock 报 active |
| 增加 SYS_ADMIN，seccomp/AppArmor/systempaths 放宽，独立 tmpfs | `available=true`；cap_drop、seccomp、Landlock 均报告 active |
| strict session、工作区 `rw`、UID/GID 65534 | 创建成功，后台 fork + `setsid` 写入器实际持续写入 |
| DELETE session | HTTP 200；心跳 132→132 字节，1.2 秒观察窗内稳定，工作区文件保留 |
| 删除后向旧 session 再提交 run | HTTP 404 / SESSION_NOT_FOUND |

`TestIsolatedSessionTeardown` 两个子用例通过，主体耗时 9.88 秒。restricted 子用例验证“能力不可用”的事实，不计作隔离会话功能通过。capabilities 的 active 状态是运行时自报，本轮没有完成隔离逃逸测试。

**这证明可以复用上游的整会话清理能力；不证明开启该模式便具备受保护租约。** 本轮增加的容器权限不能直接带入默认 Hardened Profile；须与实际 RuntimeClass、节点及最小权限配置一起验收。如果不采用会话隔离，应以独占实例内的受保护执行边界完成全部用户进程停止，不能退回仅 kill 进程组。

上游 background run 不受 `timeout_seconds` 限制，运行期间暂停 idle GC；session 删除同时清理运行记录。它们分别属于执行、空闲回收与临时记录机制，不能充当本地硬租约和持久执行回执。PID 1、lifecycle periodic hook、进程重启 supervisor 也没有提供该完整契约。

复现：[隔离会话探针](probes/isolated/README.md)、[源码](probes/isolated/session_test.go)。

## 正式实施的最小边界

```text
Agent worker → Sandbox API/执行账本 → 受保护的租约准入与回执 → execd → 用户进程
                       ↓                     ↓
              Kubernetes Provider       独立 workspace PVC
                       ↓
                独占 Pod / 实例代次
```

这是拟实施职责图。不要把受保护执行层理解成一个普通 HTTP 代理：它必须控制所有实际写入入口和现存写者，且用户代码不能修改租约、终止保护后继续执行，或伪造回执。

1. 先做一个命令/文件 Profile：一个实例、一个工作区、一个有效 lease。不同时接浏览器、PTY、预热池或内存快照。
2. 本地保护固定 `runID + epoch + instanceGeneration`，按有限期限关闭执行和文件写入、停止全部旧写者。续期必须取得 Runtime 确认；过期绑定不能被迟到续期复活。Pod 固定寿命只作为额外兜底。
3. 调用前持久化 operationID 和语义摘要。受保护回执记录受理、终态和输出范围；重启后可查。派发与回执间的崩溃窗口保留 Unknown，不以 404 触发重放，也不承诺任意外部副作用 exactly-once。
4. 工作区与 Pod 分开保存；需要保护的文件没有保存成功时，停执行并保留文件。没有旧写者停止证据，不允许新 epoch 接管写权。

已有 [supervisor 原型](supervisor-verification.md) 验证了直接启动进程的部分机制，不是正式实现。改成向 execd 派发会新增执行与回执之间的崩溃窗口。[阶段 1a](guard-execd-verification.md) 已验证控制通道、固定身份及 Guard/execd 崩溃停写。下一步按 [接入规格](runtime-integration-plan.md) 实施续租、epoch、持久回执与完整文件路径；不能将边界探针直接当作生产 Runtime。

生产发布前必须补齐：目标 RuntimeClass 的隔离与权限验收、控制面失联下所有入口到期关闭、续期/过期竞争、进程树停止与文件 API 在途写入、强杀后的回执恢复与 Unknown、双 worker 接管、SaveBlocked，以及生产存储故障边界。停止时延、启动耗时、内存/CPU 和并发吞吐需在目标环境测量并定义接受值；本轮小样本不提供容量承诺。

## 证据与范围

- 前轮真实结果：[AIO](aio-runtime-verification.md)、[OpenSandbox Docker](opensandbox-runtime-verification.md)、[保护机制原型](supervisor-verification.md)。
- [Kubernetes Pod 字段契约](https://kubernetes.io/docs/reference/kubernetes-api/core/pod-v1/)与 [kubelet deadline handler](https://github.com/kubernetes/kubernetes/blob/v1.36.0/pkg/kubelet/active_deadline.go)：用来解释原生 Pod 实验，不代替本轮实测。
- OpenSandbox 固定 `release-1.1.1`：[Provider 工厂](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.1.1/server/opensandbox_server/services/k8s/provider_factory.py)、[BatchSandbox 到期处理](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.1.1/kubernetes/internal/controller/batchsandbox_controller.go)、[隔离会话契约](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.1.1/docs/guides/isolation-sessions.md)、[强化配置](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.1.1/components/execd/configs/isolation.example.toml)、[PID 1 实施状态](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.1.1/oseps/0018-execd-as-sandbox-init.md)。源码结论不自动等同上述镜像运行事实。
- FastSandbox pin `b702fbe71593df4f36b591e540445c251a698e7c`：[到期控制器](https://github.com/opensandbox-group/fast-sandbox/blob/b702fbe71593df4f36b591e540445c251a698e7c/internal/controlplane/reconciler/sandbox_controller.go)、[Fastlet 协议](https://github.com/opensandbox-group/fast-sandbox/blob/b702fbe71593df4f36b591e540445c251a698e7c/internal/protocol/fastlet/types.go)、[部署要求与范围](https://github.com/opensandbox-group/fast-sandbox/blob/b702fbe71593df4f36b591e540445c251a698e7c/README.md)。
- E2B commit `c0e3c5fabbfde1839f6bf08d3627d0401ba82908`：[Embed 要求](https://github.com/e2b-dev/runtime/blob/c0e3c5fabbfde1839f6bf08d3627d0401ba82908/embed/compose/README.md)、[evictor](https://github.com/e2b-dev/runtime/blob/c0e3c5fabbfde1839f6bf08d3627d0401ba82908/packages/api/internal/orchestrator/evictor/evict.go)、[envd 退出事件缓存](https://github.com/e2b-dev/runtime/blob/c0e3c5fabbfde1839f6bf08d3627d0401ba82908/packages/envd/internal/services/process/service.go)。[filesystem-only pause 官方契约](https://docs.e2b.dev/sandbox/filesystem-only-snapshots)是文档证据，本轮未注入 E2B 控制面故障。
- agent-sandbox：[controller 实现](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/controllers/sandbox_controller.go)，本轮检查时的 main，未固定运行镜像，未做部署验证。

本轮两个新增探针的 gofmt、go vet 通过。实验 namespace、PVC/PV、容器、卷和临时测试镜像已清理，基础镜像缓存保留。没有改动业务 API/worker，没有提交 Git commit。3 个并行子代理完成源码调查，主代理完成实际运行与结论整合。
