package admin

// topology_models_today_test.go — 实时拓扑页「模型当天统计」端点判据
// （2026-10-28 live 页当天汇总轮）。
//
// 与 providers-today 同族的三条真问题，外加模型组特有的两条：
//  1. 租户隔离：租户管理员的查询必须带自己的 tenant_id；
//  2. 降级语义：聚合表未迁移 → 200 + degraded 信封，不是「当天 0 请求」；
//  3. canonical 名解析：LEFT JOIN models_canonical 命中时用 canonical_name，
//     canonical_id=0 的 passthrough 行回落 raw_model_name（与拓扑图
//     modelKeyOf「canonical 优先、原始名兜底」同一口径）；
//  4. share 分母是全量 requests（不是可见行和）；
//  5. 水位来自 DB 行的 watermark（不是响应生成时刻——用 now() 冒充水位
//     会把 rollup ≤1h 滞后藏起来）。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func modelsTodayRowSet() *pgxmock.Rows {
	ts := time.Date(2026, 10, 28, 11, 59, 0, 0, time.UTC)
	return pgxmock.NewRows([]string{
		"canonical_id", "canonical_name", "raw_model_name",
		"requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged",
		"cost_usd", "watermark",
	}).
		// canonical 命中行：canonical_name 非空 ⇒ 前端用 canonical_name
		AddRow(42, "glm-4.5", "glm-4.5-vendor", 900, 890, 10, 0, 800000, 100000, 80000, 2000, 900000, 30, 2.1, pgtype.Timestamptz{Time: ts, Valid: true}).
		// canonical_id=0（passthrough/别名未匹配）行：canonical_name 空 ⇒ 回落 raw_model_name
		AddRow(0, "", "vendor-raw-model", 100, 100, 0, 0, 10000, 1000, 0, 0, 11000, 1, 0.1, pgtype.Timestamptz{Time: ts, Valid: true})
}

// 1. 租户隔离：tenant_admin 的查询必须带自己的 tenant_id；缺参 = 本租户。
func TestTopologyModelsTodayScopesTenantAdminToOwnTenant(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	mock.ExpectQuery(`(?s)WITH agg AS .* FROM stats_usage_daily`).
		WithArgs(day, "tenant-a", modelsTodayLimit).
		WillReturnRows(modelsTodayRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", UserID: 7})
	rec := httptest.NewRecorder()
	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	out := decodeTopologyToday(t, rec.Body.Bytes())
	require.Equal(t, false, out["degraded"])
	items := out["items"].([]any)
	require.Len(t, items, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 2. 降级语义：聚合表缺失 → 200 + degraded（不是 500，不是 0 行静默成功）。
func TestTopologyModelsTodayDegradedWhenStatsTableMissing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH agg AS`).
		WillReturnError(errMissingStatsRelation{})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default", UserID: 1})
	rec := httptest.NewRecorder()
	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	out := decodeTopologyToday(t, rec.Body.Bytes())
	require.Equal(t, true, out["degraded"])
	require.NotEmpty(t, out["error_code"])
}

// 3. canonical 名解析 + share 全量分母 + 水位来自 DB 行。
func TestTopologyModelsTodayCanonicalNameFallbackAndShare(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	mock.ExpectQuery(`(?s)WITH agg AS .* FROM stats_usage_daily`).
		WithArgs(day, "", modelsTodayLimit).
		WillReturnRows(modelsTodayRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default", UserID: 1})
	rec := httptest.NewRecorder()
	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	out := decodeTopologyToday(t, rec.Body.Bytes())
	require.Equal(t, false, out["degraded"])
	items := out["items"].([]any)
	require.Len(t, items, 2)

	first := items[0].(map[string]any)
	// canonical 命中行：canonical_name 非空
	require.Equal(t, "glm-4.5", first["canonical_name"])
	// share 分母 = 全量 1000（900 + 100），不是可见行自己的和
	require.InDelta(t, 0.9, first["share"], 1e-9)
	// 水位来自 DB 行 watermark（2026-10-28T11:59Z），非响应生成时刻
	require.Contains(t, out["source_high_watermark"], "2026-10-28T11:59")

	second := items[1].(map[string]any)
	// passthrough 行：canonical_name 空，前端回落 raw_model_name
	require.Equal(t, "", second["canonical_name"])
	require.Equal(t, "vendor-raw-model", second["raw_model_name"])
}
