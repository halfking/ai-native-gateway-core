package bg

// ledger_reconciliation.go — Wave 3 B8 (2026-09-22): internal ledger
// reconciliation job (usage_ledger↔credit_ledger consistency).
//
// Design gap (§3 B8): until now only gateway↔provider-bill reconciliation
// existed (domains/providerprofile). The two internal ledgers were never
// cross-checked, so a crashed charge path or a mis-stamped credits_charged
// could silently diverge the wallet from what requests actually consumed.
// This job runs two checks on a fixed cadence and lands every difference in
// maas_reconciliation_findings (migration 737) plus a warning log + metric:
//
//  1. balance_chain — replay each tenant's credit_ledger rows in
//     (created_at, id) order: balance_after[n] must equal
//     balance_after[n-1] + amount[n]. A break means a ledger row was
//     written with a balance snapshot that doesn't chain (concurrent
//     wallet writers outside the serialised write path, manual edits,
//     partial replay).
//  2. usage_credit_mismatch — per (tenant, request_id): credits charged on
//     the request (_request_logs_hot.credits_charged, the same value the
//     maas backfill treats as source-of-truth) must equal the summed
//     consume deductions in credit_ledger_hot with ref_type='request'.
//     Adjudication note: the audit named "usage_ledger", but that table
//     carries cost_usd only — the credit-side anchor of the usage family
//     lives on _request_logs_hot.credits_charged, so this check reconciles
//     the two halves that actually both speak credits.
//
// Bounded noise: rows newer than settleLag are excluded (a request's ledger
// writes and its final log stamp can straddle the window edge); findings are
// capped per run; findings older than twice the window are deleted by the
// job itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/db"
)

const (
	// DefaultLedgerReconciliationInterval is the job cadence.
	DefaultLedgerReconciliationInterval = time.Hour
	// DefaultLedgerReconciliationWindow is the lookback both checks scan.
	// Must stay within credit_ledger_hot / _request_logs_hot retention.
	DefaultLedgerReconciliationWindow = 24 * time.Hour
	// ledgerSettleLag excludes the newest rows from both sides so a request
	// straddling the window boundary (ledger charged, log not yet stamped —
	// or vice versa) cannot false-positive.
	ledgerSettleLag = 10 * time.Minute
	// ledgerFindingsCap bounds one run's inserts; an incident floods
	// findings, not the table.
	ledgerFindingsCap = 200
)

var ledgerReconciliationFindings = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_ledger_reconciliation_findings_total",
		Help: "Internal ledger reconciliation differences landed in maas_reconciliation_findings, by check kind.",
	},
	[]string{"check"},
)

// LedgerReconciler periodically cross-checks the internal ledgers.
type LedgerReconciler struct {
	pool              *pgxpool.Pool
	interval          time.Duration
	window            time.Duration
	maxFindingsPerRun int
	stopCh            chan struct{}
	stopOnce          sync.Once
	running           atomic.Bool
	// skipped records, per run, which checks did NOT execute and why.
	// A skipped check returns 0, which is indistinguishable from "scanned
	// and found nothing" in the returned count — so the two must be
	// separable by callers and tests. "Swept 0 findings" and "swept 0
	// requests" are not the same statement.
	skippedMu sync.Mutex
	skipped   []string
}

// SkippedChecks returns the machine-readable reason keys of checks that were
// skipped in the most recent RunOnce, in execution order. Empty means every
// check actually ran.
func (r *LedgerReconciler) SkippedChecks() []string {
	if r == nil {
		return nil
	}
	r.skippedMu.Lock()
	defer r.skippedMu.Unlock()
	return append([]string(nil), r.skipped...)
}

func (r *LedgerReconciler) markSkipped(reason string) {
	r.skippedMu.Lock()
	defer r.skippedMu.Unlock()
	r.skipped = append(r.skipped, reason)
}

// resetSkipped clears the per-run skip list. Extracted as a named method (rather
// than inlined in RunOnce) so a test can pin that RunOnce actually calls it: the
// first version of that test reset the field *by hand* and therefore verified
// nothing — a mutation that deleted the reset from RunOnce stayed green.
func (r *LedgerReconciler) resetSkipped() {
	r.skippedMu.Lock()
	defer r.skippedMu.Unlock()
	r.skipped = nil
}

func NewLedgerReconciler(pool *pgxpool.Pool) *LedgerReconciler {
	return &LedgerReconciler{
		pool:              pool,
		interval:          DefaultLedgerReconciliationInterval,
		window:            DefaultLedgerReconciliationWindow,
		maxFindingsPerRun: ledgerFindingsCap,
		stopCh:            make(chan struct{}),
	}
}

// Start launches the ticker loop. Initial stagger mirrors the other bg
// workers so a cold boot does not probe the ledgers while migrations run.
func (r *LedgerReconciler) Start(ctx context.Context) {
	if r == nil || r.pool == nil {
		return
	}
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.running.Store(false)
		// R59 audit (S5-F2): a RunOnce panic used to kill the loop silently
		// (no recover, no log, no restart) — the reconciler would just stop
		// forever. Mirror the other bg workers: recover, log, keep ticking.
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("ledger_reconciliation: RunOnce panicked", "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-time.After(5 * time.Minute):
		}
		r.RunOnce(ctx)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
				// Panic inside one round must not end the loop.
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							slog.Error("ledger_reconciliation: tick panicked", "panic", rec, "stack", string(debug.Stack()))
						}
					}()
					r.RunOnce(ctx)
				}()
			}
		}
	}()
}

// Stop terminates the loop; safe to call multiple times.
func (r *LedgerReconciler) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// RunOnce executes both checks, persists findings (capped) and returns the
// total number of differences observed.
func (r *LedgerReconciler) RunOnce(ctx context.Context) int {
	if r == nil || r.pool == nil {
		return 0
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	window := r.effectiveWindow()
	// Reset per-run skip state: SkippedChecks must describe THIS run, never a
	// previous one. A stale skip list is the worst version of this feature — it
	// would report "skipped" for a check that ran and really did find nothing.
	r.resetSkipped()
	// Publish "this round did not execute" to /metrics. Deferred so it covers
	// every return path (including ones added later) — the skipped list is read
	// at exit, after the checks have had their chance to markSkipped. Without
	// this, a skipped check returns 0 and is indistinguishable from "scanned
	// and found nothing" on the findings counter, so stop-write reads as
	// "ledger clean". See bg/s4_scan_skip_metrics.go.
	defer recordS4ScanSkipState(s4ScanWorkerLedgerReconciliation, r.SkippedChecks(), time.Now())
	total := 0
	total += r.checkBalanceChain(runCtx, window)
	total += r.checkUsageCredit(runCtx, window)
	r.pruneOldFindings(runCtx, window)
	return total
}

// effectiveWindow clamps the configured lookback to the live *_hot retention
// window. Both checks scan only _request_logs_hot / credit_ledger_hot, so a
// lookback beyond lifecycle.hot_retention_hours silently covers less than
// requested — the R56 audit: the 24h default against the 8h hot retention
// meant the "24h" window actually reached only ~8h back, while the type
// comment claimed it must stay within hot retention.
func (r *LedgerReconciler) effectiveWindow() time.Duration {
	hours := settingsGetPlatformInt("lifecycle.hot_retention_hours", int(DefaultRetentionWindow.Hours()))
	if hours > 0 {
		if hotMax := time.Duration(hours) * time.Hour; r.window > hotMax {
			return hotMax
		}
	}
	return r.window
}

// pruneOldFindings deletes findings older than twice the effective window —
// the retention the package comment (and the 737 migration) always promised;
// the delete itself was missing until R56, so a persistent break re-inserted
// the same finding every hour without bound.
func (r *LedgerReconciler) pruneOldFindings(ctx context.Context, window time.Duration) {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tag, err := r.pool.Exec(pctx,
		`DELETE FROM maas_reconciliation_findings WHERE run_at < now() - $1::interval`,
		2*window)
	if err != nil {
		slog.Warn("ledger reconciliation: findings prune failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Debug("ledger reconciliation: pruned stale findings", "count", n)
	}
}

// balanceChainSQL replays the per-tenant balance_after chain server-side
// and returns every break. Extracted for the SQL-shape regression test.
func balanceChainSQL() string {
	return `
		WITH chain AS (
		    SELECT tenant_id, id, amount, balance_after,
		           balance_after
		             - (lag(balance_after) OVER (PARTITION BY tenant_id ORDER BY created_at, id)
		                + amount) AS drift
		    FROM credit_ledger_hot
		    WHERE created_at > now() - $1::interval
		)
		SELECT tenant_id, id, amount, balance_after, drift
		FROM chain
		WHERE drift <> 0
		LIMIT $2
	`
}

// checkBalanceChain replays the per-tenant balance_after chain server-side
// and lands every break.
func (r *LedgerReconciler) checkBalanceChain(ctx context.Context, window time.Duration) int {
	rows, err := r.pool.Query(ctx, balanceChainSQL(), window, r.maxFindingsPerRun)
	if err != nil {
		slog.Warn("ledger reconciliation: balance chain scan failed", "error", err)
		return 0
	}
	defer rows.Close()
	count := 0
	type row struct {
		tenantID     string
		id           int64
		amount       int64
		balanceAfter int64
		drift        int64
	}
	var batch []row
	for rows.Next() {
		var v row
		if err := rows.Scan(&v.tenantID, &v.id, &v.amount, &v.balanceAfter, &v.drift); err != nil {
			slog.Warn("ledger reconciliation: balance chain scan row failed", "error", err)
			return count
		}
		batch = append(batch, v)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ledger reconciliation: balance chain scan failed", "error", err)
		return count
	}
	for _, v := range batch {
		r.insertFinding(ctx, "balance_chain", v.tenantID, fmt.Sprintf("ledger:%d", v.id),
			v.balanceAfter-v.drift, v.balanceAfter, map[string]any{
				"amount":        v.amount,
				"expected_from": "prev_balance_after + amount",
			})
		count++
	}
	return count
}

// ★ 2026-10-20（迁移 890 + 并行审计轮 D0）：旧的「可比性门」已退役。
//
// 退役的是：常量 `usageCreditSkipS4StopWrite`（"s4_stop_write"）与纯函数
// `usageCreditComparability(logsWriteEnabled)`。
//
// 旧门的形状：usageCreditSQL 把 v1 族（`storage.request_logs_write_enabled`
// 门内）与 credit_ledger_hot（永不在门内）做 FULL OUTER JOIN ⇒ 停写生效后
// 两侧不再同期 ⇒ 每个请求都落进「未匹配 credit」分支被记成差异 ⇒ 无界假发现。
// ⇒ 旧门在停写时跳过本比对，形状是对的。
//
// 退役理由（两个前提同时消失，而门仍然闭合）：
//   ① 890 把 v1 族整体 DROP、usage 侧随之迁到**会话族**（写入路径无条件）
//      ⇒ 「门一关两侧就不同期」不再成立；
//   ② 该键的**代码默认**被并行审计轮改成 false ⇒ 这条**财务**比对在任何
//      未显式配置它的环境上**永久停摆**（findings 计数恒 0 ⇒ 运维会读到
//      「账务无差异」）。
//
// ⇒ 替代物：`usageArmEmptySkipReason` + **数据驱动**的可判定性检查
//   （见 checkUsageCredit）—— 判据从「某个开关的状态」换成「这一次的两侧
//   是否同期」，后者才是这个问题真正要问的。
//
// （退役记录：这两个符号的唯一用途就是那道门，留着就成了「看起来还在
//   防守」的僵尸判据。）

// usageCreditSQL compares per-request credit charges (session family)
// against ledger consume deductions (credit_ledger_hot). Extracted for the
// SQL-shape regression test.
//
// Note the two sides are NOT in the same blast radius — the usage arm comes
// from the session family (written unconditionally), the credit arm from the
// billing ledger (never gated) — which is why the decidability check is
// **data-driven** (usage arm empty ⇒ skip) rather than a switch: see
// checkUsageCredit and usageArmEmptySkipReason.
func usageCreditSQL() string {
	return `
		WITH usage AS (
		    SELECT tenant_id, request_id, SUM(credits_charged) AS charged
		    -- ★ 890（v1 族退役）：源换成会话族热腿（v1 热表已 DROP）。
		    -- credits_charged / tenant_id / request_id / ts 在会话族投影里同名同义。
		    FROM ` + db.SessionFamilyTurnsHotSourceSQL() + `
		    WHERE ts > now() - $1::interval
		      AND ts < now() - $2::interval
		      AND credits_charged IS NOT NULL AND credits_charged <> 0
		    GROUP BY tenant_id, request_id
		),
		credit AS (
		    SELECT tenant_id, ref_id, SUM(-amount) AS debited
		    FROM credit_ledger_hot
		    WHERE created_at > now() - $1::interval
		      AND created_at < now() - $2::interval
		      AND entry_type = 'consume' AND ref_type = 'request'
		    GROUP BY tenant_id, ref_id
		)
		SELECT COALESCE(u.tenant_id, c.tenant_id),
		       COALESCE(u.request_id, c.ref_id),
		       COALESCE(u.charged, 0), COALESCE(c.debited, 0),
		       -- ★ 890 + D0：usage 臂的**总体规模**。
		       --   它回答的是「这次比对有没有可比性」：usage 臂为 0 行而
		       --   credit 臂有行 ⇒ 两侧不同期（源要么没写、要么没带
		       --   credits_charged、要么镜像滞后）⇒ 比对结果会把
		       --   credit 侧每一行都记成「计了费但没有用量」——**假发现**。
		       --   旧实现用「S4 停写键」当这个开关（证据源在门内 ⇒ 门一关
		       --   就冻结），890 之后证据源迁到会话族、且该键的代码默认被
		       --   并行审计轮改成 false ⇒ 那个开关既过时又恒闭 ⇒ 这条
		       --   **财务**比对在任何未显式配置它的环境上永久停摆。
		       --   ⇒ 改用**数据驱动**的可判定性：臂空 ⇒ 不可判定 ⇒ 跳过
		       --   并把跳过做成可观测（见 usageArmEmptySkipReason）。
		       --   ⚠ 这不是「用数据换开关」——它比开关更贴切：开关只能回答
		       --   「源是否被关掉」，数据回答的是「这一次的两侧是否同期」。
		       (SELECT count(*)::int FROM usage) AS usage_arm_rows
		FROM usage u
		FULL OUTER JOIN credit c
		  ON u.tenant_id = c.tenant_id AND u.request_id = c.ref_id
		WHERE COALESCE(u.charged, 0) <> COALESCE(c.debited, 0)
		LIMIT $3
	`
}

// checkUsageCredit compares per-request credit charges (_request_logs_hot)
// against ledger consume deductions (credit_ledger_hot).
//
// Gated on the S4 stop-write key: the two sides belong to different blast
// radii, so while stop-write is active the comparison is not decidable and
// running it would land an unbounded stream of false findings. See
// usageCreditComparability for the full failure shape.
// usageArmEmptySkipReason 是 machine-readable 原因键：当 usage 臂在窗口内
// **一行都没有**、而 credit 臂有行时，本次比对**不可判定**。
//
// ★ 2026-10-20（迁移 890 + 并行审计轮 D0）：它取代了旧的
// `usageCreditSkipS4StopWrite`（"s4_stop_write"）。
//
// 旧键的前提是「usage 侧（v1 族）在 S4 停写门内 ⇒ 门一关，两侧不再同期」。
// 890 把 v1 族整体 DROP、usage 侧随之迁到**会话族**（写入路径无条件）⇒
// ① 「门一关就不同期」不再成立；② 而该键的**代码默认**被改成 false
// ⇒ 这条**财务**比对在任何未显式配置它的环境上**永久停摆**
// （停摆期间 findings 计数为 0，运维会读到「账务无差异」）。
//
// ⇒ 改为**数据驱动**的可判定性：理由不再来自某个开关，而是来自
// 「这一次的两侧是否同期」。
const usageArmEmptySkipReason = "usage_arm_empty"

// usageCreditComparable 判定「这一次 usage↔credit 比对是否**可判定**」。
//
// 纯函数（可脱离数据库被测），两个输入都来自本次查询的结果：
//
//   - usageArmRows：usage 臂（会话族带 credits_charged 的请求）在窗口内的行数；
//   - diffRows：本次比对**要报**的差异行数（usage 与 credit 两侧不同额）。
//
// 判定：
//
//	① 没有差异行 ⇒ 可判定（且无事可报）—— 这是「确实没有差异」，
//	  **不是**「不可判定」，两者必须分开（否则运维会读到「账务无差异」
//	  而真相是「没跑」）。
//	② 有差异行、但 usage 臂为空 ⇒ **不可判定**：两侧不同期（源没写、
//	  没带 credits_charged、或镜像滞后）⇒ 继续比对会把 credit 侧每一行
//	  都记成「计了费但没有用量」的**假发现**，且数量无上界。
//	③ 其余 ⇒ 可判定。
//
// ★ 2026-10-20（890 + D0）：本函数取代旧的
// `usageCreditComparability(logsWriteEnabled)`——旧版的输入是一个**开关**，
// 而开关只能回答「源是否被关掉」；本版回答的是「**这一次**的两侧是否同期」，
// 后者才是这个问题真正要问的。旧的开关式判据会让这条财务比对在未显式
// 配置该键的环境上**永久停摆**（停摆期间 findings 恒 0 ⇒ 运维读到
// 「账务无差异」）。
func usageCreditComparable(usageArmRows, diffRows int) (comparable bool, reason string) {
	if diffRows == 0 {
		return true, ""
	}
	if usageArmRows == 0 {
		return false, usageArmEmptySkipReason
	}
	return true, ""
}

// checkUsageCredit compares per-request credit charges (session family)
// against ledger consume deductions (credit_ledger_hot).
//
// ★ 2026-10-20（890 + D0）：本函数原先的**前提闸**（读 S4 停写键）已退役，
// 换成**数据驱动**的可判定性检查：
//
//	usage 臂（会话族带 credits_charged 的请求）为空，而 credit 臂有行
//	⇒ 两侧不同期 ⇒ 比对不可判定 ⇒ 跳过并**把跳过做成可观测**，
//	  而不是把 credit 侧每一行都记成「计了费但没有用量」的**假发现**。
//
// ⚠ 这条比对是**财务控制**：它退役前的默认状态是「永远跳过」
//
//	（停写键默认 false）⇒ 恢复它本身就是修一个**静默的财务盲区**。
func (r *LedgerReconciler) checkUsageCredit(ctx context.Context, window time.Duration) int {
	rows, err := r.pool.Query(ctx, usageCreditSQL(), window, ledgerSettleLag, r.maxFindingsPerRun)
	if err != nil {
		slog.Warn("ledger reconciliation: usage↔credit scan failed", "error", err)
		return 0
	}
	defer rows.Close()
	count := 0
	type pair struct {
		tenantID, requestID string
		charged, debited    int64
		usageArmRows        int
	}
	var batch []pair
	for rows.Next() {
		var v pair
		if err := rows.Scan(&v.tenantID, &v.requestID, &v.charged, &v.debited, &v.usageArmRows); err != nil {
			slog.Warn("ledger reconciliation: usage↔credit scan row failed", "error", err)
			return count
		}
		batch = append(batch, v)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ledger reconciliation: usage↔credit scan failed", "error", err)
		return count
	}
	// 不可判定 ⇒ 跳过并说明（不产生任何 finding）。
	//
	// ⚠ 判据是「**有差异行** 且 usage 臂为 0」，不是「usage 臂为 0」：
	// 两侧都空时没有差异行、没有假发现风险，那是「确实没有差异」，
	// 不该被记成「跳过」。
	//
	// ⚠ batch 可能为空（两侧都空 ⇒ 零差异行）⇒ 取 usage 臂行数前先判长度，
	// 否则 batch[0] 会越界 panic（这类 panic 会打掉整个 worker 的 goroutine）。
	usageArmRows := 0
	if len(batch) > 0 {
		usageArmRows = batch[0].usageArmRows
	}
	if comparable, reason := usageCreditComparable(usageArmRows, len(batch)); !comparable {
		r.markSkipped(reason)
		slog.Info("ledger reconciliation: usage↔credit 比对已跳过（本轮未执行，非「无差异」）",
			"reason", reason,
			"detail", "usage 臂（会话族带 credits_charged 的请求）在窗口内一行都没有，"+
				"而 credit 臂有行 ⇒ 两侧不同期 ⇒ 比对不可判定；"+
				"继续比对会把 credit 侧每一行都记成「计了费但没有用量」的假发现")
		return 0
	}
	for _, v := range batch {
		r.insertFinding(ctx, "usage_credit_mismatch", v.tenantID, v.requestID,
			v.charged, v.debited, map[string]any{
				"charged_on_request": v.charged,
				"debited_in_ledger":  v.debited,
			})
		count++
	}
	return count
}

func (r *LedgerReconciler) insertFinding(ctx context.Context, kind, tenantID, refID string, expected, actual int64, detail map[string]any) {
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		detailJSON = []byte("{}")
	}
	_, err = r.pool.Exec(ictx, `
		INSERT INTO maas_reconciliation_findings
		    (check_kind, tenant_id, ref_id, expected, actual, detail)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, kind, tenantID, refID, expected, actual, string(detailJSON))
	if err != nil {
		slog.Warn("ledger reconciliation: finding insert failed",
			"check", kind, "tenant", tenantID, "ref", refID, "error", err)
	}
	ledgerReconciliationFindings.WithLabelValues(kind).Inc()
	slog.Warn("ledger reconciliation: difference found",
		"check", kind, "tenant_id", tenantID, "ref_id", refID,
		"expected", expected, "actual", actual)
}
