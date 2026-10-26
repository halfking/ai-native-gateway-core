package bg

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// settingsGetPlatformInt is a thin wrapper around settings.GetPlatformInt
// so call sites stay short and consistent with the rest of this file.
func settingsGetPlatformInt(key string, fallback int) int {
	return settings.GetPlatformInt(key, fallback)
}

// DefaultPromoteInterval is how often the hot-table promote scheduler runs.
// The default 8-hour hot window requires more frequent polling than the
// daily ensure/archive maintenance cycle.
const DefaultPromoteInterval = 1 * time.Hour

// partitionTZ is the partition-boundary calendar (687/694/699 convention):
// month bounds are Asia/Shanghai calendar months. FixedZone follows the
// domains/streaming precedent — Shanghai has no DST, and a fixed offset
// avoids a runtime tzdata dependency in minimal containers.
var partitionTZ = time.FixedZone("Asia/Shanghai", 8*60*60)

// DefaultRetentionWindow is the *_default "hot data" keep-window. Rows
// in *_default whose ts_col is older than (now - DefaultRetentionWindow)
// are eligible to be migrated to the matching monthly partition by the
// promote_*_default_batch functions. R78 订正：原文写「installed in
// migration 336」，但 336 的三个文件在本机均为 .skip（.sql.skip / .bak.skip /
// .down.sql.skip），该迁移从未在本环境执行；实际安装的是
// promote_*_hot_to_partition 模式（见本文件 promoteSpecs 注释）。
//
// 2026-07 hot-table architecture:
//   - most *_hot tables keep an 8-hour hot window by default, then promote
//     into monthly partitions on the promote scheduler;
//   - model_probe_runs_hot is an exception as of 2026-07-14: it no longer
//     promotes and is cleaned by direct TTL DELETE (14 天, 非 8h)。
//     R78 订正：原文让人以为分区结构已废弃，实际父表 model_probe_runs 仍
//     保留 4 个月度分区（3 个 columnar），只是无任何写入方、count(*)=0——
//     空壳结构，占 152 kB。全仓无 model_probe_runs 的非 hot 写入。
const DefaultRetentionWindow = 8 * time.Hour

// promoteCycleTimeout bounds one promote scheduler cycle so a large backlog
// cannot starve partition creation and cleanup workers.
const promoteCycleTimeout = 5 * time.Minute

// promoteCycleMaxBatches bounds the number of database batches processed by a
// single cycle. The next cycle resumes from the remaining hot-table rows.
const promoteCycleMaxBatches = 100

// providerErrorCleanupInterval is independent from the daily partition/archive
// schedule because resolved error rows should not wait 24 hours to be reaped.
const providerErrorCleanupInterval = time.Hour

// promoteBatchSize is the per-call LIMIT inside each promote_xxx_batch
// CTE. Keeps per-tx memory bounded so a backlog cannot OOM the gateway.
//
// Per-table promote batch sizes can be overridden via settings_kv:
//   - lifecycle.promote_batch_size (default 5000)
const promoteBatchSize = 5000

// PartitionManager automatically creates next month's partition,
// archives old partitions to columnar storage, continuously migrates
// cold rows from most *_hot tables into matching monthly partitions,
// and applies direct TTL cleanup for model_probe_runs_hot.
//
// Runs `interval` for ensure+archive (typically 24h), an independent
// hourly promote cycle, and an independent cleanup cycle. Promote cycles
// are time- and batch-bounded so a backlog cannot delay other maintenance.

// On day 1-3 of each month, archives the partition from 2 months ago.
//
// Usage:
//
// partitionDB 是 PartitionManager 对 *pgxpool.Pool 的窄接口视图：生产代码
// 传 pgxpool（构造签名不变），863 判据（parent-shape 门）传 pgxmock。方法
// 集就是本文件实际用到的全部池操作，勿为测试便利扩面。
type partitionDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// pm := bg.NewPartitionManager(dbConn.Pool(), 24*time.Hour)
// pm.Start(context.Background())
// defer pm.Stop()
type PartitionManager struct {
	db              partitionDB
	interval        time.Duration
	promoteInterval time.Duration
	errorAggregator *ProviderErrorAggregator      // 2026-08-29: provider error aggregation
	supplierStats   *SupplierErrorStatsAggregator // 2026-09-05: supplier_errors_hot → stats 预聚合（审计闭环1）

	mu            sync.Mutex
	cancel        context.CancelFunc
	done          chan struct{}
	promoteDone   chan struct{}
	cleanupDone   chan struct{}
	ready         chan struct{}
	started       bool
	stopped       bool
	lastAnalyzeAt time.Time // 2026-07-20: analyze cooldown 5min
}

// archiveSpec describes one archive_xxx call: which SQL function to
// invoke and which day-of-month it should run on. day 0 means "run
// every day in the window" (kept for backwards compatibility if we
// ever go back to that style).
type archiveSpec struct {
	day     int    // 1..7 — only run archiveOldPartitions on this day
	fnName  string // SQL function name, e.g. "archive_request_logs"
	label   string // human label for logs
	enabled bool   //nolint:unused

	// argExpr is the placeholder expression passed to fnName in
	// ensureNextMonthPartitions. Empty means "$1" (timestamptz, the
	// original convention). Functions declared with a `date` parameter
	// need "$1::date" — pgx sends a Go time.Time as timestamptz, and PG
	// will not implicitly down-cast timestamptz → date when resolving
	// the function, so the call fails with "function does not exist".
	argExpr string

	// scalarResult marks functions that return a bare bigint instead of
	// the (status, rows_migrated, partition_dropped) tuple — today only
	// drop_old_state_partitions(p_retention_days int) from migration 391.
	// Its argument is a retention-day count (not a month boundary) and its
	// return is a plain count, so runArchive switches query shape per spec.
	// SELECTing tuple columns from it fails with 42703 "column status does
	// not exist" (pg log 2026-09-03/04 audit, EXPLAIN-verified).
	scalarResult bool

	// partitionUnit controls how ensureNextMonthPartitions derives the
	// argument date for this spec: "" / "month" → AddDate(0, offset, 0)
	// (the historical convention, monthly partitions); "day" → AddDate(0,
	// 0, offset) (daily partitions, R68 迁移 750 usage_facts 按日分区接入）。
	// pgx still receives time.Time; argExpr controls the cast to ::date.
	partitionUnit string

	// parentTable gates the 24h-tick call on the parent table actually being
	// a partitioned table (relkind 'p'). 2026-10-14（本轮 211 日志审计）：
	// db.go 的 boot ensure 对 830 家族先探形状再决定调用，但本 tick 直接
	// 调函数——在 830 未应用（或已回滚、或 restore 后丢失分区性）的环境
	// 上每 24h 报一条 42P17「is not partitioned」。两条路径的契约必须
	// 一致（r1021 教训同族）：声明了 parentTable 的 spec 先探形状，
	// 非 'p' 则按 db.go 的口径 Info 跳过，而不是反复报错。
	parentTable string
}

func NewPartitionManager(db partitionDB, interval time.Duration) *PartitionManager {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	// 聚合器要 *pgxpool.Pool 真身（后台 goroutine 自起生命周期）；
	// 判据注入 pgxmock 时不构造（不会 Start，nil 无害）。
	var errorAggregator *ProviderErrorAggregator
	var supplierStats *SupplierErrorStatsAggregator
	if pool, ok := db.(*pgxpool.Pool); ok {
		errorAggregator = NewProviderErrorAggregator(pool, 10*time.Minute)
		supplierStats = NewSupplierErrorStatsAggregator(pool, 5*time.Minute)
	}
	return &PartitionManager{
		db:              db,
		interval:        interval,
		promoteInterval: DefaultPromoteInterval,
		errorAggregator: errorAggregator,
		supplierStats:   supplierStats,
		done:            make(chan struct{}),
		promoteDone:     make(chan struct{}),
		cleanupDone:     make(chan struct{}),
		ready:           make(chan struct{}),
	}
}

// SetPromoteInterval overrides the default promote cycle (1h). A non-positive
// value disables the promote scheduler. It is safe to call before or after
// Start; a running worker observes the value on its next cycle.
func (pm *PartitionManager) SetPromoteInterval(d time.Duration) {
	pm.mu.Lock()
	pm.promoteInterval = d
	pm.mu.Unlock()
}

func (pm *PartitionManager) Start(ctx context.Context) {
	if pm == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pm.mu.Lock()
	if pm.started {
		pm.mu.Unlock()
		return
	}
	pm.started = true
	pm.stopped = false
	ctx, pm.cancel = context.WithCancel(ctx)
	pm.mu.Unlock()

	Go("partition_manager.run", func() { pm.run(ctx) })
	Go("partition_manager.runPromote", func() { pm.runPromote(ctx) })
	Go("partition_manager.runCleanup", func() { pm.runCleanup(ctx) })
	if pm.errorAggregator != nil {
		pm.errorAggregator.Start(ctx)
	}
	if pm.supplierStats != nil {
		pm.supplierStats.Start(ctx)
	}
	slog.Info("partition_manager started", "interval", pm.interval)
}

func (pm *PartitionManager) Stop() {
	if pm == nil {
		return
	}
	pm.mu.Lock()
	if !pm.started || pm.stopped {
		pm.mu.Unlock()
		return
	}
	pm.stopped = true
	cancel := pm.cancel
	pm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pm.errorAggregator != nil {
		pm.errorAggregator.Stop()
	}
	if pm.supplierStats != nil {
		pm.supplierStats.Stop()
	}
	<-pm.done
	<-pm.promoteDone
	<-pm.cleanupDone
}

func (pm *PartitionManager) run(ctx context.Context) {
	defer close(pm.done)

	// Run partition creation and archive once on startup. Promote runs in its
	// own worker so a backlog cannot block these maintenance operations.
	if pm.db != nil {
		pm.ensureNextMonthPartitions(ctx)
		pm.archiveOldPartitionsIfNeeded(ctx)
		// D8-d: read-only scan for rows stranded in a `*_default` partition.
		// A hit means that month's partition can no longer be created (§9.152).
		pm.checkDefaultPartitionResidue(ctx)
	}
	close(pm.ready)

	mainTicker := time.NewTicker(pm.interval)
	defer mainTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-mainTicker.C:
			if pm.db != nil {
				pm.ensureNextMonthPartitions(ctx)
				pm.archiveOldPartitionsIfNeeded(ctx)
				pm.checkDefaultPartitionResidue(ctx)
			}
		}
	}
}

func (pm *PartitionManager) runPromote(ctx context.Context) {
	defer close(pm.promoteDone)
	select {
	case <-pm.ready:
	case <-ctx.Done():
		return
	}
	pm.promoteDefaultToPartitions(ctx)

	for {
		pm.mu.Lock()
		promoteInterval := pm.promoteInterval
		pm.mu.Unlock()
		if promoteInterval <= 0 {
			// A disabled scheduler remains cancellable and observes a later
			// re-enable without constructing an invalid ticker.
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}

		timer := time.NewTimer(promoteInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			pm.promoteDefaultToPartitions(ctx)
		}
	}
}

func (pm *PartitionManager) runCleanup(ctx context.Context) {
	defer close(pm.cleanupDone)
	ticker := time.NewTicker(providerErrorCleanupInterval)
	defer ticker.Stop()
	pm.cleanupOldProviderErrorDetails(ctx)
	pm.cleanupOldSupplierErrorStats(ctx)
	pm.cleanupOldRoutingFeedbackLog(ctx)
	pm.cleanupOldRoutingOptimizationMetrics(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.cleanupOldProviderErrorDetails(ctx)
			pm.cleanupOldSupplierErrorStats(ctx)
			pm.cleanupOldRoutingFeedbackLog(ctx)
			pm.cleanupOldRoutingOptimizationMetrics(ctx)
			// _request_logs 主表归档（迁移 754）。2026-09-28 十六轮审计
			// 根修：此前挂在 pm.run 的 24h 定相 ticker 上再过滤 hour==3，
			// 而 24h ticker 的相位 = 进程启动时刻——除非网关恰好在
			// 03:00–03:59 之间启动，归档永远不会执行（754 流水线静默
			// 失效）。1h ticker 每个自然日内必然恰好有一次 tick 落在
			// [03:00,04:00) 本地窗口，shouldRunRequestLogsArchive 的
			// hour 门在这里才真正等价于「每日 03 点一次」。
			pm.archiveOldRequestLogs(ctx)
			// R73 审计 N2 收口：turn-logs 聚合积压三件套 gauge（rows /
			// pending_sessions / oldest_age），随 1h tick 刷新。查询走
			// idx_session_turn_logs_expires 索引；健康表近空时近零开销，
			// 积压态下每小时一次索引扫描即为该 gauge 的设计目的。
			pm.refreshTurnLogsBacklogGauge(ctx)
			// migration 759 follow-up：session_turn_details_hot 过期残留
			// 五分类 gauge。759 的 drain 有界无损，真冲突行常驻 hot 等
			// 人工裁决——这组 gauge 就是让「残留构成」可见（良性重复 vs
			// 卡死的 drain）。retention 与 promote worker 同源。
			pm.refreshSessionTurnDetailsExpiredHotGauges(ctx)
		}
	}
}

// ensureNextMonthPartitions creates partitions for current and next
// boundary (month by default; day for daily-partition specs) for every
// table we manage. Idempotent: each underlying ensure_<table>_partition()
// function checks for existence first.
//
// 2026-09-12 (694/699 follow-up): partition bounds are Asia/Shanghai
// calendar months (687/694 convention), so "current/next month" is derived
// in that calendar rather than the server's local zone. Date-signature
// functions additionally receive the calendar day as a literal string — a
// timestamptz→date cast reads the session TimeZone, so a UTC session inside
// the [00:00, 08:00) +08 window of day 1 would derive the previous
// Shanghai month and pre-create the wrong partition.
//
// 2026-09-26 (R68, migration 750): archiveSpec.partitionUnit="day" 让本
// 循环按日驱动（usage_facts 按日分区）；同一 Asia/Shanghai 日历钉扎逻辑
// 自然适用，UTC 会话在 +08 0-8h 同样不会把次日误建为今日。
func (pm *PartitionManager) ensureNextMonthPartitions(ctx context.Context) {
	specs := ensureSpecs()
	for _, s := range specs {
		if !pm.ensureSpecEligible(ctx, s) {
			continue
		}
		for offset := 0; offset <= 1; offset++ {
			unit := s.partitionUnit
			if unit == "" {
				unit = "month"
			}
			var targetBoundary time.Time
			var dateLabel string
			if unit == "day" {
				// R68 (2026-09-26): usage_facts 按日分区接入。offset=0 当日，
				// offset=1 次日；partitionTZ 保证 UTC 会话在 +08 0-8h 不把次
				// 日误建为今日（与月分区的 687/694 钉扎同源）。
				targetBoundary = time.Now().In(partitionTZ).AddDate(0, 0, offset)
				dateLabel = targetBoundary.Format("2006-01-02")
			} else {
				targetBoundary = time.Now().In(partitionTZ).AddDate(0, offset, 0)
				dateLabel = targetBoundary.Format("2006-01")
			}
			argExpr := s.argExpr
			if argExpr == "" {
				argExpr = "$1"
			}
			var arg any = targetBoundary
			if argExpr == "$1::date" {
				arg = targetBoundary.Format("2006-01-02")
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := pm.db.Exec(timeoutCtx,
				"SELECT "+s.fnName+"("+argExpr+")", arg)
			cancel()
			if err != nil {
				slog.Error("partition_manager: ensure partition failed",
					"fn", s.fnName, "label", s.label,
					"unit", unit, "date", dateLabel,
					"error", err)
				continue
			}
			slog.Info("partition_manager: ensured partition",
				"fn", s.fnName, "label", s.label,
				"unit", unit, "date", dateLabel)
		}
	}
}

// ensureSpecEligible 报告本 tick 是否应对该 spec 调 ensure。声明了
// parentTable 的 spec 先探父表形状（与 db.go boot ensure 的 0) 探测同序同
// 口径）：非分区父表 = 迁移未应用/已回滚/restore 丢失分区性，属合法世界
// 态，Info 跳过而不是每 24h 报 42P17。to_regclass 而非 ::regclass：表本身
// 可能不存在（部分供给的库），裸 cast 会 raise。探测失败按保守方向跳过
// （本轮不促升，下轮重试）。
//
// ⚠️ 2026-10-14（12h 审计轮 D-F3）：本探测必须自带超时，不能直接用裸 ctx。
// 它是本文件**唯一**一处没有派生的 WithTimeout 的 DB 调用，而它位于
// ensureNextMonthPartitions 头部、且在启动期先于 close(pm.ready) 执行
// ⇒ 探测挂起会同时卡住 run()（分区 ensure/归档/residue 全停）与 runPromote()
// （等 <-pm.ready，促升与 ANALYZE 全停）。Stop() 的 cancel() 能解开，所以不是
// 永久死锁，但会让分区维护**静默失效**且无任何日志。与同函数下游的 30s 口径
// （见调用方 ensure 的 WithTimeout）对齐。
func (pm *PartitionManager) ensureSpecEligible(ctx context.Context, s archiveSpec) bool {
	if s.parentTable == "" {
		return true
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var partitioned bool
	if err := pm.db.QueryRow(probeCtx, `
		SELECT EXISTS (
		           SELECT 1 FROM pg_class c
		            WHERE c.oid = to_regclass('public.' || $1)
		              AND c.relkind = 'p')`,
		s.parentTable).Scan(&partitioned); err != nil {
		slog.Error("partition_manager: probe parent shape failed",
			"fn", s.fnName, "parent", s.parentTable, "error", err)
		return false
	}
	if !partitioned {
		slog.Info("partition_manager: parent is not a partitioned table — migration not applied (or rolled back); skipping partition ensure",
			"fn", s.fnName, "parent", s.parentTable)
		return false
	}
	return true
}

// checkDefaultPartitionResidue surfaces rows stranded in a `*_default`
// partition (D8-d, audit §9.152 / §9.153).
//
// Why this exists. Both `session_turns` and `_request_logs` carry a DEFAULT
// partition, so a write whose month has no dedicated partition does NOT fail
// with 23514 — PostgreSQL silently routes the row into `*_default`
// (measured: `INSERT 0 1`, row lands in t_default). Nothing ever moves it
// back out: `promote_*_hot_to_partition` drains the `*_hot` tables, and no
// code, SQL or Go, references the `*_default` partitions. The row is therefore
// kept, but the month's dedicated partition can no longer be created —
// `CREATE TABLE ... PARTITION OF ...` fails with
//
//	ERROR: updated partition constraint for default partition "..." would be violated by some row
//
// and nothing retries it into a recovery. That is a self-lock, not a delay.
//
// It also propagates: ensure_sessions_v2_partitions creates sessions /
// session_turns / session_bodies sequentially inside one plpgsql block with
// no EXCEPTION clause, so a failure on session_turns means session_bodies
// never gets its partition either.
//
// Scope. Only partitions whose parent ALSO has dedicated (non-default)
// children are reported. A table whose sole partition is its default
// (stats_event_inbox, system_probe_runs) uses that default by design —
// reporting it would be a false positive on healthy data.
//
// This check is read-only and must never fail the maintenance tick: every
// error path logs and returns.
func (pm *PartitionManager) checkDefaultPartitionResidue(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	tables, err := defaultResidueTargets(queryCtx, pm.db)
	if err != nil {
		slog.Warn("partition_manager: default-residue scan failed", "error", err)
		return
	}

	for _, t := range tables {
		// count(*) is safe here: in the healthy state these partitions are
		// empty, and a non-empty one is exactly the condition we must report.
		// Timeout mirrors the convention used by hotTableBacklogRows.
		cCtx, cCancel := context.WithTimeout(ctx, 30*time.Second)
		var n int64
		err := pm.db.QueryRow(cCtx,
			"SELECT count(*) FROM "+pgxIdent("public", t)).Scan(&n)
		cCancel()
		if err != nil {
			// -1 keeps "could not measure" distinguishable from "empty".
			recordPartitionDefaultResidue(t, -1)
			slog.Warn("partition_manager: default-residue count failed",
				"table", t, "error", err)
			continue
		}
		recordPartitionDefaultResidue(t, n)
		if n > 0 {
			slog.Error("partition_manager: rows stranded in a default partition",
				"table", t, "rows", n,
				"impact", "the matching monthly/daily partition can no longer be created; ensure_ functions for this table will fail on every tick until the rows are relocated",
				"remediation", "relocate the rows (or shift their ts) out of the default partition, then let ensure_sessions_v2_partitions rebuild it — see audit §9.152")
		}
	}
}

// defaultResidueTargets lists the `*_default` partitions that are worth
// counting, i.e. the ones where a non-zero row count is an anomaly.
//
// A default partition is only suspicious when its parent ALSO has dedicated
// non-default children. stats_event_inbox and system_probe_runs are partitioned
// with default as their sole partition — there, default is the design, and
// counting them would report healthy data as residue.
//
// Kept separate from the counting loop so the selector itself can be tested
// against a real catalog (bg/default_residue_realdb_test.go).
func defaultResidueTargets(ctx context.Context, db partitionDB) ([]string, error) {
	return defaultResidueTargetsInSchema(ctx, db, "public")
}

func defaultResidueTargetsInSchema(ctx context.Context, db partitionDB, schema string) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT d.relname
		FROM pg_class d
		JOIN pg_namespace dn ON dn.oid = d.relnamespace AND dn.nspname = $1
		JOIN pg_inherits hi ON hi.inhrelid = d.oid
		JOIN pg_class p ON p.oid = hi.inhparent
		WHERE d.relispartition
		  AND d.relkind = 'r'
		  AND d.relname LIKE '%\_default'
		  AND EXISTS (
		      SELECT 1 FROM pg_inherits i2
		      WHERE i2.inhparent = p.oid AND i2.inhrelid <> d.oid
		  )
		ORDER BY d.relname`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// pgxIdent quotes a schema/table pair for safe interpolation into a statement.
// pgx does not accept a parameter in a FROM position, so the identifier has to
// be quoted here; the caller must pass catalog-derived names only.
func pgxIdent(schema, table string) string {
	return pgx.Identifier{schema, table}.Sanitize()
}

// archiveOldPartitionsIfNeeded archives 2-months-old partition to columnar
// for the table scheduled for today (day-of-month in 1..3).
// Outside that window this is a no-op.
//
// Also runs model_probe_runs_hot 14-day TTL DELETE cleanup
// (controlled by lifecycle.model_probe_runs_ttl_days, hot reloadable).
// 2026-07-14: switched from columnar-partition strategy to pure-hot-table,
// so no more monthly partition drops for model_probe_runs.
func (pm *PartitionManager) archiveOldPartitionsIfNeeded(ctx context.Context) {
	now := time.Now()

	// 1. Monthly archive (day 1..3 only)
	if now.Day() <= 3 {
		twoMonthsAgo := now.AddDate(0, -2, 0)
		today := now.Day()

		for _, s := range archiveSpecs() {
			if s.day != today {
				continue
			}

			// 2026-07-13: state table archive is per-table retention,
			// not twoMonthsAgo. We pass a sentinel and the dedicated
			// drop function reads per-table settings.
			if s.fnName == "drop_old_state_partitions" {
				pm.dropOldStatePartitions(ctx, s)
				continue
			}

			pm.runArchive(ctx, s, twoMonthsAgo)
		}
	}

	// 2. model_probe_runs hot table cleanup (every tick, 14-day TTL).
	// 2026-07-14: switched from columnar-partition strategy to pure-hot-table.
	pm.cleanupOldModelProbeRuns(ctx)

	// 3. 2026-07-13: credential_model_index 7-day TTL cleanup.
	// The SQL function `cleanup_old_credential_model_index()` deletes
	// `credential_model_index` rows older than 7 days. The dedup change
	// in auto_index_refresher.go (only-when-changed writes) keeps the
	// table from growing while traffic is stable; this cleanup handles
	// the long-tail case where a model stops being routed entirely
	// (e.g. credential disabled) so no more rows are inserted and the
	// historical ones can be reaped. Hot-reloadable via
	// lifecycle.credential_model_index_ttl_days (default 7).
	pm.cleanupOldCredentialModelIndex(ctx)

	// 4. 2026-07-13: credential_probe_model_log TTL cleanup.
	// This is a heap (columnar) table without partitions, so we
	// directly DELETE old rows. Runs every tick (1h); default 90d
	// retention via lifecycle.credential_probe_model_log_ttl_days.
	pm.cleanupOldCredentialProbeModelLog(ctx)

	// 5. 2026-07-14: _request_logs_bodies partition cleanup — **已于 2026-10-20
	// 随迁移 890 退役**（v1 正文族已被物理删除，见下方注释块）。
	//
	//    退役了什么：每 tick 调用 `drop_old_request_logs_bodies_partitions($1)`
	//    的那一步（以及它对应的 Go 函数）。该 SQL 函数被 890 一并 DROP ⇒
	//    890 之后每次 tick 都会 42P01（未定义函数），每 tick 一条 ERROR。
	//    为什么退役：它清理的是 v1 正文族 `_request_logs_bodies` 的月分区；
	//    890 之后那张表连同族内其余表/分区/函数一起没了 ⇒ **没有对象可清**。
	//    现在由什么承担：会话族正文（`session_bodies`）走另一条链：
	//      · hot → 月分区：`promote_session_bodies_hot_to_partition`（614 建，
	//        见 promoteSpecs；它只搬运、不删除）；
	//      · 月分区过期 DROP：迁移 841 的**通用**月度分区保留机制
	//        （`llm_gateway_partition_retention` 配置表 +
	//        `llm_gateway_drop_expired_month_partitions()`，由本文件
	//        dropExpiredMonthlyPartitions 调用）。
	//    ⚠ 如实记录：841 的配置表**出厂为空** ⇒ 默认不删任何月分区；
	//      仓库里也没有任何地方 INSERT 它 ⇒ v1 族那条 7 天正文 TTL
	//      **没有等价物**，会话族月分区目前只增不减（列为遗留项）。

	// 6. 2026-07-15: node_probe_runs audit-table TTL cleanup (every tick).
	// Append-only audit table with no partition strategy; without this it
	// grows unboundedly. Default 14d via lifecycle.node_probe_runs_ttl_days.
	// Does NOT touch node_probe_state (the upsert state machine).
	pm.cleanupOldNodeProbeRuns(ctx)

	// 7. 2026-07-15: request_context_attrs side-table TTL cleanup (every
	// tick). One row per business request, no partition strategy; grows at
	// the same rate as request_logs. Default 7d via
	// lifecycle.request_context_attrs_ttl_days.
	pm.cleanupOldRequestContextAttrs(ctx)

	// 8. 2026-07-15: request_stats_minute family TTL cleanup (every tick).
	// Minute-level rollup tables upserted every minute; without this they
	// grow linearly with (minutes × dimension cardinality). Default 14d via
	// lifecycle.request_stats_minute_ttl_days. Covers all three rollup
	// tables; the single-row rollup cursor is not touched.
	pm.cleanupOldRequestStatsMinute(ctx)

	// 9. 2026-07-15: runtime_metrics TTL cleanup (every tick). Instances
	// push metrics periodically, no partition strategy. Default 30d via
	// lifecycle.runtime_metrics_ttl_days.
	pm.cleanupOldRuntimeMetrics(ctx)

	// 10. 2026-07-15: runtime_alert_events TTL cleanup (every tick).
	// Append-only alert events, no partition strategy. Default 30d via
	// lifecycle.runtime_alert_events_ttl_days.
	pm.cleanupOldRuntimeAlertEvents(ctx)

	// 11. 2026-09-27 (R67 session-storage 审计子任务 2, handoff §4):
	// session_turn_logs 保留时长改为可配。此前 430 schema 写死
	// expires_at = NOW() + 24h，且 430 定义的清理函数全仓无调用方——
	// 该表在生产从未被清理过（无界增长）。默认 24h 只在「写入侧烘焙
	// expires_at」这一点上与旧行为一致；清扫本身是首次接上，不是
	// 参数化既有行为（批判式审计证伪了初稿的说法）。跑在本函数
	// （pm.interval，main.go 传 24h）而非 1h 的 runCleanup。
	pm.cleanupSessionTurnLogsByTTL(ctx)

	// 12. 2026-09-30 (R27-HC-1/HC-2): analysis_events / stats_event_inbox
	// 终态行 TTL 清理。两表此前只做状态流转、全仓零 DELETE——本地真库
	// 实测 analysis_events 102 万行/864MB（全部已处理）、inbox default 分区
	// 138 万行（其中 processed 23 万）。分批 ctid 删除（R72「首扫无界
	// DELETE」教训），每 tick 每表有界。
	pm.cleanupOldAnalysisEvents(ctx)
	pm.cleanupOldStatsEventInboxTerminal(ctx)
}

// clampRequestLogsArchiveDays keeps the retention the SQL guard would accept.
//
// archive_request_logs_default RAISEs outside [7,365]; clamping here means the
// value we log is the value the function saw, and a bad settings value degrades
// to a safe default instead of aborting the whole call every tick. The SQL-side
// guard is still the authority — this is a courtesy, not a security boundary.
func clampRequestLogsArchiveDays(raw int) int {
	if raw < 7 {
		return 7
	}
	if raw > 365 {
		return 365
	}
	return raw
}

// requestLogsArchiveHourOfDay is the single hour (local) in which the
// _request_logs archive sweep is allowed to run. See archiveOldRequestLogs for
// why the sweep cannot simply run on every cleanup tick.
const requestLogsArchiveHourOfDay = 3

// shouldRunRequestLogsArchive gates the archive sweep to once per calendar day.
//
// Pure function so the cadence is unit-testable without a clock or a database.
func shouldRunRequestLogsArchive(now time.Time) bool {
	return now.Hour() == requestLogsArchiveHourOfDay
}

// archiveOldRequestLogs archives _request_logs monthly partitions older than the
// configured retention into request_logs_archive_YYYY_MM.
//
// 2026-09-27 (R67 session-storage 审计子任务 7, handoff §9). 2026-09-28
// (十六轮修订审计 P1 根修): the call originally lived on the 24h phase-locked
// pm.run ticker with an hour==3 gate — a phase-locked 24h ticker only ever
// fires at the process start hour, so unless the gateway happened to (re)start
// between 03:00 and 03:59 the sweep never ran at all. It now rides the 1h
// runCleanup tick (see runCleanup), where the hour gate selects exactly one
// tick per calendar day regardless of process start phase. Read fresh from
// settings every tick (lifecycle.request_logs_ttl_days, default 30), so an
// operator change applies without a gateway restart.
//
// ## Why this is daily and not on every tick
//
// The SQL has no "already archived" marker. It enumerates every _request_logs
// month whose month_end is past the cutoff and, for each, pages the WHOLE
// source partition through INSERT ... ON CONFLICT DO NOTHING. Rows archived on
// a previous run simply conflict — but they are still read, joined, and
// re-checked. So the per-run cost is O(all rows older than the retention
// window), and it never shrinks.
//
// Running it on every hourly cleanup tick (providerErrorCleanupInterval = 1h)
// would mean a growing full re-scan of historical _request_logs, forever, for
// work that is already done. The hour==3 gate keeps it to once a day (~24x
// less) without touching the migration.
//
// 2026-09-29 (R80, D07 plan §8.1): the earlier note here said an archive ledger
// was deliberately skipped because "that SQL has never been executed against a
// real PostgreSQL". **That premise expired** — D07 S-01 ran it for real (25.96s
// / 2,125,857 rows) and migration 756 added the (id) index the batch cursor
// needs, so the "wait until it has run" argument no longer holds. The question
// was therefore re-answered with a fresh measurement instead of inherited:
//
//	hot re-run,  6 expired partitions / 1.8M rows : 5.78-7.87s, rows_archived=0
//	hot re-run, 12 expired partitions / 3.6M rows : 10.44s,  rows_archived=0
//
// Doubling the expired data roughly doubled the time — the re-scan is LINEAR
// (Index Scan per batch, 230 buffers / 1.294ms per S-01), not the O(rows^2)
// livelock shape. Extrapolating the measured 2.1M rows/month ingest rate: ~98s
// at 1 year of history, ~493s at 5 years — all far inside the 30min budget
// below, and paid once a day. Reaching 30min would take ~15 years.
//
// On the 1h ticker the gate passes exactly once per calendar day (any start
// phase has exactly one tick inside [03:00,04:00)); a failed 03:xx run waits
// for the next day, same trade-off as the pre-fix wiring.
//
// **Decision: no ledger migration.** A ledger buys a few seconds of daily CPU
// at the cost of a new table, an upsert per archived partition, cross-instance
// consistency reasoning, and a new fail-closed gate — and it opens a fresh
// inconsistency surface (ledger says archived, source partition gets rewritten).
// Revisit if ANY of: (a) ingest exceeds 10x the measured 2.1M rows/month,
// (b) the cadence moves from daily to hourly, (c) a hot re-run is ever measured
// approaching the 30min budget. Until then this daily gate is the mitigation.
//
// The function returns one row per archived partition, so this uses Query and
// sums rows_archived — a QueryRow against a set-returning function would read
// only the first partition and under-report the work done.
//
// R73 审计残留收口（2026-09-28）：调用包在显式事务里并先 SET LOCAL
// statement_timeout='30min'（与 30min Go 预算对齐）。集合返回函数是单条
// 驱动层语句，函数内的批游标不稀释 statement_timeout；252 角色级 30s 会
// 击杀大积压首跑并整批回滚（活锁，同 R72 F2 对 753 的诊断）。
//
// ── 2026-10-20（迁移 890 真删族）：本作业**已退役**，下面这段函数体不会再被执行 ──
//
// 退役了什么：`archive_request_logs_default($1)` 这条 v1 族归档调用。
// 为什么退役：890 把 v1 族（表 + 视图 + 只服务它的分区管理函数）整体删除，
// `archive_request_logs_default` 在 890 的 DROP 列表里 ⇒ 890 之后调用它必然
// 42P01（未定义函数）。旧闸（v1LifecycleRetired = !写开关）只是**恰好**挡住了
// 这条路（停写时为真）；但那是个**偶然**的护栏，不是设计——写开关一翻就漏。
// ⇒ 现在由 v1LifecycleRetired **恒 true** 无条件挡住（见其注释）。
// 函数体保留而不删，是为了留住 F-11/F-12/R73 三条审计结论所依赖的形状
// （每日一次节奏门 + 显式事务 + 30min statement_timeout）：若将来真要复活某个
// 归档作业，这些必须一起回来，否则会重犯「相位锁死 + 角色级 30s 击杀大积压」。
func (pm *PartitionManager) archiveOldRequestLogs(ctx context.Context) {
	if v1LifecycleRetired() {
		// 890 之后 v1 族已不存在 ⇒ 没有可归档的对象，也没有可调用的函数。
		// 回写开关不再能把它打开（v1LifecycleRetired 恒 true）。
		return
	}
	if pm.db == nil {
		return
	}
	if !shouldRunRequestLogsArchive(time.Now()) {
		return
	}
	retentionDays := clampRequestLogsArchiveDays(
		settings.GetPlatformInt("lifecycle.request_logs_ttl_days", 30))

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// R73 审计残留收口：整个集合返回函数是一次驱动层语句——函数内部的
	// 1000 行批游标并不能把 statement_timeout 切成小段。252 生产角色级
	// statement_timeout=30s（llm_gateway rolconfig）会在首批积压 >30s 时
	// 击杀整个调用并连批带回滚，次日重试同一批形成活锁（与 R72 F2 对 753
	// 首扫的诊断同型）。事务内 SET LOCAL 抬到与 Go 侧 30min 预算一致，
	// 事务结束自动还原，pooled 连接不保留（与 promote 60s / analyze 10min
	// 同型，252 SQL 日志审计轮先例）。
	tx, err := pm.db.Begin(timeoutCtx)
	if err != nil {
		slog.Error("partition_manager: _request_logs archive begin failed",
			"ttl_days", retentionDays, "error", err)
		return
	}
	if _, err := tx.Exec(timeoutCtx,
		"SET LOCAL statement_timeout = '30min'"); err != nil {
		tx.Rollback(timeoutCtx)
		slog.Error("partition_manager: _request_logs archive timeout setup failed",
			"ttl_days", retentionDays, "error", err)
		return
	}
	rows, err := tx.Query(timeoutCtx,
		"SELECT archived_partition, rows_archived FROM archive_request_logs_default($1)",
		retentionDays)
	if err != nil {
		tx.Rollback(timeoutCtx)
		slog.Error("partition_manager: _request_logs archive failed",
			"ttl_days", retentionDays, "error", err)
		return
	}

	var partitions int
	var totalRows int64
	var scanFailed bool
	for rows.Next() {
		var name string
		var archived int64
		if err := rows.Scan(&name, &archived); err != nil {
			// pgx 里 rows 未关闭前连接不能复用：先记住失败、跳出循环、
			// Close 之后再统一 Rollback，绝不在 rows 存活时触碰 tx。
			slog.Error("partition_manager: _request_logs archive scan failed", "error", err)
			scanFailed = true
			break
		}
		partitions++
		totalRows += archived
	}
	rows.Close() // pgx v5 Close 无返回值；先归还连接再触碰 tx
	rowsErr := rows.Err()
	if scanFailed || rowsErr != nil {
		tx.Rollback(timeoutCtx)
		if rowsErr != nil {
			slog.Error("partition_manager: _request_logs archive rows failed",
				"rows_error", rowsErr)
		}
		return
	}
	if err := tx.Commit(timeoutCtx); err != nil {
		slog.Error("partition_manager: _request_logs archive commit failed", "error", err)
		return
	}

	if partitions > 0 {
		slog.Info("partition_manager: _request_logs archived",
			"ttl_days", retentionDays, "partitions", partitions, "rows", totalRows)
	}
}

// refreshTurnLogsBacklogGauge publishes the turn-logs aggregation backlog
// 水位（R73 审计 N2 收口，R72 §三遗留）：pending 行数、涉及会话数、最老
// pending 行龄。pending = expires_at > NOW()——聚合器只消费未过期行，
// 过期未聚合即永久丢失，这组 gauge 就是丢失风险的前瞻信号（对照 R47/R48
// hot 表 backlog_rows + oldest_row_age 配对惯例）。
//
// 挂在 runCleanup 的 1h tick：健康表近空时走 idx_session_turn_logs_expires
// 近零开销；积压态下每小时一次索引扫描正是观测目的。查询自身失败（含被
// 角色级 statement_timeout 击杀）只 Warn 并保留上次数值，下个 tick 重试
// ——观测失败不得反过来干扰清扫与归档主流程。
func (pm *PartitionManager) refreshTurnLogsBacklogGauge(ctx context.Context) {
	if pm.db == nil {
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var rows, sessions int64
	var oldestAge float64
	err := pm.db.QueryRow(timeoutCtx, `
		SELECT count(*),
		       count(DISTINCT (tenant_id, session_id)),
		       COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(started_at))), 0)
		  FROM public.session_turn_logs
		 WHERE expires_at > NOW()
	`).Scan(&rows, &sessions, &oldestAge)
	if err != nil {
		slog.Warn("partition_manager: turn-logs backlog gauge refresh failed", "error", err)
		return
	}
	sessionTurnLogsBacklogRows.Set(float64(rows))
	sessionTurnLogsBacklogPendingSessions.Set(float64(sessions))
	sessionTurnLogsBacklogOldestAgeSeconds.Set(oldestAge)
}

// clampSessionTurnLogsTTLHours forces the configured retention into the
// [1, 168] window that migration 753's spec entry advertises.
//
// The lower bound is a data-safety floor, not a style preference: a
// misconfigured 0 (or a negative value from a bad hand-edit) would
// otherwise be handed straight to the SQL interlock as a raise-condition.
// Clamping here means the value we log is the value the function saw.
//
// Note the setting does NOT widen or narrow this sweep. Retention is baked
// into session_turn_logs.expires_at at write time by
// domains/session/v2/turn_logs_writer.go; this function just deletes rows
// whose baked expiry has passed.
func clampSessionTurnLogsTTLHours(raw int) int {
	if raw < 1 {
		return 1
	}
	if raw > 168 {
		return 168
	}
	return raw
}

// cleanupSessionTurnLogsByTTL deletes session_turn_logs rows whose baked
// expires_at has passed.
//
// 2026-09-27 (R67 session-storage 审计子任务 2, handoff §4; semantics
// corrected by critical audit). The audit found the task's premise was
// wrong in a way worth writing down: migration 430 defines
// cleanup_expired_session_turn_logs(), but nothing ever calls it — no Go
// reference, no pg_cron registration, no shell invocation. session_turn_logs
// therefore grew unbounded in production. This is the first sweep that
// actually runs, not a parameterisation of an existing one.
//
// Retention itself is decided at write time: turn_logs_writer.go bakes
// expires_at from lifecycle.session_turn_logs_ttl_hours. The setting is
// re-read every tick, so an operator change applies to newly written rows
// without a gateway restart; rows already written keep their original
// expiry and are swept when they reach it.
//
// The predicate is `expires_at < NOW()`, which is index-backed by
// idx_session_turn_logs_expires (migration 430:275).
// sessionTurnLogsTTLCleanupBatchSize bounds each cleanup call: the SQL
// function deletes at most this many rows per invocation, so a backlog of
// millions of rows is drained as a series of short statements instead of one
// unbounded DELETE holding row locks and emitting a WAL spike for its whole
// duration (R72 audit round; the migration header documents the revision).
const sessionTurnLogsTTLCleanupBatchSize = 10000

// sessionTurnLogsTTLCleanupMaxBatches is a runaway guard on the drain loop;
// with the 5-minute statement budget below it is not expected to bind.
const sessionTurnLogsTTLCleanupMaxBatches = 600

func (pm *PartitionManager) cleanupSessionTurnLogsByTTL(ctx context.Context) {
	ttlHours := clampSessionTurnLogsTTLHours(settings.GetPlatformInt("lifecycle.session_turn_logs_ttl_hours", 24))

	// Match AuditTrimmer.TrimOnce: a nil pool is a no-op rather than a panic,
	// so the manager stays constructible in tests and in degraded boot order.
	if pm.db == nil {
		return
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Drain in bounded batches. Each call commits its own progress: if the
	// 5-minute budget expires mid-drain, at most the current batch is lost —
	// the next 24h tick resumes from what was already deleted.
	var deleted int64
	for batch := 0; batch < sessionTurnLogsTTLCleanupMaxBatches; batch++ {
		var n int64
		err := pm.db.QueryRow(timeoutCtx,
			"SELECT cleanup_session_turn_logs_by_ttl($1, $2)",
			ttlHours, int64(sessionTurnLogsTTLCleanupBatchSize),
		).Scan(&n)
		if err != nil {
			slog.Error("partition_manager: session_turn_logs TTL cleanup failed",
				"ttl_hours", ttlHours, "batch", batch, "deleted_so_far", deleted, "error", err)
			return
		}
		deleted += n
		if n < int64(sessionTurnLogsTTLCleanupBatchSize) {
			break // last (partial) batch — the backlog is drained
		}
	}

	if deleted > 0 {
		slog.Info("partition_manager: session_turn_logs TTL cleanup",
			"ttl_hours", ttlHours, "deleted", deleted)
	}
}

// stateTableTTLSpec describes one parent table dropped via the SQL helper
// drop_old_state_partition_table(parent, retention_days) (migration 689),
// each with its OWN lifecycle TTL. Tables without a dedicated lifecycle
// setting fall back to the given default.
type stateTableTTLSpec struct {
	parent   string // partitioned parent table name
	setting  string // lifecycle.*_ttl_days settings_kv key
	fallback int    // default when the setting is absent

	// partitionUnit selects the DROP strategy:
	//   "" / "month" ⇒ 既有 SQL helper（只枚举 ^<parent>_\d{4}_\d{2}$ 的月分区）
	//   "day"        ⇒ 按日 helper（dropOldDailyStatePartitions）
	//
	// ★ 为什么必须分派（§143.44 实测）：既有 helper 的正则对**日分区名**
	//   （`usage_facts_20261021`）**一个都不匹配** ⇒ 把它登记进这张表
	//   **等于什么都没做**（分区一个也掉不了）—— 这正是我上一轮推理里
	//   犯过的同类错误（把两个不相干的机制接在一起）。
	partitionUnit string
}

// stateTableTTLSpecs mirrors the target_tables list inside
// drop_old_state_partitions (migrations 391/689) — one entry per parent,
// each dropped at its own retention. candidate_failure_logs is 7d
// (2026-09-09 审计 R3#2: 旧行为取全部表 TTL 的 max 且 floor 30d,导致
// 7d 语义脱节;689 起逐表传参,不再取 max/floor)。model_probe_runs
// 已退出分区策略(纯 hot 表),无月度分区,该条目天然空转,保留只为
// 与 SQL 侧 target_tables 对齐。credential_model_index 用它自己的
// lifecycle.credential_model_index_ttl_days(默认 7,与
// cleanupOldCredentialModelIndex 的 7d 语义一致)。
func stateTableTTLSpecs() []stateTableTTLSpec {
	return []stateTableTTLSpec{
		{parent: "routing_decision_log", setting: "lifecycle.routing_decision_log_ttl_days", fallback: 30},
		{parent: "candidate_failure_logs", setting: "lifecycle.candidate_failure_logs_ttl_days", fallback: 7},
		{parent: "handoff_logs", setting: "lifecycle.handoff_logs_ttl_days", fallback: 30},
		{parent: "model_probe_runs", setting: "lifecycle.model_probe_runs_ttl_days", fallback: 14},
		{parent: "credential_model_index", setting: "lifecycle.credential_model_index_ttl_days", fallback: 7},
		// R30 审计（2026-09-16）：supplier_errors 历史月分区此前不在任何
		// drop 清单（hot 侧 8h promote 有界，历史表无限增长）。列存分区
		// DROP 安全（689 helper 逐分区 DROP TABLE），fallback 90 天对齐
		// 诊断/质量评估类数据的保留预期；settings_kv 行在管理员首次
		// 显式设置时落库，此前按 fallback 生效。
		{parent: "supplier_errors", setting: "lifecycle.supplier_errors_ttl_days", fallback: 90},
		// R47（存储演进批，R46 §五#8）：cache_metrics 月分区此前只建不删。
		// 分区名 cache_metrics_YYYY_MM（475）与 689 helper 的
		// ^<parent>_\d{4}_\d{2}$ 枚举正则匹配，直接复用调度。诊断写侧
		// 数据（cachemetrics recorder），fallback 90 天对齐诊断类保留。
		{parent: "cache_metrics", setting: "lifecycle.cache_metrics_ttl_days", fallback: 90},
		// §143.44（2026-10-21）：usage_facts —— **两实例上唯一没有任何留存的族**。
		//
		// 证据（值无关判据）：211 与 252 的 settings_kv 里 11 个
		// `lifecycle.*_ttl_days` 键**完全一致**，其中**没有** usage_facts
		// ⇒ 无 TTL、无 worker、无 DROP 登记 ⇒ 逐日分区 + DEFAULT 分区无限增长。
		// 实测增速：252 ≈1.5 MB/天（275,828 行 / 15 天）、211 ≈0.9 MB/天。
		//
		// ★ 该族是**按日分区**（`usage_facts_YYYYMMDD`，迁移 750），
		//   而 689 helper 只枚举**月度**分区（^<parent>_\d{4}_\d{2}$）
		//   ⇒ 必须走 partitionUnit="day" 的按日 helper。
		//
		// ★ TTL = 365 天（属主 2026-10-21 定）：它是**计费事实**族，
		//   保留期是账期/审计语义（年审窗口），不是技术参数。
		//   ⇒ 代价要说清：252 年增 ≈550 MB（可预期、可预算）。
		{parent: "usage_facts", setting: "lifecycle.usage_facts_ttl_days", fallback: 365, partitionUnit: "day"},
	}
}

// dropOldStatePartitions drops expired monthly partitions for the state
// tables, each at its own per-table retention (lifecycle.<table>_ttl_days,
// read fresh from settings.Global on every call so changes take effect on
// the next partition_manager tick).
//
// 2026-09-09 审计 R3#2: previously this computed the MAX across all state
// table TTLs and clamped to a 30-day floor, then made a single
// drop_old_state_partitions(maxDays) call — candidate_failure_logs (7d
// semantics) therefore never dropped before 30d. Since migration 689 the
// SQL helper drop_old_state_partition_table(parent, days) takes one parent
// per call, so we iterate the tables and pass each its own TTL. No max, no
// 30d floor: tables with an explicitly shorter TTL (7d) drop at 7d; the
// safety floor is the per-call `days < 1` clamp inside the SQL function.
//
// Designed to run on day-2 of the month (matches archiveSpecs()).
func (pm *PartitionManager) dropOldStatePartitions(ctx context.Context, s archiveSpec) {
	for _, spec := range stateTableTTLSpecs() {
		days := settingsGetPlatformInt(spec.setting, spec.fallback)
		if days < 1 {
			days = spec.fallback // safety floor — never 0 (would wipe history)
		}

		// ★ 按日族走按日 helper（§143.44）：既有 SQL helper 只认月度分区名。
		if spec.partitionUnit == "day" {
			dropped, err := pm.dropOldDailyStatePartitions(ctx, spec.parent, days)
			if err != nil {
				slog.Error("partition_manager: daily state partition drop failed",
					"label", s.label, "parent", spec.parent,
					"ttl_days", days, "error", err)
				continue
			}
			if dropped > 0 {
				slog.Info("partition_manager: daily state partition drop ran",
					"label", s.label, "parent", spec.parent,
					"ttl_days", days, "partitions_dropped", dropped)
			}
			continue
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		var dropped int64
		err := pm.db.QueryRow(timeoutCtx,
			"SELECT drop_old_state_partition_table($1, $2)", spec.parent, days,
		).Scan(&dropped)
		cancel()
		if err != nil {
			slog.Error("partition_manager: state table drop failed",
				"label", s.label, "parent", spec.parent,
				"ttl_days", days, "error", err)
			continue
		}
		if dropped > 0 {
			slog.Info("partition_manager: state table drop ran",
				"label", s.label, "parent", spec.parent,
				"ttl_days", days, "partitions_dropped", dropped)
		}
	}
}

// dailyStateDropLockKey 是按日分区 DROP 的跨实例互斥 advisory lock 键。
//
// ★ 与快照族的 632_2026_10_14 **刻意用不同键**（两族互不干扰，
//   否则两族会互相「让位」，一个族的锁会把另一个族的回收饿死）。
const dailyStateDropLockKey int64 = 632_2026_10_21

// dropOldDailyStatePartitions 按【日】粒度回收「整天已过去」的分区。
//
// ★ 为什么必须有它（§143.44 实测）：
//
//	既有的 SQL helper `drop_old_state_partition_table(parent, days)`
//	只枚举**月度**分区（正则 `^<parent>_\d{4}_\d{2}$`）
//	⇒ 它的正则对 `usage_facts_20261021` **一个都不匹配**
//	⇒ usage_facts 的日分区从来不会被回收 ⇒ 该族是全库唯一「无界增长」的族。
//
// ★ 判据（零数据损失 + 永不打断写入）：
//
//	① 整天已在过去（分区的右开边界 ≤ NOW() - days）
//	② 只回收**契约名**的分区（`^<parent>_\d{8}$`）—— 名字不符的**永不删**
//	   （宁可留垃圾也不误删）
//	⇒ 不碰今天/未来 ⇒ 写入路径永远不受影响（23514 不可能发生）
//	⇒ TTL 仍是唯一的语义来源（「保多少天」由配置决定）
//
// ★ 计数诚实：**不用** `DROP TABLE IF EXISTS` —— 对端实例删过时它同样「成功」
//	⇒ 计数被虚增（这正是 retention_partition.go 里已登记的教训）
//	⇒ 改用 42P01（对象不存在）归类为「跳过」。
func (pm *PartitionManager) dropOldDailyStatePartitions(ctx context.Context, parent string, days int) (int64, error) {
	if days < 1 {
		days = 1 // safety floor — 0 会把历史全删光
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	// 候选：只认 ^<parent>_\d{8}$ 的日分区。
	rows, err := pm.db.Query(ctx, `
		SELECT c.relname
		  FROM pg_inherits i
		  JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = to_regclass('public.' || $1)
		   AND c.relname ~ ('^' || $1 || '_\d{8}$')
		 ORDER BY c.relname`,
		parent)
	if err != nil {
		return 0, fmt.Errorf("list daily partitions of %s: %w", parent, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan daily partition name of %s: %w", parent, err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate daily partitions of %s: %w", parent, err)
	}

	var dropped int64
	for _, name := range names {
		// 解析名字里的日期 → 分区右开边界（Asia/Shanghai 日历，与建分区同源）。
		suffix := strings.TrimPrefix(name, parent+"_")
		day, perr := time.ParseInLocation("20060102", suffix, partitionTZ)
		if perr != nil {
			// 名字不合契约 ⇒ 永不删（上面 SQL 已过滤，这里是双保险）。
			slog.Warn("partition_manager: daily partition name does not follow the YYYYMMDD contract; never dropped",
				"parent", parent, "partition", name)
			continue
		}
		dayEnd := day.AddDate(0, 0, 1) // 右开边界：次日零点
		if dayEnd.After(cutoff) {
			continue // 还没过期（含今天/未来）⇒ 不碰
		}

		if err := pm.dropDailyPartition(ctx, name); err != nil {
			if errors.Is(err, errDailyPartitionGone) {
				slog.Info("partition_manager: daily partition already absent; not counted as reclaimed",
					"parent", parent, "partition", name)
				continue
			}
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}

// errDailyPartitionGone 表示「分区在我们列完目录、拿锁之前已经不存在」
// （对端实例刚删完，或运维手工删过）—— 它**不是错误**，只是「没删成」。
var errDailyPartitionGone = errors.New("daily partition already absent before drop")

// dropDailyPartition 在独立事务里拿留存锁后 DROP 一个日分区。
//
// ★ 锁的处理与快照族一致（retention_partition.go 的 dropPartition）：
//   - `SET LOCAL lock_timeout = '0'` 让 advisory **try** 锁立即返回
//     （否则 try 语义也会等满 lock_timeout 才返回 false）；
//   - 拿到锁后恢复 `lock_timeout = 5min` 保护 DROP TABLE 的表锁等待；
//   - **不用** IF EXISTS ⇒ 对端已删 ⇒ 42P01 ⇒ 归类为「跳过」，计数不虚增。
func (pm *PartitionManager) dropDailyPartition(ctx context.Context, name string) error {
	tx, err := pm.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // safe no-op if Commit succeeds

	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '0'`); err != nil {
		return fmt.Errorf("set lock_timeout=0 for advisory lock: %w", err)
	}
	var gotLock bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, dailyStateDropLockKey).
		Scan(&gotLock); err != nil {
		return fmt.Errorf("acquire daily drop lock for %s: %w", name, err)
	}
	if !gotLock {
		return fmt.Errorf("%s: %w", name, errDailyPartitionHeldByPeer)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5min'`); err != nil {
		return fmt.Errorf("restore lock_timeout=5min for drop: %w", err)
	}

	stmt := `DROP TABLE ` + pgx.Identifier{"public", name}.Sanitize()
	if _, err := tx.Exec(ctx, stmt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return fmt.Errorf("%s: %w", name, errDailyPartitionGone)
		}
		return fmt.Errorf("drop daily partition %s: %w", name, err)
	}
	return tx.Commit(ctx)
}

// errDailyPartitionHeldByPeer 表示另一实例正在删 ⇒ 本轮让位（不计数、不报错）。
var errDailyPartitionHeldByPeer = errors.New("daily partition drop lock held by another instance")

// cleanupOldCredentialModelIndex calls the SQL helper
// `cleanup_old_credential_model_index()` which deletes rows older than
// the configured TTL. Designed to run every partition_manager tick
// (1h by default); the SQL DELETE is index-backed on `bucket` so the
// overhead is a few hundred milliseconds even on million-row tables.
func (pm *PartitionManager) cleanupOldCredentialModelIndex(ctx context.Context) {
	ttlDays := settings.GetPlatformInt("lifecycle.credential_model_index_ttl_days", 7)
	if ttlDays < 1 {
		ttlDays = 7 // safety floor — never set to 0 (would wipe the table)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var deleted int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT cleanup_old_credential_model_index()",
	).Scan(&deleted)
	if err != nil {
		slog.Error("partition_manager: credential_model_index cleanup failed",
			"ttl_days", ttlDays, "error", err)
		return
	}

	if deleted > 0 {
		slog.Info("partition_manager: credential_model_index cleanup ran",
			"ttl_days", ttlDays, "rows_deleted", deleted)
	}
}

// cleanupOldModelProbeRuns deletes old rows from model_probe_runs_hot.
// 2026-07-14: pure-hot-table strategy — no more columnar partitions.
// Retention is controlled by lifecycle.model_probe_runs_ttl_days
// (default 14, hot-reloadable).
func (pm *PartitionManager) cleanupOldModelProbeRuns(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.model_probe_runs_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM model_probe_runs_hot WHERE created_at < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: model_probe_runs hot cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("partition_manager: cleaned model_probe_runs_hot",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldNodeProbeRuns deletes audit rows from node_probe_runs older
// than the configured TTL. node_probe_runs is an append-only audit table
// (one row per probe attempt) and has no partition strategy, so without
// this cleanup it grows unboundedly. The sibling node_probe_state table
// is the upsert state machine and is intentionally NOT touched here.
// Retention: lifecycle.node_probe_runs_ttl_days (default 14, hot-reloadable).
// Uses the existing idx_node_probe_runs_started index on started_at.
func (pm *PartitionManager) cleanupOldNodeProbeRuns(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.node_probe_runs_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM node_probe_runs WHERE started_at < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: node_probe_runs cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("partition_manager: cleaned node_probe_runs",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRequestContextAttrs deletes rows from request_context_attrs
// older than the configured TTL. This side table stores per-request
// observability attributes (one row per business request) and has no
// partition strategy, so it grows at the same rate as request_logs.
// Retention: lifecycle.request_context_attrs_ttl_days (default 7,
// hot-reloadable). Backed by idx_rca_ts (added in migration 412).
func (pm *PartitionManager) cleanupOldRequestContextAttrs(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.request_context_attrs_ttl_days", 7)
	if retentionDays < 1 {
		retentionDays = 7
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM request_context_attrs WHERE ts < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: request_context_attrs cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("partition_manager: cleaned request_context_attrs",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRequestStatsMinute deletes rows older than the configured TTL
// from the three minute-level rollup tables. They are upserted every minute
// (bg/stats_minute_rollup.go) with no partition strategy, so without this
// they grow linearly with (minutes × dimension cardinality). All three share
// the `bucket` timestamp column and the lifecycle.request_stats_minute_ttl_days
// setting (default 14). request_stats_rollup_cursor (single-row state table)
// is intentionally NOT touched. Each table has a bucket-leading index so the
// DELETE is an index descent.
func (pm *PartitionManager) cleanupOldRequestStatsMinute(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.request_stats_minute_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for _, table := range []string{
		"request_stats_minute",
		"request_stats_dim_minute",
		"request_stats_error_drill_minute",
	} {
		tag, err := pm.db.Exec(timeoutCtx,
			"DELETE FROM "+table+" WHERE bucket < now() - ($1 || ' days')::interval",
			retentionDays)
		if err != nil {
			slog.Error("partition_manager: request_stats cleanup failed",
				"table", table, "retention_days", retentionDays, "error", err)
			continue
		}
		if n := tag.RowsAffected(); n > 0 {
			slog.Info("partition_manager: cleaned request_stats table",
				"table", table, "deleted_rows", n, "retention_days", retentionDays)
		}
	}
}

// cleanupOldRuntimeMetrics deletes rows from runtime_metrics older than the
// configured TTL. Instances push metrics periodically (center/runtime_metrics)
// and the table has no partition strategy. Retention:
// lifecycle.runtime_metrics_ttl_days (default 30, hot-reloadable). Backed by
// idx_rt_time (timestamp DESC).
func (pm *PartitionManager) cleanupOldRuntimeMetrics(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.runtime_metrics_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM runtime_metrics WHERE timestamp < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: runtime_metrics cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned runtime_metrics",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRuntimeAlertEvents deletes rows from runtime_alert_events older
// than the configured TTL. Alert events are append-only (status can be
// triggered/resolved but rows are never deleted). Retention:
// lifecycle.runtime_alert_events_ttl_days (default 30, hot-reloadable).
// Backed by idx_rae_detected_at (added in migration 413).
func (pm *PartitionManager) cleanupOldRuntimeAlertEvents(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.runtime_alert_events_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		"DELETE FROM runtime_alert_events WHERE detected_at < now() - ($1 || ' days')::interval",
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: runtime_alert_events cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned runtime_alert_events",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldCredentialProbeModelLog deletes rows from
// credential_probe_model_log older than the configured TTL.
//
// 2026-07-13: this is a heap (columnar) table without monthly
// partitions, so we use direct DELETE. Designed to run every tick
// (1h by default); the SQL DELETE is index-backed on created_at
// (we add an index if missing). Hot-reloadable via
// lifecycle.credential_probe_model_log_ttl_days (default 90).
func (pm *PartitionManager) cleanupOldCredentialProbeModelLog(ctx context.Context) {
	ttlDays := settingsGetPlatformInt("lifecycle.credential_probe_model_log_ttl_days", 90)
	if ttlDays < 7 {
		ttlDays = 7 // safety floor
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var deleted int64
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT cleanup_old_credential_probe_model_log($1)", ttlDays,
	).Scan(&deleted)
	if err != nil {
		slog.Error("partition_manager: credential_probe_model_log cleanup failed",
			"ttl_days", ttlDays, "error", err)
		return
	}

	if deleted > 0 {
		slog.Info("partition_manager: credential_probe_model_log cleanup ran",
			"ttl_days", ttlDays, "rows_deleted", deleted)
	}
}

func (pm *PartitionManager) runArchive(ctx context.Context, s archiveSpec, twoMonthsAgo time.Time) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var status string
	var rowsMigrated int64
	var partitionDropped bool

	var err error
	if s.scalarResult {
		// Scalar fns return the dropped-partition count directly; there is
		// no status tuple to scan. 60 days mirrors the two-month hold that
		// the tuple specs archive (twoMonthsAgo below).
		var dropped int64
		err = pm.db.QueryRow(timeoutCtx,
			"SELECT "+s.fnName+"($1::int)", int(60),
		).Scan(&dropped)
		if err == nil {
			status = "success"
			rowsMigrated = dropped
			partitionDropped = dropped > 0
		}
	} else {
		// Two caller-side traps, both observed in the PG log 2026-09-03/04
		// and EXPLAIN-verified against the live DB:
		//   1. "$1" alone sends pgx's timestamptz for the functions' `date`
		//      parameter — timestamptz→date is not an implicit cast, so the
		//      call dies with 42883 "function ... does not exist". The
		//      $1::date cast matches the argExpr convention documented on
		//      archiveSpec.
		//   2. A named column list breaks under column-name drift:
		//      archive_credential_model_index shipped as (status,
		//      rows_archived, rows_deleted) in 318 and 653/654 realigned it
		//      to (status, rows_migrated, partition_dropped); whichever
		//      shape the database still has, the other spelling fails with
		//      42703. SELECT * scans either 3-tuple positionally.
		err = pm.db.QueryRow(timeoutCtx,
			"SELECT * FROM "+s.fnName+"($1::date)",
			twoMonthsAgo,
		).Scan(&status, &rowsMigrated, &partitionDropped)
	}

	if err != nil {
		slog.Error("partition_manager: archive failed",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"),
			"error", err)
		return
	}

	switch status {
	case "success":
		slog.Info("partition_manager: archive succeeded",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"),
			"rows_migrated", rowsMigrated,
			"partition_dropped", partitionDropped)
	case "skipped":
		slog.Debug("partition_manager: archive skipped (partition not found)",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"))
	default:
		slog.Warn("partition_manager: archive returned unknown status",
			"fn", s.fnName, "label", s.label,
			"month", twoMonthsAgo.Format("2006-01"),
			"status", status)
	}
}

// v1LifecycleRetired reports whether the v1 request_logs family's lifecycle
// jobs (ensure/promote/archive) are retired.
//
// 2026-10-20（迁移 890 真删族）：**本函数恒为 true**。
//
// 旧语义（2026-10-13 起）：S4 停写把 storage.request_logs_write_enabled 翻成
// false，迁移 860 再把族改名成 `_request_logs*` 作为「待删除标志」。旧闸的前提是
// **「族还在，只是被冻结」** ⇒ 停写时暂停维护（预建未来分区 / 排空恒空的 hot 表
// / 归档没人读的摘要都是纯浪费），回写开启时自动恢复维护。
//
// 890 之后这个前提**已不存在**：族（表 + 分区 + 序列 + 只服务它的分区管理函数）
// 被物理删除。于是「恢复写 ⇒ 恢复维护」没有对象可维护 —— 恢复维护只会把
// 「调用已被 DROP 的函数」重新变成每 tick 一次的 42P01。⇒ 维护侧**无条件**退役：
// ensure/promote 的 v1 条目已从清单里移除（见 ensureSpecs / promoteSpecs 的注释），
// archive 的调用由本函数的恒 true 挡在门外。
//
// ⚠ 与写开关的关系：storage.request_logs_write_enabled 现在只影响**写面**，
// 而写面在 890 之后已无落库对象（族表没了）⇒ 「回切到 v1 写」这条路本身已由
// 890 关闭，不是本函数关的。本函数不再读该设置，是为了让「维护侧无条件退役」
// 这一事实**不依赖任何设置的状态**。
func v1LifecycleRetired() bool {
	return true
}

// ensureSpecs returns the ensure_<table>_partition() functions we
// invoke on every tick. Add a row here when a new table is onboarded.
//
// 2026-10-20（迁移 890 真删族）：**v1 request_logs 族的两条 ensure 条目已从本
// 清单移除**（`ensure_request_logs_partition` / `ensure_request_logs_bodies_partition`）。
//
// 退役了什么：这两条 v1 族生命周期条目（它们调用的是 890 已 DROP 的函数）。
// 为什么退役：旧闸（filterRetiredV1Lifecycle）的前提是「族还在但冻结」——
// 停写时维护一个冻结的族是浪费，回写时自动恢复维护。890 之后族已被物理删除，
// 前提消失 ⇒ 「恢复维护」没有对象可维护，而继续调用必然 42P01。
// ⇒ 条目**无条件移除**（不再随写开关出现/消失），避免「看起来还在管、其实管的是空气」。
// 现在由什么承担：会话族的分区预建由 `ensure_sessions_v2_partitions`（430，
// 一次覆盖 sessions / session_turns / session_bodies）与
// `ensure_session_family_partitions`（706，覆盖 memora/censors/tools）承担，
// 两者都在本清单里，未受本次改动影响。
//
// Migration 328a (2026-07-02) split the body columns out of
// _request_logs into request_logs_bodies. The bodies partition was
// paired with the metadata partition by (target_ts → month), so we
// used to ensure both at the same time. That pairing is gone with the
// v1 family itself (890).
func ensureSpecs() []archiveSpec {
	specs := []archiveSpec{
		{fnName: "ensure_request_wal_partition", label: "request_wal"},
		{fnName: "ensure_routing_decision_log_partition", label: "routing_decision_log"},
		{fnName: "ensure_credential_model_index_partition", label: "credential_model_index"},
		{fnName: "ensure_usage_ledger_partition", label: "usage_ledger"}, // Migration 330

		// ── 2026-08-07 审计补齐 ────────────────────────────────────────
		// 迁移 473 一次性补了 2026_09 + 2026_10 分区，但这些表从未接入
		// ensureSpecs()，到 2026-11-01 会再次因「no partition of relation
		// found for row」全面写入失败。函数本身早已随各自迁移安装，
		// 这里只是把它们接入 24h 定时轮转。
		//
		// timestamptz 签名（默认 argExpr="$1"）
		{fnName: "ensure_credit_ledger_partition", label: "credit_ledger"},       // Migration 334
		{fnName: "ensure_tool_usage_stats_partition", label: "tool_usage_stats"}, // Migration 335

		// date 签名 —— pgx 传 time.Time 为 timestamptz，需显式 ::date 转换，
		// 否则 PG 报 "function does not exist"。
		//
		// ensure_sessions_v2_partitions 一次调用同时覆盖 public.sessions /
		// session_turns / session_bodies 三张表（见 Migration 430）。这三张
		// 表是 V2 会话主链路的写入目标，缺分区等于聊天全挂。
		{fnName: "ensure_sessions_v2_partitions", label: "sessions_v2 (sessions/session_turns/session_bodies)", argExpr: "$1::date"}, // Migration 430
		{fnName: "ensure_session_module_executions_partition", label: "session_module_executions", argExpr: "$1::date"},              // Migration 382
		{fnName: "ensure_dashboard_events_partition", label: "dashboard_access_events", argExpr: "$1::date"},                         // Migration 383
		{fnName: "ensure_cache_metrics_partition", label: "cache_metrics", argExpr: "$1::date"},                                      // Migration 475
		{fnName: "ensure_handoff_logs_partition", label: "handoff_logs"},                                                             // Migration 532
		{fnName: "ensure_auto_route_selections_partition", label: "auto_route_selections", argExpr: "$1::date"},                      // Migration 656
		// 2026-09-05 (D-2#13): V371 的 ensure_supplier_errors_partition 是
		// timestamptz 签名（返回 text），走默认 argExpr="$1"。缺这条时
		// supplier_errors 下月分区不预建，promote 只能靠函数内自 ensure 兜底，
		// ensure 日志/可观测链路缺一张表。
		{fnName: "ensure_supplier_errors_partition", label: "supplier_errors"}, // Migration V371 (2026-09-05)
		// 2026-09-12 审计（694 跟进）：candidate_failure_logs 的 ensure 自
		// 689/694 起是真实 body（heap 分区 + Asia/Shanghai 边界钉扎），旧注释
		// 「空 body」已失真。不预建时，cfl 分区只能靠 promote 函数内按会话
		// 时区自 ensure——UTC 会话在每月 1 日 00:00–08:00+08 会把缝隙行路由
		// 到不存在的 +08 当月分区，整批 promote 永久失败、行滞留 hot 直至
		// 被 7d TTL trim 静默删除（473 同族复发面）。接入后本循环保证当前
		// 与次月分区始终先于 promote 存在。
		{fnName: "ensure_candidate_failure_logs_partition", label: "candidate_failure_logs"}, // Migration 689/694

		// 706（存储优化方案 v2 S1a）：session_memora/session_censors/
		// session_tools 三新表族。一次调用覆盖三表（706 函数体）。不预建时，
		// 三表月更后在下一 tick 全部写入失败（473 同族），故紧随 sessions_v2
		// 接入 24h ensure 循环。
		{fnName: "ensure_session_family_partitions", label: "session_family (memora/censors/tools)", argExpr: "$1::date"}, // Migration 706

		// model_probe_runs 已切换为纯 hot 表策略（2026-07-14），
		// 不再 promote 到 columnar 分区，所以也不需要 ensure。
		// {fnName: "ensure_model_probe_runs_partition", label: "model_probe_runs"}, // Migration 385 (retired)
		//
		// 以下一张表有意不接入：
		//   routing_decision_log_archive —— 仅 archive job（每月 1-3 日）写入，
		//     archive_routing_decision_log() 自建目标分区。
		// （candidate_failure_logs 已于 2026-09-12 接入，见上方 689/694 注。）

		// R68 (2026-09-26) migration 750：usage_facts 按日分区。DEFAULT 分区
		// 保留作历史 catch-all，新一日数据走日分区，partition pruning 对
		// WHERE 范围查询仅扫命中分区。partitionUnit="day" 让
		// ensureNextMonthPartitions 走 AddDate(0, 0, offset) 派生当日/次日，
		// 与月分区同源 Asia/Shanghai 日历钉扎（防 UTC 会话在 +08 0-8h 把
		// 次日误建为今日）。date 签名，与 sessions_v2_partitions 同款。
		{fnName: "ensure_usage_facts_daily_partition", label: "usage_facts (daily)", argExpr: "$1::date", partitionUnit: "day"},
		// 2026-10-04 (migration 830): ursm_node_snapshot_min 改为按日 RANGE
		// 分区。★ 该表**没有 DEFAULT 分区**（全仓无 snapshot_ts 单列读路径，
		// 有 DEFAULT 只会变成永远扫到的垃圾堆），所以「今天的分区不存在」
		// 不是降级而是**全量写入失败**（no partition of relation found）。
		// 因此这条接线与 750/706 同属可用性硬依赖，漏掉就是 473 同族的
		// 「迁移建了分区但忘了接 ensureSpecs」——务必与 830 一起进。
		//
		// 830 是手工迁移、刻意不进 installer 启动序列，所以本函数在 830
		// 执行前会一直不存在。ensureNextMonthPartitions 的错误处理是
		// slog.Error + continue（每 24h 记一条，不打断进程），与 db.go 的
		// boot ensure 容忍缺失一致 ⇒ 两个步骤的先后顺序无关。
		{fnName: "ensure_ursm_node_snapshot_min_daily_partition", label: "ursm_node_snapshot_min (daily)", argExpr: "$1::date", partitionUnit: "day", parentTable: "ursm_node_snapshot_min"}, // Migration 830
	}
	return specs
}

// archiveSpecs returns the archive_<table>() functions we invoke on
// day-1..3 of each month, paired with the day-of-month they run on.
//
// Migration 331 (2026-07-04): Removed archive_request_logs and archive_request_wal
// to simplify architecture. Archive tables had minimal storage footprint (~30 MB/partition)
// and no application code queried them. Old partitions are now dropped directly when
// no longer needed, or retained longer in main table if required.
//
// Remaining archive functions:
//   - routing_decision_log: lightweight archive
//   - credential_model_index: 7-day cutoff archive
//   - state tables (routing_decision_log, candidate_failure_logs, handoff_logs):
//     day-2 monthly DROP via drop_old_state_partitions (per-table retention)
func archiveSpecs() []archiveSpec {
	return []archiveSpec{
		{day: 1, fnName: "archive_routing_decision_log", label: "routing_decision_log"},
		{day: 2, fnName: "drop_old_state_partitions", label: "state_tables", scalarResult: true},
		{day: 3, fnName: "archive_credential_model_index", label: "credential_model_index"},
	}
}

// promoteSpecs lists every hot → monthly-partition migration function.
// Migrations 341, 343-347 (2026-07-05) unified all tables to hot table architecture.
// All promote functions now use *_hot_to_partition pattern.
//
// 2026-07-13: added candidate_failure_logs_hot (Migration 392).
// 2026-08-25: added session_module_executions_hot (Migration 580) and
//
//	dashboard_access_events_hot (Migration 579); both ship the
//	hot → monthly-partition drain that previously relied on
//	manually-run pg_cron archive_* scripts which drifted.
//
// Each function signature is promote_<table>_hot_to_partition(p_retention interval,
// p_batch_size int) RETURNS bigint; the caller loops until the function
// returns 0 (no more eligible cold rows for this table).
//
// 2026-10-20（迁移 890 真删族）：**v1 request_logs 族的两条 promote 条目已从本
// 清单移除**（`promote_request_logs_hot_to_partition` /
// `promote_request_logs_bodies_hot_to_partition`）。
//
// 退役了什么：这两条 v1 族生命周期条目（它们调用的是 890 已 DROP 的函数）。
// 为什么退役：旧闸（filterRetiredV1Lifecycle，已随之删除）的前提是「族还在但
// 冻结」⇒ 排空一个恒空的 hot 表是纯浪费，回写时又自动恢复维护。890 之后族已被
// 物理删除 ⇒ 前提消失 ⇒ 条目**无条件移除**；继续调用必然 42P01。
// 现在由什么承担：会话族的 hot 排水由 `promote_session_turns_hot_to_partition`
// （526）、`promote_session_turn_details_hot_to_partition`（733）、
// `promote_session_bodies_hot_to_partition`（614）承担，均在本清单内。
//
// ⚠ 位次变化：本清单去掉两条 v1 条目后，`promote_session_turn_details_hot_to_partition`
// 从第 11 位前移到**第 9 位**（下文 promoteDefaultToPartitions 的 R51 批预算注释
// 同步改了位次）。
func promoteSpecs() []archiveSpec {
	specs := []archiveSpec{
		{fnName: "promote_usage_ledger_hot_to_partition", label: "usage_ledger_hot"},
		{fnName: "promote_request_wal_hot_to_partition", label: "request_wal_hot"},
		{fnName: "promote_routing_decision_log_hot_to_partition", label: "routing_decision_log_hot"},
		{fnName: "promote_credential_model_index_hot_to_partition", label: "credential_model_index_hot"},
		{fnName: "promote_credit_ledger_hot_to_partition", label: "credit_ledger"},
		{fnName: "promote_tool_usage_stats_hot_to_partition", label: "tool_usage_stats"},
		// model_probe_runs_hot 已切换为纯 hot 表策略（2026-07-14），
		// 不再 promote 到 columnar 分区。hot 表数据通过 cleanupOldModelProbeRuns()
		// 按 lifecycle.model_probe_runs_ttl_days 直接 DELETE 清理。
		// {fnName: "promote_model_probe_runs_hot_to_partition", label: "model_probe_runs_hot"},
		{fnName: "promote_candidate_failure_logs_hot_to_partition", label: "candidate_failure_logs_hot"},       // Migration 392
		{fnName: "promote_session_turns_hot_to_partition", label: "session_turns_hot"},                         // Migration 526
		{fnName: "promote_session_turn_details_hot_to_partition", label: "session_turn_details_hot"},           // Migration 733
		{fnName: "promote_session_bodies_hot_to_partition", label: "session_bodies_hot"},                       // Migration 614
		{fnName: "promote_handoff_logs_hot_to_partition", label: "handoff_logs_hot"},                           // Migration 532
		{fnName: "promote_session_module_executions_hot_to_partition", label: "session_module_executions_hot"}, // Migration 580
		{fnName: "promote_dashboard_access_events_hot_to_partition", label: "dashboard_access_events_hot"},     // Migration 579
		{fnName: "promote_auto_route_selections_hot_to_partition", label: "auto_route_selections_hot"},         // Migration 656
		// 2026-09-05 (D-2#1): V371 建了 supplier_errors_hot 并在
		// admin/data_lifecycle_hot_partition.go 注册了手动 promote，
		// 但后台调度漏注册 → hot 表只有管理员手动迁移，8h 不变式断裂
		// 且错误明细无界增长。resolvePromoteConfig 走 default 8h 分支。
		{fnName: "promote_supplier_errors_hot_to_partition", label: "supplier_errors_hot"}, // Migration V371 (2026-09-05)
		// 706（存储优化方案 v2 S1a）：三新表族 hot → 月分区排水。
		{fnName: "promote_session_memora_hot_to_partition", label: "session_memora_hot"},   // Migration 706
		{fnName: "promote_session_censors_hot_to_partition", label: "session_censors_hot"}, // Migration 706
		{fnName: "promote_session_tools_hot_to_partition", label: "session_tools_hot"},     // Migration 706
	}
	return specs
}

// promoteDefaultToPartitions iterates promoteSpecs() and, for each,
// drains all cold rows from *_hot tables into the matching monthly
// partition by repeatedly calling promote_<table>_hot_to_partition with
// DefaultRetentionWindow and promoteBatchSize. Each iteration is one
// single-statement CTE inside PostgreSQL — atomic per batch.
//
// Retention and batch size come from settings.Global (hot-reloadable).
// model_probe_runs_hot no longer participates in this flow.
//
// The function always terminates: each batch either moves
// `promoteBatchSize` rows (caller loops) or 0 rows (caller breaks
// out). On error we log and move to the next table so one broken
// function does not starve the others.
//
// R51 (2026-09-21): the promoteCycleMaxBatches budget is shared across all
// tables, but each table is guaranteed at least one batch per cycle even
// after the budget is exhausted — otherwise late-ordered specs (e.g.
// session_turn_details_hot) starve behind large backlogged tables.
// promoteLockKey returns a stable advisory-lock key for a hot-table label.
// Both gateway instances (e.g. 245 + 154) share the same PostgreSQL, so two
// concurrent hourly promote cycles can race the same *_hot table and one
// ends up blocked into PG statement_timeout. pg_try_advisory_xact_lock on
// this key makes the loser skip the table for that tick instead of waiting.
// The key is derived with FNV-1a so it is identical across instances.
func promoteLockKey(label string) int64 {
	const prefix = "llm-gateway:promote:"
	h := fnv.New64a()
	h.Write([]byte(prefix + label))
	return int64(h.Sum64())
}

// analyzeLockKey returns the advisory-lock key that serializes the whole
// analyze pass. Same reasoning as promoteLockKey — the peer gateway shares
// this PostgreSQL — but the granularity is the whole pass rather than one
// table, because analyze_llm_gateway_table_stats ANALYZEs a fixed set of
// tables inside a single transaction.
//
// Deliberately a DIFFERENT key namespace from promoteLockKey: the two paths
// are independent jobs, and reusing a key would make a promote lock make
// analyze skip (and vice versa) for reasons that have nothing to do with
// each other.
func analyzeLockKey() int64 {
	const prefix = "llm-gateway:analyze:"
	h := fnv.New64a()
	h.Write([]byte(prefix))
	return int64(h.Sum64())
}

// analyzeThrottleTaskName is the row key in public.llm_gateway_task_state that
// both gateway instances contend for (migration 840).
//
// ★ 为什么需要它而不是 §10.53 那把 advisory 锁：实测（runbook §10.107.6）那把锁
// 一次都没拦住 —— 2026-10-07 今日 154 跑 15 趟 / 245 跑 16 趟，**0 次跳过**，
// 2 次/小时原封不动。
//
// ⚠️ 「锁不生效」的**成因目前仍未解释**，不要照抄旧结论：
// · 旧结论说「两台 tick 偏移 6 分 01 秒 ≫ 持有时长 ⇒ 永不争用」——**该数字是错的**。
// 稳态逐小时实测偏移只有约 40 秒（−52s ~ +36s 来回摆），不是 6 分钟。
// · 已排除：两台不是不同库（DSN 同为 172.16.2.210:5432/llm_gateway）；
// 锁键也不含实例身份（analyzeLockKey() 是常量 fnv64a("llm-gateway:analyze:")）。
// · 未排除两种可能：「单趟其实 < 40s 所以两趟不重叠」与「重叠了但锁没拦住」。
// 本函数只记 "tables" 不记耗时，历史读数无法回溯 ⇒ 需 2s 采样
// pg_stat_activity 判定，采样前不下结论。
//
// ⇒ 无论成因是哪一种，**跨实例共享的节流槽都是必需的**：它把「每小时跑两遍」压成
// 「每小时跑一遍」，不依赖任何时序侥幸。两者正交、都留着：
// 锁防「同时」，槽防「重复」。
const analyzeThrottleTaskName = "analyze_llm_gateway_table_stats"

// analyzeThrottleMinInterval is the throttle window. The goal is ONE pass per
// hour, not "as often as possible": after migration 838/839 the per-table
// statistics burden belongs to autovacuum (runbook §10.105), so this manual
// pass does not need to be frequent. 50 minutes leaves 10 minutes of slack to
// absorb clock drift and the single pass (838 落地后实测 51.3 秒，runbook §10.107.6；
// 之前普遍是 94 秒，故这里的余量按更长的那个值留）。
const analyzeThrottleMinInterval = 50 * time.Minute

// isUndefinedTable reports whether err is a Postgres 42P01 (undefined_table).
//
// Used so the analyze path can DEGRADE rather than fail when migration 840 has
// not been applied yet: without the throttle table the behaviour is exactly
// what it was before (both instances run), which is the documented rollback
// behaviour of 840's .down.sql.
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func (pm *PartitionManager) promoteDefaultToPartitions(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	pm.mu.Lock()
	promoteInterval := pm.promoteInterval
	pm.mu.Unlock()
	if promoteInterval <= 0 {
		// Disabled via SetPromoteInterval(0) — used by tests.
		return
	}
	cycleCtx, cycleCancel := context.WithTimeout(ctx, promoteCycleTimeout)
	defer cycleCancel()
	// R51 (2026-09-21)：batches 是全周期共享预算，排位靠后的表（如
	// session_turn_details_hot，promoteSpecs 第 9 位——890 退役掉两条 v1 条目后
	// 由第 11 位前移）在前序大表积压耗尽
	// 预算后整个周期颗粒无收。改为每表保底至少 1 批：预算耗尽后，尚未跑过
	// 批的表仍允许再跑一批才让位；外层循环因此遍历全部 spec，不再提前 break。
	batches := 0
	for _, s := range promoteSpecs() {
		retention, batchSize := resolvePromoteConfig(s.label)
		lockKey := promoteLockKey(s.label)
		tableRanBatch := false
		for {
			if cycleCtx.Err() != nil || (batches >= promoteCycleMaxBatches && tableRanBatch) {
				break
			}
			batchStart := time.Now() // 2026-08-29 P2: track duration
			timeoutCtx, cancel := context.WithTimeout(cycleCtx, 60*time.Second)
			tx, err := pm.db.Begin(timeoutCtx)
			if err != nil {
				cancel()
				recordPromoteDuration(s.label, time.Since(batchStart).Seconds())
				slog.Error("partition_manager: promote begin failed",
					"label", s.label, "error", err)
				recordPromoteFailure(s.label) // 2026-08-29 P2: record failure
				break
			}
			// Serialize against the peer gateway's promote on the same
			// shared table. pg_try_advisory_xact_lock never blocks: if the
			// other instance holds the key, skip this table for this tick
			// instead of waiting into PG statement_timeout=30s.
			var locked bool
			if err := tx.QueryRow(timeoutCtx,
				"SELECT pg_try_advisory_xact_lock($1)", lockKey,
			).Scan(&locked); err != nil {
				tx.Rollback(timeoutCtx)
				cancel()
				recordPromoteDuration(s.label, time.Since(batchStart).Seconds())
				slog.Error("partition_manager: promote lock failed",
					"label", s.label, "error", err)
				recordPromoteFailure(s.label) // 2026-08-29 P2: record failure
				break
			}
			if !locked {
				tx.Rollback(timeoutCtx)
				cancel()
				recordPromoteDuration(s.label, time.Since(batchStart).Seconds())
				slog.Debug("partition_manager: promote skipped (peer holds lock)",
					"label", s.label)
				recordPromoteSkipped(s.label)       // 2026-08-29 P2: record skip
				incPromoteZombieLockStreak(s.label) // 2026-08-31 P2-8: zombie-lock observability
				break
			}
			// 2026-08-31 (P2-8): we just acquired the lock for this table, so
			// any previously-recorded zombie-lock streak is cleared. This
			// keeps the gauge semantically a "consecutive-skip" counter,
			// not a lifetime counter.
			resetPromoteZombieLockStreak(s.label)
			// 2026-09-14 审计 #6：promote 以应用角色（非 superuser）在 FORCE
			// RLS 的 supplier_errors / candidate_failure_logs 分区父表上搬行
			// （V367/V371 policy：tenant_id 匹配或 app.bypass_rls）。缺旁路时
			// promote 函数静默迁 0 行，hot 表 8h 不变式与错误数据闭环一起断。
			// is_local=true 把旁路限制在本事务，pooled 连接不保留提权。
			if _, err := tx.Exec(timeoutCtx,
				"SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
				tx.Rollback(timeoutCtx)
				cancel()
				recordPromoteDuration(s.label, time.Since(batchStart).Seconds())
				slog.Error("partition_manager: promote RLS setup failed",
					"label", s.label, "error", err)
				recordPromoteFailure(s.label)
				break
			}
			// 2026-09-22 252 SQL 日志审计轮：角色级 statement_timeout=30s
			// （252 生产 llm_gateway rolconfig）会击杀超过 30s 的单批
			// promote——而本函数的 Go 侧批次预算是 60s。大批次（5000 行跨
			// 分区搬移实测 26.5s 贴线、>30s 即被 PG 杀整批回滚，下个 tick
			// 重试同一批形成活锁，252 日志 21 分钟窗口 1 次实锤）。
			// SET LOCAL 抬到与 Go 预算一致，事务结束自动还原，pooled
			// 连接不保留。
			if _, err := tx.Exec(timeoutCtx,
				"SET LOCAL statement_timeout = '60s'"); err != nil {
				tx.Rollback(timeoutCtx)
				cancel()
				recordPromoteDuration(s.label, time.Since(batchStart).Seconds())
				slog.Error("partition_manager: promote timeout setup failed",
					"label", s.label, "error", err)
				recordPromoteFailure(s.label)
				break
			}
			var n int64
			err = tx.QueryRow(timeoutCtx,
				"SELECT "+s.fnName+"($1::interval, $2::int)",
				retention, batchSize,
			).Scan(&n)
			commitErr := tx.Commit(timeoutCtx) // releases the xact lock
			cancel()
			batchDuration := time.Since(batchStart).Seconds() // 2026-08-29 P2
			if err != nil {
				slog.Error("partition_manager: promote failed",
					"fn", s.fnName, "label", s.label,
					"retention", retention, "batch_size", batchSize,
					"error", err)
				recordPromoteDuration(s.label, batchDuration)
				recordPromoteFailure(s.label) // 2026-08-29 P2: record failure
				break                         // move on to the next table
			}
			if commitErr != nil {
				slog.Error("partition_manager: promote commit failed",
					"label", s.label, "error", commitErr)
				recordPromoteDuration(s.label, batchDuration)
				recordPromoteFailure(s.label) // 2026-08-29 P2: record failure
				break
			}
			// Record every completed attempt, including an empty batch.
			recordPromoteDuration(s.label, batchDuration)
			if n == 0 {
				slog.Debug("partition_manager: promote done",
					"label", s.label,
					"retention", retention)
				break
			}
			batches++
			tableRanBatch = true
			// 2026-08-29 P2: record successful batch
			recordPromoteBatch(s.label, n)
			slog.Info("partition_manager: promote batch",
				"label", s.label, "rows", n)
		}
		// R47（存储演进批，R46 §五#8）：排水结束后记录 hot 剩余行数水位。
		// 持续高于「批量×周期」= promote 吞吐跟不上写入；查错只 Warn 不阻断。
		recordHotTableBacklog(s.label, pm.hotTableBacklogRows(ctx, s.label))
		// R48 §五#3：oldest-row-age 与 backlog 互补——观测 sessions 族 TTL 裁决
		// 前置门禁。失败时 -1 保持 last gauge value，不发噪。
		if age := pm.hotTableOldestRowAge(ctx, s.label); age >= 0 {
			recordHotTableOldestRowAge(s.label, age)
		}
	}
	pm.analyzePartitionStats(ctx)
	// 841（2026-10-07）：月度分区族的 DROP 保留清扫。
	// 放在 promote cycle 尾部 ⇒ 与「保证当月分区存在」同一条 tick，
	// 但**独立降级**：841 未应用 / 配置表为空 ⇒ 一个分区都不会被删。
	pm.dropExpiredMonthlyPartitions(ctx)
}

// hotTableBacklogRows counts remaining rows in a spec's hot table. The
// physical hot table is <label> (already suffixed) or <label>_hot for the
// three labels that name the partition family instead (credit_ledger,
// _request_logs_bodies, tool_usage_stats). Query errors return -1 (gauge
// keeps last value; caller Warns) — never blocks the promote cycle.
func (pm *PartitionManager) hotTableBacklogRows(ctx context.Context, label string) int64 {
	table := label
	if !strings.HasSuffix(table, "_hot") {
		table += "_hot"
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var n int64
	if err := pm.db.QueryRow(timeoutCtx,
		"SELECT COUNT(*) FROM "+table,
	).Scan(&n); err != nil {
		slog.Warn("partition_manager: hot backlog count failed",
			"label", label, "table", table, "error", err)
		return -1
	}
	return n
}

// hotTableOldestRowAge (R48 §五#3)：查询指定 hot 表最旧一行的 age（秒）。
// 表名解析逻辑与 hotTableBacklogRows 一致（自动追加 _hot 后缀）；
// 时间戳列名通过 hotTableTSColumn(label) 决定（默认 ts，部分表为 created_at）。
//
// 返回：
//
//	0   = 表为空（MIN 返回 NULL → caller 写 0 进 gauge，符合 "空表=0" 语义）
//	> 0 = 最旧行距今的秒数
//	-1  = 查询失败（slog.Warn + 保持 last gauge value）
func (pm *PartitionManager) hotTableOldestRowAge(ctx context.Context, label string) float64 {
	table := label
	if !strings.HasSuffix(table, "_hot") {
		table += "_hot"
	}
	tsCol := hotTableTSColumn(label)
	// label/tsCol 均来自固定 switch + promoteSpecs，无用户输入；白名单校验
	// 防止有人修改 switch 后误注入 SQL。（2026-09-23 审计轮：补 bucket /
	// occurred_at——credential_model_index_hot 与 supplier_errors_hot 的
	// 时间列不走 ts/created_at。）
	allowed := map[string]bool{"ts": true, "created_at": true, "bucket": true, "occurred_at": true}
	if !allowed[tsCol] {
		slog.Error("partition_manager: hot ts column rejected", "label", label, "ts_col", tsCol)
		return -1
	}
	query := "SELECT EXTRACT(EPOCH FROM (now() - MIN(" + tsCol + ")))::bigint FROM " + table
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ageSec *float64
	if err := pm.db.QueryRow(timeoutCtx, query).Scan(&ageSec); err != nil {
		slog.Warn("partition_manager: hot oldest age query failed",
			"label", label, "table", table, "ts_col", tsCol, "error", err)
		return -1
	}
	if ageSec == nil {
		return 0
	}
	return *ageSec
}

// analyzePartitionStats refreshes planner stats on hot heap tables and recent
// monthly partitions. Columnar partitions after bulk promote often keep
// n_mod_since_analyze=0 so autovacuum analyze never fires.
func (pm *PartitionManager) analyzePartitionStats(ctx context.Context) {
	// 2026-07-20: 5 min cooldown. analyze 一次 3.3s 频繁跑会拖慢 commit.
	pm.mu.Lock()
	if !pm.lastAnalyzeAt.IsZero() && time.Since(pm.lastAnalyzeAt) < 5*time.Minute {
		pm.mu.Unlock()
		return
	}
	pm.lastAnalyzeAt = time.Now()
	pm.mu.Unlock()

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// 2026-09-23 252 SQL 日志审计轮：analyze 全实例逐表 ANALYZE 实测 >30s，
	// 被角色级 statement_timeout=30s（252 llm_gateway rolconfig）击杀
	// （252 快照 74min ×4），GUC 注释里的 5min cooldown 形同虚设——analyze
	// 每次都半途而废。Go 侧预算 10min，事务内 SET LOCAL 抬到同值对齐，
	// 事务结束自动还原，pooled 连接不保留（与 promote 的 60s 同型）。
	tx, err := pm.db.Begin(timeoutCtx)
	if err != nil {
		slog.Warn("partition_manager: analyze stats begin failed", "error", err)
		return
	}
	if _, err := tx.Exec(timeoutCtx,
		"SET LOCAL statement_timeout = '10min'"); err != nil {
		tx.Rollback(timeoutCtx)
		slog.Warn("partition_manager: analyze timeout setup failed", "error", err)
		return
	}
	// 2026-10-06（审计 §10.53）：analyze 此前**没有任何互斥**。
	// promote 路径有 promoteLockKey + pg_try_advisory_xact_lock，其注释明确
	// 「两台实例共享同一个 PG」，抢不到就跳过该表；而 analyze 是一次性
	// ANALYZE 整组 44 张表，没有对应的锁 ⇒ 两台各跑一遍全量。
	// 实测窗口 06:00:53~12:51:04：154 日志 7 次 + 245 journalctl 7 次
	// = 14 次 = pg_stat_statements Δcalls，且两次落在同一分钟内互争 I/O。
	// 这里沿用同一把锁的同型写法：抢不到就跳过本轮，交给对端做。
	var locked bool
	if err := tx.QueryRow(timeoutCtx,
		"SELECT pg_try_advisory_xact_lock($1)", analyzeLockKey(),
	).Scan(&locked); err != nil {
		tx.Rollback(timeoutCtx)
		slog.Warn("partition_manager: analyze lock failed", "error", err)
		return
	}
	if !locked {
		tx.Rollback(timeoutCtx)
		slog.Debug("partition_manager: analyze skipped (peer holds lock)")
		return
	}

	// 2026-10-07（runbook §10.107/§10.108，迁移 840）：节流槽。
	//
	// ★ 上面那把锁一次都不会命中（xact 级、只活 93.81 秒，而两台 tick 偏移
	//   6 分 01 秒）⇒ 「两台各跑一遍」原封不动。这张共享表才是真正的节流：
	//   claim_* 的 `INSERT … ON CONFLICT DO UPDATE … WHERE … RETURNING`
	//   是单语句原子操作，两个实例并发时只有一个能拿到 true。
	//
	// ★ 缺表（迁移 840 未应用）时**降级为照常执行**，而不是跳过或报错：
	//   这正是 840 的 .down.sql 回滚后的目标行为。
	var slotClaimed bool
	if err := tx.QueryRow(timeoutCtx,
		"SELECT claim_llm_gateway_task_slot($1, $2::interval)",
		analyzeThrottleTaskName, analyzeThrottleMinInterval.String(),
	).Scan(&slotClaimed); err != nil {
		if isUndefinedTable(err) {
			slog.Warn("partition_manager: analyze throttle table missing (migration 840 not applied?) — running unthrottled")
			slotClaimed = true
		} else {
			tx.Rollback(timeoutCtx)
			slog.Warn("partition_manager: analyze slot claim failed", "error", err)
			return
		}
	}
	if !slotClaimed {
		tx.Rollback(timeoutCtx)
		// ★ 用 Info 而不是 Debug：数「实际执行次数」时看不到跳过会让人误判
		//   「没跑」与「被跳过」是一回事（runbook §10.107.1）。
		slog.Info("partition_manager: analyze skipped (peer holds shared throttle slot)",
			"task", analyzeThrottleTaskName, "min_interval", analyzeThrottleMinInterval.String())
		return
	}

	// 2026-10-11（runbook §10.106.29）：整趟计时。此前这条路径既不记日志耗时
	// 也没有 Prometheus 指标（metrics.go 里 analyze 相关一条都没有），
	// 而它受 Go 侧 10min 预算 + SET LOCAL statement_timeout='10min' 双重约束
	// —— 没有读数就无法判断是否在逼近预算。
	//
	// ★ 计时起点选在**认领节流槽之后**：跳过路径在认领失败时就 return 了，
	// 根本走不到这里，所以这条序列天然只含**真实执行**的趟次。
	analyzeStart := time.Now()
	var n int64
	err = tx.QueryRow(timeoutCtx,
		"SELECT analyze_llm_gateway_table_stats($1)", 2,
	).Scan(&n)
	analyzeSeconds := time.Since(analyzeStart).Seconds()
	// 成功与失败都记：失败趟恰恰是最需要被看见的（逼近/撞上预算的那种），
	// 只记成功等于把最该告警的那批样本丢掉。
	defer recordAnalyzeDuration(analyzeSeconds)
	if cerr := tx.Commit(timeoutCtx); cerr != nil {
		slog.Warn("partition_manager: analyze commit failed",
			"error", cerr, "duration_s", analyzeSeconds)
		return
	}
	if err != nil {
		slog.Warn("partition_manager: analyze stats failed",
			"error", err, "duration_s", analyzeSeconds)
		return
	}

	// 记录完成时刻。**必须在 commit 之后**——放在同一事务里会被上面的
	// commit 一起处理，而 claim 已经把 last_started_at 写掉了，
	// 所以即使这里失败，槽位也会在 min_interval 后自然过期。
	if _, cerr := pm.db.Exec(timeoutCtx,
		"SELECT complete_llm_gateway_task_slot($1)", analyzeThrottleTaskName); cerr != nil {
		slog.Warn("partition_manager: analyze slot completion failed (non-fatal)", "error", cerr)
	}

	if n > 0 {
		slog.Info("partition_manager: analyze stats", "tables", n, "duration_s", analyzeSeconds)
	}
}

// dropExpiredMonthlyPartitions sweeps monthly partitions that are past the
// retention configured in public.llm_gateway_partition_retention (migration
// 841). DROP, never DELETE — measured dead-tuple ratios on these families are
// ~0%, so DELETE reclaims nothing (runbook §10.106.13).
//
// ★ The config table ships EMPTY, so this is a no-op until someone INSERTs a
// per-family retain_months row. That keeps the mechanism deployable without
// baking in a retention policy, which is a business decision.
// ★ Missing table/function (841 not applied, or after .down.sql) degrades to
// "do nothing" for the same reason 840's slot claim does.
func (pm *PartitionManager) dropExpiredMonthlyPartitions(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var dropped []string
	err := pm.db.QueryRow(timeoutCtx,
		"SELECT llm_gateway_drop_expired_month_partitions()",
	).Scan(&dropped)
	if err != nil {
		if isUndefinedTable(err) || isUndefinedFunction(err) {
			slog.Info("partition_manager: partition retention table missing (migration 841 not applied?) — skipping sweep")
			return
		}
		slog.Warn("partition_manager: partition retention sweep failed (non-fatal)", "error", err)
		return
	}
	// ★ Info, not Debug: "expired partitions existed but were not dropped" and
	// "nothing was expired" must both be visible when auditing (§10.107.1).
	slog.Info("partition_manager: monthly partition retention sweep",
		"dropped", len(dropped), "partitions", dropped)
}

// isUndefinedFunction reports whether err is a Postgres 42883
// (undefined_function). 841 can be rolled back while the table survives
// (or vice versa), so the sweep must handle both codes independently.
func isUndefinedFunction(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42883"
}

// resolvePromoteConfig returns the retention interval and batch size for
// a given hot table. Reads from settings.Global with hot-reload support
// (every call reads the latest value from settings_kv). Falls back to
// DefaultRetentionWindow / promoteBatchSize for tables without a
// per-table setting.
//
// This function is called on every promote tick — there is no caching
// layer to invalidate. Updated settings take effect within one tick
// (DefaultPromoteInterval = 1h).
//
// Per-table retention settings (all hot-reloadable):
//   - lifecycle.hot_retention_hours            — _request_logs_hot, usage_ledger_hot, ... (default 8h)
//   - lifecycle.handoff_logs_hot_retention_hours — handoff_logs_hot (8h default)
//   - lifecycle.session_module_executions_hot_retention_hours — Migration 580 (8h default)
//   - lifecycle.dashboard_access_events_hot_retention_hours — Migration 579 (8h default)
//   - lifecycle.credential_model_index_ttl_days  — credential_model_index (reaped by
//     cleanup_old_credential_model_index())
func resolvePromoteConfig(label string) (time.Duration, int) {
	switch label {
	case "handoff_logs_hot":
		hours := settingsGetPlatformInt("lifecycle.handoff_logs_hot_retention_hours", 8)
		retention := time.Duration(hours) * time.Hour
		if retention < time.Hour {
			retention = time.Hour
		}
		batchSize := settingsGetPlatformInt("lifecycle.promote_batch_size", promoteBatchSize)
		if batchSize < 100 {
			batchSize = 100
		}
		if batchSize > 50_000 {
			batchSize = 50_000
		}
		return retention, batchSize
	case "session_module_executions_hot", "dashboard_access_events_hot":
		// Migrations 580/579 pair these hot tables with PartitionManager
		// and seed lifecycle.<label>_retention_hours = 8. (579's function
		// body shipped with a wrong column projection; migration 607
		// re-installs the corrected body.)
		hours := settingsGetPlatformInt("lifecycle."+label+"_retention_hours", 8)
		retention := time.Duration(hours) * time.Hour
		if retention < time.Hour {
			retention = time.Hour
		}
		batchSize := settingsGetPlatformInt("lifecycle.promote_batch_size", promoteBatchSize)
		if batchSize < 100 {
			batchSize = 100
		}
		if batchSize > 50_000 {
			batchSize = 50_000
		}
		return retention, batchSize
	case "auto_route_selections_hot":
		// 2026-09-05 (audit H-3): the settle worker needs settleDelay (2min)
		// + settleAbandonAfter (4h) to finish before promote drains hot, and
		// it only ever touches auto_route_selections_hot (never the parent).
		// Promoting earlier would strand unsettled rows in the columnar
		// parent forever (reward lost, affinity sample lost). The generic
		// 1h floor is therefore not enough for this table — clamp to 5h
		// (4h abandon window + 2min settle delay + margin) and warn when a
		// smaller lifecycle.hot_retention_hours is configured.
		const autoRouteMinRetention = 5 * time.Hour
		hours := settingsGetPlatformInt("lifecycle.hot_retention_hours", int(DefaultRetentionWindow.Hours()))
		retention := time.Duration(hours) * time.Hour
		if retention < autoRouteMinRetention {
			slog.Warn("partition_manager: auto_route_selections_hot retention below settle window, clamped",
				"configured", retention.String(),
				"clamped_to", autoRouteMinRetention.String(),
				"reason", "settleDelay(2m)+settleAbandonAfter(4h) must finish before promote")
			retention = autoRouteMinRetention
		}
		batchSize := settingsGetPlatformInt("lifecycle.promote_batch_size", promoteBatchSize)
		if batchSize < 100 {
			batchSize = 100
		}
		if batchSize > 50_000 {
			batchSize = 50_000
		}
		return retention, batchSize
	default:
		hours := settingsGetPlatformInt("lifecycle.hot_retention_hours", int(DefaultRetentionWindow.Hours()))
		retention := time.Duration(hours) * time.Hour
		if retention < time.Hour {
			retention = time.Hour
		}
		batchSize := settingsGetPlatformInt("lifecycle.promote_batch_size", promoteBatchSize)
		if batchSize < 100 {
			batchSize = 100
		}
		if batchSize > 50_000 {
			batchSize = 50_000
		}
		return retention, batchSize
	}
}

// cleanupOldProviderErrorDetails deletes resolved error aggregations from
// provider_error_details older than the configured TTL. Unresolved errors
// (resolved=false) are retained indefinitely for operational visibility.
// This prevents the aggregation table from growing unboundedly while
// preserving active error signals.
//
// Retention: lifecycle.provider_error_details_ttl_days (default 30, hot-reloadable).
// Backed by idx_ped_resolved_updated_at (partial index on resolved=true,
// keyed by updated_at).
//
// 2026-08-29 P2: Added to prevent unbounded growth of the error aggregation
// table after the聚合器 was implemented in migration 616.
func (pm *PartitionManager) cleanupOldProviderErrorDetails(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	retentionDays := settings.GetPlatformInt("lifecycle.provider_error_details_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30 // safety floor — never set to 0
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Only delete resolved errors older than TTL.
	// Unresolved errors are kept indefinitely for operational visibility.
	//
	// RLS Phase 2 适配 (R41 P1-3): 跨租户 TTL 清理必须显式事务 +
	// super_admin 旁路 GUC（provider_error_details policy 含
	// `current_role=super_admin OR bypass_rls=true` 分支），否则降权后
	// 静默 0 行（真库实测 22,489+1('chenb') 行滞留）。
	tag, err := pm.runWithBypass(timeoutCtx, func(tx pgx.Tx) (pgconn.CommandTag, error) {
		return tx.Exec(timeoutCtx,
			`DELETE FROM provider_error_details
			 WHERE resolved = true
			 AND updated_at < now() - ($1 || ' days')::interval`,
			retentionDays)
	})
	if err != nil {
		slog.Error("partition_manager: provider_error_details cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned provider_error_details",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldSupplierErrorStats deletes minute-bucket pre-aggregations from
// supplier_error_stats older than the configured TTL. The table has no
// partition strategy and the aggregator upserts one row per minute per
// (supplier × credential × error_type × model) combination, so without
// this it grows linearly forever — and it is the sole read source for the
// /api/errors/trend endpoint (audit 2026-09-05 D-2#5 / E-#8).
//
// Only granularity='minute' rows are deleted; hour/day rollups are far
// smaller and are kept for long-range trend views (24h view reads hour,
// 168h view reads day). The (stat_time DESC, granularity) index from V371
// backs the DELETE as an index descent.
//
// Retention: lifecycle.supplier_error_stats_ttl_days (default 30,
// hot-reloadable via settings_kv — same pattern as
// lifecycle.provider_error_details_ttl_days; no config struct change).
func (pm *PartitionManager) cleanupOldSupplierErrorStats(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	retentionDays := settingsGetPlatformInt("lifecycle.supplier_error_stats_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30 // safety floor — never set to 0 (would wipe minute history)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		`DELETE FROM supplier_error_stats
		 WHERE granularity = 'minute'
		   AND stat_time < now() - ($1 || ' days')::interval`,
		retentionDays)
	if err != nil {
		slog.Error("partition_manager: supplier_error_stats cleanup failed",
			"retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned supplier_error_stats minute buckets",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRoutingFeedbackLog deletes rows from routing_feedback_log older
// than the configured TTL. The table takes one INSERT per auto-route
// decision (routingopt feedback hook, migration 670) but is a plain heap —
// no hot+partition promote, no other pruning — so without this it grows
// linearly with decision volume and drags its five indexes along
// (2026-09-07 audit P1). Until the table is migrated to the hot+partition
// architecture this daily TTL is the growth bound.
//
// Retention: lifecycle.routing_feedback_log_ttl_days (default 14; the
// learner only needs recent training signal).
func (pm *PartitionManager) cleanupOldRoutingFeedbackLog(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	retentionDays := settingsGetPlatformInt("lifecycle.routing_feedback_log_ttl_days", 14)
	if retentionDays < 1 {
		retentionDays = 14 // safety floor — never set to 0
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		`DELETE FROM routing_feedback_log
		 WHERE created_at < now() - ($1 || ' days')::interval`,
		retentionDays)
	if err != nil {
		// 42P01 (table missing, e.g. migration 670 not applied yet) is the
		// expected steady state on deployments without the optimizer; keep
		// it out of the error log to avoid a per-tick flood.
		slog.Debug("partition_manager: routing_feedback_log cleanup skipped",
			"error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned routing_feedback_log",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// cleanupOldRoutingOptimizationMetrics deletes expired 5-minute metric
// buckets from routing_optimization_metrics (migration 670). Insert rate is
// low (one row per time_bucket × task_type × provider upsert) but the table
// has no partition strategy and no other pruning.
//
// Retention: lifecycle.routing_optimization_metrics_ttl_days (default 30).
func (pm *PartitionManager) cleanupOldRoutingOptimizationMetrics(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	retentionDays := settingsGetPlatformInt("lifecycle.routing_optimization_metrics_ttl_days", 30)
	if retentionDays < 1 {
		retentionDays = 30 // safety floor
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pm.db.Exec(timeoutCtx,
		`DELETE FROM routing_optimization_metrics
		 WHERE time_bucket < now() - ($1 || ' days')::interval`,
		retentionDays)
	if err != nil {
		slog.Debug("partition_manager: routing_optimization_metrics cleanup skipped",
			"error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned routing_optimization_metrics",
			"deleted_rows", n, "retention_days", retentionDays)
	}
}

// runWithBypass wraps a write in a tx with super_admin/bypass RLS GUCs set.
// RLS Phase 2 适配 (R41 P1-3/P1-5): 跨租户清理 / 状态转移路径在
// NOSUPERUSER owner 下必须显式事务 + 旁路 GUC，否则 policy 触发 0 行
// 静默失败。事务语义与 admin/tenant_ctx.go setAllTenantGUC 同源。
func (pm *PartitionManager) runWithBypass(ctx context.Context, fn func(pgx.Tx) (pgconn.CommandTag, error)) (pgconn.CommandTag, error) {
	var zero pgconn.CommandTag
	if pm == nil || pm.db == nil {
		return zero, fmt.Errorf("partition manager not initialized")
	}
	tx, err := pm.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true)`); err != nil {
		return zero, fmt.Errorf("set super_admin role: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
		return zero, fmt.Errorf("set bypass_rls: %w", err)
	}
	tag, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit: %w", err)
	}
	return tag, nil
}

// deleteTerminalRowsBatched deletes at most limit rows matching predicate in
// bounded ctid batches (single statement, atomic visibility — the same shape
// the turn-logs fix landed in the 15th 12h audit round). Returns rows
// deleted. R72 lesson: an unbounded first-sweep DELETE on a million-row
// table stalls replication and bloats the WAL in one shot.
func (pm *PartitionManager) deleteTerminalRowsBatched(ctx context.Context, table, predicate string, retentionDays, limit int) int64 {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag, err := pm.db.Exec(timeoutCtx,
		// (tableoid, ctid) 复合键：table 可能是分区父表（stats_event_inbox），
		// 裸 ctid 只在分区内唯一，多分区时跨分区同位行会被误删——与 e92eaebab
		// 修掉的 candidate_failure_logs P1 同型（三十七轮审计闭合）。
		"DELETE FROM "+table+" WHERE (tableoid, ctid) IN (SELECT tableoid, ctid FROM "+table+" WHERE "+predicate+" LIMIT $2)",
		retentionDays, limit)
	if err != nil {
		slog.Error("partition_manager: terminal-row TTL batch delete failed",
			"table", table, "retention_days", retentionDays, "error", err)
		return 0
	}
	return tag.RowsAffected()
}

// cleanupOldAnalysisEvents deletes PROCESSED analysis_events rows older than
// the TTL (R27-HC-1, 2026-09-30): the table previously had zero DELETE
// paths and grew to 1.02M rows / 864MB on the local shard, all already
// consumed (processed_at set). Unprocessed rows are never touched — they are
// pending work for pg_poll. Retention:
// lifecycle.analysis_events_ttl_days (default 7, hot-reloadable). Backed by
// idx_analysis_events_processed_old (migration 760).
func (pm *PartitionManager) cleanupOldAnalysisEvents(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.analysis_events_ttl_days", 7)
	if retentionDays < 1 {
		retentionDays = 7
	}
	const batchLimit = 20000
	total := int64(0)
	for round := 0; round < 10; round++ {
		n := pm.deleteTerminalRowsBatched(ctx, "analysis_events",
			"processed_at IS NOT NULL AND occurred_at < now() - ($1 || ' days')::interval",
			retentionDays, batchLimit)
		total += n
		if n < batchLimit {
			break
		}
	}
	if total > 0 {
		slog.Info("partition_manager: cleaned analysis_events processed rows",
			"deleted_rows", total, "retention_days", retentionDays)
	}
}

// cleanupOldStatsEventInboxTerminal deletes TERMINAL inbox rows (processing
// status processed / dead_letter) older than the TTL (R27-HC-2, 2026-09-30):
// the inbox previously only transitioned state — the local shard's default
// partition reached 1.38M rows. Active rows (pending / retryable /
// processing) are never touched, including the currently-stuck pending
// backlog (R27-HC-10, owner decision required). Retention:
// lifecycle.stats_event_inbox_ttl_days (default 7, hot-reloadable). Backed
// by idx_stats_event_inbox_terminal_old (migration 760). stats_event_inbox
// is a partitioned parent; DELETE routes to partitions automatically.
func (pm *PartitionManager) cleanupOldStatsEventInboxTerminal(ctx context.Context) {
	retentionDays := settings.GetPlatformInt("lifecycle.stats_event_inbox_ttl_days", 7)
	if retentionDays < 1 {
		retentionDays = 7
	}
	const batchLimit = 20000
	total := int64(0)
	for round := 0; round < 10; round++ {
		n := pm.deleteTerminalRowsBatched(ctx, "stats_event_inbox",
			"processing_status IN ('processed','dead_letter') AND occurred_at < now() - ($1 || ' days')::interval",
			retentionDays, batchLimit)
		total += n
		if n < batchLimit {
			break
		}
	}
	if total > 0 {
		slog.Info("partition_manager: cleaned stats_event_inbox terminal rows",
			"deleted_rows", total, "retention_days", retentionDays)
	}
}
