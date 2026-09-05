# 2026-07-28: 仪表盘实时请求流空闲块丢失修复

## 现象

仪表盘实时请求流（Live Stream Swim Lane）在某个 vendor/provider/model
泳道长时间没有新请求时，应该在该泳道右端显示一个"空闲 N 分钟"的色块
（idle marker tile），用于告知运维人员"这个通道在等业务来"。从用户反馈
和 24h 审计报告来看，这个色块最近"突然消失"了。

## 根因（Phase 1-2 调研结论）

经系统排查（git log -S 检索 + 代码 path trace + Explore 子代理验证），
确认 **空闲记录功能代码一直存在，但被"读路径不可见"导致失效**。

### 写入路径

`admin/live_stream_redis_store.go:1374-1385` `idleMarkerQueueKeys()` 把
idle marker 写到 **main queue**（`llmgw:live:main` 或
`llmgw:live:tenant:{id}:main`）：

```go
// 旧实现
func idleMarkerQueueKeys(tenantID, dimension, dimensionKey string) []string {
    if strings.TrimSpace(tenantID) == "" {
        return []string{liveStreamMainKey}
    }
    return []string{tenantLiveStreamKey(normalizeLiveStreamTenant(tenantID), "main")}
}
```

### 读取路径

生产热路径使用 `SnapshotFromDimensionQueues`（位于
`admin/live_stream_redis_store_snapshot_fix.go:38-181`），它**只读
dimension queues**（`llmgw:live:dim:*`），完全不读 main queue：

```go
// SnapshotFromDimensionQueues 第 78 行
members, err := s.rdb.ZRevRange(ctx, key, 0, int64(LiveStreamLaneVisibleLimit-1)).Result()
// key 是 llmgw:live:dim:vendor:* 等
```

`computeScopeDelta` (`admin/live_stream_sse.go:500-555`) 的 fallback
逻辑（line 527-533）只在 `snapshot.Summary.Total == 0` 时才回退到
`Replay()`（读 main queue）。**只要任一 dim queue 有任何数据，main queue
中的 idle marker 永远不可见。**

### 历史背景

- `c5261c7e fix(live-stream): idle markers, no-candidate probe, swim-lane flicker`
  — 首次实现 idle marker
- `f5eed122 refactor(live-stream): deprecate Snapshot in favor of SnapshotFromDimensionQueues`
  — 引入 dim-queue 读取路径，**没有同步更新 idle 写入路径**
- 后续若干次 `fix(live-stream)` 提交（d5c020f4、fad4683e、b0eefa1d 等）
  都没有触碰 idle 写入

### 测试盲点

`admin/live_stream_redis_store_test.go` 中所有 idle 相关测试都用
`store.Replay()`（读 main queue）或 `rdb.ZRange(main, ...)` 验证，**从不
触发 `SnapshotFromDimensionQueues` 这条生产路径**，所以这个 bug 一直
没被发现。

## 修复

### 改动 1：`idleMarkerQueueKeys` 同时写入 dim queue

文件：`admin/live_stream_redis_store.go`

把 idle marker 镜像写入对应的 dim queue，让
`SnapshotFromDimensionQueues` 也能读到：

- **Global scope**：写入 main queue + global dim queue
- **Tenant scope**：写入 tenant main queue + tenant dim queue；不再写 global dim queue，否则超管视图会把多个 tenant 的同一泳道 idle marker 合并成多个相同空闲块
- **Dimension key 防御**：`dimensionKey` 为空时 fallback 到 main queue only
- **Dimension key 转义**：`:` 和 `/` 替换为 `_`，与 `idleMarkerRequestID` 一致

### 改动 2：`ScanAndRecordIdleMarkers` 用 scan time 作为 score

文件：`admin/live_stream_redis_store.go`

旧行为：score = `lastActivity + idleThresholdSeconds`（沉默开始那一刻）。
ZADD 同一 member + 同一 score 是 no-op，TTL 不刷新。

新行为：score = `ts`（每次 tick 刷新）。每次 tick 都是一次真实 ZADD，
TTL 刷新，Ts 推进，前端"空闲 X 分钟"显示实时累加。

### 改动 3：测试更新

- `TestIdleMarkerAnchorsAtSilenceStart` → `TestIdleMarkerUsesScanTimeAsTs`
  （反映新语义：Ts 是 scan time，不是 silence start）
- `TestIdleMarkerQueueKeys_ScopeRouting` 重写为 4 个子测试，覆盖
  global/tenant/empty-key/unsafe-char 四种场景
- 新增 5 个回归测试覆盖修复点：
  1. `TestIdleMarker_VisibleInDimensionQueueSnapshot` — 关键回归：dim
     队列里的 idle marker 必须能被 `SnapshotFromDimensionQueues` 看到
  2. `TestIdleMarker_StableRequestIdAcrossTicks` — 验证"末尾已 idle 则
     更新不新增"（3 个 tick 后 ZSet 中只有 1 个 idle marker，RequestID 稳定）
  3. `TestIdleMarker_PushedRightByNewRequest` — 验证"新请求把 idle 推到
     最左"（按 score 倒序：新请求 → idle → 老请求）
  4. `TestIdleMarker_RefreshesTsOnEachTick` — 验证"更新空闲时间"
     （两次 tick 之间 detail hash 中的 Ts 推进）
  5. `TestIdleMarker_BothMainAndDimQueueUpdated` — 验证双写
     （main queue 和 tenant dim queue 都包含 idle marker，score 一致）

## 验证

```bash
$ go build ./...                          # OK
$ go vet ./admin/                         # clean
$ go test ./admin/ -count=1               # ok 3.342s
$ go test ./admin/ -count=1 -race         # ok 4.549s
$ golangci-lint run --timeout 60s ./admin/ # 仅有 pre-existing issues，与本修复无关
```

## 兼容性

- 写入是 idempotent：同 member + new score = ZSet 更新（不是插入）
- main queue 写入保留：向后兼容 `Replay()` 路径和现有 main queue 测试
- 前后端契约不变：前端 `RequestTile.vue:78-85` 的 `idleElapsedMinutes`
  计算和 `liveStreamStore.ts:430-437` 的 idle_marker envelope 处理都
  不需要改
- TTL 假设不变：`liveStreamLaneQueueTTL` (24h) / `liveStreamTTL` (4h)
  不动

## 风险评估

- 风险低：仅 Redis 写入双写，不影响 API 行为
- 回归保护：5 个新测试 + 2 个重写测试覆盖所有改动点
- 回滚容易：单次提交，单文件改动 3 个函数
