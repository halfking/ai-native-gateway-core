# 经验总结：仪表盘泳道数据跳动（Redis 读取超时）(2026-08-04)

> **场景**：实时请求流"仪表盘"页面，按供应商分维时 minimax 泳道条数在 20 ↔ 3 之间跳动。
> **修复 commit**：`ba0a7e02` fix(live-stream): 修复仪表盘泳道数据跳动（Redis 读取超时）
> **影响范围**：245 预发布 + 154 生产（已全量上线）

---

## 1. 背景

### 1.1 现象
用户在仪表盘"实时请求流"页面观察到泳道条数不稳定——按供应商（vendor）分维时，minimax 泳道的 tile 数在 20 条（一帧）→ 3 条（下一帧）→ 20 条之间来回跳。NVIDIA NIM、普联等其他供应商也有类似现象但频率较低。

### 1.2 排查链路
1. 用户报告"按供应商分维，minimax 一会 20 一会 3"
2. 排查 redis 多维泳道缓存数据长度 → `ZCARD` 连测 5 次稳定 = 20（队列无问题）
3. 排查 SSE→Vue 数据更新 → 无并发 bug、前端忠实反映后端推送
4. **抓 245 网关 stderr 日志** → 发现 `snapshot: failed to read dimension queue`、`err: context deadline exceeded` 大量出现，`total_requests` 在 258 ↔ 8/34/165 之间跳

### 1.3 关键链路
```
新请求 → Record() → Redis 维度队列 (ZSET, 24h TTL)
  ↓
PUBLISH llmgw:live:events
  ↓
SSE hub (Run loop, 245/154) → computeScopeDelta()
  ↓
SnapshotFromDimensionQueues() → SCAN + ZRevRange + GET 详情
  ↓
ComputeDelta(cached, snapshot) → SSE 推 delta
  ↓
前端 Vue mergeDelta → mergeTilesById (整盘替换 + 截断 20)
```

---

## 2. 事故时间线

| 时间 | 事件 |
|---|---|
| 2026-07-19 | 引入 `SnapshotFromDimensionQueues`（admin/live_stream_redis_store_snapshot_fix.go），从 main queue 读取改为从维度队列读取 |
| 2026-07-23 | 超时从 200ms 放宽到 2s（注释 "改为 2 秒，覆盖 SCAN 全量（27+ 维度队列）"） |
| 2026-07-20 | 已写过根因文档 `docs/245-swimlane-jump-real-root-cause.md`，但归因错误（认为是 pushFullSnapshots / idle marker 的全量 delta）|
| 2026-07-27 | 引入 per-request_id SETNX 锁（commit `live_stream_redis_store.go:308-330`），但只解决"重复 ZSET 成员"，没解决超时 |
| 2026-08-04 (10:00) | 用户报告 "minimax 一会 20 一会 3" |
| 2026-08-04 (10:30) | 排查：ZCARD 稳定、Redis 队列无问题；日志显示 40% 读取因 `context deadline exceeded` 残缺 |
| 2026-08-04 (11:30) | 实施三方案修复：残缺检测 + pipeline 批量化 + SET 索引替代 SCAN |
| 2026-08-04 (12:00) | 245 部署验证（source=index、零超时、total_requests 稳定）|
| 2026-08-04 (12:30) | 154 部署验证 + Redis 慢查询 dim scan = 0 |

---

## 3. 根因分析

### 3.1 直接原因：snapshot 读取被 2s 超时截断

`SnapshotFromDimensionQueues`（`live_stream_redis_store_snapshot_fix.go:38`）读取一次 snapshot 需要：

| 步骤 | 实测耗时 | 操作数 |
|---|---|---|
| SCAN `llmgw:live:dim:*` (在 12.8 万 keyspace 下) | **16-18ms/次** | ~3 次迭代 |
| 对 42 个维度队列**串行** `ZRevRange` | **1.1-1.8s/次** | 42 次 |
| 对 ~840 个成员**串行** `GET llmgw:live:req:<id>` | ~2ms/次（远程 RTT）| 840 次 |

总耗时远超 `computeScopeDelta` 的 **2s 超时预算**（`live_stream_sse.go:424/995`、`pushScopeSnapshot:670`）。

### 3.2 根本原因：三个设计缺陷叠加

1. **串行读取 → RTT 爆炸**：每维度队列一次 ZRevRange、每个成员一次 GET，没有 pipeline。
2. **超时预算太紧**：2s 不够 SCAN + 42×ZRevRange + 840×GET 在远程共享 Redis 上完成。
3. **大 keyspace 下 SCAN 慢且刷慢查询**：`scan count 10000` 在 12.8 万 key 的共享 Redis (`172.16.2.210:6389`) 下需多次迭代，每次进 Redis 慢查询日志，加剧 Redis 实例压力。

### 3.3 关键矛盾
- `SnapshotFromDimensionQueues` 内部使用传入的 ctx，超时后 ctx 取消，**剩余的 ZRevRange/Get 因 ctx cancelled 返回 error 并被 `continue` 跳过**，但**已读成员仍组成"残缺" snapshot 返回**（无 error）。
- `computeScopeDelta` 拿到这个残缺 snapshot 后，与 cached 比较算 delta，把残缺数据推给前端。
- 前端 `mergeTilesById`（`liveStreamStore.ts:697-714`）整盘替换：minimax 泳道从 20 瞬间变 3 或消失。
- 5 分钟后下一次 `maybeEmitIdleMarker` 触发新一轮 `computeScopeDelta` → 这次完整读 → 恢复 20 → 视觉上是"跳变"。

### 3.4 量化（生产日志 245，部署前）
- 总 snapshot 数：50 次
- **残缺（total < 200）：20 次（40%）**
- total_requests 分布：258(正常)、165、142、115、113、101、96、87、63、49、48、34、27、23、20、8、3
- 慢查询日志 128 条**全是** `scan count 10000`（discoverDimensionQueues 产生）
- 每次 snapshot 完整读耗时 ~5-8s（远超 2s 超时）

---

## 4. 修复方案（三层防线）

### 4.1 方案 A — 残缺检测（治标，立即止血）
**文件**：`admin/live_stream_sse.go` `computeScopeDelta`

新增常量 + 守卫：当 cached 已有基线（total ≥ 20）且本次 snapshot total < cached 的 40% → 判定为超时残缺，**丢弃、不更新 cached、不推前端**。前端保留上次好数据，下次完整 snapshot 自动修正。

```go
if cached != nil && cached.Summary.Total >= degradedSnapshotMinBaseline &&
    snapshot.Summary.Total*100 < cached.Summary.Total*degradedSnapshotThresholdPct {
    atomic.AddInt64(&h.cachedSnapshotDegradedSkips, 1)
    slog.Warn("live stream: degraded snapshot skipped (likely Redis read timeout)", ...)
    return nil
}
```

新增计数器 `cachedSnapshotDegradedSkips` 用于观测误杀。

### 4.2 方案 B — pipeline 批量化（治本）
**文件**：`admin/live_stream_redis_store_snapshot_fix.go` `SnapshotFromDimensionQueues`

读取循环重构为两阶段 pipeline：
- Phase 1：1 次 pipeline 把 42 个 `ZRevRange` 批量发出 → 1 次 RTT
- Phase 2：1 次 pipeline 把去重后的 ~840 个 `Get` 详情批量发出 → 1 次 RTT

总 RTT 从 ~882 次降到 2 次。搭配超时从 2s 放宽到 5s（`liveStreamSnapshotReadTimeout` 常量，统一 3 处调用点），实测读取降到 <1s。

### 4.3 方案 C — SET 索引替代 SCAN（消灭慢查询源头）
**写侧**：`admin/live_stream_redis_store.go`
- `recordLocked` 对每个 dim queue key 同步 `SAdd` 到作用域索引 SET
- `ScanAndRecordIdleMarkers` 同样
- 新增 `liveStreamDimIndexKey()`、`isDimensionQueueKey()`、`isGlobalDimKey()`

**读侧**：`admin/live_stream_redis_store_snapshot_fix.go` `discoverDimensionQueues`
- 优先 `SMembers 索引 SET`，缺失时降级 SCAN（零停机上线不丢泳道）
- 日志新增 `source` 字段（index / scan-fallback）

**效果**：dim scan 从刷屏 → **0**（slowlog 最新 50 条无 `llmgw:live:dim` 的 scan）。

---

## 5. 验证结果（生产实测）

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `total_requests` | 258↔8/34/165（40% 残缺）| 127/130/192（全量稳定）|
| `failed to read dimension queue` | 465 次 | **0** |
| `context deadline exceeded` | 大量 | **0** |
| `degraded snapshot skipped` | — | 0（方案 B 治本后无需兜底）|
| `dimension queues discovered source` | scan | **index** |
| Redis 慢查询 `llmgw:live:dim` scan | 刷屏 | **0** |
| minimax ZCARD 连测 5 次 | 20 | **20**（稳定）|

部署链路：245 预发布（seq 1431, 24s）→ 154 生产（seq 1432, 48s），均通过 seamless 无感部署，healthz + DB + admin 密码同步全部自动验证通过。

---

## 6. 关键经验教训

### 6.1 ⚠️ CRITICAL — 共享 Redis 上的串行读是性能陷阱
**教训**：跨机器远程 RTT 下，每多一次串行 Redis 操作就多 ~2ms。看似无害的"42 次 ZRevRange + 840 次 GET" = ~882 × 2ms ≈ 1.7s，**与 2s 超时预算擦边**，任何一次 Redis 抖动就会超时。

**规则**：
- 任何批量 Redis 读取 ≥ 5 次时**必须**用 pipeline
- 单次 pipeline 内的 cmd 数不限，但要把 ctx deadline 设为"单次 RTT × 2"以上
- 共享 Redis 上的 SCAN 是性能炸弹——优先用 SET/ZSET 注册表替代

### 6.2 ⚠️ CRITICAL — "返回数据" ≠ "返回完整数据"
**教训**：`SnapshotFromDimensionQueues` 在 ctx 超时时仍返回非 nil snapshot（残缺但无 error）。`computeScopeDelta` 无法用 `err != nil` 区分"正常空"和"超时残缺"。

**规则**：
- 任何"读取大量数据"的函数必须有**幂等性 + 完整性自检**（如本例的 `Summary.Total` 与上次比较）
- 超时场景下，宁可"丢弃本次、保留上次"，不要"推送残缺数据"
- 哨兵字段（cached 计数、空 marker、CRC 校验）是必须的，不能只依赖 `err != nil`

### 6.3 ⚠️ HIGH — 超时预算要从"实测最坏情况"出发，不是"理想情况"
**教训**：原注释 "2 秒覆盖 27+ 维度队列" 是开发环境（小 keyspace）的乐观估计。生产 12.8 万 key 共享 Redis 上 SCAN 自身就要 50-200ms。

**规则**：
- 设定超时时必须**实测生产最坏情况**的 2-3 倍作为预算
- 超时调整需要同步更新所有调用点的注释，避免"忘了改"
- 引入 `liveStreamSnapshotReadTimeout` 这种**单一常量 + 全文 grep 替换**比硬编码好

### 6.4 ⚠️ HIGH — 慢查询日志是最便宜的可观测性
**教训**：Redis `slowlog` 是默认开启、低开销、立即可读的诊断入口。本次根因能在 5 分钟内定位，全靠 `redis-cli slowlog get` 看到 128 条 `scan count 10000`。

**规则**：
- 任何对共享 Redis 的"高成本操作"（SCAN、KEYS、SMEMBERS 大集合）必须能解释慢查询日志中是否刷屏
- 每月做一次慢查询审计：`SLOWLOG GET 100` 看 top commands
- 引入"高基数 key 注册表"（SET/ZSET）替代 SCAN 是治本措施

### 6.5 ⚠️ MEDIUM — "按需查询"不如"主动注册"
**教训**：`discoverDimensionQueues` 每次 snapshot 都 SCAN 全 keyspace 找 ~40 个 dim key，是典型的"按需查询反模式"。换成 SET 注册表后变成 O(1) SMEMBERS。

**规则**：
- 写入路径已知 keys 的，**写入时**就 SAdd 到索引 SET
- 索引 SET 的 TTL ≥ 队列 TTL，让索引先于队列消失
- 读取路径必须有"索引缺失降级 SCAN"的兜底（零停机上线不丢数据）

### 6.6 ⚠️ MEDIUM — 被动发现 = 永远滞后
**教训**：本次问题直到用户肉眼看到"20↔3 跳动"才被发现。中间 14 天（7-19 → 8-04）一直存在但无人察觉。

**规则**：
- 关键路径必须有"异常计数器 + 日志告警"（如本例 `cachedSnapshotDegradedSkips` 应接 alerts）
- Redis `scan` 在 slowlog 出现 ≥ N 次/分钟 应触发告警
- 仪表盘的"稳定性"应纳入 SLO（如"minimax 泳道条数 5 分钟内方差 < 5"）

### 6.7 ⚠️ LOW — 文档可能错，但日志不会
**教训**：`docs/245-swimlane-jump-real-root-cause.md`（7-20 写的）把根因归到 "pushFullSnapshots / idle marker 全量 delta"，是错的。真正根因是**读取超时残缺**——但当时日志没有足够上下文（旧的 `cachedSnapshotEmptySkips` 计数器只看 total=0，不看 total 缩水）。

**规则**：
- 根因文档必须基于**生产日志实测**，不能基于代码推测
- 修复方案上线的 commit 必须能 link 到原文档（如本例 ba0a7e02 引用了 `docs/245-swimlane-jump-real-root-cause.md`）做"假说 vs 真相"对比

---

## 7. 后续预防项（已识别的 TODO）

| 优先级 | 任务 | 说明 |
|---|---|---|
| P1 | 加告警：`cachedSnapshotDegradedSkips` > 10/分钟 → 告警 | 方案 A 的计数器已存在，需要接告警系统 |
| P1 | 加告警：Redis slowlog 中 `scan count >= 5000` > 5/分钟 → 告警 | 防止再次出现 SCAN 滥用 |
| P2 | 监控 `cachedSnapshotEmptySkips` 与 `cachedSnapshotDegradedSkips` 的比值 | > 0.5 即意味着超时普遍发生 |
| P2 | 仪表盘前端 `mergeTilesById` 加"渐进收敛"保护 | 当前整盘替换是放大器，治标（已讨论过，权衡风险与收益后未实施）|
| P3 | 把 `liveStreamSnapshotReadTimeout` 暴露为 `LiveStreamConfig` 字段 | 当前是硬编码常量，env 覆盖更灵活 |
| P3 | `domains/credential/rpm_redis.go` 等其他共享 Redis 使用方做同样 SCAN 审计 | 防止 `pms-redis`（共享）被其他子系统再拖累 |
| P3 | 考虑把 `pms-redis` 拆分为多实例（按系统隔离） | 97.6% 的 key 是 PMS session，llmgw 用量 2.4% |

---

## 8. 相关文件

| 角色 | 路径 |
|---|---|
| 修复 commit | `ba0a7e02` fix(live-stream): 修复仪表盘泳道数据跳动（Redis 读取超时）|
| 后端 - 残缺检测 | `admin/live_stream_sse.go` `computeScopeDelta`（含 `degradedSnapshotMinBaseline`/`degradedSnapshotThresholdPct`）|
| 后端 - pipeline 化 | `admin/live_stream_redis_store_snapshot_fix.go` `SnapshotFromDimensionQueues` |
| 后端 - 索引 SET | `admin/live_stream_redis_store.go`（`recordLocked`、`ScanAndRecordIdleMarkers`、`liveStreamDimIndexKey`、`isDimensionQueueKey`、`isGlobalDimKey`）|
| 后端 - 索引读取 | `admin/live_stream_redis_store_snapshot_fix.go` `discoverDimensionQueues`、`discoverDimensionQueuesByScan`（降级路径）|
| 后端 - 超时常量 | `admin/live_stream_sse.go` `liveStreamSnapshotReadTimeout`（5s，3 处统一）|
| 单测 | `admin/live_stream_redis_store_test.go` `TestComputeScopeDelta_DropsDegradedSnapshot` / `AcceptsNonDegradedSnapshot` / `TestLiveStreamDimIndex_PopulatedAndRead` / `TestLiveStreamDimIndex_FallbackToScan` / `TestIsDimensionQueueKey` |
| 诊断脚本 | `scripts/diagnose-swimlane-jump.sh` |
| 旧的根因文档（部分错） | `docs/245-swimlane-jump-real-root-cause.md`、`SWIMLANE_JUMP_DIAGNOSIS.md` |
| 旧的 HANDOFF | `docs/HANDOFF_SWIMLANE_JUMPING_ISSUE.md` |
| 部署版本 | `version.json` 2.4.9-ba0a7e02-20260804-1432（245 seq 1431, 154 seq 1432）|

---

## 9. 反思：12.7 万 Redis key 的真实归属

排查中连带查清了一个**误判**：生产 Redis `db0 keys=128700` 看起来很多，但实际归属是：

| 命名空间 | key 数 | 占比 | 归属 |
|---|---|---|---|
| `session:key` | 31,637 | 24.7% | **PMS 系统**（与 llmgw 无关）|
| `session_pref:*` | 13,516 | 10.6% | **PMS 系统** |
| `session:sc` + `session:apiKey` | 292 | 0.2% | **PMS 系统** |
| 其他（`pending_response`、`ursm:v2`、`llmgw:*`）| ~1,855 | 1.4% | llmgw |
| **llmgw 小计** | **~1,800** | **1.4%** | llmgw |
| **PMS + 其他小计** | **~125,700** | **98.6%** | PMS 等 |

**结论**：llmgw 在共享 Redis 里**仅占 1.4%**。剩余 98.6% 是 PMS 系统自己产生的（session 数据）。这不是 llmgw 的责任，但也提示了：共享 Redis `172.16.2.210:6389` 上的 SCAN 性能问题，llmgw 和 PMS 都会受影响。**未来拆分 Redis 实例是值得考虑的优化**（见 §7 P3）。

**附：llmgw 自己的 key 全部有合理 TTL**，无累积风险。详情：

| llmgw 前缀 | 数量 | TTL | 评估 |
|---|---|---|---|
| `llmgw:live:req:*` | 683 | ~3.3h | ✅ |
| `llmgw:live:tenant:*:req:*` | 693 | ~3.5h | ✅ |
| `llmgw:live:dim:*` / `tenant:*:dim:*` | 86 | ~24h | ✅ |
| `llmgw:live:dim:index:*` | 3 | ~24h | ✅（本次新增）|
| `llmgw:avail:*` | 200 | ~3.86h（生产实测；4h 设的）| ✅ |
| `llmgw:cred_fp_node:*` | 6 | ~1h | ✅ |
| `llmgw:callhist:*` | 7 | ~2h | ✅ |
| `llmgw:credstate:*` | 6 | ~1-5min | ✅ |
| `llmgw:monitor:node:recent_success:*` | 5 | ~5min | ✅ |
| `llmgw:blocklist:*` | 3 | ~3min | ✅ |
| `llmgw:stats:*` | 7 | 10min-7day | ✅ |
| `pending_response:gw_*` | 120 | ~2-5min | ✅ |
| `llmgw:disguise:*` | 3 | `ttl=-1` 无过期 | ✅ **设计正确**（`disguise/pool.go:30-32`：UA 池/语言池/最后轮转时间是持久化系统元数据，不能过期）|
---

## 10. 后续行动：llmgw 切到 db=2 与共享 Redis 隔离（2026-08-04 落地）

### 10.1 决策
§7 P3 的"拆分 pms-redis 多实例"短期不可行（需要 PMS 团队协同+新资源），但可以**先在共享实例内做 db 隔离**——把 llmgw 切到 db=2，db=0 留给 PMS。这样：

- 未来 `KEYS *` / `SCAN` 不再误扫到对方 keys
- 共享 Redis 上的慢查询 SCAN 数量减半（llmgw 走索引，PMS 自己 SCAN）
- 后续若要拆实例，llmgw 只需要把 `db=2` 改成一个独立实例的 `db=0`，无代码改动

### 10.2 实施（commit `e94d7a55`）
- `config/config.go`：`cfg.RedisDB` 默认值 0 → 2（env `LLM_GATEWAY_REDIS_DB` 仍可覆盖）
- `domains/credential/rpm_redis.go`：rpm limiter 自动从 `LLM_GATEWAY_REDIS_DB` 拿 db，保证 rpm 与主网关 db 一致
- 单测：`TestNewRPMLimiterFromEnvPicksGatewayDB` 验证 db=0/2/5 三种情况
- `.env.example`：补 `LLM_GATEWAY_REDIS_ADDR/DB/PASSWORD` 文档

### 10.3 部署
- 245：`.env` 改 `LLM_GATEWAY_REDIS_DB=2`，seq=1436
- 154：env 无 `LLM_GATEWAY_REDIS_DB`，走代码默认 db=2，seq=1437
- 两个 release 都验证 healthz + DB + admin 密码同步通过

### 10.4 验收（生产实测）
- db=0 仅剩 PMS session（131422 keys），不再有 llmgw 新增
- **db=2 收到 312 个 llmgw keys**（含 `llmgw:avail:*`、`llmgw:stats:*`、`llmgw:live:dim:index:global`、`llmgw:live:dim:index:tenant:default` 等）
- 154 live stream 日志：`source=index`、`total_keys=6` 持续工作
- rpm limiter 走 db=2（245 当前流量小未触发 key 产生，但单测覆盖了 db 传递逻辑）

### 10.5 数据迁移策略
- **不迁移** db=0 的 ~1800 个 llmgw 历史 key
- 这些 key 全部有合理 TTL（4h-24h），最迟 24h 后自动过期清空
- 过渡期内 db=0 仍有 llmgw "残影"，不影响功能
