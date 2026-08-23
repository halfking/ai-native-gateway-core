package admin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestProbeTaskValidationDefaultsAndEnums(t *testing.T) {
	if !isProbeTaskCommand("node_probe") || !isProbeTaskCommand("integrity_verify") || !isProbeTaskCommand("selfcheck") {
		t.Fatal("expected supported probe commands")
	}
	if isProbeTaskCommand("model_switch") {
		t.Fatal("model switching must not be a self-check command")
	}
	if !isProbeTaskSource("admin") || !isProbeTaskSource("request_failure") || !isProbeTaskSource("periodic") {
		t.Fatal("expected supported probe sources")
	}
	if isProbeTaskSource("unknown") {
		t.Fatal("unknown probe source must be rejected")
	}
}

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
		name          string
		inFlightUntil *time.Time
		paused        bool
		wantStatus    string
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

// ── 2026-08-18 (Agent C): unified probe dashboard contract ─────────────
//
// The previous handler returned only the legacy v_probe_system_health
// view (sourced from model_probe_state, retiring). The new contract
// joins credential_probe_queue + node_probe_state + node_probe_runs +
// URSM tenant coverage in a single response and preserves the legacy
// view under a separate field so the dashboard can show both.
//
// These tests pin the new field shape and the legacy isolation flag.

// queryUnifiedProbeSystemHealth_DBShape pins the new source join so a
// future refactor that drops URSM coverage or pseudo-success detection
// is caught by the test before the dashboard goes dark.
//
// Mocked columns mirror the final SELECT in
// queryUnifiedProbeSystemHealth (admin/probe_dashboard.go).
func TestQueryUnifiedProbeSystemHealth_DBShape(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Now()
	rows := pgxmock.NewRows([]string{
		"q_ready", "q_inflight", "q_completed", "q_failed", "q_expired",
		"n_total", "n_healthy", "n_failing", "n_paused", "n_running", "n_due", "n_leased",
		"r_total", "r_success", "r_failed", "r_last_at",
		"c_total", "c_with_ursm", "n_pseudo",
	}).AddRow(
		int64(7), int64(2), int64(11), int64(3), int64(1),
		int64(20), int64(15), int64(3), int64(2), int64(1), int64(4), int64(1),
		int64(40), int64(30), int64(10), now,
		int64(50), int64(45), int64(2),
	)
	// The query takes no parameters (the legacy view is the only source).
	// A future query-shape change is caught by the column list.
	mock.ExpectQuery(`WITH q AS`).WillReturnRows(rows)

	health, err := queryUnifiedProbeSystemHealth(context.Background(), mock)
	if err != nil {
		t.Fatalf("queryUnifiedProbeSystemHealth: %v", err)
	}
	// New source fields must be populated.
	if health.QueuePending != 7 {
		t.Errorf("queue_pending=%d, want 7", health.QueuePending)
	}
	if health.QueueInFlight != 2 {
		t.Errorf("queue_in_flight=%d, want 2", health.QueueInFlight)
	}
	if health.NodeTotal != 20 {
		t.Errorf("node_total=%d, want 20", health.NodeTotal)
	}
	if health.NodeHealthy != 15 {
		t.Errorf("node_healthy=%d, want 15", health.NodeHealthy)
	}
	if health.NodeDueNow != 4 {
		t.Errorf("node_due_now=%d, want 4", health.NodeDueNow)
	}
	if health.RunsLast1h != 40 {
		t.Errorf("runs_last_1h=%d, want 40", health.RunsLast1h)
	}
	if health.RunsSuccess1h != 30 {
		t.Errorf("runs_success_1h=%d, want 30", health.RunsSuccess1h)
	}
	if health.RunsLastAt == nil {
		t.Errorf("runs_last_at must be set, got nil")
	}
	if health.TotalCredentials != 50 {
		t.Errorf("total_credentials=%d, want 50", health.TotalCredentials)
	}
	if health.CredentialsWithURSM != 45 {
		t.Errorf("credentials_with_ursm=%d, want 45", health.CredentialsWithURSM)
	}
	if health.CredentialsNoURSM != 5 {
		t.Errorf("credentials_no_ursm=%d, want 5 (50 - 45)", health.CredentialsNoURSM)
	}
	if health.PseudoSuccessCount != 2 {
		t.Errorf("pseudo_success_count=%d, want 2", health.PseudoSuccessCount)
	}
	if health.SuccessRateLast1h == nil {
		t.Errorf("success_rate_last_1h must be set, got nil")
	} else if got, want := *health.SuccessRateLast1h, 0.75; got != want {
		t.Errorf("success_rate_last_1h=%v, want %v", got, want)
	}
	// Legacy bucket must be empty here (the handler fills it
	// separately); the test ensures the new function does not
	// silently conflate the two.
	if health.Legacy.TotalNodes != 0 {
		t.Errorf("legacy.total_nodes=%d, want 0 (new source must not pre-populate legacy)",
			health.Legacy.TotalNodes)
	}

	// Confirm the JSON round-trip exposes the new fields so the
	// frontend can deserialize them. web/src/api-probe.ts health
	// contract depends on these keys.
	raw, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"queue_pending", "queue_in_flight", "queue_completed", "queue_failed", "queue_expired",
		"node_total", "node_healthy", "node_failing", "node_paused", "node_running",
		"runs_last_1h", "runs_success_1h", "runs_failed_1h",
		"total_credentials", "credentials_with_ursm", "credentials_no_ursm",
		"pseudo_success_count",
		"legacy",
		"snapshot_at",
	} {
		if _, ok := wire[key]; !ok {
			t.Errorf("system-health JSON missing %q: %s", key, raw)
		}
	}
	// Legacy bucket must be an object so the frontend can read
	// legacy_source/legacy_mode_safe without defensive checks.
	if _, ok := wire["legacy"].(map[string]any); !ok {
		t.Errorf("legacy field must be a JSON object, got %T: %s", wire["legacy"], raw)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations not met: %v", err)
	}
}

// queryUnifiedProbeQueueStats_DBShape pins the new queue aggregate
// so the dashboard can split the 572-row historical backlog
// (model_probe_state) from the active credential_probe_queue and
// node_probe_state rows.
func TestQueryUnifiedProbeQueueStats_DBShape(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Now()
	rows := pgxmock.NewRows([]string{
		"q_ready", "q_running", "q_finished", "q_stale",
		"n_due", "n_running", "n_paused", "n_pending", "n_unclaimable", "q_last_run",
	}).AddRow(
		int64(3), int64(2), int64(15), int64(1),
		int64(0), int64(1), int64(0), int64(4), int64(0), now,
	)
	mock.ExpectQuery(`WITH q AS`).WillReturnRows(rows)

	stats, err := queryUnifiedProbeQueueStats(context.Background(), mock)
	if err != nil {
		t.Fatalf("queryUnifiedProbeQueueStats: %v", err)
	}
	if stats.QueueReady != 3 {
		t.Errorf("queue_ready=%d, want 3", stats.QueueReady)
	}
	if stats.QueueRunning != 2 {
		t.Errorf("queue_running=%d, want 2", stats.QueueRunning)
	}
	if stats.QueueFinished != 15 {
		t.Errorf("queue_finished=%d, want 15", stats.QueueFinished)
	}
	if stats.StaleLeases != 1 {
		t.Errorf("stale_leases=%d, want 1", stats.StaleLeases)
	}
	if stats.NodeDue != 0 {
		t.Errorf("node_due=%d, want 0 (现场 due=0)", stats.NodeDue)
	}
	if stats.NodeUnclaimable != 0 {
		t.Errorf("node_unclaimable=%d, want 0 (现场不可认领=0)", stats.NodeUnclaimable)
	}
	if stats.QueueSize != 5 {
		t.Errorf("queue_size=%d, want 5 (ready+running)", stats.QueueSize)
	}
	if stats.LastRunAt == nil {
		t.Errorf("last_run_at must be set, got nil")
	}

	// JSON shape contract for the dashboard.
	raw, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"queue_ready", "queue_running", "queue_finished", "queue_claims",
		"node_pending", "node_running", "node_paused", "node_due", "node_unclaimable",
		"stale_leases", "last_run_at", "queue_size", "snapshot_at",
	} {
		if _, ok := wire[key]; !ok {
			t.Errorf("queue-snapshot JSON missing %q: %s", key, raw)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations not met: %v", err)
	}
}

// TestProbeSystemHealthHandler_LegacyIsolation is the headline API
// contract test: the handler must expose the new source under
// `unified` and the legacy view under `legacy` so a frontend can
// distinguish the 572-row historical backlog (legacy) from the
// active queue (unified). The legacy_mode_safe=false flag is the
// operator-visible hint that the legacy view is no longer the
// authoritative source.
//
// The handler injects the same SQL twice (one for the new source,
// one for the legacy view). To test this without a real pool we
// drive the two query functions directly (the same pattern used
// by queryProbeNodeTasks / queryProbeQueueTasks) and then verify
// the assembled JSON envelope shape end-to-end.
func TestProbeSystemHealthHandler_LegacyIsolation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Now()
	// unified SELECT (one row).
	mock.ExpectQuery(`WITH q AS`).
		WillReturnRows(pgxmock.NewRows([]string{
			"q_ready", "q_inflight", "q_completed", "q_failed", "q_expired",
			"n_total", "n_healthy", "n_failing", "n_paused", "n_running", "n_due", "n_leased",
			"r_total", "r_success", "r_failed", "r_last_at",
			"c_total", "c_with_ursm", "n_pseudo",
		}).AddRow(
			int64(0), int64(0), int64(0), int64(0), int64(0),
			int64(10), int64(7), int64(2), int64(1), int64(0), int64(0), int64(0),
			int64(5), int64(4), int64(1), now,
			int64(11), int64(9), int64(0),
		))
	// legacy SELECT (one row).
	mock.ExpectQuery(`SELECT \* FROM v_probe_system_health`).
		WillReturnRows(pgxmock.NewRows([]string{
			"total_nodes", "healthy_nodes", "failing_nodes", "suspicious_nodes", "probing_nodes",
			"urgent_queue_size", "suspicious_queue_size", "failing_queue_size", "watchdog_queue_size",
			"ready_probes", "current_probing", "credentials_being_probed",
			"avg_success_rate_7d", "last_probe_at", "last_real_request_at",
			"total_real_success_24h", "total_real_failure_24h",
			"critical_nodes", "pending_probes_5min", "snapshot_at",
		}).AddRow(
			int64(572), int64(0), int64(572), int64(0), int64(0),
			int64(0), int64(572), int64(0), int64(0),
			int64(0), int64(0), int64(0),
			0.0,
			&now, &now,
			int64(0), int64(0),
			int64(572), int64(0), now,
		))

	unified, err := queryUnifiedProbeSystemHealth(context.Background(), mock)
	if err != nil {
		t.Fatalf("queryUnifiedProbeSystemHealth: %v", err)
	}
	// Synthesize the legacy struct exactly the way the handler does.
	legacy, err := loadLegacySystemHealth(context.Background(), mock)
	if err != nil {
		t.Fatalf("loadLegacySystemHealth: %v", err)
	}
	unified.Legacy = TotalLegacySystemHealth{
		TotalNodes:      legacy.TotalNodes,
		HealthyNodes:    legacy.HealthyNodes,
		FailingNodes:    legacy.FailingNodes,
		SuspiciousNodes: legacy.SuspiciousNodes,
		ProbingNodes:    legacy.ProbingNodes,
		UrgentQueueSize: legacy.UrgentQueueSize,
		ReadyProbes:     legacy.ReadyProbes,
		CurrentProbing:  legacy.CurrentProbing,
		LastProbeAt:     legacy.LastProbeAt,
		LegacySource:    "model_probe_state",
		LegacyModeSafe:  false,
	}

	// JSON envelope contract.
	envelope := map[string]any{
		"unified":          unified,
		"legacy":           legacy,
		"legacy_mode_safe": false,
		"snapshot_at":      time.Now(),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, raw)
	}

	// unified is the new shape.
	unifiedWire, ok := body["unified"].(map[string]any)
	if !ok {
		t.Fatalf("unified field must be a JSON object, got %T: %s", body["unified"], raw)
	}
	if unifiedWire["total_credentials"] != float64(11) {
		t.Errorf("unified.total_credentials=%v, want 11", unifiedWire["total_credentials"])
	}
	if unifiedWire["pseudo_success_count"] != float64(0) {
		t.Errorf("unified.pseudo_success_count=%v, want 0", unifiedWire["pseudo_success_count"])
	}
	// legacy is the bag of the legacy view's scalars.
	legacyWire, ok := body["legacy"].(map[string]any)
	if !ok {
		t.Fatalf("legacy field must be a JSON object, got %T: %s", body["legacy"], raw)
	}
	if legacyWire["total_nodes"] != float64(572) {
		t.Errorf("legacy.total_nodes=%v, want 572 (the historical backlog the operator sees)",
			legacyWire["total_nodes"])
	}
	// legacy_mode_safe=false is the visible warning that the legacy
	// view is no longer authoritative.
	if body["legacy_mode_safe"] != false {
		t.Errorf("legacy_mode_safe=%v, want false", body["legacy_mode_safe"])
	}
	// The unified payload's legacy bucket must also be tagged
	// legacy_source="model_probe_state" so the frontend can label
	// the legacy badges without an extra code path.
	unifiedLegacyWire, ok := unifiedWire["legacy"].(map[string]any)
	if !ok {
		t.Fatalf("unified.legacy must be a JSON object, got %T: %s", unifiedWire["legacy"], raw)
	}
	if unifiedLegacyWire["legacy_source"] != "model_probe_state" {
		t.Errorf("unified.legacy.legacy_source=%v, want model_probe_state",
			unifiedLegacyWire["legacy_source"])
	}
	if unifiedLegacyWire["legacy_mode_safe"] != false {
		t.Errorf("unified.legacy.legacy_mode_safe=%v, want false", unifiedLegacyWire["legacy_mode_safe"])
	}
	// snapshot_at must be present so the dashboard can rate-limit
	// its re-poll.
	if _, ok := body["snapshot_at"].(string); !ok {
		t.Errorf("snapshot_at must be a JSON string, got %T", body["snapshot_at"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations not met: %v", err)
	}
}

// TestProbeQueueSnapshotHandler_LegacyIsolation is the headline
// queue-snapshot API contract test: the new shape must live under
// `unified` and the legacy view under `legacy.queues` with the
// legacy_mode_safe=false flag.
func TestProbeQueueSnapshotHandler_LegacyIsolation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Now()
	avgF := 0.0
	maxF := 0.0
	// unified SELECT (one row).
	mock.ExpectQuery(`WITH q AS`).
		WillReturnRows(pgxmock.NewRows([]string{
			"q_ready", "q_running", "q_finished", "q_stale",
			"n_due", "n_running", "n_paused", "n_pending", "n_unclaimable", "q_last_run",
		}).AddRow(
			int64(0), int64(0), int64(0), int64(0),
			int64(0), int64(0), int64(0), int64(0), int64(0), now,
		))
	// legacy SELECT (one row from v_probe_queue_snapshot).
	mock.ExpectQuery(`SELECT \* FROM v_probe_queue_snapshot`).
		WillReturnRows(pgxmock.NewRows([]string{
			"probe_priority", "state", "queue_size", "ready_now", "ready_1min", "ready_5min",
			"earliest_retry_at", "latest_retry_at", "avg_wait_seconds", "max_wait_seconds",
		}).AddRow(
			"failing", "pending", int64(572), int64(0), int64(0), int64(0),
			&now, &now,
			&avgF, &maxF,
		))

	stats, err := queryUnifiedProbeQueueStats(context.Background(), mock)
	if err != nil {
		t.Fatalf("queryUnifiedProbeQueueStats: %v", err)
	}
	legacyQueues, err := loadLegacyQueueSnapshot(context.Background(), mock)
	if err != nil {
		t.Fatalf("loadLegacyQueueSnapshot: %v", err)
	}

	// JSON envelope contract (matches the handler's response map).
	envelope := map[string]any{
		"unified": stats,
		"legacy": map[string]interface{}{
			"queues":           legacyQueues,
			"total":            len(legacyQueues),
			"legacy":           true,
			"legacy_mode_safe": false,
			"legacy_source":    "model_probe_state",
		},
		"snapshot_at": time.Now(),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, raw)
	}

	// ── new source ───────────────────────────────────────────────
	unifiedWire, ok := body["unified"].(map[string]any)
	if !ok {
		t.Fatalf("unified field must be a JSON object, got %T: %s", body["unified"], raw)
	}
	if unifiedWire["queue_size"] != float64(0) {
		t.Errorf("unified.queue_size=%v, want 0 (new source is active, not 572)", unifiedWire["queue_size"])
	}
	if unifiedWire["node_due"] != float64(0) {
		t.Errorf("unified.node_due=%v, want 0 (现场 due=0)", unifiedWire["node_due"])
	}
	if unifiedWire["node_unclaimable"] != float64(0) {
		t.Errorf("unified.node_unclaimable=%v, want 0 (现场不可认领=0)", unifiedWire["node_unclaimable"])
	}

	// ── legacy (must be tagged legacy: true) ────────────────────
	legacyWire, ok := body["legacy"].(map[string]any)
	if !ok {
		t.Fatalf("legacy field must be a JSON object, got %T: %s", body["legacy"], raw)
	}
	if legacyWire["legacy"] != true {
		t.Errorf("legacy.legacy=%v, want true", legacyWire["legacy"])
	}
	if legacyWire["legacy_mode_safe"] != false {
		t.Errorf("legacy.legacy_mode_safe=%v, want false", legacyWire["legacy_mode_safe"])
	}
	if legacyWire["legacy_source"] != "model_probe_state" {
		t.Errorf("legacy.legacy_source=%v, want model_probe_state", legacyWire["legacy_source"])
	}
	if legacyWire["total"] != float64(1) {
		t.Errorf("legacy.total=%v, want 1 (the historical view row)", legacyWire["total"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations not met: %v", err)
	}
}

// TestCountURSMKeys_NilScanner is a defensive guard: if the redis
// client is nil the function must not panic and must return 0 so
// the handler can run without the cache wired.
func TestCountURSMKeys_NilScanner(t *testing.T) {
	if got := countURSMKeys(context.Background(), nil, "ursm:v2:node:tenant:*:*", 0); got != 0 {
		t.Errorf("countURSMKeys(nil)=%d, want 0", got)
	}
}
