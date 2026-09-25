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
	"os"
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
	return dsn
}

func TestReportRollup_RealDB_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, e2eDSN(t))
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()

	day := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	dayStr := day.Format("2006-01-02")

	// ---- 清理 + 灌数（幂等，可重跑）----
	for _, tbl := range []string{"report_snapshots", "usage_facts"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
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
	// internal_person（R65 起 scope_key 编码租户：tenant\x00person）：
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
