package settings

// spec_rate_policy_test.go — token 桶 W1 节奏校准旋钮的 spec 契约门
//（默认值 / clamp 边界 / 0 哨兵不被 Min 收走）。复用 registry_test.go 的
// fakeBackend（同包）。

import (
	"strconv"
	"testing"
)

func newRatePolicyTestGlobal(t *testing.T, refill, threshold string) {
	t.Helper()
	registry := NewRegistry()
	store := map[string][]byte{}
	if refill != "" {
		store[KeyRateWindowRefillSeconds] = []byte(refill)
	}
	if threshold != "" {
		store[KeyRatePaceViolationThreshold] = []byte(threshold)
	}
	registry.RegisterBackend(ScopePlatform, &fakeBackend{store: store})
	for _, sp := range RatePolicySpecs() {
		if err := registry.RegisterSpec(sp); err != nil {
			t.Fatalf("register %s: %v", sp.Key, err)
		}
	}
	prev := Global
	Global = registry
	t.Cleanup(func() { Global = prev })
}

func TestRatePolicySpecs_DefaultsWhenUnset(t *testing.T) {
	newRatePolicyTestGlobal(t, "", "")
	if got := RateWindowRefillSeconds(); got != 60 {
		t.Fatalf("unset refill = %d want 60 (spec default)", got)
	}
	if got := RatePaceViolationThreshold(); got != 3 {
		t.Fatalf("unset threshold = %d want 3 (spec default)", got)
	}
}

func TestRatePolicySpecs_ZeroIsLegacySentinelNotClamped(t *testing.T) {
	newRatePolicyTestGlobal(t, "0", "2")
	// 0 = 遗留 A2 哨兵：Min=0 ⇒ 不得被收走（收走 = 逃生口失效 = 回不去）。
	if got := RateWindowRefillSeconds(); got != 0 {
		t.Fatalf("refill=0 read back = %d want 0 (legacy sentinel must survive clamp)", got)
	}
	if got := RatePaceViolationThreshold(); got != 2 {
		t.Fatalf("threshold=2 read back = %d want 2", got)
	}
}

func TestRatePolicySpecs_ClampBounds(t *testing.T) {
	cases := []struct {
		refill, threshold string
		wantRefill        int
		wantThreshold     int
	}{
		{"-5", "0", 0, 2},        // 负值收到 Min（refill Min=0，threshold Min=2）
		{"99999", "99", 600, 10}, // 超上限收到 Max
		{"15", "4", 15, 4},       // 界内原样
	}
	for i, c := range cases {
		newRatePolicyTestGlobal(t, c.refill, c.threshold)
		if got := RateWindowRefillSeconds(); got != c.wantRefill {
			t.Errorf("case %d refill(%s) = %d want %d", i, strconv.Quote(c.refill), got, c.wantRefill)
		}
		if got := RatePaceViolationThreshold(); got != c.wantThreshold {
			t.Errorf("case %d threshold(%s) = %d want %d", i, strconv.Quote(c.threshold), got, c.wantThreshold)
		}
	}
}
