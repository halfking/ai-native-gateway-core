// gate_test.go — W3 灰度门与五态分类的判定矩阵（P1-2③，ADR §2）。
package memoraprewarm

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// 1) 未 Set ⇒ 关（fail-closed）。
func TestGateDefaultsClosed(t *testing.T) {
	g := NewGate()
	if g.Allowed("any-tenant") {
		t.Fatal("unset gate must be closed")
	}
}

// 2) enabled=false ⇒ 全关（即使名单命中）。
func TestGateDisabledOverridesAllowlist(t *testing.T) {
	g := NewGate()
	g.Set(false, []string{"acme"})
	if g.Allowed("acme") {
		t.Fatal("disabled gate must deny even allowlisted tenants")
	}
}

// 3) allowlist 空 ⇒ 全开（平台级放量）。
func TestGateEmptyAllowlistIsPlatformWide(t *testing.T) {
	g := NewGate()
	g.Set(true, nil)
	for _, tenant := range []string{"a", "b", ""} {
		if !g.Allowed(tenant) {
			t.Fatalf("empty allowlist must allow tenant %q", tenant)
		}
	}
}

// 4) allowlist 非空 ⇒ 仅名单内放行（灰度）。
func TestGateAllowlistScoping(t *testing.T) {
	g := NewGate()
	g.Set(true, []string{"acme", "beta"})
	if !g.Allowed("acme") || !g.Allowed("beta") {
		t.Fatal("allowlisted tenants must pass")
	}
	if g.Allowed("gamma") {
		t.Fatal("tenant outside allowlist must be denied (control group)")
	}
}

// 5) nil receiver 安全（装配层可不建 Gate）。
func TestGateNilReceiver(t *testing.T) {
	var g *Gate
	if g.Allowed("t") {
		t.Fatal("nil gate must deny")
	}
	g.Set(true, nil) // 不得 panic
}

// 6) Set 是全量覆盖：收窄后的名单立即生效。
func TestGateSetReplaces(t *testing.T) {
	g := NewGate()
	g.Set(true, []string{"acme", "beta"})
	g.Set(true, []string{"beta"})
	if g.Allowed("acme") {
		t.Fatal("Set must replace, not merge")
	}
	if !g.Allowed("beta") {
		t.Fatal("remaining tenant must still pass")
	}
}

// 7) ClassifyOutcome 五态映射。
func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		err        error
		want       string
	}{
		{"timeout err", 0, context.DeadlineExceeded, OutcomeTimeout},
		{"client timeout", 0, errors.New("net/http: request canceled (Client.Timeout exceeded)"), OutcomeTimeout},
		{"conn refused", 0, &net.OpError{Op: "dial", Err: errors.New("connection refused")}, OutcomeUnavailable},
		{"plain err", 0, errors.New("boom"), OutcomeUnavailable},
		{"204 miss", 204, nil, OutcomeMiss},
		{"200 hit", 200, nil, OutcomeHit},
		{"201 hit", 201, nil, OutcomeHit},
		{"500 unavailable", 500, nil, OutcomeUnavailable},
	}
	for _, tc := range cases {
		if got := ClassifyOutcome(tc.statusCode, tc.err); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// 8) RecordPrewarm 冒烟（promauto 注册一次，重复调不 panic）。
func TestRecordPrewarmSmoke(t *testing.T) {
	RecordPrewarm(OutcomeRequested)
	RecordPrewarm(OutcomeHit)
}

// 9) gate 门禁在 Trigger 内生效：门外连 goroutine 都不启动。
func TestTriggerGateClosedNoGoroutine(t *testing.T) {
	c := &fakeClient{ok: true}
	w := &fakeWriter{}
	g := NewGate() // 未 Set ⇒ 关
	Trigger(c, w, g, nil, "tenant", "sess-g", "prompt", time.Hour)
	time.Sleep(80 * time.Millisecond)
	if c.calls.Load() != 0 || w.calls.Load() != 0 {
		t.Fatalf("closed gate must not start prewarm, client=%d writer=%d", c.calls.Load(), w.calls.Load())
	}
}

// 10) 门开 + 名单外租户 ⇒ 同样不启动（对照组保证）。
func TestTriggerGateAllowlistControlGroup(t *testing.T) {
	c := &fakeClient{ok: true}
	w := &fakeWriter{}
	g := NewGate()
	g.Set(true, []string{"acme"})
	Trigger(c, w, g, nil, "gamma", "sess-h", "prompt", time.Hour)
	time.Sleep(80 * time.Millisecond)
	if c.calls.Load() != 0 {
		t.Fatal("control-group tenant must not be prewarmed")
	}
	// 名单内放行。
	Trigger(c, w, g, nil, "acme", "sess-i", "prompt", time.Hour)
	waitFor(t, func() bool { return c.calls.Load() == 1 })
}
