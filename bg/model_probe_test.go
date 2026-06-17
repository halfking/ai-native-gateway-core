// bg/model_probe_test.go — unit tests for the consensus state machine
//
// These tests exercise ModelProbeRunner.computeConsensus in isolation,
// so they don't need a database connection.
package bg

import "testing"

func TestComputeConsensus_NoPriorState(t *testing.T) {
	// 1st success on a freshly-discovered failing binding: counter
	// goes 0→1, state stays 'recovering', no change applied.
	sc, applied, succ, fail, st := computeConsensusForTest("ok", "unknown", 0, 0)
	if sc != "unchanged" || !applied || succ != 1 || fail != 0 || st != "recovering" {
		t.Errorf("1st success: got (%s,%v,%d,%d,%s) want (unchanged,true,1,0,recovering)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_3ConsecutiveSuccesses(t *testing.T) {
	// 3rd consecutive success on a recovering binding: state flips
	// to healthy_confirmed, state_change = 'recovered'.
	sc, applied, succ, fail, st := computeConsensusForTest("ok", "recovering", 2, 0)
	if sc != "recovered" || !applied || succ != 3 || fail != 0 || st != "healthy_confirmed" {
		t.Errorf("3rd success: got (%s,%v,%d,%d,%s) want (recovered,true,3,0,healthy_confirmed)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_SuccessResetsFailures(t *testing.T) {
	// 1st success after 2 failures: succ=1, fail=0, state recovering.
	sc, applied, succ, fail, st := computeConsensusForTest("ok", "recovering", 0, 2)
	if sc != "unchanged" || !applied || succ != 1 || fail != 0 || st != "recovering" {
		t.Errorf("success after failures: got (%s,%v,%d,%d,%s) want (unchanged,true,1,0,recovering)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_FailureResetsSuccesses(t *testing.T) {
	// 1st failure after 2 successes: succ=0, fail=1, state recovering.
	sc, applied, succ, fail, st := computeConsensusForTest("http_4xx", "recovering", 2, 0)
	if sc != "unchanged" || !applied || succ != 0 || fail != 1 || st != "recovering" {
		t.Errorf("failure after successes: got (%s,%v,%d,%d,%s) want (unchanged,true,0,1,recovering)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_3ConsecutiveFailures(t *testing.T) {
	// 3rd consecutive failure: state = broken_confirmed, state_change = 'broke'.
	sc, applied, succ, fail, st := computeConsensusForTest("http_5xx", "recovering", 0, 2)
	if sc != "broke" || !applied || succ != 0 || fail != 3 || st != "broken_confirmed" {
		t.Errorf("3rd failure: got (%s,%v,%d,%d,%s) want (broke,true,0,3,broken_confirmed)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_Skipped(t *testing.T) {
	// Skipped must never change counters or state.
	sc, applied, succ, fail, st := computeConsensusForTest("skipped", "recovering", 1, 1)
	if sc != "unchanged" || applied || succ != 1 || fail != 1 || st != "recovering" {
		t.Errorf("skipped: got (%s,%v,%d,%d,%s) want (unchanged,false,1,1,recovering)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_AuthFailureCountsAsFailure(t *testing.T) {
	// auth failures count toward the failure counter just like any other.
	_, _, succ, fail, st := computeConsensusForTest("auth", "unknown", 0, 1)
	if succ != 0 || fail != 2 || st != "recovering" {
		t.Errorf("auth failure: got (succ=%d fail=%d state=%s) want (0,2,recovering)", succ, fail, st)
	}
}

func TestComputeConsensus_NetworkFailureCountsAsFailure(t *testing.T) {
	_, _, succ, fail, st := computeConsensusForTest("network", "unknown", 0, 1)
	if succ != 0 || fail != 2 || st != "recovering" {
		t.Errorf("network failure: got (succ=%d fail=%d state=%s) want (0,2,recovering)", succ, fail, st)
	}
}

func TestComputeConsensus_HealthyConfirmed_OneMoreFailure(t *testing.T) {
	// A single failure on a healthy_confirmed binding drops it back
	// to recovering and starts the failure counter fresh.
	sc, applied, succ, fail, st := computeConsensusForTest("http_4xx", "healthy_confirmed", 3, 0)
	if sc != "unchanged" || !applied || succ != 0 || fail != 1 || st != "recovering" {
		t.Errorf("healthy->recovering: got (%s,%v,%d,%d,%s) want (unchanged,true,0,1,recovering)",
			sc, applied, succ, fail, st)
	}
}

func TestComputeConsensus_HealthyConfirmed_WatchdogSuccess(t *testing.T) {
	// A watchdog success on a healthy_confirmed binding stays there
	// and does NOT re-fire 'recovered' (that would spam the state
	// change log on every 2h tick).
	sc, applied, succ, fail, st := computeConsensusForTest("ok", "healthy_confirmed", 5, 0)
	if sc != "unchanged" || !applied || succ != 6 || fail != 0 || st != "healthy_confirmed" {
		t.Errorf("watchdog success: got (%s,%v,%d,%d,%s) want (unchanged,true,6,0,healthy_confirmed)",
			sc, applied, succ, fail, st)
	}
}

// computeConsensusForTest is a thin wrapper that takes ints (not strings)
// for the success/fail counters so the test signatures stay readable.
func computeConsensusForTest(status, prevState string, prevSucc, prevFail int) (
	stateChange string, applied bool, newSucc, newFail int, newState string,
) {
	// Build a runner just to call the unexported method.  All its
	// fields are nil-safe — computeConsensus is pure.
	r := &ModelProbeRunner{}
	return r.computeConsensus(status, prevState, prevSucc, prevFail)
}
