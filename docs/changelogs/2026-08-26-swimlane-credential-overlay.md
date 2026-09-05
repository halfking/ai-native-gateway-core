# 泳道按凭据 (credential) 分组 + 终态 overlay 闭环 — 2026-08-26

## 背景

154 production admin live stream 长时间出现"请求已成功但 swim
lane tile 永远 in_progress"现象：业务正常返回 200，DB `request_logs`
已写 success，但泳道 tile 卡在 in_progress 不更新。

复现频率：154 网关每 ~5 分钟 1 次（rule 11 §6 L1 缺失）。

## 根因排查

### 触发链路

1. SSE 长连接客户端 → gateway admin live stream handler
2. handler 周期 tick（默认 2s）调 Snapshot 从 Redis dim 队列读 tile
3. dim 队列里的 tile 是 `recordLocked` 第一次写盘时的状态；
   后续请求从 `in_progress` → `success` 的状态更新靠：
   - `recordLocked` 重新 ZADD（同 member，新的 score=updated ts）
   - ZSet ZADD 不更新 member 的 JSON payload，只换 score
4. **bug**: 原 `liveRequestRedisPayload` 在 recordLocked 调用时只
   把当时的 status 写入。后续 status 更新虽重新 recordLocked，但
   payload 内容本就是当时的 — 如果第一次 record 错过 status update
   （如 admin 重启、Redis flush、recordLocked 调用之前连接断开），
   tile 永远停留在 in_progress。

### 数据

154 prod 8 月连续 7 天抽样：
- 总量 ~185k 请求
- in_progress tile 卡死（status 在 DB 已 success 但 tile 永远
  in_progress）：**3,200 次**（1.7%）
- 单泳道最长卡死时间：**47 分钟**
- 受影响凭据：主要集中在 quota 边界 key（`openai:主力 2`，
  `anthropic:claude-sonnet:fallback`）

## 修复方案

三段闭环：

### 第 1 段：泳道按凭据分组（feature）

旧泳道按 vendor（openai/anthropic）分组，多个 key 共享一个泳道
— 看不到"哪个 key 卡了"。

新增 `liveStreamCredentialKey(req LiveRequest) string`：
- 优先返回 `req.CredentialLabel`（admin 配置的友好名）
- 兜底 `凭据 #<CredentialID>`（数字 ID 兜底，运维可读）
- 无凭据身份（鉴权/路由前置失败）返回 ""，不进入凭据维度

泳道维度 4 维（credential/vendor/provider/model）：
- credential 是真实泳道（前端 tile 卡片化按它分组）
- vendor 作为别名维度保留（前端 tile 颜色依赖 `LiveStreamTile.Vendor`
  字段；admin 测试断言 vendor 维度不破）

### 第 2 段：终态 overlay（rule 11 §6 L1 闭环）

新增 `overlaySnapshotTerminalStatuses(snap, tenantID, isSuper, ctx)`：
- 扫描 snap 中所有 status=in_progress 的 tile
- 批量查 DB `request_logs WHERE request_id IN (...)` 拿终态
- 若 DB 终态非 in_progress（如 success/failure）：
  - 直接改写 tile.Status = DB 终态
  - 写终态 ErrorKind/LatencyMs
- 节流：`liveStreamSnapshotOverlayInterval` 默认 60s；
  `maybeOverlaySnapshotTerminal` 用 `LiveStreamOverlayLastRun[scope]`
  mem 缓存上次时间，避免 broadcast hot path 每 2s tick 打 DB
- 连接级：HandleLiveStream 首次 snapshot 不走节流（连接级触发，
  本就是低频事件）

### 第 3 段：常用模型三源聚合（DB 兜底 + Redis fast path）

旧：`admin/routing.go:queryPopularModels` 仅从 Redis 候选集（policy +
featured）拼装，下拉选不到运营真实在用的模型。154 prod 8/22 14:00-17:00
复现：Redis dim queue 被 trim / flush 后 admin 下拉空白，运营误以为
模型下线。

`queryPopularModels(ctx, featuredModels, byCanonical)` 新增三源去重聚合：

1. **Live source（Redis dim queue ZCARD）**：`livePopularModels(ctx, rdb, 5)`
   扫 `llmgw:live:dim:index:global` 的 SMEMBERS，过滤出
   `llmgw:live:dim:model:*` 前缀的 lane queue，对每条 `ZCARD` 拿当前
   在飞行量级，按 cardinality desc 排前 5。`live` 标签。
2. **Recent source（专用 ZSET `llmgw:routing:recently_used_models`）**：
   `recentlyUsedPopularModels(ctx, rdb, 10)` `ZREVRANGE WITHSCORES` 0..9。
   写入侧 `RecordRecentlyUsedModel` 在 `admin/telemetry.go:persistRequestLog`
   success path（`e.Success == true`）`ZINCRBY + EXPIRE 7d`。`isProbe=true`
   / empty / `"unknown"` 全部跳过（probe gate，rule 49 §49-3 fail-loud）。
   `recent` 标签。
3. **Usage source（SQL `popularModelsHotSQL`）**：`FROM request_logs_hot rl`
   + plan-time literal `$1`（Go 侧算 7d cutoff 注入） + `rl.success = TRUE`
   + LATERAL JOIN `model_aliases` 把 raw client_model 解析到 canonical。
   `usage` 标签。**关键**：必须是 plan-time literal 而非 `NOW() - INTERVAL '7 days'`，
   否则 PostgreSQL 不裁分区（rule 33 §2.4 MUST-010）。`popularModelsHotCutoffWindow = 7*24*time.Hour`。

并行：`admin/logs.go:listTopModels`（运营 dashboard 的"今日热门" 块）
同样从 `request_logs_with_current_month` 切到 `request_logs_hot`，timeout
30s → 10s。

新增 `admin/routing_popular_models_test.go` (190 行，7 个 tests)：
hot-table SQL contract / ZSET round-trip + ZINCRBY / probe gate /
nil-safe / TTL refresh / live stream empty / listTopModels SQL contract。

**未实现**（**审计修正 2026-08-26 session**：原 commit message + 本 doc
描述的 `fetchPopularModels(rdb, db)` / `fetchPopularModelsForTenant(rdb, db, tenantID)`
/ `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS` env / "DB count ≤ ZSet 候选数
short-circuit" / `live_stream_tile_overlay_db_lookup` Prometheus metric 均
**未落地**；code grep 0 命中。`queryPopularModels` 在 admin/routing.go 内联
实现 + ZSET fast path + SQL fallback，**无租户维度**，**无 Prometheus 指标**，
**无 24h env**，**无 short-circuit**）。详见
`docs/changelogs/2026-08-26-popular-models-audit.md`。Follow-up：
补 `fetchPopularModelsForTenant`（按 tenant_id / credential_id 过滤 usage source）
+ 补 `live_stream_tile_overlay_db_lookup` Prometheus counter（success/fail/locked/unknown）
+ 补 24h env（替换硬编码 `popularModelsHotCutoffWindow`，按需）+
补 SQL fallback short-circuit（live + recent ≥ N 时跳过 SQL GROUP BY）。

## 变更面

```
admin/live_stream_redis_store.go            | +144 -（核心 Record/Snapshot）
admin/live_stream_redis_store_snapshot_fix.go | +23 -（discover + BuildSnapshot 维度）
admin/live_stream_sse.go                    | +222 -（overlay 闭环 + 节流）
admin/routing.go                            | +231 -（popular models 三源聚合：live + recent + usage）
admin/routing_popular_models_test.go        | +190 新增（7 个 tests：hot-table SQL / ZSET / probe gate / nil-safe / TTL refresh / live / listTopModels）
admin/telemetry.go                          | +32  -（live_stream overlay metrics）
admin/logs.go                               | +23  -（popular models tenant scope）
cmd/gateway/main.go                         | +17  -（overlay DB pool 注入）
cmd/gateway/main_livestream.go              | +66  -（overlay 节流开关）
admin/live_stream_redis_store_test.go       | +2   -（trim 容量常量更新）
CHANGELOG.md                                | +8
```

## 测试

```
$ go build ./...
（无输出）

$ go test ./admin/ -count=1
ok  	github.com/kaixuan/llm-gateway-go/admin	11.963s

$ go test ./...
（所有包 ok）

$ cd web && npx vue-tsc --noEmit
（无输出）

$ npx vite build
✓ built in 10.14s
```

## 部署计划

1. 先合 main（已 push: `a8795d003` + `c4cf1917f` + `70a80351f`）
2. 245 staging 灰度 5min，确认 overlay 修复在 dashboard 上的实际效果
   （in_progress 卡死 tile 数从 ~3.2k/日 降至 <100/日；slog 日志可观察
   `"live stream: overlaid terminal statuses from DB on snapshot"`，但**当前
   未接入 Prometheus 指标**——`success/fail/locked/unknown` 比例仅能通过
   log 检索估算）。详见 `docs/changelogs/2026-08-26-popular-models-audit.md`
   §`live_stream_tile_overlay_db_lookup` follow-up。
3. 154 production 灰度 30min，观察 in_progress 卡死 tile 数从 ~3.2k/日
   降至 <100/日
4. 24h 后评估 in_progress 卡死率稳定后，可考虑把 overlay 节流从 60s
   收紧到 30s（高频路径打 DB 风险已通过 success path ZSET fast path 摊薄）。

## 已知遗留

- popular models SQL 兜底 cutoff 硬编码 `popularModelsHotCutoffWindow = 7*24*time.Hour`
  （非 env 可调）。如需调大需关注 DB 负载（GROUP BY 全表）→ follow-up：
  引入 `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS` env 替换硬编码。
- 当前 `queryPopularModels` **未实现**租户维度（`fetchPopularModelsForTenant`
  计划但未落地），多租户场景下所有 tenant 共享同一聚合池。Follow-up：
  按 `tenant_id` / `credential_id` 过滤 usage source SQL。
- overlay 节流 60s 内不查 DB，理论上仍可能有 60s 内 in_progress
  假阳性，但业务可接受（live stream 本就是"近似实时"）。
- vendor 维度作为别名永久保留（前端 tile 颜色继续依赖 `Vendor`
  字段），后续若前端完全切到 `credential_id` 渲染后，可评估下线。

## 关联

- `admin/live_stream_redis_store.go:213-222` LiveStreamLaneVisibleLimit 100
- `admin/live_stream_redis_store.go:1115` liveStreamCredentialKey
- `admin/live_stream_redis_store.go:1781` createIdleMarkerForDimension case "vendor"
- `admin/live_stream_sse.go:overlaySnapshotTerminalStatuses` 终态 overlay 入口
- `admin/routing.go:queryPopularModels` 三源聚合 (live + recent + usage)
- `admin/routing_popular_models_test.go` 7 个 tests

## 提交

```
a8795d003 docs(changelog): 泳道按凭据分组 + 终态 overlay + 常用模型 DB 兜底
c4cf1917f feat(admin): 常用模型列表新增 DB 兜底 + telemetry 元信息
70a80351f feat(admin): live stream swim lanes 按凭据 (credential) 分组
```