# R175 · routing_analytics_7d REFRESH 降本轮（逐周期分解 + 分族会话画像 + JIT 轴）

> 会话：ZCode（checkout `llm-gateway-go-4`，分支 `main`）。
>承接 R166（`docs/audit/2026-10-27-R166-routing-analytics-7d-REFRESH降本-父表腿日界下推.md`）
> 的遗留：「routing_analytics_7d 的 REFRESH 降本」+ 拍板材料㉙(d)。
> **对生产的写入 = 0**（252 全程只读：pgss / pg_get_viewdef / EXPLAIN (ANALYZE, BUFFERS)
> （只读 SELECT）/ 目录表查询 / pg_stat_activity 采样 / 154 与 245 的 journalctl 读取）。
> 落地面 = 仓库内 `bg/materialized_view_refresher.go` + 分族门（**未部署**，上线由部署列车执行）。

---

## §1 结论（TL;DR）

1. **目标的 headline 读数已过时，且「比 matrix 大一倍」这个优先次序今天正好反过来。**
   目标给的 `pgss 5.2 天 4.97h / 293 calls / mean 61s` 是 **R147 在 10-24 02:51 的读数**，
落在 **907 应用（10-24 23:07）之前**。本轮实查：迁移 910 已于 **10-27 02:57:44+08**
   应用 252（`schema_migrations` 实查），pgss 的 REFRESH 条目 `stats_since=2026-10-18 22:31:26`
   且**从未被淘汰**（`pg_stat_statements_info.dealloc=68` 淘汰的是别的条目）⇒ 累计计数连续可信。
   ⇒ 切段后 **910 后段 = 100 次 / 4,301s / 均值 43.0s**（窗口 1.926 天，51.9 次/天）。
   **⇒ 按单周期实测，routing 族现在只有 0.33h/天，而 session 族 2.7h/天 ——
   matrix 一族就是 routing 一族的 5 倍，整个 session 族是 8 倍。**
   ⇒ **「先修 routing」这个前提在本轮被证伪**：routing 族已经是刷新链里最便宜的一族。
2. **单周期分解（2026-10-29 01:00 那一轮，pgss 逐周期差分 + pg_stat_activity 采样）：**

   | 视图 | 墙钟 | shared_blks_read | shared_blks_hit | temp_written |
   |---|---|---|---|---|
   | routing_analytics_7d | **21.6s** | **0** | 64,293 | **0** |
   | routing_audit_summary_7d | 0.7s | 2 | 48,786 | 0 |
   | session_client_task_matrix | **203.0s** | 12 | 332,469 | 0 |
   | session_task_stats | **122.6s** | 28 | 321,225 | 0 |
   | session_client_stats | 6.9s | 5 | 302,265 | 0 |

   ★ **routing_analytics_7d 的 REFRESH 从磁盘读 0 块**（全部命中缓存）⇒
   按 §143.29 的口径（「成本问 shared_blks_read」），**这一族在 I/O 轴上已经是零成本**。
   ★ 21.6s 的墙钟**不是锁等待**：采样抓到该 REFRESH 全程 `state=active`、
   `wait_event_type='-'`（在 CPU 上跑，不是等锁）。
3. **EXPLAIN 分解（910 现行形态，暖态，root `shared hit=47,203`）：**
   · turns 腿（`session_turns_2026_10_ts_idx` 范围扫）：**31,724（67%）** ——
     扫了 **75,809** 个索引元组，只留下 **28,436** 行，**47,373 行被过滤掉**。
   · ★ **这 47,346 行全部是探针流量**（`origin_stage` ∈ 8 个探针阶段；
     `origin_stage IS NULL` 的行数 = 0）⇒ **窗口内 62.5% 的行是被扔掉的**。
   · details 腿（8 天日界下推后的 pkey 范围扫 + Hash）：**12,521（26%）** ——
     哈希了 **80,705** 行，而只有约 28,436 行可能被匹配。
   · hot 腿（`session_turns_hot` 顺序扫 + `details_hot` 索引探针）：**2,958（6%）**。
   · 聚合层：Sort `quicksort` 3,550kB **内存内**、HashAgg `Batches: 1`、**零 temp**。
   ⇒ **成本结构（按 buffer）**：turns 腿 67% > details 腿 26% > hot 腿 6%。
   ⇒ **成本结构（按行）**：探针行占 62.5%，details 侧多哈希了 2.8 倍的行。
4. **漂移核查是刷新链的**同量级**隐藏成本**：每次刷新后 `checkConsistency`
   会把整个 7 天基准聚��**重算一遍** ⇒ 实测 **48,541 buffers/次**（与刷新自身的
   47,680 同量级）⇒ **整条链每周期约 96,221 buffers，其中核查占 50%**。
5. **㉙(d) 的实测结论：「抬 work_mem」这件事**早已落地且已足够**。
   ·部署态证据（pgss 逐周期差分，五个 REFRESH 条目 **Δtemp_blks_written 全为 0**）
     ⇒ §109「收益未验证」的诚实边界**本轮被实测关闭**。
   · 同缓存态 A/B（session 族）：8MB ⇒ 单次溢写 **151MB**、Sort变 external merge、
     HashAgg `Batches 4`；256MB ⇒ **temp = 0**、quicksort 56,034kB、`Batches 1`。
   · 同缓存态 A/B（routing 族）：**8MB / 64MB / 256MB 三档 buffer 足迹逐字相同**
     ⇒ **work_mem 对 routing 族不是杠杆**。
   ⇒ **⇒ 不再抬**（再抬没有实测支撑），**⇒ 改为按族分族接线 +逐族下界钉死**。
6. **★ 本轮最值钱的新发现（一个此前无人看的轴）：`jit`。**
   252 库级配置 `jit=on` / `jit_above_cost=100000`，而 routing_analytics_7d 的
   **定义查询计划代价是 193,601 ⇒ 超过阈值 1.93× ⇒ 每次刷新都付 91 个函数的 JIT 编译代价**。
   · 同 session交错 **8 轮**：`jit=on` 中位 **2,494ms** vs `jit=off` 中位 **1,257ms**
     （**8/8 轮更快**，1.98×；最坏一轮 5.2×）。
   · session 族 matrix 交错 **5 轮**：`jit=on` 最坏轮 **75,471ms** vs `jit=off` 最坏轮
     **6,401ms**；中位 7,185 vs 5,791 ⇒ **削的是长尾**。
   · ★ **两臂的 buffer 足迹逐项相等** ⇒ JIT 是**纯 CPU/编译时间**的轴，
     **不动「成本问 shared_blks_read」口径** ⇒ 关掉它是零 buffer 代价的墙钟优化。
   ⇒ 落地面：`refreshView` 的会话画像从「一个 statement_timeout」扩成
 **三轴分族（statement_timeout / work_mem / jit）**，两个族各传各的常量。
7. **纪律遵守**：252 全程只读（**无任何 DDL/DML**，本轮连一个索引都没建）；
   取号当场取（本轮**不需要新迁移号** —— 落地面全在 Go 侧，未触碰 §302 的迁移族）；
   动门前 grep 产物历史形态全集：routing_analytics_source 的历史形态（canonical宽投影 /
   907 LATERAL / 910 日界下推）逐条核对过（见 §4.3）。

---

## §2 读数（252 只读，pgss 1.11 / dbid=16384 / extversion 1.11）

pgss 自 **2026-10-18 22:03:37+08** reset 起累计（本轮读取时点 **2026-10-29 01:12:18**）：

| stmt | calls | total | mean | max | read | temp_written | rows |
|---|---|---|---|---|---|---|---|
| REFRESH CONCURRENTLY routing_analytics_7d | 551 | 7.95h | 51.9s | 179.5s | 7,802,916 | 60,399 | 2,137,495 |
| REFRESH CONCURRENTLY routing_audit_summary_7d | 538 | 0.77h | 5.1s | 86.2s | 34,495 | 0 | 998 |
| REFRESH CONCURRENTLY session_client_task_matrix | 293 | 9.19h | 113.0s | 598.8s | 2,841,655 | 103,070 | 395,579 |
| REFRESH CONCURRENTLY session_task_stats | 298 | 5.10h | 61.7s | 427.7s | 2,672,401 | 489,199 | 1,600 |
| REFRESH CONCURRENTLY session_client_stats | 287 | 3.13h | 39.3s | 176.0s | 1,874,958 | 144,739 | 214,627 |

**⇒ REFRESH 族合计 26.1h / 10.1 天 = 2.59h/天**，其中：

| 族 | 次数/天 | 单次墙钟 | **h/天** | buffers/天 |
|---|---|---|---|---|
| routing（2 视图） | 54.6 | 21.6s / 0.7s | **0.33** | 3.1M |
| session（3 视图） | 28.8 | 203.0 / 122.6 / 6.9s | **2.70** | 23.0M |

**⇒ ★优先次序反转：session 族是 routing 族的 8.2 倍；单看 matrix 一族就是 5.0 倍。**

### §2.1 切段（910 应用时刻 = 2026-10-27 02:57:44+08，`schema_migrations` 实查）

| 段 | 窗口 | calls | total | 均值 | read/次 |
|---|---|---|---|---|---|
| 907 形态段 | 10-24 23:07:44 → 10-27 02:57:48（2.16 天） | 111（R166 推算） | 3,548s | 31.8s | ~132MB |
| **910 形态段** | 10-27 02:57:48 → 10-29 01:12:18（**1.926** 天） | **100** | **4,301s** | **43.0s** | **149 块** |

★ **910 把「读磁盘」几乎打到零（132MB/次 → 149 块/次，−99.9%），
但墙钟均值从 31.8s 升到 43.0s。** 两者并不矛盾：910 把父表腿从
「27,486 次窄索引探针」换成「一次 Hash，代价是哈希 80,705 行 details」⇒
**buffer 触碰少了，行处理多了**，而墙钟看的是后者。
⇒ ★ **这条正是「成本问 shared_blks_read」口径的边界**：
   shared_blks_read 被优化到 ≈0 的同时，工作量其实**变大了**（行数 +52%），
   **只报 read 会把「变贵了」说成「变便宜了」**。

### §2.2 节奏与协调（154 + 245 的 journalctl 实读）

- 逐条重建 :00/:30 的领票时间线：**10-27 02:00 → 10-29 01:00 共 95 个窗口，105 次
  「materialized view refresh: redis leader token acquired」，**缺 0 个窗口**。
- 同一窗口另一个实例必记「materialized view refresh skipped, redis token held by
  another instance」⇒ **跨实例协调正常，无失败、无漏刷**。
- ⚠ **10.5% 的窗口出现了两个领票者**（105/95）⇒ 存在**未解释的放大**。
  pgss 侧对应：54.6 次/天 vs 名义 48 次/天（+13.7%）。**归因未查，登记为遗留项。**
- ★ 300+ 次 follower 记录里**没有一条失败/超时** ⇒
  **「刷新被自己的语句上限杀掉」在本窗口内零发生**（与 2026-10-22 的 180s 杀线不同）。

---

## §3 EXPLAIN 分解（910 现行形态，252 生产只读）

`EXPLAIN (ANALYZE, BUFFERS, TIMING OFF)`，work_mem=256MB，暖态：

```
GroupAggregate  (actual rows=2487)                 Buffers: shared hit=47,203
  Sort  (actual rows=28,967)  Sort Method: quicksort  Memory: 3550kB   ← 内存内，无外排
    Append (actual rows=28,967)                    Buffers: shared hit=47,203
      ├─ hot 腿 Nested Loop Left Join               Buffers: shared hit=2,958
      │    Seq Scan session_turns_hot   (565 rows)              hit=713
      │    Index Scan details_hot        (565 loops)            hit=2,245
      └─ 父表腿 Hash Left Join                      Buffers: shared hit=44,245
           Index Scan session_turns_2026_10_ts_idx  (75,809 → 28,436)  hit=31,724
             Rows Removed by Filter: 47,373
           Hash ← Index Scan session_turn_details_2026_10_pkey
             Index Cond: partition_date >= (now()-'8 days')::date
             (80,705 rows)                                      hit=12,521
```

**★ 行级归因（252 只读实测）**：

| 量 | 值 |
|---|---|
| 7 天窗内 `session_turns` 行数 | 75,770 |
| 其中 `origin_stage` ∈ 8 个探针阶段 | **47,346（62.5%）** |
| 其中 `origin_stage IS NULL` | **0** |
| 非探针行 | 28,424 |
| matview 行数 | 2,489（heap 1,632kB）⇒ **CONCURRENTLY 的 diff 微不足道** |

⇒ **turns 腿 67% 的 buffer 里有 62.5% 的行是白搬的** ——
这是本族**下一层**的结构（要治它需要新索引，见 §7）。

---

## §4 同缓存态 A/B（同一 session 内交错，成本轴 = shared_blks_read / hit / temp）

### §4.1 JIT 轴（routing 族，8 轮交错，两批次合并）

| 轮 | jit=on | jit=off | root buffers（两臂） |
|---|---|---|---|
| 1 | 11,337.5 | 1,015.4 | 47,415 / 47,302 |
| 2 | 2,390 | 812 | 47,203 / 47,203 |
| 3 | 2,835.9 | 1,302.0 | 47,203 / 47,203 |
| 4 | 2,598.4 | 2,182.8 | 47,203 / 47,203 |
| 5 | 2,717.7 | 1,298.0 | 47,203 / 47,203 |
| 6 | 1,927.3 | 723.2 | 47,301 / 47,301 |
| 7 | 2,376.0 | 1,967.1 | 47,302 / 47,302 |
| 8 | 2,275.2 | 1,216.2 | 47,203 / 47,203 |

⇒ **8/8 轮更快**；中位 **2,494ms → 1,257ms（1.98×）**；最坏一轮 **5.2×**。
⇒ **buffer 足迹两臂逐项相等** ⇒ 纯 CPU 轴。

### §4.2 JIT 轴（session 族 matrix，5 轮交错，两批次合并）

`jit=on`：75,471 / 35,158 / 7,185 / 6,330 / 5,324 ms（中位 7,185）
`jit=off`： 6,401 /  4,355 / 5,791 / 5,694 / 5,797 ms（中位 5,791）
⇒ **最坏轮 11.8×**；中位 1.24×。
⚠ **诚实边界**：同一臂两轮相差 14× ⇒ **宿主负载抖动大于臂间差异** ⇒
**不能承诺倍率**，只能承诺「削长尾」+「buffer 足迹不变」。

### §4.3 work_mem 轴（每族各测）

| 族 | work_mem | temp（块） | Sort | HashAgg | 墙钟 | root buffers |
|---|---|---|---|---|---|---|
| session | 8MB | 19,363 / 19,394（≈151MB） | external merge 13~15MB ×3 进程 | Batches 4 | 28,814 / 7,929ms | 318,5xx |
| session | 256MB | **0** | quicksort 56,034kB | Batches 1 | 5,560 / 4,957ms | 318,5xx |
| routing | 8MB | 0 | quicksort | Batches 1 | 2,328 / 1,738ms | 47,203 |
| routing | 64MB | 0 | quicksort | Batches 1 | 2,310 / 1,738ms | 47,203 |
| routing | 256MB | 0 | quicksort | Batches 1 | 2,390 / 2,275ms | 47,203 |

⇒ **session 族：256MB 让溢写归零**（已在部署态生效，逐周期差分 Δtemp=0）。
⇒ **routing 族：work_mem 不是杠杆**（三档逐字相同）。

### §4.4 一个被否掉的候选（记下来，别重做）

★ **routing 族的 details 日界从 8 天收到 7 天** ⇒ **不能做**：
`dd.partition_date = t.partition_date` 而 turns 腿按 `ts >= now()-7d` 过滤 ⇒
`partition_date` 的取值域是 **[date(now()-7d), date(now())]＝ 8 个日历日**
⇒8 天是**紧的下界**，收到 7 天会丢掉边界日那一批行。
（910 的注释里写的是「8 天 = 7 天窗 + 跨日边界余量」，本轮把它算成了**恰好**的紧界。）

### §4.5 一个测错了的实验（记下来，别重做）

★ 本轮第一版 A/B 用 `SELECT count(*) FROM (<source 内联>)` 当臂 ——
**丢了 matview 的 WHERE** ⇒ 规划器改选 details 的 **Seq Scan**
（`hit=1 read=141,905`）⇒ 两臂读数完全不可比。
⇒ ★ **通用式：把一个视图的「定义查询」搬到 A/B 里时，必须搬它的**整个** WHERE，
否则量的是另一个计划。**（与 §419「0 命中有两种读法」同族。）**

---

## §5 落地面（仓库内，未部署）

### §5.1 `bg/materialized_view_refresher.go` —— 会话画像三轴分族

`refreshView` 的签名由 `(…, statementTimeout)` 扩为 `(…, statementTimeout, workMem, jitMode)`：

```go
SET statement_timeout = '"+statementTimeout+"'   // 既有
SET work_mem          = '"+workMem+"'            // 新：按族
SET jit               = '"+jitMode+"'            // 新：按族
```
三者的 RESET 都在 `defer` + `context.WithoutCancel(ctx)` 上（错误路径也复位）。

新增/调整的常量：

| 常量 | 值 | 依据 |
|---|---|---|
| `mvRefreshWorkMem` | 256MB（不变） | routing 族实测 8/64/256MB 三档逐字相同 ⇒ 沿用全局值保连续性 |
| `sessionViewsWorkMem` | 256MB |8MB 下溢写 151MB ⇒ 256MB 让 temp=0、Batches 4→1 |
| `mvRefreshJIT` | **off** | 8/8 轮更快，中位 1.98×，buffer 足迹不变 |
| `sessionViewsJIT` | **off** | 最坏轮 75,471→6,401ms；中位 7,185→5,791ms；buffer 足迹不变 |

调用点：`refreshAll` 传 routing 族的两个常量；`refreshSessionViews` 传 session 族的两个常量。

### §5.2 门（`bg/materialized_view_refresher_workmem_test.go`，改写为 R75 分族同款）

- `TestRefreshView_UsesThePassedWorkMemNotAGlobalConstant` —— **参数必须是摆设的反向判据**
- `TestRefreshView_WorkMemResetIsDeferred` —— 钉**精确形态**（第三版；前两版是假绿）
- `TestRefreshCallSites_PassTheRightFamilyWorkMem` —— 两个族的**标识符**必须不同
  （不要求取值不同：实测两族都取 256MB）
- `TestSessionViewsWorkMem_CoversItsOwnMeasuredSpill` —— ≥ 136MB（**逐族**的下界）
- `TestMVRefreshWorkMem_NotBelowTheLibraryDefault` —— routing 族的下界（**弱**不等式，
  并在门里写明「routing 族压根没有溢写」，防后人把「routing 需要大 work_mem」当事实）
- `TestRefreshView_UsesThePassedJITModeNotAGlobalConstant` / `TestRefreshView_JITResetIsDeferred`
- `TestRefreshCallSites_PassTheRightFamilyJIT`
- `TestRoutingFamilyJIT_IsOff` / `TestSessionFamilyJIT_IsOff`（后者在门里写明
  「这是削尾决策，不是提速承诺」）
- `bg/materialized_view_refresher_session_timeout_test.go` 的调用点判据同步放宽
  （实参数增加 ⇒ 原「带右括号的整串」不再匹配），**并补一条反向判据**：
  session 调用点不得出现 `mvRefreshStatementTimeout`。

### §5.3 语义边界（必须随改动一起读）

★ `jit = off` **不是**严格零语义：PG 文档载明 JIT 编译的浮点运算可能与解释器**差最后一位**。
本仓 matview 里有 `percentile_cont`（double precision）⇒ 理论上 p50/p95/p99 的最后一位
在两种编译模式下可能不同。
⇒ 但：(a) 漂移核查比较的是**计数**不是分位数；(b) 固定成一种模式后值**稳定**
（而不是随编译与否抖动）⇒ 取舍是「用一个 ULP 级的理论差异换掉每次刷新 91 个函数的编译代价」。
⇒ **这条写进了常量注释和门里，不是藏在「零语义」四个字后面。**

---

## §6 诚实边界

1. **本轮没有改任何查询/索引** ⇒ routing 族 67% 的 turns 腿（62.5% 白搬的探针行）
   **原样保留**。治它需要新索引或写路径分离 ⇒ §7 登记。
2. **墙钟承诺的口径**：`jit=off` 的倍率**只在 routing 族可承诺**（8/8 轮）；
   session 族只能承诺「削长尾」。**同一臂两轮相差 14×** 这个事实照实记。
3. **pgss 的 `dealloc=68`**：本轮核实过 REFRESH 条目 `stats_since` 未被淘汰 ⇒
   其累计计数连续；但**别的**条目（低频语句）确实在被淘汰 ⇒ 用 pgss 做长窗口
   趋势时**必须**逐条查 `stats_since`，不能默认全体连续。
4. **`routing_analytics_7d` 的 21.6s 里，约 2.4s 是查询本身**（暖态 EXPLAIN）
   ⇒ 剩下约 19s 是**并发/环境**（同刻 fleet 负载）而不是查询形态。
   ★ 与 `routing_audit_summary_7d` 的对照可以钉死这一点：
   **同样 48,786 buffers、同样在 01:00 那一轮，它只花了 0.7s。**
   ⇒ ★ **同一批次、同样的 buffer 量、30 倍墙钟差 ⇒ 墙钟差异来自环境，不是来自 SQL。**
   （这也是 §143.28「墙钟只作时点」的直接实证。）
5. **未部署**：本轮改动全在 Go 侧，**生产上尚未生效**（154=69434c88 / 245=75848f69
   都不含本轮改动）。
6. **未验证**：部署后的浏览器/端点面未人工浏览（无凭据）。

---

## §7 遗留清单（本轮明确没做，别重复排查）

1. **★ turns 腿的 62.5% 探针行**（31,724 buffers 的 67% 那一族）——
   治法候选：(a) `session_turns` 上按 `origin_stage` 排除探针的**部分索引**
   （谓词可被 matview 与核查查询同时匹配 ⇒ 两者都受益）；
   (b) 探针写入与业务写入**分表/分分区**（写路径语义变更，属主）。
   ⇒ 两者都需要**属主授权**（DDL / 写路径），本轮只登记。
2. **漂移核查的 48,541 buffers/次**（= 刷新自身的量级）——
   核查的价值就是「独立重算」，把它并进刷新就不再是核查 ⇒
   只能从**节奏**（不必每周期都查）或**来源**（换一个更便宜的独立源）下手，属主决策。
3. **10.5% 窗口的双领票放大**（105/95；pgss 54.6 次/天 vs 名义 48）—— **归因未查**。
4. **session 族是刷新链现在的最大头**（2.70h/天 vs routing 0.33h/天）——
   matrix 单次 203s、task_stats 单次 122.6s。下一轮的第一个候选应当是
   **session 族**，而不是 routing 族。
5. **`session_turns` 探针流量为什么占 62.5%** —— 写入侧是不是该少写/不写。
6. **252 的 PG 宿主内存预算**：实测 `shared_buffers = 458752×8kB = 3.5GB`，
   而工作集（`session_turns_2026_10` 2,446MB + details 192MB + ursm 快照 4GB+ …）
   远超缓存 ⇒ 刷新墙钟在2.4s↔23s 之间摆动。**这是宿主级配置变更，属主授权面。**
7. **`ursm_node_snapshot_min` 的 `WITH win` 族**（79/65/25 次，均值 236~257s，
   合计 10.7h DB 时间）—— 本轮在 pgss TOP 里看见，**未展开**（另一条线）。

---

## §8 通用式（并入 `LESSONS.md` §540）

1. ★★★ **「成本问 shared_blks_read」这条口径有自己的适用边界：当一次优化把
   「读磁盘」打到 ≈0、却把「处理的行数」推高 52% 时，read 会把「变贵了」
   说成「变便宜了」。⇒ 报成本时必须同时报「行数」这一个第二坐标。**
2. ★★★ **同一批次、同样 buffer 量、30 倍墙钟差 ⇒ 墙钟差异来自环境，不来自 SQL。**
   （本轮：`routing_analytics_7d` 21.6s vs `routing_audit_summary_7d` 0.7s，
   同为 ~48K buffers、同一刷新周期。）⇒ **单看墙钟会把「宿主忙」误判成「这条 SQL 慢」。**
3. ★★★ **优化之前先量「优化后的那个坐标会不会换人背」**：
   910 把父表腿从「27,486 次窄索引探针」换成「一次 Hash80,705 行」⇒
   buffer 触碰 −64%、**行处理 +52%**。⇒ **成本口径换了轴，就必须换轴报数。**
4. ★★★ **一个量具（pgss 累计计数）在被淘汰时会「静默清零」** ⇒ 用它做长窗口趋势前，
   必须逐条查 `stats_since`（PG 17 才有这一列）。本轮实测 `dealloc=68` 真的在发生，
   只是没砸中 REFRESH 那几条。⇒ **「计数器是单调的」是假设，不是事实。**
5. ★★★ **把视图的「定义查询」搬到 A/B 里时，必须搬它的整个 WHERE** ——
   少搬一条就量到另一个计划（本轮：`count(*)` 让规划器改选 details 的 Seq Scan，
   两臂读数完全不可比）。
6. ★★ **「抬某个资源上限」这件事可能早就做过了，而且可能已经足够** ——
   本轮实测部署态 Δtemp=0 ⇒ §109「收益未验证」的边界被关闭 ⇒
   **正确结论不是「再抬」，是「别再抬，把逐族的下界钉住」。**
7. ★★ **分族的收益不在「值不同」，在「下界能用各自的实测来钉」。**
   共用一个全局值时，下界只能取全族最大值 ⇒ 小族的下界永远没被钉住。
8. ★★ **一条查询是否值得 JIT，取决于「计划代价 /阈值」这个比值，而不是「查询贵不贵」。**
   本轮：计划代价 193,601 vs 阈值 100,000（1.93×）⇒ 付编译代价；实测8/8 轮关掉更快。
   ⇒ ★ **判据是「离阈值有多近」，不是「绝对代价」。**
9. ★★ **两臂的 buffer 足迹逐项相等 ⇒ 差异一定在 CPU/编译侧** ⇒
   这是把「墙钟差异」归因到「CPU 而不是 I/O」的**可证伪**判据。
10. ★★ **关掉 JIT 不是严格零语义**（浮点可能差最后一位）⇒
写「零语义」之前先查 PG 文档那一句，并写明取舍，而不是把它藏起来。
11. ★★ **协调正常 ≠ 节奏正常**：本轮 95 个窗口 0 缺失，但**多出 10.5% 的双领票**
    ⇒ 两者要分别量（缺领票与多领票是相反方向的缺陷）。

---

## §9 合并与验收（诚实边界）

### §9.1 合并上游

- 开工时 `origin/main` 已推进到 **`LESSONS.md` §531**（本机基点只有 §485）⇒
 按仓内「**跨号 + 留缓冲**」惯例（远端取号成对：515/516 · 520/521 · 525/526 · 530/531）
  ⇒ 本轮初取 **§535**。
- ★★ **推之前那一刻的 `origin/main` 才是要对拍的对手**：第一次 push 被拒
  （远端又推进），fetch 后发现**远端自己刚取走了 §535 / §536 / §537**
  ⇒ 本轮**让号重取 §540**（max+3 ⇒ 538/539 留作缓冲带），
  并把 `LESSONS.md` / `AGENTS.md` / 本审计档里的三处引用**同步改号**
  （改号漏一处 = 读者按旧号找不到）。
- 合并：`AGENTS.md` / `LESSONS.md` 两侧都是**追加**，冲突1 处/文件 ⇒
  按仓内先例「**对方的块在前、本分支新增块在后**」解。
 ⇒ **合并后逐字复核**：远端 `LESSONS.md` / `AGENTS.md` 全文作为**连续块**完整存在
  （`r in merged` 实测 True），本轮 §535 / R175 块在其**之后**。

### §9.2 全量测试基线（逐条在干净 HEAD 树上复现）

`go test ./...` 本轮读到 **4 个失败包**。逐条用 `git worktree add /tmp/head-check HEAD`
的**干净树**复跑（串行）：

| 包 | 干净 HEAD | 本轮树 | 判定 |
|---|---|---|---|
| `cmd/tools/sql_source_indirection_audit`（2 条） | **FAIL（同样2 条）** | FAIL | **既有红**（清单72 条 / 实测 76 文件；报出的文件里**没有**本轮改的 `bg/materialized_view_refresher.go`） |
| `sql/migrations/domain`（`TestStandardModelSeedsStayIdentical`） | **FAIL** | FAIL | **既有红**（与本轮无关：SQL seed 比对，本轮未动任何 SQL） |
| `scripts/ursmcheck`（`TestStructuredRegistryHollowoutReadersRejectEmptyRegistry`） | **FAIL（同样 2 个子项）** | FAIL | **既有红**（`web/scripts/i18n-error-*-registry.json` 的读方在基线就红 ⇒不可判定） |
| `domains/streaming/state`（`TestRuntime_RaceCancellation`） | PASS（串行） | **并行跑时 FAIL、串行重跑 PASS** | **并行跑 go 命令抢 build 缓存的假失败**（远端 HEAD 提交信息已记录该陷阱） |

⇒ ★★ **本轮零新增失败。** ★★

### §9.3 本轮读数

- `go build ./...` ✓ ／ `go vet ./bg/` ✓
- `go test ./bg/ -count=1` ✓（含本轮新门 **13 条** + 改写的 R75 分族门）
- 合并上游后**重跑**：`go build ./...` ✓ ／ `go vet ./bg/` ✓ ／ `go test ./bg/` ✓
- 252 生产写入 = **0**；**未部署**（154=69434c88 / 245=75848f69 都不含本轮改动）
