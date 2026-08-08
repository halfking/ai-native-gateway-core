package streaming

import (
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// TestOverloadRetryAfterSeconds pins the client-facing Retry-After the
// gateway advertises when every candidate returned an overload-shaped 5xx.
//
// The value has to be usable by a dumb client that does no parsing of its
// own, so two properties matter beyond "prefer the upstream hint": it must
// never be 0 (an immediate hot retry against a still-overloaded relay), and
// it must never be the multi-day figure clampRetryAfter would happily allow
// — that bound exists for DB cooling windows, not for a waiting caller.
func TestOverloadRetryAfterSeconds(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "no upstream error falls back to default",
			err:  errors.New("some opaque failure"),
			want: defaultOverloadRetryAfterSeconds,
		},
		{
			name: "upstream error without a hint falls back to default",
			err:  &upstreampkg.Error{Kind: errorsx.KindUpstreamOverloaded, Message: "overloaded"},
			want: defaultOverloadRetryAfterSeconds,
		},
		{
			name: "upstream hint is honoured",
			err:  &upstreampkg.Error{Kind: errorsx.KindUpstreamOverloaded, RetryAfter: 3 * time.Second},
			want: 3,
		},
		{
			name: "sub-second hint rounds up to 1 rather than down to 0",
			err:  &upstreampkg.Error{Kind: errorsx.KindUpstreamOverloaded, RetryAfter: 200 * time.Millisecond},
			want: 1,
		},
		{
			name: "fractional hint rounds up",
			err:  &upstreampkg.Error{Kind: errorsx.KindUpstreamOverloaded, RetryAfter: 2500 * time.Millisecond},
			want: 3,
		},
		{
			name: "absurd hint is capped at the in-flight ceiling",
			err:  &upstreampkg.Error{Kind: errorsx.KindUpstreamOverloaded, RetryAfter: 31 * 24 * time.Hour},
			want: 10,
		},
		{
			name: "negative hint falls back to default",
			err:  &upstreampkg.Error{Kind: errorsx.KindUpstreamOverloaded, RetryAfter: -5 * time.Second},
			want: defaultOverloadRetryAfterSeconds,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overloadRetryAfterSeconds(tt.err); got != tt.want {
				t.Errorf("overloadRetryAfterSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestStreamErrorKindForDetailCode_OverloadIsDistinct guards the operator-facing
// split. Before 2026-08-08 an overloaded relay and a dead one both surfaced as
// "upstream_error", so no dashboard could tell "provider is busy, it will pass"
// from "provider is down, page someone".
func TestStreamErrorKindForDetailCode_OverloadIsDistinct(t *testing.T) {
	overloaded := streamErrorKindForDetailCode(&StreamOutcome{Kind: errorsx.KindUpstreamOverloaded}, "")
	if overloaded != "upstream_overloaded" {
		t.Errorf("overloaded kind = %q, want %q", overloaded, "upstream_overloaded")
	}
	down := streamErrorKindForDetailCode(&StreamOutcome{Kind: errorsx.KindUpstreamDown}, "")
	if down != "upstream_error" {
		t.Errorf("upstream_down kind = %q, want %q", down, "upstream_error")
	}
	if overloaded == down {
		t.Error("overloaded and down must not share one dashboard bucket")
	}
}

// TestIsRetriableError_UpstreamOverloaded verifies Goal-mode retry still
// covers the new kind. It is retryable at the routing layer, so excluding it
// here would make Goal mode give up on a failure every other layer retries.
func TestIsRetriableError_UpstreamOverloaded(t *testing.T) {
	if !errorsx.IsRetryable(errorsx.KindUpstreamOverloaded) {
		t.Fatal("KindUpstreamOverloaded must be retryable in errorsx")
	}
}
