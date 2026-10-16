package admin

// topology_providers_today_test.go — 实时拓扑页「供应商当天统计」端点判据。
//
// 三条真问题各有判据，且都做过**变异验证**（改坏逻辑必须变红）：
//  1. 租户隔离：租户管理员不得看到别的租户当天数据（不能只靠前端过滤）；
//  2. 降级语义：聚合表未迁移时必须是 200 + degraded 信封，**不是**「当天 0 请求」；
//  3. 未知供应商桶（provider_id=0）不得被静默丢掉。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func topologyTodayRowSet(tenant string) *pgxmock.Rows {
	ts := time.Date(2026, 10, 16, 11, 59, 0, 0, time.UTC)
	_ = tenant
	return pgxmock.NewRows([]string{
		"provider_id", "catalog_code", "display_name", "enabled",
		"requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged",
		"cost_usd", "watermark",
	}).
		AddRow(12, "deepseek", "DeepSeek", true, 1284, 1270, 14, 3, 1500000, 640000, 90000, 4000, 2234000, 42, 3.5, pgtype.Timestamptz{Time: ts, Valid: true}).
		AddRow(0, "", "", false, 7, 0, 7, 0, 0, 0, 0, 0, 0, 0, 0.0, pgtype.Timestamptz{Time: ts, Valid: true})
}

func decodeTopologyToday(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

//  1. 租户隔离：tenant_admin 的查询必须带自己的 tenant_id；
//     缺参 = 本租户，而不是全量。
func TestTopologyProvidersTodayScopesTenantAdminToOwnTenant(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	mock.ExpectQuery(`(?s)WITH agg AS .* FROM stats_usage_daily`).
		WithArgs(day, "tenant-a", providersTodayLimit).
		WillReturnRows(topologyTodayRowSet("tenant-a"))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", UserID: 7})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	body := decodeTopologyToday(t, rec.Body.Bytes())
	require.Equal(t, "tenant-a", body["tenant_scope"])
}

// 1b) 超管不带 tenant_id 时是全量（tenant 参数为空串），但**不能**因此
//
//	把另一租户的数据当成自己的——作用域由 auth 决定，不由查询参数决定。
func TestTopologyProvidersTodaySuperAdminMayPassExplicitTenant(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	mock.ExpectQuery(`(?s)WITH agg AS `).
		WithArgs(day, "tenant-b", providersTodayLimit).
		WillReturnRows(topologyTodayRowSet("tenant-b"))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today?tenant_id=tenant-b", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default", UserID: 1})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, "tenant-b", decodeTopologyToday(t, rec.Body.Bytes())["tenant_scope"])
}

// 2) 降级：聚合表缺失 → 200 + degraded，且 items 为空数组而不是 null。
func TestTopologyProvidersTodayDegradesWhenStatsRelationMissing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH agg AS `).
		WithArgs(todayUTCDay(time.Now()), "", providersTodayLimit).
		WillReturnError(errMissingStatsRelation{})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeTopologyToday(t, rec.Body.Bytes())
	require.Equal(t, true, body["degraded"])
	require.Equal(t, "STATS_NOT_MIGRATED", body["error_code"])
	items, ok := body["items"].([]any)
	require.True(t, ok, "items must be an empty array, not null")
	require.Empty(t, items)
}

// 3) 未知供应商桶（provider_id=0）保留并标 known=false；share 分母是当日总请求。
func TestTopologyProvidersTodayKeepsUnknownProviderBucketAndComputesShare(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH agg AS `).
		WithArgs(todayUTCDay(time.Now()), "", providersTodayLimit).
		WillReturnRows(topologyTodayRowSet("default"))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeTopologyToday(t, rec.Body.Bytes())
	items, ok := body["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)

	first, _ := items[0].(map[string]any)
	require.Equal(t, "deepseek", first["catalog_code"])
	require.Equal(t, true, first["known"])
	share, _ := first["share"].(float64)
	require.InDelta(t, 1284.0/1291.0, share, 1e-6)

	second, _ := items[1].(map[string]any)
	require.Equal(t, false, second["known"], "provider_id=0 must be marked unknown, not dropped")
	unknownShare, _ := second["share"].(float64)
	require.InDelta(t, 7.0/1291.0, unknownShare, 1e-6)

	totals, _ := body["totals"].(map[string]any)
	require.Equal(t, float64(1291), totals["requests"])
	require.Equal(t, float64(2234000), totals["total_tokens"])
	require.Equal(t, "1h", body["rollup_cadence"], "UI 依赖 cadence 显式标注数据滞后")
}

// 4) 方法门：POST 不得落库/落查询。
func TestTopologyProvidersTodayRejectsNonGET(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/topology/providers-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

//  5. ★ SQL 契约钉子。**已知假绿面**：pgxmock 只匹配正则、不执行 SQL，
//     所以「LEFT JOIN 被改成 INNER JOIN（未知供应商桶被静默丢弃）」这类
//     形态改动，上面第 3 条行级判据**照常全绿**（变异验证实测过）。
//     因此形态类不变量必须钉在 SQL 常量本身，缺了这层就等于没测。
func TestProvidersTodaySQLContractPinsJoinAndTenantGuard(t *testing.T) {
	sql := providersTodaySQL

	// 未知供应商桶（provider_id=0）不得被丢：必须 LEFT JOIN。
	require.Contains(t, sql, "LEFT JOIN providers p ON p.id = a.provider_id",
		"providers must be LEFT JOINed — an INNER JOIN silently drops the provider_id=0 bucket")

	// 租户隔离：租户条件必须留在聚合子查询里（对 agg 生效，而不是对最终行做过滤）。
	require.Contains(t, sql, "AND ($2::text = '' OR tenant_id = $2)",
		"tenant guard must live inside the aggregate, otherwise the row shape changes")

	// 维度固定：provider 维度，不能被改成一个更宽的维度。
	require.Contains(t, sql, "dimension_type = 'provider'")

	// 日桶口径固定：UTC 日，与 stats_usage_daily.day_utc 生成方式一致。
	require.Contains(t, sql, "day_utc = $1::date")
}

//  6. todayUTCDay 的口径钉子：必须与 stats_usage_daily.day_utc
//     （(occurred_at AT TIME ZONE 'UTC')::date）一致 —— 改成本地日会让
//     「当天」在部署时区非 UTC 的机器上错位半天，且不会报错。
func TestTodayUTCDayUsesUTCDateBoundary(t *testing.T) {
	// 2026-10-16 23:30 +08:00 == 2026-10-16 15:30 UTC，仍是同一个 UTC 日。
	loc := time.FixedZone("UTC+8", 8*3600)
	got := todayUTCDay(time.Date(2026, 10, 16, 23, 30, 0, 0, loc))
	require.Equal(t, "2026-10-16", got.Format("2006-01-02"))

	// 2026-10-17 00:30 +08:00 == 2026-10-16 16:30 UTC —— **本地日已翻页，
	// UTC 日没有**。这正是不能用本地日的原因。
	got = todayUTCDay(time.Date(2026, 10, 17, 0, 30, 0, 0, loc))
	require.Equal(t, "2026-10-16", got.Format("2006-01-02"))

	// UTC 侧的翻页仍然生效。
	got = todayUTCDay(time.Date(2026, 10, 16, 23, 59, 59, 0, time.UTC))
	require.Equal(t, "2026-10-16", got.Format("2006-01-02"))
	got = todayUTCDay(time.Date(2026, 10, 17, 0, 0, 1, 0, time.UTC))
	require.Equal(t, "2026-10-17", got.Format("2006-01-02"))
}

// errMissingStatsRelation 模拟「聚合表尚未迁移」的 pg 错误文本。
type errMissingStatsRelation struct{}

func (errMissingStatsRelation) Error() string {
	return `ERROR: relation "stats_usage_daily" does not exist (SQLSTATE 42P01)`
}
