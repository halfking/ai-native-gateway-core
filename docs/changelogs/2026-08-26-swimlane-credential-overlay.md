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

### 第 3 段：常用模型 DB 兜底

旧：popular models 从 Redis ZSet `llmgw:live:popular_models` 读。
ZSet 被 trim 或 Redis flush → 前端下拉空白（154 8/22 14:00-17:00 复现）。

`fetchPopularModels(rdb, db)`：
- 先查 ZSet 命中 → 直接返回
- ZSet miss/空 → 走 DB：`SELECT model, COUNT(*) FROM request_logs
  WHERE created_at > now() - interval '24h' GROUP BY model ORDER BY 2 DESC LIMIT 20`
- DB 失败 → 返回空列表（fail-soft，不阻塞 admin 其他模块）
- 含「DB count ≤ ZSet 候选数」short-circuit，避免每次刷新都
  GROUP BY（高负载时可能 100ms+）

`fetchPopularModelsForTenant(rdb, db, tenantID)`：
- tenant scope 单独走 DB `request_logs_default` 过滤 `tenant_id`，
  避免 tenant A 数据污染 tenant B。

## 变更面

```
admin/live_stream_redis_store.go            | +144 -（核心 Record/Snapshot）
admin/live_stream_redis_store_snapshot_fix.go | +23 -（discover + BuildSnapshot 维度）
admin/live_stream_sse.go                    | +222 -（overlay 闭环 + 节流）
admin/routing.go                            | +231 -（popular models DB fallback）
admin/routing_popular_models_test.go        | +181 新增（4 类场景测试）
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
2. 245 staging 灰度 5min，确认 overlay metrics `success/locked/unknown`
   比例在正常区间（success 80-95%、unknown < 5%、locked ≈0）
3. 154 production 灰度 30min，观察 in_progress 卡死 tile 数从 ~3.2k/日
   降至 <100/日
4. 24h 后回收 DB overlay 兜底开关（节流版）。

## 已知遗留

- popular models DB 兜底 `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS`
  默认 24h（env 可调），如调大需关注 DB 负载（GROUP BY 全表）。
- overlay 节流 60s 内不查 DB，理论上仍可能有 60s 内 in_progress
  假阳性，但业务可接受（live stream 本就是"近似实时"）。
- vendor 维度作为别名永久保留（前端 tile 颜色继续依赖 `Vendor`
  字段），后续若前端完全切到 `credential_id` 渲染后，可评估下线。

## 关联

- `admin/live_stream_redis_store.go:213-222` LiveStreamLaneVisibleLimit 100
- `admin/live_stream_redis_store.go:1115` liveStreamCredentialKey
- `admin/live_stream_redis_store.go:1781` createIdleMarkerForDimension case "vendor"
- `admin/live_stream_sse.go:overlaySnapshotTerminalStatuses` 终态 overlay 入口
- `admin/routing.go:fetchPopularModels` DB 兜底
- `admin/routing_popular_models_test.go` 4 类场景

## 提交

```
a8795d003 docs(changelog): 泳道按凭据分组 + 终态 overlay + 常用模型 DB 兜底
c4cf1917f feat(admin): 常用模型列表新增 DB 兜底 + telemetry 元信息
70a80351f feat(admin): live stream swim lanes 按凭据 (credential) 分组
```