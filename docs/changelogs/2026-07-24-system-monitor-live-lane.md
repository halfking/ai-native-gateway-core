# SystemMonitor 动态泳道

## 目标

让 `/dashboard` 的管理员默认看到系统监测实时任务流，以及等待、完成、失败和 token 用量。

## 改动

- Dashboard 新增并默认打开“系统监测”tab，直接挂载 `SystemMonitorPanel`。
- SystemMonitor 通过 `llmgw:monitor:events` 发布 submitted、started、skipped、completed、failed、timeout 和 network_error 事件。
- SSE 任务摘要增加 `total_tokens`。
- 探测响应兼容 `total_tokens`、`input_tokens/output_tokens` 和 `prompt_tokens/completion_tokens` 三种 usage 结构。
- 新增实时检查泳道，显示最新任务事件和 token 数。
- Stats API 增加最近 1 小时完成、失败、跳过和 token 汇总。
- 新增 `346_system_probe_run_tokens.sql` 及对应 down migration，持久化探测 token 用量。

## 部署前置

1. 执行 `sql/migrations/domain/346_system_probe_run_tokens.sql`。
2. 确认服务环境变量 `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`。
3. 确认 Redis 可用，SSE 端点才能接收动态事件。

## 验证

- `go test ./bg/systemmonitor/... ./bg/... ./admin/...`
- `pnpm exec vue-tsc --noEmit`
- `pnpm build`
- 本地浏览器已打开首页并保存截图；受保护的 Dashboard 需要管理员登录凭据，当前未完成登录后的泳道交互截图。
