# 供应商凭据错误详情与透明 Failover

## 做了什么

- 新增 `GET /api/vendors/credentials/{id}/error-detail?hours=1|24|168`。
- 聚合 `candidate_failure_logs_with_current_month`、`credentials`、`provider_profile_daily`，展示错误分布、最近失败、凭据状态和质量评分。
- provider detail 页面新增供应商错误 tab，并从凭据列表提供入口。
- failover 的同凭据重试和备用节点切换通过现有 SSE comment 发送脱敏进度提示；提示不进入模型消息，也不改变请求成功/失败语义。

## 安全与可观测性

- 客户端只收到受控 `error_kind` 和 HTTP status，不收到上游响应 body、URL、凭据或 token。
- DB 查询失败在服务端以 `slog.Error` 记录上下文，客户端只收到稳定错误码。
- 错误详情以 `credential_id` 归集，支持 1h / 24h / 7d 窗口评估供应商质量。

## 验证

- `go test ./...`：通过。
- `go vet ./...`：通过。
- `pnpm i18n:check`：通过。
- `go test ./domains/dispatch/... ./domains/streaming/... ./admin/...`：通过。
- `pnpm exec vue-tsc --noEmit`、`pnpm i18n:check:strict`、`pnpm build`：通过。
- browser-use：浏览器沙箱无法连接宿主机 Vite 服务（`ERR_CONNECTION_REFUSED`），需在可访问本地服务的浏览器环境补跑交互和双主题截图。
