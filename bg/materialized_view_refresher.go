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
	mvRefreshWorkMem = "256MB"

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

	// SessionViewsTimeout 绑定一轮 session 刷新。它必须**明显大于**实测的
	// 40-70s：被 ctx 掐断的表现不是报错，而是**后几个视图根本没刷新**，
	// 且前面已刷的会留下半轮状态 —— 比整体失败更难发现。
	//
	// ★ 2026-10-22：10min → 15min。理由是 sessionViewsStatementTimeout
	// 抬到 600s 之后，**ctx 才是唯一还兜得住整轮的那道闸**：
	// 600s 的语句上限必须**小于**轮内 ctx，否则 ctx 会先掐断，抬钉子等于白抬。
	//
	// ★★ R107 轮（12h 审计）订正：上面那条判据**只比了「语句上限 vs ctx」**，
	//   漏掉了**锁等待预算** —— 而锁等待是**加在语句之前**的，**不是包含在**它里面：
	//     一轮的最坏耗时 = SessionViewLockRetryBudget（整轮共享，≤4min）
	//                    + 各视图的语句耗时
	//   ⇒ 15min(900s) 的 ctx **装不下**「4min 锁等待 + 600s 的 client_stats
	//     + 两个快视图（实测 182.33s + 65.40s）」= 240+600+247.7 = **1087.7s**。
	//   ⇒ ⇒ 也就是说：**抬到 600s 之后，ctx 仍可能是先掐断的那道闸** ——
	//     而 ctx 掐断正是本注释开头说的「比整体失败更难发现」的那种失败形态。
	//     ⇒ 修法不是「再抬语句钉子」（那只是把同一个洞挪个位置），而是**把 ctx
	//       抬到能装下「锁预算 + 慢视图的语句上限 + 其余视图的实测最大值」**。
	//
	// ★ 20min 的依据（把每一项都写出来，便于下一轮复核）：
	//     锁等待（整轮共享上限）      240.0s  （SessionViewLockRetryBudget）
	//     client_stats 语句上限      600.0s  （sessionViewsStatementTimeout）
	//     task_stats 实测最大值      182.33s （154 04:00 轮）
	//     matrix 实测最大值          65.40s （154 04:00 轮）
	//     ─────────────────────────────────────
	//     合计                       1087.7s = 18.13min
	//   ⇒ 20min(1200s) 留 112.3s 余量（≈10%）。
	//
	//   ⚠ 诚实边界：上表的「task_stats/matrix 实测最大值」来自**有语句上限**的轮次，
	//     而新上限 600s 是这次才抬的 ⇒ 若这两个视图在新上限下也跑到 600s，
	//     合计会变成 240+1800=2040s=34min ⇒ 20min 的 ctx 仍会先掐断。
	//     ⇒ 但**这条路今天不成立**：这两个视图的实测成本是 6.19s/7.45s 与 182.33s，
	//     与 client_stats 的 ≥180.31s 差一个量级 ⇒ 它们是「快视图」的前提
	//     **正是本轮把清单改成「快的先刷」所依赖的那个前提**。
	//     ⇒ 若哪天它们也变慢，那说明「快视图」这个前提本身塌了，
	//       届时应当重估的是**成本根因**（把 REFRESH 做便宜），而不是继续抬钉子。
	//
	//   ★ 为什么 20min 仍然是安全的（不会拖累 routing 族）：
	//     session 轮错开到 :05（sessionViewsPhaseOffset），routing 轮在 :00/:30。
	//     20min 的轮 ⇒ 最晚跑到 :25 ⇒ **不撞 :30 那轮 routing**；
	//     15min 的轮跑到 :20 也安全，但 20min 仍在同一量级内
	//     ⇒ 也就是说这次把 ctx 抬到 20min **没有引入新的族间饥饿**。
	//   ⇒ 同时仍**小于** SessionViewsInterval(60min)
	//     （既有门要求：超时 < 间隔，否则刷新周期会堆积）。
	SessionViewsTimeout = 20 * time.Minute

	// sessionViewsOtherViewsBudget 是「client_stats 之外的两个视图」的实测
	// 最坏耗时合计（task_stats 182.33s + matrix 65.40s = 247.73s）。
	//
	// ★ 它存在的唯一目的：让上面那条不变量**可被机器复核**，而不是只活在注释里。
	//   不变量：SessionViewsTimeout ≥ SessionViewLockRetryBudget
	//                                + sessionViewsStatementTimeout
	//                                + sessionViewsOtherViewsBudget
	//   （门见 bg/materialized_view_refresher_session_timeout_test.go）
	sessionViewsOtherViewsBudget = 4 * time.Minute

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
	// 4min 重试 + 195s 物化 = ~7.3min，仍在 SessionViewsTimeout(20min) 之内；
	// 共享而非每视图一份，是因为 SessionViewsTimeout 是**整轮**预算，
	// 三视图各等 4min 会把 ctx 掐断变成新的失败形态。
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
	// ★ 2026-10-22：**按实测成本从快到慢排序**（原来是最慢的排第一）。
	//
	// 为什么要改顺序：一轮是**串行**的，而实测三个视图的成本差 25 倍
	// （252 生产 2026-10-22）：
	//   session_client_task_matrix  7.45s / 65.4s
	//   session_task_stats          6.19s / 151.5s
	//   session_client_stats        34.28s / **≥180.31s（被杀）**
	// ⇒ 慢视图排第一时，它在开跑前就把整轮推迟了它自己的耗时
	//   （实测 245 03:00 轮：client_stats 跑满 180.31s 被杀后，
	//    task_stats 才在 03:03:00 开始、到 03:05:31 才完成），
	//   后面两个视图因此**被推到离新鲜度预算只剩 28 分钟**的位置。
	// ⇒ 改成快的先刷：慢视图无论成败，都**不再推迟**快的两个，
	//   快的两个因此总能在自己的预算内拿到新数据。
	//   ⇒ 这是「限制爆炸半径」，不是「让慢的更快」。
	//
	// ⇒ 语义不变：三个视图彼此独立，admin 侧的新鲜度门按**端点所需**的
	//   视图列表逐个判定（admin/session_analytics_*.go），与本清单顺序无关。
	"session_client_task_matrix",
	"session_task_stats",
	"session_client_stats",
}

// MaterializedViewRefresher manages periodic refresh of routing analytics
// materialized views.
type MaterializedViewRefresher struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
	// Failure tracking for alerting (2026-09-01 P1-B)
	failureCount       int
	lastFailureTime    time.Time
	alertCallback      func(viewName string, consecutiveFailures int, err error)
	driftAlertCallback func(viewName string, breaches int, maxPct float64, maxAbs int64, summary string)
	lastDriftAlertTime time.Time

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
	if reason, err := r.refreshView(ctx, "routing_analytics_7d", useAdvisoryLock, mvRefreshStatementTimeout); err != nil {
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
	if reason, err := r.refreshView(ctx, "routing_audit_summary_7d", useAdvisoryLock, mvRefreshStatementTimeout); err != nil {
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

	ctx, cancel := context.WithTimeout(parentCtx, SessionViewsTimeout)
	defer cancel()

	if handle := r.acquireDistLock(ctx, sessionViewsDistLockLogical); handle != nil {
		defer handle.Release(context.WithoutCancel(ctx))
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

	var failed int
	for _, view := range SessionAnalyticsViews {
		start := time.Now()
		reason, err := retryOnLockHeld(ctx, view, retryDeadline, func() (mvSkipReason, error) {
			return r.refreshView(ctx, view, true, sessionViewsStatementTimeout)
		})
		switch {
		case err != nil:
			// 不 return：三个视图用途独立，单个失败不该作废已刷好的。
			failed++
			recordMVRefreshFailure(view)
			slog.Error("failed to refresh session analytics view",
				"view", view, "error", err, "elapsed", time.Since(start))
		case !reason.skipped():
			recordMVRefreshSuccess(view, time.Since(start).Seconds())
			slog.Info("refreshed session analytics view",
				"view", view, "elapsed", time.Since(start))
		}
	}

	// 整轮被 ctx 掐断是最难发现的一种失败：已刷的视图盖上了新时间戳，
	// 没刷的保持旧值，没有一条错误日志（refreshView 的错误由调用方 ctx 传播，
	// 这里必须自己判）。落到周期级指标上。
	if ctx.Err() != nil {
		slog.Error("session analytics refresh cycle cut off by deadline",
			"timeout", SessionViewsTimeout.String(),
			"views", len(SessionAnalyticsViews), "failed_before_cutoff", failed)
		recordMVRefreshFailure("session_analytics_cycle")
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
//   的抽取锚点 —— 那个门断言 work_mem 的 SET/RESET 就在**本函数体内**，
//   所以这些语句不能被挪进别的函数。
func (r *MaterializedViewRefresher) refreshView(ctx context.Context, viewName string, useAdvisoryLock bool, statementTimeout string) (mvSkipReason, error) {
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
	// largest measured sort. See mvRefreshWorkMem for the full measurement.
	// RESET keeps the pool clean for whatever statement runs next.
	if _, err := conn.Exec(ctx, "SET work_mem = '"+mvRefreshWorkMem+"'"); err != nil {
		return mvRefreshed, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET work_mem")
	}()

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
