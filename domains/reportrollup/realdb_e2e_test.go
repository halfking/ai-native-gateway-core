package reportrollup

// realdb_e2e_test.go —— 对账报表全链路真库回归（2026-09-25 落地轮）。
//
// 路径：真 PG（scratch 库）+ 迁移 536/537/745/746 建表 → 灌合成
// usage_facts（success/failure/rate_limited × business/probe × 双租户 ×
// 双模型，含缓存 token/成本/积分/延迟/error_kind）→ RollupDay 真聚合 →
// report_snapshots 断言（scope×key×计数×透视×冻结价）→ BuildRangeReport
// 区间汇总 → BuildWorkbookBytes 产出合法 xlsx。
//
// SQL 正确性（jsonb GROUP BY、percentile FILTER、ON CONFLICT 四键）只有
// 真 PG 能验证——pgxmock 单测不覆盖这一层。
//
// 门控：TEST_DATABASE_URL / TEST_DB_URL 未设置时跳过。目标库应为一次性
// scratch 库（本测试只写 usage_facts/report_snapshots 并在结束时清理行）。

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func e2eDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库 E2E")
	}
	guardDestructiveE2E(t, dsn)
	return dsn
}

// guardDestructiveE2E 拒绝把「有真实数据」的库当 scratch 库。
//
// 本测试开头就 DELETE report_snapshots / usage_facts——它自己文件头写着的
// 前置条件是「目标库应为一次性 scratch 库」，但那只是注释，没有任何机制
// 拦住误用。2026-09-29 实测踩过：本地开发库 llm_gateway 被当 scratch 库
// 传进来，近 30 天约 97.8 万行 usage_facts 一夜清零（生产库未受影响，本地
// 开发环境的历史用量只能等真实流量重新积累）。
//
// 放行条件（二选一，都要求显式表态）：
//
//	· 库名含 test / scratch / tmp / e2e —— 惯用的一次性库命名；
//	· 显式设置 REPORT_E2E_ALLOW_DESTRUCTIVE=1，表示「我知道我在干什么」。
func guardDestructiveE2E(t *testing.T, dsn string) {
	t.Helper()
	if os.Getenv("REPORT_E2E_ALLOW_DESTRUCTIVE") == "1" {
		return
	}
	if isScratchDB(dsn) {
		return
	}
	t.Fatalf("拒绝在库 %q 上跑破坏性 E2E：它会 DELETE report_snapshots / usage_facts。\n"+
		"请指向一次性 scratch 库（库名含 test/scratch/tmp/e2e），或显式设置 REPORT_E2E_ALLOW_DESTRUCTIVE=1。",
		dsnDatabaseName(dsn))
}

// isScratchDB 是护栏的纯判断部分，单独抽出来是为了能直接测它。
//
// 不抽出来就只能靠「跑一次护栏看它红不红」来验证，而断言「子测试应当失败」
// 在 Go 里没有干净写法——于是这道护栏曾经**零测试**：谁把它放宽成允许
// llm_gateway 也不会有任何测试变红，而它的失效后果是 DELETE 掉一整个库。
//
// 收紧的理由（不是洁癖，是有实例）：早先的规则是「库名含 test/scratch/
// tmp/e2e 任一子串即放行」。但本机就存在两个**长期**库 llm_gateway_test 与
// llm_gateway_test_r112——按子串规则它们都会被当成一次性 scratch 库然后被
// DELETE。测试（见 TestIsScratchDB）第一次就逮到了这条。
//
// 现在的规则：**只认 scratch / e2e 两个无歧义标记，且必须按 `_`/`-` 分词后
// 整词命中**。test / tmp 仍被大量长期库使用，不再隐式放行；要跑就显式设
// REPORT_E2E_ALLOW_DESTRUCTIVE=1，那是「我知道我在干什么」的表态。
func isScratchDB(dsn string) bool {
	name := strings.ToLower(dsnDatabaseName(dsn))
	for _, tok := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		switch tok {
		case "scratch", "e2e":
			return true
		}
	}
	return false
}

// dsnDatabaseName 从 DSN 里取出库名；URL 与 key=value 两种写法都支持。
func dsnDatabaseName(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		return strings.TrimPrefix(u.Path, "/")
	}
	for _, kv := range strings.Fields(dsn) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "dbname" {
			return v
		}
	}
	return "<unknown>"
}

func TestReportRollup_RealDB_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, e2eDSN(t))
	if err != nil {
		// DSN 显式指定却连不上 = 配错，不是「本测试不适用」。早先 Skip 会让
		// 库名打错时整条破坏性 E2E 静默消失，而包级仍报 ok。
		t.Fatalf("connect（DSN 已指定，不该静默跳过）: %v", err)
	}
	defer pool.Close()

	day := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	dayStr := day.Format("2006-01-02")

	// ---- 清理 + 灌数（幂等，可重跑）----
	//
	// 表不存在时跳过而不是报错：既然护栏已经强制「必须是一次性 scratch
	// 库」，那么「全新空库」就是最正常的目标库形态——原实现直接 DELETE 会
	// 在 42P01 上失败，等于这个测试只能在跑过迁移的库上跑，与护栏的意图
	// 正好相反。真正的建表在后面（CREATE TABLE IF NOT EXISTS）。
	for _, tbl := range []string{"report_snapshots", "usage_facts"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT to_regclass('public."+tbl+"') IS NOT NULL").Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", tbl, err)
		}
		if !exists {
			continue
		}
		if _, err := pool.Exec(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
	// usage_facts / report_snapshots 在 scratch 库里可能根本不存在——本测试
	// 只依赖这两张表的少数几列，用最小自足 DDL 建出来即可（与迁移 537/745+
	// 746+759 的列定义一致；完整形状由迁移与 ensureReportSnapshots 负责）。
	for _, ddl := range []string{e2eUsageFactsDDL, e2eReportSnapshotsDDL} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("ensure scratch tables: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS maas_settings (
		id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
		cents_per_credit NUMERIC(10, 4) NOT NULL DEFAULT 0.1) `); err != nil {
		t.Fatalf("ensure maas_settings: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO maas_settings (id, cents_per_credit) VALUES (1, 0.2)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed maas_settings: %v", err)
	}

	// 合成数据：tenantA/alice 成功 2 笔 + timeout 失败 1 笔（business），
	// tenantA/alice rate_limited 1 笔，tenantB/alice 成功 1 笔（business——
	// R65 P1 钉桩：跨租户同人，scope_key 不编码租户则两桶同键互相覆盖），
	// provider 未落定失败 1 笔（只进 daily_total）。
	seed := []struct {
		tenant, person, model string
		provider              any
		status, kind          string
		prompt, completion    int64
		credits               int64
		costCents             float64
		latency               int64
	}{
		{"tenantA", "alice", "gpt-x", int64(11), "success", "", 1000, 500, 100, 5.0, 800},
		{"tenantA", "alice", "gpt-x", int64(11), "success", "", 2000, 800, 200, 9.5, 1200},
		{"tenantA", "alice", "gpt-x", int64(11), "failure", "timeout", 500, 0, 0, 0.5, 30000},
		{"tenantA", "alice", "gpt-y", int64(12), "rate_limited", "", 0, 0, 0, 0, 0},
		{"tenantB", "alice", "gpt-x", int64(11), "success", "", 999, 99, 50, 3.3, 600},
		{"tenantA", "alice", "gpt-x", nil, "failure", "auth", 0, 0, 0, 0, 0},
	}
	for i, s := range seed {
		occurred := day.Add(time.Duration(10+i) * time.Minute)
		if _, err := pool.Exec(ctx, `
			INSERT INTO usage_facts (event_id, request_id, occurred_at, tenant_id, traffic_class,
				status, provider_id, raw_model_name, end_user_id, person_hash,
				prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
				cost_amount, cost_currency, credits_charged, latency_ms, ttft_ms,
				error_kind, error_class)
			VALUES ($1,$2,$3,$4,'business',$5,$6,$7,$8,
			        CASE WHEN $8 = '' THEN NULL ELSE 'ph' || $8 END,
			        $9,$10,50,20,$11,'USD',$12,$13,100,
			        NULLIF($14,''), NULLIF($14,''))
		`,
			fmt.Sprintf("evt-%s-%d", dayStr, i), fmt.Sprintf("req-%d", i), occurred,
			s.tenant, s.status, s.provider, s.model, s.person,
			s.prompt, s.completion, s.costCents, s.credits, s.latency, s.kind,
		); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	// ---- RollupDay ----
	stats, err := RollupDay(ctx, pool, day)
	if err != nil {
		t.Fatalf("RollupDay: %v", err)
	}
	if stats.RequestsSeen != int64(len(seed)) {
		t.Errorf("requests_seen = %d, want %d", stats.RequestsSeen, len(seed))
	}

	// ---- report_snapshots 断言 ----
	type row struct {
		scope, scopeKey, model string
		req, ok, err           int64
		input                  int64
		credits                int64
		breakdown              map[string]int64
	}
	rows, err := pool.Query(ctx, `
		SELECT scope, scope_key, raw_model_name, request_count, success_count, error_count,
		       input_tokens, credits_charged, error_kind_breakdown::text
		FROM report_snapshots WHERE report_date = $1 ORDER BY scope, scope_key, raw_model_name`, day)
	if err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		var bd []byte
		if err := rows.Scan(&r.scope, &r.scopeKey, &r.model, &r.req, &r.ok, &r.err,
			&r.input, &r.credits, &bd); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if err := decodeBreakdown(bd, &r.breakdown); err != nil {
			t.Fatalf("decode breakdown: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	find := func(scope, key, model string) *row {
		for i := range got {
			if got[i].scope == scope && got[i].scopeKey == key && got[i].model == model {
				return &got[i]
			}
		}
		return nil
	}

	// daily_total：全部 6 笔（含 provider 未落定）。
	total := find("daily_total", "all", "")
	if total == nil || total.req != 6 || total.ok != 3 || total.err != 3 {
		t.Errorf("daily_total = %+v, want 6/3/3", total)
	}
	// daily_by_provider：provider 11 全部流量 4 笔（3 成功 1 timeout）。
	p11 := find("daily_by_provider", "11", "")
	if p11 == nil || p11.req != 4 || p11.ok != 3 {
		t.Errorf("daily_by_provider/11 = %+v, want 4/3", p11)
	}
	// daily_by_model：provider11 × gpt-x 4 笔 = tenantA 3 笔 + tenantB
	// 探针 1 笔（provider 面含全部流量类——探针同样烧供应商钱）。
	pm := find("daily_by_model", "11", "gpt-x")
	if pm == nil || pm.req != 4 || pm.ok != 3 || pm.input != 4499 || pm.breakdown["timeout"] != 1 {
		t.Errorf("daily_by_model/11/gpt-x = %+v, want 4/3 input 4499", pm)
	}
	// internal_tenant：tenantA 仅 business 5 笔（probe 的 tenantB 不计），
	// credits = 100+200+0+0+0 = 300。
	ta := find("internal_tenant", "tenantA", "")
	if ta == nil || ta.req != 5 || ta.ok != 2 || ta.credits != 300 {
		t.Errorf("internal_tenant/tenantA = %+v, want 5/2 credits 300", ta)
	}
	tb := find("internal_tenant", "tenantB", "")
	if tb == nil || tb.req != 1 {
		t.Errorf("internal_tenant/tenantB = %+v, want 1", tb)
	}
	// internal_person（scope_key 编码租户，长度前缀格式 len:tenant:person）：
	// tenantA/alice 5 笔 + tenantB/alice 1 笔——跨租户同人必须各自成桶。
	alice := find("internal_person", internalPersonScopeKey("tenantA", "alice"), "")
	if alice == nil || alice.req != 5 {
		t.Errorf("internal_person/tenantA·alice = %+v, want 5", alice)
	}
	aliceB := find("internal_person", internalPersonScopeKey("tenantB", "alice"), "")
	if aliceB == nil || aliceB.req != 1 {
		t.Errorf("internal_person/tenantB·alice = %+v, want 1", aliceB)
	}
	var personRows int
	for i := range got {
		if got[i].scope == "internal_person" {
			personRows++
		}
	}
	if personRows != 2 {
		t.Errorf("internal_person rows = %d, want 2 (cross-tenant same person must not collide)", personRows)
	}
	// internal_model：tenantA × gpt-x 4 笔（2 成功 2 失败）。
	im := find("internal_model", "tenantA", "gpt-x")
	if im == nil || im.req != 4 || im.ok != 2 || im.err != 2 {
		t.Errorf("internal_model/tenantA/gpt-x = %+v, want 4/2/2", im)
	}

	// 幂等回填：重跑一次，行数不翻倍。
	if _, err := RollupDay(ctx, pool, day); err != nil {
		t.Fatalf("RollupDay rerun: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM report_snapshots WHERE report_date = $1`, day).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != len(got) {
		t.Errorf("rerun duplicated rows: before %d after %d", len(got), count)
	}

	// ---- 区间汇总 + Excel ----
	rep, err := BuildRangeReport(ctx, pool, day, day, ViewInternal, RangeFilter{}, nil)
	if err != nil {
		t.Fatalf("BuildRangeReport internal: %v", err)
	}
	if rep.Totals.RequestCount != 6 || rep.Totals.CreditsCharged != 350 {
		t.Errorf("internal totals = %d req / %d credits, want 6/350", rep.Totals.RequestCount, rep.Totals.CreditsCharged)
	}
	// 内部金额：350 credits × 0.2 分 = 70 分。
	if rep.Totals.InternalCostCents != 70 {
		t.Errorf("internal cost cents = %v, want 70", rep.Totals.InternalCostCents)
	}

	repP, err := BuildRangeReport(ctx, pool, day, day, ViewProvider, RangeFilter{}, map[int64]string{11: "Provider Eleven"})
	if err != nil {
		t.Fatalf("BuildRangeReport provider: %v", err)
	}
	if repP.Totals.RequestCount != 6 || repP.Totals.EstimatedCostCents != 1830 {
		t.Errorf("provider totals = %d req / %d cents, want 6/1830 (5.0+9.5+0.5+0+3.3+0 分×100… 见 seed)",
			repP.Totals.RequestCount, repP.Totals.EstimatedCostCents)
	}

	xlsxBytes, err := BuildWorkbookBytes(repP)
	if err != nil {
		t.Fatalf("BuildWorkbookBytes: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(xlsxBytes), int64(len(xlsxBytes)))
	if err != nil {
		t.Fatalf("xlsx not a valid zip: %v", err)
	}
	if len(zr.File) < 7 {
		t.Errorf("xlsx members = %d, want >= 7", len(zr.File))
	}
	xlsxBytes2, err := BuildWorkbookBytes(rep)
	if err != nil {
		t.Fatalf("BuildWorkbookBytes internal: %v", err)
	}
	if len(xlsxBytes2) == 0 {
		t.Errorf("internal workbook empty")
	}

	// ---- 事务路径（worker RollupDate 的封装形态）：pgx.Tx 满足 Querier，
	// 单日聚合在真库上以事务提交，中途失败整体回滚 ----
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := RollupDay(ctx, tx, day.AddDate(0, 0, -1)); err != nil {
		t.Fatalf("RollupDay on tx: %v", err)
	}
	// 回滚验证：回滚后昨日无任何快照行（事务原子性真库证据）。
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback tx: %v", err)
	}
	var txRows int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM report_snapshots WHERE report_date = $1`,
		day.AddDate(0, 0, -1)).Scan(&txRows); err != nil {
		t.Fatalf("count after rollback: %v", err)
	}
	if txRows != 0 {
		t.Errorf("rows survived rollback: %d", txRows)
	}

	// ---- 追赶探测（MissingRollupDates）：昨日+前日无 daily_total 行，
	// 应被探测为缺失；已聚合的 day 不应出现 ----
	missing, err := MissingRollupDates(ctx, pool, 7, time.Now().UTC())
	if err != nil {
		t.Fatalf("MissingRollupDates: %v", err)
	}
	missStr := map[string]bool{}
	for _, d := range missing {
		missStr[d.Format("2006-01-02")] = true
	}
	if !missStr[day.AddDate(0, 0, -1).Format("2006-01-02")] {
		t.Errorf("yesterday %s should be detected missing", day.AddDate(0, 0, -1).Format("2006-01-02"))
	}
	if missStr[day.Format("2006-01-02")] {
		t.Errorf("aggregated day %s must not be reported missing", day.Format("2006-01-02"))
	}

	// ---- 清理（不留合成数据）----
	if _, err := pool.Exec(ctx, "DELETE FROM report_snapshots"); err != nil {
		t.Fatalf("cleanup snapshots: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM usage_facts"); err != nil {
		t.Fatalf("cleanup facts: %v", err)
	}
}

// e2eUsageFactsDDL 是真库 E2E 依赖的 usage_facts 最小列集（列名/类型与
// 迁移 537 一致；分区与索引省略——本测试只按 occurred_at 灌单日数据）。
const e2eUsageFactsDDL = `
CREATE TABLE IF NOT EXISTS usage_facts (
    fact_id           bigserial,
    event_id          text NOT NULL,
    request_id        text NOT NULL,
    revision          integer NOT NULL DEFAULT 1,
    occurred_at       timestamptz NOT NULL,
    finalized_at      timestamptz NOT NULL DEFAULT now(),
    tenant_id         text NOT NULL DEFAULT 'default',
    traffic_class     text NOT NULL DEFAULT 'business',
    status            text NOT NULL,
    provider_id       bigint,
    credential_id     bigint,
    canonical_id      bigint,
    raw_model_name    text,
    api_key_id        bigint,
    application_id    bigint,
    end_user_id       text,
    person_hash       text,
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    cost_amount       numeric(20,8) NOT NULL DEFAULT 0,
    cost_currency     text,
    credits_charged   bigint NOT NULL DEFAULT 0,
    latency_ms        bigint NOT NULL DEFAULT 0,
    ttft_ms           bigint NOT NULL DEFAULT 0,
    error_kind        text,
    error_class       text,
    failure_stage     text,
    PRIMARY KEY (fact_id)
)`

// e2eReportSnapshotsDDL 是 759 之后的 report_snapshots 最小列集。
//
// 用 DROP + CREATE 而不是 CREATE TABLE IF NOT EXISTS：IF NOT EXISTS 在表已
// 存在时会**沿用旧形状**，于是任何一次运行前遗留的、形状不对的表都会让本测试
// 以「column "cache_read_tokens" does not exist」永久失败——它无法自愈，且失败
// 原因离真因很远。另一个测试若在同库重建过这张表（本轮新增的
// TestMigration759_AppliesToRealScratchDB 就会），就正好制造这种残留。
const e2eReportSnapshotsDDL = `
DROP TABLE IF EXISTS report_snapshots CASCADE;
CREATE TABLE report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,
    scope_key           TEXT NOT NULL,
    report_date         DATE NOT NULL,
    raw_model_name      TEXT NOT NULL DEFAULT '',
    granularity         TEXT NOT NULL DEFAULT 'day',
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    cache_hit_ratio     NUMERIC(6,4),
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,
    canonical_id        BIGINT,
    tenant_id           TEXT,
    credential_id       BIGINT,
    api_key_id          BIGINT,
    person              TEXT,
    credits_charged     BIGINT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
)`

// ── 护栏自身的门 ────────────────────────────────────────────────────────────
//
// guardDestructiveE2E 拦的是「DELETE report_snapshots / usage_facts 打到一个
// 真实库上」。2026-09-29 实测踩过一次：本地开发库被当 scratch 库传进来，
// 近 30 天约 97.8 万行 usage_facts 一夜清零。
//
// 它此前零测试——护栏失效（被放宽、被改名、marker 列表被清空）不会有任何
// 现有测试变红，而失效的后果是删库。这里把判断提成纯函数 isScratchDB 后
// 直接测它。
func TestIsScratchDB(t *testing.T) {
	dsn := func(db string) string {
		return "postgres://u:p@127.0.0.1:5432/" + db + "?sslmode=disable"
	}
	cases := []struct {
		db   string
		want bool
		why  string
	}{
		// 必须放行：scratch / e2e 两个无歧义标记。
		{"llm_gateway_scratch_e2e", true, "本轮实际使用的 scratch 库"},
		{"scratch_a", true, "一次性库"},
		{"report_grain_e2e", true, "e2e 结尾"},
		{"E2E_TMP", true, "大小写不敏感 + 其它分隔符"},
		// 必须拦下：真实/共享库。这是 2026-09-29 真踩过的那一类。
		{"llm_gateway", false, "本地开发库，有真实数据"},
		{"llm_gateway_sync_20260718", false, "同步库，有真实数据"},
		// 本机真实存在的两个长期库：名字含 test，但绝不能被当成一次性库 DELETE。
		{"llm_gateway_test", false, "长期测试库，不是 scratch"},
		{"llm_gateway_test_r112", false, "长期测试库，test 出现在词中间"},
		{"tmp_report", false, "tmp 仍被长期库使用，改走显式环境变量"},
		// 纯子串误判：contest 含 test、estuary 含 tmp，都必须拦下。
		{"contest", false, "test 只是子串"},
		{"estuary", false, "tmp 只是子串"},
		{"postgres", false, "库名无法表明它是一次性库"},
	}
	for _, c := range cases {
		if got := isScratchDB(dsn(c.db)); got != c.want {
			t.Errorf("isScratchDB(%q) = %v，期望 %v（%s）", c.db, got, c.want, c.why)
		}
	}
}

// 已收紧的行为固化：marker 必须是**整词**，且只认 scratch / e2e。
// 写成测试是为了让「顺手改回子串匹配」也会变红。
func TestIsScratchDB_SubstringIsNotEnough(t *testing.T) {
	// 名字里偶然含 test/tmp 的一词，不足以放行——护栏宁可多拦，
	// 因为放行的代价是 DELETE 掉一整个长期库。
	for _, db := range []string{"contest", "estuary", "latest_tmp"} {
		if isScratchDB("postgres://u:p@h:5432/" + db + "?x=1") {
			t.Errorf("%q 应被拦下（子串不是整词）", db)
		}
	}
	// 大小写不敏感是刻意的：PG 库名未加引号时会被折叠成小写。
	if !isScratchDB("postgres://u:p@h:5432/SCRATCH?x=1") {
		t.Error("SCRATCH 应被放行（大小写不敏感）")
	}
	// DSN 里带端口/参数不能干扰库名提取——否则 query 里出现 scratch
	// 就能把真实库伪装成 scratch。
	if isScratchDB("postgres://u:p@h:5432/llm_gateway?options=-c%20search_path%3Dscratch") {
		t.Error("库名解析把 query 参数里的 scratch 算进去了——会误放行真实库")
	}
	// 空/畸形 DSN 不得被当成 scratch（默认拒绝）。
	if isScratchDB("") {
		t.Error("空 DSN 不应放行")
	}
}
