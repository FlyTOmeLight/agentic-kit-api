# OpenSandbox Runtime 故障探针

使用真实 OpenSandbox 管理服务、Docker backend 和 execd，验证其是否满足本项目的 Worker 接管要求。结果见 [验证记录](../../opensandbox-runtime-verification.md)。本目录不接入业务运行路径，只依赖 Go 标准库。

## 固定环境

使用官方 `release-1.1.1` [发布清单](https://raw.githubusercontent.com/opensandbox-group/OpenSandbox/release-1.1.1/docs/releases/1.1.1.yaml) 对应镜像：

```text
opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b
opensandbox/execd@sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded
busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
```

首轮环境为 Docker 29.8.2 / OrbStack、linux/arm64 镜像、Go 1.27.3。server 镜像内部 `opensandbox-server` 包版本显示为 `0.1.0.dev0`，因此以发布清单和 digest 标识被测对象，不以该包版本推断代码版本。

工作负载复用 server 镜像中的 Python/bash，以 `sleep infinity` 作为入口；官方管理服务注入 execd。此选择用于验证通用执行机制，不代表生产 Coding Profile 已完成选型。

## 复现

先确保上述三个镜像已存在。探针不主动调用 pull，但官方管理服务可能自行拉取缺少的镜像。使用没有其他 OpenSandbox 实例的实验 Docker daemon；探针发现 `opensandbox.io/id` 标签容器时会拒绝启动，避免上游启动恢复逻辑处理已有实例。

在仓库根目录执行：

```sh
probe_tmp=$(mktemp -d /private/tmp/opensandbox-runtime-check.XXXXXX)
env OPENSANDBOX_DISPOSABLE_PROBE=1 GOCACHE="$probe_tmp/cache" \
  GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -v -count=1 -timeout 300s ./docs/sandbox-center/probes/opensandbox
# 检查上条命令的输出和退出码后清理本地构建缓存。
rm -rf "$probe_tmp"
```

耗时约三分钟。环境 TTL 的实际最小值为 60 秒，命令 timeout 单位为毫秒。普通 `go test` 未设置开关时会跳过运行，不构成验证通过。

**当前固定组合预期返回非零退出码**：探针保留本项目要求的断言，暴露后端能力缺口，不将这些失败改写成成功。测试框架错误、启动失败和故障注入未成功不能作为后端缺口证据。

## 资源与清理

- 创建唯一 `agentic-osb-probe-*` bridge 网络及同名管理容器；不是 `--internal` 网络，不承诺禁止出站。HTTP 仅发布到宿主 `127.0.0.1` 的动态端口。
- 管理容器挂载 Docker socket，用于上游创建和回收工作负载。这具有控制该 daemon 的权限，应使用实验环境。管理容器限制为 1 CPU / 512 MiB / 128 PID。
- 每个用例创建独立工作负载和 `agentic-osb-work-*` 卷，限制为 1 CPU / 256 MiB / 128 PID；没有业务目录或业务凭据挂载。卷的 `deleteOnSandboxTermination=false`，生命周期结束后用只读、无网络的 BusyBox 容器读取。
- 正常通过或断言失败均通过 `t.Cleanup` 删除本次卷关联的容器、卷、管理容器和网络。按唯一卷查找工作负载，覆盖创建响应丢失时未取得 sandbox ID 的情况。
- 测试进程被强杀或整体超时可能绕过 cleanup。此时按本轮日志中的容器 ID、资源名确认归属后清理；不要全局 prune 或删除其他实例。镜像留在本机缓存。

## 验证边界

1. kill / 3 秒命令 timeout：分别观察普通子进程与 `setsid()` 子进程；命令终态后继续观察 1.2 秒工作区心跳。心跳继续即证明仍可写，不依赖终态名称推断。
2. 管理服务存活时的 60 秒环境 TTL：观察容器删除相对 `expiresAt` 的延迟，实验容差为 -1 至 +5 秒；独立卷文件存在且心跳停止。
3. 管理服务 SIGKILL：确认退出码 137，超过 `expiresAt` 后观察心跳并直连 execd 提交命令；再重启管理服务，观察补回收和文件保留。
4. 完成命令后重启整个工作负载容器：确认容器 `StartedAt` 变化，比较原执行 ID 的状态和日志，以及独立卷回执。不是仅重启 execd 的实验，也不覆盖节点磁盘丢失。
5. 初次请求产生文件副作用后取消 HTTP，必须观察到 `context.Canceled` 才认定响应丢失注入成功；尝试按调用方身份查询并重复同一请求。`Idempotency-Key` 并非上游支持的契约，该项测试衡量项目所需能力，不将缺失记为上游违约。生产不能照此自动重发不确定执行。

不覆盖 Kubernetes/FastSandbox、gVisor/Kata、租约续期/epoch、文件 API、PTY、浏览器、网络分区或运行中 execd 崩溃。本实验也不证明隔离强度或生产可用性。
