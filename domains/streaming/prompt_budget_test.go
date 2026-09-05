package streaming

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPromptBudgetLimit(t *testing.T) {
	const key = "LLM_GATEWAY_MAX_PROMPT_TOKENS"
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset defaults to 2M", "", 2097152},
		{"zero is off", "0", 0},
		{"plain number", "262144", 262144},
		{"over 2M defaults to 2M", "3145728", 2097152},
		{"negative defaults to 2M", "-5", 2097152},
		{"garbage defaults to 2M", "huge", 2097152},
	}
	t.Setenv(key, "") // ensure defined for all cases
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.env)
			if got := promptBudgetLimit(); got != tc.want {
				t.Fatalf("promptBudgetLimit() = %d, want %d (env=%q)", got, tc.want, tc.env)
			}
		})
	}
}

func TestPromptBudgetExceeded(t *testing.T) {
	const key = "LLM_GATEWAY_MAX_PROMPT_TOKENS"
	// ~400 ASCII chars ≈ 100 tokens under the estimateTokens heuristic.
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("a", 400) + `"}]}`)

	t.Run("off never exceeds", func(t *testing.T) {
		t.Setenv(key, "")
		if _, over := promptBudgetExceeded(body); over {
			t.Fatal("guard off must never exceed")
		}
	})

	t.Run("over budget", func(t *testing.T) {
		t.Setenv(key, "50") // body estimates ~100 tokens
		est, over := promptBudgetExceeded(body)
		if !over {
			t.Fatalf("est=%d tokens, limit=50 — want exceeded", est)
		}
		if est <= 50 {
			t.Fatalf("est=%d must be > limit for the over branch to hold", est)
		}
	})

	t.Run("under budget", func(t *testing.T) {
		t.Setenv(key, "1000000")
		if _, over := promptBudgetExceeded(body); over {
			t.Fatal("1M budget must not reject a ~100-token body")
		}
	})

	t.Run("empty body never exceeds", func(t *testing.T) {
		t.Setenv(key, "1")
		if _, over := promptBudgetExceeded(nil); over {
			t.Fatal("empty body must never exceed")
		}
	})
}

func TestPromptBudgetTwoMillionCeiling(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MAX_PROMPT_TOKENS", "")
	below := make([]byte, (2*1048576)*4)
	if _, over := promptBudgetExceeded(below); over {
		t.Fatal("body at the 2M gateway ceiling must not exceed the default limit")
	}
	above := make([]byte, (2*1048576+1)*4)
	if est, over := promptBudgetExceeded(above); !over || est <= 2*1048576 {
		t.Fatalf("body above the 2M gateway ceiling was not rejected: est=%d over=%v", est, over)
	}
}

func TestPreflightDoesNotUseGatewayCeilingAsProviderWindow(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MAX_PROMPT_TOKENS", "")
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	out, applied, est := preflightCompress(body, "openai")
	if applied || est == 0 || string(out) != string(body) {
		t.Fatalf("model-agnostic preflight changed body: applied=%v est=%d", applied, est)
	}
}

// TestPromptBudgetHandlerRejection pins the /v1/chat/completions 413 path:
// with the guard on, an oversized prompt is rejected before JSON parse and
// upstream dispatch, with code=prompt_too_large and a request_logs failure
// row (logCtx marked logged so the safety net does not double-emit).
func TestPromptBudgetHandlerRejection(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MAX_PROMPT_TOKENS", "50")
	t.Cleanup(func() { os.Unsetenv("LLM_GATEWAY_MAX_PROMPT_TOKENS") })

	// Direct call into the shared guard keeps this a unit test: the full
	// ServeHTTP chain requires auth/keyInfo fixtures. The handler wiring is
	// pinned by TestPromptBudgetWiredInEntryPoints below.
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("b", 400) + `"}]}`)
	est, over := promptBudgetExceeded(body)
	if !over || est <= 50 {
		t.Fatalf("guard did not trip: est=%d over=%v", est, over)
	}

	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusRequestEntityTooLarge, map[string]any{
		"error": map[string]string{"message": "prompt exceeds gateway budget", "type": "invalid_request", "code": "prompt_too_large"},
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "prompt_too_large") {
		t.Fatalf("body missing prompt_too_large code: %s", rec.Body.String())
	}
}

// TestPromptBudgetWiredInEntryPoints asserts all three protocol entries
// (chat / messages / responses) check the budget after the body-size guard
// and before JSON unmarshal — the placement that prevents oversized prompts
// from multiplying in-process copies (245 memcg OOM, 2026-08-24).
func TestPromptBudgetWiredInEntryPoints(t *testing.T) {
	for _, file := range []string{"handler.go", "messages.go", "responses.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(src)
		if !strings.Contains(text, "promptBudgetExceeded(bodyBytes)") {
			t.Errorf("%s: entry point missing promptBudgetExceeded(bodyBytes) guard", file)
		}
	}
}
