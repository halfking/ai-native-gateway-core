# request_logs / request_logs_hot INSERT failed: invalid input syntax for type json (SQLSTATE 22P02)

**Date**: 2026-07-20
**Priority**: P1
**Type**: Bug Fix

## Summary

`persistRequestLog` 在 `entry.RoutingAttempts == nil` 时通过 `string(nil) == ""` 传参，
SQL 端 `$84::text::jsonb` 把空字符串当 JSON 解析 → `SQLSTATE 22P02`。
所有 probe 请求（含 `probe-direct-c{cred}-m{model}-a{attempt}-{ok|fail}-{unix_nano}`），
以及任何不写 `routing_attempts` 的普通请求，只要 `RoutingAttempts` 为 nil 都会失败。

## 根因

- Migration 350/448 给 `request_logs` 和 `request_logs_hot` 加了 `routing_attempts JSONB`
  和 `routing_summary TEXT` 两列（与 routing attempt tracking 一同上线）。
- Go struct 上 `RoutingAttempts` 类型是 `json.RawMessage`（`[]byte` 别名），可能为 nil。
- 旧 INSERT 绑定使用 `string(entry.RoutingAttempts)`：
  - 当 `RoutingAttempts` 为 nil → `string(nil)` = `""` → 传给 pgx → SQL 端 `""::text::jsonb`
  - PG 把 `""` 当 JSON 解析 → "invalid input syntax for type json" (22P02)
- 之前的 `$84::text::jsonb` cast 是为了解决 pgx 二进制协议在 NULL 参数下的 22P02，
  顺手加的，但 nil→空字符串分支没覆盖。

## 影响

- **probe-direct-* 请求**：credstate 触发 active_probe 后整条 INSERT 失败，写不进
  `request_logs_hot`。前端 swim-lane 显示 tile 但点开后 `request not found`（404）。
  之前的 1×100ms 重试不足以等到下次写入尝试。
- **普通请求**：所有不显式设置 `RoutingAttempts` 的调用路径（占大多数）也中招。
  154 日志观察到 `telemetry request db persist failed; fallback written` 自 2026-07-20 02:32
  起持续到 04:59（修复前），频次较高。
- 之前的 routing_attempts insert 是 "unused argument" 修复（83→85 列）后的下一层 bug。

## Fix 内容

### `domains/hooks/observability/telemetry/client.go`

`string(entry.RoutingAttempts)` 改为 `jsonOrNull(entry.RoutingAttempts)`：

```go
// 复用现有的 helper（line 1444）
func jsonOrNull(raw json.RawMessage) string {
    if len(raw) == 0 {
        return "null"        // ← 关键：nil → "null"（合法 JSON 字面量）
    }
    return string(raw)
}
```

SQL 端仍为 `$84::text::jsonb`：
- nil → `"null"` → `"null"::text::jsonb` → SQL NULL（合法）
- 非空 → 原始 JSON 字节流 → `string(json.RawMessage)` → `::text::jsonb` → 解析为 jsonb

`routing_summary`（`entry.RoutingSummary` 类型 `*string`）原本就传 nil → SQL NULL，
不需要改。

## 验证

部署到 154（seq=1200, sha=78112d6e → 现 1201）后：

- ✅ 0 个 `telemetry request db persist failed` 日志（修复前分钟级持续）
- ✅ 0 个 `SQLSTATE 22P02` 错误
- ✅ `request_logs_hot` 中新的 probe / 普通请求都成功落库
- ✅ 前端 swim-lane 点击后 RequestLogDrawer 不再 404（probe 永久丢失的旧请求除外）

## 已丢失的数据（不可恢复）

- 自 2026-07-20 02:32（上一版 0ebbe4d5 部署后）到 04:59（本 fix 部署前）期间：
  - 用户提到的 `a7fefa37881dc3d418d8b1596a22f77b`
  - `probe-direct-c23-mz-ai_glm-5.2-a1-fail-1784486864087039551`
  - 多个 probe-direct-c19/c29-* 请求
  - 这些请求的 INSERT 都失败了，DB 里没有记录
- 因为 telemetry worker 有 fallback write（failed→fallback path），数据没进 DB
  也没进 fallback table，仅在日志里有 warn 记录。已永久丢失。

## 配套修复

- `fix(live-stream): drain 404 race in swimlane drawer with exponential-backoff retry`
  (`59586407`)：前端 RequestLogDrawer 重试从 1×100ms 升级到 4×(200/500/1500ms) 指数退避。
  即使后端有偶发慢写，前端也能 ~2.2s 内命中。两层保护互补。

## 后续 / 不在范围

- telemetry worker 的 `fallback written` 路径目前只写日志，没落盘。
  建议加本地环形 buffer（cap=10000）保留最近 N 条失败记录，便于人工补录。
- 长效方案：把 telemetry DB write 改成同步落 `request_logs_default`（heap 表，
  与 hot/cold 解耦），probe 等"必须立即可读"的请求走 default，再异步搬迁到
  hot/cold。本期不在范围。