// bg/model_probe_test.go — unit tests for the consensus state machine
package bg

import "testing"

func TestComputeConsensus_NoPriorState(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("ok", probeCategoryOK, "unknown", "", 0, 0)
	if sc != "unchanged" || !applied || succ != 1 || fail != 0 || st != "recovering" {
		t.Errorf("1st success: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_3ConsecutiveSuccesses(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("ok", probeCategoryOK, "recovering", "", 2, 0)
	if sc != "recovered" || !applied || succ != 3 || fail != 0 || st != "healthy_confirmed" {
		t.Errorf("3rd success: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_ModelUnavailableCountsAsFailure(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("http_4xx", probeCategoryModelUnavailable, "recovering", "", 2, 0)
	if sc != "unchanged" || !applied || succ != 0 || fail != 1 || st != "recovering" {
		t.Errorf("model_unavailable: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_3ConsecutiveModelUnavailable(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("http_4xx", probeCategoryModelUnavailable, "recovering", "", 0, 2)
	if sc != "broke" || !applied || succ != 0 || fail != 3 || st != "broken_confirmed" {
		t.Errorf("3rd model_unavailable: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_ProviderErrorDoesNotCountAsFailure(t *testing.T) {
	_, _, succ, fail, st := computeConsensusForTest("auth", probeCategoryProviderError, "unknown", "decrypt_error", 0, 1)
	if succ != 0 || fail != 1 || st != "recovering" {
		t.Errorf("provider_error auth: got (succ=%d fail=%d state=%s)", succ, fail, st)
	}
}

func TestComputeConsensus_NetworkErrorDoesNotCountAsFailure(t *testing.T) {
	_, _, succ, fail, st := computeConsensusForTest("network", probeCategoryProviderError, "unknown", "", 0, 1)
	if succ != 0 || fail != 1 || st != "recovering" {
		t.Errorf("provider_error network: got (succ=%d fail=%d state=%s)", succ, fail, st)
	}
}

func TestComputeConsensus_Http5xxErrorDoesNotCountAsFailure(t *testing.T) {
	_, _, succ, fail, st := computeConsensusForTest("http_5xx", probeCategoryProviderError, "recovering", "", 2, 2)
	if succ != 0 || fail != 2 || st != "recovering" {
		t.Errorf("provider_error http_5xx: got (succ=%d fail=%d state=%s) — fail must NOT increment", succ, fail, st)
	}
}

func TestComputeConsensus_RateLimitedDoesNotCountAsFailure(t *testing.T) {
	_, _, succ, fail, st := computeConsensusForTest("http_4xx", probeCategoryProviderError, "recovering", "rate_limited", 0, 2)
	if succ != 0 || fail != 2 || st != "recovering" {
		t.Errorf("rate_limited: got (succ=%d fail=%d state=%s) — fail must NOT increment", succ, fail, st)
	}
}

func TestComputeConsensus_Skipped(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("skipped", probeCategorySkipped, "recovering", "", 1, 1)
	if sc != "unchanged" || applied || succ != 1 || fail != 1 || st != "recovering" {
		t.Errorf("skipped: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_Skipped_EndpointIDRequired_ResetsFailures(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("skipped", probeCategorySkipped, "broken_confirmed", "endpoint_id_required", 0, 3)
	if sc != "unchanged" || !applied || succ != 0 || fail != 0 || st != "recovering" {
		t.Errorf("endpoint_id_required: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_HealthyConfirmed_WatchdogSuccess(t *testing.T) {
	sc, applied, succ, fail, st := computeConsensusForTest("ok", probeCategoryOK, "healthy_confirmed", "", 5, 0)
	if sc != "unchanged" || !applied || succ != 6 || fail != 0 || st != "healthy_confirmed" {
		t.Errorf("watchdog: got (%s,%v,%d,%d,%s)", sc, applied, succ, fail, st)
	}
}

func computeConsensusForTest(status string, category probeCategory, prevState, errCode string, prevSucc, prevFail int) (
	stateChange string, applied bool, newSucc, newFail int, newState string,
) {
	r := &ModelProbeRunner{}
	return r.computeConsensus(status, category, prevState, errCode, prevSucc, prevFail)
}
