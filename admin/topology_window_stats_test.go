package admin

// topology_window_stats_test.go — 短窗口口径（?window=5m|1h）的端点判据。
//
// 与 day 口径同款的三层判据（同 §507 通式）：
//  1. 路由与租户：?window= 必须真的改走 request_logs 直查（不是仍打日桶表），
//     且租户条件跟 day 口径同源；
//  2. 形状：响应回显 window、rollup_cadence=realtime、day_utc 置空 ——
//     前端靠这三个字段画对口径行；day 路径的响应不得被波及（无 window 字段）；
//  3. SQL 契约钉子：pgxmock 只匹配正则、不执行 SQL ——「LEFT JOIN 被改成
//     INNER JOIN（未知供应商桶被丢）」「状态表达式被改得与实时流不一致」
//     这类形态改动，行级判据照常全绿，必须钉在 SQL 常量本身。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func topologyWindowRowSet() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"provider_id", "catalog_code", "display_name", "enabled",
		"requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged",
		"cost_usd", "watermark",
	}).
		AddRow(12, "deepseek", "DeepSeek", true, 41, 40, 1, 0, 52000, 21000, 3000, 200, 76200, 2, 0.4, nil).
		AddRow(0, "", "", false, 2, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0.0, nil)
}

func topologyWindowModelRowSet() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"canonical_id", "canonical_name", "raw_model_name",
		"requests", "success", "failure", "rate_limited",
		"prompt_tokens", "completion_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_tokens", "credits_charged",
		"cost_usd", "watermark",
	}).
		AddRow(7, "glm-5", "glm-5-flash", 43, 40, 3, 0, 52000, 21000, 3000, 200, 76200, 2, 0.4, nil)
}

//  1. ?window=5m 必须改走 request_logs 直查（而不是照旧打 stats_usage_daily），
//     且租户条件与 day 口径同源同参位。
func TestTopologyProvidersTodayWindowQueriesRequestLogs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH base AS .* FROM request_logs_with_current_month`).
		WithArgs(pgxmock.AnyArg(), "tenant-a", providersTodayLimit).
		WillReturnRows(topologyWindowRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today?window=5m", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", UserID: 7})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "5m", body["window"], "窗口口径必须回显，前端据此画口径行")
	require.Equal(t, "realtime", body["rollup_cadence"])
	require.Equal(t, "", body["day_utc"], "短窗口没有 UTC 日桶，不得冒充 day 口径")
	require.Nil(t, body["source_high_watermark"], "直查没有 rollup 滞后，不得伪造水位")

	totals, _ := body["totals"].(map[string]any)
	require.Equal(t, float64(43), totals["requests"])
	require.Equal(t, float64(76200), totals["total_tokens"])

	items, _ := body["items"].([]any)
	require.Len(t, items, 2)
	second, _ := items[1].(map[string]any)
	require.Equal(t, false, second["known"], "provider_id=0 未知桶不得被丢")
}

// 2. ?window=1h 同样走直查，回显 "1h"。
func TestTopologyProvidersTodayWindow1h(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH base AS .* FROM request_logs_with_current_month`).
		WithArgs(pgxmock.AnyArg(), "", providersTodayLimit).
		WillReturnRows(topologyWindowRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today?window=1h", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "1h", body["window"])
}

//  3. 无法识别的 window 值（含缺省）必须回落 day 日桶路径，且响应不带
//     window 字段 —— 静默吞掉未知值没关系，但绝不能把日桶数据标成短窗口。
func TestTopologyProvidersTodayUnknownWindowFallsBackToDay(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH agg AS .* FROM stats_usage_daily`).
		WithArgs(todayUTCDay(time.Now()), "", providersTodayLimit).
		WillReturnRows(topologyTodayRowSet("default"))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today?window=6h", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyProvidersToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotContains(t, body, "window", "day 路径的响应不得被窗口字段波及")
	require.Equal(t, "1h", body["rollup_cadence"])
}

//  4. 模型端点：窗口口径走 request_logs 直查；?by=provider 在窗口口径下
//     被忽略（不做交叉展开），返回默认形状。
func TestTopologyModelsTodayWindowQueriesRequestLogs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH base AS .* FROM request_logs_with_current_month`).
		WithArgs(pgxmock.AnyArg(), "tenant-a", modelsTodayLimit).
		WillReturnRows(topologyWindowModelRowSet())

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/models-today?window=5m&by=provider", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a", UserID: 7})
	rec := httptest.NewRecorder()

	(&Handler{topologyStatsDB: mock}).handleTopologyModelsToday(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "5m", body["window"])
	require.Equal(t, "realtime", body["rollup_cadence"])
	items, _ := body["items"].([]any)
	require.Len(t, items, 1)
	first, _ := items[0].(map[string]any)
	require.NotContains(t, first, "providers", "窗口口径不提供交叉展开，响应不得带 providers 子行")
}

// 5. ★ SQL 契约钉子（形态类不变量，pgxmock 行级判据测不到）。
func TestTopologyWindowSQLContractPins(t *testing.T) {
	// 未知供应商桶必须保留（LEFT JOIN），否则客户端侧被拒/未归因的流量消失。
	require.Contains(t, providersWindowSQL, "LEFT JOIN providers p ON p.id = COALESCE(c.provider_id, rl.provider_id)")
	require.Contains(t, providersWindowSQL, "COALESCE(p.id, 0)                       AS provider_id")

	// 租户条件必须与 day 口径同参位（$2）。
	require.Contains(t, providersWindowSQL, "($2::text = '' OR rl.tenant_id = $2)")
	require.Contains(t, modelsWindowSQL, "($2::text = '' OR rl.tenant_id = $2)")

	// 状态判定必须与 live-stream SSE catch-up 逐字同款 —— 两处对同一条
	// 请求的成败判定分叉，右栏与头部统计就对不上账。
	require.Contains(t, requestLogsStatusExpr,
		`COALESCE(NULLIF(rl.request_status, ''), CASE WHEN rl.success THEN 'success' WHEN rl.success = FALSE THEN 'failure' ELSE 'in_progress' END)`)
	require.Contains(t, providersWindowSQL, requestLogsStatusExpr)
	require.Contains(t, modelsWindowSQL, requestLogsStatusExpr)

	// 窗口下限走参数化 timestamptz（不是把时长拼进 SQL 文本）。
	require.Contains(t, providersWindowSQL, "rl.ts >= $1::timestamptz")
	require.Contains(t, modelsWindowSQL, "rl.ts >= $1::timestamptz")

	// watermark 恒 NULL：直查没有 rollup 滞后，伪造水位 = 把滞后藏起来。
	require.Contains(t, providersWindowSQL, "NULL::timestamptz")
	require.Contains(t, modelsWindowSQL, "NULL::timestamptz")

	// 模型归并粒度：canonical_id 上合并（跨供应商/别名一行）。
	require.Contains(t, modelsWindowSQL, "LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id")
}

//  6. 窗口参数解析的口径钉子：只认 5m / 1h；乱写值必须回落 day（宁可
//     返回口径正确的日桶数据，也不猜用户想要什么）。
func TestTopologyWindowParamParsesStrictly(t *testing.T) {
	mk := func(q string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/topology/providers-today"+q, nil)
		return r
	}

	d, ok := topologyWindowParam(mk("?window=5m"))
	require.True(t, ok)
	require.Equal(t, 5*time.Minute, d)

	d, ok = topologyWindowParam(mk("?window=1h"))
	require.True(t, ok)
	require.Equal(t, time.Hour, d)

	_, ok = topologyWindowParam(mk(""))
	require.False(t, ok, "缺省 = day 口径")
	_, ok = topologyWindowParam(mk("?window=6h"))
	require.False(t, ok)
	_, ok = topologyWindowParam(mk("?window=5min"))
	require.False(t, ok, "别名不认 —— 未登记的值回落 day")
	_, ok = topologyWindowParam(mk("?window=DAY"))
	require.False(t, ok, "大小写敏感，避免静默扩出口径面")

	require.Equal(t, "1h", topologyWindowLabel(time.Hour))
	require.Equal(t, "5m", topologyWindowLabel(5*time.Minute))
}
