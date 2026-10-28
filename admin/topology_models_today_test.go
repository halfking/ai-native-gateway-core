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

// 4. ?by=provider 交叉展开：父行指标 = Σ 子行（构造守恒），子行 share
//    是**模型内**分布（分母 = 该模型的 requests，不是当日总量），
//    provider_id=0 的未知桶 known=false。
func TestTopologyModelsTodayByProviderFoldsAndSharesWithinModel(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	ts := time.Date(2026, 10, 28, 11, 59, 0, 0, time.UTC)
	// glm-4.5 跨两个供应商（600 + 300）+ 一个 passthrough 模型（100）
	rows := pgxmock.NewRows([]string{
		"canonical_id", "canonical_name", "raw_model_name",
		"provider_id", "catalog_code", "display_name",
		"requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged",
		"cost_usd", "watermark",
	}).
		AddRow(42, "glm-4.5", "glm-4.5-vendor", 12, "deepseek", "DeepSeek", 600, 590, 10, 0, 100, 50, 0, 0, 150, 1, 0.5, pgtype.Timestamptz{Time: ts, Valid: true}).
		AddRow(42, "glm-4.5", "glm-4.5-vendor", 0, "", "", 300, 300, 0, 0, 50, 25, 0, 0, 75, 1, 0.2, pgtype.Timestamptz{Time: ts, Valid: true}).
		AddRow(0, "", "vendor-raw-model", 13, "moonshot", "Moonshot", 100, 100, 0, 0, 10, 5, 0, 0, 15, 1, 0.1, pgtype.Timestamptz{Time: ts, Valid: true})

	mock.ExpectQuery(`(?s)WITH agg AS .* GROUP BY canonical_id, provider_id, CASE WHEN canonical_id = 0 THEN COALESCE\(raw_model_name, ''\) ELSE '' END`).
		WithArgs(day, "", modelsTodayLimit).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today?by=provider", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default", UserID: 1})
	rec := httptest.NewRecorder()
	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	out := decodeTopologyToday(t, rec.Body.Bytes())
	require.Equal(t, false, out["degraded"])
	items := out["items"].([]any)
	require.Len(t, items, 2)

	first := items[0].(map[string]any)
	require.Equal(t, "glm-4.5", first["canonical_name"])
	// 守恒：父 requests = Σ 子行（600+300），不是单独一份再算的数
	require.Equal(t, float64(900), first["requests"])
	// 父 share 分母 = 当日总量 1000（与默认口径同分母）
	require.InDelta(t, 0.9, first["share"], 1e-9)

	providers := first["providers"].([]any)
	require.Len(t, providers, 2)
	known := providers[0].(map[string]any)
	require.Equal(t, "deepseek", known["catalog_code"])
	require.Equal(t, true, known["known"])
	// 子行 share 是模型内分布：600/900，不是 600/1000
	require.InDelta(t, 600.0/900.0, known["share"], 1e-9)
	// provider_id=0 的未知桶：known=false，但流量不丢（300 计入父行）
	unknown := providers[1].(map[string]any)
	require.Equal(t, false, unknown["known"])
	require.Equal(t, float64(300), unknown["requests"])
	require.NoError(t, mock.ExpectationsWereMet())
}

// 5. 默认调用（无 by 参数）响应形状兼容：不出现 providers 键（omitempty），
//    且走单维聚合 SQL（GROUP BY canonical_id + passthrough 拆名 CASE）。
func TestTopologyModelsTodayDefaultShapeHasNoProvidersKey(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	// pgxmock 的 stripQuery 把所有空白（含换行）归一成单空格 ⇒ 断言
	// 默认路径走的是单维聚合（canonical + passthrough 拆名 CASE 收在
	// 单个 GROUP BY 里），而 by=provider 路径是三元组聚合——两条 SQL 在
	// 这里分叉。
	mock.ExpectQuery(`(?s)WITH agg AS .* GROUP BY canonical_id, CASE WHEN canonical_id = 0 THEN COALESCE\(raw_model_name, ''\) ELSE '' END \)`).
		WithArgs(day, "", modelsTodayLimit).
		WillReturnRows(modelsTodayRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default", UserID: 1})
	rec := httptest.NewRecorder()
	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `"providers"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 6. ★ passthrough 拆名（2026-10-28 审计轮修）：canonical_id=0 的不同
//    raw 名是**不同模型**（图 modelKeyOf 按原始名分节点；211 实测一天
//    189 个不同 raw 名）。Go 折叠键必须镜像 SQL 的分组 CASE——只按
//    CanonicalID 折叠会把全部 passthrough 并回一行（名字 MAX 任选、
//    请求量合并），SQL 拆了 Go 又合上。本测试钉住折叠键：
//    两个 canonical=0 的不同 raw 名 ⇒ 两个父行，providers 不串。
func TestTopologyModelsTodayByProviderPassthroughRawNamesDoNotMerge(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	ts := time.Date(2026, 10, 28, 11, 59, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{
		"canonical_id", "canonical_name", "raw_model_name",
		"provider_id", "catalog_code", "display_name",
		"requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged",
		"cost_usd", "watermark",
	}).
		AddRow(0, "", "alpha-raw", 12, "deepseek", "DeepSeek", 100, 100, 0, 0, 10, 5, 0, 0, 15, 1, 0.1, pgtype.Timestamptz{Time: ts, Valid: true}).
		AddRow(0, "", "beta-raw", 12, "deepseek", "DeepSeek", 50, 50, 0, 0, 5, 3, 0, 0, 8, 1, 0.05, pgtype.Timestamptz{Time: ts, Valid: true}).
		AddRow(0, "", "beta-raw", 13, "moonshot", "Moonshot", 30, 30, 0, 0, 3, 2, 0, 0, 5, 1, 0.03, pgtype.Timestamptz{Time: ts, Valid: true})

	mock.ExpectQuery(`(?s)WITH agg AS .* GROUP BY canonical_id, provider_id, CASE`).
		WithArgs(day, "", modelsTodayLimit).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today?by=provider", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default", UserID: 1})
	rec := httptest.NewRecorder()
	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	out := decodeTopologyToday(t, rec.Body.Bytes())
	items := out["items"].([]any)
	// 两个不同 raw 名的 passthrough = 两个模型行，不许并成一行
	require.Len(t, items, 2)

	first := items[0].(map[string]any)
	require.Equal(t, "alpha-raw", first["raw_model_name"])
	require.Equal(t, float64(100), first["requests"])
	require.Len(t, first["providers"].([]any), 1)

	second := items[1].(map[string]any)
	require.Equal(t, "beta-raw", second["raw_model_name"])
	// beta-raw 折叠了 deepseek(50) + moonshot(30) 两个子行
	require.Equal(t, float64(80), second["requests"])
	require.Len(t, second["providers"].([]any), 2)
	require.NoError(t, mock.ExpectationsWereMet())
}
