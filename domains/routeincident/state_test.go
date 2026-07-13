package routeincident

import (
	"testing"
	"time"
)

// helper to build an Incident with only the fields DecideState reads.
func mkIncident(state State, fs, rs int) *Incident {
	return &Incident{
		State:          state,
		FailureStreak:  fs,
		RecoveryStreak: rs,
		FirstFailureAt: time.Now().Add(-time.Minute),
	}
}

func TestDecideState_NoIncident_FirstFailure(t *testing.T) {
	state, fs, rs, applied := DecideState(nil, TerminalFailure, DefaultThresholds())
	if !applied {
		t.Fatal("first failure must open a new incident")
	}
	if state != StateActive {
		t.Fatalf("want active, got %s", state)
	}
	if fs != 1 || rs != 0 {
		t.Fatalf("want streak 1/0, got %d/%d", fs, rs)
	}
}

func TestDecideState_NoIncident_SuccessIsNoop(t *testing.T) {
	_, _, _, applied := DecideState(nil, TerminalSuccess, DefaultThresholds())
	if applied {
		t.Fatal("success with no incident must be a noop")
	}
}

func TestDecideState_Active_SecondFailure(t *testing.T) {
	cur := mkIncident(StateActive, 1, 0)
	state, fs, rs, applied := DecideState(cur, TerminalFailure, DefaultThresholds())
	if !applied || state != StateActive || fs != 2 || rs != 0 {
		t.Fatalf("want active 2/0, got %s %d/%d applied=%v", state, fs, rs, applied)
	}
}

func TestDecideState_Active_ThirdFailureReachesThreshold(t *testing.T) {
	cur := mkIncident(StateActive, 2, 0)
	state, fs, _, _ := DecideState(cur, TerminalFailure, DefaultThresholds())
	if state != StateActive || fs != 3 {
		t.Fatalf("want active 3, got %s %d", state, fs)
	}
}

func TestDecideState_Active_SuccessMovesToRecovering(t *testing.T) {
	cur := mkIncident(StateActive, 3, 0)
	state, fs, rs, applied := DecideState(cur, TerminalSuccess, DefaultThresholds())
	if !applied || state != StateRecovering || fs != 0 || rs != 1 {
		t.Fatalf("want recovering 0/1, got %s %d/%d applied=%v", state, fs, rs, applied)
	}
}

func TestDecideState_Recovering_FifthSuccessRecovers(t *testing.T) {
	cur := mkIncident(StateRecovering, 0, 4)
	state, _, rs, applied := DecideState(cur, TerminalSuccess, DefaultThresholds())
	if !applied || state != StateRecovered || rs != 5 {
		t.Fatalf("want recovered 5, got %s rs=%d applied=%v", state, rs, applied)
	}
}

func TestDecideState_Recovering_FailureResetsToActive(t *testing.T) {
	cur := mkIncident(StateRecovering, 0, 3)
	state, fs, rs, applied := DecideState(cur, TerminalFailure, DefaultThresholds())
	if !applied || state != StateActive || fs != 1 || rs != 0 {
		t.Fatalf("want active 1/0, got %s %d/%d applied=%v", state, fs, rs, applied)
	}
}

func TestDecideState_Recovered_FailureReopens(t *testing.T) {
	cur := mkIncident(StateRecovered, 0, 5)
	state, fs, _, _ := DecideState(cur, TerminalFailure, DefaultThresholds())
	if state != StateActive || fs != 1 {
		t.Fatalf("want active 1 (reopen), got %s %d", state, fs)
	}
}

func TestDecideState_NonTerminalIsIgnored(t *testing.T) {
	cur := mkIncident(StateActive, 2, 0)
	state, fs, rs, applied := DecideState(cur, "in_progress", DefaultThresholds())
	if applied {
		t.Fatal("in_progress must not be a transition")
	}
	if state != StateActive || fs != 2 || rs != 0 {
		t.Fatalf("streaks should be preserved on in_progress")
	}
}

func TestIsVisible(t *testing.T) {
	cases := map[State]bool{
		StateActive:     true,
		StateRecovering: true,
		StateRecovered:  false,
		"":              false,
	}
	for s, want := range cases {
		if got := s.IsVisible(); got != want {
			t.Errorf("%s: want visible=%v got %v", s, want, got)
		}
	}
}

func TestRedactErrorKind_Bounds(t *testing.T) {
	long := make([]byte, 0, MaxErrorKindLen*4)
	for i := 0; i < MaxErrorKindLen*4; i++ {
		long = append(long, 'a')
	}
	got := RedactErrorKind(string(long))
	if len([]rune(got)) > MaxErrorKindLen {
		t.Errorf("RedactErrorKind exceeded %d runes: %d", MaxErrorKindLen, len([]rune(got)))
	}
}

func TestRedactErrorKind_EmptyAndJunk(t *testing.T) {
	if got := RedactErrorKind(""); got != "" {
		t.Errorf("empty: %q", got)
	}
	if got := RedactErrorKind("   "); got != "" {
		t.Errorf("whitespace: %q", got)
	}
	if got := RedactErrorKind("\xff\xfe\xfd"); got != "" {
		t.Errorf("non-utf8: %q", got)
	}
}

func TestSanitizeEvidence_StripsForbidden(t *testing.T) {
	in := map[string]any{
		"failure_kind":       "rate_limited",
		"Authorization":      "Bearer xxxx",         // not in allow-list
		"api_key":            "sk-live-xxx",         // not in allow-list
		"request_body":       "should never appear", // not in allow-list
		"sample_request_ids": []string{"r1", "r2"},
	}
	got := SanitizeEvidence(in)
	if got["Authorization"] != nil || got["api_key"] != nil || got["request_body"] != nil {
		t.Fatalf("forbidden keys leaked: %#v", got)
	}
	if got["failure_kind"] != "rate_limited" {
		t.Fatalf("allowed key lost: %#v", got)
	}
}

func TestSanitizeEvidence_NilSafe(t *testing.T) {
	got := SanitizeEvidence(nil)
	if got == nil {
		t.Fatal("nil-safe contract violated")
	}
	if len(got) != 0 {
		t.Fatalf("empty input should yield empty map: %#v", got)
	}
}

func TestDecideState_CustomThresholds(t *testing.T) {
	th := Thresholds{FailureToActive: 1, SuccessToRecovered: 2}
	// First failure opens at threshold=1.
	state, fs, _, _ := DecideState(nil, TerminalFailure, th)
	if state != StateActive || fs != 1 {
		t.Fatalf("threshold=1 first failure: want active 1, got %s %d", state, fs)
	}
	// First success after active moves to recovering(1/2).
	cur := mkIncident(StateActive, 1, 0)
	state, _, rs, _ := DecideState(cur, TerminalSuccess, th)
	if state != StateRecovering || rs != 1 {
		t.Fatalf("threshold=2 recovery: want recovering 1, got %s %d", state, rs)
	}
	// Second success recovers.
	cur = mkIncident(StateRecovering, 0, 1)
	state, _, rs, _ = DecideState(cur, TerminalSuccess, th)
	if state != StateRecovered || rs != 2 {
		t.Fatalf("threshold=2 recover: want recovered 2, got %s %d", state, rs)
	}
}
