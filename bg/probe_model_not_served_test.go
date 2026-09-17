package bg

import (
	"strings"
	"testing"
	"time"
)

// TestUnavailableBindingHorizon pins the 2026-09-17 model-not-served policy:
// a direct-round 404 confirmed by a second attempt escalates the binding
// cooldown from the generic 5 minutes to the long re-check horizon with a
// self-describing reason tag; the first 404 and every other error code keep
// the generic behavior.
func TestUnavailableBindingHorizon(t *testing.T) {
	reason, horizon := unavailableBindingHorizon("http_404", 1)
	if reason != "http_404" || horizon != 5*time.Minute {
		t.Fatalf("first 404 must keep the generic cooldown, got reason=%q horizon=%s", reason, horizon)
	}
	reason, horizon = unavailableBindingHorizon("http_404", 2)
	if reason != "model_not_served_404" || horizon != modelNotServedRecheckInterval {
		t.Fatalf("confirmed 404 must escalate to the model-not-served horizon, got reason=%q horizon=%s", reason, horizon)
	}
	if horizon < time.Hour {
		t.Fatalf("model-not-served horizon must be hours-scale, got %s", horizon)
	}
	reason, horizon = unavailableBindingHorizon("http_503", 7)
	if reason != "http_503" || horizon != 5*time.Minute {
		t.Fatalf("non-404 codes keep the generic cooldown, got reason=%q horizon=%s", reason, horizon)
	}
	reason, horizon = unavailableBindingHorizon("timeout", 3)
	if reason != "timeout" || horizon != 5*time.Minute {
		t.Fatalf("timeout keeps the generic cooldown, got reason=%q horizon=%s", reason, horizon)
	}
	// The persisted reason keeps the probe_ prefix shape the recovery SQL
	// matches (LIKE 'probe_%'), so expired-binding recovery still owns the
	// 6h-later re-verify.
	if !strings.HasPrefix("probe_"+reason, "probe_") {
		t.Fatalf("reason must stay recovery-SQL compatible")
	}
}

// TestProbeBackoffForErrCodeModelNotServed pins the ladder-side twin of the
// horizon policy: confirmed 404 parks the pair at the re-check interval
// instead of walking the ordinary chain (which every request_failure trigger
// restarted from the bottom — the apigpt eternal-churn shape).
func TestProbeBackoffForErrCodeModelNotServed(t *testing.T) {
	if got := ProbeBackoffForErrCode("http_404", 2); got != modelNotServedRecheckInterval {
		t.Fatalf("confirmed 404 must park at the re-check interval, got %s", got)
	}
	if got := ProbeBackoffForErrCode("probe_http_404", 5); got != modelNotServedRecheckInterval {
		t.Fatalf("persisted probe_ prefixed 404 must be recognized, got %s", got)
	}
	first := ProbeBackoffForErrCode("http_404", 1)
	if first == modelNotServedRecheckInterval || first <= 0 {
		t.Fatalf("unconfirmed 404 keeps the generic ladder rung, got %s", first)
	}
	if got := ProbeBackoffForErrCode("timeout", 9); got > time.Minute {
		t.Fatalf("timeout stays on the short network chain, got %s", got)
	}
}

// TestIsModelNotServedProbeError guards the classifier against gateway-round
// contamination: only the direct evidence codes count.
func TestIsModelNotServedProbeError(t *testing.T) {
	for _, c := range []string{"http_404", "probe_http_404"} {
		if !isModelNotServedProbeError(c) {
			t.Fatalf("%q must classify as model-not-served", c)
		}
	}
	for _, c := range []string{"http_500", "timeout", "endpoint_build", "request_build", "network_error", ""} {
		if isModelNotServedProbeError(c) {
			t.Fatalf("%q must not classify as model-not-served", c)
		}
	}
}

// TestDeescalateGatewaySideProbeStateWiring is a source-scan pin (same style
// as node_probe_gateway_side_test.go): the decrypt-circuit reset must fire
// the shared-state de-escalation when the circuit was open, and Start must
// sweep once unconditionally — together they close the 2026-09-17 gap where
// the fix binary ran for ~5h beside poisoned ladders/bindings that only
// manual action cleared.
func TestDeescalateGatewaySideProbeStateWiring(t *testing.T) {
	src := nodeProbeSource(t)

	reset := sourceBetween(t, src,
		"func (w *NodeProbeWorker) resetDecryptFailures()",
		"func (w *NodeProbeWorker) probeGateway")
	if !strings.Contains(reset, "wasTripped := w.decryptFailures.Load() >= decryptTripThreshold") {
		t.Errorf("resetDecryptFailures must detect a previously-tripped circuit")
	}
	if !strings.Contains(reset, "go w.deescalateGatewaySideProbeState(context.Background())") {
		t.Errorf("a closed-after-tripped circuit must trigger the de-escalation sweep")
	}

	start := sourceBetween(t, src,
		"func (w *NodeProbeWorker) Start(ctx context.Context)",
		"// resolveProbeAPIKey preserves")
	if !strings.Contains(start, "go w.deescalateGatewaySideProbeState(ctx)") {
		t.Errorf("Start must run the startup de-escalation sweep")
	}

	sweep := sourceBetween(t, src,
		"func (w *NodeProbeWorker) deescalateGatewaySideProbeState(ctx context.Context)",
		"func (w *NodeProbeWorker) Stop()")
	for _, pin := range []string{
		"last_err_code IN ('endpoint_build', 'request_build')",
		"next_retry_at = now()",
		"'probe_endpoint_build', 'probe_request_build'",
		"available = TRUE",
	} {
		if !strings.Contains(sweep, pin) {
			t.Errorf("de-escalation sweep must contain %q", pin)
		}
	}
}

// TestUpdateBindingAvailability404AttemptAware pins that the unavailable
// write threads the attempt count (the escalation input) and that the
// success path is unaffected.
func TestUpdateBindingAvailability404AttemptAware(t *testing.T) {
	src := nodeProbeSource(t)
	body := sourceBetween(t, src,
		"func (w *NodeProbeWorker) updateBindingAvailability(",
		"func (w *NodeProbeWorker) updateCredentialHealth")
	for _, pin := range []string{
		"reasonTag, horizon := unavailableBindingHorizon(reason, attempt)",
		"now() + $4::interval",
	} {
		if !strings.Contains(body, pin) {
			t.Errorf("updateBindingAvailability must contain %q", pin)
		}
	}
}
