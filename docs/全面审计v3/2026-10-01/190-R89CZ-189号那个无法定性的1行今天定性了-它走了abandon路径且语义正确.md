# 190 号 · R89-CZ —— **189 号那个「无法定性」的 1 行，今天定性了**：它走了 `abandon` 路径，且 abandon 的语义是**正确的**

> **日期**：2026-10-01
> **轮次**：R89-CZ（第 90 轮，审计第 190 号）
> **类型**：**把上一轮标为「不可查」的事情查清**（零新增待裁决；**收掉一条方法论边界**）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：189 号（1 行空 tenant 是孤立单行）

> ## ✅ 本报告唯一保留的未决项（「为什么这条请求日志没落库」）已由 191 号关闭
>
> 191 号（[R89-DA](2026-10-01/191-R89DA-190号留下的最后一条也查清了-那条WAL已出hot而UPDATE只打hot-late-update被静默丢弃.md)）
> 走了一条新路——**不查日志，查它经过了哪些表**：
> **它在 `request_wal` 里，状态是 `status='pending'` / `stage=0`**，
> 且位于 **`request_wal_2026_09` 分区（已 promote 出 hot）**；
> 而 `request_logger.go:684-716` 的 UPDATE **只打 `request_wal_hot`**
> ⇒ **第二次写入被静默丢弃**（注释明写「which is the **intended behavior**」）。
>
> **⇒ 完整机制链**：请求在第一阶段中断 → 唯一能救它的 UPDATE 打不到它 →
> **下游两处异常（`request_logs` 无此行、auto_route 4h 后走 abandon）都是同一个上游原因的下游表现。**
>
> **⚠️ 仍未定的只剩「它为什么在第一阶段中断」**（需网关侧日志）——
> **但那已是「为什么这一次请求失败」，不是「为什么数据对不上」了。**
>
> **⚠️ 191 号同时纠正了一个差点发出的 P1**：`request_wal` 父表 `count(*)` 得 666,046「pending 积压」，
> **逐分区数只有 520**（Citus 父表聚合重复计数，差 1280 倍）。

---

## 〇、起手

189 号结尾写了一句：

> 「**为什么这 1 行没落库** ⚠️ **无法定性**。需网关侧日志（§44③ 本机无进程）⇒ **不猜**」

**本轮先查 189 号留下的另一条（`success` 26.8% 的主因），
结果顺着 `success` 字段的写入链一路读下去，把那条「无法定性」也一并查清了。**

---

## 一、F1：先查 26.8% —— 按 `candidate_rank` 分层，出现明显差异

```
 candidate_rank |   n  | ok | ok_pct
              1 |  426 | 170 |  39.9%   ← 明显高于其它两档
              2 |  229 |  42 |  18.3%
              3 | 1092 | 257 |  23.5%
```

**⇒ 排名第一的选择成功率显著更高。但这只说明「相关」，不说明「原因」。**
**⇒ 要判原因，必须先搞清楚 `success` 这个字段到底是什么。**

---

## 二、🔴 F2：`success` 根本不是写入时就有的 —— 它是**事后回填**的

`selection_writer.go:33-40` 的注释先给了答案：

> ```
> // Outcome fields are deliberately absent: they are backfilled later by
> // AutoRouteSettleWorker once the request has settled, because latency and cost
> // are not known at decision time.
> ```

**⇒ `AutoSelection` 结构体里根本没有 `Success` 字段**（grep 确认），
**`auto_route_selections_hot` 里的 `success` 是 settle worker 回填的。**

**回填链（`bg/auto_route_settle_worker.go`）：**

| 行 | 内容 |
|---|---|
| `:15` | 「3. **Join request_logs for success / latency / cost**」 |
| `:379` | `rl.success, rl.latency_ms, rl.cost_usd`（**从 `request_logs` LEFT JOIN 取**） |
| `:433` | `if p.success == nil { … }` —— **取不到日志时的分支** |
| `:436-438` | `if now.Sub(p.ts) > settleAbandonAfter { w.abandon(ctx, p) }`，`settleAbandonAfter = 4 * time.Hour`（`:60`） |
| `:576-583` | `abandon()` **只写 `settled_at` + `reward_source='request'`，不写 `success`、不写 `reward`** |

**⇒ 所以「`success IS NULL`」的语义是明确的：这一行等不到它的 `request_logs`，
超过 4 小时后被 `abandon` 掉了。**

---

## 三、🔴 F3：三项独立证据全部指向 `abandon` —— **189 号那个「不可查」今天查清了**

### 证据一：全表 settle 状态

| 指标 | 值 |
|---|---|
| 总行数 | 1,747 |
| `settled_at IS NOT NULL`（已回填） | **1,747（100%）** |
| `settled_at IS NULL`（仍在等待） | **0** |
| `success IS NULL` | **1** |
| `reward IS NULL` | **1** |

**⇒ 没有「还在等」的行 ⇒ 那 1 行不可能还在等，只能是被 abandon 了。**

### 证据二：**延迟分布**（决定性）

```sql
SELECT round(EXTRACT(EPOCH FROM (settled_at - ts))/3600.0, 2) AS hours_to_settle, count(*)
FROM auto_route_selections GROUP BY 1 ORDER BY 2 DESC;

  0.06h | 542      0.07h | 368      0.13h | 249      0.05h | 219
  0.04h | 126      0.08h | 121      0.09h |  33      0.12h |  29
```

**⇒ 其余 1,746 行全部在 0.04–0.13 小时内被回填（正常：请求落库后很快就能 join 到）。**
**⇒ 而 id=313 的 `ts = 2026-09-15 03:06:56`、`settled_at = 2026-09-15 08:09:38` ——
间隔正好 5 小时**，**远超 `settleAbandonAfter = 4h`**。

### 证据三：`reward` 也恰好只有它为 NULL

**⇒ `abandon()` 的代码明确只写 `settled_at` 与 `reward_source`，不写 `reward` ——
与观测完全一致（`reward_source='request'` 而 `reward` 为 NULL）。**

**⇒ 三条独立证据（settle 完备性 / 延迟分布 / reward 空值）同时指向 abandon 路径。**

---

## 四、⚠️ F4：但**这个 abandon 是正确的行为，不是缺陷**

这是本轮最重要的判断。回到代码注释：

```go
// abandon stamps a row settled with no reward, so it stops being scanned and is
// excluded from learning (the affinity rollup requires reward IS NOT NULL).
```

**设计意图非常明确：**
- **「stopped being scanned」** —— 防止 `idx_ars_unsettled` 索引无限增长；
- **「excluded from learning」** —— **没有结果数据的行不参与 affinity 学习**。

**⇒ 189 号那个 1 行的正确处置就是被排除出学习 ——
因为它对应的 `request_logs` 记录真的不存在（189 号 F2 已证 `0 行`），
**它没有「成功/失败」这个事实可学。**
**⇒ 若强行给它填一个 `success=false`，反而会**污染 affinity 学习**（把「查不到日志」误当成「请求失败」）。

**⇒ 结论：那 1 行的 `success IS NULL` 与 `tenant_id` 为空，
都是「这条请求的日志整行没落库」的**同一个上游原因的下游表现**，
而系统对它的处理是**符合设计的安全降级**。**

**⚠️ 诚实边界**：**「为什么这条请求的日志没落库」本轮仍未定性**（需网关侧日志，§44③）。
**⇒ 已查清的是「网关侧对它的处理是对的」，未查清的是「它是怎么坏的」。**
**⇒ 两者必须分开说，否则会读成「这个问题已经解决」。**

---

## 五、26.8% 的主因：也一并结清

| 问 | 答 |
|---|---|
| `success=26.8%` 是不是 auto 路由质量差？ | ❌ **不是**。188 号已证伪方向（同期 `request_logs` 侧只有 8.0%） |
| `candidate_rank=1` 成功率 39.9% 更高，说明什么？ | ⚠️ **只说明相关**。rank 越靠前说明打分越高、被选中的次数分布不同；**本轮未做「同 rank 下控制其他变量」的比较，不下因果结论** |

**⇒ 26.8% 的主因仍是「未查」，但本轮把它的**性质**查清了**：
它不是路由质量指标，而是「事后能否 join 到 `request_logs` 的结果分布」，
而这个分布本身被 `abandon` 机制、探测流量占比、请求类型分布共同决定。**

---

## 六、playbook §82 新增

> **§82 「某个字段全空/全假」先查它是**什么时候**被写的 —— 写入时机的字段，它的空值往往不是「没发生」，而是「按设计没发生」**

**由来**（R89-CZ / 190 号）：`success` 只有 1 行是 NULL、只有 1 行 `reward` 是 NULL。
**我原本准备把它当成一条「坏数据」继续追**，直到读到
`// Outcome fields are deliberately absent: they are backfilled later by AutoRouteSettleWorker`
—— **它根本不是写入时就有的字段**，而空值对应的是一条**明确的降级路径**（`abandon`）。

**⇒ 落地三条：**
1. **查「某字段为什么空」之前，先查它**是写入时填的还是事后回填的** ——
   **事后回填的字段，它的 NULL 往往对应一条显式的降级分支，而不是写入失败**；
   本轮那条分支在 `settle_worker.go:433-441`，且注释写明了设计意图；
2. **⚠️ 降级分支被正确触发 ≠ 上游问题已解决** ——
   **本轮查清的是「网关侧对它的处理是对的」，未查清的是「它是怎么坏的」**
   ⇒ **两者必须分栏写**，否则读者会以为问题已经关闭；
3. **⚠️ 「某字段只有 1 行为空」要和「哪些字段同时为空」一起看** ——
   本轮 `success IS NULL` 与 `reward IS NULL` **恰好都是那同一行**，
   而 `abandon()` 的代码正好只写 `settled_at`+`reward_source`、不写这两个
   ⇒ **多个字段同时为空，是「同一条代码路径」的最强指纹**
   （比逐个字段单独查快得多，§63「先数写方」的变体）。

**⇒ 与 §81 的关系**：
§81 说「先问同类有几个」；
**本条说「问清它的写入时机」——同类计数告诉你「有多少」，
写入时机告诉你「为什么」，缺后者就会把设计当缺陷。**

**同族**：§33（双向证据）/ §41（注释三桶）/ §45 / §59 / §61 / §63（先数写方）/
§71 / §75 / §79 / §80 / §81。
