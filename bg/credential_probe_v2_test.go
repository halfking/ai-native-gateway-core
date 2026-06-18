package bg

import (
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

func TestClassifyProbeFailure_Plain404_StillUnreachable(t *testing.T) {
	// Plain 404 without InvalidEndpointOrModel — should fall through to
	// the unreachable default (this is a real model/provider problem).
	pr := classifyProbeFailure("chat status 404: model not found")
	if pr.HealthStatus != "unreachable" {
		t.Errorf("HealthStatus: got %q want \"unreachable\" (plain 404 should still mark unreachable)", pr.HealthStatus)
	}
	if pr.StateReasonCode != "network_error" {
		t.Errorf("StateReasonCode: got %q want \"network_error\"", pr.StateReasonCode)
	}
}

func TestClassifyProbeFailure_AuthFailed(t *testing.T) {
	pr := classifyProbeFailure("401/403: invalid api key")
	if pr.HealthStatus != "auth_failed" || pr.AvailabilityState != "auth_failed" || pr.StateReasonCode != "auth_error" {
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