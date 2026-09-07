// routing_opt_handler_test.go — P2.2 Track C routing-opt admin API 单测。
//
// 无 DB 依赖部分（nil pool 503 / 方法守卫 / hours clamp / SQL 参数化静态
// 检查）直接 httptest 调 handler 方法；SQL 路径通过 pgxmock 注入
// routingOptDBOverride seam（与 beginApprovalTxOverride 同款模式）覆盖。
package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// decodeRoutingOptJSON decodes a recorder's JSON body into v.
func decodeRoutingOptJSON(t *testing.T, w *httptest.ResponseRecorder, v interface{}) error {
	t.Helper()
	return json.NewDecoder(w.Body).Decode(v)
}

// newRoutingOptMockEnv 注入 pgxmock pool 并注册清理，返回 mock 以排队期望。
func newRoutingOptMockEnv(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() {
		mock.Close()
		routingOptDBOverride = nil
	})
	routingOptDBOverride = mock
	return mock
}

// routingOptGet 按 kind 直调对应 handler（不走 HasSuffix 分发，
// 因为带 query string 的路径后缀不匹配）。
func routingOptGet(h *Handler, kind, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	switch kind {
	case "stats":
		h.handleRoutingOptStats(w, req)
	case "accuracy":
		h.handleRoutingOptAccuracy(w, req)
	case "parameters":
		h.handleRoutingOptParameters(w, req)
	default:
		panic("unknown routing-opt kind: " + kind)
	}
	return w
}

// =============================================================================
// nil pool → 503（生产路径 h.db == nil）
// =============================================================================

func TestRoutingOptNilPoolReturns503(t *testing.T) {
	h := &Handler{} // h.db == nil, override nil

	for _, tc := range []struct{ kind, path string }{
		{"stats", "/api/admin/routing-opt/stats"},
		{"accuracy", "/api/admin/routing-opt/accuracy"},
		{"parameters", "/api/admin/routing-opt/parameters"},
	} {
		w := routingOptGet(h, tc.kind, tc.path)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s with nil pool: want 503, got %d body=%s", tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Database not available") {
			t.Errorf("GET %s: want 'Database not available' body, got %q", tc.path, w.Body.String())
		}
	}
}

// =============================================================================
// 方法守卫：非 GET → 405（在 pool 检查之前，nil pool 也不影响）
// =============================================================================

func TestRoutingOptMethodGuard(t *testing.T) {
	h := &Handler{}
	calls := []struct {
		name string
		call func(w *httptest.ResponseRecorder, req *http.Request)
	}{
		{"stats", func(w *httptest.ResponseRecorder, req *http.Request) { h.handleRoutingOptStats(w, req) }},
		{"accuracy", func(w *httptest.ResponseRecorder, req *http.Request) { h.handleRoutingOptAccuracy(w, req) }},
		{"parameters", func(w *httptest.ResponseRecorder, req *http.Request) { h.handleRoutingOptParameters(w, req) }},
	}
	for _, tc := range calls {
		for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
			req := httptest.NewRequest(method, "/api/admin/routing-opt/x", nil)
			w := httptest.NewRecorder()
			tc.call(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: want 405, got %d", method, tc.name, w.Code)
			}
		}
	}
}

// =============================================================================
// hours 参数 clamp：1..720，默认 24；非数字/0/负数 → 默认，超大 → 720
// =============================================================================

func TestRoutingOptParseHoursClamp(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 24},        // 缺省
		{"24", 24},      // 常规
		{"1", 1},        // 下界
		{"720", 720},    // 上界
		{"0", 24},       // 0 → 默认
		{"-5", 24},      // 负数 → 默认
		{"abc", 24},     // 非数字 → 默认
		{"  48  ", 48},  // 容忍空白
		{"999999", 720}, // 超大 → clamp 上界
		{"1.5", 24},     // 小数非法 → 默认
		{"24; DROP TABLE routing_feedback_log", 24}, // 注入尝试 → 默认（且永不进 SQL 文本）
	}
	for _, tc := range cases {
		if got := parseRoutingOptHours(tc.raw); got != tc.want {
			t.Errorf("parseRoutingOptHours(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// =============================================================================
// SQL 注入防护静态检查：全参数化（$1）、无 fmt 格式动词、hours 值不落文本
// =============================================================================

func TestRoutingOptSQLFullyParameterized(t *testing.T) {
	parameterized := map[string]string{
		"stats_auto":  routingOptStatsAutoSQL,
		"stats_human": routingOptStatsHumanSQL,
		"accuracy":    routingOptAccuracySQL,
	}
	for name, sql := range parameterized {
		if !strings.Contains(sql, "$1") {
			t.Errorf("%s SQL must use the $1 placeholder, got: %s", name, sql)
		}
	}
	all := map[string]string{
		"stats_auto":  routingOptStatsAutoSQL,
		"stats_human": routingOptStatsHumanSQL,
		"stats_state": routingOptStatsStateSQL,
		"accuracy":    routingOptAccuracySQL,
		"parameters":  routingOptParametersSQL,
	}
	for name, sql := range all {
		// 任何 fmt 格式动词都不允许出现 → fmt.Sprintf 拼接在文本层面不可能。
		if strings.ContainsAny(sql, "%") {
			t.Errorf("%s SQL contains '%%' (fmt verb risk): %s", name, sql)
		}
	}
	// hours 的边界值绝不能被烘焙进 SQL 文本（只允许作为 $1 绑定参数）。
	for _, lit := range []string{"720", "999999"} {
		if strings.Contains(routingOptAccuracySQL, lit) {
			t.Errorf("accuracy SQL must not embed the hours literal %q", lit)
		}
	}
}

// =============================================================================
// pgxmock：stats 聚合口径 (auto + 2×human)/(total + 2×human)
// =============================================================================

func TestRoutingOptStatsWeightedAggregation(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	// auto: 8/10 正确；human: 2/4 一致 → (8+2×2)/(10+2×4) = 12/18
	// pgxmock 的 Scan 不做二次解引用：可空列的 AddRow 值必须直接给
	// 目标元素类型（*float64 / *time.Time）。
	stateNow := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_feedback_log")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"correct", "total"}).AddRow(int64(8), int64(10)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_feedback_log")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"agreeing", "total"}).AddRow(int64(2), int64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_optimization_state")).
		WillReturnRows(pgxmock.NewRows([]string{"version", "overall_accuracy", "updated_at"}).
			AddRow(3, nil, &stateNow))

	w := routingOptGet(h, "stats", "/api/admin/routing-opt/stats")
	if w.Code != http.StatusOK {
		t.Fatalf("stats: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp RoutingOptStatsResponse
	if err := decodeRoutingOptJSON(t, w, &resp); err != nil {
		t.Fatalf("decode stats response: %v", err)
	}
	want := 12.0 / 18.0
	if diff := resp.OverallAccuracy - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("overall_accuracy = %v, want %v", resp.OverallAccuracy, want)
	}
	if resp.AccuracySource != "weighted_feedback" {
		t.Errorf("accuracy_source = %q, want weighted_feedback", resp.AccuracySource)
	}
	if resp.ParameterVersion != 3 {
		t.Errorf("parameter_version = %d, want 3", resp.ParameterVersion)
	}
	if resp.HumanAnnotationsUsed != 4 {
		t.Errorf("human_annotations_used = %d, want 4", resp.HumanAnnotationsUsed)
	}
	if resp.AutoSamples != 10 || resp.HumanSamples != 4 {
		t.Errorf("samples = auto %d human %d, want 10/4", resp.AutoSamples, resp.HumanSamples)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// 无反馈样本时回退 optimization_state.overall_accuracy。
func TestRoutingOptStatsFallbackToPersistedState(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_feedback_log")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"correct", "total"}).AddRow(int64(0), int64(0)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_feedback_log")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"agreeing", "total"}).AddRow(int64(0), int64(0)))
	persisted := 0.91
	stateNow := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_optimization_state")).
		WillReturnRows(pgxmock.NewRows([]string{"version", "overall_accuracy", "updated_at"}).
			AddRow(7, &persisted, &stateNow))

	w := routingOptGet(h, "stats", "/api/admin/routing-opt/stats")
	if w.Code != http.StatusOK {
		t.Fatalf("stats: want 200, got %d", w.Code)
	}
	var resp RoutingOptStatsResponse
	if err := decodeRoutingOptJSON(t, w, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OverallAccuracy != 0.91 {
		t.Errorf("overall_accuracy = %v, want persisted 0.91", resp.OverallAccuracy)
	}
	if resp.AccuracySource != "persisted_state" {
		t.Errorf("accuracy_source = %q, want persisted_state", resp.AccuracySource)
	}
	if resp.ParameterVersion != 7 {
		t.Errorf("parameter_version = %d, want 7", resp.ParameterVersion)
	}
}

// DB 错误 → 500（绝不 panic、绝不 200 半数据）。
func TestRoutingOptStatsDBErrorReturns500(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_feedback_log")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	w := routingOptGet(h, "stats", "/api/admin/routing-opt/stats")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("stats db error: want 500, got %d body=%s", w.Code, w.Body.String())
	}
}

// =============================================================================
// pgxmock：accuracy 按小时×任务类型聚合 + hours clamp 回显
// =============================================================================

func TestRoutingOptAccuracyBuckets(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	hour := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("GROUP BY 1, 2")).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"hour_bucket", "task_type", "correct", "total", "human_correct", "human_total",
		}).
			AddRow(hour, "code", int64(9), int64(10), int64(1), int64(2)).
			AddRow(hour.Add(time.Hour), "chat", int64(0), int64(4), int64(0), int64(0)))

	w := routingOptGet(h, "accuracy", "/api/admin/routing-opt/accuracy?hours=48")
	if w.Code != http.StatusOK {
		t.Fatalf("accuracy: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp RoutingOptAccuracyResponse
	if err := decodeRoutingOptJSON(t, w, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Hours != 48 {
		t.Errorf("hours = %d, want 48", resp.Hours)
	}
	if len(resp.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(resp.Buckets))
	}
	// code: (9 + 2×1)/(10 + 2×2) = 11/14
	if diff := resp.Buckets[0].Accuracy - 11.0/14.0; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("code bucket accuracy = %v, want %v", resp.Buckets[0].Accuracy, 11.0/14.0)
	}
	// chat: 全错且无人工 → 0
	if resp.Buckets[1].Accuracy != 0 {
		t.Errorf("chat bucket accuracy = %v, want 0", resp.Buckets[1].Accuracy)
	}
	if resp.Buckets[0].Samples != 10 || resp.Buckets[0].HumanSamples != 2 {
		t.Errorf("bucket samples = %d/%d, want 10/2", resp.Buckets[0].Samples, resp.Buckets[0].HumanSamples)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// 非法 hours 在 handler 侧 clamp 为默认 24（响应回显 clamp 结果）。
func TestRoutingOptAccuracyClampsInvalidHours(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"not-a-number", 24},
		{"0", 24},
		{"-1", 24},
		{"999999", 720},
	} {
		// 每个请求消费一条期望（pgxmock 期望队列按次匹配）。
		mock.ExpectQuery(regexp.QuoteMeta("GROUP BY 1, 2")).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"hour_bucket", "task_type", "correct", "total", "human_correct", "human_total",
			}))
		w := routingOptGet(h, "accuracy", "/api/admin/routing-opt/accuracy?hours="+tc.raw)
		if w.Code != http.StatusOK {
			t.Fatalf("accuracy hours=%s: want 200, got %d", tc.raw, w.Code)
		}
		var resp RoutingOptAccuracyResponse
		if err := decodeRoutingOptJSON(t, w, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Hours != tc.want {
			t.Errorf("hours=%q clamped to %d, want %d", tc.raw, resp.Hours, tc.want)
		}
	}
}

// =============================================================================
// pgxmock：parameters 激活版本 JSON / 无激活行 404
// =============================================================================

func TestRoutingOptParametersActiveVersion(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	activated := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	notes := "adaptive checkpoint"
	accuracy := 0.88
	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_optimization_state")).
		WillReturnRows(pgxmock.NewRows([]string{
			"version", "classifier_weights", "confidence_thresholds", "recommender_weights",
			"exploration_rate", "learning_rate", "adaptation_window", "overall_accuracy",
			"activated_at", "created_by", "notes",
		}).AddRow(
			5, []byte(`{"code":1.1}`), []byte(`{}`), []byte(`{"quality":0.4,"cost":0.3}`),
			0.05, 0.01, 1000, &accuracy,
			activated, "system", &notes,
		))

	w := routingOptGet(h, "parameters", "/api/admin/routing-opt/parameters")
	if w.Code != http.StatusOK {
		t.Fatalf("parameters: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp RoutingOptParametersResponse
	if err := decodeRoutingOptJSON(t, w, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Version != 5 {
		t.Errorf("version = %d, want 5", resp.Version)
	}
	if string(resp.RecommenderWeights) != `{"quality":0.4,"cost":0.3}` {
		t.Errorf("recommender_weights = %s", resp.RecommenderWeights)
	}
	if resp.OverallAccuracy == nil || *resp.OverallAccuracy != 0.88 {
		t.Errorf("overall_accuracy = %v, want 0.88", resp.OverallAccuracy)
	}
	if resp.Notes == nil || *resp.Notes != notes {
		t.Errorf("notes = %v, want %q", resp.Notes, notes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRoutingOptParametersNoActiveRowReturns404(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_optimization_state")).
		WillReturnError(pgx.ErrNoRows)

	w := routingOptGet(h, "parameters", "/api/admin/routing-opt/parameters")
	if w.Code != http.StatusNotFound {
		t.Fatalf("parameters no active row: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRoutingOptParametersDBErrorReturns500(t *testing.T) {
	mock := newRoutingOptMockEnv(t)
	h := &Handler{}

	mock.ExpectQuery(regexp.QuoteMeta("FROM routing_optimization_state")).
		WillReturnError(errors.New("relation does not exist"))

	w := routingOptGet(h, "parameters", "/api/admin/routing-opt/parameters")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("parameters db error: want 500, got %d", w.Code)
	}
}
