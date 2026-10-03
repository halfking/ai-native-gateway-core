# 2026-10-04 §10 补充：落库倍率与容量口径的实测定论

> 本篇**不改** [`2026-10-04-ursm-snapshot-partitioning-design.md`](./2026-10-04-ursm-snapshot-partitioning-design.md)（评审稿，commit `0a2d94d1b`），
> 只补两件用只读手段就能定论的事：评审稿 **§10 拍板问题 #2（行数是否因双写翻倍）**
> 与 **#3（对外承诺 3.6 GB 还是 4~7 GB）**。
>
> 另起一篇而不追加进评审稿，是因为共享工作区里已有多方在同一份审计链上追加，
> 同一文件尾部并发追加会变成三方竞争。
>
> **测量时刻：2026-10-03 21:56 ~ 22:05 (+08)。全部数据为该窗口实测。**

---

## 1. ★ 结论先行

| 问题 | 评审稿的疑问 | 实测定论 |
|---|---|---|
| 落库是否因双写翻倍 | Finding D：「1.83× 写放大」与「行数翻倍」**不能同时成立** | **两者同时成立，且倍数就是 2.00×**，不是 1.83×。`ON CONFLICT DO NOTHING` **从未生效过**。 |
| 稳态容量承诺 | #3：3.6 GB 还是 4~7 GB | 两个都是错的。真值 **5.1 ~ 6.9 GB**（单写 + 7 天），**3.6 GB 低估了 40%~90%**。 |

---

## 2. 为什么 `ON CONFLICT` 不生效

主键定义（`deploy/sql/schemas/baseline/01-schema.sql:22635`）：

```sql
ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey
    PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);
```

`snapshot_ts` 是**主键第一列**，而它由每个写入方**各自用本地时钟**生成
（`domains/ursm/v2/persist/writer.go` 的 INSERT 参数 `$1`）。
两台机器的批次相位差 30 秒（见 §3），于是同一节点的同一状态在两台写入的
主键组合**不同** ⇒ 不冲突 ⇒ `ON CONFLICT ... DO NOTHING`（`writer.go:402`）
**一次都没命中过**。

> 这也说明 §3.1「writer 逐字不用改」在分区化后依然成立，但要清楚：
> 不改 writer 的代价是**双写期间锁死了 2× 存储**。这不是分区化引入的，
> 是分区化之前就一直在付的成本，只是此前没人把倍率算清楚。

---

## 3. 证据链（三段独立证据，缺一不可）

### 3.1 两台日志：批相位差 30 秒，且写的是同一批节点

154（`llm-gateway-go-canary@8782`）：

```
22:03:35  persist committed  rows=1242  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:03:17
22:04:30  persist committed  rows=1242  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:04:17
```

245（`llmgo-245-canary@8781`，`URSM_V2_MODE=shadow`）：

```
22:01:59  persist committed  rows=1242  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:01:47
22:02:57  persist committed  rows=1243  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:02:47
22:03:57  persist committed  rows=1244  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:03:47
```

- 两台都是 **60s 周期**，但 154 落在 `:17`、245 落在 `:47` ⇒ **相位差 30 秒**
- `first_cid` / `first_model` **完全相同** ⇒ 两台写的是同一批节点
- 合计 **1242 + 1242 = 2484 行/分**

### 3.2 表内批指纹：每 30 秒一批

```sql
SELECT date_trunc('second', snapshot_ts), count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '8 minutes'
GROUP BY 1 ORDER BY 1;
```

```
21:56:17 | 1245      21:58:17 | 1243      22:00:17 | 1242
21:56:47 | 1245      21:58:47 | 1242      22:00:47 | 1242
21:57:17 | 1244      21:59:17 | 1242      22:01:17 | 1242
21:57:47 | 1242      21:59:47 | 1242      22:01:47 | 1242
...                            22:02:17 | 1243      22:02:47 | 1243
                             22:03:17 | 1242
```

### 3.3 落库速率：1 小时窗口 2430 行/分 ≈ 2484

```sql
SELECT round(count(*)::numeric/60.0, 1) FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '1 hour';     -- 2430.4 行/分（145825 行）
```

### ⚠️ 一处必须写明的判据局限

**§3.2 的批指纹单独看无法区分两种情形**：

- (A) 双台各 60s 周期、相位差 30 秒 ⇒ 每 30s 一个时间戳
- (B) 单台 30s 周期 ⇒ 同样每 30s 一个时间戳

**两者在表内数据上完全同形。** 结论靠的是 §3.1（日志给出两台各自的
批时刻与行数）＋ §3.3（总速率 2430 只能由两个写入方凑出），**不是靠表内指纹**。
若只做 §3.2 就会得出「单台高频写入」的错误结论。

---

## 4. 单行成本（实测，不是估值）

`ursm_node_snapshot_min` 存活 **18,719,448** 行，堆 9,129 MB，索引 1,362 MB：

| 项 | 字节/行 |
|---|---|
| 堆 | **511.4** |
| 索引 | **76.3** |
| **合计** | **587.7** |

> 提案 §10 的 3.6 GB 用的是 **220 B/行**（活行口径）。按**整表堆/行**实测是
> **511.4 B/行**，差 2.3 倍 —— 这正是评审稿 Finding E 指出的口径分歧，
> 本篇把它定死为 587.7 B/行（含索引）。

---

## 5. 容量折算：为什么给区间而不是单值

| 口径 | 算式 | 结果 |
|---|---|---|
| 现状（双写） | 18,719,448 行 × 587.7 B | **10.25 GB** |
| 单写·折半法 | 18,719,448 ÷ 2 × 587.7 B | **5.12 GB** |
| 单写·速率外推法 | 1,242 行/分 × 1440 × 7 × 587.7 B | **6.85 GB** |

**两个口径差 34%，原因是它们对不上：**

- 速率外推：单写 7 天应有 `1242 × 1440 × 7 = 12,519,360` 行
- 实测存活折半：只有 `18,719,448 ÷ 2 = 9,359,724` 行
- **缺口 3,159,636 行（25%）**

### 5.1 缺口定位：昼夜节律，不是保留期也不是节点数（2026-10-03 22:15 补测）

保留期实测 `min=2026-09-26 21:37:28` → `max=2026-10-03 22:06:47`，
**跨度 7.020 天 / 10,109 分钟**，与契约一致 ⇒ 「保留期不足 7 天」被排除。

最近 24 小时逐小时落库量（走 `_ts_idx`，有界，耗时 6.7s）：

```
10-02 22:00 |  33,221 |   554 行/分
10-03 00:00 |  14,354 |   239
10-03 06:00 |   2,518 |    42   ← 谷
10-03 08:00 |   3,654 |    61
10-03 11:00 |  (整行缺失，见 §5.2)
10-03 13:00 |  40,397 |   673
10-03 16:00 |  71,665 |  1194
10-03 19:00 | 139,861 |  2331
10-03 21:00 | 145,872 |  2431   ← 峰
```

**谷 42 行/分 vs 峰 2431 行/分，差 58 倍。** 7 天平均
`18,719,448 ÷ 10,109 = 1851 行/分` 之所以远低于当前峰值，正是被凌晨时段
拉低的。**缺口主因是昼夜节律**，不是节点数增长（活跃节点 1,276，
距摘要记录的 1,292 上限只剩 4%）。

**⇒ 容量不是稳态量，是随时段波动的量。** 这恰好是分区化最该解决的场景：
每天 DROP 旧分区，空间随写入速率波动自动伸缩，而不是让 7 天窗口里的
低速率时段永久占着位置。

### 5.2 ★ 附带发现：10-03 10:20~12:00 有约 100 分钟写入空洞

`date_bin('10 minutes')` 粒度下：

```
10:00 | 2406      10:20 | 2398
12:00 | 2400      12:30 | 2385
```

**10:20 到 12:00 之间整段无数据**，前后速率正常（240 行/分）。
同窗口 154 日志该时段 400 行消息，全部是
`snapshot from dimension queues built`（263）与 `auto index refreshed`（137），
**没有一条 `persist committed`**。

两点必须分清：

- **日志里没有 `persist committed`，不能当作空洞的原因** —— 154 的
  `releases/2427-b7f371e5` 是 16:29 才部署的，空洞发生在上午，
  那时跑的旧版本根本不打印这条日志。
- 快照在采集（`snapshot ... built` 跑了 263 次）却零提交，方向上指向
  **persist collect 失败**（已知症状：`redis scan failed: context deadline
  exceeded`，SCAN 全库实测 12.95~30.40s 顶着 writer 的 30s 预算）。
  **这只是方向，不是结论**。

这个空洞只占 25% 缺口的约 4%（约 2.4 万行），**不是缺口主因**，
但它是一个独立的生产异常，**建议单独立项排查**。

### 5.3 ★ 横向判据：排除网关/DB 层，成因锁定在 persist writer 自身

**这条是在证据已轮转（§5.4）之后，用交叉数据源把范围缩小的。**

同一时段（`date_bin('10 minutes')`）查 `request_logs`（每次请求都写这张表）：

```
10:00 | 121     11:00 | 120     12:00 | 58
10:10 | 120     11:10 | 210     12:10 | 171
10:20 |  51     11:20 | 117     12:20 | 187
10:30 |  83     11:30 | 152     12:30 | 130
10:40 |  63     11:40 | 115
10:50 | 189     11:50 | 174
```

**请求在空洞时段持续进入，一张都没断。** 于是：

| 假设 | 状态 | 依据 |
|---|---|---|
| 网关停机 / 未部署 | **排除** | `request_logs` 全时段有请求 |
| PG 不可写 / 连接中断 | **排除** | 同一张 PG 上 `request_logs` 正常写入 |
| 整库/表被误删 | **排除** | 前后数据完整，仅中间整段空缺；`n_tup_del` 净删 4.0M 与 2.4 万行的空洞量级差 160 倍 |
| **persist writer 未提交** | **锁定** | 两条失败路径都能导致零提交（见 §5.3.1） |

**⇒ 成因范围锁定在 persist writer 内部。**

### 5.3.1 ★★ 静态读代码：两条失败路径，范围比原估计更宽

`cmd/gateway/main.go:1282-1295` 的实际结构：

```go
ctx, timeoutCancel := context.WithTimeout(persistCtx, 30*time.Second)
rows, err := persistWriter.Collect(ctx)
if err != nil {
    slog.Warn("ursm.v2: persist collect failed", "error", err)
    timeoutCancel()
    continue                     // ← 路径 A：整轮跳过，零提交
}
if err := persistWriter.Flush(ctx, rows); err != nil {
    slog.Warn("ursm.v2: persist flush failed", "error", err)   // ← 路径 B：事务回滚，零提交
}
```

**关键结构问题：Collect 与 Flush 共用同一个 30s `ctx`，没有各自独立的预算。**

而 `writer.go:318-320` 显示 Collect 的最坏路径就是全库 SCAN
（实测 12.95~30.40s，**已经贴着甚至超过 30s 预算**）。于是：

- **路径 A**：`Collect` 超时 → `continue` → 本轮完全不做 → 零提交
- **路径 B**：`Collect` 花了 28s → `Flush` 只剩 2s → 1,242 行 INSERT 加
  索引维护做不完 → 事务回滚 → 零提交

**两条路径都会产生同样的表象（整段零行），而路径 B 此前根本不在怀疑列表里。**
`Flush` 内部是单事务（`writer.go:421` `tx.Commit`），回滚干净，不会留半截数据 ——
这与「空缺是整齐的整段」相符。

**⇒ 不能只按「collect 超时」排查。必须同时看 `persist flush failed` 的计数。**

#### ★★ 一条必须订正的误判

我原先用 154 该时段「263 次 `snapshot from dimension queues built`」推断
「persist 采集成功、只是没提交」。**这个推断是错的**，因为：

```
admin/live_stream_redis_store_snapshot_fix.go:196  → "snapshot from dimension queues built"
domains/ursm/v2/persist/writer.go:426               → "ursm.v2: persist committed"
```

**前者属于 admin 的 live_stream redis store 修复子系统，与 persist writer
毫无关系。** 拿它当 persist 的采集证据，等于把两个子系统的日志混为一谈。

订正后，§5.2 里「快照在采集（263 次）却零提交」这句**依据不成立**，
采集侧没有任何有效证据。成立的只有「零提交」本身。

> 教训与 §5.4 那条同源：**证据的归属要先确认，再看它说了什么。**
> 消息名里带 `snapshot` 不足以证明它属于 persist writer。

#### ★ 迁键价值的第三条独立佐证

若路径 B（Flush 预算被挤占）成立，则迁键同样能解决它 ——
SCAN 迁到独立 db 后实测 **0.11s**，`Collect` 几乎不耗时，
`Flush` 自然拿回完整的 30s 预算。**迁键不只治"collect 超时"，还治"flush 没预算"。**

**⇒ 验收项再加一条**：迁键后统计 `persist flush failed` 是否归零。

**★ 顺带一个独立推论**：如果空洞确由 SCAN 超时导致，那它恰好是**迁键价值的
独立佐证** —— 迁到独立 db 后 SCAN 只遍历约 1.3K 键（实测 0.11s），
不再与 30s 预算同量级。若迁键后此类空洞消失，即构成闭环证据。
**建议把「空洞是否消失」列为迁键后的验收项之一。**

### 5.3.2 ★★★ 两条路径都被 error 原文实测坐实（2026-10-03 23:28）

上一版把「Collect 挤占 Flush 预算」写成"若成立"。**现在拿到证据了。**
`journalctl` 最早只到 20:07（154）/ 21:08（245），该窗口内的 WARN 原文尚在。

| | committed | collect failed | flush failed | 失败率 |
|---|---|---|---|---|
| **154** | 193 | **3** | **3** | **3.0%** |
| **245** | 141 | 0 | 0 | **0%** |

失败时间点 —— 间隔正好 60s，即每个 tick 一轮：

```
T20:09:47 collect failed      T21:14:47 flush failed
T20:10:47 collect failed      T21:15:47 flush failed
T20:11:47 collect failed      T21:16:47 flush failed
```

**路径 A 的 error 原文**（collect 超时）：

```
"persist collect failed","error":"redis scan failed: context deadline exceeded"
"persist collect failed","error":"ursm.v2.persist: read node hash:
    redis HGETALL failed after TYPE check: context deadline exceeded"
```

**路径 B 的 error 原文**（Flush 无预算）：

```
"persist flush failed","error":"insert row cid=37 model=gpt-5.6-terra:
    timeout: context deadline exceeded"
"persist flush failed","error":"insert row cid=63 model=claude-opus-5:
    timeout: context already done: context deadline exceeded"
```

**★ `timeout: context already done` 是最关键的一条** —— 字面含义是
**ctx 在 `Flush` 刚开始执行时就已经到期**。这与 §5.3.1 从代码读出的
「Collect 与 Flush 共用 30s，Collect 吃满后 Flush 无预算」**完全吻合，不是巧合。**

#### 三条由实测导出的新结论

1. **这是持续性缺陷，不是一次性事故。** 3.0% 失败率意味着
   **每天约 43 分钟的快照空洞**（0.03 × 1440）。今天 3 小时只发生 2 段 × 3 分钟，
   而 100 分钟那个空洞对应失败率约 **7%** —— 负载高时显著恶化。
2. **154 失败、245 零失败。** 同一批 Redis、同一套代码，只有 154 命中
   ⇒ 失败与**单实例状态**相关（连接/负载/调度），不是全局性问题。
3. **每段失败都是连续 3 个 tick**（20:09/10/11、21:14/15/16），
   说明不是随机抖动，而是**持续一段时间的慢** —— 这正好解释长空洞的形成方式。

**⇒ 迁键的收益从"预计"变成"已验证机制"**：SCAN 全库 12.95~30.40s 是
路径 A 的直接成因；迁到独立 db 后实测 0.11s，路径 A 消失；
Collect 不再吃满预算，路径 B 一并消失。**两台都会受益。**

#### ★★ 上一版有一处说法是错的：不是「空洞」，是「降级为单写」

上一版写"3.0% 失败率意味着每天约 43 分钟的**快照空洞**"。**这不准确。**
数据层逐分钟实测（2026-10-03 20:05~21:25）：

```
20:05 | 2540   20:08 | 2536      20:12 | 2532   20:19 | 2522
20:09 |    0 ★ 20:10 | 1267 ★    20:13 | 2529   20:20 |  —
20:11 |    0 ★                20:14 | 2532
                                  ...
21:15 | 1250 ★ 21:16 | 1250 ★    21:17 | 2500   21:24 | 2508
```

**journal 与数据层逐分钟精确咬合**（这就是「交叉数据源 > 缺失证据」）：

| journal 记录 | 数据层对应 | 含义 |
|---|---|---|
| 20:09 / 20:10 / 20:11 collect failed | 20:09 **0 行**、20:10 **1267**、20:11 **0 行** | 154 连续 3 轮零提交 |
| 21:14 / 21:15 / 21:16 flush failed | 21:15 **1250**、21:16 **1250** | 154 连续 3 轮零提交 |
| 正常时段 | ~2530 | 双写 |

**⇒ 落库速率是三态的，不是有/无两态：**

| 状态 | 行/分 | 何时 | 后果 |
|---|---|---|---|
| **双写** | ~2530 | 两台都成功 | 正常 |
| **单写（降级）** | ~1250 | **一台失败，另一台顶上** | **数据不丢，但写入量减半** |
| **真空洞** | 0 | **两台同时失败** | 真丢数据 |

**订正**：
- 3.0% 的失败率（154）⇒ **每天约 43 分钟处于「单写降级」态**，
  **不是**每天丢 43 分钟数据。
- **真空洞很罕见**（3 小时内 2 分钟），但 10-03 上午那次是 **100 分钟**
  ⇒ 那一次**两台都失败了**，与降级是完全不同的严重级别。

**★ 20:09 与 20:11 的 0 行如何解释**：154 在这三个 tick 零提交，而
245 本应顶上却也是 0 行。245 的 journal 最早只到 21:08，**该时段无记录**
⇒ 按 §5.4 的规矩，这里只能说「不可得」，**不能断言** 245 失败。
但「两台都没写」是数据层事实（0 行），这一点不依赖任何日志。

**★ 由此得到一个可直接监控的判据**（比看 failed 计数更贴近业务影响）：
按分钟统计 `ursm_node_snapshot_min` 的行数，三态阈值约
`< 500` = 真空洞、`500~1800` = 降级、`> 1800` = 正常。
**降级会立刻体现在存储量上（当日写入量减半），真空洞才会影响正确性。**

### 5.4 ★ 根因无法最终定位：证据已被轮转删除

追查了两个可能的日志源，**都已不可得**：

| 源 | 覆盖范围 | 能否拿到 10:20~12:00 |
|---|---|---|
| 154 `gateway-canary-8782.log` | 04:06 → 22:09 | ✗ 无 persist committed（旧版本不打） |
| 154 `gateway-canary-8781.log` | **16:13:55 → 16:29:57** | ✗ 只覆盖部署窗口本身 |
| 154 journald | **最早 18:58:44** | ✗ |
| 245 journald | **最早 19:52:26** | ✗ |
| 245 文件日志 | 无 gateway/canary 日志（走 journald） | ✗ |

**⇒ 范围已由 §5.3 锁定到 persist writer 内部，但最终根因无法用现有证据确认。**
若要继续追，必须先解决「日志留存短于排查周期」这个问题本身（见 §5.5）。

#### ★★ 一条必须订正的无效推论

追查中途我写过一句「154/245 在 09:30–12:30 都没有重启记录 ⇒ 排除服务重启」。

**这条推论无效，予以作废。** 那次 `journalctl --since 09:30 --until 12:30`
返回空，**原因是 journal 根本不覆盖那个时段**（最早只到 18:58/19:52），
而不是「那个时段没有发生重启」。

**「查询无记录」与「事件未发生」是两件事** —— 前者只说明日志留存窗口不够。
这与 §5.2 第一条是**同一个误判的两次踩中**：第一次差点把「日志没记录」
当成空洞原因，第二次差点把「日志没记录」当成排除依据。
**教训：凡是用日志缺失做正面推论，必须先确认 journal 的实际覆盖范围。**

> 注：§5.3 里「systemd 无该时段失败记录」这一项也要按同样的标准打折 ——
> 它同样是 journal 查询，**受同一轮转限制**。真正支撑「排除网关停机」的是
> `request_logs` 的交叉数据，不是那条 journal 查询。

### 5.5 附带建议：journal 留存窗口短于排查周期

两台 journal 最早只到当天 18:58 / 19:52，意味着**约 6 小时前的日志已不可查**。
对一次需要跨小时对齐的线上排查，这个窗口太短了。建议纳入运维改进项
（调大 `SystemMaxUse`/`MaxRetentionSec`，或把 persist 相关的 WARN 单独落文件）。

**⇒ 对外承诺建议写「约 5~7 GB」，不要写 3.6 GB，也不要只写 7.2 GB。**
若评审要求单值，取 **6 GB**（区间上偏保守，且含索引）。

---

## 6. 这对 §10 改造的实际影响

1. **容量收益要按「单写」承诺，不能按「双写」。** 245 退出 shadow 是
   分区化收益的前置条件 —— 不退出，稳定态就是现在的 10 GB，
   分区化只会让这 10 GB 每天归还一部分，而不是降到 5~7 GB。
2. **退出 245 shadow 的收益现在可以量化了**：写放大从 2.00× 降到 1.00×，
   即存储与索引写入量**立刻减半**（每分钟少 1,242 次 INSERT 及对应索引维护）。
3. **这不改变分区化的必要性**：即便单写，DELETE 型留存仍然不归还磁盘
   （§1.1 撤回的那条），分区化仍是唯一让空间每日回到操作系统的手段。

---

## 7. 本篇用到的查询（均可安全重跑）

全部有界：`pg_stat_*` 走系统目录；`ursm_node_snapshot_min` 的查询都带
`snapshot_ts > now() - interval ...` 上界，走 `_ts_idx` 范围扫描。

```sql
-- 落库速率（1h / 6h 窗口）
SELECT round(count(*)::numeric/60.0,1) AS rows_per_min
FROM ursm_node_snapshot_min WHERE snapshot_ts > now() - interval '1 hour';

-- 批指纹（写放方判别用，须配合两台日志，单独看会歧义）
SELECT to_char(date_trunc('second',snapshot_ts),'HH24:MI:SS') AS sec, count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '8 minutes' GROUP BY 1 ORDER BY 1;

-- 累计计数与索引数（O(1)）
SELECT relname, n_tup_ins, n_tup_upd, n_tup_del,
       (SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
        WHERE c.relname LIKE 'ursm\_node\_snapshot\_min%') AS idx_cnt
FROM pg_stat_user_tables WHERE relname LIKE 'ursm\_node\_snapshot\_min%';

-- 保留期实际跨度（min/max 走索引，O(1)）
SELECT min(snapshot_ts), max(snapshot_ts),
       round(EXTRACT(epoch FROM (max(snapshot_ts)-min(snapshot_ts)))/86400.0,3)
FROM ursm_node_snapshot_min;

-- 昼夜节律曲线（24h 有界，实测 6.7s）
SELECT date_trunc('hour', snapshot_ts) AS hr, count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '24 hours' GROUP BY 1 ORDER BY 1;

-- 定位写入空洞（10 分钟粒度；注意 date_trunc 不认 'ten_min'，要用 date_bin）
SELECT date_bin('10 minutes', snapshot_ts, '2000-01-01 00:00+08'), count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts >= '2026-10-03 10:00+08' AND snapshot_ts < '2026-10-03 12:40+08'
GROUP BY 1 ORDER BY 1;

-- 活跃节点数（1h 有界）
SELECT count(*) FROM (SELECT DISTINCT tenant_id, credential_id, raw_model_name
  FROM ursm_node_snapshot_min WHERE snapshot_ts > now() - interval '1 hour') t;
```

> ⚠️ 两个我在本篇踩到的换算坑（都写在这里免得下次照抄）：
>
> 1. **速率**：用 `n_tup_ins / (now() - stats_reset)` 时，时间单位极易搞错。
>    我第一次多除了一个 60，得到「27.2 行/分」。正确是 **1,639 行/分**
>    （stats_reset = `2026-09-23 06:56:38`，即 255.11 小时 / 10.63 天）。
> 2. **批大小**：`count(*)` 是「窗口内总行数」，要除以**批数**而不是 1440。
>    批数 = 窗口秒数 ÷ 批间隔（当前每 30s 一批）。除以 1440 会得到
>    「101.2 行/批」这种明显不可能的数。

> ⚠️ 累计计数要配 `pg_stat_database.stats_reset` 解读
> （本窗口实测 `2026-09-23 06:56:38`，即 255.11 小时 / 10.63 天）。
> **不要**用 `n_tup_ins / now() - stats_reset` 直接当速率，中间的
> 时间单位换算极易出错 —— 我第一次就多除了一个 60，得到「27.2 行/分」
> 这种明显不可能的数字。（订正后：10.63 天平均 **1,639 行/分**。）

---

## 8. ★★ 追加：DEFAULT 分区拍板项的仓库级定论（2026-10-03 22:20）

评审稿 §10 拍板 #4 建议「**不建** DEFAULT 分区，理由是黑洞 + ATTACH 变慢 +
静默丢数优于响亮失败」。**这条建议与本仓库既定模式相悖，应予推翻。**

### 8.1 仓库既有模式就是「预建 + DEFAULT 兜底」

全仓已有 6 张表采用 DEFAULT 分区（`sql/migrations/` 实测）：

| 表 | 出处 |
|---|---|
| `platform_outbox` | `local/641_...` |
| `request_wal` | `startup/332_...` |
| `routing_decision_log` | `startup/333_...` |
| `stats_event_inbox` | `startup/536_...` |
| `usage_facts` | `startup/537_...` |
| `auto_route_selections` | `startup/478_...` |

且 `startup/473_partition_precreate_2026_09_10.sql` 的预建循环第三步注释写的是：

```sql
-- 3. Add default partition if missing (rule 33 §2 兜底)
IF NOT has_default THEN
    EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I DEFAULT', ...);
```

**⇒ DEFAULT 分区是仓库规范（rule 33 §2）要求的兜底，不是可选项。**

### 8.2 而且按日分区已有现成机制，不必新写

`bg/partition_manager.go` 的 `archiveSpec.partitionUnit` 字段：

```go
// partitionUnit controls how ensureNextMonthPartitions derives the ...;
// "day" → AddDate(0,0,offset) (daily partitions, R68 迁移 750 usage_facts 按日分区接入)
partitionUnit string
```

`usage_facts` 已用 `partitionUnit="day"` 接入按日分区并跑了几个月。
**⇒ `ursm_node_snapshot_min` 只需在 spec 里加一条 + `partitionUnit="day"`，
不需要另建 ensure 函数**（评审稿改动清单 #1 的「新增迁移建 ensure 函数」
与 #4 的「加 spec 条目」可以合并成后者）。

### 8.3 ★ 切换顺序有一条硬约束（踩坑史就在仓库里）

`750_usage_facts_daily_partition.sql` 的注释记录：

> **PG 对「父表挂 DEFAULT 分区时新建具体分区」会先校验 DEFAULT 内无落入
> 区间内的行**。存量数据环境（252 共享 PG 的 telemetry 持续写入 DEFAULT）
> **首启必踩**。

修法是六步：移走 DEFAULT 内的界内行 → 建空表 → `ALTER TABLE ... ATTACH`。

**⇒ 对本改造的直接影响（且是个好消息）**：
`ursm_node_snapshot_min` 现在是**普通表**（`relkind='r'`，0 分区），
**没有 DEFAULT 分区**，切换时 DEFAULT 天然为空 ⇒ **不存在这个校验陷阱**，
比 `usage_facts` 的切换简单一整级。但**顺序仍必须遵守**：
挂 DEFAULT 与建首个具体分区不能反着来。

### 8.4 订正后的拍板 #4

| | 评审稿建议 | 订正 |
|---|---|---|
| DEFAULT 分区 | 不建 | **建**（仓库既定模式 + rule 33 §2 兜底） |
| 越界/未来数据 | 响亮失败 | 落 DEFAULT catch-all（与 `usage_facts` 一致） |
| 监控 | 需另设「有行即报警」 | 复用 `usage_facts` 的既有约定即可 |

代价是评审稿原先列的三条确实存在（黑洞、ATTACH 校验、需监控），
但它们是**本仓库已经付过的代价**，不是本次新增的风险。
真正的新增风险只有一条：**§8.3 的挂载顺序**。

**⇒ 拍板 #4 不再是开放问题，按仓库惯例即可定。**

### 8.5 ★★ 拍板 #1 收敛成一个真问题：7 天是否短于审计需求

`domains/ursm/v2/persist/retention.go:37-38` 的注释是决定性的：

```go
// DefaultSnapshotRetentionConfig 返回默认配置。30 天审计窗口与容量基线
// 报告的建议一致（快照仅用于 URSM v2 cutover 前后的状态审计/比对）。
```

**⇒ 30 天不是随手写的默认值，是「cutover 前后审计/比对」的窗口。**
`envs.samples/CONFIG-REFERENCE.md:136` 也记着「默认 30d / 7d」两套口径。

而生产设的是 **7 天**（`URSM_SNAPSHOT_RETENTION_DAYS=7`）。

**这构成一个真实的矛盾，且它是时间性的**：

| 时段 | 快照的用途 | 7 天够不够 |
|---|---|---|
| cutover 进行中（现在，至 **10-10**） | 245 shadow 第 7 天结论的比对证据 | **可能不够** —— 门禁 `identical ≥ 10000` 已在缺口 40×，若还要回溯更长窗口做比对，7 天是硬约束 |
| cutover 完成后（10-10 起） | 注释里的审计窗口失去对象 | 够，且 7 天更省空间 |

**⇒ 拍板 #1 真正要问的是：cutover 结论出完后，快照还需要保留多久做审计？
30 天是为此设的，还是 7 天即可？** 这个答案决定分区粒度与容量：

- 若定 30 天 ⇒ 稳态分区数 31、容量 13~19 GB，分区化收益大幅缩水
- 若定 7 天 ⇒ 稳态分区数 8~10、容量 5~7 GB，收益成立

**我倾向 7 天**（cutover 证据应在结论产出后即归档，而非无限期留在热表），
但**这是 runbook 契约口径，按规矩不代你改阈值**。

### 8.6 拍板 #7 收敛：`_ts_idx` 不该删，但基线那行要改

索引定义（两份 schema 同 md5，必须同步）：

```sql
CREATE INDEX ursm_node_snapshot_min_ts_idx
  ON public.ursm_node_snapshot_min USING btree (snapshot_ts);
```

全仓对这张表的引用只有 4 类（`.db-audit/` 与 `sql/fixes/` 实测）：

| 用途 | 需要 `_ts_idx` 吗 |
|---|---|
| `count(*)`（`07_dist2.sql:33`） | 不需要 |
| `min(snapshot_ts)` / `max(snapshot_ts)`（`07_dist2.sql:45`） | **需要** |
| `pg_stats` 宽度/空值率探查（`04_slow2` `09_final` `10_cols`） | 不需要（走系统目录） |
| `ANALYZE`（`sql/fixes/2026-10-02-...sql:269`） | 不需要 |

**⇒ 它有真实用途（审计脚本的 min/max），不应删除。**
分区化之后，父表上的这行索引会**自动在每个分区上创建对应索引**，
所以真正要处理的不是「删不删」，而是**基线 `:27486` 那一行在父表上保留即可**，
删掉反而会让分区子表失去索引。

**⇒ 拍板 #7 不再是开放问题。**（评审稿把它列为"删除本身是一个 DDL 动作"
属于误判——它不是可选项，是审计查询的依赖。）

### 8.7 ★ 拍板 #8 定论：技术面几乎零影响（而且「静默归零」的说法本身不准确）

**先纠正一个流行说法**：分区化后 `pg_stat_user_tables` 里父表
`ursm_node_snapshot_min` 的 `n_live_tup` 确实会近乎为 0，
但**分区子表本身就在 `pg_stat_user_tables` 里**（它们是独立普通表，`relkind='r'`），
所以不按表名过滤的通用看板**不会丢数据**，只会「一张大表变成多行小表」。

**真正要看的是「哪些地方按具体表名过滤」**（全仓实测）：

| 位置 | 性质 | 分区化后是否要改 |
|---|---|---|
| `scripts/252-monitor/pg17-index-bloat.sh` | **周日凌晨自动跑** | **不用改** —— 候选查询已带 `AND NOT c.relispartition`，显式排除分区索引；且 `--fix` 只做 `REINDEX INDEX CONCURRENTLY`，**从不 DROP 索引**，不会破坏分区结构 |
| `scripts/252-monitor/ursm-snapshot-health.sh` | 本会话新增（未上 cron） | **不用改** —— 查父表，分区化后 PG 自动路由到子表 |
| `.db-audit/sql/{04_slow2,07_dist2,09_final,10_cols,23_crashlog}.sql` | 一次性手工审计脚本 | 不用改 —— 手动触发，且 `count(*)`/`min/max` 对分区父表仍可用 |
| `sql/fixes/2026-10-0*-*.sql` | 已执行完的历史修复脚本 | 不用改 —— 存档性质 |
| `deploy/prometheus/rules/` | 告警规则 | **无针对该表的规则**（`routing-credential-state.yml` 里的 `ursmv2` 是 Redis 状态机 reset，与本表无关） |

**⇒ 拍板 #8 不再是开放问题。** 唯一需要在实施文档里写一句的是：

> 看板上看到 `ursm_node_snapshot_min` 父表 0 行 + 若干 `ursm_node_snapshot_min_YYYY_MM_DD`
> 子表，**是分区化的预期形态，不是故障**。排查时需按 `pg_inherits` 汇总。

**性能提示**：`min(snapshot_ts)` / `max(snapshot_ts)` 在分区化后会扫**每个分区**的索引
（当前 1 个 → 未来 ~8 个），比现在慢数倍。`.db-audit/sql/07_dist2.sql:45` 依赖它，
属手工脚本可接受；**但不要把它放进任何高频巡检**。

---

## 9. 仍未定论的（不要当结论用）

- **§5.2 那 100 分钟空洞（10-03 10:20~12:00）的最终根因** ——
  范围已由 §5.3 交叉数据锁定在 **persist writer 内部**（网关/DB/误删均排除），
  但该时段 error 原文随日志轮转丢失（§5.4），**方向是 collect 阶段超时，
  这仍是推断不是证据**。
- 空洞仅占 25% 缺口的约 4%，**缺口主因已定位为昼夜节律**（§5.1），
  这一条不再是开放问题。
- Finding D 里「1.83×」这个旧数字的来源已无从追溯；本篇只确立**当前实测是 2.00×**。
- 245 退出 shadow 后的**实际**稳态体积仍需实测确认，本篇是折算值不是实测值。
