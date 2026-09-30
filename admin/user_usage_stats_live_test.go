package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// user_usage_stats_live_test.go — live-DB regression for the /users 用户级
// 统计端点（2026-09-30 统计 UI 优化轮）。
//
// 端点读面 = usage_facts（rollup 快照同源事实表）× api_keys.owner_user，
// SQL 形状（generate_series 零填充 / percentile_cont / FILTER 分桶 /
// owner→users 收敛）按 models_alias_sql_live_test.go 模式对真实 PostgreSQL 锁定：
//
//   - usage-summary：owner 非 users 表账号（zz-ustats-other）必须被
//     JOIN 过滤掉（owner 标记 ≠ 平台账号是常态）。
//   - {id}/stats：KPI 分桶（success/非 success）、P95、daily 零填充、
//     Top 模型/应用/密钥、最近请求（ttft/总耗时）。
//
// Run with:
//
//	TEST_DATABASE_URL=postgres://... go test -count=1 -run TestUserUsageStats_Live ./admin
func TestUserUsageStats_Live(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live-DB regression")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const owner = "zz-ustats"
	const ownerNotAUser = "zz-ustats-other"
	const appCode = "zz-ustats-app"

	cleanup := func() {
		stmts := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM usage_facts WHERE event_id LIKE 'zz-ustats-%'`, nil},
			{`DELETE FROM api_keys WHERE key_prefix LIKE 'zz-k%'`, nil},
			{`DELETE FROM applications WHERE code = $1`, []any{appCode}},
			{`DELETE FROM users WHERE username IN ($1, $2)`, []any{owner, ownerNotAUser}},
		}
		for _, s := range stmts {
			//nolint:errcheck // 清场 best-effort；占位符与参数一一对应
			pool.Exec(ctx, s.sql, s.args...)
		}
	}
	cleanup()
	defer cleanup()

	var userID, appID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, role, enabled)
		VALUES ('default', $1, 'x', 'ZZ UStats', 'tenant_admin', true)
		RETURNING id
	`, owner).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO applications (code, display_name) VALUES ($1, 'ZZ UStats App') RETURNING id
	`, appCode).Scan(&appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	// key1/key2 归属 zz-ustats；key3 归属一个不存在于 users 的 owner 标记。
	seedKeys := `
		INSERT INTO api_keys (application_id, key_hash, key_prefix, key_alias, owner_user, enabled, created_at)
		VALUES ($1, 'x1', 'zz-k1', 'zz-alias-1', $2, true, now()),
		       ($1, 'x2', 'zz-k2', 'zz-alias-2', $2, true, now()),
		       ($1, 'x3', 'zz-k3', 'zz-alias-3', $3, true, now())
		RETURNING id`
	keyRows, err := pool.Query(ctx, seedKeys, appID, owner, ownerNotAUser)
	if err != nil {
		t.Fatalf("seed api_keys: %v", err)
	}
	keyIDs := make([]int, 0, 3)
	for keyRows.Next() {
		var kid int
		if err := keyRows.Scan(&kid); err != nil {
			t.Fatalf("seed api_keys scan: %v", err)
		}
		keyIDs = append(keyIDs, kid)
	}
	keyRows.Close()
	if len(keyIDs) != 3 {
		t.Fatalf("seed api_keys: got %d ids, want 3", len(keyIDs))
	}
	key1, key2, key3 := keyIDs[0], keyIDs[1], keyIDs[2]

	// 今日 2 行（1 成功 1 失败，key1）、昨日 1 行（key2）；key3 的行必须被排除。
	seedFacts := `
		INSERT INTO usage_facts (event_id, request_id, occurred_at, tenant_id, api_key_id, application_id,
		                         status, raw_model_name, total_tokens, credits_charged, cost_amount, latency_ms, ttft_ms)
		VALUES
		 ('zz-ustats-e1', 'zz-ustats-r1', now(),                    'default', $1, $4, 'success', 'claude-opus-5', 100, 10, 0.5, 1200, 400),
		 ('zz-ustats-e2', 'zz-ustats-r2', now(),                    'default', $1, $4, 'failure', 'claude-opus-5',  50,  0, 0.0, 9000, 0),
		 ('zz-ustats-e3', 'zz-ustats-r3', now() - INTERVAL '1 day', 'default', $2, NULL, 'success', 'gpt-5.6-terra',  70,  7, 0.3,  800, 300),
		 ('zz-ustats-e4', 'zz-ustats-r4', now(),                    'default', $3, $4, 'success', 'grok-4.7',      999, 99, 9.9,  100,  50)`
	if _, err := pool.Exec(ctx, seedFacts, key1, key2, key3, appID); err != nil {
		t.Fatalf("seed usage_facts: %v", err)
	}

	h := &Handler{db: pool}

	// ── usage-summary：非账号 owner 行被 JOIN 过滤 ─────────
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/users/usage-summary?days=7", nil)
	h.handleUserUsageSummary(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage-summary status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var summary struct {
		Days  int `json:"days"`
		Items []struct {
			Username   string     `json:"username"`
			Requests   int64      `json:"requests"`
			Tokens     int64      `json:"tokens"`
			Credits    int64      `json:"credits"`
			LastActive *time.Time `json:"last_active_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.Days != 7 {
		t.Fatalf("summary days = %d, want 7", summary.Days)
	}
	var mine *struct {
		Username   string     `json:"username"`
		Requests   int64      `json:"requests"`
		Tokens     int64      `json:"tokens"`
		Credits    int64      `json:"credits"`
		LastActive *time.Time `json:"last_active_at"`
	}
	for i := range summary.Items {
		if summary.Items[i].Username == owner {
			mine = &summary.Items[i]
		}
		if summary.Items[i].Username == ownerNotAUser {
			t.Fatalf("usage-summary must exclude non-account owner markers, got %s", ownerNotAUser)
		}
	}
	if mine == nil {
		t.Fatalf("usage-summary missing seeded owner %s: %s", owner, rec.Body.String())
	}
	if mine.Requests != 3 || mine.Tokens != 220 || mine.Credits != 17 {
		t.Fatalf("usage-summary aggregate = %+v, want requests=3 tokens=220 credits=17", mine)
	}
	if mine.LastActive == nil {
		t.Fatalf("usage-summary last_active must be set")
	}

	// ── {id}/stats：KPI / daily / top / recent ────────────
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/users/"+strconv.Itoa(userID)+"/stats?days=7", nil)
	h.handleUserStatsDispatcher(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("user stats status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	var detail struct {
		Username string `json:"username"`
		Days     int    `json:"days"`
		Kpi      struct {
			Requests     int64   `json:"requests"`
			Tokens       int64   `json:"tokens"`
			Credits      int64   `json:"credits"`
			Errors       int64   `json:"errors"`
			ErrorRate    float64 `json:"error_rate"`
			LatencyP95Ms *int64  `json:"latency_p95_ms"`
		} `json:"kpi"`
		Daily []struct {
			Date     string `json:"date"`
			Requests int64  `json:"requests"`
			Errors   int64  `json:"errors"`
		} `json:"daily"`
		TopModels []struct {
			Name     string `json:"name"`
			Requests int64  `json:"requests"`
		} `json:"top_models"`
		TopApps []struct {
			Name     string `json:"name"`
			Requests int64  `json:"requests"`
		} `json:"top_apps"`
		TopKeys []struct {
			Name     string `json:"name"`
			Requests int64  `json:"requests"`
		} `json:"top_keys"`
		Recent []struct {
			Model        string `json:"model"`
			Status       string `json:"status"`
			Credits      int64  `json:"credits"`
			FirstChunkMs *int64 `json:"first_chunk_ms"`
			TotalMs      *int64 `json:"total_ms"`
		} `json:"recent_requests"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Username != owner || detail.Days != 7 {
		t.Fatalf("detail header = %s days=%d, want %s/7", detail.Username, detail.Days, owner)
	}
	if detail.Kpi.Requests != 3 || detail.Kpi.Errors != 1 {
		t.Fatalf("kpi = %+v, want requests=3 errors=1", detail.Kpi)
	}
	if detail.Kpi.ErrorRate < 0.33 || detail.Kpi.ErrorRate > 0.34 {
		t.Fatalf("error_rate = %v, want ≈0.333", detail.Kpi.ErrorRate)
	}
	if detail.Kpi.LatencyP95Ms == nil || *detail.Kpi.LatencyP95Ms < 8000 {
		t.Fatalf("latency_p95_ms = %v, want ≥8000 (seed max 9000)", detail.Kpi.LatencyP95Ms)
	}
	if len(detail.Daily) != 7 {
		t.Fatalf("daily len = %d, want 7", len(detail.Daily))
	}
	if detail.Daily[6].Requests != 2 || detail.Daily[6].Errors != 1 {
		t.Fatalf("today bucket = %+v, want requests=2 errors=1", detail.Daily[6])
	}
	if detail.Daily[5].Requests != 1 {
		t.Fatalf("yesterday bucket = %+v, want requests=1", detail.Daily[5])
	}
	if len(detail.TopModels) != 2 || detail.TopModels[0].Name != "claude-opus-5" || detail.TopModels[0].Requests != 2 {
		t.Fatalf("top_models = %+v", detail.TopModels)
	}
	if len(detail.TopApps) != 2 || detail.TopApps[0].Name != appCode {
		t.Fatalf("top_apps = %+v", detail.TopApps)
	}
	if len(detail.TopKeys) != 2 || detail.TopKeys[0].Name != "zz-alias-1" {
		t.Fatalf("top_keys = %+v", detail.TopKeys)
	}
	if len(detail.Recent) != 3 {
		t.Fatalf("recent len = %d, want 3 (key3 rows must be excluded)", len(detail.Recent))
	}

	// ── 不存在的用户 → 404 ──────────────────────────────
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/api/admin/users/99999999/stats?days=7", nil)
	h.handleUserStatsDispatcher(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("missing user status = %d, want 404", rec3.Code)
	}
}
