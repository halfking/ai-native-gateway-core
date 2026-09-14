// annotation_handler_firstturn_test.go — 首轮会话标注工作台单测（2026-09-14）。
//
// 覆盖：默认日期范围（当天 UTC 半开区间）、WHERE 构建的筛选→占位符映射、
// pgxmock 全查询路径（count + data 扫描，含 NULL 列）、handler 的 nil-pool
// 503 / 方法守卫 405、annotation_metadata JSONB 构建规则。
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// ─────────────────────────────────────────────────────────────────────────
// resolveFirstTurnDateRange
// ─────────────────────────────────────────────────────────────────────────

func TestFirstTurnDateRangeDefaultsToTodayUTC(t *testing.T) {
	startTS, endTS, startDay, endDay, err := resolveFirstTurnDateRange("", "")
	if err != nil {
		t.Fatalf("empty dates: unexpected error %v", err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	wantDay, _ := time.Parse("2006-01-02", today)
	if !startTS.Equal(wantDay) {
		t.Errorf("default startTS = %v, want %v", startTS, wantDay)
	}
	if !endTS.Equal(wantDay.AddDate(0, 0, 1)) {
		t.Errorf("default endTS = %v, want start+24h (half-open)", endTS)
	}
	if !startDay.Equal(wantDay) || !endDay.Equal(wantDay) {
		t.Errorf("default days = %v/%v, want %v/%v", startDay, endDay, wantDay, wantDay)
	}
}

func TestFirstTurnDateRangeExplicit(t *testing.T) {
	startTS, endTS, startDay, endDay, err := resolveFirstTurnDateRange("2026-09-01", "2026-09-07")
	if err != nil {
		t.Fatalf("explicit dates: unexpected error %v", err)
	}
	d1, _ := time.Parse("2006-01-02", "2026-09-01")
	d7, _ := time.Parse("2006-01-02", "2026-09-07")
	if !startTS.Equal(d1) || !startDay.Equal(d1) {
		t.Errorf("start = %v/%v, want %v", startTS, startDay, d1)
	}
	if !endDay.Equal(d7) {
		t.Errorf("endDay = %v, want %v (inclusive pruning bound)", endDay, d7)
	}
	if !endTS.Equal(d7.AddDate(0, 0, 1)) {
		t.Errorf("endTS = %v, want day+1 exclusive %v", endTS, d7.AddDate(0, 0, 1))
	}
}

func TestFirstTurnDateRangeInvalid(t *testing.T) {
	for _, tc := range []struct{ start, end string }{
		{"not-a-date", ""},
		{"", "2026-13-99"},
		{"2026/09/01", ""},
	} {
		if _, _, _, _, err := resolveFirstTurnDateRange(tc.start, tc.end); err == nil {
			t.Errorf("resolve(%q,%q): want error, got nil", tc.start, tc.end)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// buildFirstTurnWhere
// ─────────────────────────────────────────────────────────────────────────

func firstTurnTestOptions(mutate func(*firstTurnQueryOptions)) firstTurnQueryOptions {
	d1, _ := time.Parse("2006-01-02", "2026-09-14")
	opts := firstTurnQueryOptions{
		StartTS:  d1,
		EndTS:    d1.AddDate(0, 0, 1),
		StartDay: d1,
		EndDay:   d1,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return opts
}

func TestFirstTurnWhereNoFilters(t *testing.T) {
	where, args := buildFirstTurnWhere(firstTurnTestOptions(nil))
	if where != "WHERE TRUE" {
		t.Errorf("no-filter where = %q, want %q", where, "WHERE TRUE")
	}
	if len(args) != 4 {
		t.Errorf("no-filter args = %d, want 4 (fixed ft bounds)", len(args))
	}
}

func TestFirstTurnWhereAllFilters(t *testing.T) {
	ttrue := true
	minC, maxC := 0.2, 0.9
	opts := firstTurnTestOptions(func(o *firstTurnQueryOptions) {
		o.Scoped = true
		o.TenantID = "acme"
		o.TaskType = "code"
		o.Model = "glm-5"
		o.HumanTaskType = "reasoning"
		o.Annotated = &ttrue
		o.MinConfidence = &minC
		o.MaxConfidence = &maxC
	})
	where, args := buildFirstTurnWhere(opts)
	wantOrder := []string{
		"ft.tenant_id = $5",
		"ars.task_type = $6",
		"ars.chosen_model = $7",
		"tha.annotation_metadata->>'task_type' = $8",
		"tha.request_id IS NOT NULL",
		"ars.confidence >= $9",
		"ars.confidence <= $10",
	}
	idx := 0
	for _, want := range wantOrder {
		pos := strings.Index(where[idx:], want)
		if pos < 0 {
			t.Errorf("where %q missing fragment %q (in order)", where, want)
			continue
		}
		idx += pos + len(want)
	}
	if len(args) != 10 {
		t.Errorf("args = %d, want 10", len(args))
	}
	if args[4] != "acme" || args[5] != "code" || args[6] != "glm-5" || args[7] != "reasoning" {
		t.Errorf("filter args 5..8 = %v/%v/%v/%v, want acme/code/glm-5/reasoning", args[4], args[5], args[6], args[7])
	}
	if args[8] != minC || args[9] != maxC {
		t.Errorf("confidence args = %v/%v, want %v/%v", args[8], args[9], minC, maxC)
	}
}

func TestFirstTurnWhereUnannotatedAndUnscoped(t *testing.T) {
	opts := firstTurnTestOptions(func(o *firstTurnQueryOptions) {
		ffalse := false
		o.Annotated = &ffalse
		// Scoped=false (super admin) → no tenant predicate even with TenantID set.
		o.TenantID = "default"
	})
	where, args := buildFirstTurnWhere(opts)
	if !strings.Contains(where, "tha.request_id IS NULL") {
		t.Errorf("where %q missing IS NULL (unannotated)", where)
	}
	if strings.Contains(where, "tenant_id") {
		t.Errorf("unscoped where must not carry tenant predicate: %q", where)
	}
	if len(args) != 4 {
		t.Errorf("args = %d, want 4", len(args))
	}
}

// ─────────────────────────────────────────────────────────────────────────
// queryFirstTurnSamples — pgxmock 全查询路径
// ─────────────────────────────────────────────────────────────────────────

func TestFirstTurnQueryScansRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	opts := firstTurnTestOptions(func(o *firstTurnQueryOptions) {
		o.Limit = 50
		o.Offset = 0
	})
	where, baseArgs := buildFirstTurnWhere(opts)
	countSQL := "SELECT COUNT(*)" + firstTurnFromClause + where

	ts := time.Date(2026, 9, 14, 8, 31, 2, 0, time.UTC)
	conf := 0.91
	code, latency, turns := 200, 812, 4
	title := "修复登录页 401"
	client := "claude-code"
	annotatedAt := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta(countSQL)).
		WithArgs(baseArgs...).
		WillReturnRows(mock.NewRows([]string{"count"}).AddRow(1))

	dataSQL := `
		SELECT
			ft.session_id,
			ft.request_id,
			ft.ts,
			COALESCE(s.title, s.last_request_summary),
			s.client_type,
			ars.task_type,
			ars.chosen_model,
			ars.confidence,
			ft.status_code,
			ft.success,
			ft.latency_ms,
			s.total_turns,
			tha.human_label,
			tha.annotation_metadata->>'task_type',
			tha.annotation_metadata->>'model',
			tha.is_correct,
			tha.annotation_reason,
			tha.annotator,
			tha.annotated_at
	` + firstTurnFromClause + where + `
		ORDER BY ft.ts DESC
		LIMIT $5 OFFSET $6
	`
	rows := mock.NewRows([]string{
		"session_id", "request_id", "ts", "title", "client_type",
		"task_type", "chosen_model", "confidence", "status_code", "success",
		"latency_ms", "total_turns", "human_label", "human_task_type",
		"human_model", "is_correct", "reason", "annotator", "annotated_at",
	})
	// Row 1: fully populated (annotated).
	rows.AddRow("s-1", "req-1", ts, title, client, "code", "glm-5", conf,
		code, true, latency, turns, "glm-5", "code", "glm-5", true, "correct", "alice", annotatedAt)
	// Row 2: annotation-less row with NULL session/turn columns.
	rows.AddRow("s-2", "req-2", ts, nil, nil, "chat", "gpt-5", nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	mock.ExpectQuery(regexp.QuoteMeta(dataSQL)).
		WithArgs(append(append([]any{}, baseArgs...), 50, 0)...).
		WillReturnRows(rows)

	samples, total, err := queryFirstTurnSamples(context.Background(), mock, opts)
	if err != nil {
		t.Fatalf("queryFirstTurnSamples: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	s1 := samples[0]
	if s1.SessionID != "s-1" || s1.RequestID != "req-1" || !s1.Ts.Equal(ts) {
		t.Errorf("row1 ids/ts = %v/%v/%v", s1.SessionID, s1.RequestID, s1.Ts)
	}
	if s1.Title == nil || *s1.Title != title || s1.Client == nil || *s1.Client != client {
		t.Errorf("row1 title/client = %v/%v", s1.Title, s1.Client)
	}
	if s1.Confidence == nil || *s1.Confidence != conf {
		t.Errorf("row1 confidence = %v, want %v", s1.Confidence, conf)
	}
	if s1.StatusCode == nil || *s1.StatusCode != 200 || s1.Success == nil || !*s1.Success || s1.LatencyMs == nil || *s1.LatencyMs != latency {
		t.Errorf("row1 turn metrics = %v/%v/%v", s1.StatusCode, s1.Success, s1.LatencyMs)
	}
	if s1.TotalTurns == nil || *s1.TotalTurns != turns {
		t.Errorf("row1 total_turns = %v, want %v", s1.TotalTurns, turns)
	}
	if s1.HumanTaskType == nil || *s1.HumanTaskType != "code" || s1.HumanModel == nil || *s1.HumanModel != "glm-5" {
		t.Errorf("row1 human labels = %v/%v", s1.HumanTaskType, s1.HumanModel)
	}
	if s1.AnnotatedAt == nil || !s1.AnnotatedAt.Equal(annotatedAt) {
		t.Errorf("row1 annotated_at = %v, want %v", s1.AnnotatedAt, annotatedAt)
	}

	s2 := samples[1]
	if s2.Title != nil || s2.Client != nil || s2.Confidence != nil || s2.StatusCode != nil ||
		s2.Success != nil || s2.LatencyMs != nil || s2.TotalTurns != nil ||
		s2.HumanProvider != nil || s2.IsCorrect != nil || s2.AnnotatedAt != nil {
		t.Errorf("row2 NULL columns must stay nil, got %+v", s2)
	}
	if s2.TaskType != "chat" || s2.ChosenModel != "gpt-5" {
		t.Errorf("row2 auto decision = %v/%v, want chat/gpt-5", s2.TaskType, s2.ChosenModel)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Handler guards
// ─────────────────────────────────────────────────────────────────────────

func TestFirstTurnHandlerNilPoolReturns503(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/annotations/first-turn-samples", nil)
	w := httptest.NewRecorder()
	h.handleAnnotationFirstTurnSamples(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("nil pool: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestFirstTurnHandlerMethodGuard(t *testing.T) {
	h := &Handler{}
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/api/admin/annotations/first-turn-samples", nil)
		w := httptest.NewRecorder()
		h.handleAnnotationFirstTurnSamples(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: want 405, got %d", method, w.Code)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// buildAnnotationMetadata
// ─────────────────────────────────────────────────────────────────────────

func TestBuildAnnotationMetadata(t *testing.T) {
	if v, err := buildAnnotationMetadata("", ""); err != nil || v != nil {
		t.Errorf("both empty: want nil/nil, got %v/%v", v, err)
	}

	b, err := buildAnnotationMetadata("code", "glm-5")
	if err != nil {
		t.Fatalf("both set: unexpected error %v", err)
	}
	want := `{"model":"glm-5","task_type":"code"}`
	if s, ok := b.(string); !ok || s != want {
		t.Errorf("both set: got %v (%T), want string %s", b, b, want)
	}
	// JSONB 载荷必须是 string 而非 []byte:pgx 会把 []byte 按 bytea 编码,
	// jsonb 列报 22P02(生产冒烟 2026-09-15 实测)。
	if _, ok := b.([]byte); ok {
		t.Errorf("metadata must be string not []byte (pgx bytea trap)")
	}

	b, err = buildAnnotationMetadata("reasoning", "")
	if err != nil {
		t.Fatalf("task only: unexpected error %v", err)
	}
	if s, ok := b.(string); !ok || s != `{"task_type":"reasoning"}` {
		t.Errorf("task only: got %v (%T)", b, b)
	}

	if _, err := buildAnnotationMetadata(strings.Repeat("x", 65), ""); err == nil {
		t.Error("task_type >64: want error, got nil")
	}
	if _, err := buildAnnotationMetadata("", strings.Repeat("x", 129)); err == nil {
		t.Error("model >128: want error, got nil")
	}
}

// 全参数化静态检查：FROM 子句固定 $1..$4，动态筛选只允许经 buildFirstTurnWhere
// 追加占位符 — 文本层面不得出现 fmt 动词或字面值烘焙。
func TestFirstTurnSQLStaticSafety(t *testing.T) {
	if strings.Contains(firstTurnFromClause, "%") {
		t.Errorf("FROM clause contains fmt verb risk: %s", firstTurnFromClause)
	}
	where, args := buildFirstTurnWhere(firstTurnTestOptions(func(o *firstTurnQueryOptions) {
		o.TaskType = "code'; DROP TABLE x"
	}))
	if strings.Contains(where, "DROP TABLE") {
		t.Errorf("filter value leaked into SQL text: %q", where)
	}
	found := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == "code'; DROP TABLE x" {
			found = true
		}
	}
	if !found {
		t.Error("filter value must be a bound arg")
	}
	_ = reflect.TypeOf("")
}
