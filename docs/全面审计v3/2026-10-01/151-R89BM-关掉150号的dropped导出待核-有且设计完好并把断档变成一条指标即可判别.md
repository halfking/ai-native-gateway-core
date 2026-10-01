# 151 号｜R89-BM：关掉 150 号留的「`dropped` 有没有导出」—— **有，而且设计得很好**；副产品是把 150 号的断档从「无法定性」变成「**一条指标即可判别**」

- 日期：2026-10-01
- 轮次：R89-BM
- 起因：150 号留了一条待核 —— *「`dropped` 是否有对应的 Prometheus 导出，本轮未确认
  ⇒ 若没有，那么「写失败」在本系统里是无声的」*。**本轮把它走完。**
- **结论先行**：
  1. **✅ 结论相反：完全有导出，而且是一对设计完整的指标。**
     - `llm_gateway_auto_selections_total{task_type,affinity_applied,explore}` —— 持久化行数（带三个标签）
     - `llm_gateway_auto_selections_dropped_total` —— 丢弃行数
     （`domains/hooks/observability/telemetry/selection_metrics.go:26-40`）
  2. **✅ 两个计数器都被喂，且喂在同样两处、相邻行**（无死计数器）：
     `selection_writer.go` **:147/:148**（队列满）与 **:222/:223**（批量插入失败）
     —— 每一处都 `w.dropped.Add(n)` **紧接** `RecordAutoSelectionDropped()`，
     **且各带一条可区分的 `slog.Warn`**（`"queue full, dropping row"` / `"batch insert failed"`）。
  3. **⇒ 150 号「写失败是无声的」这个担心，证伪。**
     写得很好：带标签的持久化计数器能看**是哪类 task_type 在落库**，
     丢弃计数器 + 两条不同原因的 warn 能看**为什么丢**。
  4. **⚠️ 但真正的副产品更有价值：这两条指标把 150 号的 33 小时断档
     从「本机无法定性」变成「**拉远端 `/metrics` 一条曲线即可判别**」。**（见 §三）
  5. **⚠️ 本轮我自己又被同一形态绊了一次**（§44 ②）：第一次 grep 只查了
     `selection_writer.go` 里的 `prometheus|promauto`，**没查同包另有 `selection_metrics.go`** ——
     **我差点把「有导出」报成「无导出」。**

---

## 一、事实：可观测性是完整的

```go
// domains/hooks/observability/telemetry/selection_metrics.go:26-40
autoSelectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "llm_gateway_auto_selections_total",
    Help: "Total auto-route selection rows persisted",
}, []string{"task_type", "affinity_applied", "explore"})

autoSelectionsDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
    Name: "llm_gateway_auto_selections_dropped_total",
    Help: "Total auto-route selection rows dropped (queue full or insert failure)",
})
```

### 两个 drop 点都同时喂了两个计数器

| # | 位置 | 触发条件 | 计数器 | 日志 |
|---|---|---|---|---|
| 1 | `selection_writer.go:144-150` | **`select { case queue <- sel: default: }`** —— 非阻塞入队，**队列满就丢** | `w.dropped.Add(1)` **:147** + `RecordAutoSelectionDropped()` **:148** | `Warn "auto_route_selections queue full, dropping row"` |
| 2 | `selection_writer.go:221-225` | `insertBatch` 返回 err | `w.dropped.Add(len(batch))` **:222** + `RecordAutoSelectionDropped()` **:223** | `Warn "auto_route_selections batch insert failed" + count + error` |

**⇒ 原子计数器（给测试与 `SelectionWriterStats()`）与 Prometheus 计数器（给运维）**
**在同一对相邻行上一起递增，不存在「一个在喂、另一个在空转」的死计数器。**

### 为什么这套设计值得肯定

1. **持久化计数器带三个标签**（`task_type` / `affinity_applied` / `explore`）
   ⇒ 不只能看「有没有落库」，还能看**是哪一类决策在落库**；
2. **两个丢弃原因各有一条独立 warn**（队列满 vs 插入失败）⇒ 不需要猜；
3. 包头注释**主动写明了取舍**：
   > 「a slow or dead database degrades into **dropped telemetry rather than dropped traffic**」
   ⇒ **丢弃是设计，不是事故**，而且作者说这句话的时候就把 drop 的两条路径都加了指标。

## 二、⚠️ 150 号的担心被证伪

150 号原文：
> ⚠️ **未确认 `dropped` 是否有 Prometheus 导出** ⇒ **若没有，「写失败」在本系统里是无声的。**

**⇒ 证伪。**「写失败无声」不成立：**既有 Prometheus 计数器，也有区分原因的 warn 日志。**
**⇒ 150 号那条「需运维取数」的清单里，第 ② 项（`dropped` 计数）从「待确认是否存在」变成「确实存在，去拉它」。**

## 三、⚠️⚠️ 真正的副产品：两条指标把 150 号的断档变成「一条曲线即可判别」

150 号因为**本机无网关进程**而无法定性断档（候选 A：远端构建早于接线 / 某类入口不经过；
候选 B：`w.pool == nil` 或插入失败）。
**现在有了判别器** —— 只需在**远端实例**上拉这两条曲线：

| 观测到的形态 | 判定 | 对应 150 号的候选 |
|---|---|---|
| `…_total` **与** `…_dropped_total` **两条都平** | `WriteAutoSelection` **根本没被调用** | **候选 A**（构建早于接线，或该入口不经过这条路径） |
| `…_total` 平、**`…_dropped_total` 在涨** | 写方**被调用但在丢** | **候选 B**（队列满 or 插入失败）—— 再看 warn 区分 |
| 两条都在涨 | 写方正常，**是表/查询侧的问题** | 本轮已排除（hot 表 0 行 ⇒ 写入侧） |

**⇒ 请运维在远端 `/metrics` 上跑这一条**：
```
llm_gateway_auto_selections_total          # 按 task_type/affinity_applied/explore 展开
llm_gateway_auto_selections_dropped_total
```
**并把同一时刻的两条 warn 日志行数给我**（`queue full` / `batch insert failed`）
⇒ **「两条指标 + 两类日志行数」四元组，一次就能把断档定性。**

⚠️ **本轮仍未取到远端数据** ⇒ **断档原因依然未定性**，**不发 P1**。
**本轮交付的是判别器，不是结论。**

## 四、⚠️ 本轮我自己又被 §44 ② 绊了一次

**第一次检索只查了 `selection_writer.go` 这一个文件里的指标声明：**
```bash
grep -nE "prometheus|promauto|MustRegister|metrics\." domains/hooks/observability/telemetry/selection_writer.go
# ⇒ 0 命中
```
**而同包另有 `selection_metrics.go` 承担全部指标声明。**
⚠️ **我据此准备写下「dropped 无 Prometheus 导出 ⇒ 写失败是无声的」——
那是一条方向完全错误的结论，且它恰好会强化 150 号那个未经证实的担心。**

⇒ **这是本会话第二次在同一族上失手**（150 号是 `head` 截断，本轮是**只看一个文件**）。
**两者的共同点是「把检索范围缩到一个我刚好在看的东西上」。**

**playbook §44 增补（④）**：

> **④ 判「有没有 X」之前，先确认「X 会声明在哪」——不要只查你正在读的那个文件。**
> 指标/配置/常量常常声明在**专门的配套文件**里
> （本轮：`*_metrics.go`；150 号：`*_writer.go` 之外的 `registry.go`）。
>
> **落地**：判「有没有导出 / 有没有注册 / 有没有常量」时，
> **先按文件名模式列一遍整个包（`*_metrics.go` `*_prometheus.go` `*_config.go` `*_const.go`），
> 再决定查哪里**；
> **并且优先用「同包/同目录全部文件」而不是「当前这个文件」作为检索范围。**
>
> **与 ①②③ 的关系**：① 截断了范围；② 过滤错了；③ 归属错了；
> **④ 是范围本身就选窄了** —— **四种错误的共同点：把「我看到的」当成了「全部」。**

## 五、诚实边界

- **未改动任何生产代码或数据库**（全部只读）。
- **未取到远端 `/metrics` 与日志** ⇒ **150 号的 33 小时断档原因仍未定性**，**本轮不发 P1**。
- **本轮只核了 auto 选择记录这一条写入路径的可观测性**；
  ⚠️ **未核其它「异步 best-effort 写入器」是否都有等价指标**（同包还有
  `tuning_signal_writer.go`，其 `:16` 注释只说「errors are logged and dropped」
  ⇒ **未见指标声明**，但**本轮未逐项核实**，**如实登记为待查**）。
