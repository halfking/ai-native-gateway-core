package bg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Wave 3 B8: the two reconciliation verdicts are exercised end-to-end
// against a real-shaped schema with pgxmock-style fakes where cheap; the
// decisive logic (chain replay, mismatch pairing) is pinned here against
// synthetic ledgers via the SQL semantics they must implement:
//   - chain rows ordered by (created_at, id), first row per tenant anchors;
//   - usage/credit pairs compared on (tenant_id, request_id/ref_id) with
//     COALESCE 0 on the missing side.

// The job's verdict predicates are the SQL WHERE clauses; their Go-side
// contract is pinned indirectly through the insert-shape functions. What a
// unit test CAN decisively pin here is the bounded-run contract:
// cap, settle lag, and window are wired into both queries.

func TestLedgerReconciler_BoundedRunContract(t *testing.T) {
	r := NewLedgerReconciler(nil)
	if r.interval != DefaultLedgerReconciliationInterval {
		t.Fatalf("interval = %v, want %v", r.interval, DefaultLedgerReconciliationInterval)
	}
	if r.window != DefaultLedgerReconciliationWindow {
		t.Fatalf("window = %v, want %v", r.window, DefaultLedgerReconciliationWindow)
	}
	if r.maxFindingsPerRun != ledgerFindingsCap {
		t.Fatalf("cap = %d, want %d", r.maxFindingsPerRun, ledgerFindingsCap)
	}
	if ledgerSettleLag <= 0 || ledgerSettleLag >= r.window {
		t.Fatalf("settle lag %v must be positive and smaller than the window", ledgerSettleLag)
	}
	// nil pool must be a no-op, not a panic (data-plane / lite mode).
	if got := r.RunOnce(context.Background()); got != 0 {
		t.Fatalf("nil-pool RunOnce = %d findings, want 0", got)
	}
}

func TestLedgerReconciler_StopIdempotent(t *testing.T) {
	r := NewLedgerReconciler(nil)
	done := make(chan struct{})
	go func() {
		r.Stop()
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop must be idempotent and non-blocking")
	}
}

// The window boundary arithmetic used by both queries: a row at the window
// edge minus the settle lag must be included; a fresher row must not.
func TestLedgerReconciliationWindowBounds(t *testing.T) {
	now := time.Now()
	included := now.Add(-DefaultLedgerReconciliationWindow + time.Minute)
	excludedBySettle := now.Add(-time.Minute)
	if !included.Before(excludedBySettle) {
		t.Fatal("test premise broken: window rows must precede settle-lag rows")
	}
	if excludedBySettle.Sub(now.Add(-ledgerSettleLag)) <= 0 {
		t.Fatal("rows newer than the settle lag must fall outside both scans")
	}
}

// SQL shape regression (same discipline as auto_index_refresher_sql_test.go):
// unbalanced parens / stray commas only surface on a live PG tick — hold the
// shape here so a CI run without a DB still catches it.
func TestLedgerReconciliationSQLShape(t *testing.T) {
	for name, sql := range map[string]string{
		"balance_chain": balanceChainSQL(),
		"usage_credit":  usageCreditSQL(),
	} {
		clean := stripSQLCommentsAndLiterals(sql)
		minDepth, finalDepth := parenDepthProfile(clean)
		if minDepth < 0 || finalDepth != 0 {
			t.Fatalf("%s: unbalanced parens (min=%d final=%d)", name, minDepth, finalDepth)
		}
	}
	// Argument wiring is pinned explicitly: the chain query takes window +
	// cap ($1..$2); the usage↔credit query takes window, settle lag, cap
	// ($1..$3).
	for _, ph := range []string{"$1", "$2"} {
		if !strings.Contains(balanceChainSQL(), ph) {
			t.Fatalf("balance_chain missing %s", ph)
		}
	}
	for _, ph := range []string{"$1", "$2", "$3"} {
		if !strings.Contains(usageCreditSQL(), ph) {
			t.Fatalf("usage_credit missing %s", ph)
		}
	}
	// The usage↔credit verdict must compare both directions with COALESCE 0
	// (a missing side is a mismatch, not a skip).
	if !strings.Contains(usageCreditSQL(), "COALESCE(u.charged, 0) <> COALESCE(c.debited, 0)") {
		t.Fatal("usage_credit verdict must treat a missing side as zero")
	}
}

// Full-path integration against a real database (local deploy smoke runs
// this shape): inserts a synthetic ledger chain with one corrupted row and
// one usage/credit divergence, runs RunOnce, and asserts both checks land
// findings. Skipped unless TEST_DATABASE_URL is set.
func TestLedgerReconciler_RunOnce_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; SQL semantics need a real PG")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tenant := fmt.Sprintf("recon_it_%d", time.Now().UnixNano())
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM credit_ledger_hot WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM request_logs_hot WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM maas_reconciliation_findings WHERE tenant_id = $1`, tenant)
	}()

	// Chain: 100 → 90 → (201, corrupt) → 151, then a consume row continuing it.
	//
	// 余额语义是 running balance，链断判据是
	// `balance_after - (lag(balance_after) + amount) <> 0`。所以「种一个断点」
	// 只能靠**改余额**、不能靠改金额：我第一版写 amounts 100/-10/110/-50 配
	// balances 100/90/200/150 并注释成「corrupt 200 instead of 190」——
	// 但 90+110 本来就等于 200，四行 drift 全是 0，**一个断点都没种下**。
	// 现在把第三行余额改成 201（应为 200，drift=+1），第四行跟着改成 151
	// 使其自洽（151-(201-50)=0），于是恰好一个断点。
	//
	// created_at 全部显式给出：不显式时依赖插入顺序（各自事务的 now()），
	// 相等时 lag 顺序不确定，断点落在哪一行会飘。
	rows := []struct {
		amount    int64
		bal       int64
		createdAt time.Duration
	}{
		{100, 100, 90 * time.Minute}, // 起点
		{-10, 90, 80 * time.Minute},  // 100-10=90 自洽
		{110, 201, 70 * time.Minute}, // ← 唯一断点：应为 200
		{-50, 151, 60 * time.Minute}, // 201-50=151 自洽
	}
	for _, rw := range rows {
		if _, err := pool.Exec(ctx, `
			INSERT INTO credit_ledger_hot (tenant_id, entry_type, amount, balance_after, note, created_at)
			VALUES ($1, 'adjust', $2, $3, 'recon_it', now() - $4::interval)
		`, tenant, rw.amount, rw.bal, rw.createdAt); err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
	}
	// usage charged 30, ledger debited 25 → mismatch finding.
	//
	// created_at 必须**显式早于 ledgerSettleLag（10 分钟）**：usageCreditSQL 的
	// 两侧都有 `created_at < now() - $2` 上界，用来排除还在结算中的新行。
	// 我第一版让账本行走默认 now()，于是它被上界排除、debited 变成 0，
	// 门拿到的是 30 vs 0 —— 一条**由夹具制造的伪差异**，而不是注释声称的 30 vs 25。
	// 这比断链更隐蔽：门确实是红的，但红的原因不是它声称要验的东西。
	//
	// 该 consume 行同时参与余额链，故 balance_after 取 126 = 151-25 保持自洽。
	if _, err := pool.Exec(ctx, `
		INSERT INTO request_logs_hot (request_id, ts, tenant_id, credits_charged, total_tokens, success)
		VALUES ('recon_it_req', now() - interval '1 hour', $1, 30, 10, TRUE)
	`, tenant); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credit_ledger_hot (tenant_id, entry_type, amount, balance_after, ref_type, ref_id, note, created_at)
		VALUES ($1, 'consume', -25, 126, 'request', 'recon_it_req', 'recon_it', now() - interval '1 hour')
	`, tenant); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	r := NewLedgerReconciler(pool)
	r.window = 24 * time.Hour
	if got := r.RunOnce(ctx); got < 2 {
		t.Fatalf("RunOnce found %d differences, want >=2 (one chain break + one usage/credit mismatch)", got)
	}
	// 数量不够身份。「至少 2 条」可以被**两条错误类型的发现**满足 —— 而本门
	// 存在的全部意义就是验这两个检查各自落地。所以断身份。
	var n int
	byType := map[string]int{}
	rowsF, err := pool.Query(ctx, `
		SELECT check_kind FROM maas_reconciliation_findings WHERE tenant_id = $1
	`, tenant)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	for rowsF.Next() {
		var ck string
		if err := rowsF.Scan(&ck); err != nil {
			rowsF.Close()
			t.Fatalf("scan finding: %v", err)
		}
		byType[ck]++
		n++
	}
	rowsF.Close()
	if err := rowsF.Err(); err != nil {
		t.Fatalf("iterate findings: %v", err)
	}
	for _, want := range []string{"balance_chain", "usage_credit_mismatch"} {
		if byType[want] == 0 {
			t.Errorf("没有落地 %s 类发现：实际按类型 %v（n=%d）—— 数量达标不等于种类达标", want, byType, n)
		}
	}
	if n < 2 {
		t.Fatalf("findings persisted = %d, want >=2", n)
	}
}
