# AIO Runtime 故障探针

其他候选的独立对照：[OpenSandbox Docker/execd 探针](opensandbox/README.md)。

进一步验证：[原生 Kubernetes 固定截止](kubernetes/README.md)、[execd 隔离会话](isolated/README.md)。综合结论见 [首版选型](../backend-selection.md)。

这是实施前的真实 Runtime 验证，尚未接入 Sandbox Center。探针通过 HTTP 调用 AIO，并在同一容器内检查文件与 `/proc`，避免只凭 API 终态判断进程已经停止。当前结果见 [AIO 验证记录](../aio-runtime-verification.md)。

只在可删除的独立容器内设置 `AIO_DISPOSABLE_PROBE=1`。不要用于共享 AIO：探针会创建进程、文件，并要求重启 `python-server`。普通 `go test` 默认跳过运行用例。失败代表被测 Runtime 不满足停止/恢复要求，不应改成通过断言。

## 本轮组合

- 镜像：`ghcr.io/agent-infra/sandbox@sha256:6328d7fd2f0ff0b4c147c3d05b3df1ce331f4a482eb6e550ecd64ed1fcf906e7`
- 架构：`linux/arm64`；实际 OpenAPI `info.version=1.11.0`。
- Docker / OrbStack，独立 `--internal` 网络；没有主机挂载、生产凭据或 Kubernetes 资源。
- 2 CPU、3 GiB 内存、512 PID、256 MiB shm；使用 Docker 默认 seccomp，没有采用上游示例的 `seccomp=unconfined`。本轮不构成 Hardened 隔离验收。

## 复现

从仓库根目录运行。以下为 bash/zsh 命令；镜像须已存在。测试二进制只依赖 Go 标准库：

```sh
probe_tmp=$(mktemp -d /private/tmp/aio-runtime-check.XXXXXX)
probe_name="agentic-aio-probe-$(date +%s)"
probe_image='ghcr.io/agent-infra/sandbox@sha256:6328d7fd2f0ff0b4c147c3d05b3df1ce331f4a482eb6e550ecd64ed1fcf906e7'
env GOCACHE="$probe_tmp/cache" GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go test -c -o "$probe_tmp/aio-probe" ./docs/sandbox-center/probes
docker network create --internal "$probe_name"
docker run -d --name "$probe_name" --network "$probe_name" \
  --cpus 2 --memory 3g --pids-limit 512 --shm-size 256m "$probe_image"
docker cp "$probe_tmp/aio-probe" "$probe_name:/tmp/aio-probe"
```

先确认 API 就绪和版本，启动或服务重启后可重复此只读命令：

```sh
docker exec "$probe_name" python3 -c 'import json,urllib.request; d=json.load(urllib.request.urlopen("http://127.0.0.1:8080/v1/openapi.json",timeout=5)); print(d["info"]["version"]); assert "/v1/bash/exec" in d["paths"]'
```

运行停止与断连用例；当前固定镜像预期会暴露 detached writer 未停止，进程退出码为 1：

```sh
docker exec -e AIO_DISPOSABLE_PROBE=1 "$probe_name" /tmp/aio-probe \
  -test.v -test.run 'TestAIOStopsWriters|TestAIOClientDisconnect' -test.timeout 90s
```

完成命令的重启查询必须分两次运行，查询阶段核对 daemon 的 PID/starttime 确已变化；当前镜像预期返回 session 404，退出码为 1：

```sh
docker exec -e AIO_DISPOSABLE_PROBE=1 -e AIO_RESTART_PHASE=seed "$probe_name" \
  /tmp/aio-probe -test.v -test.run '^TestAIOSeedRestart$' -test.timeout 15s
docker exec "$probe_name" supervisorctl restart python-server
# 重复上面的只读命令确认 API 就绪，再执行查询。
docker exec -e AIO_DISPOSABLE_PROBE=1 -e AIO_RESTART_PHASE=query "$probe_name" \
  /tmp/aio-probe -test.v -test.run '^TestAIOQueryAfterRestart$' -test.timeout 15s
```

测试成功或失败后均清理本次专用容器、网络和本地构建目录，不清理镜像或其他资源：

```sh
docker rm -f "$probe_name"
docker network rm "$probe_name"
rm -rf "$probe_tmp"
```

## 断言边界

- kill / hard_timeout：普通子进程与 `start_new_session=True` 脱离进程分别测；终态后观察 1.2 秒心跳文件并检查父子 PID/starttime，忽略已退出的 zombie，清理只杀仍匹配身份的探针进程。
- 失联：hard_timeout 用例在等待期间不发送 HTTP；断连用例先看到服务端 writer 已启动，再取消同步请求，确认命令仍继续且普通子进程最终停止。
- 到期准入：记录 hard_timeout 后 file.write、新 Exec 是否仍被接受；这是能力观察，不将单命令超时当作租约到期。
- 恢复：只验证已完成命令在 `python-server` 重启后的身份和输出查询。不覆盖运行中服务崩溃、节点故障、Kubernetes 隔离或外部保存。
- 没有宣称 requestKey 去重或 Unknown 自动重试；这些能力仍需要中心与 Runtime 共同实现。
