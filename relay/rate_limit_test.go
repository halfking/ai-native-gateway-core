// relay/rate_limit_test.go — cross-endpoint behavioural parity for the
// gateway-layer API key rate limit check.
//
// 2026-06-15: added to lock in the unification across /v1/chat/completions,
// /v1/responses and /v1/messages. Before this test, the three endpoints
// disagreed about what to do with a database row whose rate_limit_rpm was
// NULL: chat would fall back to tierDefault, the other two would skip the
// check entirely.
package relay

import (
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// TestCheckGatewayRateLimit_Parity locks in the three-state behaviour:
//
//	DB=NULL  → tierDefault (e.g. "default" tier = 12 RPM) → enforced
//	DB=0     → explicit unlimited → never blocked
//	DB=N>0   → enforced at N RPM
//
// All three endpoints (chat/responses/messages) now go through the same
// helper, so the table covers every code path a client could hit.
func TestCheckGatewayRateLimit_Parity(t *testing.T) {
	applicant := 6
	explicit0 := 0
	explicit60 := 60

	cases := []struct {
		name        string
		rateRPM     *int
		keyTier     string
		limit       int
		expectBlock bool
	}{
		{"db_null_default_tier_falls_back_to_tierDefault", nil, "default", 12, true},
		{"db_null_applicant_tier_falls_back_to_6", nil, "applicant", 6, true},
		{"db_zero_explicit_unlimited", &explicit0, "default", 0, false},
		{"db_60_enforced", &explicit60, "default", 60, true},
		{"applicant_6_via_db_explicit", &applicant, "default", 6, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ki := &auth.KeyInfo{
				ID:           42,
				RateLimitRPM: tc.rateRPM,
				KeyTier:      tc.keyTier,
			}
			rl := ratelimit.NewSlidingWindowLimiter()

			o := checkGatewayRateLimit(ki, rl)
			if o.Skipped {
				t.Fatalf("expected non-skipped outcome, got %+v", o)
			}
			if o.Limit != tc.limit {
				t.Errorf("outcome.Limit: want %d, got %d", tc.limit, o.Limit)
			}
			if o.Blocked {
				t.Fatalf("first request must not be blocked, got %+v", o)
			}

			if tc.limit > 0 {
				// First call already consumed 1 slot; we have (limit-1) left.
				for i := 0; i < tc.limit-1; i++ {
					o = checkGatewayRateLimit(ki, rl)
					if o.Blocked {
						t.Fatalf("burst within limit: blocked at i=%d (limit=%d)", i, tc.limit)
					}
				}
				// The (limit)th call must be blocked.
				o = checkGatewayRateLimit(ki, rl)
				if !o.Blocked {
					t.Fatalf("expected Blocked=true after %d successful calls, got %+v", tc.limit, o)
				}
			}

			if tc.limit == 0 {
				for i := 0; i < 100; i++ {
					o = checkGatewayRateLimit(ki, rl)
					if o.Blocked {
						t.Fatalf("unlimited key blocked at i=%d: %+v", i, o)
					}
				}
			}
		})
	}
}

// TestCheckGatewayRateLimit_SkippedWhenNoKeyInfo guards the early-return
// branches that callers rely on for unauthenticated paths.
func TestCheckGatewayRateLimit_SkippedWhenNoKeyInfo(t *testing.T) {
	rl := ratelimit.NewSlidingWindowLimiter()
	t.Run("nil_keyInfo", func(t *testing.T) {
		o := checkGatewayRateLimit(nil, rl)
		if !o.Skipped {
			t.Errorf("expected Skipped=true, got %+v", o)
		}
	})
		t.Run("internal_key", func(t *testing.T) {
			ki := &auth.KeyInfo{ID: 1, IsInternal: true, RateLimitRPM: intPtrLocal(1)}
			o := checkGatewayRateLimit(ki, rl)
			if !o.Skipped {
				t.Errorf("expected Skipped=true, got %+v", o)
			}
		})
	t.Run("nil_limiter", func(t *testing.T) {
		ki := &auth.KeyInfo{ID: 1}
		o := checkGatewayRateLimit(ki, nil)
		if !o.Skipped {
			t.Errorf("expected Skipped=true, got %+v", o)
		}
	})
}

// TestWriteRateLimitHeaders covers the header-writing contract:
//   - X-RateLimit-Limit only when Limit > 0
//   - X-RateLimit-Remaining only when Remaining >= 0
//   - X-RateLimit-Reset only when ResetSec > 0
//   - Retry-After only when Blocked
func TestWriteRateLimitHeaders(t *testing.T) {
	cases := []struct {
		name       string
		outcome    rateLimitOutcome
		wantHeader map[string]string
		notWant    []string
	}{
		{
			name:    "unlimited_no_headers",
			outcome: rateLimitOutcome{Limit: 0, Remaining: -1},
			notWant: []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"},
		},
		{
			name:    "allowed_within_window_writes_limit_only",
			outcome: rateLimitOutcome{Limit: 60, Remaining: -1, ResetSec: 0},
			wantHeader: map[string]string{
				"X-RateLimit-Limit": "60",
			},
			notWant: []string{"X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"},
		},
		{
			name:    "blocked_writes_retry_after_and_remaining_zero",
			outcome: rateLimitOutcome{Blocked: true, Limit: 60, Remaining: 0, ResetSec: 60},
			wantHeader: map[string]string{
				"X-RateLimit-Limit":     "60",
				"X-RateLimit-Remaining": "0",
				"Retry-After":           "60",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeRateLimitHeaders(w, tc.outcome)
			for k, v := range tc.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Errorf("header %q: want %q, got %q", k, v, got)
				}
			}
			for _, k := range tc.notWant {
				if got := w.Header().Get(k); got != "" {
					t.Errorf("header %q should not be set, got %q", k, got)
				}
			}
		})
	}
}

func intPtrLocal(n int) *int { return &n }
