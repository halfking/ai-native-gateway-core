package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ──────────────────────────────────────────────────────────────────────────
// 本文件在守什么：三个「用量」端点的取数口径
// ──────────────────────────────────────────────────────────────────────────
//
// 取数源实测于 admin/usage_enhanced.go（2026-10-21 R80 轮），不是猜的：
//
//	usageCostTrend      group_by=model    → usage_ledger_with_current_month
//	                    group_by=provider → usage_ledger_with_current_month
//	                    group_by=api_key  → usage_ledger_with_current_month
//	                    group_by=work_type / intent
//	                                   → request_logs_with_current_month
//	usagePeriodCompare  → usage_ledger_with_current_month
//	usageCacheEconomics 主聚合 → usage_ledger_with_current_month
//	                    压缩计数 → session_turns.compression_strategy
//
// 归属由 planCostTrend 决定（usage_enhanced.go:94-129）：model / provider /
// api_key 是**计费侧**维度，基表是计费宽表 usage_ledger；work_type / intent 才是
// **请求侧**维度，基表是请求日志族。usage_ledger 从来没有 work_type /
// gw_session_id 这两列（它只有 20 列），在那上面引用必然 42703。
//
// ★★ R80 轮订正 R70 轮的表述：R70 说「改瞄会话族」是**错的**。
//   usageCostTrend 的 model 维度基表是 usage_ledger，不是会话族。
//   而且就算 v1 族没退役，把种子插进 `_request_logs` 也喂不到 model 维度聚合
//   —— 那时它是「按名像」而不是「按形对」。
//   ⇒ 种子落点 = usage_ledger（计费宽表） + session_turns（会话族，只为压缩计数）。
//
// ★★ 租户过滤靠 auth context，**不靠 X-Tenant-ID 头**。
//
// EffectiveTenantIDAll(r)（admin/context.go:69）的语义是：
//
//	租户管理员 → GetTenantID(r)（读 request context 里的 AuthContext）
//	其它       → ""（= 查所有租户）
//
// 而 GetTenantID（context.go:37）只读 AuthContext，**不读任何 header**。
// ⇒ 旧夹具那句 `req.Header.Set("X-Tenant-ID", tenantID)` 是**无效的**：
//   那个头从头到尾没有参与过 SQL ⇒ 统计口径其实是「所有租户」。
//   ⇒ 在共享库上 COUNT(*) 会把别人的行一起算进来，「期望 2 条」变成「期望 5 条」。
//   ⇒ 现在改成 SetAuthContext(Role: "tenant_admin", TenantID: ...)，
//     租户过滤才真的落到 SQL 的 AND tenant_id = $3 上。
//
// ★★ 本文件不需要 R78 那套「破坏性门自带一次性库」的机制。
//   R78 那条硬约束针对的是 DROP/CREATE 产品对象的门（db 包）。这里只做**行级**
//   插入与按 tenant_id 的删除，不改任何结构。而官方 gate 把 TEST_DATABASE_URL
//   指向自己新建的一次性库（scripts/audit/run-integration-gate.sh:843
//   TEST_DATABASE_URL="$GATE_URL"）⇒ 那里写进去的数据随 gate 一起丢弃。

// usageLedgerSeed 是一行 usage_ledger 种子。
//
// 列名与 usage_ledger 的真实列同名（2026-10-21 真库 information_schema 实测）：
// request_id / ts / tenant_id / raw_model_name / prompt_tokens /
// completion_tokens / cache_read_tokens / total_tokens / cost_usd /
// latency_ms / success。
//
// ★ 注意旧夹具用过的 `outbound_model` **不存在**于 usage_ledger；
// model 维度的分组列是 `raw_model_name`。
type usageLedgerSeed struct {
	model            string
	cost             float64
	promptTokens     int
	completionTokens int
	cacheReadTokens  int
	success          bool
	ts               time.Time
}

// uniqueTenantID 造一个本次运行独有的租户 id。
//
// 为什么不用固定字符串：固定 id + 共享库 ⇒ 上一次**失败**运行留下的行会混进
// 下一次的 COUNT(*)（「期望 2 条」变成「期望 5 条」），而且失败与成功两次运行
// 的行为还不一样 ⇒ 这种门在共享库上不可复现。
func uniqueTenantID(prefix string) string {
	return fmt.Sprintf("test-%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano()%1_000_000_000)
}

// requireRelations 检查本用例的**种子落点与取数源**在不在。
//
// 「不在」的处置是**显式 SKIP 并点名缺哪个关系**，而不是让插入失败后继续跑
// —— 后者会把「环境不是产品形态」显示成产品缺陷。
//
// ★ 这道守卫是**用例级**的，不是文件级的。
//
//	R70 轮把它放在共享 helper testDB() 里（查 public._request_logs 在不在，
//	不在就整个文件 SKIP）⇒ 连坐了三处：
//	  ① 本文件 9 个用例里有 4 个根本不插种子（纯参数校验）；
//	  ② helper 是共享的 ⇒ admin/usage_trend_series_test.go 的 2 个用例也被
//	     挡住，而它们的种子在 request_stats_dim_minute（另一个族）；
//	  ③ 守卫问的是「v1 族在不在」，而这三个端点的取数源从来不是 v1 族。
//	⇒ 守卫下沉到种子级：每个用例按自己真正需要的关系统一地问。
//
// 同族第三例见 R79（cmd/gateway/dual_read_* 的 3 个门被共用入口连坐）。
func requireRelations(t *testing.T, db *pgxpool.Pool, rels ...string) {
	t.Helper()
	ctx := context.Background()
	for _, rel := range rels {
		var got string
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(to_regclass('public.' || $1)::text, '')`, rel).Scan(&got); err != nil {
			t.Fatalf("探测关系 public.%s 失败: %v", rel, err)
		}
		if got == "" {
			t.Skipf("关系 public.%s 不存在 —— 本用例的取数源不在当前库里，"+
				"本次**没有**跑真库行为验证（不是通过）", rel)
		}
	}
}

// seedTenant 建一条 tenants 行。
//
// ★ 旧夹具写的是 `INSERT INTO tenants (id, name)` —— tenants **没有 id 列**
// （主键是 code，2026-10-21 真库实测：tenants_pkey PRIMARY KEY (code)）⇒
// 那条语句一直以 42703 失败，又被 `_, _ = db.Exec(...)` 吞掉。
// ⇒ 这次修成真能执行的样子。顺带说明：它对断言无影响 ——
// usage_ledger / session_turns 都没有指向 tenants 的外键
// （真库实测：指向 tenants 的外键只有 output_compliance_policies /
// prompt_injection_policies / session_summaries 三张表）。
func seedTenant(t *testing.T, db *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx,
		`INSERT INTO tenants (code, name) VALUES ($1, $1) ON CONFLICT (code) DO NOTHING`,
		tenantID); err != nil {
		t.Fatalf("插入 tenants 行失败（code=%s）: %v", tenantID, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM tenants WHERE code = $1`, tenantID)
	})
}

// seedUsageLedger 把种子插进 usage_ledger，并在用例结束时按 tenant_id 清掉。
//
// ★ 插入失败一律 t.Fatalf，**不吞**。
// 旧夹具用 `_, _ = db.Exec(...)` ⇒ 插入失败（表不存在 / 列不存在）被吞掉，
// 接着在断言处红 ⇒ 看起来像产品缺陷，实际是夹具漂移。
// 改瞄之后插入失败就是真问题，必须红。
//
// ★ 用 t.Cleanup 而不是用例末尾手写 DELETE：旧写法在 t.Fatalf 之后**不会**
// 执行 ⇒ 失败运行会留下孤儿行 ⇒ 污染下一次运行（见 uniqueTenantID）。
//
// ★ 分区不会成为障碍：usage_ledger 的分区键是 ts（RANGE），且带一个 DEFAULT
// 分区（真库实测：usage_ledger_2026_07/08/10/11 + usage_ledger_default）
// ⇒ 任何 ts 都有落点，不会 23514。
func seedUsageLedger(t *testing.T, db *pgxpool.Pool, tenantID string, seeds []usageLedgerSeed) {
	t.Helper()
	ctx := context.Background()
	for _, s := range seeds {
		// ★ total_tokens 必须显式 cast：$4 + $5 两个参数都是 unknown 类型，
		//   PG 无法解析 `unknown + unknown` ⇒ 42725「operator is not unique」。
		//   （同族：admin/usage_trend_series_test.go 里那条 `$1::timestamptz`
		//   注释说的就是同一件事。）
		_, err := db.Exec(ctx, `
			INSERT INTO usage_ledger (
				request_id, ts, tenant_id, raw_model_name,
				prompt_tokens, completion_tokens, cache_read_tokens,
				total_tokens, cost_usd, latency_ms, success
			) VALUES (
				gen_random_uuid()::text, $1, $2, $3,
				$4, $5, $6,
				($4::int + $5::int), $7, 42, $8
			)
		`, s.ts, tenantID, s.model, s.promptTokens, s.completionTokens,
			s.cacheReadTokens, s.cost, s.success)
		if err != nil {
			t.Fatalf("插入 usage_ledger 种子失败（model=%s）: %v", s.model, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(),
			`DELETE FROM usage_ledger WHERE tenant_id = $1`, tenantID)
	})
}

// seedCompressedTurn 在 session_turns 里插一条带 compression_strategy 的会话回合。
//
// compression_strategy 的原生归属是会话族（890 之后是 session_turns）——
// usage_ledger 是计费宽表，根本没有这一列（usage_enhanced.go:674-719 的注释
// 写明了这一点：在这里 FILTER 它必然 42703）。
//
// ★ NOT NULL 且无默认值的列只有 session_id / turn_no / tenant_id / request_id
// （真库 information_schema 实测，108 列）⇒ 至少要给这四个。
// ★ partition_date 是分区键（date 类型）⇒ 用 YYYY-MM-DD 字符串传，避免
// timestamptz→date 的会话时区偏移。
func seedCompressedTurn(t *testing.T, db *pgxpool.Pool, tenantID string, ts time.Time, strategy string) {
	t.Helper()
	ctx := context.Background()
	sessionID := fmt.Sprintf("sess-%s", tenantID)
	_, err := db.Exec(ctx, `
		INSERT INTO session_turns (
			session_id, turn_no, tenant_id, request_id, ts,
			partition_date, compression_strategy
		) VALUES ($1, 1, $2, $3, $4, $5::date, $6)
	`, sessionID, tenantID, "req-"+sessionID, ts, ts.UTC().Format("2006-01-02"), strategy)
	if err != nil {
		t.Fatalf("插入 session_turns 压缩种子失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(),
			`DELETE FROM session_turns WHERE tenant_id = $1`, tenantID)
	})
}

// tenantRequest 造一个「租户过滤真的生效」的请求。
//
// ★ 不是靠 X-Tenant-ID 头 —— 见文件头那段：EffectiveTenantIDAll 只读
// auth context，header 从来没参与过 SQL。
func tenantRequest(t *testing.T, tenantID, url string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	return SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: tenantID})
}

// ──────────────────────────────────────────────────────────────────────────
// Test 1: GET /api/admin/usage/cost-trend
// ──────────────────────────────────────────────────────────────────────────

func TestUsageCostTrend_ByModel(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	// 种子落点 + 取数源（usage_ledger 与它的双腿视图）。
	requireRelations(t, db, "usage_ledger", "usage_ledger_with_current_month")

	tenantID := uniqueTenantID("cost-trend")
	seedTenant(t, db, tenantID)

	// 窗口是 [今天 UTC 零点, now)（resolveUsageTimeRange 的 days=1 语义）⇒
	// 种子取 now-1min：比 now 稳（不会撞上 end 的开区间边界），
	// 又不会掉出 24 小时宽的窗口。
	now := time.Now().Add(-time.Minute)
	seedUsageLedger(t, db, tenantID, []usageLedgerSeed{
		{model: "gpt-4o", cost: 1.50, promptTokens: 5000, completionTokens: 5000, success: true, ts: now},
		{model: "gpt-4o", cost: 2.00, promptTokens: 7500, completionTokens: 7500, success: true, ts: now},
		{model: "claude-3-5", cost: 0.80, promptTokens: 4000, completionTokens: 4000, success: true, ts: now},
		{model: "gpt-3.5-turbo", cost: 0.20, promptTokens: 2500, completionTokens: 2500, success: true, ts: now},
		{model: "gpt-3.5-turbo", cost: 0.15, promptTokens: 2000, completionTokens: 2000, success: false, ts: now},
	})

	// ★★ 负控：邻居租户的行**必须**不进入本租户的统计。
	//   没有这一行，「租户过滤真的生效」这件事就**没有任何断言在守** ——
	//   去掉 tenantRequest 里的 SetAuthContext 之后本文件照样全绿
	//   （门绿 ≠ 量具有牙）。有了它，去掉租户过滤就会红在「恰好 3 个分组」上。
	neighborTenant := uniqueTenantID("cost-trend-neighbor")
	seedUsageLedger(t, db, neighborTenant, []usageLedgerSeed{
		{model: "gpt-4o-neighbor", cost: 99.00, promptTokens: 900000, completionTokens: 900000, success: true, ts: now},
	})

	h := &Handler{db: db}

	req := tenantRequest(t, tenantID, "/api/admin/usage/cost-trend?group_by=model&days=1")
	w := httptest.NewRecorder()
	h.usageCostTrend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CostTrendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证响应
	if resp.GroupBy != "model" {
		t.Errorf("expected group_by=model, got %s", resp.GroupBy)
	}

	// ★ R80 轮把「至少一条」收紧成「恰好三条 + gpt-4o 合计对」。
	//   旧夹具插不进任何表时 entries 是空的，而旧断言在 >0 时才红 ⇒
	//   一个什么都没查到的端点也能过。改瞄之后才有资格断言确切条数
	//   （长尾合并只在 len(entries) >= 10 时才发生，见 usage_enhanced.go:265）。
	if len(resp.Entries) != 3 {
		t.Fatalf("expected 3 model groups (gpt-4o / claude-3-5 / gpt-3.5-turbo), got %d: %+v",
			len(resp.Entries), resp.Entries)
	}
	byModel := map[string]CostTrendEntry{}
	for _, e := range resp.Entries {
		byModel[e.DimensionValue] = e
	}
	gpt4o, ok := byModel["gpt-4o"]
	if !ok {
		t.Fatalf("gpt-4o group missing: %+v", resp.Entries)
	}
	if math.Abs(gpt4o.TotalCostUSD-3.50) > 0.001 {
		t.Errorf("gpt-4o total cost = %.4f, want 3.50 (1.50 + 2.00)", gpt4o.TotalCostUSD)
	}
	if gpt4o.RequestCount != 2 {
		t.Errorf("gpt-4o request count = %d, want 2", gpt4o.RequestCount)
	}

	// 验证成本排序（降序）
	for i := 1; i < len(resp.Entries); i++ {
		if resp.Entries[i].TotalCostUSD > resp.Entries[i-1].TotalCostUSD {
			t.Errorf("entries not sorted by cost DESC: entry[%d]=%.2f > entry[%d]=%.2f",
				i, resp.Entries[i].TotalCostUSD, i-1, resp.Entries[i-1].TotalCostUSD)
		}
	}
}

func TestUsageCostTrend_ByProvider(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	h := &Handler{db: db}

	// 这个用例**不插种子**：它守的是「group_by=provider 被接受」。
	// 仍然给一个租户上下文，让口径与其它用例一致（而不是查所有租户）。
	tenantID := uniqueTenantID("cost-trend-provider")

	req := tenantRequest(t, tenantID, "/api/admin/usage/cost-trend?group_by=provider&days=7")
	w := httptest.NewRecorder()
	h.usageCostTrend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CostTrendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.GroupBy != "provider" {
		t.Errorf("expected group_by=provider, got %s", resp.GroupBy)
	}
}

func TestUsageCostTrend_InvalidGroupBy(t *testing.T) {
	// ★ R80 轮：这个用例**不查库** —— group_by 非法 ⇒ 在碰库之前就 400 了
	//   （usage_enhanced.go:143-147）。所以没有 DSN 时它照样是证据，
	//   原来它是被文件级守卫连坐成 SKIP 的。
	//   有 DSN 时仍然用真库跑（保留端到端保真度）。
	db := testDB(t)
	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/cost-trend?group_by=invalid", nil)
	w := httptest.NewRecorder()
	h.usageCostTrend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	body := w.Body.String()
	if body == "" {
		t.Error("expected error message in response body")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 2: GET /api/admin/usage/period-compare
// ──────────────────────────────────────────────────────────────────────────

func TestUsagePeriodCompare_MonthOverMonth(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	requireRelations(t, db, "usage_ledger", "usage_ledger_with_current_month")

	tenantID := uniqueTenantID("period-compare")
	seedTenant(t, db, tenantID)

	// ★ R80 轮：周期改成**当月 + 上一个月**（原来写死 2026-07 / 2026-06）。
	//   写死的月份是「绝对数 pin」的一种：它永远只落在历史月分区上，
	//   而生产真正命中的**当月**分区从不被这条门覆盖。
	//   动态周期同时解决「pin 会腐烂」与「当月不设防」两件事。
	now := time.Now().UTC()
	currentPeriod := now.Format("2006-01")
	previousPeriod := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")

	currentStart, err := time.Parse("2006-01", currentPeriod)
	if err != nil {
		t.Fatalf("parse current period %s: %v", currentPeriod, err)
	}
	previousStart, err := time.Parse("2006-01", previousPeriod)
	if err != nil {
		t.Fatalf("parse previous period %s: %v", previousPeriod, err)
	}

	var seeds []usageLedgerSeed
	// 当前月 5 条 × $2.00 = $10.00
	for i := 0; i < 5; i++ {
		seeds = append(seeds, usageLedgerSeed{
			model: "gpt-4o", cost: 2.00, promptTokens: 5000, completionTokens: 5000,
			success: true, ts: currentStart.Add(time.Duration(i) * 24 * time.Hour),
		})
	}
	// 上月 3 条 × $1.50 = $4.50
	for i := 0; i < 3; i++ {
		seeds = append(seeds, usageLedgerSeed{
			model: "gpt-4o", cost: 1.50, promptTokens: 4000, completionTokens: 4000,
			success: true, ts: previousStart.Add(time.Duration(i) * 24 * time.Hour),
		})
	}
	seedUsageLedger(t, db, tenantID, seeds)

	h := &Handler{db: db}

	req := tenantRequest(t, tenantID,
		"/api/admin/usage/period-compare?current="+currentPeriod+"&previous="+previousPeriod)
	w := httptest.NewRecorder()
	h.usagePeriodCompare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp PeriodCompareResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证当前周期
	if resp.Current.Period != currentPeriod {
		t.Errorf("expected current period %s, got %s", currentPeriod, resp.Current.Period)
	}

	// 验证对比周期
	if resp.Previous.Period != previousPeriod {
		t.Errorf("expected previous period %s, got %s", previousPeriod, resp.Previous.Period)
	}

	// ★ R80 轮把这条从 t.Logf（只记不红）收紧成 t.Errorf。
	//   旧夹具插不进任何表时两期都是 0，0 <= 0 成立 ⇒ 这条断言**永远不可能红**
	//   —— 它当时不是断言，是日志。
	if resp.Current.TotalCostUSD <= resp.Previous.TotalCostUSD {
		t.Errorf("expected current cost > previous cost, got current=%.2f, previous=%.2f",
			resp.Current.TotalCostUSD, resp.Previous.TotalCostUSD)
	}
	if resp.Current.TotalRequests != 5 {
		t.Errorf("expected 5 current requests, got %d", resp.Current.TotalRequests)
	}
	if resp.Previous.TotalRequests != 3 {
		t.Errorf("expected 3 previous requests, got %d", resp.Previous.TotalRequests)
	}
	if resp.Trend != "up" {
		t.Errorf("expected trend=up, got %s", resp.Trend)
	}
	if !resp.Significant {
		t.Errorf("expected significant=true (change %.1f%% > 20%%)", resp.ChangePct)
	}
}

func TestUsagePeriodCompare_MissingParams(t *testing.T) {
	// ★ 同上：这个用例不查库（缺参数 ⇒ 在碰库之前就 400，usage_enhanced.go:360-363）。
	db := testDB(t)
	h := &Handler{db: db}

	// 缺少 previous 参数
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/period-compare?current=2026-07", nil)
	w := httptest.NewRecorder()
	h.usagePeriodCompare(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestUsagePeriodCompare_InvalidPeriodFormat(t *testing.T) {
	// ★ 同上：这个用例不查库（周期格式非法 ⇒ parsePeriod 失败 ⇒ 400）。
	db := testDB(t)
	h := &Handler{db: db}

	// 错误的日期格式
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/period-compare?current=2026-07-15&previous=2026-06", nil)
	w := httptest.NewRecorder()
	h.usagePeriodCompare(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 3: GET /api/admin/usage/cache-economics
// ──────────────────────────────────────────────────────────────────────────

func TestUsageCacheEconomics_BasicCalculation(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	requireRelations(t, db, "usage_ledger", "usage_ledger_with_current_month")

	tenantID := uniqueTenantID("cache-economics")
	seedTenant(t, db, tenantID)

	now := time.Now().Add(-time.Minute)

	seedUsageLedger(t, db, tenantID, []usageLedgerSeed{
		// 缓存命中的请求
		{model: "gpt-4o", cost: 1.00, promptTokens: 3000, completionTokens: 5000, cacheReadTokens: 2000, success: true, ts: now},
		// 普通请求（无缓存）
		{model: "gpt-4o", cost: 1.50, promptTokens: 8000, completionTokens: 7000, cacheReadTokens: 0, success: true, ts: now},
	})

	// ★★ 负控：邻居租户的一行，数字全部不同。任何一项被算进来，
	//   下面那三个精确断言（2 条请求 / 2000 cache_read / 11000 prompt）
	//   就会红 ⇒ 它们因此同时在守「租户过滤真的生效」。
	neighborTenant := uniqueTenantID("cache-economics-neighbor")
	seedUsageLedger(t, db, neighborTenant, []usageLedgerSeed{
		{model: "gpt-4o", cost: 99.00, promptTokens: 900000, completionTokens: 900000, cacheReadTokens: 888888, success: true, ts: now},
	})

	h := &Handler{db: db}

	req := tenantRequest(t, tenantID, "/api/admin/usage/cache-economics?days=1")
	w := httptest.NewRecorder()
	h.usageCacheEconomics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CacheEconomicsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证基础字段
	if resp.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", resp.TotalRequests)
	}

	if resp.CacheReadTokens != 2000 {
		t.Errorf("expected 2000 cache_read_tokens, got %d", resp.CacheReadTokens)
	}

	if resp.PromptTokens != 11000 { // 3000 + 8000
		t.Errorf("expected 11000 prompt_tokens, got %d", resp.PromptTokens)
	}

	// 验证缓存命中率计算
	// cache_hit_ratio = 2000 / (2000 + 11000) = 0.1538...
	expectedRatio := 2000.0 / 13000.0
	if resp.CacheHitRatio < expectedRatio-0.01 || resp.CacheHitRatio > expectedRatio+0.01 {
		t.Errorf("expected cache_hit_ratio ~%.4f, got %.4f", expectedRatio, resp.CacheHitRatio)
	}

	// 验证节省金额（应该 > 0）
	if resp.DollarsSaved <= 0 {
		t.Errorf("expected dollars_saved > 0, got %.4f", resp.DollarsSaved)
	}

	// 验证有效成本占比（应该 < 1）
	if resp.EffectiveCostRatio >= 1.0 {
		t.Errorf("expected effective_cost_ratio < 1.0, got %.4f", resp.EffectiveCostRatio)
	}
}

func TestUsageCacheEconomics_NoCache(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	requireRelations(t, db, "usage_ledger", "usage_ledger_with_current_month")

	tenantID := uniqueTenantID("no-cache")
	seedTenant(t, db, tenantID)

	now := time.Now().Add(-time.Minute)

	seedUsageLedger(t, db, tenantID, []usageLedgerSeed{
		{model: "gpt-3.5-turbo", cost: 0.50, promptTokens: 2000, completionTokens: 3000, cacheReadTokens: 0, success: true, ts: now},
	})

	h := &Handler{db: db}

	req := tenantRequest(t, tenantID, "/api/admin/usage/cache-economics?days=1")
	w := httptest.NewRecorder()
	h.usageCacheEconomics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CacheEconomicsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 无缓存时，命中率应该为 0
	if resp.CacheHitRatio != 0.0 {
		t.Errorf("expected cache_hit_ratio = 0.0 with no cache, got %.4f", resp.CacheHitRatio)
	}

	// 无缓存时，节省应该为 0
	if resp.DollarsSaved != 0.0 {
		t.Errorf("expected dollars_saved = 0.0 with no cache, got %.4f", resp.DollarsSaved)
	}
}

func TestUsageCacheEconomics_WithCompression(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}

	// 压缩计数读 session_turns.compression_strategy ⇒ 这一条要多问一个关系。
	requireRelations(t, db, "usage_ledger", "usage_ledger_with_current_month", "session_turns")

	tenantID := uniqueTenantID("compression")
	seedTenant(t, db, tenantID)

	now := time.Now().Add(-time.Minute)

	seedUsageLedger(t, db, tenantID, []usageLedgerSeed{
		{model: "gpt-4o", cost: 1.20, promptTokens: 5000, completionTokens: 7000, cacheReadTokens: 0, success: true, ts: now},
	})
	// 压缩标记在会话族上，不在计费宽表上。
	seedCompressedTurn(t, db, tenantID, now, "smart")

	h := &Handler{db: db}

	req := tenantRequest(t, tenantID, "/api/admin/usage/cache-economics?days=1")
	w := httptest.NewRecorder()
	h.usageCacheEconomics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CacheEconomicsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// 验证压缩请求数
	if resp.CompressedRequests != 1 {
		t.Errorf("expected 1 compressed request, got %d", resp.CompressedRequests)
	}

	// 验证压缩节省（应该 > 0）
	if resp.CompressionSaved <= 0 {
		t.Errorf("expected compression_saved > 0, got %.4f", resp.CompressionSaved)
	}

	// 验证总节省
	if resp.TotalSaved != resp.DollarsSaved+resp.CompressionSaved {
		t.Errorf("total_saved mismatch: expected %.4f, got %.4f",
			resp.DollarsSaved+resp.CompressionSaved, resp.TotalSaved)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Helper: testDB 返回测试数据库连接
// ──────────────────────────────────────────────────────────────────────────

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	// ★ R70 轮：原先**写死** `postgres://localhost/llm_gateway_test`（**完全无视 env**）⇒
	//   在「本机恰好有那个库、且当前 OS 用户有权限」的机器上会**真的连上去**；
	//   连不上时只 t.Logf ⇒ **静默 SKIP 被 `go test` 记成绿**（R69 §19.18）。
	//   ⇒ 改成认事实源（scripts/audit/datasource_env.sh）；未设 ⇒ 不连、明确说明。
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Log("TEST_DATABASE_URL not set —— 本门没有连库（请 source scripts/audit/datasource_env.sh \"$DSN\"）")
		return nil
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Logf("Skipping test: cannot connect to test database: %v", err)
		return nil
	}
	// 池必须回收：原来每个用例都新建一个池且从不关 ⇒ 长跑会攒连接。
	t.Cleanup(db.Close)

	// 测试连接
	if err := db.Ping(ctx); err != nil {
		t.Logf("Skipping test: cannot ping test database: %v", err)
		return nil
	}

	// ★ R80 轮：**这里原来还有一道文件级守卫**（R70 轮加的）—— 查
	//   `public._request_logs` 在不在，不在就**整个文件** SKIP。
	//   ⇒ 它连坐了三处（详见 requireRelations 的注释）：
	//     ① 本文件 9 个用例里有 4 个根本不插种子；
	//     ② helper 是共享的 ⇒ admin/usage_trend_series_test.go 的 2 个用例
	//        也被挡住（它们的种子在 request_stats_dim_minute，另一个族）；
	//     ③ 守卫问的是「v1 族在不在」，而这三个端点的取数源从来不是 v1 族。
	//   ⇒ 守卫下沉到种子级，由每个用例按自己真正需要的关系统一地问。
	return db
}
