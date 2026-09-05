package errorsx

import (
	"fmt"
	"testing"
)

// TestRegression_ApigptCreditExhaustionClassification locks in the regression
// fix for the user's report: when an apigpt (third_party_relay, apiclaude.cc
// backend) node runs out of credits and the upstream body contains
// "insufficient credit" (with space), "insufficient credits" (plural),
// "quota exhausted", or "No available accounts", the gateway must classify
// these as KindQuotaPermanent so the credential is permanently ejected and
// the failover dispatcher can switch to a sibling node.
//
// Before the fix, these matched concurrentOverloadRe first (which has
// patterns "insufficient credit", "quota exhausted", "available accounts"
// from the 2026-07-12 commit "classify 'No available accounts' as
// KindConcurrent") and were returned as KindConcurrent → not fatal to the
// credential → same-cred retry kept hitting the dead node → eventual
// attempt-cap exhaustion → 5xx to the client. The fix moves the
// budgetExceededRe check before concurrentOverloadRe and removes the
// conflicting patterns.
func TestRegression_ApigptCreditExhaustionClassification(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ErrorKind
	}{
		{
			name: "apigpt insufficient credit (space)",
			body: `{"error":{"message":"insufficient credit","type":"insufficient_credit"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "apigpt insufficient credits (plural)",
			body: `{"error":{"message":"insufficient credits","type":"insufficient_credit"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "apigpt quota exhausted",
			body: `{"error":{"message":"quota exhausted"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "apigpt No available accounts",
			body: `{"error":{"message":"No available accounts","type":"api_error"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "apigpt account unavailable",
			body: `{"error":{"message":"account unavailable"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "apigpt out of credits (existing covered)",
			body: `{"error":{"message":"You are out of credits"}}`,
			want: KindQuotaPermanent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []int{402, 403, 429} {
				got := ClassifyErrorWithBody(status, []byte(tc.body))
				if got != tc.want {
					t.Errorf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
						status, tc.body, got, tc.want)
				}
			}
		})
	}
}

func TestRegression_ApigptChineseCreditExhaustion(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ErrorKind
	}{
		{
			name: "费用用完",
			body: `{"error":{"message":"费用用完，请充值"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "费用已用完",
			body: `{"error":{"message":"费用已用完，请充值"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "额度用完",
			body: `{"error":{"message":"额度用完，请充值"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "余额用完",
			body: `{"error":{"message":"余额用完，请充值"}}`,
			want: KindQuotaPermanent,
		},
		{
			name: "节点费用已用完（用户原文）",
			body: `{"error":{"message":"节点费用已用完，请充值"}}`,
			want: KindQuotaPermanent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []int{402, 403, 429} {
				got := ClassifyErrorWithBody(status, []byte(tc.body))
				if got != tc.want {
					t.Errorf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
						status, tc.body, got, tc.want)
				}
			}
		})
	}
}

// TestRegression_ApigptOverloadStillConcurrent locks in the fix does NOT
// regress genuine overload signals. "concurrent limit exceeded" is
// overload, not quota — must still be KindConcurrent so the tuner's
// concurrency ratchet applies.
func TestRegression_ApigptOverloadStillConcurrent(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ErrorKind
	}{
		{
			name: "concurrent limit exceeded (real overload)",
			body: `{"error":{"message":"concurrent limit exceeded for this account","type":"rate_limit_error"}}`,
			want: KindConcurrent,
		},
		{
			name: "engine busy (real overload)",
			body: `{"error":{"message":"engine busy, please retry"}}`,
			want: KindConcurrent,
		},
		{
			name: "slow down (real overload)",
			body: `{"error":{"message":"slow down, too many requests"}}`,
			want: KindConcurrent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []int{429, 503} {
				got := ClassifyErrorWithBody(status, []byte(tc.body))
				if got != tc.want {
					t.Errorf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
						status, tc.body, got, tc.want)
				}
			}
		})
	}
}

// TestRegression_ApigptCreditExhaustionWrapped covers the wrapped-error path
// through ClassifyError (the upstream.Error → fmt.Errorf wrapping that the
// executor emits). The fix removes the budgetExceededRe-vs-concurrentOverload
// ordering bug here too, so wrapped error strings carrying "insufficient
// credit" / "quota exhausted" / "no available credits" bodies classify as
// KindQuotaPermanent instead of falling through to KindTransient (which
// would have caused the executor to retry the dead credential).
func TestRegression_ApigptCreditExhaustionWrapped(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{
			name: "wrapped insufficient credit",
			err:  fmt.Errorf(`[upstream] 429: {"error":{"message":"insufficient credit","type":"insufficient_credit"}}`),
			want: KindQuotaPermanent,
		},
		{
			name: "wrapped quota exhausted",
			err:  fmt.Errorf(`[upstream] 429: {"error":{"message":"quota exhausted"}}`),
			want: KindQuotaPermanent,
		},
		{
			name: "wrapped No available accounts",
			err:  fmt.Errorf(`[upstream] 503: {"error":{"message":"No available accounts","type":"api_error"}}`),
			want: KindQuotaPermanent,
		},
		{
			name: "wrapped 费用已用完（用户原文）",
			err:  fmt.Errorf(`[upstream] 429: {"error":{"message":"节点费用已用完，请充值"}}`),
			want: KindQuotaPermanent,
		},
		{
			name: "wrapped out of credits",
			err:  fmt.Errorf(`[upstream] 429: {"error":{"message":"You are out of credits"}}`),
			want: KindQuotaPermanent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.err, nil)
			if got != tc.want {
				t.Errorf("ClassifyError(%q) = %q, want %q",
					tc.err.Error(), got, tc.want)
			}
		})
	}
}

// TestRegression_ApigptCreditExhaustion_AllStatuses exercises the
// classification across every status code that apiclaude.cc / OneAPI
// family relays might return for credit exhaustion. Per status:
//   - 402 (Payment Required) / 403 (Forbidden) / 429 (Too Many Requests):
//     must classify as KindQuotaPermanent (or KindQuotaPeriodic if the
//     body carries a reset timestamp). This is the gate inside
//     ClassifyErrorWithBody; bodies without these statuses fall through
//     to ClassifyResponseStatus which is out of scope for the budget fix.
//   - 200: body-only SSE error-chunk path (ClassifyResponseBody).
//   - 500/503: ambiguity — server error vs quota. Status-based fallback
//     applies; the budgetExhausted check is status-gated to 402/403/429
//     by design, so 5xx bodies with these messages fall through to
//     KindUpstreamDown. This is acceptable: the executor will retry,
//     and the next attempt that lands on 402/403/429 will properly
//     eject the credential.
func TestRegression_ApigptCreditExhaustion_AllStatuses(t *testing.T) {
	body := []byte(`{"error":{"message":"insufficient credit","type":"insufficient_credit"}}`)

	// Statuses where the budget check runs.
	for _, status := range []int{402, 403, 429} {
		got := ClassifyErrorWithBody(status, body)
		if got != KindQuotaPermanent {
			t.Errorf("status=%d body=%q got=%q want=%q",
				status, body, got, KindQuotaPermanent)
		}
	}

	// ClassifyResponseBody applies the same 402/403/429 status gate for
	// the budget check (the SSE error-chunk path through this function
	// does not surface apigpt's status code, so the status gate only
	// matters for non-200 responses that arrive via this path).
	for _, status := range []int{402, 403, 429} {
		got := ClassifyResponseBody(status, body)
		if got != KindQuotaPermanent {
			t.Errorf("ClassifyResponseBody(%d, %q) = %q, want %q",
				status, body, got, KindQuotaPermanent)
		}
	}
}
