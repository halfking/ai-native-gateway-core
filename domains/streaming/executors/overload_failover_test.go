package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestIsTransientFailoverKind_UpstreamOverloadedFailsOver guards the most
// dangerous regression available when adding an upstream-failure kind.
//
// The candidate walk hands off to the next credential only for the kinds this
// predicate accepts. Streaming requests never enter the sync retry loop that
// rescues non-streaming callers, so for them this handoff is the ONLY failover
// path: a retryable kind missing here turns an overload-shaped 502 into
// all_candidates_failed and the client sees a hard 503 even though a healthy
// sibling credential was sitting idle.
//
// KindUpstreamOverloaded is deliberately NOT modelled on KindConcurrent, which
// is absent from this list precisely because a genuine concurrency signal is
// meant to stop the walk and let the concurrency tuner react.
func TestIsTransientFailoverKind_UpstreamOverloadedFailsOver(t *testing.T) {
	tests := []struct {
		name string
		kind errorsx.ErrorKind
		want bool
	}{
		// The new kind must behave exactly like upstream_down here.
		{"upstream_overloaded_fails_over", errorsx.KindUpstreamOverloaded, true},
		{"upstream_down_fails_over", errorsx.KindUpstreamDown, true},
		{"transient_fails_over", errorsx.KindTransient, true},
		{"rate_limit_fails_over", errorsx.KindRateLimit, true},
		{"timeout_fails_over", errorsx.KindTimeout, true},
		{"stream_timeout_fails_over", errorsx.KindStreamTimeout, true},
		{"empty_response_fails_over", errorsx.KindEmptyResponse, true},

		// Boundary: concurrent stays out. If a future change adds it here,
		// that is a deliberate behaviour change and this row should fail.
		{"concurrent_stays_out", errorsx.KindConcurrent, false},

		// Credential-fatal and client-side kinds have their own branches
		// upstream of this predicate and must not be swept in.
		{"auth_not_here", errorsx.KindAuth, false},
		{"quota_permanent_not_here", errorsx.KindQuotaPermanent, false},
		{"model_deprecated_not_here", errorsx.KindModelDeprecated, false},
		{"content_filter_not_here", errorsx.KindContentFilter, false},
		{"canceled_not_here", errorsx.KindCanceled, false},
		{"empty_kind_not_here", errorsx.ErrorKind(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientFailoverKind(tt.kind); got != tt.want {
				t.Errorf("isTransientFailoverKind(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

// TestIsTransientRouteNodeFailure_UpstreamOverloaded pins that an overload
// does not get persisted as a route-node failure. A node failure record
// outlives the request and steers future routing away from the node; a relay
// that was briefly busy has not earned that, and KindUpstreamDown is already
// treated the same way.
func TestIsTransientRouteNodeFailure_UpstreamOverloaded(t *testing.T) {
	tests := []struct {
		name string
		kind errorsx.ErrorKind
		want bool
	}{
		{"upstream_overloaded_is_transient", errorsx.KindUpstreamOverloaded, true},
		{"upstream_down_is_transient", errorsx.KindUpstreamDown, true},
		{"timeout_is_transient", errorsx.KindTimeout, true},
		{"network_is_transient", errorsx.KindNetwork, true},
		{"canceled_is_transient", errorsx.KindCanceled, true},

		// Real credential faults must still be recorded against the node.
		{"auth_is_recorded", errorsx.KindAuth, false},
		{"quota_permanent_is_recorded", errorsx.KindQuotaPermanent, false},
		{"empty_kind_is_recorded", errorsx.ErrorKind(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientRouteNodeFailure(tt.kind); got != tt.want {
				t.Errorf("isTransientRouteNodeFailure(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
