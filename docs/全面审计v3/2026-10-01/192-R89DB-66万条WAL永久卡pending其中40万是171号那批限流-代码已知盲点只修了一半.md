# 192 号 · R89-DB —— 🔴 **66.6 万条 WAL 永久卡在 `pending`，其中 40.3 万是 171 号那批限流** —— 且代码**已经知道这个盲点，只修了一半**

> **日期**：2026-10-01
> **轮次**：R89-DB（第 92 轮，审计第 192 号）
> **类型**：把 191 号的单行机制**规模化**（新增 **待裁决 77，P2**）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：191 号（那 1 条 WAL 卡 pending 的机制）

---

## 〇、起手

191 号查清了**一条**记录的完整机制链：
`WAL 停在 pending/stage=0` → `该行已 promote 出 hot` → `而 UPDATE 只打 hot` → **late update 被静默丢弃**，
并定性为「**intended behavior**」。

**但「intended behavior」这个定性只在单条上成立。**
**本轮去量它的规模 —— 结果是一个六位数的量级。**

---

## 一、🔴 F1：规模 —— **`request_wal_2026_09` 里 986,861 行中 666,046 行（67.5%）卡在 `pending`**

```sql
-- 逐分区统计（父表聚合会重复计数，见 191 号 §83）
  part   | pending |  total
 2026_06 |       0 |      0
 2026_07 |       0 |      0
 2026_08 |       0 |      0
 2026_09 | 666046 | 986861     ← 67.5%
 2026_10 |       0 |      0
     hot |     515 |    722     ← 71.4%（仅今天 8 小时窗口）
```

**⚠️ 而 promote 函数 `promote_request_wal_hot_to_partition` 的筛选条件只有：**

```sql
WITH batch AS (
  SELECT request_id, created_at FROM public.request_wal_hot
  WHERE created_at < now() - p_retention        -- 只看时间
  ORDER BY created_at, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
)
```

**`grep -c pending` 该函数 = 0 ⇒ 它完全不区分状态，`pending` 行照搬不误。**

---

## 二、🔴 F2：这些 pending **不是「请求没结束」** —— 它们**都走到了有模型名的阶段**

```sql
SELECT count(*), count(*) FILTER (WHERE completed_at IS NOT NULL),
       count(*) FILTER (WHERE stage > 0),
       count(*) FILTER (WHERE client_model IS NOT NULL),      -- ← 关键
       count(*) FILTER (WHERE prompt_tokens IS NOT NULL),
       count(*) FILTER (WHERE error IS NOT NULL)
FROM request_wal_2026_09 WHERE status='pending';
→ 666046 | 148 | 148 | 666046 | 148 | 148
```

**⇒ `client_model` 100% 有值（说明路由已完成、模型已选定），
而 `completed_at` / `stage` / `tokens` / `error` 几乎全空（说明终态那一次 UPDATE 丢了）。**
**⇒ 这不是「请求卡住了」，是「请求结束了但结局没写回去」。**

**⚠️ 对照组（同分区的 success/failure）：**

| status | 行数 | `completed_at` 已填 | 平均耗时 |
|---|---|---|---|
| success | 297,529 | **297,529（100%）** | **17.5 秒** |
| failure | 23,286 | **23,286（100%）** | — |
| **pending** | **666,046** | **148（0.02%）** | — |

**⇒ 正常完成的两类 100% 有 `completed_at`；pending 类 0.02%。**

---

## 三、🔴 F3：这 66.6 万条的真实结局 —— 并**修正 171 号的一个数字**

```sql
-- 这批 pending 的 request_id 在 request_logs 里的真实状态
  request_status | count
  rate_limited   | 402980      ← 67.1%
  failure        | 199063      ← 29.9%
  success        |    266      ←  0.04%
  in_progress    |    219
```

**⇒ 90.46%（602,528 / 666,046）在 `request_logs` 里有对应记录 —— 请求确实结束了。**

**⚠️ 而这里有一个必须写出来的发现：**

| 口径 | 数量 |
|---|---|
| 171 号报的 `error_kind='rate_limit_exceeded'` 总数 | **403,031** |
| 本轮查到的「WAL 卡 pending 且真实 rate_limited」 | **402,980** |
| **差值** | **51（0.01%）** |

**⇒ 171 号统计的 40.3 万次限流里，**99.99% 的 WAL 行至今仍停在 `pending`**。**
**⇒ 这是 171 号从未看到的第二个面：限流事件在 `request_logs` 里记得清清楚楚，
在 `request_wal` 里却像「从没发生过」。**

---

## 四、🔴 F4：机制 —— **代码已经知道这个盲点，并且只修了一半**

`domains/streaming/handler.go:8110-8122` 有一段注释，**它描述的正是本轮这个现象**：

> ```go
> // insertRateLimitedPlaceholder ensures request_logs_hot has a row before the
> // rate-limit UPDATE writes its terminal fields. Used by the rate-limit
> // early-return path (handler.go captureAndEmitRateLimited) which bypasses
> // recordInitialRequestLog.
> //
> // 2026-08-26: introduced to plug the kimi-k3 / RPM queue blind spot —
> // when the queue budget was exceeded the request returned 429/200 bytes
> // but never landed in request_logs_hot (only WAL + Redis trace did),
> // producing "request log row not found, retaining Redis trace" warnings
> // at FlushToPG.
> ```

**⇒ 作者在 2026-08-26 就发现了：限流早退路径只落 WAL，不落 `request_logs`。**
**⇒ 他的修法是「补一个 `request_logs` 占位行」（`insertRateLimitedPlaceholder`）。**

**⚠️ 但他补的是 `request_logs` 那一侧 —— **`request_wal` 侧的 UPDATE 仍然没被调用**。**

**⇒ 于是形成今天这个局面：**
- `request_logs` 侧：**已修**（402,980 条限流都有行、状态正确）✅
- `request_wal` 侧：**未修**（同样这 402,980 条永远停在 `pending`）🔴

**⇒ 这不是「设计上接受静默丢弃」，而是「一个已识别盲点的修复只覆盖了两个出口中的一个」。**

---

## 五、定级与影响面（§37）

| 维度 | 评估 |
|---|---|
| **会失败吗** | ✅ 已发生。**66.6 万行**（67.5%）WAL 状态失真 |
| **会波及吗** | ✅ **任何按 `request_wal.status` 做的统计/对账都会错**；WAL 是「写入先行记录」，其失真会传导到任何以它为准的下游 |
| **当事方知道吗** | ⚠️ **部分知道** —— `request_logs` 侧的盲点已被识别并修（2026-08-26 注释），**WAL 侧未见任何记录** |
| **有痕迹吗** | ⚠️ 部分。`client_model` 有值而 `completed_at` 空这个组合是可查的指纹；**但没有任何告警** |

**⇒ 定级 P2**（规模巨大 ✅ / 机制清楚 ✅ / **当前是否有下游真的按 WAL.status 统计，本轮未查**）。

### 决定定级的那条前置问题 —— 本轮已查掉

报告初稿写「未验证有没有读端按 `request_wal.status` 过滤」，**写完立刻查了**：

```bash
grep -rn "FROM request_wal|request_wal WHERE|request_wal_hot WHERE" --include=*.go .
  → 唯一命中是 request_logger.go:707 —— 那条 UPDATE 自身
```

**⇒ 没有任何 Go 代码按 `request_wal.status` 做过过滤或统计。**

**⚠️ 但有一个 SQL 读端**：`sql/objects/views/request_wal_with_current_month.sql`
—— 它 `UNION ALL hot + 父表`，**把 `status` 原样透出**（含 hot 与月分区两路）
⇒ **这个视图是对外可查的**，**任何用它做统计的查询都会读到那 66.6 万条假 `pending`**。
`admin/data_lifecycle_*.go` 里出现 `request_wal` 的地方是**表名常量与迁移函数名，不是状态统计**。

**⇒ 定级依据更新：**
| 情形 | 定级 | 本轮结论 |
|---|---|---|
| 无读端 | P2（纯数据质量） | — |
| 有读端且按 status 过滤 | P1（统计错误） | — |
| **有读端但只是透出** | **P2（数据质量 + 对外可查的失真）** | ✅ **本轮是这一档** |

**⇒ 维持 P2**，理由更精确：**失真数据通过一个公开视图对外可见，但仓库内没有代码消费它。**

---

## 六、playbook §84 新增

> **§84 看到「intended behavior / 静默丢弃」的注释时，先问「丢的是哪一侧」—— 一个已知盲点被修一半，比完全没修更难发现**

**由来**（R89-DB / 192 号）：`request_logger.go` 注释写「Late updates after migration are
**silently dropped, which is the intended behavior**」。
**单条看确实符合设计；但 191 号顺着它找到的那条记录只是冰山一角，
本轮一量就是 66.6 万条 —— 且其中 40.3 万对应 171 号早就报过的限流事件。**

**⇒ 落地三条：**
1. **「单条符合设计」不能推广到「这类都符合设计」** ——
   191 号在单条上验证了机制，**但没问「有多少条走在这条路径上」**；
   ⚠️ **规模一出来，性质就可能变**（P3 单点 → P2 规模）；
2. **⚠️ 看到一个已识别的盲点被修复时，要数「它覆盖了几个出口」** ——
   本轮 `handler.go:8110` 的注释说明 2026-08-26 已修「限流早退不落 `request_logs`」，
   **但同一事件还要写 `request_wal`，那一侧没修**
   ⇒ **「`request_logs` 里有行」不等于「这个事件的记录是完整的」**（§71 的出口分工，换了个方向用）；
3. **⚠️ 「一个字段有值、配套字段全空」是最强的路径指纹** ——
   本轮 66.6 万行 **`client_model` 100% 有值而 `completed_at` 0.02% 有值**，
   **一眼就说明「终态 UPDATE 丢了」**；
   ⇒ **遇到大批「半成品」记录，先找那个「有值」和「空值」的分界**，
   **它直接告诉你流程停在哪一步**（§82 的字段层 + §83 的记录层，合起来用）。

**⇒ 与既有条目的关系**：
§82 问「字段什么时候写的」、§83 问「记录停在哪张表」；
**本条问「同一个事件有几个出口，哪些被修了」——
前两条定位单点，本条防止把「修了一半」读成「已经修好」。**

**同族**：§33 / §41（注释三桶，「intended behavior」也要问覆盖面）/ §45 / §59 / §61 /
§70 / §71（出口分工）/ §75 / §79 / §80 / §81 / §82 / §83。
