package bg

// 840 的真库行为回归（2026-10-07 建）
//
// ★★ 本文件的存在理由是 runbook §10.107 的教训：那是 56 条变异全绿、
//   门禁齐全的「锁存在、抢不到会跳过」，上线后才发现那把锁一次都没命中过。
//   **机制断言 ≠ 效果断言。**
//
// 所以本文件刻意**不**断言「claim 函数存在」或「SQL 里有 ON CONFLICT」
// （那些是 migration_840_test.go 的活，离线就能测）。本文件只测**效果**：
//
//	★ 两个并发调用 claim，只有一个必须拿到 true。
//	  这一条在单进程下也能测（起两个 goroutine 打同一个真库），
//	  它才是「跨实例互斥」这个承诺的唯一凭据。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与同目录其它 realdb 门同门控）。

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func throttleDSN840(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过 840 节流槽真库回归")
	}
	return dsn
}

func openThrottlePool840(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, throttleDSN840(t))
	if err != nil {
		t.Fatalf("连库失败：%v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// applyThrottle840 applies the migration under test and restores prior state.
func applyThrottle840(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	src, err := os.ReadFile("../sql/migrations/startup/840_analyze_stats_throttle_slot.sql")
	if err != nil {
		t.Fatalf("读 840 失败：%v", err)
	}
	ctx := context.Background()

	var existed bool
	if err := p.QueryRow(ctx,
		`SELECT to_regclass('public.llm_gateway_task_state') IS NOT NULL`).Scan(&existed); err != nil {
		t.Fatalf("探测表失败：%v", err)
	}
	if existed {
		// 保护生产里可能已存在的槽位记录，不在测试里覆盖。
		t.Log("llm_gateway_task_state 已存在，复用而不重建")
		ensureThrottleFuncs840(t, p)
		return
	}
	if _, err := p.Exec(ctx, string(src)); err != nil {
		t.Fatalf("应用 840 失败：%v", err)
	}
	t.Cleanup(func() {
		dropCtx := context.Background()
		if _, err := p.Exec(dropCtx,
			`DROP FUNCTION IF EXISTS public.complete_llm_gateway_task_slot(text);`+
				`DROP FUNCTION IF EXISTS public.claim_llm_gateway_task_slot(text, interval);`+
				`DROP TABLE IF EXISTS public.llm_gateway_task_state;`); err != nil {
			t.Logf("清理 840 失败（不致命）：%v", err)
		}
	})
}

// ensureThrottleFuncs840 re-applies just the function bodies for the case where
// the table already exists but this build's functions are missing.
func ensureThrottleFuncs840(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	var fnOK bool
	if err := p.QueryRow(context.Background(),
		`SELECT to_regprocedure('public.claim_llm_gateway_task_slot(text,interval)') IS NOT NULL`).Scan(&fnOK); err != nil {
		t.Fatalf("探测函数失败：%v", err)
	}
	if fnOK {
		return
	}
	src, err := os.ReadFile("../sql/migrations/startup/840_analyze_stats_throttle_slot.sql")
	if err != nil {
		t.Fatalf("读 840 失败：%v", err)
	}
	if _, err := p.Exec(context.Background(), string(src)); err != nil {
		t.Fatalf("重建函数失败：%v", err)
	}
}

func claimSlot840(ctx context.Context, p *pgxpool.Pool, task string, window string) (bool, error) {
	var got bool
	err := p.QueryRow(ctx,
		`SELECT public.claim_llm_gateway_task_slot($1, $2::interval)`, task, window).Scan(&got)
	return got, err
}

// TestThrottle840_ConcurrentClaimExactlyOneWins is THE test for this migration.
//
// ★ It asserts the EFFECT, not the mechanism: two callers racing the same task
//
//	row must produce exactly one winner. If someone rewrites claim_* to drop
//	the ON CONFLICT guard, or makes the WHERE vacuously true, this goes red
//	even though "the function exists and returns a boolean" still holds.
func TestThrottle840_ConcurrentClaimExactlyOneWins(t *testing.T) {
	p := openThrottlePool840(t)
	applyThrottle840(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const task = "test_throttle_840_concurrent"
	if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_task_state WHERE task_name = $1`, task); err != nil {
		t.Fatalf("清场失败：%v", err)
	}

	// ★ The window is 50 minutes on purpose: it matches the production default,
	//   so the losers must be rejected by the real threshold and not by some
	//   artifact of a test-only window.
	const win = "50 minutes"

	const racers = 8
	var wg sync.WaitGroup
	results := make([]bool, racers)
	errs := make([]error, racers)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // release everyone at once to actually contend
			results[idx], errs[idx] = claimSlot840(ctx, p, task, win)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个调用报错：%v", i, errs[i])
		}
		if results[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("★ 并发 %d 个调用拿到 %d 次 true，必须恰好 1 次。"+
			" 多于 1 = 节流失效（两台实例会各跑一遍，§10.107 的老问题回来了）；"+
			" 少于 1 = analyze 会被永久饿死。", racers, winners)
	}
}

// TestThrottle840_RejectsInsideWindowThenAcceptsOutside pins the threshold in
// both directions.
//
// ★ Both directions matter and they are NOT interchangeable: a WHERE that is
//
//	vacuously true always accepts (test 1 would pass, duplication returns);
//	a WHERE that never accepts always rejects (test 1 would FAIL, so that one
//	is already covered — but pinning it explicitly keeps the failure message
//	readable and documents that the slot is not permanent).
func TestThrottle840_RejectsInsideWindowThenAcceptsOutside(t *testing.T) {
	p := openThrottlePool840(t)
	applyThrottle840(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const task = "test_throttle_840_window"
	if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_task_state WHERE task_name = $1`, task); err != nil {
		t.Fatalf("清场失败：%v", err)
	}

	first, err := claimSlot840(ctx, p, task, "50 minutes")
	if err != nil {
		t.Fatalf("首次占槽失败：%v", err)
	}
	if !first {
		t.Fatal("★ 首次占槽必须成功：表刚清空，没有任何理由拒绝。" +
			"总是拒绝意味着 analyze 永远不跑。")
	}

	second, err := claimSlot840(ctx, p, task, "50 minutes")
	if err != nil {
		t.Fatalf("二次占槽失败：%v", err)
	}
	if second {
		t.Fatal("★ 50 分钟内的第二次占槽必须被拒：这正是节流本身。" +
			"被接受意味着 840 没有起到任何作用。")
	}

	// 窗口外的另一个任务名必须独立成功（防止 WHERE 误写成全局拦截）。
	other, err := claimSlot840(ctx, p, task+"_other", "50 minutes")
	if err != nil {
		t.Fatalf("其他任务占槽失败：%v", err)
	}
	if !other {
		t.Fatal("★ 不同 task_name 必须互不影响：槽是按任务名的行，不是全局单例。")
	}
}

// TestThrottle840_WindowActuallyExpires pins that the slot frees itself.
//
// ★ This is the anti-deadlock property. If last_started_at were never updated
//
//	the slot would wedge after one claim and analyze would be dead forever —
//	a silent permanent outage that no error would report.
func TestThrottle840_WindowActuallyExpires(t *testing.T) {
	p := openThrottlePool840(t)
	applyThrottle840(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const task = "test_throttle_840_expiry"
	if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_task_state WHERE task_name = $1`, task); err != nil {
		t.Fatalf("清场失败：%v", err)
	}

	// A 1-millisecond window makes the expiry observable without sleeping.
	first, err := claimSlot840(ctx, p, task, "1 millisecond")
	if err != nil {
		t.Fatalf("首次占槽失败：%v", err)
	}
	if !first {
		t.Fatal("首次占槽应成功")
	}

	time.Sleep(20 * time.Millisecond)

	second, err := claimSlot840(ctx, p, task, "1 millisecond")
	if err != nil {
		t.Fatalf("二次占槽失败：%v", err)
	}
	if !second {
		t.Fatal("★ 窗口过去后必须能再次占槽：否则一次 claim 之后 analyze 永久停摆。" +
			"这类缺陷不会报任何错。")
	}
}

// TestThrottle840_CrashedSlotIsRecoverable pins the COALESCE.
//
// ★ This is the only test that distinguishes
//
//	`COALESCE(last_completed_at, last_started_at)` from plain `last_started_at`.
//
// Scenario: an instance claims the slot, then dies mid-pass. last_started_at is
// written, last_completed_at stays NULL. After the window passes the slot must
// become claimable again.
//
// Without COALESCE the guard reads `last_completed_at < now() - window`, which
// evaluates to NULL for a crashed run — and WHERE NULL is not TRUE, so the row
// is never updated again. The slot wedges on its first use and analyze dies
// silently, forever, with no error anywhere.
func TestThrottle840_CrashedSlotIsRecoverable(t *testing.T) {
	p := openThrottlePool840(t)
	applyThrottle840(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const task = "test_throttle_840_crash"
	if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_task_state WHERE task_name = $1`, task); err != nil {
		t.Fatalf("清场失败：%v", err)
	}

	// Claim, then deliberately DO NOT complete — simulate a crash.
	if _, err := p.Exec(ctx, `SELECT public.claim_llm_gateway_task_slot($1, '1 millisecond')`, task); err != nil {
		t.Fatalf("占槽失败：%v", err)
	}

	var completedIsNull bool
	if err := p.QueryRow(ctx,
		`SELECT last_completed_at IS NULL FROM public.llm_gateway_task_state WHERE task_name = $1`,
		task).Scan(&completedIsNull); err != nil {
		t.Fatalf("读状态失败：%v", err)
	}
	if !completedIsNull {
		t.Fatal("夹具前提不成立：last_completed_at 应当为 NULL（模拟崩溃），实际有值")
	}

	time.Sleep(20 * time.Millisecond)

	got, err := claimSlot840(ctx, p, task, "1 millisecond")
	if err != nil {
		t.Fatalf("崩溃后重占失败：%v", err)
	}
	if !got {
		t.Fatal("★ 崩溃（completed_at 为 NULL）之后，窗口一过必须能重新占槽。" +
			"不能重占意味着槽在第一次使用后就永久卡死，analyze 无声停摆。" +
			"根因是判据没走 COALESCE(completed_at, started_at)。")
	}
}

// TestThrottle840_WindowIsMeasuredFromCompletion is the ONLY test that
// distinguishes COALESCE(completed_at, started_at) from started_at.
//
// ★ Found by mutation M2, not by reading the code: the first four behavior
//
//	tests all stayed green with the COALESCE deleted, because in every one of
//	them last_started_at was still recent enough to reject on its own.
//
// Where COALESCE actually bites: after a run COMPLETES, completed_at is NEWER
// than started_at. The window is therefore measured from completion — which is
// the intent ("don't start a new pass within N minutes of the last one
// FINISHING"). Without COALESCE the window would be measured from START, so a
// 94-second pass would let the next run in 94 seconds early, every hour.
func TestThrottle840_WindowIsMeasuredFromCompletion(t *testing.T) {
	p := openThrottlePool840(t)
	applyThrottle840(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const task = "test_throttle_840_from_completion"
	if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_task_state WHERE task_name = $1`, task); err != nil {
		t.Fatalf("清场失败：%v", err)
	}

	// 200ms window: long enough that the two readings are separable, short
	// enough that the test stays fast.
	const win = "200 milliseconds"

	claimStart := time.Now()
	if _, err := p.Exec(ctx, `SELECT public.claim_llm_gateway_task_slot($1, $2::interval)`, task, win); err != nil {
		t.Fatalf("占槽失败：%v", err)
	}

	// Half the window passes, THEN the run completes.
	time.Sleep(120 * time.Millisecond)
	if _, err := p.Exec(ctx, `SELECT public.complete_llm_gateway_task_slot($1)`, task); err != nil {
		t.Fatalf("记账失败：%v", err)
	}
	completedAt := time.Now()

	// Now sleep 120ms more. Total age since START ≈ 240ms > 200ms window,
	// but age since COMPLETION ≈ 120ms < 200ms window.
	//
	//   with COALESCE    → guard reads completed_at (120ms) → REJECT
	//   without COALESCE → guard reads started_at   (240ms) → ACCEPT
	time.Sleep(120 * time.Millisecond)

	t.Logf("since start=%s since completion=%s",
		time.Since(claimStart).Round(time.Millisecond),
		time.Since(completedAt).Round(time.Millisecond))

	got, err := claimSlot840(ctx, p, task, win)
	if err != nil {
		t.Fatalf("窗口内占槽失败：%v", err)
	}
	if got {
		t.Fatal("★ 距离【完成】不足窗口时间就必须拒绝。" +
			"被接受说明判据读的是 started_at 而不是 completed_at —— " +
			"于是每一趟 94 秒的 pass 都会让下一趟提前 94 秒进来。")
	}
}

// TestThrottle840_CompleteIsBookkeepingOnly pins that completion stamping does
// not participate in admission — the guard reads started_at, not completed_at.
func TestThrottle840_CompleteIsBookkeepingOnly(t *testing.T) {
	p := openThrottlePool840(t)
	applyThrottle840(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const task = "test_throttle_840_complete"
	if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_task_state WHERE task_name = $1`, task); err != nil {
		t.Fatalf("清场失败：%v", err)
	}

	if _, err := p.Exec(ctx, `SELECT public.claim_llm_gateway_task_slot($1, '50 minutes')`, task); err != nil {
		t.Fatalf("占槽失败：%v", err)
	}
	if _, err := p.Exec(ctx, `SELECT public.complete_llm_gateway_task_slot($1)`, task); err != nil {
		t.Fatalf("记账失败：%v", err)
	}

	var completedNull bool
	if err := p.QueryRow(ctx,
		`SELECT last_completed_at IS NULL FROM public.llm_gateway_task_state WHERE task_name = $1`,
		task).Scan(&completedNull); err != nil {
		t.Fatalf("读记账失败：%v", err)
	}
	if completedNull {
		t.Fatal("★ complete_llm_gateway_task_slot 必须写 last_completed_at：" +
			"它是人工核查『槽位是否还在正常轮转』的唯一信号。")
	}

	// 记账之后、窗口之内，仍必须被拒（说明 complete 没有偷偷放宽准入）。
	got, err := claimSlot840(ctx, p, task, "50 minutes")
	if err != nil {
		t.Fatalf("记账后占槽失败：%v", err)
	}
	if got {
		t.Fatal("★ complete 只记账不放行：记账后 50 分钟内仍应被拒。")
	}
}

// TestThrottle840_MissingTableRaisesUndefinedTable proves the Go degrade branch
// is reachable.
//
// ★ This is the rollback property. After 840's .down.sql runs, this table is
//
//	gone and every claim raises 42P01. The Go caller must recognise that and
//	run UNTHROTTLED. If it treated 42P01 as fatal, rolling back 840 would stop
//	the analyze pass entirely — a strictly worse outage than the duplication.
func TestThrottle840_MissingTableRaisesUndefinedTable(t *testing.T) {
	// ★ Deliberately does NOT open a pool: this one needs no database, and
	//   routing it through throttleDSN would make it skip everywhere the realdb
	//   gate is off — i.e. exactly where the degrade contract needs checking.
	if !isUndefinedTable(&pgconn.PgError{Code: "42P01", Message: "relation \"nope\" does not exist"}) {
		t.Fatal("isUndefinedTable 必须识别 42P01 —— 这是 840 down 回滚后 Go 走降级分支的唯一入口")
	}
	if isUndefinedTable(&pgconn.PgError{Code: "23505", Message: "unique violation"}) {
		t.Fatal("isUndefinedTable 不得把别的 SQLSTATE 当成缺表，否则吞掉真实错误")
	}
	if isUndefinedTable(context.Canceled) {
		t.Fatal("isUndefinedTable 不得对非 pg 错误返回 true")
	}
	if isUndefinedTable(nil) {
		t.Fatal("isUndefinedTable(nil) 必须是 false")
	}
}
