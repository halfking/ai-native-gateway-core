package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestDeriveModelEffectiveState_PriorityOrder covers the 5-state priority chain.
// Priority (first match wins):
//  1. credentialManualDisabled (整凭据被禁用) -> "manual_disabled"
//  2. binding_unavailable_reason == "manual_offline" -> "manual_disabled"
//  3. probe_state == "broken_confirmed" -> "probe_broken"
//  4. offer_available == false -> "offer_missing"
//  5. binding_available == false -> "binding_missing"
//  6. default -> "available"
func TestDeriveModelEffectiveState_PriorityOrder(t *testing.T) {
	mkStr := func(s string) *string { return &s }

	tests := []struct {
		name                     string
		ms                       *CredentialModelStatus
		credentialManualDisabled bool
		want                     string
	}{
		{
			name: "整凭据 manual_disabled 优先级最高",
			ms: &CredentialModelStatus{
				OfferAvailable:   false,
				BindingAvailable: false,
				ProbeState:       "broken_confirmed",
			},
			credentialManualDisabled: true,
			want:                     "manual_disabled",
		},
		{
			name: "per-model manual_offline 优先级高于 probe_broken",
			ms: &CredentialModelStatus{
				OfferAvailable:           true,
				BindingAvailable:         false,
				BindingUnavailableReason: mkStr("manual_offline"),
				ProbeState:               "broken_confirmed",
			},
			credentialManualDisabled: false,
			want:                     "manual_disabled",
		},
		{
			name: "probe_broken 优先级高于 offer_missing",
			ms: &CredentialModelStatus{
				OfferAvailable:   false,
				BindingAvailable: true,
				ProbeState:       "broken_confirmed",
			},
			credentialManualDisabled: false,
			want:                     "probe_broken",
		},
		{
			name: "offer_missing",
			ms: &CredentialModelStatus{
				OfferAvailable:   false,
				BindingAvailable: true,
				ProbeState:       "healthy_confirmed",
			},
			credentialManualDisabled: false,
			want:                     "offer_missing",
		},
		{
			name: "binding_missing",
			ms: &CredentialModelStatus{
				OfferAvailable:           true,
				BindingAvailable:         false,
				BindingUnavailableReason: mkStr("rate_limited"),
				ProbeState:               "healthy_confirmed",
			},
			credentialManualDisabled: false,
			want:                     "binding_missing",
		},
		{
			name: "available 全部正常",
			ms: &CredentialModelStatus{
				OfferAvailable:   true,
				BindingAvailable: true,
				ProbeState:       "healthy_confirmed",
			},
			credentialManualDisabled: false,
			want:                     "available",
		},
		{
			name: "probe unknown + binding 缺失 = binding_missing (probe 不参与后 3 档)",
			ms: &CredentialModelStatus{
				OfferAvailable:           true,
				BindingAvailable:         false,
				BindingUnavailableReason: mkStr("auth_failed"),
				ProbeState:               "unknown",
			},
			credentialManualDisabled: false,
			want:                     "binding_missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveModelEffectiveState(tt.ms, tt.credentialManualDisabled)
			if got != tt.want {
				t.Fatalf("deriveModelEffectiveState() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHumanizeDisabledReason covers the human-readable reason strings.
// Returns "" when fully available.
func TestHumanizeDisabledReason(t *testing.T) {
	mkStr := func(s string) *string { return &s }

	tests := []struct {
		name string
		ms   *CredentialModelStatus
		want string
	}{
		{
			name: "available 返回空",
			ms: &CredentialModelStatus{
				EffectiveState: "available",
			},
			want: "",
		},
		{
			name: "manual_offline 优先于 effective_state",
			ms: &CredentialModelStatus{
				EffectiveState:           "manual_disabled",
				BindingUnavailableReason: mkStr("manual_offline"),
			},
			want: "管理员手动下线",
		},
		{
			name: "整凭据 manual_disabled",
			ms: &CredentialModelStatus{
				EffectiveState: "manual_disabled",
			},
			want: "整凭据被禁用",
		},
		{
			name: "probe_broken",
			ms: &CredentialModelStatus{
				EffectiveState: "probe_broken",
			},
			want: "探测失败 (broken_confirmed)",
		},
		{
			name: "offer_missing 带原因",
			ms: &CredentialModelStatus{
				EffectiveState:         "offer_missing",
				OfferUnavailableReason: mkStr("deprecated"),
			},
			want: "Offer 不可用: deprecated",
		},
		{
			name: "offer_missing 无原因",
			ms: &CredentialModelStatus{
				EffectiveState: "offer_missing",
			},
			want: "Offer 缺失",
		},
		{
			name: "binding_missing 带原因",
			ms: &CredentialModelStatus{
				EffectiveState:           "binding_missing",
				BindingUnavailableReason: mkStr("rate_limited"),
			},
			want: "Binding 不可用: rate_limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := humanizeDisabledReason(tt.ms)
			if got != tt.want {
				t.Fatalf("humanizeDisabledReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMonitorSummarySchemaVersion_NotZero ensures the schema version constant
// is set to a non-zero value, otherwise the cache would never invalidate.
func TestMonitorSummarySchemaVersion_NotZero(t *testing.T) {
	if monitorSummarySchemaVersion == 0 {
		t.Fatalf("monitorSummarySchemaVersion must be > 0; cache key would not differentiate schemas")
	}
}

// queryLogger is a no-op identity wrapper used by tests so a mock pool can be
// passed straight to runMonitorSummary (which is pgxQueryer-parameterized).
func queryLogger(p pgxQueryer) pgxQueryer { return p }

// timeNow returns the current time; isolated so tests can capture a stable
// "now" for window calculations.
func timeNow() time.Time { return time.Now() }

// summaryMockRow builds a single CredentialMonitorSummary-shaped row whose
// column order matches buildMonitorSummarySQL / runMonitorSummary's Scan list.
func summaryMockRow() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "provider_id", "provider_name", "label", "status",
		"availability_state", "health_status", "quota_state",
		"concurrency_limit", "concurrency_limit_auto", "effective_concurrency",
		"manual_disabled", "consecutive_failures",
		"availability_recover_at", "state_reason_code", "state_reason_detail",
		"health_checked_at", "total_requests", "model_total", "model_available",
		"broken_model_count", "aggregated_success_rate", "models",
	}).AddRow(
		int64(1), int64(7), "zhipu", "zhipu-cred", "active",
		"available", "healthy", "ok",
		nil, nil, 5,
		false, 0,
		nil, nil, nil,
		nil, int64(100), 1, 1,
		0, 0.95, []byte("[]"),
	)
}

// pgxmock-driven tests for runMonitorSummary (the SQL builder/executor
// extracted from handleMonitorSummary). They validate that the query binds
// the tenant filter ($3) and that rows.Err() propagates to the caller.
// The cache-key dimension is asserted in TestMonitorSummarySchemaVersion_NotZero
// (and built in the HTTP handler).

func TestBuildMonitorSummarySQLModes(t *testing.T) {
	core := buildMonitorSummarySQL(monitorSummarySQLParams{Mode: "core", TenantID: "tenant-a"})
	if strings.Contains(core, "%!(EXTRA") || strings.Contains(core, "request_logs_with_current_month") {
		t.Fatalf("core SQL must be lightweight and fully formatted: %s", core)
	}
	if !strings.Contains(core, "model_offers") || !strings.Contains(core, "c.tenant_id") {
		t.Fatalf("core SQL omitted model state or tenant filter: %s", core)
	}

	detail := buildMonitorSummarySQL(monitorSummarySQLParams{CredentialID: 1, Mode: "detail", TenantID: "tenant-a"})
	if strings.Contains(detail, "%!") || !strings.Contains(detail, "request_logs_with_current_month") || !strings.Contains(detail, "percentile_cont") {
		t.Fatalf("detail SQL must include formatted detail aggregates: %s", detail)
	}
}

func TestRunMonitorSummary_TenantIsolation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	tenant := "tenant-isolation-7"

	q := queryLogger(mock)
	mock.ExpectQuery(`SELECT[\s\S]*FROM[\s\S]*credentials[\s\S]*`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), tenant).
		WillReturnRows(summaryMockRow())

	_, got, err := runMonitorSummary(context.Background(), q, monitorSummarySQLParams{TenantID: tenant})
	if err != nil {
		t.Fatalf("runMonitorSummary: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

func TestMonitorSummaryCacheKeyDimensions(t *testing.T) {
	base := monitorSummarySQLParams{ProviderID: 7, CredentialID: 11, Mode: "detail", TenantID: "tenant-a"}
	baseKey := monitorSummaryCacheKey(base)
	for name, changed := range map[string]monitorSummarySQLParams{
		"provider":   {ProviderID: 8, CredentialID: 11, Mode: "detail", TenantID: "tenant-a"},
		"credential": {ProviderID: 7, CredentialID: 12, Mode: "detail", TenantID: "tenant-a"},
		"mode":       {ProviderID: 7, CredentialID: 11, Mode: "core", TenantID: "tenant-a"},
		"tenant":     {ProviderID: 7, CredentialID: 11, Mode: "detail", TenantID: "tenant-b"},
	} {
		if got := monitorSummaryCacheKey(changed); got == baseKey {
			t.Errorf("%s did not change cache key %q", name, baseKey)
		}
	}
	if !strings.Contains(baseKey, fmt.Sprintf("v%d", monitorSummarySchemaVersion)) {
		t.Fatalf("cache key %q omits schema version", baseKey)
	}
}

func TestRunMonitorSummary_QueryErrorPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	tenant := "tenant-rows-err"
	// Simulated query failure must propagate (audit guarantee: incomplete
	// payloads never reach the UI). pgxmock surfaces this via the Query error,
	// which runMonitorSummary wraps and returns.
	mock.ExpectQuery(`SELECT[\s\S]*FROM[\s\S]*credentials[\s\S]*`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), tenant).
		WillReturnError(errors.New("simulated query failure"))

	q := queryLogger(mock)
	if _, _, err := runMonitorSummary(context.Background(), q, monitorSummarySQLParams{TenantID: tenant}); err == nil {
		t.Fatalf("expected query error to propagate, got nil")
	}
}

func TestRunMonitorSummary_RowsErrPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	tenant := "tenant-iteration-error"
	rows := summaryMockRow().CloseError(errors.New("simulated iteration failure"))
	mock.ExpectQuery(`SELECT[\s\S]*FROM[\s\S]*credentials[\s\S]*`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), tenant).
		WillReturnRows(rows)

	_, summaries, err := runMonitorSummary(context.Background(), mock, monitorSummarySQLParams{TenantID: tenant})
	if err == nil || !strings.Contains(err.Error(), "rows iteration failed") {
		t.Fatalf("expected rows iteration error, got %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected row before iteration failure, got %d summaries", len(summaries))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

// TestRunMonitorSummary_DetailModelsDeriveStateAndWorstRate 覆盖 detail 模式的
// **Go 侧** per-model 状态派生，以及 summary 模式的 worst-rate 语义。
//
// ⚠ 判据纪律（AGENTS.md #3 / #12）：
//
//	聚合成功率**不能**从 mock 行里断言 —— 那是 mock 把自己回显给自己，
//	恒绿（本仓库上一轮就踩过：分支那份判据断言 `*AggregatedSuccessRate == 0.72`，
//	而 0.72 正是 mock 自己塞进去的值，删掉整段实现它照样全绿）。
//	worst-rate 的语义在 **SQL** 里（summary 模式 `MIN(rsr.rate)`），
//	所以那条断言落在 SQL 文本上；detail 模式则必须显式为 NULL（不编造）。
func TestRunMonitorSummary_DetailModelsDeriveStateAndWorstRate(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	models := []byte(`[
		{"raw_model_name":"manual","offer_available":true,"binding_available":false,"binding_unavailable_reason":"manual_offline","probe_state":"healthy_confirmed","recent_success_rate":0.91},
		{"raw_model_name":"broken","offer_available":true,"binding_available":true,"probe_state":"broken_confirmed","recent_success_rate":0.72}
	]`)
	rows := pgxmock.NewRows([]string{
		"id", "provider_id", "provider_name", "label", "status",
		"availability_state", "health_status", "quota_state",
		"concurrency_limit", "concurrency_limit_auto", "effective_concurrency",
		"manual_disabled", "consecutive_failures", "availability_recover_at",
		"state_reason_code", "state_reason_detail", "health_checked_at", "total_requests",
		"model_total", "model_available", "broken_model_count", "aggregated_success_rate", "models",
	}).AddRow(
		int64(11), int64(7), "provider", "credential", "active", "ready", "healthy", "ok",
		nil, nil, 5, false, 0, nil, nil, nil, nil, int64(100), 2, 1, 1, nil, models,
	)
	mock.ExpectQuery(`SELECT[\s\S]*FROM[\s\S]*credentials[\s\S]*`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "tenant-a").
		WillReturnRows(rows)

	_, summaries, err := runMonitorSummary(context.Background(), mock,
		monitorSummarySQLParams{ProviderID: 7, CredentialID: 11, Mode: "detail", TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("runMonitorSummary: %v", err)
	}
	if len(summaries) != 1 || len(summaries[0].Models) != 2 {
		t.Fatalf("unexpected summary result: %+v", summaries)
	}

	// ★ Go 侧派生（真被测逻辑）：manual_offline → manual_disabled，
	//   broken_confirmed → probe_broken，理由文案同源。
	manual, broken := summaries[0].Models[0], summaries[0].Models[1]
	if manual.EffectiveState != "manual_disabled" || manual.ModelDisabledReason != "管理员手动下线" {
		t.Fatalf("manual model derivation drifted: %+v", manual)
	}
	if broken.EffectiveState != "probe_broken" || broken.ModelDisabledReason != "探测失败 (broken_confirmed)" {
		t.Fatalf("broken model derivation drifted: %+v", broken)
	}

	// ⚠ 这里**不能**断言扫描出来的 AggregatedSuccessRate —— 那个值是 mock 行
	//   自己塞进去的，断言它等于 mock 就是「mock 把自己回显给自己」，恒绿。
	//   detail 模式「不编造聚合成功率」是一条 **SQL** 契约，落在 SQL 文本上。
	detailSQL := buildMonitorSummarySQL(monitorSummarySQLParams{ProviderID: 7, CredentialID: 11, Mode: "detail", TenantID: "tenant-a"})
	if !strings.Contains(detailSQL, "NULL::double precision AS aggregated_success_rate") {
		t.Fatalf("detail mode must not fabricate an aggregated success rate, SQL:\n%s", detailSQL)
	}

	// ★ worst-rate 语义落在 summary 模式的 SQL 上：取各模型最近成功率的**最小值**。
	summarySQL := buildMonitorSummarySQL(monitorSummarySQLParams{ProviderID: 7, TenantID: "tenant-a"})
	if !strings.Contains(summarySQL, "MIN(rsr.rate) AS aggregated_success_rate") {
		t.Fatalf("summary mode must take the worst model success rate, SQL:\n%s", summarySQL)
	}
	if !strings.Contains(summarySQL, "recent_success_rate") {
		t.Fatalf("summary mode must source the per-model success rate, SQL:\n%s", summarySQL)
	}
	// core 模式刻意不碰 request_logs，也不算聚合成功率。
	coreSQL := buildMonitorSummarySQL(monitorSummarySQLParams{Mode: "core", TenantID: "tenant-a"})
	if strings.Contains(coreSQL, "MIN(rsr.rate)") {
		t.Fatalf("core mode must not compute the worst rate, SQL:\n%s", coreSQL)
	}
}

// TestHandleMonitorSummary_NilDB：未接 DB 时给 503，而不是 nil pointer panic。
//
// ⚠ 与兄弟 handler 的口径一致性：promote/demote/set-concurrency/model-toggle/
// decisions/manual-disabled/sliding-window 等 9 个 handler 都有这道门，
// summary handler 原先是本组**唯一**没有的（2026-10-20 补上）。
func TestHandleMonitorSummary_NilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	// ⚠ 必须用**唯一** provider_id：monitorSummaryCache 是**包级全局**，
	//   同 key 的条目会被同包其它测试写进去并跨测试存活
	//   （默认 provider_id=0 的那条已被 server_duration 测试缓存 ⇒
	//     本用例会走到缓存命中分支，拿到 200 而不是 503）。
	//   缓存命中在 nil-db 判空**之前**短路是有意为之：缓存命中不需要 DB。
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/monitor-summary?provider_id=987654", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", UserID: 1, TenantID: "default"})
	rr := httptest.NewRecorder()
	m.handleMonitorSummary(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rr.Code, rr.Body.String())
	}
}

// TestHandleMonitorSummary_RoleGateBeforeNilDB：未授权者得 403 而不是 503。
//
// 503 会泄露「DB 是否已配置」，与本组读写两侧的角色门口径一致。
func TestHandleMonitorSummary_RoleGateBeforeNilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/monitor-summary", nil)
	rr := httptest.NewRecorder()
	m.handleMonitorSummary(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rr.Code, rr.Body.String())
	}
}
