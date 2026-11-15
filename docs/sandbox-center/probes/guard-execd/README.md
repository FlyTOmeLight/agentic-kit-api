# Guard + execd 受保护控制边界探针

这是 Runtime 阶段 1a 的可复现实验，验证 root PID 1 Guard、execd 私有 Unix socket、非 root 命令/文件写入与整实例停止。结果见 [验证记录](../../guard-execd-verification.md)，完整交付条件见 [接入规格](../../runtime-integration-plan.md)。

## 固定输入

- OpenSandbox 源码提交：`b7d7697750b45be2ccc876da645c8880a0cf69fc`。
- 补丁：[execd-uds.patch](patches/execd-uds.patch)，仅修改 listener、flag、调用装配、测试及上游说明，不修改模块依赖。
- 原镜像身份：`opensandbox/execd@sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded`。实验使用从上述源码加补丁构建的二进制，不把它冒充原镜像。
- 测试基底：`opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b`，提供 bash/Python。
- 已测架构：Linux arm64；本地 Docker context 为 OrbStack。没有在目标 Kubernetes RuntimeClass 联合运行。

上游许可证为 Apache-2.0。补丁新增代码仅依赖 Go 标准库；API 和 worker 未增加生产依赖。维护成本是固定上游提交、保存补丁、升级时重新检查路由/身份并重跑测试。此次没有运行供应链漏洞扫描，不能据此批准生产镜像。

## 构建与执行

在 API 仓库根目录执行。需要本仓库 Go 工具链、curl、tar、Git 和可用的 Linux arm64 Docker daemon；预先拉取固定基底镜像。变量只用于此次临时构建，清理前先保存需要的日志。

```sh
probe_repo="$PWD"
probe_build=$(mktemp -d /tmp/agentic-execd-uds.XXXXXX)
probe_commit=b7d7697750b45be2ccc876da645c8880a0cf69fc
curl -fL "https://codeload.github.com/opensandbox-group/OpenSandbox/tar.gz/$probe_commit" -o "$probe_build/source.tar.gz"
tar -xzf "$probe_build/source.tar.gz" -C "$probe_build"
probe_source="$probe_build/OpenSandbox-$probe_commit"
git -C "$probe_source" apply --check "$probe_repo/docs/sandbox-center/probes/guard-execd/patches/execd-uds.patch"
git -C "$probe_source" apply "$probe_repo/docs/sandbox-center/probes/guard-execd/patches/execd-uds.patch"
(
  cd "$probe_source/components/execd"
  GOWORK=off go mod download
  GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$probe_build/execd" .
  GOWORK=off go test ./pkg/flag
  GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o "$probe_build/listener-tests" ./pkg/listener
)
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$probe_build/guard" ./docs/sandbox-center/probes/guard-execd
GUARD_EXECD_BINARY="$probe_build/guard" PATCHED_EXECD_BINARY="$probe_build/execd" \
  GOWORK=off go test -v -count=1 -timeout 200s ./docs/sandbox-center/probes/guard-execd
```

集成测试自动创建并删除自己命名的容器、两个独立 volume 和临时镜像。未设置两个二进制变量时明确 Skip；Skip 不算验收通过。测试不修改已有容器或卷。

Linux root listener 单测可用一次性容器运行；`CHOWN` 仅用于构造非 root 目录测试，Guard 运行容器不需要该能力：

```sh
probe_unit="agentic-execd-uds-unit-$$"
docker create --name "$probe_unit" --network none --cap-drop ALL \
  --cap-add SETUID --cap-add SETGID --cap-add CHOWN \
  busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e \
  /listener-tests -test.v
docker cp "$probe_build/listener-tests" "$probe_unit:/listener-tests"
docker start -a "$probe_unit"
docker inspect "$probe_unit" --format '{{.State.ExitCode}}'
docker rm "$probe_unit"
rm -rf -- "$probe_build"
```

## 运行边界

Guard 必须为独占 PID namespace 中的 root PID 1。`/state` 是 UID 0、0700 的独立卷，两个 socket 为 0600；`/workspace` 是 UID/GID 65534 可写的独立卷。根文件系统只读，drop ALL 后仅保留 SETUID/SETGID，启用 no-new-privileges、默认 Docker seccomp，限制 1 CPU、256 MiB 和 128 PID。`/tmp` 为 16 MiB、noexec/nosuid/nodev tmpfs，网络为 none。

- `guard serve SECONDS`：接受 1–86400 秒不可续期期限（阶段 2 放宽上限以承载长租约；实测到期行为仍只有 20 秒样本）；首次 boot 写入并 fsync 关闭恢复标记。到期或 execd 退出时，PID 1 退出，由 Linux 终止 namespace 内其余进程。
- `guard call /health '{}'`：查询期限、closed 与 execdPID，调用者必须可访问私有 socket。
- `/command` 仅接受 `script`，固定 UID/GID 65534、`/workspace`、环境和 10 秒命令 timeout。
- `/file-write` 仅接受 `content`，经 identity-scoped 文件 worker 写入固定 `/workspace/guard-file`。
- 请求体上限 16 KiB，响应读取上限 1 MiB。传输失败/不完整响应返回结果不确定，不自动重试或远程 Kill；没有持久查询能力。

本探针不包含远程 mTLS、租约续期、执行账本、孤儿进程 reaper 或完整文件路径 API。此次普通命令和文件 worker 路径无需上游隔离会话的 native helpers；不能将这个简化构建用于宣称上游全部功能可用。补充组来自上游身份解析，本镜像实测没有 65534 之外的组；生产仍须显式落实清空额外组的契约。完整隔离、在途写入排空、并发路径替换、节点/存储故障和性能预算均另行验收。
