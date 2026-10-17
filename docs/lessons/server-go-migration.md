# 发布依赖与框架迁移经验

## 发布依赖必须独立验证

迁移 server-go 时，本地 workspace 曾包含尚未发布的框架接口，联合测试通过后，公开依赖仍无法独立构建。workspace 会优先使用本地模块源码，因此它验证的是联合工作区，不能证明 `go.mod` 中的发布版本包含所需接口。

本项目在联合验证之外，关闭 workspace、移除本地 `replace`，再执行集成测试与无 CGO 构建。容器构建排除 `go.work`，从公开依赖构建。若接口缺失，应先核对发布模块的源码和版本，再升级引用。

Coding SDK 并入 `forge-coding-api` 根模块后，模块依赖改为根模块，源码继续导入其中的 `sdk/go/codingv1`。导入路径和模块版本并非同一层信息，升级时要同时检查 `go.mod` 和实际调用源码。

证据：[模块依赖](../../go.mod)、[容器上下文排除项](../../.dockerignore)、[Coding 适配](../../internal/client/coding.go)；历史经过见 [验证记录](../verification.md) 中的“server-go v0.3.0 发布版核对”“server-go v0.3.1 发布与独立验证”和“server-go v0.9.0 重构与 Coding 根模块升级”。

## 请求绑定要保留字段来源

提交消息时，路径里的 Session ID、JSON 中的 query 和 Header 中的 Idempotency-Key 各有来源。将整份请求直接用于 Header 绑定，可能让其他 Header 覆盖已经绑定的字段；使用通用绑定也可能让 query 冒充必需的 Header。

当前实现先绑定路径、query 和 JSON，再用仅含幂等字段的结构绑定 Header，最后赋回请求。验证重点是调用方最终收到的 Session、query 和幂等键，以及缺少 Header 时确实拒绝请求。

证据：[提交消息绑定](../../internal/app/session/controller.go)、[TestSubmitMessageSourcesRemainSeparate](../../internal/app/session/controller_test.go)。回归用例覆盖 Header 无法覆盖正文或 URI、query 无法替代 Header，以及伪造 query 不改变合法 Header 的值。

## 入口迁移要核对配置和可选能力

采用 `app.Run` 后，配置由框架读取，环境变量映射放入 TOML 占位符。迁移入口时需同步默认配置、部署模板和启动说明，不能继续按旧配置加载器的覆盖规则理解自定义 TOML。

Swagger 等可选 HTTP 能力还需要入口通过 `Setup: httpintegration.Install` 安装。配置字段存在并不代表入口已接入相应能力，应验证实际 HTTP 响应。动态配置重建 handler 时也要保持公开路径；本项目拒绝添加 `HTTPServer.APIBasePath`。

证据：[服务入口](../../cmd/server/main.go)、[默认配置](../../conf/server/config.toml)、[配置更新约束](../../service/build.go)、[公开路径回归测试](../../service/build_test.go)、[OpenAPI 与 Swagger 测试](../../service/openapi_test.go)。
