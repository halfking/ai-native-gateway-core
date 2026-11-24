package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// partition_analyze_settings.go —— 会话族月分区的 autovacuum analyze 设置巡检。
//
// # 为什么需要它（211 生产实证，2026-11-25 request-logs 超时根因的另一半）
//
// public.session_turns / session_turn_details 按月分区。日志列表永远查「最近的
// 那一天」，而月分区的统计信息可以陈旧到**两天**：
//
//	session_turns_2026_11.ts 直方图上界 = 2026-11-23 09:07（采集于 11-23 17:08）
//	当天 = 2026-11-25 06:20 ⇒ 陈旧 45.3 小时
//
// 查询窗口 [now-24h, now] 整体落在直方图上界**之外** ⇒ `ts >= …` 的选择性被压到
// ≈0 ⇒ 冷分区被估成 3 行（实际 4,850）⇒ 规划器给 details 连接选了
// Nested Loop + Materialize + Join Filter ⇒ 单条语句 1 亿次比较、26~38 秒。
//
// # 为什么会陈旧
//
// 这两张表的月分区从未被纳入任何 autovacuum analyze 设置：启动期的
// apply_llm_gateway_autovacuum_settings()（404/838/839）只覆盖 `%_hot` 表和
// 11 张 v1 时代的分区父表 ⇒ 会话族月分区一直用**全局默认**
// autovacuum_analyze_scale_factor=0.1 ⇒ 24 万行要累计 2.4 万行变更才重采。
// 月分区越大、重采间隔越长，问题随月份推进而加重。对照：热表配了 0.02，
// 陈旧度 0.0 小时，估算准确（6,280 估 vs 6,324 实际）。
//
// # 做什么
//
// 对会话族月分区逐个设置与 839 同款的 reloptions（当月 heap 分区 analyze
// scale factor 0.005，其余月份 0.02），幂等：已有的跳过。PG17 不允许在分区
// **父表**上设 storage parameter（实测：`cannot specify storage parameters for a
// partitioned table`），新分区也不继承 ⇒ 只能逐叶子分区设，并且在分区管理器的
// 每趟 ensure 之后巡检（新月份分区被预建出来的同一趟就拿到设置）。
//
// # 为什么不放进 apply_llm_gateway_autovacuum_settings()
//
// 那份内联副本被 migration_839_test.go 钉住、必须与迁移 838/839 逐字同步；
// 改它等于改迁移台账。这里是独立的一道，不触碰既有函数。
//
// # 锁纪律
//
// ALTER TABLE … SET (…) 取 ACCESS EXCLUSIVE。本函数**从不无条件 ALTER**：
// 先按 reloptions 筛出缺失的候选，再在事务里 `lock_timeout` 之内拿锁、
// **拿锁后复查**（另一实例可能刚配好）才 ALTER。拿不到锁就跳过——这一趟
// 不配，下一趟再配；绝不为一个「锦上添花」的设置去排队堵住生产读写。
//
// # 统计信息什么时候真的刷新
//
// 设置生效后由 autovacuum 自己触发：当月分区的 analyze 阈值降到
// 50 + 0.005 × reltuples，而 n_mod_since_analyze 早已超过 ⇒ 下一个 naptime
// 周期就重采，之后约每 3~4 小时一次。本函数不显式 ANALYZE：大表 ANALYZE
// 可能撞角色级 statement_timeout，而 autovacuum worker 不受它约束。

// sessionFamilyAnalyzeParents 是需要巡检的会话族分区父表（按月分区、被日志
// 列表按时间窗读取）。
var sessionFamilyAnalyzeParents = []string{"session_turns", "session_turn_details"}

// sessionFamilyAnalyzeLockTimeout 是拿 ACCESS EXCLUSIVE 锁的等待上限。
const sessionFamilyAnalyzeLockTimeout = 3 * time.Second

// sessionFamilyAnalyzeOptsBase 是与 839 同款的 reloptions（不含 analyze scale
// factor——它按是否当月分区而定）。
const sessionFamilyAnalyzeOptsBase = "autovacuum_enabled=true, autovacuum_vacuum_scale_factor=0.05, " +
	"autovacuum_vacuum_threshold=10, autovacuum_analyze_threshold=50"

// sessionFamilyAnalyzeScaleCurrentMonth / sessionFamilyAnalyzeScaleOtherMonths
// 是 839 的口径：当月 heap 分区 0.005，其余 0.02。
const (
	sessionFamilyAnalyzeScaleCurrentMonth = "0.005"
	sessionFamilyAnalyzeScaleOtherMonths  = "0.02"
)

// sessionFamilyExpectedSFExpr 是「这个分区应该用的 analyze scale factor」的 SQL
// 表达式。候选筛选与 ALTER 两处必须同口径（只改一头 = 下个月初被改回去）。
const sessionFamilyExpectedSFExpr = `CASE WHEN c.relam = (SELECT oid FROM pg_am WHERE amname = 'heap')
             AND c.relname ~ ('_' || to_char(date_trunc('month', now()), 'YYYY_MM') || '$')
            THEN '` + sessionFamilyAnalyzeScaleCurrentMonth + `' ELSE '` + sessionFamilyAnalyzeScaleOtherMonths + `' END`

// sessionFamilyAnalyzeCandidatesSQL 选出「reloptions 还不完整」的会话族月分区。
// 已配好的分区不进结果 ⇒ 不会被 ALTER（也就不会被取 ACCESS EXCLUSIVE 锁）。
const sessionFamilyAnalyzeCandidatesSQL = `SELECT c.relname, ` + sessionFamilyExpectedSFExpr + ` AS expect_sf
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_inherits i ON i.inhrelid = c.oid
JOIN pg_class p ON p.oid = i.inhparent
WHERE n.nspname = 'public'
  AND p.relname = ANY($1::text[])
  AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[
      'autovacuum_enabled=true',
      'autovacuum_vacuum_scale_factor=0.05',
      'autovacuum_vacuum_threshold=10',
      'autovacuum_analyze_threshold=50',
      'autovacuum_analyze_scale_factor=' || ` + sessionFamilyExpectedSFExpr + `])
ORDER BY c.relname`

// sessionFamilyAnalyzeConfiguredSQL 在已持有锁的事务里复查：该分区的 reloptions
// 是否已经包含期望的全部设置。
const sessionFamilyAnalyzeConfiguredSQL = `SELECT COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[
      'autovacuum_enabled=true',
      'autovacuum_vacuum_scale_factor=0.05',
      'autovacuum_vacuum_threshold=10',
      'autovacuum_analyze_threshold=50',
      'autovacuum_analyze_scale_factor=' || ` + sessionFamilyExpectedSFExpr + `]
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relname = $1`

type sessionFamilyAnalyzeTarget struct {
	relname  string
	expectSF string
}

// sessionFamilyAnalyzeOutcome 是一趟巡检的读数（进日志，也供测试断言）。
type sessionFamilyAnalyzeOutcome struct {
	Applied int // 本趟实际 ALTER 的分区数
	Skipped int // 拿不到锁而跳过的分区数
}

// sessionFamilyAnalyzeAlterSQL 渲染一条 ALTER。relname 经标识符转义后再拼接。
func sessionFamilyAnalyzeAlterSQL(relname, expectSF string) string {
	return "ALTER TABLE public." + pgx.Identifier{relname}.Sanitize() +
		" SET (" + sessionFamilyAnalyzeOptsBase + ", autovacuum_analyze_scale_factor=" + expectSF + ")"
}

// isLockTimeoutError 报告 err 是否为 lock_timeout（SQLSTATE 55P03）。
func isLockTimeoutError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// applySessionFamilyAnalyzeSettings 给单个分区配置 reloptions。返回是否真的
// ALTER 了（拿锁后发现已被别的实例配好 ⇒ false, nil）。
func (pm *PartitionManager) applySessionFamilyAnalyzeSettings(ctx context.Context, t sessionFamilyAnalyzeTarget) (bool, error) {
	// expectSF 本应只来自候选 SQL 里那个二选一的 CASE；它是从查询结果读回来再拼进
	// DDL 的，所以仍然按白名单校验——不认识的值一律不执行。
	if t.expectSF != sessionFamilyAnalyzeScaleCurrentMonth && t.expectSF != sessionFamilyAnalyzeScaleOtherMonths {
		return false, fmt.Errorf("unexpected analyze scale factor %q for %s", t.expectSF, t.relname)
	}
	tx, err := pm.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // 只读或已提交 ⇒ ErrTxClosed，无害

	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+sessionFamilyAnalyzeLockTimeout.String()+"'"); err != nil {
		return false, err
	}
	// 先 LOCK 再复查：复查必须发生在持锁之后，否则两个实例会各自 ALTER 一遍。
	if _, err := tx.Exec(ctx, "LOCK TABLE public."+pgx.Identifier{t.relname}.Sanitize()+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return false, err
	}
	var configured bool
	if err := tx.QueryRow(ctx, sessionFamilyAnalyzeConfiguredSQL, t.relname).Scan(&configured); err != nil {
		return false, err
	}
	if configured {
		return false, nil
	}
	if _, err := tx.Exec(ctx, sessionFamilyAnalyzeAlterSQL(t.relname, t.expectSF)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ensureSessionFamilyAnalyzeSettings 巡检一趟：给缺失 autovacuum analyze 设置的
// 会话族月分区补上。永不返回错误——它是维护性的「锦上添花」，任何失败都只记
// 日志、留到下一趟；调用方不必处理。
//
// 由 PartitionManager.run 在每趟 ensureNextMonthPartitions **之后**调用
// （启动一趟 ＋ 每个 tick 一趟）：新月份分区被预建出来的同一趟就拿到设置。
func (pm *PartitionManager) ensureSessionFamilyAnalyzeSettings(ctx context.Context) sessionFamilyAnalyzeOutcome {
	outcome := sessionFamilyAnalyzeOutcome{}
	if pm == nil || pm.db == nil {
		return outcome
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	rows, err := pm.db.Query(timeoutCtx, sessionFamilyAnalyzeCandidatesSQL, sessionFamilyAnalyzeParents)
	if err != nil {
		slog.Warn("partition_manager: session family analyze settings scan failed", "error", err)
		return outcome
	}
	targets := make([]sessionFamilyAnalyzeTarget, 0, 4)
	for rows.Next() {
		var t sessionFamilyAnalyzeTarget
		if err := rows.Scan(&t.relname, &t.expectSF); err != nil {
			slog.Warn("partition_manager: session family analyze settings scan row failed", "error", err)
			break
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("partition_manager: session family analyze settings scan failed", "error", err)
		return outcome
	}

	for _, t := range targets {
		applied, err := pm.applySessionFamilyAnalyzeSettings(timeoutCtx, t)
		switch {
		case err == nil && applied:
			outcome.Applied++
			slog.Info("partition_manager: session family analyze settings applied",
				"partition", t.relname, "analyze_scale_factor", t.expectSF)
		case isLockTimeoutError(err):
			// 表上正有长查询 / autovacuum ⇒ 这一趟不配。reloptions 没配上，
			// 下一趟它仍是候选 ⇒ 不会漏。
			outcome.Skipped++
			slog.Info("partition_manager: session family analyze settings skipped (table busy)",
				"partition", t.relname, "lock_timeout", sessionFamilyAnalyzeLockTimeout.String())
		case err != nil:
			slog.Warn("partition_manager: session family analyze settings apply failed",
				"partition", t.relname, "error", err)
		}
	}
	if outcome.Applied > 0 || outcome.Skipped > 0 {
		slog.Info("partition_manager: session family analyze settings pass",
			"applied", outcome.Applied, "skipped_busy", outcome.Skipped, "candidates", len(targets))
	}
	return outcome
}

// scheduleSessionFamilyAnalyzeSettings 在独立 goroutine 里跑一趟巡检。
//
// 为什么不内联在 run() 里：巡检在表忙时要等 lock_timeout（每分区最多 3s），
// 内联会推迟 `close(pm.ready)`（promote 等它）和同一趟里排在后面的
// archive / default 分区残留检查。它自身不阻塞任何东西才是「锦上添花」的
// 正确姿势。
func (pm *PartitionManager) scheduleSessionFamilyAnalyzeSettings(ctx context.Context) {
	Go("partition_manager.sessionFamilyAnalyzeSettings", func() {
		pm.ensureSessionFamilyAnalyzeSettings(ctx)
	})
}
