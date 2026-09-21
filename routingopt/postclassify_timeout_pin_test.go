package routingopt

import (
	"os"
	"strings"
	"testing"
)

// R43 (2026-09-18): Options.HookTimeout documents that it bounds all three
// hooks, but PostClassify was the only one not wrapped in hookContext — on
// TTL expiry the first request refreshed the stats snapshot while holding
// c.mu across two DB aggregates, so a slow DB queued every concurrent Decide
// with no timeout bound. This pin keeps the third hook wired; a behavioral
// test would need a real pool (the DAO degrades fail-open, which hides the
// regression instead of surfacing it).
func TestPostClassify_WrappedInHookContext(t *testing.T) {
	src, err := os.ReadFile("real_optimizer.go")
	if err != nil {
		t.Fatalf("read real_optimizer.go: %v", err)
	}
	s := string(src)
	fn := functionBody(s, "func (o *RealOptimizer) PostClassify(")
	if !strings.Contains(fn, "o.opts.hookContext(ctx)") {
		t.Fatal("RealOptimizer.PostClassify no longer applies o.opts.hookContext — " +
			"slow stats refresh can again stall concurrent Decide calls without the documented HookTimeout bound")
	}
}

// functionBody extracts the body of a top-level func declaration up to its
// closing brace (brace-counting from the signature line).
func functionBody(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	depth := 0
	started := false
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return src[i : j+1]
			}
		}
	}
	return src[i:]
}
