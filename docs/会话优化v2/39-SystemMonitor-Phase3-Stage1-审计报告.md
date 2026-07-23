# SystemMonitor Phase 3 Stage 1 审计报告

## 审计范围

审计提交：`e6659ced0`、`d8e6e3371`、`9854b7dc2`、`40d9abe2f`、`3ef1a8351`。

覆盖 Task 1.1 MetricsCollector、Task 1.2 旧 worker 打标、Task 1.3 Dashboard 切流卡片，以及相关 API 契约和历史编译问题。

## 发现与修正

### P1：旧 worker 审计写入不符合 `system_probe_runs` schema

`bg/credential_selfcheck.go` 原实现遗漏必填 `task_id`，并写入 schema CHECK 不允许的 `task_type='credential_selfcheck'`。由于调用方采用 non-blocking 告警，主流程继续运行但迁移指标不会产生有效数据。

修正：复用 `self_check_runs.id` 作为审计关联 `task_id`，将实际工具调用映射为允许的 `chat_tool`，并新增 pgxmock 回归测试锁定 INSERT 参数契约。

### P1：Metrics API 在 backend 未 wiring 时可能 panic

`handleSystemMonitorMigrationMetrics` 直接调用 `h.systemMonitor.GetMetricsCollector()`，而 `systemMonitor` 可按设计为 nil。

修正：未 wiring 时返回 `503 system monitor not wired`。

### P2：MetricsCollector 缺少 nil DB 防护

修正：数据库池不可用时返回带上下文的错误，不执行 panic 风险调用。

### P2：Dashboard 进度值未限制在 0-100

修正：新增 `migrationCoverage` 计算值，统一用于文案、颜色和 `el-progress`，避免异常数据造成非法进度显示。

### P2：Dashboard API 查询参数未编码

修正：`window_days` 使用 `encodeURIComponent` 构造请求参数。

### P2：Phase 3 CSS 位于 SFC `</style>` 之后

修正：将迁移卡片样式并入同一个 `<style scoped>` 块，确保样式实际进入构建产物。

## 验证结果

- `go test ./bg/systemmonitor/... ./bg/...`：通过。
- 定向回归：`TestAuditToSystemProbeRunsUsesCompatibleSchema`、`TestRunOne_NoRoutableModels_InsertsFailedPlaceholder`：通过。
- `pnpm exec vue-tsc --noEmit`：通过。
- `pnpm build`：通过；仅有既有 Vite chunk size、CJS API 和 Sass legacy API 警告。
- `git diff --check`：通过。
- `go test ./...`：未整体通过。已有 `domains/credential` 测试失败：Redis 读取性能阈值和 Redis 恢复数量断言；失败位置不在本次审计改动，完整命令在超时前已暴露这些失败。
- 浏览器实测：已启动本地预览并打开 `http://127.0.0.1:4173/`。当前页面为公开首页，未具备登录态，未能进入 SystemMonitor 页面进行鉴权后的交互验证。截图证据：`/tmp/ui-verify-systemmonitor-preview-20260724.png`。

## 遗留风险

1. 需要在 245 测试环境使用有效管理员登录态验证 `/system-monitor` 页面、迁移指标卡片、API 失败容错、daylight/night 主题和移动端布局。
2. 需要在真实 PostgreSQL 上对 `system_probe_runs` 的 `started_at` 窗口查询执行 `EXPLAIN ANALYZE`，确认大数据量下的 COUNT/GROUP BY 性能。
3. 全仓已有 `domains/credential` 测试失败，需要单独修复，不应与本次 SystemMonitor 审计修正混合。

## 结论

Stage 1 代码存在两个会影响线上功能的契约问题，已完成最小修正并补回归测试。后端相关验证通过，前端可编译构建；在取得 245 管理员浏览器会话前，Dashboard 的真实交互验收保持未完成。
