package bg

import (
	"testing"
	"time"
)

// TestProbeRoundLogGateFirstHitAllowed —— 同键首条放行。
func TestProbeRoundLogGateFirstHitAllowed(t *testing.T) {
	g := newProbeRoundLogGate(30 * time.Minute)
	now := time.Unix(1794656400, 0)
	allowed, suppressed := g.allow(24, "glm-5.3-flash", ProbeRootCauseNode, now)
	if !allowed || suppressed != 0 {
		t.Fatalf("first hit must be allowed with zero suppressed, got allowed=%v suppressed=%d", allowed, suppressed)
	}
}

// TestProbeRoundLogGateSuppressionWindow —— 窗内压掉计数、窗外放行并冲账。
func TestProbeRoundLogGateSuppressionWindow(t *testing.T) {
	g := newProbeRoundLogGate(30 * time.Minute)
	now := time.Unix(1794656400, 0)

	_, _ = g.allow(24, "glm-5.3-flash", ProbeRootCauseNode, now)
	for i := 1; i <= 4; i++ {
		if allowed, _ := g.allow(24, "glm-5.3-flash", ProbeRootCauseNode, now.Add(time.Duration(i)*5*time.Minute)); allowed {
			t.Fatalf("repeat at +%dmin inside 30min cooldown must be suppressed", i*5)
		}
	}

	allowed, suppressed := g.allow(24, "glm-5.3-flash", ProbeRootCauseNode, now.Add(35*time.Minute))
	if !allowed {
		t.Fatal("hit after cooldown must be allowed")
	}
	if suppressed != 4 {
		t.Fatalf("suppressed = %d, want 4 (then reset)", suppressed)
	}
}

// TestProbeRoundLogGateKeyIsolation —— 换供应商/换模型/换根因都立刻放行：
// 判决变化是新的第一现场，不得被旧键压掉。
func TestProbeRoundLogGateKeyIsolation(t *testing.T) {
	g := newProbeRoundLogGate(30 * time.Minute)
	now := time.Unix(1794656400, 0)

	_, _ = g.allow(24, "glm-5.3-flash", ProbeRootCauseNode, now)
	if allowed, _ := g.allow(25, "glm-5.3-flash", ProbeRootCauseNode, now); !allowed {
		t.Fatal("different provider must not be suppressed")
	}
	if allowed, _ := g.allow(24, "kimi-k3", ProbeRootCauseNode, now); !allowed {
		t.Fatal("different model must not be suppressed")
	}
	if allowed, _ := g.allow(24, "glm-5.3-flash", ProbeRootCauseProtocol, now); !allowed {
		t.Fatal("different root cause must not be suppressed")
	}
}

// TestProbeRoundLogGateNilSafe —— nil 闸 fail-open。
func TestProbeRoundLogGateNilSafe(t *testing.T) {
	var g *probeRoundLogGate
	allowed, suppressed := g.allow(1, "m", ProbeRootCauseNode, time.Now())
	if !allowed || suppressed != 0 {
		t.Fatalf("nil gate must fail open, got allowed=%v suppressed=%d", allowed, suppressed)
	}
}
