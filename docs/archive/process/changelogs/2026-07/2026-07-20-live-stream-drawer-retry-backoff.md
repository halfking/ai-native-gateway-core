# Live Stream 泳道点击 RequestLogDrawer 404 重试机制强化

**Date**: 2026-07-20
**Priority**: P1
**Type**: Bug Fix

## Summary

实时请求流泳道（Live Stream Swimlane）点击某些 tile → `RequestLogDrawer` 打开失败，弹
"request not found" 错误。本次修改把详情接口的重试从 1×100ms 升级为 4 次尝试 +
指数退避（200ms / 500ms / 1500ms），给异步 DB 持久化管道约 2.2 秒的容忍窗口。

## 根因

- Tile 数据从 Redis 实时推送（`live_stream_redis_store.Record`），
  亚秒级到达前端泳道。
- 同一份数据还要经 telemetry 管道异步写入 `request_logs` 表。
- 前端点击 tile 后立即调 `GET /api/logs/{request_id}`，DB 记录可能尚未持久化 → 404。
- 原重试逻辑仅 1×100ms，对主动探测 `probe-direct-*` 等较慢路径不够。

## 影响

涉及主动探测 tile（`is_probe: true`，`request_id` 形如
`probe-direct-c19-mz-ai_glm-5.2-a3-fail-1784494502962130833`）
和刚出现数秒内的普通 tile。提高命中率 ≈ 2.2s 内的延迟 DB 写都能成功加载。

## Fix 内容

### 前端 `web/src/components/RequestLogDrawer.vue`

将单次 100ms 重试改为 4 次尝试的指数退避循环：

| 尝试 | 等待（前次失败后） |
|------|------------------|
| 1    | -                |
| 2    | 200ms            |
| 3    | 500ms            |
| 4    | 1500ms           |

非 404 错误立即抛出，不消耗重试预算。

## 验证

- ✅ 单元行为：循环边界（4 次尝试 + 第 4 次仍失败时正确抛出）
- ✅ 行为契约：仅重试 `not found` / `404`，其它错误立即抛
- ✅ 加载状态：`loading` 在所有情况下正确 reset
- ✅ 文档注释：保留原有 2026-07-04 注释 + 新增 2026-07-20 注释，说明升级原因

## 后续 / 不在范围

- 后端可考虑提供 `/api/logs/stream/{request_id}` 接口，在 DB 命中前订阅
  Redis 中的同 request_id tile 元数据 → 0 等待命中。本期不在范围。
- Race condition 根因（Redis 与 PG 写顺序）若长期化，应改为写入事务后再
  fanout 到 Redis。本期不在范围。
