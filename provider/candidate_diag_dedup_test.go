package provider

import (
	"testing"
	"time"
)

// TestCandidateDiagLogGateFirstHitAllowed —— 同键首条放行（首条 WARN 不丢）。
func TestCandidateDiagLogGateFirstHitAllowed(t *testing.T) {
	g := newCandidateDiagLogGate(5 * time.Minute)
	now := time.Unix(1794656400, 0)
	allowed, suppressed := g.allow("db_empty", "gemini-2.5-flash", "default", now)
	if !allowed || suppressed != 0 {
		t.Fatalf("first hit must be allowed with zero suppressed, got allowed=%v suppressed=%d", allowed, suppressed)
	}
}

// TestCandidateDiagLogGateSuppressionWindow —— 冷却窗内重复压掉并计数，
// 窗口过后的下一条放行并携带被压掉的条数（计数清零）。
func TestCandidateDiagLogGateSuppressionWindow(t *testing.T) {
	g := newCandidateDiagLogGate(5 * time.Minute)
	now := time.Unix(1794656400, 0)

	if _, ok := g.entries["db_empty|m|t"]; ok {
		t.Fatal("fresh gate must not contain the key")
	}
	_, _ = g.allow("db_empty", "m", "t", now)

	for i := 1; i <= 3; i++ {
		allowed, _ := g.allow("db_empty", "m", "t", now.Add(time.Duration(i)*time.Minute))
		if allowed {
			t.Fatalf("repeat hit at +%dmin inside cooldown must be suppressed", i)
		}
	}

	allowed, suppressed := g.allow("db_empty", "m", "t", now.Add(6*time.Minute))
	if !allowed {
		t.Fatal("hit after cooldown must be allowed")
	}
	if suppressed != 3 {
		t.Fatalf("suppressed count = %d, want 3 (then reset)", suppressed)
	}

	allowed, suppressed = g.allow("db_empty", "m", "t", now.Add(12*time.Minute))
	if !allowed || suppressed != 0 {
		t.Fatalf("counter must reset after flush (next window starts at flush), got allowed=%v suppressed=%d", allowed, suppressed)
	}
}

// TestCandidateDiagLogGateKeyIsolation —— 不同 model / tenant 互不影响
// （候选 SQL 按租户限定 ⇒ 归因可能不同 ⇒ 分键）。
func TestCandidateDiagLogGateKeyIsolation(t *testing.T) {
	g := newCandidateDiagLogGate(5 * time.Minute)
	now := time.Unix(1794656400, 0)

	_, _ = g.allow("db_empty", "m1", "tenant-a", now)
	if allowed, _ := g.allow("db_empty", "m1", "tenant-b", now); !allowed {
		t.Fatal("different tenant must not be suppressed by another tenant's entry")
	}
	if allowed, _ := g.allow("db_empty", "m2", "tenant-a", now); !allowed {
		t.Fatal("different model must not be suppressed by another model's entry")
	}
	if allowed, _ := g.allow("enrich_empty", "m1", "tenant-a", now); !allowed {
		t.Fatal("different event must not share the db_empty bucket")
	}
}

// TestCandidateDiagLogGateNilSafe —— nil 闸 fail-open（放行），与调用方
// 的旧路径行为逐字一致。
func TestCandidateDiagLogGateNilSafe(t *testing.T) {
	var g *candidateDiagLogGate
	allowed, suppressed := g.allow("db_empty", "m", "t", time.Now())
	if !allowed || suppressed != 0 {
		t.Fatalf("nil gate must fail open, got allowed=%v suppressed=%d", allowed, suppressed)
	}
}

// TestDiagArgsKeyFields —— 键字段提取：命中 / 缺失 / 非字符串值三类形态。
func TestDiagArgsKeyFields(t *testing.T) {
	model, tenant := diagArgsKeyFields([]any{"model", "kimi-k3", "tenant_id", "default"})
	if model != "kimi-k3" || tenant != "default" {
		t.Fatalf("got model=%q tenant=%q", model, tenant)
	}
	model, tenant = diagArgsKeyFields([]any{"plan_count", 0})
	if model != "" || tenant != "" {
		t.Fatalf("missing keys must yield empty strings, got model=%q tenant=%q", model, tenant)
	}
	model, tenant = diagArgsKeyFields([]any{"model", 123})
	if model != "" {
		t.Fatalf("non-string model value must yield empty string, got %q", model)
	}
}
