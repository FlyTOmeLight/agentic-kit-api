# execd 隔离会话探针

仅创建本机可删除的专用 Docker 资源。基于固定 digest 的 execd/server 镜像构建临时测试镜像，复制 bwrap、gate、launcher，使用 server 镜像内的 bash/Python。没有引入生产镜像或依赖。

```sh
EXECD_ISOLATION_PROBE=1 GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -v -count=1 -timeout 150s ./docs/sandbox-center/probes/isolated
```

普通测试默认跳过。Docker 必须可用，两个基础镜像须已缓存。

两个子用例分别验证：

- restricted：drop ALL、禁提权、默认 seccomp，记录能力不可用及 hardening 降级信息。
- namespace-capable：增加 SYS_ADMIN，放宽 seccomp/AppArmor/systempaths，并挂独立 tmpfs。验证 PID 1 + hardening + Landlock 下，UID/GID 65534 会话删除能停止 `setsid` writer，保留 `rw` 文件，拒绝旧会话新命令。

第二种配置专用于隔离会话机制实验，不是默认 Hardened 配置。容器没有挂载 Docker socket 或主机工作目录；端口只监听 `127.0.0.1`。使用默认 bridge 网络，未做出站网络隔离验收。CPU 1、内存 256 MiB、PID 128。

测试结束删除本次容器、workspace volume、临时镜像标签和构建上下文；基础镜像及 Docker 构建缓存保留，不运行共享缓存 prune。若测试进程被强杀，按日志或 `agentic-isolation-` 名称核对本次资源后清理。

通过只证明这些具体观察窗口内的行为，不证明本地 lease、durable receipt、逃逸防护或生产负载能力。结果见 [选型记录](../../backend-selection.md#新增实测execd-隔离会话)。
