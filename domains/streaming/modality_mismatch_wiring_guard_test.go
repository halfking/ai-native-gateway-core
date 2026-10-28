package streaming

import (
	"os"
	"strings"
	"testing"
)

// TestModalityMismatchWiringGuard pins that all THREE protocol entry points
// actually call the diagnosis before falling back to their generic
// no_candidate response.
//
// ★ Why a source guard (and not a behavioural test):
//
//	Each entry point's zero-candidate branch lives inside a handler that
//	needs an executor, a DB pool and a dozen wired dependencies to reach —
//	serveWithExecutor panics on a bare ChatHandler (measured; see
//	modality_mismatch_wiring_test.go's history). The call SITES are exactly
//	what regressed in this feature's own history: the first version wired
//	one protocol and production verification caught the other two, while
//	every test that called diagnoseModalityMismatch directly stayed green.
//
//	This follows the repo's established wiring-guard pattern
//	(cmd/gateway/auto_route_wiring_guard_test.go): assert the call exists,
//	assert it sits BEFORE the fallback response, and fail loudly with the
//	context a future refactor needs.
//
//	The guard is deliberately presence+order, not semantics: it cannot prove
//	the branch conditions are right, only that deleting or reordering the
//	diagnosis relative to the fallback cannot pass silently.
//
//	★ 2026-10-28 补位（批判式审计遗留 2）：纯语义变异（调用仍在原位、
//	守卫恒假 / 调用挪进不可达分支）这个文本守卫看不见——实测把守卫改成
//	`mismatch != nil && false` 后本守卫仍绿。控制流可达性由
//	modality_mismatch_behavioural_wiring_test.go 的行为通道负责：它走各协议
//	的真实入口管线（serveWithExecutor / serveHTTPInner），那条变异下当场红
//	（status=503）。两个守卫同族不同牙，缺一不可。
func TestModalityMismatchWiringGuard(t *testing.T) {
	type site struct {
		file           string
		callNeedle     string // the diagnosis call that must be present
		fallbackNeedle string // the generic no_candidate response after it
	}

	sites := []site{
		{
			file:           "handler.go",
			callNeedle:     "h.diagnoseModalityMismatch(",
			fallbackNeedle: "writeNoCandidateWithAlternatives(",
		},
		{
			file:           "messages.go",
			callNeedle:     "h.chatHandler.diagnoseModalityMismatch(",
			fallbackNeedle: `writeAnthropicError(w, http.StatusServiceUnavailable, "overloaded_error"`,
		},
		{
			file:           "responses.go",
			callNeedle:     "h.chatHandler.diagnoseModalityMismatch(",
			fallbackNeedle: `writeResponsesError(w, http.StatusServiceUnavailable`,
		},
	}

	for _, s := range sites {
		raw, err := os.ReadFile(s.file)
		if err != nil {
			t.Fatalf("%s unreadable: %v", s.file, err)
		}
		text := string(raw)

		callIdx := strings.Index(text, s.callNeedle)
		if callIdx < 0 {
			t.Fatalf("%s: modality diagnosis call %q missing — the zero-candidate exit "+
				"has silently reverted to the generic 503 for this protocol "+
				"(2026-10-28: the Anthropic path shipped exactly this regression and "+
				"production verification caught it, not the tests)",
				s.file, s.callNeedle)
		}

		// ★ Search for the fallback AFTER the diagnosis call, not from the top
		// of the file: these handler files contain EARLIER unrelated
		// overloaded/no_candidate responses (session-store errors, other
		// exits). Matching the first occurrence from the top made this guard
		// report a correct ordering as inverted — caught by running the
		// guard against the real files, which is the only reason this
		// comment exists.
		fallbackIdx := strings.Index(text[callIdx:], s.fallbackNeedle)
		if fallbackIdx < 0 {
			t.Fatalf("%s: fallback marker %q not found after the diagnosis call — the generic "+
				"no_candidate response moved; re-evaluate this guard against the current shape",
				s.file, s.fallbackNeedle)
		}
	}
}

// TestModalityMismatchGuardSeesThreeFiles is the guard's own tripwire: the
// file list above must cover every protocol entry point that has its own
// zero-candidate branch. A new protocol handler added without a diagnosis
// wiring should be added to the list — or better, share the branch.
//
// ★ This exists because a presence check over a hardcoded list silently
// loses its value the moment the list goes stale.
func TestModalityMismatchGuardSeesThreeFiles(t *testing.T) {
	// Every handler file with its own zero-candidate exit must appear in
	// the guard's site list. resolveCandidatesForRequest call sites are the
	// reliable marker: whoever resolves candidates has to handle zero.
	wantFiles := []string{"handler.go", "messages.go", "responses.go"}
	for _, wf := range wantFiles {
		raw, err := os.ReadFile(wf)
		if err != nil {
			t.Fatalf("%s unreadable: %v", wf, err)
		}
		if !strings.Contains(string(raw), "resolveCandidatesForRequest") {
			t.Fatalf("%s no longer resolves candidates itself — if a new file took "+
				"over that responsibility, the wiring guard's file list is stale",
				wf)
		}
	}
}
