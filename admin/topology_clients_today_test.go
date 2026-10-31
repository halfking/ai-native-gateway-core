package admin

// topology_clients_today_test.go — 客户端区间聚合端点判据（2026-10-29 单一统计轮）。
//
// 四条真问题：
//  1. 租户隔离：与家族同参位（$2）；
//  2. ★ 水位封顶：day 模式必须以 rollup 水位封顶（否则客户端组比 providers/
//     models 组多出最后一小时的量 —— 同面板三组对不上账）；窗口模式不封顶；
//  3. 客户端取键：与前端 clientKeyOf 同源（agent → profile → protocol →
//     identity 前 8 位 → unknown）—— 口径分叉会让「图上 3 个客户端、组里 5 行」；
//  4. SQL 契约钉子：pgxmock 只匹配正则不执行 SQL，形态类不变量钉在常量上。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func clientsTodayRowSet() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"client_key", "requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged", "cost_usd",
	}).
		AddRow("claude-code", 41, 40, 1, 0, 52000, 21000, 3000, 200, 76200, 2, 0.4).
		AddRow("a1b2c3d4", 2, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0.0)
}

//  1. day 模式：水位封顶真发生了（水位查询 + 主查询的 $3 带上界），响应回显
//     套用的水位；租户条件同参位。
func TestTopologyClientsTodayDayCapsAtRollupWatermark(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	wm := time.Date(2026, 10, 28, 23, 2, 0, 0, time.UTC)
	mock.ExpectQuery(`MAX\(source_max_occurred_at\) FROM stats_usage_daily`).
		WithArgs(day).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(pgtype.Timestamptz{Time: wm, Valid: true}))
	mock.ExpectQuery(`(?s)WITH base AS .* FROM request_logs_with_current_month`).
		WithArgs(pgxmock.AnyArg(), "tenant-a", wm, clientsTodayLimit).
		WillReturnRows(clientsTodayRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/clients-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", UserID: 7})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyClientsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotContains(t, body, "window", "day 路径不输出 window 字段（家族契约）")
	require.Equal(t, "realtime", body["rollup_cadence"])
	require.NotNil(t, body["source_high_watermark"], "day 模式必须回显实际套用的水位封顶")
	items, _ := body["items"].([]any)
	require.Len(t, items, 2)
	first, _ := items[0].(map[string]any)
	share, _ := first["share"].(float64)
	require.InDelta(t, 41.0/43.0, share, 1e-6)
}

// 2. 水位 NULL（日桶未就绪）：不封顶（$3 传 NULL），响应不带水位字段。
func TestTopologyClientsTodayDayWithoutRollupRowsSkipsCap(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	day := todayUTCDay(time.Now())
	mock.ExpectQuery(`MAX\(source_max_occurred_at\) FROM stats_usage_daily`).
		WithArgs(day).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(nil))
	mock.ExpectQuery(`(?s)WITH base AS .* FROM request_logs_with_current_month`).
		WithArgs(pgxmock.AnyArg(), "", nil, clientsTodayLimit).
		WillReturnRows(clientsTodayRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/clients-today", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyClientsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotContains(t, body, "source_high_watermark")
}

// 3. 窗口模式：不查水位、不封顶，window 回显；?window 乱值回落 day（家族口径）。
func TestTopologyClientsTodayWindowSkipsWatermark(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH base AS .* FROM request_logs_with_current_month`).
		WithArgs(pgxmock.AnyArg(), "tenant-a", nil, clientsTodayLimit).
		WillReturnRows(clientsTodayRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/clients-today?window=5m", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", UserID: 7})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyClientsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "5m", body["window"])
	require.NotContains(t, body, "source_high_watermark", "窗口口径不封顶、不回显水位")
}

// 4. ★ SQL 契约钉子（pgxmock 行级判据测不到的形态不变量）。
func TestTopologyClientsTodaySQLContractPins(t *testing.T) {
	// 客户端取键与前端 clientKeyOf 逐字同款（agent → profile → protocol →
	// identity 前 8 位 → unknown）—— 口径分叉 = 图和组各说各话。
	require.Contains(t, clientsTodaySQL,
		"COALESCE(NULLIF(rl.agent_name, ''), NULLIF(rl.client_profile, ''),")
	require.Contains(t, clientsTodaySQL, "NULLIF(rl.client_protocol, '')")
	require.Contains(t, clientsTodaySQL, "LEFT(NULLIF(rl.identity_hash, ''), 8), 'unknown')")

	// 水位封顶必须是可选上界（NULL = 不封顶），且租户条件在家族同参位。
	require.Contains(t, clientsTodaySQL, "($3::timestamptz IS NULL OR rl.ts <= $3::timestamptz)")
	require.Contains(t, clientsTodaySQL, "($2::text = '' OR rl.tenant_id = $2)")

	// 状态表达式与窗口家族逐字同款（同一把尺，只此一份）。
	require.Contains(t, clientsTodaySQL, requestLogsStatusExpr)

	// 下界参数化（不把时长/日期拼进 SQL 文本）。
	require.Contains(t, clientsTodaySQL, "rl.ts >= $1::timestamptz")
}

// 5. 水位查询失败降级为「不封顶」——水位辅助查询挂了不该拖垮整个端点。
func TestRollupWatermarkForDayFailsOpen(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`MAX\(source_max_occurred_at\) FROM stats_usage_daily`).
		WithArgs(todayUTCDay(time.Now())).
		WillReturnError(context.DeadlineExceeded)

	require.Nil(t, rollupWatermarkForDay(mock, context.Background(), todayUTCDay(time.Now())))
}
