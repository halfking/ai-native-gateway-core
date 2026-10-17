// 钉住的行为：GET /api/providers/{id}（供应商详情页）在 5s 的 handler ctx 预算内
// 必须查得出 error_rate_24h，且该聚合**不得**回退去扫 canonical 视图
// request_logs_with_current_month。
//
// 事故链（2026-10-18 实测，llm.kxpms.cn / 154）：
//
//	getProvider 的 ctx 是 5s（providers.go:740）。error_rate_24h 原先挂在
//	LEFT JOIN 派生表上：`SELECT ... FROM request_logs_with_current_month
//	 WHERE ts >= now() - interval '24 hours' GROUP BY provider_id`，
//	再 `ON er.provider_id = p.id`。两个结构性问题叠在一起：
//	  1) LEFT JOIN 派生表让 `p.id = $1` 推不进聚合侧，于是每请求都要对
//	     **所有** provider 的 24h 行做聚合；
//	  2) 该 canonical 视图是四路 UNION ALL（session_turns_hot ∪
//	     session_turns ∪ 明细基表）+ details LATERAL 补列 + 两个 NOT EXISTS
//	     反连接，扫 24h 窗口实测 12.9s（下推 provider_id 后仍 7.6s）。
//	两者相乘 ⇒ 详情接口稳定越过 5s 预算，/api/providers/14 返 500
//	（log：getProvider query failed: timeout: context deadline exceeded），
//	13/1 等也贴着 5s 线。列表接口 0.157s 是因为它压根不算这个字段。
//
// 修复：error_rate_24h 改读 request_stats_minute 分钟级滚存，并改成相关子
// 查询让 provider 过滤落到扫描侧（45ms，比原形态快约 290 倍）。这不是新发明
// ——usageTrendSource（admin/usage_trend_series.go:153）早就把「带 provider
// 过滤」的读面分档到同一张滚存表，理由写的就是「避免明细分区慢扫」。
//
// 判据纪律：本文件分两层。
//   - 源码不变量层（默认跑，无需 DB）钉住「不得回退到 canonical 视图」与
//     「聚合必须能下推 provider 过滤」这两条结构性约束。
//   - 真库层（TEST_DATABASE_URL 门控）用与 handler **同一个 5s deadline**
//     跑真实 SQL，直接复现生产失败形态；并同时断言算出来的 rate 与独立
//     重算一致，防止「查不出来所以恒 0」这类假绿（见 AGENTS.md 判据纪律 3）。
package admin

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// getProviderSourceText 返回 getProvider 整个函数体源码（含其 SQL 字面量）。
func getProviderSourceText(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("providers.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (h *Handler) getProvider(")
	if start < 0 {
		t.Fatal("getProvider not found in providers.go")
	}
	end := strings.Index(body[start:], "func (h *Handler) createProvider(")
	if end < 0 {
		t.Fatal("createProvider marker not found; getProvider slice would be unbounded")
	}
	return body[start : start+end]
}

// getProviderSQLLiteral 返回 getProvider 里那段**逐字**SQL（含注释）。
//
// 为什么要逐字取而不是在测试里抄一份：本判据最初的版本把 SQL 抄在测试文件里，
// 结果 2026-10-18 真的漏了一次 —— providers.go 里有两行注释被误写成 Go 的
// `//` 而不是 SQL 的 `--`，raw string 里就成了 SQL 正文，线上报
// `syntax error at or near "同属"`，而当时**全部**判据仍然全绿：
// 源码不变量那几条只看视图名/关联条件（`//` 行不含它们），
// 真库那条跑的是测试自己那份抄件、与线上 SQL 早已不是一回事。
// 抄件判的是抄件，不是被测物。改成逐字取之后，同类错误会让真库判据直接红。
func getProviderSQLLiteral(t *testing.T) string {
	t.Helper()
	body := getProviderSourceText(t)
	const open = "h.db.QueryRow(ctx, `"
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatal("cannot locate the getProvider SQL literal")
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "`, id).Scan(")
	if j < 0 {
		t.Fatal("cannot locate the end of the getProvider SQL literal")
	}
	return rest[:j]
}

// stripSQLComments 去掉 SQL 行注释后再做「不得出现某视图名」这类断言。
// 本文件要断言的视图名恰恰被写进了那段解释「为什么不再读它」的注释里，
// 不剥离的话判据会对注释报错——逼人删事故记录，或把判据改成能被注释糊弄
// 过去的形态。判据只该看代码。
func stripGetProviderSQLComments(sql string) string {
	var kept []string
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestGetProviderSQLLiteralHasNoGoComments 钉住上面那次的具体事故形态：
// SQL 字面量里混入 Go 风格 `//` 注释行 —— 在 raw string 中它不是注释，是
// SQL 正文，会变成 `syntax error at or near ...`。
func TestGetProviderSQLLiteralHasNoGoComments(t *testing.T) {
	sql := getProviderSQLLiteral(t)
	for i, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			t.Errorf("getProvider SQL literal line %d starts with Go comment //: %q — "+
				"inside a raw string this is SQL text, not a comment, and the query "+
				"fails with a syntax error at run time", i+1, strings.TrimSpace(line))
		}
	}
}

// TestGetProviderNeverScansCanonicalViewForErrorRate 是本题的主判据。
//
// 只断言「源码里不再出现 request_logs_with_current_month」还不够——那个视图名
// 可能在别的子查询里正当出现。这里把范围收窄到 getProvider 整个函数体，
// 并要求 error_rate_24h 的读面必须是 request_stats_minute。
func TestGetProviderNeverScansCanonicalViewForErrorRate(t *testing.T) {
	body := stripGetProviderSQLComments(getProviderSQLLiteral(t))

	if strings.Contains(body, "request_logs_with_current_month") {
		t.Error("getProvider must not read request_logs_with_current_month: " +
			"that view is a 4-way UNION ALL with LATERAL enrichment and two NOT " +
			"EXISTS anti-joins; scanning a 24h window off it measured 12.9s " +
			"(7.6s even with provider_id pushed down) against a 5s handler ctx, " +
			"which is what turned /api/providers/14 into a 500")
	}

	if !strings.Contains(body, "request_stats_minute") {
		t.Error("getProvider must compute error_rate_24h from the " +
			"request_stats_minute rollup — the same tier usageTrendSource " +
			"(usage_trend_series.go:153) already uses for provider-filtered reads")
	}
}

// TestGetProviderErrorRatePushesProviderFilterDown 钉住第二个结构性问题：
// 聚合必须挂在 p.id 上，否则 LEFT JOIN 派生表会把单行过滤挡在聚合之外。
func TestGetProviderErrorRatePushesProviderFilterDown(t *testing.T) {
	body := stripGetProviderSQLComments(getProviderSQLLiteral(t))

	// 旧形态：先聚合全部 provider 再 LEFT JOIN 回来。
	if strings.Contains(body, "er ON er.provider_id = p.id") {
		t.Error("getProvider still aggregates every provider before joining the " +
			"single requested row: `p.id = $1` cannot be pushed into a LEFT JOIN " +
			"derived table, so each detail page load aggregates all providers' 24h rows")
	}

	// 新形态：相关子查询，扫描侧就带 provider 过滤。
	if !strings.Contains(body, "WHERE s.provider_id = p.id") {
		t.Error("error_rate_24h subquery must correlate on s.provider_id = p.id " +
			"so the provider filter reaches the scan side")
	}
	if !strings.Contains(body, "s.bucket >= now() - interval '24 hours'") {
		t.Error("error_rate_24h subquery must keep the 24h window predicate; " +
			"dropping it would silently widen the metric to all retained buckets")
	}
}

// TestGetProviderHandlerCtxBudgetIsFiveSeconds 把「5s」这个前提本身钉住。
// 真库判据要用它复现生产失败形态，前提漂了就等于在测别的东西。
func TestGetProviderHandlerCtxBudgetIsFiveSeconds(t *testing.T) {
	body := getProviderSourceText(t)
	if !strings.Contains(body, "context.WithTimeout(r.Context(), 5*time.Second)") {
		t.Error("getProvider ctx budget changed; the real-DB budget assertion " +
			"below assumes 5s and must be updated together with it")
	}
}

// getProvider 的 SELECT 列表形状。error_rate_24h 按位置定位，所以这两��
// 必须与 providers.go 的 Scan 顺序一起改——真库判据里已断言列数，不一致
// 会直接失败而不是默默读到别的列。
const (
	providerDetailColumnCount = 25
	// Scan 顺序：id code display_name catalog_code kind category protocol
	// base_url egress_profile domestic discount_rate enabled manual_disabled
	// header_profile_code notes vendor_name active healthy warning cooling
	// unreachable available unavailable error_rate_24h created_at
	providerErrorRateColumn = 23
)

// TestGetProviderErrorRateRealDBWithinHandlerBudget 真库层：带着 handler 同款
// 5s deadline 跑真实 SQL。修复前这条会以 "context deadline exceeded" 失败，
// 也就是 /api/providers/14 在生产上的那个 500。
//
// 第二段断言算出的 rate 必须等于独立重算的值——否则「子查询悄悄什么都没查到、
// 返回 0」会让本判据假绿（AGENTS.md 判据纪律 3：允许侧要单独判掉「看不见」）。
func TestGetProviderErrorRateRealDBWithinHandlerBudget(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 前提行，不得跳过。
	var id int64
	var wantReqs, wantFail int64
	err = pool.QueryRow(context.Background(), `
		SELECT s.provider_id, SUM(s.requests), SUM(s.failure_count)
		  FROM request_stats_minute s
		  JOIN providers p ON p.id = s.provider_id
		                   AND p.tenant_id = s.tenant_id
		                   AND p.deleted_at IS NULL
		 WHERE s.bucket >= now() - interval '24 hours'
		   AND s.tenant_id = 'default'
		 GROUP BY s.provider_id
		HAVING SUM(s.requests) > 0
		 ORDER BY SUM(s.requests) DESC
		 LIMIT 1`).Scan(&id, &wantReqs, &wantFail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			t.Skip("no provider with rollup traffic in the 24h window; " +
				"nothing to assert the computed rate against")
		}
		t.Fatalf("premise query failed (DB reachable but unusable): %v", err)
	}
	if wantReqs == 0 {
		t.Fatalf("premise broken: picked provider %d with zero requests", id)
	}
	want := float64(wantFail) / float64(wantReqs)

	// 跑 providers.go 里逐字取出的真实 SQL（见 getProviderSQLLiteral 的说明：
	// 抄件判的是抄件，不是被测物）。
	q := getProviderSQLLiteral(t)

	// 生产失败形态：handler 的 ctx 就是 5s。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 用 Values() 而不是 Scan(&got)：真 SQL 有 25 个返回列、类型不一
	// （string / *string / bool / float64 / int / timestamptz），
	// 逐个 Scan 既啰嗦又会在 SELECT 列表变化时炸成 arity 错误——
	// 那是判据自己的问题，不是被测行为。
	rows, err := pool.Query(ctx, q, id)
	if err != nil {
		t.Fatalf("getProvider query did not fit the 5s handler budget for provider %d: %v", id, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("getProvider returned no row for provider %d", id)
	}
	vals, err := rows.Values()
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if rows.Err() != nil {
		t.Fatalf("rows.Err: %v", rows.Err())
	}

	// 列数是**前提**，必须判掉：error_rate_24h 的位置靠下标定位，
	// SELECT 列表一旦变形，读到的就不是那一列，而断言仍可能「看着通过」。
	if len(vals) != providerDetailColumnCount {
		t.Fatalf("getProvider returned %d columns, expected %d — the "+
			"error_rate_24h index below no longer points at that column; "+
			"update both together",
			len(vals), providerDetailColumnCount)
	}

	got, ok := vals[providerErrorRateColumn].(float64)
	if !ok {
		t.Fatalf("column %d is %T, want float64 — SQL type changed under the test",
			providerErrorRateColumn, vals[providerErrorRateColumn])
	}
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("error_rate_24h = %v, want %v (requests=%d failures=%d) — "+
			"a 0 here means the subquery matched nothing, not that traffic was healthy",
			got, want, wantReqs, wantFail)
	}
}
