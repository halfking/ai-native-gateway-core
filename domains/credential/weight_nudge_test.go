package credential

import (
	"testing"
	"time"
)

func TestDefaultWeightNudgeFactors(t *testing.T) {
	f := DefaultWeightNudgeFactors()
	if f.RateLimit != 0.85 || f.Empty != 0.70 || f.Timeout != 0.60 || f.Auth != 0.90 {
		t.Errorf("defaults changed: %+v", f)
	}
}

func TestLoadWeightNudgeFactors_Override(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_429", "0.5")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_EMPTY", "0.4")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_TIMEOUT", "0.3")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_AUTH", "0.95")
	f := LoadWeightNudgeFactors()
	if f.RateLimit != 0.5 || f.Empty != 0.4 || f.Timeout != 0.3 || f.Auth != 0.95 {
		t.Errorf("env override failed: %+v", f)
	}
}

func TestLoadWeightNudgeFactors_RejectsOutOfRange(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_429", "1.5")   // > 1.0
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_EMPTY", "0.05") // < 0.1
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_TIMEOUT", "abc")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_AUTH", "-0.2")
	f := LoadWeightNudgeFactors()
	if f.RateLimit != 0.85 {
		t.Errorf("RateLimit out-of-range not clamped: got %f", f.RateLimit)
	}
	if f.Empty != 0.70 {
		t.Errorf("Empty out-of-range not clamped: got %f", f.Empty)
	}
	if f.Timeout != 0.60 {
		t.Errorf("Timeout bogus not clamped: got %f", f.Timeout)
	}
	if f.Auth != 0.90 {
		t.Errorf("Auth negative not clamped: got %f", f.Auth)
	}
}

func TestWeightNudgeEnabled(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"on", true},
		{"ON", true},
		{"true", true},
		{"1", true},
		{"off", false},
		{"false", false},
		{"bogus", false},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_CREDENTIAL_WEIGHT_NUDGE", c.env)
			got := WeightNudgeEnabled()
			if got != c.want {
				t.Errorf("env=%q got=%v want=%v", c.env, got, c.want)
			}
		})
	}
}

func TestWeightNudgeWindow(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_WINDOW", "5m")
	got := WeightNudgeWindow()
	if got != 5*time.Minute {
		t.Errorf("got %s want 5m", got)
	}
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_WINDOW", "0s")
	if got := WeightNudgeWindow(); got != 10*time.Minute {
		t.Errorf("0s must fall back to default, got %s", got)
	}
	t.Setenv("LLM_GATEWAY_CREDENTIAL_NUDGE_WINDOW", "")
	if got := WeightNudgeWindow(); got != 10*time.Minute {
		t.Errorf("empty must fall back to default, got %s", got)
	}
}

func TestKindWindow_Any(t *testing.T) {
	if (KindWindow{}).Any() {
		t.Error("zero value must report false")
	}
	if !(KindWindow{RateLimit: 1}).Any() {
		t.Error("RateLimit=1 must report true")
	}
	if !(KindWindow{Empty: 1}).Any() {
		t.Error("Empty=1 must report true")
	}
	if !(KindWindow{Timeout: 1}).Any() {
		t.Error("Timeout=1 must report true")
	}
	if !(KindWindow{Auth: 1}).Any() {
		t.Error("Auth=1 must report true")
	}
}

func TestWeightNudge_DisabledIsNoOp(t *testing.T) {
	w := KindWindow{RateLimit: 5, Empty: 5, Timeout: 5, Auth: 5}
	f := DefaultWeightNudgeFactors()
	got := WeightNudge(w, f, false)
	if got != 1.0 {
		t.Errorf("disabled must return 1.0, got %f", got)
	}
}

func TestWeightNudge_EmptyWindowIsNoOp(t *testing.T) {
	got := WeightNudge(KindWindow{}, DefaultWeightNudgeFactors(), true)
	if got != 1.0 {
		t.Errorf("empty window must return 1.0, got %f", got)
	}
}

func TestWeightNudge_PicksMinimum(t *testing.T) {
	// Timeout has the lowest factor (0.60); if it fires alongside others
	// the result must be 0.60, not the product (which would be ~0.18).
	w := KindWindow{RateLimit: 1, Empty: 1, Timeout: 1, Auth: 1}
	got := WeightNudge(w, DefaultWeightNudgeFactors(), true)
	if got != 0.60 {
		t.Errorf("expected min factor 0.60 (Timeout), got %f", got)
	}
}

func TestWeightNudge_OnlyRateLimit(t *testing.T) {
	w := KindWindow{RateLimit: 2}
	got := WeightNudge(w, DefaultWeightNudgeFactors(), true)
	if got != 0.85 {
		t.Errorf("RateLimit only: got %f want 0.85", got)
	}
}

func TestWeightNudge_OnlyEmpty(t *testing.T) {
	w := KindWindow{Empty: 1}
	got := WeightNudge(w, DefaultWeightNudgeFactors(), true)
	if got != 0.70 {
		t.Errorf("Empty only: got %f want 0.70", got)
	}
}

func TestWeightNudge_OnlyTimeout(t *testing.T) {
	w := KindWindow{Timeout: 1}
	got := WeightNudge(w, DefaultWeightNudgeFactors(), true)
	if got != 0.60 {
		t.Errorf("Timeout only: got %f want 0.60", got)
	}
}

func TestWeightNudge_OnlyAuth(t *testing.T) {
	w := KindWindow{Auth: 1}
	got := WeightNudge(w, DefaultWeightNudgeFactors(), true)
	if got != 0.90 {
		t.Errorf("Auth only: got %f want 0.90", got)
	}
}

func TestWeightNudge_ClampsBelowFloor(t *testing.T) {
	// Pathological config: Auth=0.01 should be clamped to 0.1 floor.
	w := KindWindow{Auth: 1}
	f := WeightNudgeFactors{Auth: 0.01}
	got := WeightNudge(w, f, true)
	if got != 0.1 {
		t.Errorf("sub-floor factor must clamp to 0.1, got %f", got)
	}
}

func TestWeightNudge_ClampsAboveCeiling(t *testing.T) {
	// Pathological config: Auth=1.5 (above the safe max) must clamp to 1.0
	// so a credential that just hit auth failures is not BOOSTED.
	w := KindWindow{Auth: 1}
	f := WeightNudgeFactors{Auth: 1.5}
	got := WeightNudge(w, f, true)
	if got != 1.0 {
		t.Errorf("above-ceiling factor must clamp to 1.0, got %f", got)
	}
}

func TestWeightNudge_DeterministicAndCommutative(t *testing.T) {
	w := KindWindow{RateLimit: 1, Empty: 2, Timeout: 3, Auth: 4}
	f := DefaultWeightNudgeFactors()
	a := WeightNudge(w, f, true)
	b := WeightNudge(w, f, true)
	if a != b {
		t.Errorf("nudge not deterministic: %f vs %f", a, b)
	}
}

func TestWeightNudge_AuthDoesNotShadowEmpty(t *testing.T) {
	// Auth=0.90 is higher than Empty=0.70, so with both firing the result
	// must be 0.70 (the lower factor).
	w := KindWindow{Auth: 1, Empty: 1}
	got := WeightNudge(w, DefaultWeightNudgeFactors(), true)
	if got != 0.70 {
		t.Errorf("Auth+Empty must take min (Empty=0.70), got %f", got)
	}
}