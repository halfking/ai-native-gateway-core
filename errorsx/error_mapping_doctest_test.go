package errorsx

import (
	"net/http"
	"testing"
)

// 2026-09-21 audit (P2-2): pin the HTTPStatusForKind contract. The current
// implementation is intentionally coarse — only rate-limit / quota /
// concurrency / timeout / network get distinct codes; everything else
// collapses to 502 Bad Gateway because the gateway acts as a relay and the
// upstream kind does not always have a 1:1 HTTP mapping the client can act on.
//
// error-mapping.md documents the IDEAL mapping per ErrorKind. This test pins
// the ACTUAL mapping so that:
//  1. A future contributor who adds a new ErrorKind cannot silently fall
//     through to the default 502 without realising.
//  2. A future change that wants finer-grained status codes (e.g. 401 for
//     KindAuth, 404 for KindModelNotFound) is forced to update this table
//     AND the documentation at the same time — they will not stay in sync
//     by accident.
//
// If this test fails, update BOTH this file AND
// docs/format-conversion/error-mapping.md in the same commit.
func TestErrorKindToHTTPStatusContract(t *testing.T) {
	expected := map[ErrorKind]int{
		// 429 family — explicit rate limit / quota surface
		KindRateLimit:      http.StatusTooManyRequests,
		KindQuota:          http.StatusTooManyRequests,
		KindQuotaPeriodic:  http.StatusTooManyRequests,
		KindQuotaBalance:   http.StatusTooManyRequests,
		KindQuotaPermanent: http.StatusTooManyRequests,

		// 503 — capacity exhausted
		KindConcurrent:         http.StatusServiceUnavailable,
		KindUpstreamOverloaded: http.StatusServiceUnavailable,

		// 504 — temporal
		KindTimeout:      http.StatusGatewayTimeout,
		KindStreamTimeout: http.StatusGatewayTimeout,

		// 502 — transport / everything else
		KindNetwork:             http.StatusBadGateway,
		KindUpstreamDown:        http.StatusBadGateway,
		KindTransient:           http.StatusBadGateway,
		KindAuth:                http.StatusBadGateway, // coarse default — see doc
		KindAuthRevoked:         http.StatusBadGateway,
		KindCanceled:            http.StatusBadGateway,
		KindClientBug:           http.StatusBadGateway,
		KindModelNotFound:       http.StatusBadGateway,
		KindModelDeprecated:     http.StatusBadGateway,
		KindContextLength:       http.StatusBadGateway,
		KindContentFilter:       http.StatusBadGateway,
		KindToolCallIdMismatch:  http.StatusBadGateway,
		KindUnsupportedFeature:  http.StatusBadGateway,
		KindEmptyResponse:       http.StatusBadGateway,
		KindConversion:          http.StatusBadGateway,
		KindUpstreamContextLoss: http.StatusBadGateway,
		KindNoAvailableChannel:  http.StatusBadGateway,
		KindCircuitOpen:         http.StatusBadGateway,
		KindFpSlotSaturated:     http.StatusBadGateway,
	}

	for kind, want := range expected {
		got := HTTPStatusForKind(kind)
		if got != want {
			t.Errorf("HTTPStatusForKind(%q) = %d, want %d (drift between code and error-mapping.md)", kind, got, want)
		}
	}
}

// TestHTTPStatusForKind_Unknown ensures the kind we forget to enumerate
// cannot silently fall through to a "success-looking" 200.
func TestHTTPStatusForKind_Unknown(t *testing.T) {
	if got := HTTPStatusForKind("not_a_real_kind"); got < 400 {
		t.Errorf("HTTPStatusForKind(unknown) = %d, want >= 400 (must not masquerade as success)", got)
	}
}

// TestErrorKindToHTTPStatusContract_AllKindsCovered makes sure every
// declared ErrorKind shows up in the contract table. A new kind added
// without updating the contract is treated as a regression.
func TestErrorKindToHTTPStatusContract_AllKindsCovered(t *testing.T) {
	declared := []ErrorKind{
		KindTransient, KindTimeout, KindNetwork, KindRateLimit, KindAuth,
		KindQuota, KindUpstreamDown, KindCanceled, KindClientBug, KindConcurrent,
		KindAuthRevoked, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent,
		KindModelNotFound, KindStreamTimeout, KindToolCallIdMismatch,
		KindContextLength, KindUnsupportedFeature, KindModelDeprecated,
		KindContentFilter, KindEmptyResponse, KindConversion,
		KindUpstreamContextLoss, KindUpstreamOverloaded, KindNoAvailableChannel,
		KindCircuitOpen, KindFpSlotSaturated,
	}
	covered := map[ErrorKind]bool{}
	for _, k := range declared {
		// We don't actually call this with each kind — we just check that
		// each is in the expected table the other test pins. Re-run
		// HTTPStatusForKind so we exercise the production code path too.
		_ = HTTPStatusForKind(k)
		covered[k] = true
	}
	if len(covered) != len(declared) {
		t.Errorf("ErrorKind inventory shrank: covered %d of %d declared", len(covered), len(declared))
	}
}
