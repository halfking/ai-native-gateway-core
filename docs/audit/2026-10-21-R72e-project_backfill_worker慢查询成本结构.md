# 2026-10-21 · R72e 轮 —— 把「5.5 秒」拆开：扫描 1.4s + 等待 ~4.1s（并推翻 R72d 的归因）

> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`（llm-gateway-go 主 checkout）
> **对生产的写入 = 0**（211 / 252 全程只读；唯一的写是 EXPLAIN ANALYZE 跑在**纯 SELECT** 上，未触碰任何写路径）。
> 上游上下文：R72d（生产 v1 族形态差分）⇒ 本档是对 R72d §1.2 归因的**订正与深化**。

---

## §0 摘要

R72d 记录了 252 上最慢的语句：`bg/project_backfill_worker.go` 的 CTE，**868 次 × 5,538 ms（累计 80 分钟）**。
R72d 当时写下的归因是「★ **不是扫表**，而是**每行函数开销**」⇒ ⇒ ★★★ **本轮把这条归因推翻了**：

| # | 结论 | 证据 | 证据类型 |
|---|---|---|---|
| 1 | ★★★★ **「每行函数开销」不存在** | 计划里 `Filter` **直接展开**成 `COALESCE(NULLIF(btrim(project_id),''), CASE …)` ⇒ **没有函数调用节点** ⇒ PostgreSQL **内联**了它 | 只读 `EXPLAIN (ANALYZE)` |
| 2 | ★★★★ **只读扫描只花 1,437 ms，而且是**零物理读** | `rows=14081` / `loops=14081` / `Buffers: shared hit=69654`（**没有一条 `read=`**）/ `Execution Time: 1437.366 ms` | 只读 `EXPLAIN (ANALYZE)` |
| 3 | ★★★★ **那「别的东西」是磁盘 IO，**不是**锁等待** | ★★ **采样器直接抓到了 worker 本体**：连续 **27.7 秒**，`wait_event_type = IO` / `wait_event = DataFileRead`（**82/88** 样本），**`pg_blocking_pids` 全程 `{}`**；★ **量具侧证**：`pg_stat_statements` 显示每次调用 `shared_blks_read ≈ 285`（≈ **2.2 MB 物理读**）、`temp_blks_read = 0` | 采样器（§3.4）+ `pg_stat_statements` |
| 4 | ★★★★ **「量具确认有牙」这句话必须降级** | ★★★ **实测：`pg_stat_activity` 在 PG 17.10 里被**事务级统计快照**缓存** ⇒ 同一事务内隔 9 秒再读仍是**陈旧** ⇒ ★★ **⇒ 前三版采样器全都退化成「一次观测 × N 行」** | 只读探针（§3.3） |
| 5 | ★★★ **「三次零命中」不需要任何假说** | 该语句**占空比 ≈ 1.32%**（每 ~10 分钟跑一次、单次中位 7.9 s）⇒ ★★ **⇒ 单次快照撞上它 ≈ 1.3% ⇒ 三次全落空 ≈ 96.1%** | §1.2 + §3.3 |
| 6 | ★★★ **耗时分布要重算，而且是**上升**趋势** | ★★ **干净样本 = 21 个**（不是 13 个；且剔除了 **2 条「我自己采样器」的自污染条目**）⇒ ★★ **全部 21 个：均值 11,925 / p50 7,905 / max 44,223 ms（离散度 29.2×）** ⇒ ★ **早期 13 个（均值 5,704）vs 晚期 8 个（均值 22,035）⇒ 涨了 3.86×** | PG 日志（已剔除自污染） |
| 7 | ★★★ **最省时的样本 ≈ 扫描成本** | min = **1,517 ms** ≈ 扫描 1,437 ms ⇒ ⇒ **「没事的时候」这条语句就值 1.5 秒** | ① + ⑥ |
| 8 | ★★★ **但我 §2 的「`project_dim` 热行等锁」假设**打偏了** | ★★★ **实测没有锁竞争**（`blockers` 全空、`wait_event` 是 IO 不是 Lock）⇒ ⇒ **⇒ 那 17 行热表仍然值得优化（净效果只有刷新一个没人读的 `updated_at`），但它**不是**这次慢的原因** | §3.4 + §4.1 |
| 9 | ★★ **这条慢查询是「跨环境口径差」的样本** | ★ **211 自己的 `acc-pg17` 24 小时内没有这条语句** ⇒ ⇒ **⇒ 跑它的是连 252 库的那台网关** ⇒ ⇒ **⇒ 正是那台还没部署新二进制的** | `docker logs acc-pg17` |
| 10 | ★★★★ **归因落到表：物理读热点是 `session_dim` 的 pkey 探测** | ★★ **慢/快两次运行对照**（同一份 SQL）：★ **慢**（日志 19,960 ms）⇒ 物理读 **+8,745**，其中 **`session_dim` +6,323**；★ **快**（日志 3,996 ms）⇒ 物理读 **+102**，其中 **`session_dim` +0** ⇒ ⇒ ★★★ **⇒ 耗时差 5 倍、物理读差 80 倍 ⇒ 变量部分是「缓存命中率」，不是「代码退化」**；★★ **⇒ 而那 14,081 次探测里有 99.87% 结构上永远解析不出来** | `pg_statio_user_tables` 3 秒一档快照差分 |

⇒ ⇒ ★★★★ **⇒ 结论形态：「慢」有三种（扫描慢 / **等锁** / **等 IO**），而**只读 EXPLAIN 只能证伪第一种** ⇒ ⇒ ⇒ ★★★ **⇒ 必须把「计划」「耗时分布」「瞬时 wait event」三件事分开量。**

⇒ ⇒ ★★★★ **⇒ 本轮一共四次自我订正，四次错的都是「量具」**：
★★ ① **函数被内联**（R72d 的「每行函数开销」不成立）⇒ ★★ ② **「等锁」其实是「等 IO」** ⇒ ★★★ ③ **采样器被事务快照压扁成一次观测** ⇒ ★★ ④ **抽日志的过滤器把量具自己读了进来**。

⇒ ⇒ ★★★★ **⇒ 落到行动上，正确顺序是：① 先把 IO 来源定位到表（已完成 = `session_dim`）⇒ ② 再决定改不改 ⇒ ③ 改之前先算语义代价。**
★★ **⇒ §4.1 那两条候选修法（「2b 改 `DO NOTHING`」/「缩小 48h 窗口」）的收益估算都建立在「等锁」这个已被证伪的前提上 ⇒ ⇒ ★★ **⇒ 真正对得上「物理读热点」的修法是覆盖索引 / 缩窗口 / 把 futile 的判定前移（详见 §4.3）。**

---

## §1 三把尺子，量同一个东西

### §1.1 尺子 A：只读 `EXPLAIN (ANALYZE)` —— 扫描成本

252 上跑**纯 SELECT 版**（CTE 与生产语句逐字相同，只把末尾的 `sync_session_project_attr(...)` 换成 `count(*)`）：

```
Aggregate  (actual time=1437.075..1437.076 rows=1 loops=1)
  Buffers: shared hit=69654
  ->  Limit  (actual time=1437.055..1437.059 rows=18 loops=1)
        ->  Sort  (Sort Method: quicksort  Memory: 26kB)
              ->  Nested Loop  (actual time=1436.220..1436.939 rows=18 loops=1)
                    Buffers: shared hit=69651
                    ->  Index Scan using idx_session_summaries_project_null_recent on session_summaries ss
                          (actual time=0.160..306.233 rows=14081 loops=1)
                          Index Cond: (created_at >= (now() - '48:00:00'::interval))
                    ->  Index Scan using session_dim_pkey on session_dim sd
                          (actual time=0.079..0.079 rows=0 loops=14081)
                          Index Cond: ((gw_session_id)::text = (ss.session_key)::text)
                          Filter: ((NOT ((tenant_id)::text IS DISTINCT FROM (ss.tenant_id)::text)) AND
                                   (COALESCE(NULLIF(btrim(project_id), ''::text),
                                    CASE WHEN (COALESCE(btrim((application_code)::text), ''::text) <> ''::text)
                                    THEN ('app:'::text || btrim((application_code)::text))
                                    ELSE NULL::text END) IS NOT NULL))
                          Rows Removed by Filter: 1
                          Buffers: shared hit=56324
Execution Time: 1437.366 ms
```

⇒ ⇒ ★★★★ **三条读数**：
1. ★★ **扫描面 = 14,081 行候选**（规划估 **21 行**，实测 14,081 ⇒ **低估 670×**）⇒ ⇒ 14,081 次 `session_dim` pkey 探测（56,324 buffers）。
2. ★★★ **过滤谓词已被内联**（`Filter` 里是展开后的表达式，**没有函数调用节点**）⇒ ⇒ ★★ **「每行函数调用开销」这个归因不成立。**
3. ★★ **扫描本身 1.44 秒** ⇒ ⇒ ★★★ **5.5 秒里剩下的 ~4.1 秒不在扫描里。**

★ **顺带一条结构性事实**：`session_dim` 48h 窗口内 `application_code` 为空的占 **14,063 / 14,081**
⇒ ⇒ ★★★ **⇒ 候选集里 99.87% 的行「结构上永远不可解析」**（`gw_resolve_project_ref` 两个来源都空 ⇒ 恒返回 NULL）
⇒ ⇒ ★★ **⇒ 但它们每 10 分钟被重扫一遍**（这正是该 worker 注释里自陈的老问题：48h 窗口把 128,699 行的空扫裁到 ~1.4 万行）。

### §1.2 尺子 B：PG 日志的真实耗时分布 —— 等待的指纹

> ⚠ **【订正横幅二 · 2026-10-21 R72e 本轮内】本节的样本集与结论都已被本轮后续实测修正，原文保留。**
> ★★★ **两个问题**：① 样本集被**截断**（400 MB 日志尾只覆盖到 06:22）；② 抽日志的过滤器**把量具自己读了进来**（§1.2.1）。
> ⇒ ⇒ ★★ **⇒ 修正后的口径见 §1.2.2：干净样本 21 个、均值 11,925 ms、且呈**上升**趋势。**

252 的 PG 容器日志通道（`log_min_duration_statement=1000` ⇒ >1s 的语句逐条落盘）：

```
/var/lib/containers/storage/overlay-containers/dc5e54272d7a35612cf32a1dd993d0e0610c36a9bf877c305bca4e8607fe2f5a/userdata/ctr.log
```

抽该语句的 `duration:` 行（尾 400 MB 内 13 个样本）：

| 统计量 | 值 |
|---|---|
| 样本数 | 13 |
| min | **1,517 ms** |
| p50 | **5,108 ms** |
| max | **12,186 ms** |
| 均值 | 5,704 ms |

全部值：`1517 2318 2327 3447 4394 4866 5108 5398 6268 7720 7905 10699 12186`

⇒ ⇒ ★★★★ **三个推断**：
1. ★★ **均值 5,704 ms 与 `pg_stat_statements` 的 5,538 ms 吻合** ⇒ ⇒ ★★ **⇒ 两条独立量具互相印证，这条慢查询是真的。**
2. ★★★★ **离散度 8×（1.5s → 12.2s）是「等待」的指纹**：固定成本（扫描 1.44s）解释不了 8 倍的波动。
3. ★★★ **min = 1,517 ms ≈ 扫描成本 1,437 ms** ⇒ ⇒ ★★ **⇒ 「没有竞争时这条语句就值 1.5 秒」** ⇒ ⇒ ⇒ ★★ **⇒ ⇒ 变量部分（0 → 10.7s）就是等待。**

★ **日志里还有两条更硬的确认**：
- ★★ **该语句确实每 ~10 分钟跑一次**（实测两次落在 `06:12:08` 与 `06:22:17`，后者耗时 **9,583 ms**）⇒ ⇒ **⇒ 「80 分钟累计」是当前仍在发生的，不是历史存量。**
- ★★ **全库 947 MB 日志里 `waits for` 出现 0 次** ⇒ ⇒ ★★ **⇒ 没有任何**单次**锁等待超过 1s**（`log_lock_waits=on` + `deadlock_timeout=1000ms`）
  ⇒ ⇒ ⇒ ★★★ **⇒ ⇒ 等待是「许多次短等待的累加」，不是「一次长等待」** ⇒ ⇒ ⇒ ★★ **⇒ ★ 这就是为什么只看日志找不到它 —— 必须用采样器抓瞬时 wait event。**

### §1.2.1 ★★★★ 量具事故 3：**抽日志的过滤器把我自己的采样器读成了被测样本**

★★ **触发**：§1.2 的抽法是「在日志块里找**含 `sync_session_project_attr`** 的块」⇒ ⇒ ★★ **而我的采样器 SQL 里就写着这个字面量**（`query ILIKE '%sync_session_project_attr%'`）⇒ ⇒ ★★★ **⇒ 采样器自己的日志块命中了过滤器。**

★★ **证据**：把日志块连 `statement:` 原文一起打出来（`tail -c 900000000`）：

```
2026-10-21T06:22:19.302201032+08:00 stderr F 2026-10-21 06:22:19.301 CST [1779177] LOG:  duration: 655720.719 ms  statement:
2026-10-21T06:22:19.302201032+08:00 stderr F 	SELECT s.i, clock_timestamp() AS ts, a.pid, a.client_addr, a.state, a.wait_event_type, a.wait_event,
...
2026-10-21T06:38:16.489230138+08:00 stderr F 2026-10-21 06:38:16.489 CST [1780219] LOG:  duration: 730754.894 ms  statement:
2026-10-21T06:38:16.489230138+08:00 stderr F 	SELECT s.i, clock_timestamp() AS ts,
2026-10-21T06:38:16.489230138+08:00 stderr F 	       w.seen_at, w.pid, w.client_addr, w.state, w.wait_event_type, w.wait_event,
```

⇒ ⇒ ★★★★ **⇒ 那两个「12 分钟的样本」是我的采样器，不是 worker** ⇒ ⇒ ★★★ **⇒ 本轮第三次量具事故。**

⇒ ⇒ ★★★★ **⇒ 教训（比「过滤器要写准」更普适）**：★★ **「量具的指纹会出现在被测样本里」** ——
凡是**用关键字匹配日志 / 抓包 / 指标**来筛被测对象，★ **必须先排除量具自己**（按 PID、按进程、按 `statement:` 的开头文本）。
⇒ ⇒ ★★ **⇒ 否则你测到的「异常样本」，恰恰就是「量具正在运行」的样本。**

### §1.2.2 ★★★ 修正后的耗时分布：**21 个干净样本，而且是**上升**趋势**

★ **口径**：`tail -c 900000000` 取 900 MB 日志尾 ⇒ 逐块筛含关键字的块 ⇒ ★★ **剔除 §1.2.1 的 2 条自污染** ⇒ ★ **⇒ 得 21 个样本**
（原 §1.2 只报了 13 个，因为 **400 MB 尾把窗口截断在 06:22**）。

| 统计量 | 早期窗口（03:52–06:12，13 个） | 晚期窗口（06:22–07:32，8 个） | 全部（21 个） |
|---|---|---|---|
| min | **1,517 ms** | 9,583 ms | **1,517 ms** |
| p50 | **5,108 ms** | 21,440 ms | **7,905 ms** |
| max | **12,186 ms** | **44,223 ms** | **44,223 ms** |
| 均值 | **5,704 ms** | **22,035 ms** | **11,925 ms** |
| 离散度 | 8.0× | 4.6× | **29.2×** |

⇒ ⇒ ★★★★ **⇒ 三个订正**：
1. ★★★ **离散度不是 8×，是 29.2×** ⇒ ⇒ ★★ **⇒ 变量部分的量级比原判大一个档次。**
2. ★★★★ **它不是「随机波动」，是**上升**趋势**：★ **晚期 8 个的均值 = 早期的 3.86×** ⇒ ⇒ ★★ **⇒ 这与「等 IO」的解释方向一致（共享库上 IO 竞争加剧），与「随机竞争」不一致。**
3. ★★★ **原 §1.2 那句「均值 5,704 与 `pg_stat_statements` 的 5,538 吻合 ⇒ 两条独立量具互相印证」也不成立** ⇒ ⇒ ★★ **⇒ 两者不是同一总体**：`pg_stat_statements` 的统计窗口是 **3 天**（`stats_reset = 2026-10-18 22:03:37`，893 次调用，均值 5,599 ms）⇒ ⇒ ★★★ **⇒ 正确的说法是：近期这 8 次（均值 22.0 s）是它自己 3 天均值（5.6 s）的约 4 倍** ⇒ ★★ **⇒ 这才是「它在变慢」的量化说法。**

⚠ **诚实边界**：★ **晚期窗口正好覆盖了我自己的重采样时段（06:26–07:33）** ⇒ ⇒ ★★ **⇒ 我无法干净地把「我的测量负载」与「真实增长」分开** ⇒ ⇒ ★★★ **⇒ 能确定的硬事实只有三条：① 变慢是真的；② 变量部分落在磁盘 IO 上；③ 早期基线（我还没开始重采样时）是 1.5–12.2 秒。**

### §1.3 尺子 C：`pg_stat_statements` 的盲区（★ 量具本身的缺陷）

252 的 `pg_stat_statements` 里**查不到**函数内部的语句 —— ★★ **精确计数（不是 grep 推断，是实查）**：

```
insert_project_dim     | 0     ← 2b 的 INSERT … ON CONFLICT
update_ss_gwproject    | 0     ← 2a 的 UPDATE session_summaries … gw_project_id
attr_insert            | 0     ← 2c 的 INSERT session_project_attribution
func_sync              | 2     ← 外层语句（worker）+ 我自己的 EXPLAIN ANALYZE
```

⇒ ⇒ ★★★ **根因：`pg_stat_statements.track = 'top'`** ⇒ ⇒ ★★ **⇒ 只记顶层语句，PL/pgSQL 函数体内的语句一概不记**
⇒ ⇒ ⇒ ★★★ **⇒ ⇒ 「函数内部到底慢在哪」在生产上完全看不见** ⇒ ⇒ ⇒ ★★ **⇒ 这条必须写进「给 211 启用慢查询量具」那条决策的注意事项里：光装扩展不够，还要 `track=all`（有开销，需属主定）。**

★ 另一条量具风险：`pg_stat_statements.max = 5000`，而当前已有 **4,878 条** ⇒ ⇒ ★★ **⇒ 已接近淘汰上限**，高频语句可能把低频语句挤掉。

---

## §2 等待的对象：17 行的 `project_dim`

> ⚠ **【订正横幅三 · 2026-10-21 R72e 本轮内】本节的立论前提「那 ~4.1 秒是**等锁**」已被 §3.4 实测推翻，原文保留。**
> ★★★ **实测：worker 全程 `pg_blocking_pids = {}`、`wait_event = IO` / `DataFileRead` ⇒ 根本没有锁竞争。**
> ⇒ ⇒ ★★ **⇒ 本节仍然成立的部分**：`project_dim` 只有 17 行却被 UPDATE 了 69,751 次；2b 的 `DO UPDATE` 净效果只有「刷新一个没人读的 `updated_at`」
> —— ★★ **这些是真实的结构事实，也仍然值得优化，但它们**不是**这次慢的原因。**
> ⇒ ⇒ ★★★ **⇒ 真正的成本落在磁盘 IO 上（§3.4）⇒ ⇒ ★★ **本节的两个候选修法若按「能省 4.1 秒」立项，是错的（§4.1）。**

### §2.1 表的形态

```sql
SELECT relname, n_tup_ins, n_tup_upd, n_tup_hot_upd, n_live_tup FROM pg_stat_user_tables
WHERE relname = 'project_dim';
-- project_dim | 3 | 69751 | 69583 | 17
```

⇒ ⇒ ★★★★ **⇒ 只有 17 行，却已经被 UPDATE 了 69,751 次（69,583 次是 HOT update）**
⇒ ⇒ ⇒ ★★★ **⇒ 平均每行 ~4,100 次 UPDATE** ⇒ ⇒ ⇒ ★★ **⇒ 这是全库最热的一小组行之一。**

### §2.2 谁在写它：`sync_session_project_attr` 的三段

`sql/migrations/startup/762_session_project_backfill_chain.sql` 定义（**仓内真相源**）：

| 段 | 语句 | 锁行为 |
|---|---|---|
| **2a** | `UPDATE session_summaries SET gw_project_id = v_ref WHERE session_key = … AND tenant_id IS NOT DISTINCT FROM …` | ★ **行级排他锁**（`session_summaries` 826,676 行 / 178,184 次 UPDATE 的热表） |
| **2b** | `INSERT INTO project_dim … ON CONFLICT (project_ref) DO UPDATE SET name=…, enabled=TRUE, updated_at=NOW() WHERE project_dim.synced_from_acc_at IS NULL` | ★★ **`DO UPDATE` 会锁住冲突行 ⇒ 会等锁**（对比 `DO NOTHING`：不锁已有行） |
| **2c** | `INSERT INTO session_project_attribution … ON CONFLICT (tenant_id, gw_session_id) DO NOTHING` | ★ `DO NOTHING` **不等锁**（只在插入路径上冲突） |

⇒ ⇒ ★★★ **⇒ 2b 是嫌疑最大的一段：它把 worker 的 18 个目标行，映射到 `project_dim` 的 17 个热行上。**
⇒ ⇒ ★★ **⇒ 18 个目标行 → 18 次热行 upsert → 每次都可能等一个并发持锁者 ⇒ ⇒ 累加成 ~4.1 秒。**

### §2.3 ★★★ 「那个 DO UPDATE 到底改了什么」—— 逐列核对

`v_ref` 的形态是 `'app:' || btrim(application_code)`，而 `project_ref` 正是 `v_ref` 本身 ⇒ ⇒ ⇒

| SET 子句 | 实际效果 | 判定 |
|---|---|---|
| `name = EXCLUDED.name` | `EXCLUDED.name = substring(v_ref FROM 5)` = **由冲突键 `v_ref` 唯一决定** ⇒ 行里的 `name` 本来就是它 | ★★★ **确定性 no-op** |
| `enabled = TRUE` | `project_dim.enabled NOT NULL DEFAULT TRUE`；唯一的置假路径是 ACC 同步的批量 disable，而它带 `WHERE synced_from_acc_at IS NOT NULL` ⇒ **碰不到 `app:*` 行** | ★★★ **no-op** |
| `updated_at = NOW()` | 唯一真正在动的列 | ★★ **但本仓无读方** |

★ **读方核对**（全仓 grep）：`domains/analysis/projectattr/store.go:217` 只取
`project_ref, name, match_keywords, repo_paths`；`admin/acc_projects.go` 只用 `synced_from_acc_at` 做同步元信息
⇒ ⇒ ★★★ **⇒ `project_dim.updated_at` 在本仓没有任何读方。**

⇒ ⇒ ★★★★ **⇒ 结论：2b 这个 `DO UPDATE` 的**净效果只有「刷新一个没人读的 `updated_at`」**
⇒ ⇒ ⇒ ★★★ **⇒ ⇒ 把它换成 `DO NOTHING` 可以：① 拆掉热行锁竞争 ② 少 69,751 次 UPDATE/WAL**
⇒ ⇒ ⇒ ★★ **⇒ ★ 但它**不是零语义变更**：`enabled` 的「自动复活」语义会消失，且本库是**共享库**（别的产品可能读 `updated_at`）**
⇒ ⇒ ⇒ ★★ **⇒ ⇒ 因此它是**属主决策项**，不是本轮可以单方面落地的东西（见 §4）。**

### §2.4 ★★ 多环境存在性：211 自己的库上**没有**这条语句

按 §143.19 确立的通道（211 用 **docker** ⇒ `docker logs` 正是它**唯一**的读法）：

```bash
ssh 211 'docker logs --since 24h acc-pg17 2>&1 \
  | grep -a "sync_session_project_attr" | grep -aoE "duration: [0-9.]+ ms"'
# ⇒ （24h 内无该语句）
```

⇒ ⇒ ★★★ **⇒ 211 自己的 `acc-pg17` 24 小时内没有这条语句** ⇒ ⇒ ★★ **⇒ ⇒ 跑它的是「连 252 库的那台网关」**
⇒ ⇒ ⇒ ★★ **⇒ 与 R72d 的发现对齐**：252 的 `pg_stat_activity` 显示两台主机连库（`.209` = 211、`.241` = 另一台）
⇒ ⇒ ⇒ ★★★ **⇒ ⇒ ★ 这条慢查询是**跨环境口径差**的一个具体样本：同一个 worker，在 211 自有库上没有，在共享的 252 库上有。**
⇒ ⇒ ⇒ ★★ **⇒ ⇒ 而且它和 R72d 的部署前置顺序是同一件事的两面**：★ **跑这条慢查询的，正是那台还没部署新二进制的网关。**

### §2.5 ★★★ 第三版采样器抓到的两件事（无过滤抓全部活动后台）

第三版采样器**不带任何 SQL 模式过滤**（只抓 `state <> 'idle'`），⇒ ★ **它抓到的不是 worker，而是另外两个正在跑的东西**：

| 观测 | 内容 | 归属判定 |
|---|---|---|
| **A** | ★ `REFRESH MATERIALIZED VIEW CONCURRENTLY session_task_stats`，来自 `172.16.2.209`，`wait_event_type = IO` / `wait_event = DataFileRead` | ★★ **产品代码路径**（网关的物化视图刷新 worker）⇒ ★★ **⇒ 这正是 §143.20 记过的「4 个 matview 的 REFRESH 占本库慢查询 DB 时间的 25.3%」的现场** ⇒ ★ **⇒ 属主决策项 #1 的同一条线** |
| **B** | ★ `SELECT 'v2.session_turns' AS fam, count(*), min(ts), max(ts) FROM public.session_turns UNION ALL … session_turn_details … sessions …`，`application_name = 'psql'`，**在 `.209` 与 252 本机各有一个**（252 上那个 `client_addr` 为空 ⇒ 本机 psql），`wait_event_type = IO` / `DataFileRead` | ★★★ **⇒ ⇒ ⇒ 不是产品代码：★ 是并行会话的只读审计在两台实例上跑同一段诊断 SQL** ⇒ ⇒ ⇒ ★ **⇒ 但它是**真·全表扫描**（`count(*)` + `min/max` 扫全表）⇒ ⇒ ⇒ ★★ **⇒ ⇒ ⇒ 它是「真实 IO 压力」，但**不是本仓缺陷**（本仓代码里没有这条语句）⇒ ⇒ ⇒ ★ **⇒ 归到并行会话的账上，不进本仓慢查询清单。**

## §3 量具事故：「零命中」的两版

★★ **本节记录本轮最贵的一次误判**：★ **第一版采样器跑了 1,300 次、零命中，差点被当成「重大发现」写进结论** ⇒ ⇒ **真因是量具坏了**。

**第一版采样器**（06:11:25 → 06:22:19，1,300 次采样）返回 **0 命中**。
按理该语句在窗口内跑了两次（06:12:08、06:22:17）⇒ ⇒ 「零命中」本该是**重大发现**。

★★★ **⇒ 但第一反应必须是「量具坏没坏」，而不是「被测对象在不在」⇒ 我去查量具 ⇒ 真因如下：**

```sql
-- 第一版（错的）
LEFT JOIN LATERAL (
  SELECT … FROM pg_stat_activity
  WHERE … AND query ILIKE '%sync_session_project_attr%' AND pid <> pg_backend_pid()
  ORDER BY query_start LIMIT 1
) a ON true
```

⇒ ★★★★ **这个 LATERAL 子查询里**没有任何对外层 `s` 的引用** ⇒ ⇒ 它**不是相关子查询** ⇒ ⇒
⇒ ⇒ **PostgreSQL 把它提升为一次性 InitPlan（只求值一次）** ⇒ ⇒ ⇒
⇒ ⇒ ★★★ **★ 我在 06:11:25 启动时 worker 恰好不在跑 ⇒ 那一次求值返回空 ⇒ 之后 1,300 行的 `a.*` 全部是 NULL。**
⇒ ⇒ ⇒ ★★ **⇒ 「零命中」是量具坏了，不是被测对象没出现。**

★★ **两条修法与自检**：
1. ★ **在 LATERAL 里加 `AND s.i > 0`** ⇒ 强制相关化 ⇒ **逐行求值**。
2. ★ **阳性对照**：在本地库用**必然能命中自己**的模式（`%pg_stat_activity%`）跑 5 次 ⇒ 必须每行都命中 ⇒ **量具确认有牙**（已实测通过）。

★★★ **⇒ 这一条与仓内既有教训同族**（`LESSONS.md` 的「『门绿』≠『量具有牙』」「先读量具再读产物」）
⇒ ⇒ ★★ **⇒ 补一条本轮新增的形态**：★ **「采样器零命中」是量具缺陷的高发形态 —— 因为采样器天然会「安静地」返回空集，而空集既可能是「没发生」也可能是「我看错了」。**

### §3.1 第二版采样器：**量具确认有牙**，却仍然 0 命中 ⇒ ★★ 于是「零命中」升级为**真实观测**

> ⚠ **【订正横幅一 · 2026-10-21 R72e 本轮内】本节的标题结论已被 §3.3 推翻，原文保留。**
> ★★★ **「1,450 次采样」这个说法本身不成立** —— 实测 `pg_stat_activity` 在 PG 17.10 里**按事务缓存统计快照**
> ⇒ ⇒ **1,450 行读的是同一个快照 ⇒ 实际只观测了 1 次**。
> ⇒ ⇒ ★★ **⇒ 下表「阳性对照 1,450/1,450 全中」这条**仍然成立，但证明力远弱于原判**：
> 对照匹配的是**采样器自己的那一行**，**快照新不新鲜都会 100% 命中** ⇒ ⇒ **它只证明「LATERAL 确实逐行求值了」（v1 的缺陷已修），不证明「看到了多个时刻」。**
> ⇒ ⇒ ★★★ **⇒ 「零命中升级为真实观测」也要降级** —— 那只是**一个瞬间**的事实，不是 1,450 个样本的一致事实。
> ⇒ ⇒ ★★★★ **⇒ 详见 §3.3。**

第二版（06:26 → 06:38，1,450 次采样 / 12 分钟）做了**两件事**：

| 自检项 | 结果 |
|---|---|
| **阳性对照**（`%pg_stat_activity%`，故意不排除自己 ⇒ 必须每行命中） | ★ **1,450 / 1,450 全中** ⇒ **量具逐行求值，确认有牙** |
| worker 命中（`%sync_session_project_attr%`） | ★ **0** |

⇒ ⇒ ★★★★ **⇒ 这次「零命中」不能再推给量具了** ⇒ ⇒ ★★★ **⇒ 它成了真实观测**：
★ **该语句确实在跑**（PG 日志：06:22:19 / **06:32:39** / **06:38:16** ⇒ 后两次正落在采样窗口内），
★ ★★ **⇒ 但它在 `pg_stat_activity` 里看不见。**

★ **⇒ ★★ 于是「看不见」本身变成了一条需要解释的观测** ⇒ ⇒ ★★★ **⇒ 我补了第三版采样器：不带任何 SQL 模式过滤，
只抓 `state <> 'idle'` 的全部后台**（把「它长什么样」原样录下来，不预设任何假设）⇒ 结果见 §3.2。

### §3.2 ★★ 第三版采样器：无过滤抓全部活动后台

⇒ 目的：★ **不再问「我以为它长什么样」，而是「把当时所有在跑的东西录下来」** ⇒ ★ 这与 §3.1 的教训是同一件事的另一面：
★ **⇒ 采样时不要用「我以为的标识」去过滤被测对象，要先无过滤地录下来，再从数据里认出来。**

★★ **录到的两件事（完整读数与归属判定见 §2.5）**：

| 观测 | 一句话 | 归属 |
|---|---|---|
| **A** | ★ `REFRESH MATERIALIZED VIEW CONCURRENTLY session_task_stats`（来自 `172.16.2.209`，`wait_event_type=IO` / `DataFileRead`） | ★★ **产品代码路径**（网关的物化视图刷新 worker） |
| **B** | ★ `SELECT 'v2.session_turns' AS fam, count(*), …`（`application_name='psql'`，`.209` 与 252 本机各一个） | ★★★ **不是本仓代码**（并行会话的只读诊断 SQL） |

⇒ ⇒ ★★★★ **⇒ 但这两条读数也要按 §3.3 打折**：★ **第三版同样是一个事务里读的** ⇒ ⇒ ★★ **⇒ 它等于「那一个瞬间，252 上有这两个后台」**，不是「12 分钟里一直只有这两个」。

⇒ ⇒ ★★★★ **⇒ 三版采样器的合并结论**：
1. ★★★ **worker 那条语句在 `pg_stat_activity` 里始终没被抓住** ⇒ ⇒ ★★ **⇒ 「在等谁」这最后一问，本轮没能用 wait event 坐实** ⇒ 已列进 §4 的未封口项，**不假装已归因**。
2. ★★★ **但「抓不到」这件事本身被平凡地解释了** ⇒ ⇒ ★★ **⇒ 见 §3.3 的占空比估算** ⇒ ⇒ ★★★ **⇒ 不需要任何「worker 在 `pg_stat_activity` 里不可见」的假说。**
3. ★★ **采样器本身不是坏的，是「采样」这件事被事务快照压扁了** ⇒ ⇒ ★★★ **⇒ 修法是「每次采样一个独立事务」（§3.4）。**

### §3.3 ★★★★ 量具体检 2：`pg_stat_activity` 在一个事务里是**快照**的 ⇒ ⇒ ★★ **前两版采样器全都退化成「一次观测 × N 行」**

★★★ **本节是本轮第二个量具事故，而且是更隐蔽的一个** ⇒ ⇒ ★★ **它推翻的是 §3.1 的核心主张。**

★★ **疑点**：第二版采样器把 1,450 次采样放在**同一个事务**里（`generate_series` + `pg_sleep`）⇒ ⇒ ★ **那么 1,450 行读到的 `pg_stat_activity` 会不会是同一个快照？**

★★ **决定性探针（纯只读）**：后台起一个 `SELECT pg_sleep(4)` 的 psql —— ★ **它 4 秒后 backend 就彻底退出、会从 `pg_stat_activity` 里消失** ——
然后在**一个事务**里隔 9 秒读三次：

```sql
BEGIN;
SELECT 'read1', clock_timestamp(),
       (SELECT count(*) FROM pg_stat_activity
         WHERE query LIKE '%pg_sleep(4)%' AND pid <> pg_backend_pid());
SELECT pg_sleep(9);
SELECT 'read2', clock_timestamp(),
       (SELECT count(*) FROM pg_stat_activity
         WHERE query LIKE '%pg_sleep(4)%' AND pid <> pg_backend_pid());
SELECT pg_stat_clear_snapshot();
SELECT 'read3', clock_timestamp(),
       (SELECT count(*) FROM pg_stat_activity
         WHERE query LIKE '%pg_sleep(4)%' AND pid <> pg_backend_pid());
COMMIT;
```

**实测（252，PG 17.10）**：

```
read1|2026-10-21 07:28:56.250075+08|1
read2|2026-10-21 07:29:05.264281+08|1     ← 9 秒后仍可见，但那个 backend 早就退出了 ⇒ 陈旧
read3|2026-10-21 07:29:05.288226+08|0     ← 调 pg_stat_clear_snapshot() 后立刻读到真相
```

⇒ ⇒ ★★★★ **⇒ 结论：`pg_stat_activity` 在 PG 17.10 里被**事务级统计快照**缓存；同一事务内反复读不会刷新 ⇒ ⇒ ★★★ **⇒ 「N 行采样」在单事务里等于「一次观测 × N」。**

⇒ ⇒ ★★★ **⇒ 回头重估三版采样器**：

| 版本 | 写法 | 它实际上观测了几次？ | 缺陷 |
|---|---|---|---|
| **v1** | LATERAL **不相关** ⇒ 被提升为一次性 InitPlan | **1 次** | ★ **只求值一次**（§3 已记） |
| **v2** | LATERAL 相关化（`AND s.i > 0`）⇒ 逐行求值 | ★★ **仍然只有 1 次** | ★★ **1,450 行读的是同一个事务快照** |
| **v3** | 无过滤 + `state <> 'idle'` | ★★ **仍然只有 1 次** | ★★ 同上 |

⇒ ⇒ ★★★★ **⇒ ⇒ 三版「采样」在本轮**全部是单次快照** ⇒ ⇒ ★★★ **⇒ §3.1 的「1,450 次采样」这个说法本身就不成立。**

⇒ ⇒ ★★★★ **⇒ §3.1 的结论必须降级**：
- ★★★ **「阳性对照 1,450/1,450 全中 ⇒ 量具有牙」这条仍然成立，但**它证明的东西比原来以为的少得多** ——
  对照匹配的是**采样器自己的那一行**，**无论快照新不新鲜都会 100% 命中** ⇒ ⇒ ★★ **⇒ 它只证明「LATERAL 确实逐行求值了」（v1 的缺陷已修），不证明「看到了多个时刻」。**
- ★★★ **「worker 0 命中 ⇒ 升级为真实观测」也要降级** ⇒ ⇒ ★★ **⇒ 那只是**一个瞬间**的事实，不是 1,450 个样本的一致事实。**

⇒ ⇒ ★★★★ **⇒ 但「worker 一直抓不到」这个谜团被平凡地解开了** ⇒ ⇒ ★★★ **⇒ 不需要任何「worker 在 `pg_stat_activity` 里不可见」的假说**：
★ **该语句每 ~10 分钟跑一次、单次持续 5~12 秒**（§1.2 的 13 个日志样本）⇒ ⇒ ★★ **⇒ 占空比 ≈ 1%** ⇒ ⇒ ★★★ **⇒ 单次快照撞上它的概率约 1~2%** ⇒ ⇒ ★★★ **⇒ 三版采样器全部落空（约 5%）完全在正常范围内。**

⇒ ⇒ ★★★★ **⇒ 顺带一条比 §3 那条更普适的教训**：★★ **「把 N 次采样放进一个事务」是**最隐蔽的量具缺陷** ——
因为它**不会报错、不会返回空、量具自检照样全绿**（对照必然命中）⇒ ⇒ ★ **⇒ 它只会让结论悄悄失去时间维度。**
★ **⇒ 通用式：「采样次数」这个数，在跨事务时才是真的；单事务里的 N 行只是 N 份同一张快照的副本。**

### §3.4 ★★★★ 修好的采样器（v5）：**worker 被抓住了** ⇒ ⇒ ★★ **「在等谁」的答案是「等磁盘」，不是「等锁」**

★★ **修法只有一个要点：让每次采样成为一个独立事务。** ⇒ ★ **单 psql 会话 + autocommit ⇒ 每条语句一个事务 ⇒ 快照必然新鲜**（§3.3 已证明事务内是快照）。

```sql
-- v5（要点：单会话 / autocommit / 排除自己 / 给每行打 is_worker 标记，避免靠文本截断判断）
SELECT clock_timestamp() AS ts,
       (SELECT count(*) FROM pg_stat_activity WHERE datname='llm_gateway' AND state<>'idle') AS nonidle_ctrl,
       w.pid, w.client_addr, w.application_name, w.state, w.wait_event_type, w.wait_event,
       round(EXTRACT(epoch FROM (clock_timestamp()-w.query_start))::numeric,3) AS q_age,
       pg_blocking_pids(w.pid) AS blockers,
       (w.query ILIKE '%sync_session_project_attr%') AS is_worker,
       left(regexp_replace(w.query, chr(10), chr(32), 'g'), 150) AS qprefix
FROM (SELECT 1) z
LEFT JOIN LATERAL (
  SELECT pid, client_addr, application_name, state, wait_event_type, wait_event, query_start, query
  FROM pg_stat_activity
  WHERE datname='llm_gateway' AND state<>'idle' AND pid <> pg_backend_pid()
  ORDER BY query_start LIMIT 12) w ON true;
SELECT pg_sleep(0.3);
```

★★★ **抓到了**（2026-10-21 07:32:07 → 07:32:35，pid **1785027**，`client_addr = 172.16.2.209`，`application_name` 空）：

| 读数 | 值 |
|---|---|
| 被连续观测的时长 | **27.7 秒**（`q_age` 0.040 → 27.667） |
| `wait_event_type` / `wait_event` | ★ **82/88 = `IO` / `DataFileRead`**；1 次 `LWLock` / `BufferMapping`；5 次无等待（跑在 CPU 上） |
| **`pg_blocking_pids(pid)`** | ★★★ **`{}` —— 88 个样本全程为空 ⇒ 没有任何锁阻塞** |
| `state` | `active`（88/88） |

⇒ ⇒ ★★★★ **⇒ 「那 ~4.1 秒在等谁」的答案：在等磁盘，不是在等锁。** ⇒ ⇒ ★★★ **⇒ 这直接推翻了 §2 的「`project_dim` 热行 upsert 等锁」假设。**

★★ **交叉印证（两个独立量具、同一个 pid）**：PG 日志里
`2026-10-21 07:32:35.360 CST [1785027] LOG:  duration: 27899.958 ms`
⇒ ★★ **日志说 27.9 秒、采样器说 27.7 秒 ⇒ 同一个 backend、同一段执行、两个独立量具对上。**

★★★ **量具侧证（`pg_stat_statements`，893 次调用累计）**：

| 指标 | 每次调用 | 含义 |
|---|---|---|
| `shared_blks_hit` | ≈ **129,255** | 绝大多数是内存命中 |
| **`shared_blks_read`** | ≈ **285**（254,248 / 893） | ★★ **每次约 285 次物理块读 ≈ 2.2 MB 磁盘读** ⇒ 与 `DataFileRead` 吻合 |
| `shared_blks_dirtied` | ≈ 46 | 写放大很小 |
| `shared_blks_written` | ≈ 1.6 | |
| **`temp_blks_read` / `temp_blks_written`** | **0 / 0** | ★★ **没有临时文件 ⇒ 没有排序/哈希溢出** |

⇒ ⇒ ★★★★ **⇒ 与 §1.1 对上了**：只读扫描那一段是 **69,654 次全命中、零物理读** ⇒ ⇒ ★★★ **⇒ 磁盘 IO **不在** CTE 扫描里** ⇒ ⇒ ★★ **⇒ 它落在 §1.1 没有覆盖的那一段：每调用约 24 次 `sync_session_project_attr(...)`（2a UPDATE / 2b upsert / 2c insert）里。**

★ **量具坑留证（同一个「指纹」家族的第四例）**：v5 的输出里出现过一行 `is_worker=t`、但 `application_name=psql` 且 `client_addr` 为空 ⇒ ⇒ ★★ **那是上一版采样器自己** ——
我 `task_stop` 掉的是**本地 ssh 任务**，**远端的 `bash -s` 循环成了孤儿继续跑**（`ps` 里 cmdline 是 `bash -s`，按脚本名 grep 不到）⇒ ⇒ ★★★ **⇒ 采样器的指纹又一次出现在数据里** ⇒ ★ **⇒ 清理时必须按 PID 杀，并复核 `ps` 上「无残留」。**

---

## §4 结论与待属主拍板

### §4.1 本轮**没有**改任何产品代码 —— 为什么（★ 且 §2 的两条候选修法**实测打偏了**）

★ **先认账**：§2 把两条候选修法排在前面，理由是「那 ~4.1 秒是**等锁**」⇒ ⇒ ★★★★ **§3.4 实测证明那是磁盘 IO、根本不存在锁竞争** ⇒ ⇒ ★★★ **⇒ 两条修法的收益估算都建立在错误前提上。**

| 候选 | §2 原本估的收益 | ★ **§3.4 实测后的判定** |
|---|---|---|
| **2b 的 `ON CONFLICT DO UPDATE` → `DO NOTHING`** | ★★★ 「拆掉 17 个热行的**锁竞争**；预计显著压低 ~4.1s 的可变部分」 | ★★★ **打偏**：`blockers` 全程为空 ⇒ **没有锁竞争可拆** ⇒ ⇒ ★★ **⇒ 真实收益只剩「少几万次 UPDATE/WAL」与「少掉一部分物理读」**（2b 确实要读 `project_dim` 的页，但**读页 ≠ 等锁**）⇒ ⇒ ★ **⇒ 仍是属主决策项（语义不变性依旧存疑），但不要按「能省 4.1 秒」立项** |
| **缩小 48h 窗口** | ★★★ 「扫描面 14,081 → 按比例减少 ⇒ 直接压掉 1.44s 的固定成本」 | ★★ **方向对、量级小**：固定成本确实只有 1.44s，且**零物理读** ⇒ ⇒ ★★ **⇒ 对实测的 5~44 秒耗时，最多只能省 ~3%~30%，压不动大头** ⇒ ⇒ ★ **⇒ 仍是属主决策项，但排在「先找到 IO 来源」之后** |

★ ⇒ **⇒ ⇒ 两条仍都要属主拍板 ⇒ ⇒ ⇒ 本轮只取证、不落地。**

★★★ **⇒ 那么下一步该做什么？** ⇒ ⇒ ★★★ **⇒ 不是改代码，是**先把那 285 次物理读读的是哪些表**量清楚**：
★ **按 3 秒快照 `pg_statio_user_tables` 的 `heap_blks_read + idx_blks_read`，并同步标注 worker 是否可见 ⇒ ⇒ ★★ **⇒ 差分即可把物理读归因到表**（本轮已起测量，见 §4.3）。

★ ⇒ ★★ **⇒ 归属落到「表」这一层，才谈得上「改」**：★★ **若物理读集中在 `session_summaries` 的索引 ⇒ 补索引；若集中在 `project_dim` ⇒ 换 `DO NOTHING`（★ **这才是 §2 那条修法唯一成立的场景**）；若集中在 `session_project_attribution` ⇒ 又是另一回事。**

### §4.2 待拍板清单（本轮新增 3 条）

| # | 事项 | 本轮给的新信息 |
|---|---|---|
| **6** | ★★★ **`project_dim` 的 2b upsert 是否改成 `DO NOTHING`** | ★ 净效果只有「刷新无读方的 `updated_at`」；★ 表只有 17 行却 69,751 次 UPDATE；★ **⚠ §4.1 已订正：它**拆不掉锁竞争**（实测 `blockers` 全空）⇒ 真实收益只剩「少几万次 UPDATE/WAL」**；★ **⇒ 但若 §4.3 把物理读归因到 `project_dim`，它就重新变成主修法** |
| **7** | ★★ **`pg_stat_statements` 的 `track` 要不要设成 `all`** | ★ 现在是 `top` ⇒ **函数体内的语句完全不可见**；★ 代价是有开销；★ **另：`max=5000` 已用 4,878 ⇒ 接近淘汰上限** |
| **8** | ★★★ **252 共享库的 IO 争用要不要单独立项** | ★★ 本轮实测：worker 的可变成本**全部落在磁盘 IO**（每次 ~285 次物理读 ≈ 2.2 MB）；★★ **同一时刻在 252 上吃 IO 的还有：4 个 matview 的 `REFRESH CONCURRENTLY`（§143.20：占本库慢查询 DB 时间 25.3%）、并行会话的只读全表审计、以及别的产品**；★ ★★ **⇒ 「这条语句变慢 3.86×」很可能是共享 IO 争用，而不是它自己的代码退化** ⇒ ★★ **⇒ 这条要不要单独立项（而不是当成 worker 的问题单独修）** |

★ **同时修正既有第 2 项**（「给 211 启用慢查询量具」）：★ **光装扩展 + preload 不够，还要 `track=all`，否则函数体内仍是黑洞。**
★ **另修正既有第 1 项**（auto-route 成本仪表盘）：★ **本轮又抓到一条同类语句的实测形态 —— 它的可变成本是 IO，且 `pg_stat_statements` 里 `track=top` 让函数体完全隐形** ⇒ ★ **⇒ 任何针对「慢查询」的优化，都必须先确认量具能看见函数体。**

### §4.3 ★★★★ 归因落地：物理读来自 **`session_dim`** 的 pkey 探测

★★ **手段**：按 3 秒快照 `pg_statio_user_tables` 的 `heap_blks_read + idx_blks_read`（累计值），并同步标注 worker 在该时刻是否可见 ⇒ ⇒ ★★ **⇒ 差分即可把物理读归因到表。**

⚠ **量具坑（本轮第一版白跑了一轮的原因）**：★ `pg_statio_user_tables.idx_blks_read` 对「**没有索引的表**」是 **NULL**
⇒ ★★ **⇒ 而 PostgreSQL 的 `ORDER BY x DESC` 把 NULL 排在最前** ⇒ ⇒ ★★★ **⇒ 不加 `COALESCE(idx_blks_read,0)` 的话，「top-N 物理读表」会全是 NULL 行。**

★★ **两次运行（一次慢、一次快）** 的对照 —— ★★ **同一份代码、同一套索引，两次的物理读差 80 倍**：

| 运行 | PG 日志耗时 | 物理读增量合计 | 其中 `session_dim` |
|---|---|---|---|
| **慢**（07:42:05 → 07:42:29） | **19,960 ms** | **+8,745** | **+6,323** |
| **快**（07:52:06 → 07:52:13） | **3,996 ms** | **+102** | **+0** |

⇒ ⇒ ★★★★ **⇒ 两个独立样本上「慢 ⇔ 物理读多」互相印证**：
★★ **快的那次（4.0 秒）几乎零物理读** ⇒ ⇒ ★★★ **⇒ 再次印证「固定成本（扫描，内存命中）＋ 可变成本（物理读）」这个分解**
★★ **⇒ 也印证「变量部分是缓存命中率，不是代码退化」**：★ **两次运行的 SQL 完全一样，耗时差 5 倍 ⇒ 差在缓存，不在代码。**

★★ **慢的那次（07:42）的逐表增量**：

| 表 | Δ（heap + index） | 说明 |
|---|---|---|
| **`session_dim`** | **+6,323** | ★★★★ **CTE 里 `JOIN session_dim sd ON sd.gw_session_id = ss.session_key` 的 pkey 探测** |
| `session_summaries` | +2,280 | 2a 段的 UPDATE 与候选集扫描 |
| `session_project_attribution` | +132 | 2c 段的 INSERT |
| `project_dim` | +4 | 2b 段的 upsert（17 行热表，**几乎不产生物理读** ⇒ ⇒ **进一步否证「等锁」假设**） |
| `session_turns_hot` | +6 | 与本语句无关的其它写入 |

★★★ **⇒ 机制闭合**：
1. ★★ **候选集 14,081 行**（走 `idx_session_summaries_project_null_recent`）⇒ **对每行做一次 `session_dim` pkey 探测**（实测 **4 buffers/次**：索引页 + heap 页）。
2. ★★ **`session_dim` 堆 203 MB + pkey 索引 65 MB（合计 374 MB、约 87 万行）**，而 `shared_buffers = 3584 MB` 且这是**共享库** ⇒ ⇒ ★★★ **⇒ 它的页被别的负载逐步逐出 ⇒ 每次 worker 运行都要把其中一部分重新读回磁盘。**
3. ★★★ **⇒ 而 §1.1 已经证明：这 14,081 次探测里有 14,063 次（99.87%）结构上永远解析不出来**（`project_id` 与 `application_code` 都空 ⇒ `gw_resolve_project_ref` 恒返回 NULL）
   ⇒ ⇒ ★★★ **⇒ 单次运行 6,323 次物理读里，绝大部分花在「注定读不出东西」的探测上。**

★ ⇒ ★★★ **⇒ 这也解释了「为什么它在变慢」**：★ **早期 13 个样本均值 5,704 ms**（当时 `session_dim` 的页还在缓存里 —— §1.1 的 EXPLAIN 实测 `Buffers: shared hit=69654`、**零物理读**）
★ **晚期 8 个样本均值 22,035 ms**（缓存被挤掉 ⇒ 每次多读几千块）⇒ ⇒ ★★ **⇒ 变量部分是「缓存命中率」，不是「代码退化」。**

⇒ ⇒ ★★★★ **⇒ 下一步该做什么（需属主拍板，本轮不落地）**：

| 方向 | 收益量级 | 代价 |
|---|---|---|
| **A. 给 `session_dim` 加覆盖索引**（`(gw_session_id) INCLUDE (tenant_id, project_id, application_code)`）⇒ 探测变 index-only ⇒ 每探测从 ~4 buffers 降到 ~1 | ★★★ 直接砍掉大部分物理读 | ★★ 多一个 ~65 MB 索引（写放大） |
| **B. 缩小 48h 窗口** | ★★ 线性减少候选集 ⇒ 线性减少探测与物理读 | ★★★ **语义变更**（>48h 遗留行永远回填不到） |
| **C. 2b 改 `DO NOTHING`** | ★ **对本次慢几乎没有帮助**（没有锁竞争；`project_dim` 只 +4 物理读） | ★★ 语义变更（`enabled` 的自动复活语义） |
| **D. 把「结构上不可解析」的判定前移**（在 `session_summaries` 侧就判掉，不去探测 `session_dim`） | ★★★★ **最彻底**（把那 99.87% 的探测整个消掉） | ★★★ 需改表结构或加冗余列 ⇒ **可行性本轮未验证** |

★ ⇒ ★★ **⇒ 建议的拍板顺序：A → B → D；★ C 可以基本出局。**

---

## §5 复现命令（全部只读）

```bash
# 252：只读 EXPLAIN ANALYZE —— 扫描成本（末尾换成 count(*) ⇒ 纯 SELECT，不写库）
ssh 252 'docker exec pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" -c "
SET statement_timeout='"'"'90s'"'"';
EXPLAIN (ANALYZE, BUFFERS, TIMING)
WITH targets AS (
    SELECT ss.session_key, ss.tenant_id, sd.project_id, sd.application_code
    FROM session_summaries ss
    JOIN session_dim sd ON sd.gw_session_id = ss.session_key
     AND sd.tenant_id IS NOT DISTINCT FROM ss.tenant_id
    WHERE ss.gw_project_id IS NULL
      AND ss.created_at >= (now() - interval '"'"'48 hours'"'"')
      AND public.gw_resolve_project_ref(sd.project_id, sd.application_code) IS NOT NULL
    ORDER BY ss.session_key LIMIT 2000)
SELECT count(*) FROM targets t;"'

# 252：PG 日志里的真实耗时分布（13 个样本）
ssh 252 'bash -s' <<'EOF'
L=/var/lib/containers/storage/overlay-containers/dc5e54272d7a35612cf32a1dd993d0e0610c36a9bf877c305bca4e8607fe2f5a/userdata/ctr.log
tail -c 400000000 "$L" | awk '
/LOG:/ { if (durline != "") { if (pend ~ /sync_session_project_attr/) print durline; durline=""; pend="" }
         if ($0 ~ /duration:/) { durline=$0; pend=$0 "\n" } else { pend="" } ; next }
{ if (durline != "") pend = pend $0 "\n" }' \
| sed 's/.*duration: \([0-9.]*\) ms.*/\1/'
EOF

# 252：project_dim 的热度（17 行 / 69,751 次 UPDATE）
ssh 252 'docker exec pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" -c "
SELECT relname, n_tup_ins, n_tup_upd, n_tup_hot_upd, n_live_tup FROM pg_stat_user_tables
WHERE relname='"'"'project_dim'"'"';"'

# 252：★ 量具体检 —— pg_stat_activity 在一个事务里是快照吗？（决定性探针，纯只读）
ssh 252 'bash -s' <<'EOF'
docker exec pg-252-pg17 psql -U postgres -d llm_gateway -X -c "SELECT pg_sleep(4)" >/dev/null 2>&1 &
SLEEPER=$!
sleep 1
docker exec -i pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" <<'SQL'
BEGIN;
SELECT 'read1' AS phase, clock_timestamp(),
       (SELECT count(*) FROM pg_stat_activity WHERE query LIKE '%pg_sleep(4)%' AND pid <> pg_backend_pid());
SELECT pg_sleep(9);
SELECT 'read2' AS phase, clock_timestamp(),
       (SELECT count(*) FROM pg_stat_activity WHERE query LIKE '%pg_sleep(4)%' AND pid <> pg_backend_pid());
SELECT pg_stat_clear_snapshot();
SELECT 'read3' AS phase, clock_timestamp(),
       (SELECT count(*) FROM pg_stat_activity WHERE query LIKE '%pg_sleep(4)%' AND pid <> pg_backend_pid());
COMMIT;
SQL
wait $SLEEPER
EOF
# ⇒ read1=1 / read2=1（陈旧）/ read3=0（清快照后）⇒ 事务内是快照

# 252：★ 修正后的采样器 v5（每次采样 = 独立事务 ⇒ 快照新鲜）
ssh 252 'bash -s' <<'EOF'
{
for i in $(seq 1 1500); do
cat <<'SQL'
SELECT clock_timestamp() AS ts,
       (SELECT count(*) FROM pg_stat_activity WHERE datname='llm_gateway' AND state<>'idle') AS nonidle_ctrl,
       w.pid, w.client_addr, w.application_name, w.state, w.wait_event_type, w.wait_event,
       round(EXTRACT(epoch FROM (clock_timestamp()-w.query_start))::numeric,3) AS q_age,
       pg_blocking_pids(w.pid) AS blockers,
       (w.query ILIKE '%sync_session_project_attr%') AS is_worker,
       left(regexp_replace(w.query, chr(10), chr(32), 'g'), 150) AS qprefix
FROM (SELECT 1) z
LEFT JOIN LATERAL (
  SELECT pid, client_addr, application_name, state, wait_event_type, wait_event, query_start, query
  FROM pg_stat_activity
  WHERE datname='llm_gateway' AND state<>'idle' AND pid <> pg_backend_pid()
  ORDER BY query_start LIMIT 12) w ON true;
SELECT pg_sleep(0.3);
SQL
done
} | docker exec -i pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" -f -
EOF

# 252：★ 该语句的 IO 计数（物理读 vs 内存命中；temp=0 ⇒ 无排序溢出）
ssh 252 'docker exec pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" -c "
SELECT calls, round(mean_exec_time::numeric,1) AS mean_ms, rows,
       shared_blks_hit, shared_blks_read, shared_blks_dirtied, shared_blks_written,
       temp_blks_read, temp_blks_written
FROM pg_stat_statements WHERE query ILIKE '"'"'%sync_session_project_attr%'"'"'
  AND query NOT LIKE '"'"'EXPLAIN%'"'"';"'
# ⇒ 893 次 / 均值 5599.7ms / shared_blks_read=254248（⇒ 285 次·调用 ≈ 2.2MB）/ temp=0

# 252：★ 物理读归因到表 —— pg_statio_user_tables 快照差分（3 秒一档）
#   ⚠ idx_blks_read 对「无索引的表」是 NULL ⇒ 必须 COALESCE，否则 ORDER BY DESC 会把 NULL 排到最前
ssh 252 'bash -s' <<'EOF'
{
for i in $(seq 1 280); do
cat <<'SQL'
SELECT clock_timestamp() AS ts,
       (SELECT count(*) FROM pg_stat_activity
         WHERE datname='llm_gateway' AND state<>'idle'
           AND query ILIKE '%sync_session_project_attr%' AND pid <> pg_backend_pid()) AS worker_visible,
       (SELECT string_agg(relname||'='||tot, ' ' ORDER BY relname)
          FROM (SELECT relname, heap_blks_read + COALESCE(idx_blks_read,0) AS tot
                  FROM pg_statio_user_tables
                 WHERE relname IN ('session_summaries','session_dim','project_dim',
                                   'session_project_attribution')) t) AS cand,
       (SELECT string_agg(relname||'='||tot, ' ')
          FROM (SELECT relname, heap_blks_read + COALESCE(idx_blks_read,0) AS tot
                  FROM pg_statio_user_tables
                 ORDER BY heap_blks_read + COALESCE(idx_blks_read,0) DESC LIMIT 10) t) AS top10;
SELECT pg_sleep(3);
SQL
done
} | docker exec -i pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" -f -
EOF

# 252：量具盲区（函数体内的语句根本没被记）
ssh 252 'docker exec pg-252-pg17 psql -U postgres -d llm_gateway -X -tAF"|" -c "
SHOW pg_stat_statements.track;
SELECT count(*) FROM pg_stat_statements WHERE query ILIKE '"'"'%INSERT INTO project_dim%'"'"';"'
```
