# 受保护执行层最小实验

**实验代码，不是生产 supervisor，也未接入 AIO 或 Sandbox Center。** 本轮验证一个租约使用一个私有 PID namespace：root supervisor 做 PID 1，用户命令以 UID/GID 65534 运行。期限到达时 PID 1 直接退出，由内核终止该 namespace 的全部进程，工作区与执行回执保留在卷中。结果见 [验证记录](../../supervisor-verification.md)。

## 已实现的实验路径

- `/bind`：一个容器只绑定一次 1–60 秒的不可变租约。使用进程内单调计时器，到期退出不等请求锁、输出复制或 fsync。没有续租或切换持有者。
- `/start`：稳定执行 ID 与 Script 哈希；相同输入返回原回执，冲突返回 409。CWD、环境和租约在本实验中固定；正式请求哈希还须包含全部可变执行语义与租约身份。
- `/query`：查询原 ID、状态和有界输出。不存在返回 404；调用方不能据此推断可以重跑。
- 启动顺序：Pending 回执落盘、fsync 文件及父目录，再启动用户命令。stdout/stderr 经 pipe 复制到 root-only 文件，用户不持有日志文件的可写描述符。超过 64 KiB 的输出继续排空并记录截断。
- 完成顺序：主命令退出、输出复制结束并同步后保存 Exited。写日志失败独立于 `Cmd.Wait` 的退出错误检查。**Exited 不表示后代已停止或工作区可保存。** 脱离进程仍可运行到租约整体终止。
- 任何重启都会关闭原租约，未确认记录变为 Unknown；已保存的 Exited 与输出可读。新执行与再次 Bind 均被拒绝。回执目录加 flock，禁止同一卷有两个实验 supervisor。

控制协议只监听 `/state/control.sock`，权限 0600；`/state` 为 0700。本轮用 root 的 `docker exec` 作为可信测试入口，没有对外 HTTP、鉴权或生产委托。用户只可写 `/workspace` 和临时目录。

## 一条实验链

在仓库根目录执行，需要已有 Docker 和下列固定的 arm64 基础镜像。测试会构建临时镜像、创建独立容器及卷，再自动清理；不访问业务容器或 Kubernetes。

```sh
probe_tmp=$(mktemp -d /private/tmp/supervisor-probe.XXXXXX)
env GOCACHE="$probe_tmp/cache" GOWORK=off GOPROXY=off GOSUMDB=off \
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -o "$probe_tmp/probe" ./docs/sandbox-center/probes/supervisor-prototype
env SUPERVISOR_PROBE_BINARY="$probe_tmp/probe" GOCACHE="$probe_tmp/cache" \
  GOWORK=off GOPROXY=off GOSUMDB=off \
  go test -v -count=1 -timeout 150s ./docs/sandbox-center/probes/supervisor-prototype
env GOCACHE="$probe_tmp/cache" GOWORK=off GOPROXY=off GOSUMDB=off \
  GOOS=linux GOARCH=arm64 go vet ./docs/sandbox-center/probes/supervisor-prototype
```

macOS 不编译 Linux 专用的输出单元测试。可另行编译并在无网络临时容器运行：

```sh
env GOCACHE="$probe_tmp/cache" GOWORK=off GOPROXY=off GOSUMDB=off \
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go test -c -o "$probe_tmp/output-tests" ./docs/sandbox-center/probes/supervisor-prototype
probe_unit="supervisor-output-unit-$(date +%s)"
docker create --name "$probe_unit" --network none --cap-drop ALL \
  busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e \
  /tmp/output-tests -test.v -test.run '^TestOutputEvidence$'
docker cp "$probe_tmp/output-tests" "$probe_unit:/tmp/output-tests"
docker start -a "$probe_unit"
docker inspect "$probe_unit" --format '{{.State.ExitCode}}'
docker rm "$probe_unit"
rm -rf "$probe_tmp"
```

`go test` 未设置 `SUPERVISOR_PROBE_BINARY` 时跳过 Docker 集成测试。可执行文件必须是本目录当前源码编译的 Linux arm64 版本。测试用例不在运行时下载依赖；临时镜像基于本地 `busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e`。

## 实验与正式实现的距离

- 本轮只证明整租约终止。需要 supervisor 持续在线、独立停止某条命令时，应使用受保护的进程归属机制，不能退回进程组 kill。
- 没有通用 orphan reaper，短命脱离后代可能留下 zombie 并耗尽 PID 配额。正式实现须补齐；不能用全局 `Wait4(-1)` 与 `exec.Cmd.Wait` 竞争同一子进程。
- 运行失败或输出异常可能保持 Pending，直到恢复转 Unknown；尚无生产级清理、取消与错误分类。
- 没有续租、epoch/fencing 校验、生产认证、文件/PTY/browser 接口、保存发布、配额和日志归档。受保护卷用于本地进程崩溃验证，不是正式回执数据库选型。
- 没有验证节点、磁盘故障、主机 suspend、CPU 长时间饥饿、不可中断内核 I/O，也未运行 Linux race detector。到期回调不等用户 I/O，但不承诺严格实时停止。
- 未将现成 AIO 镜像置于此边界内。AIO 的 Shell、文件 sudo、Jupyter、浏览器等写入/执行路径必须统一受控，不能因为本实验通过就把 `EnforcesLeaseDeadline` 标为生产已支持。
