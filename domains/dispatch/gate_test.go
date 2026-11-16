package dispatch

import "testing"

// TestModelChangeEnabled_DefaultOn pins the package-level default introduced
// on 2026-11-16 (auto 模式可用性轮): the model-change ladder is ON by
// default.
//
// Why: auto mode (model="auto") used to hand every upstream failure straight
// back to the client — 211 showed 13 failed auto requests in 30 days with
// fallback_used=false on every row, 7 of them on one bad credential, which
// the session then kept re-picking ("原来的模型不可用"). A resilient auto mode
// must switch models by default; operators can still turn it off live via
// the dispatch_v2.allow_model_change platform setting.
func TestModelChangeEnabled_DefaultOn(t *testing.T) {
	previous := IsModelChangeEnabled()
	t.Cleanup(func() { SetModelChangeEnabled(previous) })

	// The init() default is what production boots with.
	if !previous {
		t.Fatal("model-change gate must default to enabled (auto requests need model-level failover)")
	}
}
