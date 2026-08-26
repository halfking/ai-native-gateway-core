package admin

import (
	"context"
	"errors"
	"fmt"
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
