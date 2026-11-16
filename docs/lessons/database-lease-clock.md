# 数据库租约测试使用数据库时钟

当生产租约使用 PostgreSQL `clock_timestamp()` 时，测试也应通过数据库表达式设置未来或过期时间。不要用宿主机 `time.Now()` 模拟数据库租约过期。

2026-11-16，隔离 PostgreSQL 容器时钟与宿主机相差数小时。`TestArchiveClaimExpiresAndCanResume` 用宿主机时间减一分钟更新 `archive_until`，对数据库而言仍在未来，重试因此正确返回 Pending。只读查询验证了宿主机时间减一分钟大于数据库当前时间；改用 `clock_timestamp() ± interval '1 minute'` 后，该测试通过。

证据见 `internal/app/memory/archive_test.go` 的 `TestArchiveClaimExpiresAndCanResume`。该修复只统一测试和生产判断所用的时钟，不调整运行环境时间，也不改变生产租约期限。
