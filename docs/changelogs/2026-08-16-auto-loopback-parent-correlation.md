# Auto Loopback Parent Correlation Repair

## Summary

修复 auto-title / auto-summary 回环子请求的父子关联在落库时恒为 NULL 的问题。
两个独立缺陷叠加：回环 `X-Gw-Session-Id` 使用了 `gt:` / `gs:`（冒号）前缀而被
`sanitizeGwSessionHeader` 静默丢弃；成功路径最终 upsert 未应用
`applyParentCorrelationFields`，用 NULL 覆盖了初始写入已落库的关联字段。

## Changes

- `admin/auto_title_generator.go`、`admin/auto_summary_generator.go`：回环
  `X-Gw-Session-Id` 前缀由 `gt:` / `gs:` 改为契约要求的 `gt_` / `gs_`。
  `sanitizeGwSessionHeader` 仅接受 `gw_` / `gt_` / `gs_`，冒号形式此前被整体
  丢弃，子请求因此拿到全新的 `gw_<uuid>`，分支命名空间失效。
- `domains/streaming/handler.go`：`emitTelemetry` 的成功路径 `reqLog` 补上
  `applyParentCorrelationFields(reqLog, logCtx)`。此前该条目的
  `parent_request_id` 只来自 `result.ParentRequestID`（压缩重写父链），入口侧从
  `X-Gw-Parent-Request-Id` / `X-Gw-Source-Actor` 读入 logCtx 的关联被丢弃，
  `ON CONFLICT DO UPDATE` 又把 `recordInitialRequestLog` 已写入的值覆盖为 NULL，
  live stream 的 `child_request` 帧因此从不发射。
- 回归门禁：`domains/streaming/handler_test.go` 新增冒号前缀必须被拒绝的用例，
  钉死 `gw_/gt_/gs_` 契约；`admin/auto_summary_generator_test.go` 的断言由
  `gs:gw_abc123` 更正为 `gs_gw_abc123`（原断言锁定了缺陷行为，且与同文件注释
  自述的 `gs_gw_abc123` 相互矛盾）。

## Verification

- `go build ./...`
- `go vet ./...`
- `go test ./admin/ ./domains/streaming/... ./domains/hooks/... ./bg/...`
- `go test ./domains/streaming/ -run 'TestSanitizeGwSessionHeader|TestApplyParentCorrelationFields' -v`
- 待补：部署环境上按 `WHERE origin_actor IN ('auto-title-generator','auto-summary-generator')`
  抽查 `request_logs_hot.parent_request_id` 非空，并确认 live stream 的
  `child_request` 帧实际发射。

## Rollback

代码回滚可直接 revert 本次提交；无 schema 变更，无数据迁移。历史行的
`parent_request_id` 仍为 NULL，不会被本次改动追溯回填。
