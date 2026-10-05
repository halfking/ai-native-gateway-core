package bg

// 832 观察源健康表的真库判据。
//
// 背景：漂移对账 worker 在抓不到外部机读源时**不写任何判词**（正确：没有观察
// 就没有结论），但原来也**不留任何痕**——只打一行 slog.Warn。于是「每 12h 抓
// 一次、每次都失败」可以连续几周不被发现，而报表照常出数。迁移 832 补的就是
// 「让人知道」的那一半。
//
// 这条判据打在**库上的行为**，不是文本形状：断言 upsert 的累加/归零、条数为 0
// 记成失败、健康面在陈旧时响而在新鲜时不响，以及 Optional 机制在表不存在时
// 确实让整轮检查继续跑完。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// observationHealthFixture 只建 832 的表；RunChecks 需要的是**不依赖**它的那几条
// 检查，所以这里不铺整个 schema——见下面 TestRunChecksSkipsOptionalCheckWhenTableAbsent
// 的说明，那条测试反过来证明缺表不会炸整轮。
const observationHealthFixture = `
CREATE TABLE public.model_baseline_price_observation_health (
    source_url           text PRIMARY KEY,
    last_success_at      timestamptz,
    last_attempt_at      timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    last_error           text,
    observed_models      integer,
    updated_at           timestamptz NOT NULL DEFAULT now()
);
`

// TestReconcileWorkerRecordsEveryFetchOutcome 钉**接线**，不是行为。
//
// 为什么必须单独一条：下面那条真库判据直接调 recordObservationFailure /
// recordObservationSuccess，它验证的是**那两个函数写库写得对**。但它完全
// 不经过 RunBaselineReconciliation —— 所以把 worker 里那行
// `recordObservationFailure(ctx, db, url, err)` 删掉，它**照样全绿**。
// 而那正是 832 存在的全部理由：抓不到源时不留痕。
//
// 这个洞是 2026-10-04 做变异验证时**实测撞出来的**（T1：删掉那行，判据仍
// 通过）。⇒ 「判据跑的是真函数」不等于「判据覆盖了真路径」；中间隔着调用
// 关系时，接线本身得单独钉。
//
// 形状钉子与 db/ensure_chain_order_test.go、discovery/canonical_maintain_test.go
// 同一取舍：红不能证明语义正确，抓的是「有人把这两行当冗余删掉」这一种回退。
func TestReconcileWorkerRecordsEveryFetchOutcome(t *testing.T) {
	src, err := os.ReadFile("pricing_baseline_reconcile.go")
	if err != nil {
		t.Fatalf("read pricing_baseline_reconcile.go: %v", err)
	}
	i := strings.Index(string(src), "func RunBaselineReconciliation")
	if i < 0 {
		t.Fatal("RunBaselineReconciliation not found — the code this guard pins moved or was renamed")
	}
	body := string(src)[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}

	// ★ 不能用 strings.Contains 判这两次调用在不在。
	//
	// 实测（2026-10-04 变异验证）：把调用**注释掉**是最常见的回退形态，而
	// 注释掉之后 `recordObservationFailure(ctx, db, url, err)` 这串文本**照样
	// 出现在文件里** —— Contains 判据照样绿。⇒ 文本钉必须逐行看，且要排除
	// 注释行；否则它守的正是自己最容易被改掉的那个形态。
	calledFailure := hasLiveCall(body, "recordObservationFailure(ctx, db, url, err)")
	calledSuccess := hasLiveCall(body, "recordObservationSuccess(ctx, db, url, len(observed))")

	if !calledFailure {
		t.Error("the fetch-failure branch no longer CALLS recordObservationFailure (a commented-out " +
			"or removed call counts as absent) — reverting to \"log a warning and return\" makes a " +
			"multi-week dead reconciler invisible again, which is the exact failure mode 832 prevents")
	}
	if !calledSuccess {
		t.Error("the fetch-success branch no longer CALLS recordObservationSuccess — without it " +
			"last_success_at never advances and the health check fires forever on a healthy source")
	}
	if !calledFailure || !calledSuccess {
		return
	}

	// 成功侧必须在**对账判词之前**落库。反了的话，一次失败的 ReconcileCatalog
	// 会让「这一轮确实抓到了源」这个事实也一起丢掉。
	succAt := indexOfLiveCall(body, "recordObservationSuccess(ctx, db, url, len(observed))")
	recAt := strings.Index(body, "RecordReconciliation")
	if succAt > recAt {
		t.Error("recordObservationSuccess is called AFTER RecordReconciliation — a failing " +
			"ReconcileCatalog would then also erase the fact that this cycle fetched the source OK")
	}
}

// hasLiveCall 判断 needle 是否作为一个**真正被调用**的语句出现。
//
// 逐行扫描并跳过注释行（// 与 * 开头、含 // 的行）。这正是 Contains 判据漏掉的
// 形态：把调用注释掉，文本还在，行为已经没了。
func hasLiveCall(body, needle string) bool { return indexOfLiveCall(body, needle) >= 0 }

func indexOfLiveCall(body, needle string) int {
	off := 0
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "*") &&
			!strings.HasPrefix(t, "--") && strings.Contains(line, needle) {
			return off
		}
		off += len(line) + 1
	}
	return -1
}

func TestObservationHealthRecordsBothOutcomesAndResetsOnSuccess(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — the observation-health check needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// ★ 先注册清理再建表（建表在前、defer 在后时，建表执行到一半失败会留下
	// 残桩，而下轮的安全闸会直接 SKIP ——「通过」实际一次没跑）。
	defer func() {
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.model_baseline_price_observation_health;`)
	}()

	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='model_baseline_price_observation_health'`).Scan(&existing); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if existing > 0 {
		t.Skip("the observation-health table already exists — this test drops it")
	}

	if _, err := pool.Exec(ctx, observationHealthFixture); err != nil {
		t.Fatalf("create fixture: %v", err)
	}

	const url = "https://models.dev/api.json"

	// 两次失败：计数必须**累加**成 2，而不是被覆盖成 1。这是最容易写错的形状
	// ——ON CONFLICT DO UPDATE 若漏了 +1，「连续失败」就退化成「上一次失败」，
	// 而告警恰恰要的是连续。
	recordObservationFailure(ctx, pool, url, errStub("EOF"))
	recordObservationFailure(ctx, pool, url, errStub("EOF again"))

	var fails int
	var successAt *time.Time
	var models *int
	var lastErr *string
	if err := pool.QueryRow(ctx, `
		SELECT consecutive_failures, last_success_at, observed_models, last_error
		  FROM public.model_baseline_price_observation_health WHERE source_url = $1`, url).
		Scan(&fails, &successAt, &models, &lastErr); err != nil {
		t.Fatalf("read after failures: %v", err)
	}
	if fails != 2 {
		t.Errorf("after two failures consecutive_failures = %d, want 2 — ON CONFLICT must "+
			"accumulate, not overwrite, or \"consecutive\" degrades into \"last attempt\"", fails)
	}
	if successAt != nil {
		t.Error("last_success_at is set after only failures — a failed fetch is not a success")
	}
	if lastErr == nil || *lastErr == "" {
		t.Error("last_error is empty after a failure — the reason is the only thing an operator has")
	}

	// 条数为 0 必须记成**失败**。源答了 HTTP 200 但内容不再是能解析的形状时，
	// 错误路径一个都不触发，而所有价格都「对不上」——那是最像成功的一种失败。
	recordObservationSuccess(ctx, pool, url, 0)
	var modelsAfterEmpty int
	if err := pool.QueryRow(ctx, `SELECT consecutive_failures FROM public.model_baseline_price_observation_health
		WHERE source_url = $1`, url).Scan(&modelsAfterEmpty); err != nil {
		t.Fatalf("read after empty payload: %v", err)
	}
	if modelsAfterEmpty != 3 {
		t.Errorf("after a fetch yielding 0 models, consecutive_failures = %d, want 3 — "+
			"\"200 with an unparsable payload\" must count as a failure, not a success", modelsAfterEmpty)
	}

	// 真成功：计数归零、成功时刻写上、条数写上、错误清空。
	recordObservationSuccess(ctx, pool, url, 226)
	var fails2 int
	var successAt2 *time.Time
	var models2 *int
	var lastErr2 *string
	if err := pool.QueryRow(ctx, `
		SELECT consecutive_failures, last_success_at, observed_models, last_error
		  FROM public.model_baseline_price_observation_health WHERE source_url = $1`, url).
		Scan(&fails2, &successAt2, &models2, &lastErr2); err != nil {
		t.Fatalf("read after success: %v", err)
	}
	if fails2 != 0 {
		t.Errorf("after a success consecutive_failures = %d, want 0", fails2)
	}
	if successAt2 == nil {
		t.Error("last_success_at is still NULL after a successful fetch — the health check keys on it")
	}
	if models2 == nil || *models2 != 226 {
		t.Errorf("observed_models = %v, want 226 — \"200 but the shape changed\" has no other signal", models2)
	}
	if lastErr2 != nil {
		t.Errorf("last_error = %q after a success, want NULL — a stale error is worse than none "+
			"because it describes a failure that is over", *lastErr2)
	}

	// 健康面：陈旧时要响，新鲜时不响。两条都跑，缺一条这条判据就是恒真的。
	if got := countObservationStaleRows(t, ctx, pool); got != 0 {
		t.Errorf("the health-check query reports %d stale row(s) right after a success — "+
			"a fresh source must be silent or the check cries wolf forever", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.model_baseline_price_observation_health
		SET last_success_at = now() - interval '72 hours', consecutive_failures = 4`); err != nil {
		t.Fatalf("age the row: %v", err)
	}
	if got := countObservationStaleRows(t, ctx, pool); got != 1 {
		t.Errorf("after ageing the row the health-check query reports %d stale row(s), want 1 — "+
			"a dead reconciler must show up here", got)
	}

	// 换源 = 新的一行，旧行留成历史（不是被覆盖）。这样「我们换过源」本身可查。
	recordObservationFailure(ctx, pool, "https://mirror.internal/api.json", errStub("first try on the new source"))
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.model_baseline_price_observation_health`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Errorf("after a source change the table has %d rows, want 2 — the new source must be its "+
			"own row and the old one must stay as history", rows)
	}
}

func countObservationStaleRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	for _, chk := range AllHealthChecks() {
		if chk.CheckID != "baseline_observation_stale" {
			continue
		}
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ("+chk.Query+") q").Scan(&n); err != nil {
			t.Fatalf("run the health-check query: %v", err)
		}
		return n
	}
	t.Fatal("baseline_observation_stale check missing from AllHealthChecks()")
	return 0
}

// TestRunChecksSkipsOptionalCheckWhenTableAbsent 证明 Optional 机制真的有效。
//
// 为什么单独一条：832 未应用的环境上这张表不存在，而 RunChecks 原本对任何查询
// 错误一律 return ——**一轮里任何一条检查失败，后面所有检查都不会跑**。所以
// 「表不存在时会不会中止整轮」不是推测，是必须量的事实。
//
// 用一张**确实不存在**的表：把检查的查询换掉不可能（查询是常量），所以这里直接
// 断言 RunChecks 在「只有这条可选检查引用缺失表」的库上不返回错误。
func TestRunChecksSkipsOptionalCheckWhenTableAbsent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 安全闸：这张表必须**真的**不在，否则这条判据量不到 42P01 分支。
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='model_baseline_price_observation_health'`).Scan(&existing); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if existing > 0 {
		t.Skip("the observation-health table exists — this test needs it absent to reach the 42P01 branch")
	}

	// 先直接确认那条可选检查的查询**确实**会因为缺表而报 42P01。
	// 少了这一步，下面对 runChecks 的断言可能只是在测「它没跑到那条检查」。
	var optional HealthCheckDef
	found := false
	for _, chk := range AllHealthChecks() {
		if chk.CheckID == "baseline_observation_stale" {
			optional, found = chk, true
			break
		}
	}
	if !found {
		t.Fatal("baseline_observation_stale check missing from AllHealthChecks()")
	}
	if !optional.Optional {
		t.Fatal("baseline_observation_stale is not marked Optional — a missing table would abort " +
			"the whole health run on every environment that has not applied 832 yet")
	}
	_, qErr := pool.Query(ctx, optional.Query)
	if qErr == nil {
		t.Fatal("the optional check's query succeeded with the table absent — this test no longer " +
			"measures the 42P01 branch it exists to measure")
	}
	var pgErr *pgconn.PgError
	if !errors.As(qErr, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError for the missing table, got %T: %v", qErr, qErr)
	}
	if pgErr.Code != "42P01" {
		t.Fatalf("missing-table SQLSTATE = %s, want 42P01 — if it is something else, the skip in "+
			"RunChecks will not fire and this test is measuring the wrong thing", pgErr.Code)
	}

	// 只挂这一条可选检查跑一轮。
	//
	// ★ 断言的是**错误身份**，不是「不返回错误」：
	// runChecks 在循环之后还有一个 auto_fix 阶段（canonical_id 回填），它在
	// 本夹具库上必然因缺 provider_models 而失败。所以「返回 nil」是量不到的，
	// 而「返回的错误**不是** baseline_observation_stale 的 42P01」恰好就是
	// 那个要证的性质——循环越过了这条可选检查。
	//
	// 这么写也顺手说明了这套夹具的边界：它证明的是**跳过语义**，不是
	// 「整个健康套件在这个裸库上绿」——后者本来就不可能，也不该成立。
	_, _, runErr := runChecks(ctx, pool, []HealthCheckDef{optional})
	if runErr != nil && strings.Contains(runErr.Error(), "baseline_observation_stale") {
		t.Fatalf("the loop aborted ON the optional check instead of skipping it: %v", runErr)
	} else if runErr == nil {
		t.Logf("the whole run completed — fine, the point (loop moved past the optional check) holds either way")
	} else {
		t.Logf("the run later failed in an unrelated phase (expected on a fixture DB), "+
			"and NOT on the optional check: %v", runErr)
	}
}

type stubErr string

func (e stubErr) Error() string { return string(e) }

func errStub(msg string) error { return stubErr(msg) }
