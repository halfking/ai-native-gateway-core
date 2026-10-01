# 71 号报告：R87-m `live-stream-record-dropped` 告警核查

- 日期：2026-10-01
- 轮次：R87-m
- 变更面：**2 处注释路径订正**（`metrics/interface.go:132`、`metrics/prometheus.go:853`）+ 1 份报告
- 承接：70 号顺带登记的「告警规则本身丢失」P3-b，本轮把它查到底

---

## 0. 结论先说

70 号登记的待办是「确认 `live-stream-record-dropped` 告警是否真在跑」。**是真缺失，而且缺的原因比「文件丢了」更值得记**：

| 事实 | 核实方式 | 结论 |
|---|---|---|
| `live-stream-record-dropped.yaml` 存在吗 | `ls deploy/prometheus/alerts/` 6 个文件，无此项 | **不存在** |
| 注释指向的目录存在吗 | `ls -d deploy/monitoring` | **`deploy/monitoring/` 整个目录都不存在** |
| 有别处消费该指标吗 | `grep -rln live_stream_record_dropped deploy/` | **零命中**（6 个告警文件 + 看板文件全无） |
| 指标有生产调用点吗 | `grep RecordLiveStreamRecordDropped` 非测试非 metrics | **2 处**（`admin/live_stream_redis_store.go:348`、`:394`） |

所以 `metrics/interface.go:132` 那句「reason values (kept in sync with the alerting labels in `deploy/monitoring/grafana-alerts/live-stream-record-dropped.yaml`)」是**假承诺**：它声称与某个告警规则的 label 保持同步，而那个文件、那个目录都不存在。**这不是路径漂移（70 号已修的那类），是引用了一个从未存在的东西。**

按本会话既定纪律「发现引用了不存在的东西时，不要顺手改成另一个同样不存在的路径——那是把缺陷藏起来」，本轮**没有**把注释改指到某个新造的文件路径。

---

## 1. 但真正的问题比「文件缺失」更细：两个 reason 里只有一个可达

指标有两个 reason label。逐个追可达性（§10 三项核实）：

### 1.1 `store_unconfigured` —— 生产上**不可达**（设计使然，非疏漏）

触发点在 `admin/live_stream_redis_store.go:341-350`：

```go
if s == nil || s.rdb == nil {
    slog.Warn("live stream record: store or Redis client is nil, skipping write", ...)
    metrics.Global().RecordLiveStreamRecordDropped("store_unconfigured")
    return nil
}
```

但两条生产接线都到不了这里：

1. **SSE hub 那条**（`admin/live_stream_sse.go:684` 构造 store）→ 写入口是 `Publish`，
   而 `Publish` 在 `:2145` **显式短路**：

   ```go
   if h.store == nil || h.cfg.RedisClient == nil {
       // store 非 nil 但 RedisClient 为 nil 时, 旧版会调 Record 并由其在
       // rdb==nil 分支 Warn + no-op —— 跳过这次纯 no-op 调用, 对外行为
       // (无 Redis 写、广播照发) 不变。
       return
   }
   ```

   注释自陈这是**故意**不再走 no-op 分支。所以 `Publish` 永远不会把 nil-rdb 的 store 喂给 `Record`。

2. **admin handler 那条**（`cmd/gateway/main.go:3251`）→ 构造处包在 `if fpSlotRedis != nil` 里，
   且这个 store 只被当作**读适配器**用（`admin/unified_detail.go:65` →
   `locator.Live = &liveStreamLiveDetailAdapter{store: ...}`，该适配器只实现
   `LoadLiveDetail`，见 `admin/live_detail_adapter.go:80`）⇒ **它根本不调 `Record`**。

**⇒ `store_unconfigured` 只在测试里可达。**

### 1.2 `redis_unavailable` —— **可达**，且这才是注释承诺要给运维的信号

`admin/live_stream_redis_store.go:386-397`，per-request_id SETNX 锁拿不到时
（Redis 宕机**或**竞争耗尽）。注释写得很清楚其危害：

> Without this counter, the operator cannot tell that tiles are being lost while the canonical record still lands in `request_logs`.

**这就是实际在丢数据的那个分支，而它恰好是唯一没有告警的。**

---

## 2. 危害形态：不是「告警没建」，是「文档承诺的信号不存在」

`metrics/interface.go:124-131` 的原始意图是白纸黑字的：

> **Without this counter the operator has no signal** that the live stream hub is silently losing tiles while the canonical record still lands in `request_logs`.

counter 有了（2026-08-31 P2-2 做的），**从 counter 到运维感知的那一段没接上**：

- `/metrics` 上有 `gateway_live_stream_record_dropped_total{reason="redis_unavailable"}`；
- **没有任何 `rate(...)` 规则消费它** ⇒ 看板不会画、告警不会响；
- 运维要发现「泳道在丢 tile」，只能手工去 Prometheus 里翻这个 counter。

与 70 号附件镜像那条对照，差别很能说明问题：**附件镜像那侧 `metric → 告警 → runbook` 是全的**（`ShadowWriteAttachmentFailing`），本侧停在 `metric`。

---

## 3. 为什么本轮不直接补规则（交产品/运维裁决）

直觉上「照抄 `shadow-write-failures.yaml` 加一条规则」即可，但**不成立**，需要先定阈值语义：

`store_unconfigured` 若在**未接 Redis** 的部署里可达，加 `rate(...) > 阈值` 会**每请求触发、永久告警风暴**。本轮已证明它在当前代码里不可达（§1.1），所以规则**必须显式 scope 到 `reason="redis_unavailable"`** 才能安全——而这正是「阈值与分页语义属于运维决策」而非机械补齐的原因。

已列为**待裁决第 28 条**。

---

## 4. 本轮改动

只动 2 处注释的**事实性错误**（不改行为、不新增告警）：

- `metrics/interface.go:132`：`deploy/monitoring/grafana-alerts/live-stream-record-dropped.yaml` → 改为如实陈述「**尚无告警规则**，见 71 号报告」；
- `metrics/prometheus.go:853`：同上。

**刻意没做**：把路径改指到一个新造的 yaml 路径（那会把缺陷藏起来）、或直接补一条告警规则（未授权改运维读数）。

---

## 5. 顺带确认的事实（供后续引用）

- 本轮**未触碰**任何测试或生产逻辑，`go build ./...` 不受影响；
- `gateway_live_stream_record_dropped_total` 声明在 `metrics/prometheus.go:509`，有 label `reason`；
- `deploy/prometheus/alerts/` 现有 6 个文件：`auto-summary-failures.yaml`、`dashboard-api-alerts.yml`、`fp-slot-saturation.yaml`、`outbox.yaml`、`rpm.yaml`、`shadow-write-failures.yaml`——**没有一条消费 live-stream 相关指标**。
