package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── 静态守卫：分发注册 ───

func TestUsageTrendSeriesDispatchRegistered(t *testing.T) {
	src, err := os.ReadFile("usage.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"trend-series", "trend-models"} {
		if !strings.Contains(string(src), `case "`+endpoint+`"`) {
			t.Fatalf("usage.go HandleUsageAdmin missing dispatch case %q", endpoint)
		}
	}
}

// ─── 纯函数单测 ───

func TestUsageTrendSourceTiering(t *testing.T) {
	cases := []struct {
		f    usageTrendFilters
		want string
	}{
		{usageTrendFilters{}, "request_stats_dim_minute"},
		{usageTrendFilters{tenantID: "t1", model: "gpt-4o"}, "request_stats_dim_minute"},
		{usageTrendFilters{providerID: 3}, "request_stats_minute"},
		{usageTrendFilters{providerID: 3, tenantID: "t1"}, "request_stats_minute"},
		{usageTrendFilters{apiKeyID: 7}, "request_logs_with_current_month"},
		{usageTrendFilters{apiKeyID: 7, providerID: 3}, "request_logs_with_current_month"},
	}
	for _, c := range cases {
		if got := usageTrendSource(c.f); got != c.want {
			t.Errorf("usageTrendSource(%+v) = %s, want %s", c.f, got, c.want)
		}
	}
}

func TestFoldUsageTrendRows(t *testing.T) {
	b1 := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	mk := func(model string, reqs int64, bucket time.Time) usageTrendRow {
		return usageTrendRow{Model: model, Bucket: bucket, Requests: reqs}
	}
	rows := []usageTrendRow{
		mk("a", 30, b1),
		mk("b", 20, b1),
		mk("c", 10, b1),
		mk("a", 5, b1.Add(time.Hour)),
		mk("c", 1, b1.Add(time.Hour)),
	}

	// top=2：a/b 保留，c（两次桶行）整体折叠为 __others__（两行都改写）。
	folded := foldUsageTrendRows(rows, 2, false)
	others := 0
	for _, row := range folded {
		if row.Model == usageTrendOthersKey {
			others++
		}
	}
	if others != 2 {
		t.Fatalf("model c must fold into %s across both buckets (2 rows), got %d", usageTrendOthersKey, others)
	}
	if len(folded) != 5 {
		t.Fatalf("want 5 folded rows (a×2 + b×1 + others×2 rewrite c×2), got %d", len(folded))
	}

	// 模型数 ≤ top：原样透传。
	same := foldUsageTrendRows(rows, 5, false)
	if len(same) != len(rows) {
		t.Fatalf("no fold expected when models <= top, got %d rows", len(same))
	}

	// model 过滤分支：透传。
	sameFilter := foldUsageTrendRows(rows, 1, true)
	if len(sameFilter) != len(rows) {
		t.Fatalf("model-filtered rows must pass through, got %d rows", len(sameFilter))
	}
}

func TestPivotUsageTrendRows(t *testing.T) {
	b1 := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	b2 := b1.Add(15 * time.Minute)
	rows := []usageTrendRow{
		{Model: "a", Bucket: b1, Requests: 1},
		{Model: "b", Bucket: b1, Requests: 5},
		{Model: "a", Bucket: b2, Requests: 2},
		{Model: usageTrendOthersKey, Bucket: b1, Requests: 4},
	}
	series := pivotUsageTrendRows(rows)
	if len(series) != 3 {
		t.Fatalf("want 3 series, got %d", len(series))
	}
	// 保序：先见到的模型在前。
	if series[0].Model != "a" || series[1].Model != "b" || series[2].Model != usageTrendOthersKey {
		t.Fatalf("series order wrong: %v", []string{series[0].Model, series[1].Model, series[2].Model})
	}
	if series[0].TotalRequests != 3 || len(series[0].Points) != 2 {
		t.Fatalf("series a totals/points wrong: %+v", series[0])
	}
	if series[0].Points[0].Bucket != b1.Format(time.RFC3339) {
		t.Fatalf("bucket must serialize RFC3339 UTC, got %s", series[0].Points[0].Bucket)
	}
}

// ─── 集成（可跳过）：真实 PG 上的端到端行为 ───

func setupUsageTrendFixtures(t *testing.T, db *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()
	bucket := time.Now().UTC().Truncate(time.Minute)
	models := []struct {
		key      string
		requests int64
	}{
		{"m-alpha", 30},
		{"m-beta", 20},
		{"m-gamma", 10},
	}
	for i, m := range models {
		for j := 0; j < 3; j++ {
			_, err := db.Exec(ctx, `
				INSERT INTO request_stats_dim_minute
					(bucket, tenant_id, dim_type, dim_key, requests, total_tokens, credits_charged, cost_usd)
				VALUES ($1 + ($4 * interval '1 minute'), $2, 'model', $3, $5, $5 * 10, $5, $5 / 100.0)
				ON CONFLICT (bucket, tenant_id, dim_type, dim_key) DO UPDATE SET requests = EXCLUDED.requests
			`, bucket, tenantID, m.key, j, m.requests+int64(i))
			if err != nil {
				t.Fatalf("fixture insert failed: %v", err)
			}
		}
	}
}

func TestUsageTrendSeriesTopFoldAndModelFilter(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}
	h := &Handler{db: db}
	tenantID := "test-usage-trend-series"
	setupUsageTrendFixtures(t, db, tenantID)

	// top=2：m-alpha/m-beta 独立成线，m-gamma 折进 __others__。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/trend-series?tenant_id="+tenantID+"&top=2", nil)
	w := httptest.NewRecorder()
	h.usageTrendSeries(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp usageTrendSeriesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "request_stats_dim_minute" {
		t.Fatalf("dim path expected, got source=%s", resp.Source)
	}
	if len(resp.Series) != 3 {
		t.Fatalf("want 3 series (2 picked + others), got %d: %+v", len(resp.Series), resp.Series)
	}
	if resp.Series[0].Model != "m-alpha" || resp.Series[1].Model != "m-beta" {
		t.Fatalf("picked order wrong: %s, %s", resp.Series[0].Model, resp.Series[1].Model)
	}
	if resp.Series[2].Model != usageTrendOthersKey {
		t.Fatalf("last series must be %s, got %s", usageTrendOthersKey, resp.Series[2].Model)
	}
	if resp.Series[2].TotalRequests <= 0 {
		t.Fatalf("others series should fold m-gamma traffic, got %+v", resp.Series[2])
	}

	// model 过滤：单模型、不折叠。
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/admin/usage/trend-series?tenant_id="+tenantID+"&model=m-gamma", nil)
	w2 := httptest.NewRecorder()
	h.usageTrendSeries(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w2.Code, w2.Body.String())
	}
	var resp2 usageTrendSeriesResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if len(resp2.Series) != 1 || resp2.Series[0].Model != "m-gamma" {
		t.Fatalf("model filter must return exactly m-gamma: %+v", resp2.Series)
	}
}

func TestUsageTrendModelsOptions(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available")
	}
	h := &Handler{db: db}
	tenantID := "test-usage-trend-models"
	setupUsageTrendFixtures(t, db, tenantID)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/trend-models?tenant_id="+tenantID, nil)
	w := httptest.NewRecorder()
	h.usageTrendModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp usageTrendModelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) < 3 {
		t.Fatalf("want >=3 model options, got %d", len(resp.Models))
	}
	if resp.Models[0].Model != "m-alpha" {
		t.Fatalf("options must sort by requests desc, head=%s", resp.Models[0].Model)
	}
}
