# 83-R88-t：live-stream 丢弃指标的 label 可达性复核 + 告警规则草案（待裁决 28）

- 轮次：R88-t（R88 收尾项；R88 主体已收口并交接，本项是 28 号裁决的前置取证）
- HEAD 基线：`e6ac20b11`
- 触发：R88 收口时把「是否补建 live-stream scoped 告警」列为待裁决第 28 条，但**当时没有把前置条件核实完**
- 本轮定位：把该条从「一个待办」做成「可直接批准或否决的决策包」
- 结论：**建议补建，且只需 scope 到 `reason="redis_unavailable"` 即无告警风暴风险**；规则草案见 §6，**本轮不创建**（会改变运维侧页面行为，属裁决项）
- 改动：**零生产代码、零配置、零门**。仅本报告 + README 索引

---

## 1. 为什么先核实前置条件而不是直接写规则

待裁决 28 的原始表述是「是否补建 live-stream scoped 告警规则（仅 `reason="redis_unavailable"`，否则未接 Redis 的部署会永久告警风暴）」。这句话里已经含一个**未经核实的前提**：未接 Redis 的部署会不会真的持续产生 `redis_unavailable`。

**如果那个前提错了，scope 就是多余的，规则可以更简单；如果前提对了一半，scope 的写法就决定了会不会误报。** ⇒ 本轮把「两个 label 在生产各自是否可达」重新从头推一遍。

**纪律**：不采信上一轮结论，回原代码重推（§10）。本轮实际推翻了上一轮**一处取样面**（§3.3），并补上了上一轮**没有提到的第三个调用点**（§3.4）。

---

## 2. 指标本体

`metrics/prometheus.go:509-514`：

```
gateway_live_stream_record_dropped_total
  Help: "LiveStreamRedisStore.Record() calls that returned nil without writing
         (graceful degradation).
         Label: reason (store_unconfigured|redis_unavailable)."
```

`metrics/prometheus.go:521-523` 在 recorder 构造时对两个 reason 各 `Add(0)`，**保证两个 label 序列从进程启动起就存在**（不会因为没有事件而在 Prometheus 里缺序列）。

全仓只有 **2 个**生产写入点，都在 `admin/live_stream_redis_store.go` 的 `Record()` 内：

| reason | 行 | 触发条件 |
|---|---|---|
| `store_unconfigured` | `:348` | `s == nil \|\| s.rdb == nil` |
| `redis_unavailable` | `:394` | per-request_id SETNX 锁未取到（`!locked`） |

---

## 3. `store_unconfigured` 可达性复核 —— **生产不可达**

`Record()` 的入口有两个生产构造点。逐个证伪。

### 3.1 构造点 A：`cmd/gateway/main.go:3251`

```go
if fpSlotRedis != nil {
    liveStreamStore := admin.NewLiveStreamRedisStore(fpSlotRedis)
    adminHandler.SetLiveStreamRedisStore(liveStreamStore)
}
```

两道独立的理由让它到不了 `Record`：

1. **入参非 nil** —— 外层 `if fpSlotRedis != nil` 守卫 ⇒ `rdb` 必非 nil，`s.rdb == nil` 分支不可达；
2. **它根本不用来 Record** —— `admin/unified_detail.go:64-65` 只把它装成**读适配器**：
   ```go
   if h.liveStreamRedisStore != nil {
       locator.Live = &liveStreamLiveDetailAdapter{store: h.liveStreamRedisStore}
   }
   ```
   而 `liveStreamLiveDetailAdapter` 全仓只有两个方法（`admin/live_detail_adapter.go:25` `loadLiveDetailForStore`、`:80` `LoadLiveDetail`），内部只调 `store.LoadRequest`。**这个 store 从不写。**

### 3.2 构造点 B：`admin/live_stream_sse.go:684`

```go
store: NewLiveStreamRedisStore(cfg.RedisClient),   // cfg.RedisClient 可能为 nil
```

`LiveStreamConfig.RedisClient` 注释明写 `optional: enables 1-hour Redis cache`（`:371`）⇒ **可以为 nil**。但 `Publish` 显式短路了这条路径（`:2142-2150`）：

```go
func (h *LiveStreamSSEHub) Publish(req LiveRequest) {
    h.enqueueBroadcast(req)
    if h.store == nil || h.cfg.RedisClient == nil {
        // store 非 nil 但 RedisClient 为 nil 时, 旧版会调 Record 并由其在
        // rdb==nil 分支 Warn + no-op —— 跳过这次纯 no-op 调用, 对外行为
        // (无 Redis 写、广播照发) 不变。
        return
    }
```

注释自陈它就是为了「跳过纯 no-op 调用」而存在。⇒ Redis 未接时 `Record` **根本不被调用**。

### 3.3 我额外查的、只判 `h.store != nil` 不判 `rdb` 的路径（本轮新增）

只查上面两个构造点会漏掉 hub 内部其他 `h.store` 用法。全量列出后逐个定性：

| 行 | 用法 | 是否能到 `Record` |
|---|---|---|
| `live_stream_sse.go:757` | `h.store != nil && h.cfg.RedisClient != nil` | 否（读路径，且已判 RedisClient） |
| `:828` | `if h.store != nil` → `computeScopeDelta` | 否，**纯读路径**（`SnapshotFromDimensionQueues`） |
| `:967` / `:1201` / `:1232` | `SnapshotFromDimensionQueues` | 否，读 |
| `:1010` / `:2327-2329` / `:2377` | 快照 / `Replay` | 否，读 |
| `:1637-1644` | `maybeEmitIdleMarker` → `ScanAndRecordIdleMarkers` | 否，且**该方法自带 `s.rdb == nil` 守卫**（`live_stream_redis_store.go:1626`） |
| `:1706` / `:1772` | `h.store.rdb == nil` 显式判空（`:1772` 还先 `Ping`） | 否 |
| `:2087` / `:2103` | `LoadRequest`（notify 处理） | 否，读 |
| `:2145` | `Publish` 短路 | 否（**这是唯一的写入口关口**） |

**唯一会调 `Record` 的两个下游**：`live_stream_async.go:202-205` `recordToStore`（被 `:154`/`:160` 的 drainer 和 `live_stream_sse.go:2157` 的同步兜底调用），二者都在 `:2145` 之后。⇒ 闭环。

### 3.4 上一轮没提到的第三个「疑似调用点」—— 澄清

`admin/probe_stream_sse.go:315`：

```go
if h.store.Enabled() {
    ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
    _ = h.store.RecordWithOrigin(ctx, task, h.instanceID)
```

看起来是第三个写入口。**但 `h.store` 是 `*ProbeRedisStore`（`probe_stream_sse.go:134`），不是 `*LiveStreamRedisStore`** —— 是另一套探针流存储，`RecordWithOrigin` 也不在 `LiveStreamRedisStore` 上。⇒ **与本指标无关**。

**登记这一点的原因**：按 `.Record(` 抓取会把它一起捞出来，**很容易被数成「第 3 个生产写入口」并据此推翻可达性结论**。我第一遍就是在这里差点数错。

### 3.5 小结

⇒ **`store_unconfigured` 在生产不可达**，只有测试直接 `NewLiveStreamRedisStore(nil)`（`live_stream_redis_store_test.go:366,408`）才能造出来。

**推论（这条决定了告警怎么写）**：既然 Redis 未接时 `Record` 压根不被调用，那么**未接 Redis 的部署上 `redis_unavailable` 同样不可能产生**。⇒ **scope 到 `redis_unavailable` 不会造成告警风暴**，第 28 条原先担心的那个风险**不存在**。

---

## 4. `redis_unavailable` 可达性 + 两个必须讲清的语义

它可达（§3.3 闭环，Redis 已接时每次 `Publish` 都会走到锁）。**但它有两个成因，而告警阈值必须知道这件事**：

`acquireLiveStreamRecordLock`（`live_stream_redis_store.go:603-644`）有三条 `(nil, false)` 返回：

| 成因 | 行 | 行为 |
|---|---|---|
| **Redis SETNX 报错**（Redis 宕机/降级） | `:613-616` | **立即返回**，不重试 |
| **ctx 取消 / 超时** | `:630-632` | 立刻放弃 |
| 重试耗尽（无 ctx 取消） | `:641-644` | 注释自陈「基本不可达」 |

**关键数字**：

- `liveStreamRecordLockTTL = 5s`（`:571`）—— 锁最长持有 5s；
- 重试 200 次、起始 10ms、退避上限 50ms（`:584-585`, `:635-638`）⇒ 理论上耗尽需 ~2-5s；
- **但唯一的生产调用方 `recordToStore` 给的写预算只有 `liveStreamRecordWriteTimeout = 200ms`**（`live_stream_async.go:56-57`）。

⇒ 20ms/次退避下 200ms 只能重试约 10-20 次，**远够不到 200 次**。所以生产上：

- **成因 A：Redis 真的不可用**（立即丢）；
- **成因 B：同 request_id 竞争超过 200ms 写预算**（丢）；
- **成因 C（重试耗尽那条 `slog.Warn` 分支）在 200ms 预算下结构不可达** —— 注释的「基本不可达」比它自己以为的更成立。

**为什么这条区分对阈值重要**：`redis_unavailable` **不是纯粹的「Redis 挂了」信号**。若把阈值设得很低（比如 `increase(...) > 0` 就报），高并发场景下的正常竞争会把它变成**噪声源**；而按 `shadow-write-failures.yaml` 现有哲学（「A single lost row is signal」）直接照搬，会在竞争常态下误报。

**为什么这个丢弃值得告警**：`:580-583` 的 2026-08-26 注释记录了它造成的**用户可见症状**——「整条 terminal 更新被静默丢弃（tile 永久停在 in_progress —— 用户反馈『请求已完成但显示进行中』）」。⇒ 持续丢弃是真实影响，不是内部指标抖动。

---

## 5. 一个诚实的局限：阈值无法实测

`gateway_live_stream_record_dropped_total` 是**进程内 Prometheus counter**，不在 PG 里 ⇒ **我无法像 R88-r 那样用真库测出历史 rate**。

⇒ 下面的阈值是**推理值不是实测值**。这一点必须写进裁决材料，否则下一个执行者会误以为它有数据支撑。

可以实测的替代路径（留给运维，本轮手段不及）：从网关 `/metrics` 抓该 counter 的历史序列，看 `reason="redis_unavailable"` 的常态基线与尖峰形态。

---

## 6. 规则草案（**待批准后才创建**）

目标路径：`deploy/prometheus/alerts/`（**注意：不是 `deploy/monitoring/grafana-alerts/`——该目录在仓内不存在**）。

⚠️ **一个我自己踩的 glob 陷阱**（写这份报告时）：我先用 `ls deploy/prometheus/alerts/*.yaml` 数出「5 个告警文件」，写进了报告初稿；实际目录里有 **6 个**——`dashboard-api-alerts.yml` 用的是 **`.yml` 后缀**，被 glob 漏掉。**⇒ 数文件不要用带扩展名的 glob，要 `ls <dir>` 看全量。**（这是本会话「假干净」家族的第 12 次，前 11 次见 §3.4 与 82 号 §6.1。）

```yaml
# Grafana alert rules — live-stream tile drops (R89 / 待裁决 28)
#
# Origin: docs/全面审计v3/2026-10-01/83-R88T-live-stream丢弃指标label可达性复核.md
#   核实 82 号登记的 P3-b：metrics/interface.go:132 引用的
#   live-stream-record-dropped.yaml 在仓内不存在 ⇒ 告警规则本身缺失。
#
# 为什么只 scope 到 reason="redis_unavailable"：
#   store_unconfigured（Record 的 s.rdb==nil 分支）在生产不可达 ——
#   未接 Redis 时 Publish 在 live_stream_sse.go:2145 显式短路，Record
#   根本不被调用；main.go:3251 那条接线另有 fpSlotRedis!=nil 守卫且只
#   当读适配器用。对它告警 = 对一个永不触发的分支告警。
#   连带推论：未接 Redis 的部署上 redis_unavailable 同样不可能产生，
#   所以本规则在无 Redis 部署上不会误报。
#
# 为什么不用 increase(...[5m]) > 0（shadow-write-failures 的哲学）：
#   redis_unavailable 有两个成因 —— Redis SETNX 报错（真故障）与
#   同 request_id 竞争超过 200ms 写预算（liveStreamRecordWriteTimeout）。
#   后者在高并发下可能是常态，单次事件即告警会变成噪声源。
#
# runbook 的第一步是分辨成因：Redis 不健康 → 基础设施问题；
# Redis 健康但本规则在响 → 是竞争，不是宕机。
groups:
  - name: live_stream_record_drops
    interval: 30s
    rules:
      - alert: LiveStreamRecordDropped
        expr: rate(gateway_live_stream_record_dropped_total{reason="redis_unavailable"}[5m]) > 1/60
        for: 10m
        labels:
          severity: warning
          audit_id: R89
        annotations:
          summary: "live-stream 泳道 tile 因 Redis 锁未取到而被丢弃 >1/min，持续 10m"
          description: >-
            LiveStreamRedisStore.Record 的 per-request_id SETNX 锁未取到，
            请求被丢弃且不回写 Redis。权威记录仍在 request_logs，
            但泳道 tile 会缺失或永久停在 in_progress
            （2026-08-26 记录的用户可见症状：请求已完成但显示进行中）。
          runbook: >-
            1) 先查 Redis 健康：redis-cli ping / 延迟 / 连接数。
               若 Redis 不健康 → 基础设施问题，按 Redis 常规处置。
            2) 若 Redis 健康而本规则仍在响 → 成因是同 request_id 竞争
               超过 200ms 写预算（liveStreamRecordWriteTimeout），
               检查 recordQueue 是否积压（drainer 串行、cap 见
               live_stream_async.go）以及 upstream 是否变慢。
            3) 交叉核对 request_logs 是否有对应请求（有则说明只是
               泳道展示丢数据，主数据未丢）。
            4) 注意：本指标不区分「Redis 宕机」与「竞争」两种成因，
               只能靠第 1 步的二分来分辨。
```

**同时建议（同样待批准）——`metrics/interface.go:130-136` 的注释需要更新，但方向与我最初设想的相反**：

我最初以为该注释「指向一个不存在的 yaml，需要改指」。**读原文件后发现不是**：R87-m（2026-10-01）**已经把它订正过了**，现有内容是：

```go
//   - "store_unconfigured" : store/Redis client is nil (operator never wired it).
//     Unreachable in production: Publish() short-circuits a nil RedisClient
//     at admin/live_stream_sse.go:2145 before ever calling Record().
//   - "redis_unavailable"  : lock acquisition failed (Redis down or contended)
//
// NOTE (2026-10-01, R87-m): no alert rule consumes this counter. The yaml
// previously cited here (deploy/monitoring/grafana-alerts/
// live-stream-record-dropped.yaml) never existed. See audit report 71.
```

⇒ **没有陈旧路径需要改**（R87-k 当时刻意不改指是对的，而 R87-m 已用「说明它从不存在」的方式正确处理了）。**本报告 §3 独立复核出的结论与该注释的既有表述一致**，这是对那条注释的独立验证。

**唯一需要动的**：若本条批准、规则落地，则把那条 `NOTE` 更新为「规则已建于 `deploy/prometheus/alerts/live-stream-record-drops.yaml`」。**未批准则保持原样不动。**

---

## 7. 待裁决 28 的状态更新

| 项 | 批准前 | 批准后（本轮建议） |
|---|---|---|
| 规则文件 | 不存在 | 新建 `deploy/prometheus/alerts/live-stream-record-drops.yaml` |
| 覆盖的 label | — | 仅 `reason="redis_unavailable"` |
| 无 Redis 部署误报风险 | 未知 | **已证无风险**（§3.5） |
| 阈值依据 | — | 推理值，**非实测**（§5） |
| `metrics/interface.go` 注释 | **已由 R87-m 订正，状态正确** | 仅把「no alert rule consumes this counter」这句 NOTE 更新为「规则已建」 |
| severity | — | warning（主数据 `request_logs` 未丢，属下游泳道） |

**本轮未创建任何告警文件、未改任何注释** —— 两者都改变运维侧观感，属裁决范围。

---

## 8. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/83-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |

**零生产代码、零配置、零门、零 CI 行为变化。**

---

## 9. 交叉引用

- 82 号 §5 对照 [[门全绿≠门覆盖我]]；本报告 §3 是同一纪律的又一处兑现：**指标有生产调用点 ≠ 每个 label 可达**
- 71 号 `live-stream-record-dropped` 告警核查（当时结论：两个 reason 只在有调用点意义上「活着」）
- R87-k 的 P3-b（`live-stream-record-dropped.yaml` 丢失）—— 本报告把它从「登记」推进到「有可执行草案」
