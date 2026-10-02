# 157 / R89-BS：`if !Enqueue() { return }` 形态普查 —— 1 个候选，0 个缺陷，而它是个正面样本

> 日期：2026-10-01
> 承接 156 号 §五 写死的下一条线入口：**`if !Enqueue(...) { return }` 这类无 select 的丢弃形态**
> 结论：**全仓仅 1 个候选；它是 `void` 函数（根本没有返回值可丢），且是本仓最标准的正确实现。零缺陷。**
> 定级：**本轮零新增待裁决。** **未改动任何生产代码或数据库。**

---

## 一、机械入口与口径

**要找的形态**：方法内部是「非阻塞发送 + 满则丢 + 返回 bool」，
而**调用点把这个 bool 整个丢掉**（statement 级调用，不赋值、不进 `if`）。

两步机械法：

1. **先收集方法集**：扫全仓 `.func (recv) Name(...) (ret)` 定义，函数体内同时出现
   `select {` + `default:` + `return true|false` ⇒ 入集。
   ⇒ **14 个方法**入集（`Enqueue` / `tryEnqueue` / `enqueueRecord` / `enqueueQuotaTask` / …）。
2. **再扫调用点**：整行形如 `x.Method(args)`，末尾无 `=`，行首不是 `if/for/return/case`，
   且 `Method` 在第 1 步的集合里。

**结果：1 个候选。**

⚠️ **口径排除**：`vendor / node_modules / docs / deploy / installer / web / tests / scripts` 与所有 `_test.go`。

---

## 二、唯一的候选：`routingopt/integrator.go:161`

```go
// 2. Async batched path (P2.2 Track B): non-blocking enqueue; the worker
// runs the annotation write-back then the batch INSERT. Errors surface
// later as counters + slog, never on this hot path.
if i.batch != nil && !i.controlledSyncFallback {
	i.batch.Enqueue(log)          // ← 唯一候选
	...
	return nil
}
```

### 2.1 「返回值被丢弃」这个判定本身是错的

```go
func (w *FeedbackBatchWriter) Enqueue(log *FeedbackLog) {   // ← 没有返回值
```

**它是 `void` 函数——根本不存在返回值可丢。** 我的检测器只匹配了「statement 级调用」，
没有校验**被调方法是否真的有返回值**，于是把一个 void 调用也报成了候选。

⇒ **这是本条线第七个检测器缺陷**，形态与前六个同族：
**分类条件少了一条 ⇒ 假阳性。**

### 2.2 读实现之后，它反而是全仓最标准的写法

`routingopt/feedback_batch.go:122-143`：

```go
// Enqueue queues one feedback row without ever blocking: when the queue is
// full the entry is dropped and counted (routing beats data). Also lazily
// starts the background worker on first use.
func (w *FeedbackBatchWriter) Enqueue(log *FeedbackLog) {
	...
	select {
	case w.queue <- log:
		w.enqueued.Add(1)
	default:
		total := w.dropped.Add(1)
		// Incremental, rate-limited logging: every drop bumps the counter,
		// but only the first drop and every 1000th reach the log.
		if total == 1 || total%1000 == 0 {
			slog.WarnContext(ctx, "routingopt: feedback queue full, dropping feedback (routing first)",
				"dropped_total", total, "request_id", log.RequestID)
		}
	}
}
```

它同时具备本条线一路找的四样东西：

| §49 四道过滤器 | 本例 |
|---|---|
| ① 日志 | ✅ **限流** warn（首次 + 每 1000 次）——刻意避免日志风暴 |
| ② 计数器 | ✅ `w.dropped` / `w.enqueued` 原子计数器 |
| ③ 返回值上传 | ➖ 不适用（void），但**没有信息需要上传** |
| ④ 注释论证 | ✅ **写明取舍**：「routing beats data」；并解释限流理由 |

**更关键的两点**：

- `:215` 的注释**记录了它曾经真的静默丢过，并已修复**：
  *「…permanently — the bounded queue then filled up and Enqueue **silently dropped**」*
  ⇒ **这是一个被真实事故教育出来的实现。**
- `:290-347` 提供 `StatsEnqueued/StatsDropped/StatsFlushed/StatsBatchCount/StatsFlushFailures`
  与 `Counters()` 聚合出口。

### 2.3 且这些计数器**确实在 `/metrics` 上**（§41 桶① 的反证）

- `routingopt/metrics.go:71-73`：*「feedbackWrites 通过自定义 Collector 在 scrape 时读取注入的原子计数器」*
- `cmd/gateway/routing_optimizer_init.go:92-98`：

```go
if batch := optimizer.FeedbackBatch(); batch != nil {
	routingopt.AttachFeedbackCounters(batch.Counters)          // 真计数器
} else {
	routingopt.AttachFeedbackCounters(func() routingopt.FeedbackCounters {
		return routingopt.FeedbackCounters{}                     // 零值
	})
}
```

注释写明：*「batch 未建（同步回退路径 / nil pool）时注入零值计数器函数，
**保证指标始终可被 scrape**（三个 result 恒为 0）」*

⇒ **这不是「注册了但没人接线」，也不是「没人接线所以指标缺失」——
它连空池这条路径都显式兜住了，并把理由写了下来。**

**§41 桶④「主动论证自己会怎么坏」的最高形态：不只论证「坏了会怎样」，
还论证了「坏不了的时候指标长什么样」。**

---

## 三、结论

- 全仓 `if !Enqueue() { return }` 形态的候选：**1 个**。
- 其中真缺陷：**0 个**。
- 该候选是 **§41 桶④ 的正面样本**（计数器 + 限流日志 + 取舍注释 + `/metrics` 接线 + 空池兜底 + 事故复盘注释）。

⇒ **本条线（153–157）连续两轮零确认缺陷。**
结合 156 号的「非阻塞 channel 发送被拒」形态也已证伪，
**「异步 best-effort 写入器丢数据没人知道」这一担忧，在本仓已连续三轮未能找到任何确认缺陷。**

⚠️ **这三条否定结论各有明确边界，不合并外推**：

| 轮 | 覆盖的形态 | 规模 |
|---|---|---|
| 153 | 「日志自称在丢」的站点 | 70 个（有 39 覆盖 / 30 缺口 / 1 不确定） |
| 154–156 | 非阻塞 channel 发送被拒 | 61 → 收敛 0 |
| 157 | 无 select 的 `!Enqueue()` 丢弃 | 1 → 0 |

---

## 四、诚实边界

- **未改动任何生产代码或数据库**（纯只读）。
- ⚠️ **本轮的 14 个方法集是机械粗判**（方法名含 `Enqueue/Offer/Push/TrySend/Submit/Emit/Add/Put` 之一），
  **可能既漏（命名不含这些词的非阻塞入队）也误（含这些词但非入队的普通方法）**。
  ⇒ **1 个候选是当前口径下的观测值，不是穷举。**
- ⚠️ **未覆盖**：`select` 与 `!Enqueue()` 之外的第三种形态，
  例如「返回值被丢弃但方法不叫这些名字」、
  或「丢弃发生在结构体字段/接口后面，静态上看不出来」。
- 真库本轮未查（纯代码层形态普查）。

---

## 五、待查（不是待裁决项）

1. **`select` / `!Enqueue()` 之外的第三形态**——若要继续这条线，应先扩方法集口径再重扫。
2. **把 156 号的四道过滤器 + 本轮的返回值校验做成一个可复用脚本**，
   并**用本报告 §二 的 `routingopt` 作为「正确实现」回归样本**（负样本同样重要）。
