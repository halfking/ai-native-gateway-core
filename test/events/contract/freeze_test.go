// Package contract / freeze_test.go — T0 contract freeze (2026-08-22).
//
// Pins the exact-bytes JSON shapes for the five identities, the four restart
// lanes, the lifecycle three-state vocabulary (+ retry_at + StageTerminal +
// Outcome), and the error classification subset. Tests guard against drift in
// test/events/fixtures/identity_*.json, restart_lane_*.json,
// lifecycle_states_v1.json, error_classification_v1.json.
//
// Anchor sources:
//   - domains/requestjourney/contract.go:79-89  (StageTerminal)
//   - domains/requestjourney/contract.go:361-373 (Outcome enum)
//   - domains/dispatch/journey.go:277           (overflow dispatch classifyError)
//   - internal/liveactions/liveactions.go        (V3.3-OBS OBS-B1 action vocabulary)
package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureDoc is a permissive JSON document loader used to validate every
// fixture file. We deliberately do NOT bind to a strict schema struct — the
// fixture's contract is "exact bytes JSON shape", enforced via the per-fixture
// field assertions below.
type fixtureDoc map[string]any

func loadFixtureDoc(t *testing.T, name string) fixtureDoc {
	t.Helper()
	path := filepath.Join("..", "fixtures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var doc fixtureDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return doc
}

// ────────────────────────────────────────────────────────────────────────────
// 1) Five identities: request_id, attempt_id, gw_session_id, session_id, SessionPK
// ────────────────────────────────────────────────────────────────────────────

func TestFreezeIdentityRequestID(t *testing.T) {
	doc := loadFixtureDoc(t, "identity_request_id_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["identity_kind"].(string) != "request_id" {
		t.Fatalf("identity_kind mismatch: %v", doc["identity_kind"])
	}
	if s, _ := doc["request_id"].(string); s == "" {
		t.Fatal("request_id is required")
	}
	if s, _ := doc["tenant_id"].(string); s == "" {
		t.Fatal("tenant_id is required")
	}
	if s, _ := doc["gateway_instance_id"].(string); s == "" {
		t.Fatal("gateway_instance_id is required")
	}
	if _, ok := doc["forbidden_fields"]; !ok {
		t.Fatal("forbidden_fields section required")
	}
}

func TestFreezeIdentityAttemptID(t *testing.T) {
	doc := loadFixtureDoc(t, "identity_attempt_id_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["identity_kind"].(string) != "attempt_id" {
		t.Fatalf("identity_kind mismatch: %v", doc["identity_kind"])
	}
	if s, _ := doc["attempt_id"].(string); s == "" {
		t.Fatal("attempt_id is required")
	}
	if n, _ := doc["attempt_no"].(float64); n < 1 {
		t.Fatalf("attempt_no = %v, want >= 1", n)
	}
	if s, _ := doc["request_id"].(string); s == "" {
		t.Fatal("parent request_id is required")
	}
}

func TestFreezeIdentityGwSessionID(t *testing.T) {
	doc := loadFixtureDoc(t, "identity_gw_session_id_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["identity_kind"].(string) != "gw_session_id" {
		t.Fatalf("identity_kind mismatch: %v", doc["identity_kind"])
	}
	if s, _ := doc["gw_session_id"].(string); s == "" {
		t.Fatal("gw_session_id is required")
	}
	if doc["semantics"].(string) != "gateway_logical_session_text" {
		t.Fatalf("semantics mismatch: %v", doc["semantics"])
	}
}

func TestFreezeIdentitySessionID(t *testing.T) {
	doc := loadFixtureDoc(t, "identity_session_id_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["identity_kind"].(string) != "session_id" {
		t.Fatalf("identity_kind mismatch: %v", doc["identity_kind"])
	}
	if s, _ := doc["session_id"].(string); s == "" {
		t.Fatal("session_id is required")
	}
	if n, _ := doc["session_pk"].(float64); n <= 0 {
		t.Fatalf("session_pk = %v, want > 0", n)
	}
	if doc["semantics"].(string) != "sessions_v2_text" {
		t.Fatalf("semantics mismatch: %v", doc["semantics"])
	}
}

func TestFreezeIdentitySessionPK(t *testing.T) {
	doc := loadFixtureDoc(t, "identity_session_pk_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["identity_kind"].(string) != "session_pk" {
		t.Fatalf("identity_kind mismatch: %v", doc["identity_kind"])
	}
	if n, _ := doc["session_pk"].(float64); n <= 0 {
		t.Fatalf("session_pk = %v, want > 0", n)
	}
	if s, _ := doc["session_id"].(string); s == "" {
		t.Fatal("session_id paired value required")
	}
	if doc["semantics"].(string) != "public_sessions_numeric_surrogate" {
		t.Fatalf("semantics mismatch: %v", doc["semantics"])
	}
}

// ────────────────────────────────────────────────────────────────────────────
// 2) Four restart lanes: ordinary / pending / durable / queue-mirror
// ────────────────────────────────────────────────────────────────────────────

func TestFreezeRestartLaneOrdinary(t *testing.T) {
	doc := loadFixtureDoc(t, "restart_lane_ordinary_v1.json")
	assertLaneSemantics(t, doc, "ordinary_dispatch", restartLaneExpectation{
		ExecutionRecovery:         false,
		ResultReplay:              false,
		LeaseRequired:             false,
		FencingRequired:           false,
		MetadataRebuild:           false,
		LeaseLiveUpstreamCalls:    0,
		LeaseExpiredUpstreamCalls: 0,
		TakeoverRequiresFencing:   false,
		ExpectedProjection:        "dropped",
	})
}

func TestFreezeRestartLanePending(t *testing.T) {
	doc := loadFixtureDoc(t, "restart_lane_pending_v1.json")
	assertLaneSemantics(t, doc, "pending", restartLaneExpectation{
		ExecutionRecovery:         false,
		ResultReplay:              true,
		LeaseRequired:             false,
		FencingRequired:           false,
		MetadataRebuild:           false,
		LeaseLiveUpstreamCalls:    0,
		LeaseExpiredUpstreamCalls: 0,
		TakeoverRequiresFencing:   false,
		ExpectedProjection:        "replayed_result",
	})
}

func TestFreezeRestartLaneDurable(t *testing.T) {
	doc := loadFixtureDoc(t, "restart_lane_durable_v1.json")
	assertLaneSemantics(t, doc, "durable", restartLaneExpectation{
		ExecutionRecovery:         true,
		ResultReplay:              true,
		LeaseRequired:             true,
		FencingRequired:           true,
		MetadataRebuild:           false,
		LeaseLiveUpstreamCalls:    0,
		LeaseExpiredUpstreamCalls: 1,
		TakeoverRequiresFencing:   true,
		ExpectedProjection:        "recovered_terminal",
	})
}

func TestFreezeRestartLaneQueueMirror(t *testing.T) {
	doc := loadFixtureDoc(t, "restart_lane_queue_mirror_v1.json")
	assertLaneSemantics(t, doc, "queue_mirror", restartLaneExpectation{
		ExecutionRecovery:         false,
		ResultReplay:              false,
		LeaseRequired:             false,
		FencingRequired:           false,
		MetadataRebuild:           true,
		LeaseLiveUpstreamCalls:    0,
		LeaseExpiredUpstreamCalls: 0,
		TakeoverRequiresFencing:   false,
		ExpectedProjection:        "metadata_only",
	})
}

type restartLaneExpectation struct {
	ExecutionRecovery         bool
	ResultReplay              bool
	LeaseRequired             bool
	FencingRequired           bool
	MetadataRebuild           bool
	LeaseLiveUpstreamCalls    int
	LeaseExpiredUpstreamCalls int
	TakeoverRequiresFencing   bool
	ExpectedProjection        string
}

func assertLaneSemantics(t *testing.T, doc fixtureDoc, wantLane string, want restartLaneExpectation) {
	t.Helper()
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("%s: schema_version = %v, want 1", wantLane, got)
	}
	if doc["lane"].(string) != wantLane {
		t.Fatalf("lane = %v, want %s", doc["lane"], wantLane)
	}
	sem, ok := doc["semantics"].(map[string]any)
	if !ok {
		t.Fatalf("%s: semantics object missing", wantLane)
	}
	if b, _ := sem["execution_recovery"].(bool); b != want.ExecutionRecovery {
		t.Fatalf("%s: execution_recovery = %v, want %v", wantLane, b, want.ExecutionRecovery)
	}
	if b, _ := sem["result_replay"].(bool); b != want.ResultReplay {
		t.Fatalf("%s: result_replay = %v, want %v", wantLane, b, want.ResultReplay)
	}
	if b, _ := sem["lease_required"].(bool); b != want.LeaseRequired {
		t.Fatalf("%s: lease_required = %v, want %v", wantLane, b, want.LeaseRequired)
	}
	if b, _ := sem["fencing_required"].(bool); b != want.FencingRequired {
		t.Fatalf("%s: fencing_required = %v, want %v", wantLane, b, want.FencingRequired)
	}
	if b, _ := sem["metadata_rebuild"].(bool); b != want.MetadataRebuild {
		t.Fatalf("%s: metadata_rebuild = %v, want %v", wantLane, b, want.MetadataRebuild)
	}
	if n, _ := sem["lease_live_upstream_calls"].(float64); int(n) != want.LeaseLiveUpstreamCalls {
		t.Fatalf("%s: lease_live_upstream_calls = %v, want %d", wantLane, n, want.LeaseLiveUpstreamCalls)
	}
	if n, _ := sem["lease_expired_upstream_calls"].(float64); int(n) != want.LeaseExpiredUpstreamCalls {
		t.Fatalf("%s: lease_expired_upstream_calls = %v, want %d", wantLane, n, want.LeaseExpiredUpstreamCalls)
	}
	if b, _ := sem["takeover_requires_fencing"].(bool); b != want.TakeoverRequiresFencing {
		t.Fatalf("%s: takeover_requires_fencing = %v, want %v", wantLane, b, want.TakeoverRequiresFencing)
	}
	if s, _ := sem["expected_projection"].(string); s != want.ExpectedProjection {
		t.Fatalf("%s: expected_projection = %v, want %v", wantLane, s, want.ExpectedProjection)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// 3) Lifecycle three states + retry_at + StageTerminal + Outcome
// ────────────────────────────────────────────────────────────────────────────

func TestFreezeLifecycleThreeStates(t *testing.T) {
	doc := loadFixtureDoc(t, "lifecycle_states_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["unknown_version"].(string) != "reject" {
		t.Fatalf("unknown_version mismatch: %v", doc["unknown_version"])
	}
	states, ok := doc["lifecycle_states"].([]any)
	if !ok {
		t.Fatal("lifecycle_states array missing")
	}
	if len(states) != 3 {
		t.Fatalf("lifecycle states = %d, want exactly 3 (pending/in_flight/completed)", len(states))
	}
	wantStates := map[string]bool{"pending": true, "in_flight": true, "completed": true}
	seen := map[string]bool{}
	for _, s := range states {
		entry := s.(map[string]any)
		state, _ := entry["lifecycle_state"].(string)
		seen[state] = true
	}
	for k := range wantStates {
		if !seen[k] {
			t.Fatalf("missing lifecycle state %q (got %v)", k, seen)
		}
	}

	// retry_at section
	retry, ok := doc["retry_at_rules"].(map[string]any)
	if !ok {
		t.Fatal("retry_at_rules section missing")
	}
	if retry["field_name"].(string) != "retry_at" {
		t.Fatalf("retry_at field_name = %v", retry["field_name"])
	}
	appliesTo, _ := retry["applies_to_event_types"].([]any)
	if len(appliesTo) != 1 || appliesTo[0].(string) != "retry_scheduled" {
		t.Fatalf("retry_at applies_to_event_types = %v, want [retry_scheduled]", appliesTo)
	}

	// StageTerminal
	st, ok := doc["stage_terminal"].(map[string]any)
	if !ok {
		t.Fatal("stage_terminal section missing")
	}
	if st["stage_value"].(string) != "terminal" {
		t.Fatalf("stage_terminal.stage_value = %v", st["stage_value"])
	}
	if st["category"].(string) != "terminal" {
		t.Fatalf("stage_terminal.category = %v", st["category"])
	}

	// Outcome vocabulary
	outcomes, ok := doc["outcome_vocabulary"].(map[string]any)
	if !ok {
		t.Fatal("outcome_vocabulary section missing")
	}
	wantOutcomes := []string{"success", "failure", "canceled"}
	for _, k := range wantOutcomes {
		if _, ok := outcomes[k]; !ok {
			t.Fatalf("outcome_vocabulary missing %q (got %v)", k, mapKeys(outcomes))
		}
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ────────────────────────────────────────────────────────────────────────────
// 4) Error classification subset (11 kinds)
// ────────────────────────────────────────────────────────────────────────────

func TestFreezeErrorClassificationSubset(t *testing.T) {
	doc := loadFixtureDoc(t, "error_classification_v1.json")
	if got := doc["schema_version"].(float64); got != 1 {
		t.Fatalf("schema_version = %v, want 1", got)
	}
	if doc["policy"].(string) != "open_with_frozen_dispatch_subset" {
		t.Fatalf("policy = %v", doc["policy"])
	}
	if got := doc["frozen_subset_size"].(float64); got != 11 {
		t.Fatalf("frozen_subset_size = %v, want 11", got)
	}
	kinds, ok := doc["error_kinds"].([]any)
	if !ok {
		t.Fatal("error_kinds array missing")
	}
	if len(kinds) != 11 {
		t.Fatalf("error_kinds count = %d, want 11", len(kinds))
	}
	want := []string{
		"network", "eof_without_done", "empty_response",
		"route_transient", "state_transient", "queue_overflow",
		"no_candidate", "invalid_model", "auth_failed",
		"rate_limit", "quota",
	}
	seen := map[string]bool{}
	for _, k := range kinds {
		entry := k.(map[string]any)
		v := entry["value"].(string)
		seen[v] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("missing error_kind %q (got %v)", name, mapKeysFromBools(seen))
		}
	}
}

func mapKeysFromBools(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}