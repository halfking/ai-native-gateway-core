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
		"Available:     pr.AvailabilityState == \"ready\" && !pr.BindingOnly",
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
		"writeModels := uniqueStringSet([]string{pr.HealthProbeModel}, c.loadBoundRawModels(execCtx, credID))",
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
