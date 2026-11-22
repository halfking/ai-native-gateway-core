// Package bg — materialized_view_refresher.go
//
// MaterializedViewRefresher periodically refreshes the routing analytics
// materialized views (migration 632) so the admin analytics endpoints can
// serve pre-aggregated data instead of Seq-Scanning 314K+ _request_logs
// rows per request.
//
// Refresh strategy:
//   - routing_analytics_7d / routing_audit_summary_7d:
//     REFRESH MATERIALIZED VIEW CONCURRENTLY every RefreshInterval.
//   - Ticks are aligned to wall-clock boundaries (UTC multiples of
//     RefreshInterval), not to each process's start time. The token and
//     advisory locks below are mutual-exclusion WITHIN an overlapping
//     window only — they cannot dedup ticks that arrive minutes apart.
//     With phase-random tickers, every gateway instance on the shared
//     database refreshed on its own phase and the locks never fired
//     (252 production 2026-09-28: three instances → three fixed tick
//     phases → 3× REFRESH amplification, ~90s of matview maintenance per
//     10min). Aligned ticks make concurrent instances contend at the same
//     instant, which is exactly the case the locks DO collapse — one
//     refresh per window fleet-wide. Clock skew beyond one refresh cycle
//     degrades to the old behavior (correct, just duplicated).
//   - Cross-instance coordination is a token-bucket: every RefreshInterval
//     tick is one "token", and exactly one gateway instance should redeem
//     it. Two lock backends implement that mutual exclusion:
//     1. Redis (preferred) — admin/distlock SETNX-with-TTL leader
//     election (2026-09-01). Works across any number of instances
//     without touching Postgres, and self-heals if a leader dies
//     mid-refresh (TTL expiry, no manual unlock needed).
//     2. Postgres advisory lock (fallback) — used verbatim when the
//     distlock manager is nil/disabled (dev/local without Redis, or
//     Redis outage). Session-scoped pg_try_advisory_lock; canary and
//     prod instances share one database, and stacked REFRESHes would
//     otherwise serialize on the view lock and waste cycles.
//     The instance that fails to acquire either lock simply skips that
//     cycle — never blocks, never queues.
//   - Freshness contract with admin/analytics_materialized.go: consumers
//     only trust the views when their routing_mv_refresh_state row is within
//     15 minutes, so one missed cycle is invisible while a dead refresher
//     degrades callers back to the base-view queries. The timestamp is a
//     side-table row written by execRefresh, not a column of the view —
//     see execRefresh for why it cannot live in the target list (837).
package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

const (
	// RefreshInterval is how often we refresh the materialized views.
	// 2026-10-15：10m→30m。墙钟对齐 + Redis/advisory 已把多实例放大收敛为
	// 每窗口一次；剩余成本是单次 CONCURRENTLY ~26s（pg_stat_statements 冠军）。
	// 降频直接砍 PG 负载约 3×。消费者预算见 admin.mvFreshnessBudget=45m。
	RefreshInterval = 30 * time.Minute

	// RefreshTimeout bounds a single refresh cycle. Must stay below
	// RefreshInterval so cycles cannot pile up.
	RefreshTimeout = 5 * time.Minute

	// InitialDelay lets startup migrations (which create and populate the
	// views) settle before the first refresh.
	InitialDelay = 30 * time.Second

	// mvRefreshLockKey is the advisory lock guarding REFRESH cycles across
	// gateway instances sharing one database. Arbitrary constant; only
	// uniqueness matters.
	mvRefreshLockKey int64 = 632_2026_08_31

	// mvRefreshDistLockNamespace/Logical build the Redis key backing the
	// token-bucket leader election (2026-09-01), via distlock.BuildKey so
	// it lands in one Redis Cluster hash slot regardless of deployment.
	mvRefreshDistLockNamespace = "bg"
	mvRefreshDistLockLogical   = "materialized_view_refresh"

	// mvRefreshDistLockTTL bounds how long a Redis-elected leader holds the
	// refresh token before the lease auto-expires. Must exceed RefreshTimeout so a
	// slow-but-alive refresh never loses its lease mid-cycle; distlock
	// auto-renews at ttl/3 while the process is alive, so this is really
	// just the crash-recovery bound (dead leader → lock free within TTL).
	mvRefreshDistLockTTL = 6 * time.Minute

	// mvDriftAlertCooldown prevents a persistent drift from sending an alert on
	// every ten-minute refresh cycle. Metrics remain updated on every check.
	mvDriftAlertCooldown = 30 * time.Minute

	// mvRefreshStatementTimeout lifts the connection-default statement_timeout
	// for the REFRESH statement only (2026-09-21, 252 PG 日志审计轮). The
	// llm_gateway role carries `statement_timeout=30s` (252 生产角色级配置，
	// 兜底所有应用语句)，而 routing_analytics_7d 的 REFRESH CONCURRENTLY
	// 单次物化 ~17M 行、均值 6.2s 但 ~50% 周期在 29.9s 被角色级上限击杀
	// （252 生产日志 6.3h 窗口：38 次超时 / 35 次慢完成）——陈旧度契约
	// （15min）被打破，admin 端点退化回基础视图重查询。客户端 ctx 上限
	// RefreshTimeout=5min 本就允许更久，这里把该连接的语句上限抬到
	// 180s（< 5min ctx，且 < RefreshInterval 的 2 倍，不会堆积周期），
	// 用后立刻 RESET，不污染连接池归还后的其他语句。
	mvRefreshStatementTimeout = "180s"

	// sessionViewsStatementTimeout 是 **session 族专用**的语句上限（2026-10-22）。
	//
	// ★★ 为什么必须分族：mvRefreshStatementTimeout=180s 是**全族共用**的一个值，
	// 而 357 会话分析族三视图的实测成本跨 7s ~ >180s ⇒ 最慢的那一个被
	// **自己的语句上限**杀掉 —— **不是**被锁饿死。这个区分是本轮的核心归因。
	//
	// 生产实测（252 共享库，2026-10-22，两台实例的 journalctl）：
	//   · 245 03:00 轮 session_client_stats：**零锁等待**（上一行就是
	//     "redis leader token acquired"，中间没有任何 contended 记录），
	//     elapsed=180.31s，报错原文
	//     ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
	//     ⇒ REFRESH 本身就需要 >180s，被 180s 的 pin 砍掉。
	//   · 154 04:00 轮 session_client_stats：先为 advisory lock 等了 91s
	//     （退避 2→4→8→15→15…），再跑满 180s 被杀
	//     ⇒ elapsed=271.11s = 91s 等锁 + 180.11s 语句。
	//   · 154 02:00 轮（成功）：client_stats 34.28s / task_stats 6.19s /
	//     matrix 3.75s ⇒ 最快与最慢差 25 倍。
	//
	// 取 600s 的依据：**仓内同一类病已有先例** —— §14330 把巡检脚本的
	// STMT_TIMEOUT 从 180s 抬到 600s，理由同为「空闲 105s / 有负载 180s+」。
	// 此处同样取 600s（对实测的 180.31s 杀线有 3.3× 余量）。
	//
	// ⚠⚠ **诚实边界：被杀掉的那批慢运行不在 pg_stat_statements 里**
	//   （该扩展只统计**完成**的语句）⇒ 那个「client_stats REFRESH
	//   mean 62.4s / max 175.5s」的读数**系统性低估了尾部**：
	//   **max 175.5s < 钉子 180s 恰恰证明被杀掉的那批没被统计**。
	//   ⇒ 真实耗时的尾部目前**不可测**，600s 是「够用」而不是「已证够用」。
	//   ⇒ 根治方向是把 REFRESH 的成本降下来（审计档 R76 §成本根因），
	//     而不是继续抬钉子 —— 抬钉子只把「被杀」换成「占用锁更久」。
	sessionViewsStatementTimeout = "600s"

	// mvRefreshWorkMem lifts work_mem for the REFRESH statement only.
	//
	// Why (2026-10-19, 252 PG temp-file 审计轮，生产只读实测):
	//   pg_stat_database.temp_files = 14,458 / temp_bytes = 104 GB。
	//   pg_stat_statements（10-18 22:03 重置后的 11.6h 窗口）里
	//   TOP15 占全窗口 93%，其中物化视图 REFRESH CONCURRENTLY 约占 54%：
	//     session_task_stats          2,717 MB / 20 次 = 136 MB 每次，均值 10.8s
	//     session_client_stats          801 MB / 20 次 =  40 MB 每次，均值 31.5s
	//     session_client_task_matrix    570 MB / 20 次 = 28.5 MB 每次，均值 2.4s
	//     routing_analytics_7d          317 MB / 35 次 =   9 MB 每次，均值 81.5s
	//   PG 侧 work_mem = 8MB（配置文件）。
	//
	// ⚠⚠ 机理不是"结果集太大排不下"——**这个说法是错的，别沿用**。
	// 实测（§109 订正 §108）这些视图的**输出极小**：
	//   session_task_stats 6 行 / routing_audit_summary_7d 2 行 /
	//   session_client_stats 815 行 / session_client_task_matrix 1,437 行
	// 而 EXPLAIN（COSTS OFF，不执行）给出的节点是
	//   Partial HashAggregate → Gather → Finalize HashAggregate
	//   HashAggregate（下面挂 Gather + 一串 Nested Loop）
	// **计划里根本没有 Sort 节点**，136MB 溢写的是聚合/连接的**中间结果**。
	// ⇒ 所以正确表述是：溢写来自 **work_mem 受限的中间节点**（HashAggregate 一族）。
	// ⇒ 这不改变处置方向：PG 的 HashAggregate 同样按 work_mem 分区并在超限时落盘，
	// 而 REFRESH 是普通查询计划（不是维护命令），吃 work_mem 而非 maintenance_work_mem。
	//
	// ⚠ 诚实边界：**具体是哪个节点在溢写，本轮没有实测**。
	// 坐实它需要 EXPLAIN (ANALYZE, BUFFERS) 跑在刷新路径上，而 REFRESH 形似写操作、
	// 代价高（均值 81.5s 那一档），本轮不做。⇒ 收益未验证，见 §109。
	//
	// 与上面 statement_timeout 同一条处置路径：会话级抬高 + 用后 RESET，
	// 不污染归还连接池后的其他语句。
	//
	// ⚠ 256MB 的依据与它的上界：每轮全网只有一个实例真正执行刷新
	//（Redis 令牌桶 + advisory lock 兜底，见文件头），所以这是**单会话上界**，
	// 不是 ×实例数。调更大之前请先确认 PG 宿主（172.16.2.210）的内存余量 ——
	// 本轮**没有**核到该宿主内存（只读通道拿不到），故这里取保守值而不取"实测够用"。
	//
	// ★★ 2026-10-29（R175）：本常量自此是 **routing 族专用值**。
	//   分族的理由不是"routing 需要更多/更少内存"，而是**每个族的下界要用
	//   它自己那份实测来钉**（routing 族的实测峰值 ~13MB，session 族的实测
	//   峰值 ~120MB + 历史溢写 136MB）——共用一个值时，下界只能取全族最大值，
	//   小族的下界就永远没有被钉住。分族之后两族各自的下界都能写成不等式，
	//   由 bg/materialized_view_refresher_workmem_test.go 逐族断言。
	//   ⚠ 两族当前**取值相同**（都 256MB）——这是实测结论，不是偷懒：
	//   ① session 族在 8MB 下单次溢写 151MB（Sort external merge 13~15MB
	//      ×3 进程 + HashAggregate Batches 4），256MB 下 temp = 0、Batches = 1；
	//   ② routing 族在 8MB / 64MB / 256MB 三档下**逐字相同**（temp 恒 0，
	//      峰值 Hash 9.5MB + Sort 3.5MB）⇒ work_mem 对 routing 族**不是杠杆**。
	//   ⇒ 既然没有一族能从更小的值受益，就没有理由调低 routing 族
	//   （work_mem 是**上限**不是配额，调低只会给未来数据增长埋溢写的雷）。
	mvRefreshWorkMem = "256MB"

	// sessionViewsWorkMem 是 **session 族专用**的 work_mem（2026-10-29 R175）。
	//
	// ★ 取值依据（252 生产，同一 session 内交错 A/B，各两轮）：
	//   8MB  ：Sort Method = external merge，Disk 13,240~15,336kB（主）
	//           + Worker 0 11,512~20,104kB + Worker 1 6,688~14,968kB
	//           ⇒ temp read/write ≈ 19,363/19,394块 ≈ **151MB 单次溢写**
	//           HashAggregate：Buckets 131072 / **Batches 4** /16,512kB
	//           执行时间 28,814ms / 7,929ms（两轮差 3.6× ⇒ 落盘代价随负载放大）
	//   256MB：Sort Method = quicksort，Memory 56,034kB（内存内，无外排）
	//           HashAggregate：Buckets 524288 / **Batches 1** / 63,704kB
	//           temp read/write = **0**
	//           执行时间 5,560ms / 4,957ms
	// ⇒ 256MB 让 session 族的溢写归零（HashAgg 从 4 批降到 1 批）。
	//
	// ★★ 生产侧闭环（2026-10-29 01:12 的 pgss 逐周期差分，五个 REFRESH 条目
	//   全部 Δtemp_blks_written = 0）：**部署态确实不再溢写** ⇒
	//   §109「收益未验证」的诚实边界在本轮被实测关闭。
	//
	// ⚠ 上界同 mvRefreshWorkMem：单会话上界（令牌桶保证全网同时只有一个
	//   刷新在跑），调大之前仍须先核 PG 宿主内存余量（本轮无该读数）。
	sessionViewsWorkMem = "256MB"

	// mvRefreshJIT / sessionViewsJIT 控制刷新路径上 **JIT 编译**的开关
	//（2026-10-29 R175 新增）。
	//
	// ★★ 为什么值得单独一个轴（252 生产实测，同 session 内交错 8 轮）：
	//   252 的 PG 配置是 jit=on / jit_above_cost=100000（库级默认）。
	//   而 routing_analytics_7d 的**定义查询**计划代价是 193,601
	//   ⇒ **超过阈值 1.93×** ⇒ 每次刷新都要付 JIT 编译代价
	//   （EXPLAIN 实测：JIT Functions:91）。
	//   实测两臂（同 session 交错 8 轮，两批次合并）：
	//     jit=on  11,338 / 2,390 / 2,836 / 2,718 / 2,598 / 2,376 / 2,275 / 1,927 ms
	//     jit=off  1,015 /   812 / 1,302 / 1,298 / 1,216 / 1,967 / 2,183 /   723 ms
	//   ⇒ 8/8 轮更快，中位 2,494 → 1,257ms（1.98×），最坏一轮 5.2×。
	//   ★关键判据：**关掉 JIT 不改变 buffer 足迹**
	//     （两臂 root Buffers: shared hit 逐项相等，47,301/47,302）
	//     ⇒ JIT 是**纯 CPU/编译时间**的轴，不影响「成本问 shared_blks_read」口径。
	//   ⇒ 关掉它是零 buffer 代价的墙钟优化。
	//
	// ★ 语义边界（必须写明，别当成"完全零语义"）：
	//   PG文档载明 JIT 编译的浮点运算**可能与解释器差最后一位**。
	//   本仓的 matview 里有 percentile_cont（double precision）⇒
	//   理论上 p50/p95/p99 的最后一位可能在两种编译模式下不同。
	//   ⇒ 但漂移核查（CheckMVConsistency）比较的是**计数**，不是分位数；
	//   且固定成一种模式后值是**稳定**的（而不是随编译与否抖动）。
	//   ⇒ 取舍：用一个 ULP 级的理论差异，换掉每次刷新 91 个函数的编译代价。
	mvRefreshJIT = "off"

	// sessionViewsJIT 是 session 族的 JIT 开关（2026-10-29 R175）。
	//
	// ★ 取值依据与 routing 族**不同**：session 族的计划代价高一个量级
	//   （session_client_task_matrix 单次 318,4xx buffers），
	//   JIT 在这种计划上**有可能**是正收益 ⇒ 不能照抄 routing 族的结论。
	//   实测（同 session 交错 5 轮，两批次合并）：
	//     session_client_task_matrix  jit=on  75,471 / 35,158 / 7,185 / 6,330 / 5,324 ms
	//                             jit=off   6,401 /  4,355 / 5,791 / 5,694 / 5,797 ms
	//   ⚠⚠ **诚实边界**：同一臂两轮相差 14×（75,471 vs 5,324）⇒
	//   **宿主负载的抖动大于臂间差异** ⇒ 这组读数**不能**用来承诺倍率。
	//   能承诺的只有两件事：
	//     ① buffer 足迹不变（两臂 root buffers 逐项相等 ⇒ 不是 I/O 差异）；
	//     ② off 的**最坏轮**（6,401ms）显著低于 on 的最坏轮（75,471ms），
	//        中位也更低（5,791 vs 7,185）⇒ **关掉削的是长尾**。
	//   ⇒ 取"关掉"是**削尾决策**，不是提速承诺（证据强度低于 routing 族的 8/8）。
	sessionViewsJIT = "off"

	// ── 357 会话分析物化视图族（2026-10-07）─────────────────────────────
	// 迁移 357 建了 session_client_stats / session_task_stats /
	// session_client_task_matrix 三个物化视图，admin session-analytics 端点
	// 直接读它们。**此前没有任何刷新排程**：迁移末尾的初始刷新跑过一次之后
	// 就再没人管（245 实测：2026-10-07 06:31:36 填的，16:07 时端点仍在返回
	// 这份数据，陈旧 9h37m，而 session_summaries 仍在持续写入）。
	//
	// 间隔按实测定，不是拍的（245，session_summaries 623,006 行 / 919MB）：
	//   session_client_stats          聚合  8.3s
	//   session_task_stats            聚合  4.8s
	//   session_client_task_matrix    聚合 10.9s
	// 三个合计 ≈24s；REFRESH CONCURRENTLY 还要另建堆并算唯一索引差，
	// 整轮量级 40-70s。源表增速约 200k 行/天，小时级绰绰有余。
	SessionViewsInterval = 60 * time.Minute

	// SessionViewRefreshBudget 是**单视图**的刷新预算（2026-11-22，取代整轮
	// 共享的 SessionViewsTimeout=20min）。
	//
	// ★★ 为什么整轮共享 ctx 被废除（252 生产实测，2026-11-22，
	//   pg_stat_statements + 154/245 journalctl）：
	//
	//   981 族在 252 上单视图 mean 134~266s / max 494~578s，四张合计 mean ≈13min；
	//   加上 357 族，一轮 mean ≈14min，跨轮最坏（各视图 max 求和）≈43min
	//   ⇒ 20min 的整轮 ctx 在数学上**装不下** 7 张视图的最坏情形。
	//   后果（154 16:25/17:25 轮、245 20:25/21:25 轮 journalctl 原文）：排在
	//   清单尾部的 357 族被**饿死** —— matrix 跑 87~94s 被 ctx 掐死、
	//   task_stats/client_stats 带着已死 ctx 在 1~6ms 内秒败。
	//   「单视图失败不终止整轮」的设计意图被共享 ctx 事实性违反。
	//
	//   同日的第二处实锤（211）：matrix 的 REFRESH 选了并行计划 ⇒ 动态共享
	//   内存（DSM）撞 64MB /dev/shm（SQLSTATE 53100）⇒ 每轮秒败 ⇒ 刷新戳冻结
	//   ⇒ clients/tasks 详情端点恒 503 analytics_view_stale（owner 报障页面）。
	//   修法是把它并入 serialRefreshViews（见该 map），与预算制正交。
	//
	// ★ 语义：每个视图独立拿一份「锁等待 + 语句上限」预算，ctx 在视图循环内
	//   构造；慢视图吃满自己的份额不再侵占其他视图。取值算术：
	//
	//     SessionViewLockRetryBudget(240s) + sessionViewsStatementTimeout(600s)
	//     = 840s，+60s 余量 = 900s = 15min（余量 7.1%，满足钉测 ≥5% 要求）。
	//
	// ⚠ 代价（有意接受）：一轮的最坏总时长从「整轮 20min」变成「7×15min」——
	//   仅在每张视图都烧满预算的病态场景出现。tick 不会堆积：refreshLoop 是单
	//   goroutine，sessionTimer 在整轮结束后才 Reset；病态轮会推迟下一轮，并
	//   可能吃掉 :30 的 routing tick（routing 侧有自己的 15min 新鲜度预算与
	//   基础视图退化路径，延迟可恢复）。用「可恢复的 tick 延迟」换「用户可见的
	//   analytics 503」是刻意取舍。
	//
	//   仍必须**小于**周期（60min）：预算死亡 = 该视图本轮失败、下一轮重来，
	//   单视图预算超过周期没有任何意义。
	SessionViewRefreshBudget = 15 * time.Minute

	// SessionViewLockRetryBudget 是**整轮共享**的锁等待预算（不是每视图一份）。
	//
	// 2026-10-12 实测（§10.106.37 §六）：两个周期共用一把 mvRefreshLockKey，
	// 而 60min 的 session 周期是 30min routing 周期的整数倍，两个定时器又都
	// 对齐墙钟（nextAlignedWait）⇒ 它们每个整点必然撞在同一把锁上。
	// 10-12 02:00 那轮三个视图全部 skipped_no_lock，MV 数据陈旧 2 小时。
	// 锁只在真正执行 REFRESH 期间被持有（单轮 14-200s），不是整轮持有，
	// 所以「等一会儿再试」就能拿到 —— 不需要给两个周期各开一把锁键。
	//
	// 取 4min 的理由：实测最长一轮 195s（245 03:00 轮），
	// 4min 重试 + 195s 物化 = ~7.3min，仍在单视图预算
	// SessionViewRefreshBudget(15min) 之内；
	// 共享而非每视图一份（retryDeadline 在循环外算一次，见
	// bg/materialized_view_refresher_lockretry_test.go 的钉测）：skipped_no_lock
	// 不是新鲜度损失 —— 持锁方正在刷同一批视图并落同一张戳表，
	// 所以共享一份等待预算就够了，不会因为后面的视图各拿一份而放大等待。
	SessionViewLockRetryBudget = 4 * time.Minute

	// 退避序列：2s 起，翻倍，封顶 15s。起步要小（典型冲突只差十几秒），
	// 封顶要让「对方这一轮刷了 200s」这种情况仍在预算内等到。
	mvLockRetryInitialBackoff = 2 * time.Second
	mvLockRetryMaxBackoff     = 15 * time.Second

	// sessionViewsInitialDelay 比 InitialDelay 长：startup 迁移正在创建并
	// 首次填充这些视图，等它们落定再进第一轮，否则第一轮必然撞上
	// skipped_missing_view。
	sessionViewsInitialDelay = 2 * time.Minute

	// sessionViewsDistLockLogical 是独立的 Redis 键后缀。与 10min 的
	// routing 周期共用一个 token 会让两个节奏不同的周期互相抢票。
	sessionViewsDistLockLogical = "materialized_view_refresh_session"

	// sessionViewsPhaseOffset 把 session 周期的墙钟相位从整点错开到 :05。
	//
	// ★ 为什么：RefreshInterval=30min（:00/:30）与 SessionViewsInterval=60min
	//   （:00）**都**对齐整点 ⇒ 每个整点必然同刻撞上同一把 mvRefreshLockKey。
	//   实测（154，2026-10-22 04:00 轮）：session 轮为这把锁等了 **91s**
	//   （退避 2→4→8→15→15…），整轮被推到 04:04 才开跑；
	//   03:00 轮则是 routing 族被 session 族占着锁而**两个视图全被跳过**
	//   ⇒ 共享锁同时伤害**两族**。
	//
	// ★ 为什么**不**另开一把锁键（本文件历史上明确否掉过）：
	//   SessionViewLockRetryBudget 上方的注释写着「修法不是另开锁键
	//   （那会让 routing 与 session 的 REFRESH 并发跑，而 session 一轮已经占掉
	//   集群 62%~75% 的 WAL）」⇒ 并发的代价是真实的。
	//
	// ⇒ 错开相位是**零并发**的解法：:00 只有 routing 轮，:05 只有 session 轮，
	//   两族在共享锁上**不再相撞** ⇒ 不需要第二把锁，也没有并发 REFRESH，
	//   严格优于现状。
	//   陈旧度算术不受影响：刷新戳落在 :05，新鲜度预算 90min ⇒ 下一轮
	//   :05 之前仍有 30min 余量（与原先 :00 起算同余）。
	sessionViewsPhaseOffset = 5 * time.Minute

	// sessionViewsMinTickGap 是「启动后的首轮刷新」与「第一个对齐 tick」之间
	// 的最小间隔。错开相位会带来一个新形态：进程若在 :03 启动，首轮刷新在
	// :05，而对齐 tick 也在 :05 ⇒ **两轮只隔 2 分钟**，等于把一轮几百秒的
	// REFRESH 重复做一遍。⇒ 间隔不足时把 tick 推后一整个周期。
	sessionViewsMinTickGap = 10 * time.Minute
)

// SessionAnalyticsViews 是迁移 357 建立、admin session-analytics 端点读取的
// 物化视图族。三个都带 UNIQUE 索引（idx_session_client_stats_tenant_client /
// idx_session_task_stats_tenant_task / idx_session_client_task_matrix_uq），
// 这是 REFRESH MATERIALIZED VIEW **CONCURRENTLY** 的硬前提 —— 缺了会直接报错，
// 而不是退化成普通 REFRESH。refreshView 对不存在的视图会安全跳过
// （skipped_missing_view），所以 357 未应用的库不会因此报错。
var SessionAnalyticsViews = []string{
	// ★ 2026-11-22：**按 252 生产 pg_stat_statements 的 mean 成本重排**
	//   （mean 成本升序；「最快的先刷」纪律不变，换的是实测数字）。
	//
	//   为什么 2026-10-22 的排序被推翻：那份排序依据的是「981 族 0.6~3.5s
	//   （211 真库实测）+ 357 族 7.45~180s（252）」。2026-11-22 在 252 上的
	//   真实画像（pg_stat_statements，338~64 次完成样本）把它颠倒过来了：
	//     session_client_stats        mean  3.4s / max  18.1s（357）
	//     session_task_stats          mean  9.3s / max 140.8s（357）
	//     session_client_task_matrix  mean 102.6s / max 256.5s（357，串行实测 117.6s）
	//     agent_usage_aggregates      mean 134.1s / max 531.8s（981，串行）
	//     project_task_aggregates     mean 164.8s / max 561.7s（981，串行）
	//     model_usage_aggregates      mean 167.2s / max 493.6s（981，串行）
	//     user_session_aggregates     mean 265.9s / max 577.8s（981，串行）
	//   ⇒ 981 族在 252 上是**最慢的四个**（211 的 <1s 读数不适用于 252 的
	//     1955MB 源表），「981 排最前」等于把最慢的排最前。
	//
	//   ★ 在每视图独立预算（SessionViewRefreshBudget）之下，顺序不再影响
	//   「谁被饿死」（那类失败已结构性消失），影响的是**新鲜度年龄分布**：
	//   一个视图的年龄 = 间隔 + 排在它前面的视图耗时之和 + 它自己的耗时；
	//   最坏年龄（清单末项）= 间隔 + 整轮总时长，与顺序无关；快的先刷
	//   （SJF）让更多视图拿到更小的年龄 ⇒ 严格占优。
	//
	// ★ 981（2026-11-19 会话分析系统基础设施）新增的 4 个视图。
	//
	// ★ 为什么必须进这个清单：仓内 2026-10-07 在 245 上实测过「视图建好、
	//   刷新过一次、之后**没有任何刷新排程**」⇒ 端点从 503 变成 200，返回的是
	//   9h37m 前的数字，**没有错误也没有提示**（陈旧被伪装成结论）。
	//   ⇒ 新建物化视图而不接刷新器，等于**主动制造**同一类事故。
	//
	// ★ 全部 7 个视图都带**纯列**唯一索引
	//   （idx_user_session_agg_pk / idx_project_task_agg_pk /
	//   idx_agent_usage_agg_pk / idx_model_usage_agg_pk /
	//   idx_session_client_stats_tenant_client / idx_session_task_stats_tenant_task /
	//   idx_session_client_task_matrix_uq）⇒ REFRESH CONCURRENTLY 的硬前提成立。
	//   ★ 表达式索引不算数：PG 的 findCommittedUniqueIndex 会跳过 indexprs
	//   非空的索引（981 首跑实测踩到过，报 "Create a unique index with no
	//   WHERE clause"）。
	"session_client_stats",
	"session_task_stats",
	"session_client_task_matrix",
	"agent_usage_aggregates",
	"project_task_aggregates",
	"model_usage_aggregates",
	"user_session_aggregates",
}

// MaterializedViewRefresher manages periodic refresh of routing analytics
// materialized views.
type MaterializedViewRefresher struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
	// Failure tracking for alerting (2026-09-01 P1-B)
	failureCount    int
	lastFailureTime time.Time
	// sessionFailureCount 追踪 session 视图族（981 四视图 + 357 三视图）的连续
	// 失败轮数，与 routing 族的 failureCount 分开记账（两个周期节奏不同，共用
	// 一个计数器会互相清零）。2026-11-20 审计轮补接：此前 refreshSessionViews
	// 失败只有指标与日志、从不触发 alertCallback —— 而 252 生产恰处
	// 「981 台账记了 applied、四个视图全缺失」状态，刷新每轮必失败却无告警。
	sessionFailureCount int
	alertCallback       func(viewName string, consecutiveFailures int, err error)
	driftAlertCallback  func(viewName string, breaches int, maxPct float64, maxAbs int64, summary string)
	lastDriftAlertTime  time.Time

	// refreshMu serializes refresh cycles (2026-09-01 P2 race fix). The
	// periodic loop and TriggerRefresh (admin tools) can run concurrently;
	// without this mutex failureCount/lastFailureTime were written from two
	// goroutines — a data race that -race flags and that could also interleave
	// two full REFRESH cycles. Redis token-bucket leader election and the
	// Postgres advisory-lock fallback semantics are unchanged: the mutex only
	// dedups refreshAll entries within THIS process; cross-instance mutual
	// exclusion still rests on the distributed locks below.
	refreshMu sync.Mutex

	// distLock is the optional Redis-backed leader election manager
	// (2026-09-01). Nil (or Enabled()==false) makes refreshView fall back
	// to the Postgres advisory lock unconditionally.
	distLock distlock.Manager
}

// NewMaterializedViewRefresher creates a new refresher instance.
func NewMaterializedViewRefresher(db *pgxpool.Pool) *MaterializedViewRefresher {
	return &MaterializedViewRefresher{
		db:   db,
		done: make(chan struct{}),
	}
}

// SetDistLock wires the Redis-backed distributed lock manager used for
// cross-instance leader election (2026-09-01 — token-bucket refresh
// coordination). Optional: when never called, or called with a manager
// whose Enabled() is false, refreshView transparently falls back to the
// Postgres advisory lock. Call before Start(): the field is read by the
// refresh loop goroutine without a lock, so a post-Start call is a data
// race (current production wiring in main.go only calls it before Start).
func (r *MaterializedViewRefresher) SetDistLock(mgr distlock.Manager) {
	r.distLock = mgr
}

// Start begins the refresh loop in the background.
func (r *MaterializedViewRefresher) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	Go("materialized_view_refresher.refreshLoop", func() { r.refreshLoop(ctx) })
	slog.Info("materialized_view_refresher started",
		"interval", RefreshInterval.String(),
		"timeout", RefreshTimeout.String())
}

// Stop gracefully shuts down the refresh loop. It returns promptly even
// during the initial delay — the loop's waits are ctx-aware.
func (r *MaterializedViewRefresher) Stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
	slog.Info("materialized_view_refresher stopped")
}

// SetAlertCallback sets the callback function for refresh failure alerts.
// The callback is invoked when consecutive failures reach 2 or more.
// Call before Start(): alertCallback is read by the refresh loop goroutine
// without a lock, so a post-Start call is a data race.
func (r *MaterializedViewRefresher) SetAlertCallback(cb func(viewName string, consecutiveFailures int, err error)) {
	r.alertCallback = cb
}

// SetDriftAlertCallback wires the optional callback used when a consistency
// check finds sustained materialized-view drift. It is kept separate from the
// refresh-failure callback so operators can distinguish a successful refresh
// that produced stale data from a refresh that failed outright.
func (r *MaterializedViewRefresher) SetDriftAlertCallback(cb func(viewName string, breaches int, maxPct float64, maxAbs int64, summary string)) {
	r.driftAlertCallback = cb
}

// refreshLoop runs the periodic refresh cycle.
func (r *MaterializedViewRefresher) refreshLoop(ctx context.Context) {
	defer close(r.done)

	// Initial refresh after startup migrations settle; interruptible so
	// Stop() during deploy shutdown does not hang here.
	select {
	case <-ctx.Done():
		return
	case <-time.After(InitialDelay):
	}
	r.refreshAll(ctx)

	// 2026-09-28 252 PG 日志审计轮（R12-F1）：对齐墙钟边界，不用
	// time.NewTicker 的"进程启动相位"。根因实证：252 三台网关各持一个
	// 相位随机的 10min ticker，而 Redis token / advisory lock 都只是
	// "重叠窗口内互斥"——只去重并发刷新，不去重错峰 tick。生产日志
	// 50min 窗口 15 次 routing_analytics_7d REFRESH 呈三个固定相位
	// （:15/:25/:35…、:19/:29…、:23/:33…）= 三实例各刷各的，每轮
	// REFRESH 15-22s + 漂移核查，~90s/10min 持续烧在重复物化上。
	// 对齐后所有实例在同一边界瞬间竞争，既有互斥随之收敛为每窗口
	// 恰好一次 REFRESH。时钟偏移超过单轮刷新时长时退化为旧行为
	//（正确性无损，仅重复）。每轮重算等待时间，刷新耗时不会累积漂移。
	timer := time.NewTimer(nextAlignedWait(time.Now(), RefreshInterval))
	defer timer.Stop()

	// 2026-10-07：357 会话分析视图族接入。**不新起 goroutine**，而是在同一个
	// 循环里挂第二个定时器 —— Stop() 靠 refreshLoop 里的 close(r.done) 收口，
	// 多一个 goroutine 就得多一套等待语义，而这里并不需要并发。
	//
	// 刻意用**另一个定时器**而不是把三个视图塞进 refreshAll：两个周期的节奏
	// 与超时预算完全不同（30min/5min vs 60min/20min），共用一个 ctx 预算会让
	// routing 那一轮被 session 的耗时吃掉预算。陈旧度契约也是分开的
	// （15min vs sessionMvFreshnessBudget）。
	select {
	case <-ctx.Done():
		return
	case <-time.After(sessionViewsInitialDelay - InitialDelay):
	}
	r.refreshSessionViews(ctx)

	sessionTimer := time.NewTimer(sessionViewsTickWait(time.Now()))
	defer sessionTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			r.refreshAll(ctx)
			timer.Reset(nextAlignedWait(time.Now(), RefreshInterval))
		case <-sessionTimer.C:
			r.refreshSessionViews(ctx)
			sessionTimer.Reset(sessionViewsTickWait(time.Now()))
		}
	}
}

// sessionViewsTickWait 是 session 周期的等待时长：落在 :05 相位上，
// 且与上一轮至少隔开 sessionViewsMinTickGap（避免启动后立刻连做两轮）。
func sessionViewsTickWait(now time.Time) time.Duration {
	d := nextAlignedWaitAt(now, SessionViewsInterval, sessionViewsPhaseOffset)
	if d < sessionViewsMinTickGap {
		d += SessionViewsInterval
	}
	return d
}

// nextAlignedWait returns how long to sleep until the next wall-clock
// multiple of interval, anchored to the Unix epoch in UTC so every gateway
// instance — regardless of local timezone or process start time — wakes on
// the same boundary (CST=UTC+8 is a whole multiple of 10min, so the
// boundary set is identical in local time). Landing exactly on a boundary
// yields a full interval, never a zero-length wait.
func nextAlignedWait(now time.Time, interval time.Duration) time.Duration {
	phase := time.Duration(now.UnixNano()) % interval
	if phase < 0 {
		phase += interval
	}
	return interval - phase
}

// nextAlignedWaitAt behaves like nextAlignedWait but lands on a fixed offset
// inside every interval instead of on the interval boundary.
//
// ★ 为什么需要它（2026-10-22）：RefreshInterval=30min 与
// SessionViewsInterval=60min 都对齐整点 ⇒ 两族每个整点必然同刻抢同一把
// advisory lock（实测 session 轮为此等 91s，routing 轮则被整轮跳过）。
// 把其中一族的相位错开 5 分钟，两族就**不再相撞** —— 比「另开一把锁键」
// 好，因为那会让两族的 REFRESH 并发跑，而 session 一轮已经占掉集群
// 62%~75% 的 WAL（本文件 SessionViewLockRetryBudget 上方的注释已记）。
//
// 锚定规则与 nextAlignedWait 一致：以 Unix epoch 为基准 ⇒ 所有实例
// （无论本地时区或进程启动时刻）都落在**同一组** offset 点上。
// offset 必须落在 (0, interval) 内；越界时取模归一。
// 返回值恒 > 0：目标点若已过去，就等下一个周期（绝不返回零长等待）。
func nextAlignedWaitAt(now time.Time, interval, offset time.Duration) time.Duration {
	phase := time.Duration(now.UnixNano()) % interval
	if phase < 0 {
		phase += interval
	}
	target := offset % interval
	if target < 0 {
		target += interval
	}
	d := target - phase
	if d <= 0 {
		d += interval
	}
	return d
}

// refreshAll refreshes all materialized views.
func (r *MaterializedViewRefresher) refreshAll(parentCtx context.Context) {
	// 2026-09-01 P2 race fix: TriggerRefresh (admin) and the periodic loop can
	// enter refreshAll concurrently; serialize the whole cycle so the failure
	// counters below have a single writer and cycles never interleave.
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, RefreshTimeout)
	defer cancel()

	// 2026-09-01: token-bucket cross-instance coordination for the whole
	// cycle (both views share one token — no reason to elect a leader
	// twice per tick). Redis is preferred: it works for any instance
	// count and self-heals on crash via TTL expiry with no manual unlock.
	// When Redis is nil/disabled/unreachable, the Postgres advisory lock
	// below runs exactly as before this change.
	//
	// 2026-09-23 252 SQL 日志审计轮（FIX-4）：advisory lock 升级为全局互斥
	// 后端，Redis leader 不再绕过它（原 useAdvisoryLock=false 删除）。
	// 根因实证：Redis 选举与 advisory 兜底互不可见——无 Redis 的实例
	// （252-dev 形态）走 advisory，Redis leader（154/245）裸奔 REFRESH，
	// 同一视图双发叠跑，252-dev 每 10min tick 饿死在 180s pin 上
	// （击杀时间轴 05:33/05:43/05:53/06:03 = tick 起点精确 +180s，watcher
	// 红手抓捕 252-dev 刷新 98s 仍在跑）。现在 advisory lock 是唯一真理：
	// 任一后端赢了选举，另一个实例 pg_try_advisory_lock 失败即跳过。
	useAdvisoryLock := true
	if handle := r.acquireDistLock(ctx, mvRefreshDistLockLogical); handle != nil {
		defer handle.Release(context.WithoutCancel(ctx))
		if !handle.IsLeader() {
			slog.Info("materialized view refresh skipped, redis token held by another instance")
			// 十七轮审计接线：coordination 指标族此前是零调用死代码，
			// R12-F1（墙钟对齐）的"每窗口收敛单刷新"回验只能靠 slog 肉眼
			// 对账。follower 跳过时两视图各记一次 skipped_follower。
			recordMVCoordination("redis_follower")
			recordMVRefreshSkipped("routing_analytics_7d", "skipped_follower")
			recordMVRefreshSkipped("routing_audit_summary_7d", "skipped_follower")
			return
		}
		slog.Info("materialized view refresh: redis leader token acquired")
		recordMVCoordination("redis_leader")
	} else {
		recordMVCoordination("advisory_only")
	}

	var hasError bool
	var lastErr error
	var failedView string

	start := time.Now()
	if reason, err := r.refreshView(ctx, "routing_analytics_7d", useAdvisoryLock, mvRefreshStatementTimeout, mvRefreshWorkMem, mvRefreshJIT); err != nil {
		hasError = true
		lastErr = err
		failedView = "routing_analytics_7d"
		recordMVRefreshFailure("routing_analytics_7d")
		slog.Error("failed to refresh routing_analytics_7d",
			"error", err,
			"elapsed", time.Since(start))
	} else if !reason.skipped() {
		recordMVRefreshSuccess("routing_analytics_7d", time.Since(start).Seconds())
		slog.Info("refreshed routing_analytics_7d",
			"elapsed", time.Since(start))
		r.checkConsistency(ctx, MVDriftViewRoutingAnalytics7d)
	}

	auditStart := time.Now()
	if reason, err := r.refreshView(ctx, "routing_audit_summary_7d", useAdvisoryLock, mvRefreshStatementTimeout, mvRefreshWorkMem, mvRefreshJIT); err != nil {
		hasError = true
		lastErr = err
		failedView = "routing_audit_summary_7d"
		recordMVRefreshFailure("routing_audit_summary_7d")
		slog.Error("failed to refresh routing_audit_summary_7d",
			"error", err,
			"elapsed", time.Since(auditStart))
	} else if !reason.skipped() {
		recordMVRefreshSuccess("routing_audit_summary_7d", time.Since(auditStart).Seconds())
		slog.Info("refreshed routing_audit_summary_7d",
			"elapsed", time.Since(auditStart))
		r.checkConsistency(ctx, MVDriftViewRoutingAuditSummary7d)
	}

	// 2026-09-01 P1-B: Track consecutive failures and alert on threshold
	if hasError {
		r.failureCount++
		r.lastFailureTime = time.Now()

		// Alert on 2+ consecutive failures (交接文档建议)
		if r.failureCount >= 2 && r.alertCallback != nil {
			r.alertCallback(failedView, r.failureCount, lastErr)
		}
	} else {
		// Reset on success
		r.failureCount = 0
	}
}

// refreshSessionViews 刷新迁移 357 的会话分析物化视图族。
//
// 与 refreshAll 共用 refreshMu（本进程内串行），但走**独立的 Redis token 键**
// —— 两个节奏不同的周期共用一个 token 会互相抢票。
//
// 关于 Postgres advisory lock：refreshView 内部用的是全局的 mvRefreshLockKey，
// 这里沿用而没有另开一把。收益是不必给 refreshView 再串一个锁键参数。
//
// ⚠️ 2026-10-12 订正（§10.106.37 §六）：本函数原注释称「相撞概率约 1.7%，
// 这个取舍是安全的」——**这个估计是错的，而且方向相反**。
// SessionViewsInterval(60min) 是 RefreshInterval(30min) 的**整数倍**，
// 两个定时器又都走 nextAlignedWait 对齐墙钟 ⇒ 它们每个整点**必然**撞在同一把锁上，
// 不是 1.7% 的巧合。实测 2026-10-12 02:00 那一轮三个视图全部 skipped_no_lock，
// MV 数据陈旧 2 小时，00:00~03:00 四小时只落地三轮（0.75 次/小时）。
//
// 修法不是另开锁键（那会让 routing 与 session 的 REFRESH 并发跑，
// 而 session 一轮已经占掉集群 62%~75% 的 WAL），而是**遇到锁就退避重试**：
// 锁只在真正执行 REFRESH 期间持有，不是整轮持有。
// 见 SessionViewLockRetryBudget 与 retryOnLockHeld。
//
// 单视图失败不终止整轮：三个视图的用途彼此独立，第三个失败不该让前两个的
// 成果作废。失败计入各自视图的 failure 指标。
func (r *MaterializedViewRefresher) refreshSessionViews(parentCtx context.Context) {
	if r == nil || r.db == nil {
		return
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	if handle := r.acquireDistLock(parentCtx, sessionViewsDistLockLogical); handle != nil {
		defer handle.Release(context.WithoutCancel(parentCtx))
		if !handle.IsLeader() {
			slog.Info("session analytics view refresh skipped, redis token held by another instance")
			recordMVCoordination("redis_follower")
			for _, v := range SessionAnalyticsViews {
				recordMVRefreshSkipped(v, "skipped_follower")
			}
			return
		}
		slog.Info("session analytics view refresh: redis leader token acquired")
		recordMVCoordination("redis_leader")
	} else {
		recordMVCoordination("advisory_only")
	}

	// 2026-10-12（§10.106.37 §六）：retryDeadline 整轮共享，不是每视图一份。
	// 两个周期共用一把 advisory lock，而 60min 的 session 周期是 30min routing
	// 周期的整数倍、两个定时器又都对齐墙钟 ⇒ 整点必然撞锁。撞上就整轮跳过的话，
	// session 数据会陈旧整整一小时（实测 2026-10-12 02:00 丢了一整轮）。
	// 锁只在真正执行 REFRESH 期间持有（实测单轮 14~195s），等一会儿就能拿到。
	retryDeadline := time.Now().Add(SessionViewLockRetryBudget)

	var failed, succeeded int
	var lastErr error
	failedView := ""
	for _, view := range SessionAnalyticsViews {
		start := time.Now()
		// ★ 2026-11-22：每视图独立预算（SessionViewRefreshBudget），ctx 在
		// 循环体内构造。慢视图吃满自己的预算不再侵占后面视图的份额 ——
		// 共享整轮 ctx 的时代，252 上 981 族把它吃光，尾部的 matrix
		// 跑 87~94s 被掐死、task_stats/client_stats 带着已死 ctx 秒败。
		// ★ 变量刻意沿用名字 ctx：调用点钉测
		//   （TestRefreshCallSites_PassTheRightFamilyTimeout /
		//    TestRefreshSessionViews_RefreshesEveryListedView）锚的是
		//   `r.refreshView(ctx, view, true, sessionViewsStatementTimeout,`。
		ctx, cancel := context.WithTimeout(parentCtx, SessionViewRefreshBudget)
		reason, err := retryOnLockHeld(ctx, view, retryDeadline, func() (mvSkipReason, error) {
			return r.refreshView(ctx, view, true, sessionViewsStatementTimeout, sessionViewsWorkMem, sessionViewsJIT)
		})
		cancel()
		switch {
		case err != nil:
			// 不 return：三个视图用途独立，单个失败不该作废已刷好的。
			failed++
			failedView = view
			lastErr = err
			recordMVRefreshFailure(view)
			slog.Error("failed to refresh session analytics view",
				"view", view, "error", err, "elapsed", time.Since(start))
		case !reason.skipped():
			succeeded++
			recordMVRefreshSuccess(view, time.Since(start).Seconds())
			slog.Info("refreshed session analytics view",
				"view", view, "elapsed", time.Since(start))
		}
	}

	// ★ 2026-11-22：每视图预算制下「轮被 ctx 掐断」不再是整轮语义 ——
	// 某个视图自己的预算死亡已经作为该视图的 err 走上面的失败记账
	//（err 就是 context deadline exceeded，指标与告警面不变）。
	// 这里只剩父 ctx 死亡（进程关停中）这一档：已刷的盖了新戳、没刷的
	// 保持旧值，记一条日志供运维对账。
	if parentCtx.Err() != nil {
		slog.Error("session analytics refresh cycle interrupted by process shutdown",
			"views", len(SessionAnalyticsViews), "failed_before_shutdown", failed)
	}

	// 2026-11-20 审计轮：session 族补接告警（与 routing 族同构）。
	// 此前失败只落指标与日志；252 生产「981 台账记 applied、四视图全缺失」
	// 期间刷新每轮必失败而无任何告警面。三态记账：
	//   有真实失败（含视图预算耗尽——err 为 context deadline exceeded）
	//     → 连续失败 +1，连续 2 轮且告警面可用即告警；
	//   至少一个视图真实刷新成功 → 清零；
	//   全部 skipped（follower/锁竞争）→ 保持原值（那不是视图故障，也不是修复）。
	if failed > 0 {
		r.sessionFailureCount++
		if r.sessionFailureCount >= 2 && r.alertCallback != nil && lastErr != nil {
			r.alertCallback(failedView, r.sessionFailureCount, lastErr)
		}
	} else if succeeded > 0 {
		r.sessionFailureCount = 0
	}
}

// acquireDistLock attempts the Redis token-bucket leader election for one
// refresh cycle. Returns nil whenever Redis coordination was not usable —
// no manager wired, the manager reports Enabled()==false, or Acquire itself
// errored (e.g. Redis outage) — so the caller falls back to the Postgres
// advisory lock. Returns a non-nil handle (leader OR follower) whenever the
// Redis round-trip succeeded; the caller must Release() it either way and
// only proceeds with the refresh when handle.IsLeader() is true.
//
// Acquire is non-blocking for followers here: distlock's follower path
// only performs a quick pubsub subscribe handshake (bounded by
// handshakeTimeout, ~3s) and returns immediately — it does NOT wait for
// the leader to finish. That "return fast, let the caller decide" shape is
// exactly the token-bucket semantics: a follower redeems no token and
// skips the cycle instead of queueing behind the leader.
func (r *MaterializedViewRefresher) acquireDistLock(ctx context.Context, logical string) *distlock.Handle {
	if r.distLock == nil || !r.distLock.Enabled() {
		return nil
	}
	h, err := r.distLock.Acquire(ctx, distlock.AcquireOpts{
		Key:   distlock.BuildKey(mvRefreshDistLockNamespace, logical),
		TTL:   mvRefreshDistLockTTL,
		Mode:  distlock.ModeWaitFollower,
		Scope: "mv_refresh",
	})
	if err != nil {
		if !errors.Is(err, distlock.ErrNotEnabled) {
			slog.Warn("materialized view refresh: redis lock acquire failed, falling back to postgres advisory lock",
				"error", err)
		}
		return nil
	}
	return h
}

// checkConsistency records drift metrics after a successful refresh and emits
// a bounded operator alert when the same process observes material drift.
//
// 2026-09-24 252 SQL 日志审计轮：漂移核查重扫 7 天 routing_analytics_source，
// 在 252 当前负载下 med 21s / max 29.4s，被角色级 statement_timeout=30s
// 击杀（45min 窗口 ×14：mv_data effective_task_type ×8 + audit_summary ×6，
// 每次刷新后必跟一次）。核查跑在与刷新同等的会话级预算上（mvRefresh-
// StatementTimeout, 180s）：核查对 180s 仍然超时属于真异常，走既有的
// query_failed 指标路径暴露；被 30s rolconfig 误杀则只会留下"检查坏了"的
// 假信号并浪费 30s×N 的数据库时间。
func (r *MaterializedViewRefresher) checkConsistency(ctx context.Context, viewName string) {
	if r == nil || r.db == nil {
		return
	}
	// Pin one connection: the session-level statement_timeout must survive
	// both statements of the check (same pattern as refreshView).
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		RecordMVConsistencyError(viewName, "query_failed")
		slog.Warn("materialized view consistency check: acquire conn failed", "view", viewName, "error", err)
		return
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx,
		"SET statement_timeout = '"+mvRefreshStatementTimeout+"'"); err != nil {
		RecordMVConsistencyError(viewName, "query_failed")
		slog.Warn("materialized view consistency check: timeout pin failed", "view", viewName, "error", err)
		return
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET statement_timeout")
	}()
	result, err := CheckMVConsistency(ctx, conn, viewName)
	if err != nil {
		RecordMVConsistencyError(viewName, "query_failed")
		slog.Warn("materialized view consistency check failed", "view", viewName, "error", err)
		return
	}
	if !result.ViewExists {
		return
	}
	RecordMVConsistency(viewName, result)
	// The operator alert is intentionally stricter than the metric: one noisy
	// bucket should remain visible in Prometheus without paging the channel.
	if result.BreachCount < 3 || r.driftAlertCallback == nil || result.MaxAbs < 1000 {
		return
	}
	now := time.Now()
	if !r.lastDriftAlertTime.IsZero() && now.Sub(r.lastDriftAlertTime) < mvDriftAlertCooldown {
		return
	}
	r.lastDriftAlertTime = now
	r.driftAlertCallback(viewName, result.BreachCount, result.MaxPct, result.MaxAbs,
		fmt.Sprintf("视图 %s 检测到 %d 个漂移桶，最大百分比 %.2f%%，最大绝对差 %d", viewName, result.BreachCount, result.MaxPct, result.MaxAbs))
}

// refreshView refreshes a single materialized view using CONCURRENTLY.
// A missing view (e.g. migration 632 not applied on this database) is a
// skip, not an error — callers fall back to base-view queries anyway.
//
// The boolean return reports "skipped" (missing view / lock held by
// another instance) so the caller can distinguish it from success in
// metrics — 十七轮审计接线，skip 语义此前与 success 在观测上不可分。
//
// useAdvisoryLock controls the Postgres pg_try_advisory_lock guard
// (2026-09-01): the caller sets it to false when refreshAll already holds
// the Redis leader token for this cycle, since a second lock layer would
// only add latency. It stays true whenever Redis coordination is
// unavailable, preserving the original single-database dedup behaviour.
// mvSkipReason tells apart "we did not try" from "somebody else got there
// first". Only the latter is worth waiting out: a view that does not exist
// will still be missing thirty seconds later, and spending the retry budget
// on it would delay the views that could have been refreshed.
//
// Before this distinction existed, refreshView returned a bare bool and the
// session cycle had to choose between retrying every skip (wasteful, and it
// hides a genuinely broken view behind a timeout) and retrying none of them
// (the measured starvation in §10.106.37 §六: 2026-10-12 02:00 lost the whole
// session round to a 10-minute routing cycle that held the same lock key).
type mvSkipReason uint8

const (
	mvRefreshed mvSkipReason = iota
	mvSkippedMissingView
	mvSkippedLockHeld
)

// skipped reports whether the view was left alone.
func (s mvSkipReason) skipped() bool { return s != mvRefreshed }

// refreshView refreshes one materialized view.
//
// ★ statementTimeout 参数（2026-10-22）：语句上限**按族**传入，不再全族共用
// 一个 180s。理由与实测见 sessionViewsStatementTimeout 的注释：
// session_client_stats 的 REFRESH 在 252 生产上**零锁等待**就跑了 180.31s
// 被 180s 的 pin 杀掉 ⇒ 共用一个值必然切掉慢的那一个。
// routing 族仍传 mvRefreshStatementTimeout(180s)（12h 窗口内零失败记录，
// 且实测 max 139.6s 仍在界内 ⇒ 没有证据支持改它）。
//
// ★ 签名里的 refreshView( 前缀是既有静态门（bg/materialized_view_refresher_workmem_test.go）
//
//	的抽取锚点 —— 那个门断言 work_mem 的 SET/RESET 就在**本函数体内**，
//	所以这些语句不能被挪进别的函数。
//
// ★★ workMem / jitMode 参数（2026-10-29 R175）：会话画像**按族**传入，
//
//	与 statementTimeout同一套分族接线方式。理由：
//	· work_mem：每个族的下界要用它自己那份实测来钉（见 mvRefreshWorkMem
//	  与 sessionViewsWorkMem 的注释）；共用一个值时小族的下界永远没被钉住。
//	· jit：routing 族的定义查询计划代价 193,601 > jit_above_cost 100000
//	  ⇒ 每次刷新都付91 个函数的编译代价；关掉它**不改变 buffer 足迹**
//	  （A/B 逐项相等）⇒ 纯 CPU 轴的零代价墙钟优化。
//	⇒ 三个轴（statement_timeout / work_mem / jit）共用同一处分族接线，
//	调用点各传自己那份常量，避免「加了参数却仍写死常量」的假修复。
//
// 第四个轴：并行度（max_parallel_workers_per_gather）。
//
// ★ 为什么需要（2026-11-20 211 实测）：REFRESH MATERIALIZED VIEW CONCURRENTLY
//
//	在会话族上会**自己选计划**，而 981 族的 model_usage_aggregates（请求级、
//	40 万行）是全族最重的一个 ⇒ PG 选并行计划 ⇒ 需要**动态共享内存**（DSM）。
//	211 的 PG 容器 `/dev/shm` 只有 **64MB**（Docker 默认 ShmSize=67108864）
//	⇒ 分配 16MB 段失败：
//	  ERROR: could not resize shared memory segment
//	         "/PostgreSQL.4193532418" to 16777216 bytes: No space left on device
//	  (SQLSTATE 53100)
//	⇒ 该视图**永远刷不上** ⇒ 刷新戳永远不落 ⇒ admin 侧新鲜度门
//	  （90min 预算）永远判陈旧 ⇒ 端点恒 503 analytics_view_stale。
//
// ★ 为什么不靠"失败重试串行"兜底（那更"通用"）：
//
//	刷新器对连续失败有**告警**（failureCount ≥ 2 ⇒ alertCallback），
//	每轮都先失败一次 ⇒ 每轮都踩告警线 ⇒ 假告警比假数据更坏。
//	⇒ 走**确定性**路线：这几个视图**一开始就串行刷**。
//
// ★ 为什么 2026-11-22 起旧族（357）也进串行集合（订正本注释此前的
//
//	「只钉 981 族、不动旧族」）：那个结论的前提是「给旧族关并行可能把
//	252 上最慢一条的 ≥180s 顶到 600s 语句上限之外」。2026-11-22 的实测
//	把它证伪了：252 上旧族串行 matrix 117.6s / task_stats 12.1s /
//	client_stats 5.2s（并行 mean 102.6/9.3/3.4s），全在 600s 之内；
//	而 211 上 matrix 的并行计划已经开始 53100 秒败（owner 报障）。
//	⇒ 同族一次修完，见 serialRefreshViews 里 357 族的注释。
//
// serialRefreshViews 是必须**串行**刷新（不启用并行查询）的视图集合。
var serialRefreshViews = map[string]bool{
	// 981 会话分析族（2026-11-19）：最重的 model_usage_aggregates 会选并行计划
	// ⇒ 在 64MB /dev/shm 的 PG 容器上必然 53100。其余三个即使并行也无压力，
	// 但同族统一串行更好推理（且实测代价 <1s）。
	"user_session_aggregates": true,
	"project_task_aggregates": true,
	"agent_usage_aggregates":  true,
	"model_usage_aggregates":  true,

	// 357 会话分析族（2026-11-22，owner 报障轮）：与 981 族**同病同修**。
	//
	// 缺陷本体（211 acc-pg17 生产实测）：
	//   session_client_task_matrix 的 REFRESH CONCURRENTLY 选了**并行计划**
	//   ⇒ 需要动态共享内存（DSM）⇒ 211 的 PG 容器 /dev/shm 只有 64MB
	//   （Docker 默认 ShmSize）⇒ 秒败：
	//     ERROR: could not resize shared memory segment "/PostgreSQL.xxx"
	//            to 8388608 bytes: No space left on device (SQLSTATE 53100)
	//   2026-11-22 20:05/21:05/22:05 三轮连续同错（19:05 是最后一轮成功）
	//   ⇒ 刷新戳冻结 ⇒ clients/{id} 与 tasks/{id} 详情端点恒
	//   503 analytics_view_stale（守卫原文就是 owner 看到的报错文案）。
	//   ⚠ 并行计划不是一直都在：11:05Z 还成功、12:05Z 起连续 53100 ⇒
	//   计划在数据量/统计信息演化中越过了并行阈值 —— **阈值是会自己走过来的**，
	//   不能按「今天还没发病」排除。
	//
	// 实测串行代价（两台生产真库，同参数 SET work_mem=256MB / jit=off /
	// max_parallel_workers_per_gather=0）：
	//   211:  matrix 0.596s
	//   252:  matrix 117.6s（并行 mean 102.6s，+15%）
	//         task_stats 12.1s（并行 mean 9.3s）
	//         client_stats 5.2s（并行 mean 3.4s）
	// ⇒ 全族串行可负担（252 的 +15%~+50% 由单视图预算 15min 吸收）。
	//
	// ★ 为什么 task_stats / client_stats 在「实测未发病」时就一起串行：
	//   211 的并行阈值随 session_summaries 增长会逐个越过（matrix 就是今天刚
	//   越过的）⇒ 同族一次修完，不再逐个等发病再修一轮。两台的实测都证明
	//   串行代价与并行同量级。
	"session_client_task_matrix": true,
	"session_task_stats":         true,
	"session_client_stats":       true,
}

// 第四个轴的接线点：refreshView 里在 jit 之后。
//
// ★ 用**按视图查表**而不是再加一个"按族"参数：串行与否在本族内部就不一致
//
//	（只有最重的那个需要），按族参数表达不了这个事实。
func (r *MaterializedViewRefresher) refreshView(ctx context.Context, viewName string, useAdvisoryLock bool, statementTimeout string, workMem string, jitMode string) (mvSkipReason, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_matviews
			WHERE schemaname = 'public'
			  AND matviewname = $1
		)
	`, viewName).Scan(&exists)
	if err != nil {
		return mvRefreshed, err
	}
	if !exists {
		recordMVRefreshSkipped(viewName, "skipped_missing_view")
		slog.Warn("materialized view does not exist, skipping refresh",
			"view", viewName)
		return mvSkippedMissingView, nil
	}

	// Pin one connection for the whole timeout/refresh(/lock/unlock) sequence:
	// the session-level statement_timeout below and advisory locks are both
	// session-scoped, and pool.Exec may hop connections.
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return mvRefreshed, err
	}
	defer conn.Release()

	// Lift the role-level statement_timeout (252 生产 llm_gateway 角色=30s)
	// for this session only; see mvRefreshStatementTimeout. Reset via
	// WithoutCancel so a canceled/failed refresh still returns a clean
	// connection to the pool.
	//
	// ★ 用参数 statementTimeout 而不是直接用常量：语句上限按族不同
	//   （见 refreshView 的注释）。
	if _, err := conn.Exec(ctx,
		"SET statement_timeout = '"+statementTimeout+"'"); err != nil {
		return mvRefreshed, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET statement_timeout")
	}()

	// Same shape, different axis: the REFRESH rewrite spills to temp files
	// because work_mem (8MB on 252) is two orders of magnitude below the
	// largest measured sort. See mvRefreshWorkMem / sessionViewsWorkMem for
	// the full measurement. RESET keeps the pool clean for whatever
	// statement runs next.
	//
	// ★ 用参数 workMem 而不是直接用常量：按族不同（见 refreshView 的注释）。
	if _, err := conn.Exec(ctx, "SET work_mem = '"+workMem+"'"); err != nil {
		return mvRefreshed, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET work_mem")
	}()

	// Third axis: JIT compilation. The refresh path's queries are re-planned
	// and re-compiled on every cycle; with jit_above_cost=100000 on 252 the
	// routing family's defining query (plan cost 193,601) pays for 91
	// compiled functions per refresh while its whole run is ~2.4s.
	// Turning JIT off does not change the buffer footprint (measured
	// equal), so this is a pure CPU-axis knob. See mvRefreshJIT.
	//
	// ★ 用参数 jitMode 而不是直接用常量：按族不同（session 族的计划代价
	//   高一个量级 ⇒ 不能照抄 routing 族的结论）。
	if _, err := conn.Exec(ctx, "SET jit = '"+jitMode+"'"); err != nil {
		return mvRefreshed, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET jit")
	}()

	// 第四个轴：并行度。串行视图关掉并行查询 ⇒ 不再申请动态共享内存（DSM）
	// ⇒ 绕开 "could not resize shared memory segment ... No space left on device"
	// （SQLSTATE 53100，PG 容器 /dev/shm 只有 64MB 时的必然结局）。
	// 见 serialRefreshViews 的注释（211 实测：关并行后该视图 2.6s 刷完）。
	if serialRefreshViews[viewName] {
		if _, err := conn.Exec(ctx, "SET max_parallel_workers_per_gather = 0"); err != nil {
			return mvRefreshed, err
		}
		defer func() {
			_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET max_parallel_workers_per_gather")
		}()
	}

	if !useAdvisoryLock {
		if err := r.execRefresh(ctx, conn, viewName); err != nil {
			return mvRefreshed, err
		}
		return mvRefreshed, nil
	}

	var locked bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1)`, mvRefreshLockKey).Scan(&locked); err != nil {
		return mvRefreshed, err
	}
	if !locked {
		recordMVRefreshSkipped(viewName, "skipped_no_lock")
		slog.Info("materialized view refresh skipped, another instance holds the lock",
			"view", viewName)
		return mvSkippedLockHeld, nil
	}
	defer func() {
		// Unlock even when ctx is done, or the lock sticks to the pooled
		// connection until it is recycled.
		_, _ = conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, mvRefreshLockKey)
	}()

	if err := r.execRefresh(ctx, conn, viewName); err != nil {
		return mvRefreshed, err
	}
	return mvRefreshed, nil
}

// retryOnLockHeld re-runs attempt while the only thing standing in the way is
// another instance holding mvRefreshLockKey. It gives up on the first attempt
// that reports anything else — an error, a missing view, or a success —
// because none of those get better by waiting, and a missing view in
// particular would burn the whole budget before the next view got a turn.
//
// The attempt function is a parameter rather than a hard-coded call to
// refreshView so the policy can be tested without a database: the contention
// we are fixing is a property of two processes and a Postgres advisory lock,
// and a unit test that cannot manufacture that contention would only be able
// to assert on the source text.
func retryOnLockHeld(ctx context.Context, view string, deadline time.Time, attempt func() (mvSkipReason, error)) (mvSkipReason, error) {
	return retryOnLockHeldWithBackoff(ctx, view, deadline,
		mvLockRetryInitialBackoff, mvLockRetryMaxBackoff, attempt)
}

func retryOnLockHeldWithBackoff(ctx context.Context, view string, deadline time.Time,
	initial, max time.Duration, attempt func() (mvSkipReason, error)) (mvSkipReason, error) {
	backoff := initial
	for {
		reason, err := attempt()
		if err != nil || reason != mvSkippedLockHeld {
			return reason, err
		}

		wait := backoff
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			slog.Info("session analytics view refresh gave up waiting for the advisory lock",
				"view", view, "budget", SessionViewLockRetryBudget.String())
			return mvSkippedLockHeld, nil
		}

		recordMVRefreshSkipped(view, "lock_retry")
		slog.Info("session analytics view refresh contended on the advisory lock, retrying",
			"view", view, "wait", wait.String())

		select {
		case <-ctx.Done():
			return mvSkippedLockHeld, nil
		case <-time.After(wait):
		}

		if backoff < max {
			backoff = nextLockRetryBackoff(backoff, max)
		}
	}
}

// nextLockRetryBackoff doubles the wait and clamps it to max. Extracted so the
// schedule is testable as a value: asserting on it by timing the loop would
// need sleeps, and sleep-based assertions pass on a loaded CI box for the
// wrong reason.
func nextLockRetryBackoff(current, max time.Duration) time.Duration {
	if next := current * 2; next < max {
		return next
	}
	return max
}

// execRefresh runs one REFRESH ... CONCURRENTLY and, only on success, records
// the refresh time in routing_mv_refresh_state.
//
// Migration 837 moved that timestamp out of the materialized view's target
// list. The column used to be NOW() inside the view, which meant REFRESH had
// no way to tell which rows actually changed and rewrote 100% of them
// (measured 252 prod: 100.7% of rows per cycle, only 0.339% really different).
// The stamp now has to be written by whoever ran the refresh — that is the
// whole reason it lives in a table instead of the view.
//
// A failed stamp is returned as an error rather than swallowed: the view
// itself is fine, but every consumer's freshness gate (15-minute budget in
// admin/analytics_materialized.go) would silently read "stale" forever and
// fall back to the base-view queries that this refresh exists to avoid.
// That degradation is worth paging on.
//
// The statement runs on the caller's already-pinned connection, so the
// lifted statement_timeout still applies (see mvRefreshStatementTimeout).
func (r *MaterializedViewRefresher) execRefresh(ctx context.Context, conn *pgxpool.Conn, viewName string) error {
	if _, err := conn.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+viewName); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, dbpkg.StampRoutingMVRefreshSQL, viewName); err != nil {
		return fmt.Errorf("refreshed %s but failed to record refresh time: %w", viewName, err)
	}
	return nil
}

// TriggerRefresh manually triggers an immediate refresh cycle (admin tools,
// tests). Blocking; returns after the cycle completes. A nil-db refresher
// is a no-op so tests can exercise wiring without a database.
func (r *MaterializedViewRefresher) TriggerRefresh(ctx context.Context) error {
	if r == nil || r.db == nil {
		return nil
	}
	slog.Info("manual refresh triggered")
	r.refreshAll(ctx)
	return nil
}
