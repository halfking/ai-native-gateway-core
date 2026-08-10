package admin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// PR-7 (2026-06-30): buildStateDistribution must flatten a breakdown
// slice into {state: count}. Frontend ProbeHealthDetailView.vue reads
// `state_distribution` for the 4 status badges (audit P0-10).
func TestBuildStateDistribution_EmptyBreakdown(t *testing.T) {
	got := buildStateDistribution(nil)
	if got == nil {
		t.Fatal("should return non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestBuildStateDistribution_SingleState(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Count: 5},
	}
	got := buildStateDistribution(breakdown)
	want := map[string]int{"healthy": 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildStateDistribution_MultipleStates(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Count: 5},
		{State: "failing", Count: 2},
		{State: "suspicious", Count: 1},
		{State: "probing", Count: 3},
	}
	got := buildStateDistribution(breakdown)
	want := map[string]int{
		"healthy":    5,
		"failing":    2,
		"suspicious": 1,
		"probing":    3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// PR-7: real-world state enum (from db/migrations/308) is the union of
// healthy / healthy_confirmed / available / failing / broken_confirmed /
// unavailable / recovering / suspicious / probing. The function must
// preserve all of them verbatim (frontend aggregates them).
func TestBuildStateDistribution_AllStateEnums(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Count: 1},
		{State: "healthy_confirmed", Count: 1},
		{State: "available", Count: 1},
		{State: "failing", Count: 1},
		{State: "broken_confirmed", Count: 1},
		{State: "unavailable", Count: 1},
		{State: "recovering", Count: 1},
		{State: "suspicious", Count: 1},
		{State: "probing", Count: 1},
	}
	got := buildStateDistribution(breakdown)
	if len(got) != 9 {
		t.Errorf("expected 9 distinct states, got %d: %v", len(got), got)
	}
	for _, state := range []string{
		"healthy", "healthy_confirmed", "available",
		"failing", "broken_confirmed", "unavailable", "recovering",
		"suspicious", "probing",
	} {
		if got[state] != 1 {
			t.Errorf("state %q = %d, want 1", state, got[state])
		}
	}
}

// PR-7: regression — multiple breakdown rows with same state should
// aggregate (SUM), not last-wins. Audit P0-10 reports show distinct
// (state, priority) groups can collapse into same state bucket.
func TestBuildStateDistribution_DuplicateStatesAggregated(t *testing.T) {
	breakdown := []ModelStateBreakdown{
		{State: "healthy", Priority: "watchdog", Count: 3},
		{State: "healthy", Priority: "watchdog", Count: 2},
		{State: "healthy", Priority: "suspicious", Count: 1}, // unusual but defensive
	}
	got := buildStateDistribution(breakdown)
	if got["healthy"] != 6 {
		t.Errorf("expected healthy=6 (3+2+1), got %d", got["healthy"])
	}
}

// ── handleProbeNodeTasks (2026-08-10, fix/selfcheck-queue-and-recovery) ──
//
// handleProbeQueueTasks only sees credential_probe_queue (integrity probe)
// rows; the error-triggered NodeProbeWorker retries live in
// node_probe_state/node_probe_runs and were invisible in the self-check
// swimlanes. These tests drive the new endpoint against a pgxmock pool to
// pin the row-mapping and status classification contract the frontend
// depends on (web/src/api-selfcheck.ts NodeProbeTaskRow).

// statusFor mirrors the CASE expression's classification embedded in
// handleProbeNodeTasks' SQL so this Go-side test can assert against the
// same three-way split without a live Postgres instance. Keeping this
// helper in the test (not production code) is deliberate: the source of
// truth is the SQL CASE statement in admin/probe_dashboard.go; this
// function exists only to make the classification rule inspectable/
// testable at the unit level and must be kept in sync by hand if that
// CASE expression changes.
func statusFor(inFlightUntil *time.Time, paused bool, now time.Time) string {
	if inFlightUntil != nil && inFlightUntil.After(now) {
		return "running"
	}
	if paused {
		return "paused"
	}
	return "pending"
}

func TestHandleProbeNodeTasks_StatusClassification(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	cases := []struct {
		name           string
		inFlightUntil  *time.Time
		paused         bool
		wantStatus     string
	}{
		{"leased and in-flight window still open -> running", &future, false, "running"},
		{"in-flight window expired, not paused -> pending", &past, false, "pending"},
		{"in-flight window expired, paused -> paused", &past, true, "paused"},
		{"never leased, not paused -> pending", nil, false, "pending"},
		{"never leased, paused (attempt cap) -> paused", nil, true, "paused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := statusFor(c.inFlightUntil, c.paused, now)
			if got != c.wantStatus {
				t.Errorf("statusFor(inFlightUntil=%v, paused=%v) = %q, want %q",
					c.inFlightUntil, c.paused, got, c.wantStatus)
			}
		})
	}
}

// TestHandleProbeNodeTasks_RowMapping drives the real handler against a
// pgxmock pool to pin the JSON row shape (field names the frontend's
// NodeProbeTaskRow interface depends on) and the source="node_probe"
// label used to distinguish this queue from ProbeQueueTaskRow's
// source="integrity".
func TestQueryProbeNodeTasks_RowMapping(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Now()
	nextRetry := now.Add(30 * time.Second)
	rows := pgxmock.NewRows([]string{
		"credential_id", "provider_id", "provider_name", "provider_code",
		"raw_model_name", "standardized_name", "status", "attempt",
		"consecutive_failures", "next_retry_at", "last_direct_ok",
		"last_gateway_ok", "last_err_code", "direct_latency_ms", "paused", "updated_at",
	}).AddRow(
		int64(42), int64(7), "OpenAI", "openai",
		"gpt-5.6-luna", "gpt-5.6-luna", "pending", 3,
		2, nextRetry, false,
		false, "timeout", int32(1200), false, now,
	)
	mock.ExpectQuery(`SELECT`).WithArgs(10).WillReturnRows(rows)

	tasks, err := queryProbeNodeTasks(context.Background(), mock, 10)
	if err != nil {
		t.Fatalf("queryProbeNodeTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks)=%d, want 1", len(tasks))
	}
	got := tasks[0]
	if got.CredentialID != 42 {
		t.Errorf("credential_id=%d, want 42", got.CredentialID)
	}
	if got.Source != "node_probe" {
		t.Errorf("source=%q, want %q", got.Source, "node_probe")
	}
	if got.Status != "pending" {
		t.Errorf("status=%q, want %q", got.Status, "pending")
	}
	if got.LastErrCode == nil || *got.LastErrCode != "timeout" {
		t.Errorf("last_err_code=%v, want %q", got.LastErrCode, "timeout")
	}
	if got.LastLatencyMs == nil || *got.LastLatencyMs != 1200 {
		t.Errorf("last_latency_ms=%v, want 1200", got.LastLatencyMs)
	}
	if got.NextRetryAt == nil {
		t.Errorf("next_retry_at is nil, want set")
	}

	// Confirm the row also survives a JSON round-trip with the expected
	// wire field names (web/src/api-selfcheck.ts NodeProbeTaskRow depends
	// on these exact keys).
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"credential_id", "provider_id", "raw_model", "status", "source", "paused"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("wire JSON missing expected key %q: %s", key, raw)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations not met: %v", err)
	}
}

// TestQueryProbeQueueTasks_SourceLabel pins that queryProbeQueueTasks (the
// pre-existing integrity-probe query, called from handleProbeQueueTasks)
// tags every row with source="integrity", the counterpart label to
// queryProbeNodeTasks' source="node_probe" — the frontend swimlane merge
// in SelfCheckPanel.vue keys off this field.
func TestQueryProbeQueueTasks_SourceLabel(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Now()
	rows := pgxmock.NewRows([]string{
		"id", "credential_id", "provider_id", "provider_name", "provider_code",
		"raw_model", "standardized_name", "status", "attempt", "priority",
		"reason_code", "next_run_at", "result_latency_ms", "result_http_status", "updated_at",
	}).AddRow(
		int64(1), int64(2), int64(3), "Anthropic", "anthropic",
		"claude-sonnet-5", "claude-sonnet-5", "ready", 0, int16(10),
		"integrity_verify", now, nil, nil, now,
	)
	mock.ExpectQuery(`SELECT`).WithArgs(10).WillReturnRows(rows)

	tasks, err := queryProbeQueueTasks(context.Background(), mock, 10)
	if err != nil {
		t.Fatalf("queryProbeQueueTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks)=%d, want 1", len(tasks))
	}
	if tasks[0].Source != "integrity" {
		t.Errorf("source=%q, want %q", tasks[0].Source, "integrity")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations not met: %v", err)
	}
}
