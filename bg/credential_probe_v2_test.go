package bg

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestClassifyProbeFailure_EndpointIDRequired(t *testing.T) {
	// Realistic Volcano Ark error body — should NOT be marked unreachable.
	pr := classifyProbeFailure("endpoint_id_required: chat status 404: {\"error\":{\"code\":\"InvalidEndpointOrModel.NotFound\",\"message\":\"The model or endpoint glm-5.1 does not exist or you do not have access to it.\"}}")
	if pr.HealthStatus != "warning" {
		t.Errorf("HealthStatus: got %q want \"warning\" (credential is reachable; only this model needs endpoint ID)", pr.HealthStatus)
	}
	if pr.AvailabilityState != "ready" {
		t.Errorf("AvailabilityState: got %q want \"ready\" (other models on this credential must remain routable)", pr.AvailabilityState)
	}
	if pr.StateReasonCode != "endpoint_id_required" {
		t.Errorf("StateReasonCode: got %q want \"endpoint_id_required\"", pr.StateReasonCode)
	}
	if !strings.Contains(pr.HealthError, "endpoint_id_required") {
		t.Errorf("HealthError should preserve endpoint_id_required hint, got %q", pr.HealthError)
	}
}

func TestClassifyProbeFailure_Plain404IsModelBindingOnly(t *testing.T) {
	// A probe reached the selected model endpoint and received its explicit
	// model-not-found response. The credential may still serve sibling models.
	pr := classifyProbeFailure("chat status 404: model not found")
	if pr.HealthStatus != "warning" || !pr.BindingOnly {
		t.Errorf("plain model 404 classification wrong: %+v", pr)
	}
	if pr.AvailabilityState != "ready" || pr.StateReasonCode != "model_binding_error" {
		t.Errorf("plain model 404 must preserve credential availability: %+v", pr)
	}
}

func TestClassifyProbeFailure_ModelBindingDoesNotDisableCredential(t *testing.T) {
	for _, errMsg := range []string{
		"chat status 404: model not found",
		"chat status 410: model has been deprecated",
		"endpoint_id_required: chat status 404",
	} {
		pr := classifyProbeFailure(errMsg)
		if !pr.BindingOnly {
			t.Errorf("%q BindingOnly = false, want true", errMsg)
		}
		if pr.AvailabilityState != "ready" {
			t.Errorf("%q AvailabilityState = %q, want ready", errMsg, pr.AvailabilityState)
		}
		if pr.QuotaState != "" {
			t.Errorf("%q QuotaState = %q, want empty", errMsg, pr.QuotaState)
		}
	}
}

func TestClassifyProbeFailure_ModelsEndpointFailureRemainsCredentialScoped(t *testing.T) {
	pr := classifyProbeFailure("models endpoint unreachable (after 3 attempts): status 404: model not found")
	if pr.BindingOnly {
		t.Fatalf("models endpoint failure must remain credential-scoped: %+v", pr)
	}
	if pr.AvailabilityState != "unreachable" {
		t.Fatalf("models endpoint AvailabilityState = %q, want unreachable", pr.AvailabilityState)
	}
}

func TestClassifyProbeFailure_AuthFailed(t *testing.T) {
	pr := classifyProbeFailure("401/403: invalid api key")
	if pr.HealthStatus != "unreachable" || pr.AvailabilityState != "auth_failed" || pr.StateReasonCode != "auth_error" {
		t.Errorf("auth classification wrong: %+v", pr)
	}
}

func TestClassifyProbeFailure_RateLimited(t *testing.T) {
	pr := classifyProbeFailure("429 rate limited")
	if pr.HealthStatus != "warning" || pr.AvailabilityState != "rate_limited" || pr.StateReasonCode != "rate_limited" {
		t.Errorf("429 classification wrong: %+v", pr)
	}
	if pr.AvailabilityRecoverAt == nil {
		t.Errorf("429 should set AvailabilityRecoverAt")
	}
}

func TestClassifyProbeFailure_BalanceLow(t *testing.T) {
	pr := classifyProbeFailure("402 payment required: balance too low")
	if pr.HealthStatus != "warning" || pr.QuotaState != "periodic_exhausted" || pr.StateReasonCode != "balance_low" {
		t.Errorf("402 classification wrong: %+v", pr)
	}
}

func TestClassifyProbeFailure_Network_Unreachable(t *testing.T) {
	pr := classifyProbeFailure("chat unreachable: dial tcp: i/o timeout")
	if pr.HealthStatus != "unreachable" || pr.AvailabilityState != "unreachable" || pr.StateReasonCode != "network_error" {
		t.Errorf("network classification wrong: %+v", pr)
	}
	if pr.AvailabilityRecoverAt == nil {
		t.Errorf("network error should set AvailabilityRecoverAt")
	}
}

// TestWriteHealth_HardQuotaBypassOnSuccess pins the 2026-08-07 P0 fix
// to writeHealth(). Production evidence (cred 34 zhima-1):
//
//	quota_state='permanently_exhausted' + availability_state='suspended'
//	balance_quota_probe probes it every 2 min, returns 200 healthy,
//	but the WHERE clause unconditionally filtered out hard-quota
//	credentials → 0 rows affected → permanent deadlock.
//
// New contract: a successful probe (writing quota_state='ok') MUST
// bypass the hard-quota guard. The probe's 200 response is fresher
// evidence than the historical quota_state (which records "exhausted
// at probe time T0"); once we see a healthy response at T1, we must
// allow the flip regardless of quota_state's history.
//
// Conversely, a failed probe (writing quota_state='permanently_exhausted'
// or NULL) MUST still be filtered for hard-quota credentials so we
// don't accidentally clear their state on transient errors.
//
// We assert this by reading the source and verifying the WHERE guard
// uses an OR with the probe-success branch.
func TestWriteHealth_HardQuotaBypassOnSuccess(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	// The bypass clause: probe success ($8='ok') OR not a hard-quota cred.
	pattern := regexp.MustCompile(
		`COALESCE\(\$8,\s*''\)\s*=\s*'ok'\s*` +
			`OR\s+quota_state\s+NOT\s+IN\s*\(\s*'permanently_exhausted',\s*'balance_exhausted'\s*\)`,
	)
	if !pattern.MatchString(body) {
		t.Fatalf("writeHealth hard-quota guard missing OR-bypass for probe success — 2026-08-07 P0 regression")
	}
	// Tolerant negative: the OLD unconditional quota_state NOT IN(...)
	// must no longer be the entire WHERE clause (without the OR-bypass).
	oldPattern := regexp.MustCompile(
		`AND\s+quota_state\s+NOT\s+IN\s*\(\s*'permanently_exhausted',\s*'balance_exhausted'\s*\)`,
	)
	if oldPattern.MatchString(body) {
		t.Fatalf("writeHealth still has unconditional hard-quota guard without OR-bypass — deadlock regression")
	}
}

// TestWriteHealth_HardQuotaFailureBooksKeeping pins the 2026-09-14 audit
// A-P1-1 fix: when a probe of a hard-quota row FAILS ($8=NULL), the main
// UPDATE matches 0 rows (hard-quota guard) — but the failure must still
// advance last_probe_at / probe_consecutive_failures via the dedicated
// bookkeeping UPDATE. Without it the exponential due-gate never climbs and
// the dead upstream is re-bombed on every 2-min tick (the exact shape
// f8322dc04 R4 set out to eliminate). The bookkeeping UPDATE must NOT touch
// quota_state (hard-quota state stays authoritative) and must keep the
// lifecycle/manual-disabled guards.
func TestWriteHealth_HardQuotaFailureBooksKeeping(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// The 0-rows branch gates the bookkeeping UPDATE on failures only.
		"if pr.HealthStatus != \"healthy\" {",
		// The bookkeeping UPDATE advances the probe ladder…
		"probe_consecutive_failures = COALESCE(credentials.probe_consecutive_failures, 0) + 1",
		"last_probe_success = FALSE",
		// …targets ONLY hard-quota rows (the guard-miss case)…
		"AND quota_state IN ('permanently_exhausted', 'balance_exhausted')",
		// …and keeps the manual-disable guard.
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("hard-quota failure bookkeeping is missing %q (A-P1-1 regression)", want)
		}
	}
	// The bookkeeping UPDATE must not write quota_state (COALESCE($8, …) /
	// quota assignments belong to the main write only). Extract the
	// bookkeeping statement and check it has no quota_state assignment.
	start := strings.Index(body, "if pr.HealthStatus != \"healthy\" {")
	if start < 0 {
		t.Fatalf("bookkeeping branch not found")
	}
	end := strings.Index(body[start:], "slog.Info(\"credential probe v2: writeHealth skipped stale result\"")
	if end < 0 {
		t.Fatalf("bookkeeping branch end not found")
	}
	bookkeeping := body[start : start+end]
	if strings.Contains(bookkeeping, "quota_state =") {
		t.Fatalf("bookkeeping UPDATE must not assign quota_state (hard-quota state must stay authoritative)")
	}
	if !strings.Contains(bookkeeping, "COALESCE(manual_disabled, FALSE) = FALSE") {
		t.Fatalf("bookkeeping UPDATE must keep the manual-disable guard")
	}
}

// TestWriteHealth_EvidenceAtOptimisticGate pins the 2026-09-14 audit R28 #5
// fix: a FAILURE verdict is only evidence from the probe's start time — the
// network I/O can take tens of seconds and a state write landing mid-flight
// (admin reset, balance_floor guard pull, writer.go quota branch) is fresher
// and must not be clobbered. Contract:
//
//   - probeResult carries EvidenceAt (probe-start timestamp);
//   - every failure-producing call site stamps it (cycleAll per-row T0,
//     cycleAll decrypt-failure literal, ProbeNow via its existing probeStart);
//   - the main UPDATE WHERE gains `($ok OR $evidence IS NULL OR
//     state_updated_at < $evidence)` — success writes stay unconditional
//     (same semantics as the hard-quota OR-bypass pinned above), unset
//     evidence fails open;
//   - the R27 bookkeeping UPDATE (A-P1-1) must stay UNgated — bookkeeping is
//     probe self-accounting, not a health verdict, and must never be lost to
//     a race;
//   - the race case gets its own log line, separate from the pre-existing
//     "skipped stale result" guard-miss message;
//   - the race-discriminating SELECT must run BEFORE the bookkeeping UPDATE
//     (which stamps state_updated_at and would mask every guard miss as a
//     race).
func TestWriteHealth_EvidenceAtOptimisticGate(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// Evidence stamp flows from probe start to writeHealth.
		"EvidenceAt time.Time",
		"rowT0 := time.Now()",
		"pr.EvidenceAt = rowT0",
		"pr.EvidenceAt = probeStart",
		"EvidenceAt:",
		// Gate in the main UPDATE: success unconditional, unset evidence
		// fails open, failure only when nobody wrote during the probe.
		"OR $11::timestamptz IS NULL",
		"COALESCE(state_updated_at, to_timestamp(0)) < $11::timestamptz",
		// evidenceAt is bound as the 11th parameter of the main UPDATE.
		"credID, evidenceAt)",
		// Split race-lost log, distinct from the guard-miss message.
		"writeHealth dropped stale failure result (state changed during probe)",
		"writeHealth skipped stale result",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("EvidenceAt optimistic gate is missing %q (R28 #5 regression)", want)
		}
	}

	// The R27 bookkeeping UPDATE must remain UNgated by the evidence
	// condition — extract it exactly like
	// TestWriteHealth_HardQuotaFailureBooksKeeping and assert no $11/evidence
	// parameter leaked into its WHERE.
	bookStart := strings.Index(body, "if pr.HealthStatus != \"healthy\" {")
	if bookStart < 0 {
		t.Fatalf("bookkeeping branch not found")
	}
	bookEnd := strings.Index(body[bookStart:], "slog.Info(\"credential probe v2: writeHealth skipped stale result\"")
	if bookEnd < 0 {
		t.Fatalf("bookkeeping branch end not found")
	}
	bookkeeping := body[bookStart : bookStart+bookEnd]
	for _, banned := range []string{"$11", "evidenceAt", "state_updated_at <"} {
		if strings.Contains(bookkeeping, banned) {
			t.Fatalf("R27 bookkeeping UPDATE must stay unconditional, found %q inside it (A-P1-1 regression)", banned)
		}
	}

	// The race-discriminating SELECT must run BEFORE the bookkeeping branch:
	// bookkeeping stamps state_updated_at and would turn every guard miss
	// into a false "race lost" diagnosis.
	raceIdx := strings.Index(body, "touchedDuringProbe")
	if raceIdx < 0 {
		t.Fatalf("race-discriminating SELECT not found")
	}
	if raceIdx > bookStart {
		t.Fatalf("race SELECT must run before the bookkeeping UPDATE (bookkeeping stamps state_updated_at)")
	}
}

func TestWriteHealth_ClosesBindingFailuresWithoutCredentialWideWrite(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"if pr.BindingOnly {",
		"c.writeBindingUnavailable(execCtx, credID, pr)",
		"c.restoreBindingOnProbeSuccess(execCtx, credID, pr.HealthProbeModel)",
		"available := pr.AvailabilityState == \"ready\" && !pr.BindingOnly",
		"state = \"model_binding\"",
		"modelAvailable := available",
		"if pr.BindingOnly {",
		"modelAvailable = model != pr.HealthProbeModel",
		"Available:     modelAvailable",
		"UPDATE credential_model_bindings cmb",
		"pm.raw_model_name = $2",
		"cmb.unavailable_reason = 'auto_probe_model_binding'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("binding failure closure is missing %q", want)
		}
	}
}

func TestWriteHealth_FansOutBoundRawModels(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"func (c *CredentialProbeV2) loadBoundRawModels(",
		"COALESCE(cmb.available, TRUE) = TRUE",
		"COALESCE(pm.available, TRUE) = TRUE",
		"if err := rows.Err(); err != nil",
		// 2026-08-26 self-check audit: pin the failure-branch call site
		// (the cache mirrors current DB state when the probe is sick).
		// The healthy-ready branch uses loadBoundRawModelsAll instead;
		// covered by TestCredentialProbeV2_CacheFanOutIncludesAllBindings.
		"uniqueStringSet([]string{pr.HealthProbeModel}, c.loadBoundRawModels(execCtx, credID))",
		"for _, model := range writeModels",
		"c.cache.Set(execCtx, credID, model",
		"Model:         model",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("raw-model fan-out is missing %q", want)
		}
	}
	if strings.Contains(body, "cm.archived") || strings.Contains(body, "pm.archived") {
		t.Fatal("raw-model fan-out must not reference nonexistent archived columns")
	}
}

func TestUniqueStringSet(t *testing.T) {
	got := uniqueStringSet([]string{"default", "a", "a"}, []string{"b", "default", ""})
	want := []string{"default", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
