package bg

// credential_probe_v2_retry_test.go — unit tests for the probe-retry
// classifier (shouldRetryChatErrMsg + extractStatusCode).
//
// 2026-06-29 audit: a single failed probe must not declare a credential
// dead. The retry policy classifies chat errMsg strings into retryable
// vs fail-fast. These tests lock the policy so a future refactor cannot
// regress it.

import (
	"strconv"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
)

func TestShouldRetryChatErrMsg_FailFast(t *testing.T) {
	// Clear business failures — must fail fast (return false). Cases mirror
	// the EXACT errMsg strings produced by miniChat / miniAnthropic.
	cases := []string{
		// miniChat / miniAnthropic 401/403 special-case prefix:
		"401/403: invalid api key",
		"401/403: {\"error\":{\"message\":\"Incorrect API key\"}}",
		// miniChat 402 special-case (NO "chat status" prefix):
		"402 payment required",
		// miniChat 404 endpoint_id_required (Volcano Ark config issue):
		"endpoint_id_required: chat status 404: ...",
		// miniChat / miniAnthropic 400/404/422 fall-through (chat status NNN):
		"chat status 400: bad request",
		"chat status 404: model not found",
		"chat status 422: validation error",
		"messages status 400: bad request",
		"messages status 404: not found",
		"messages status 422: validation error",
	}
	for _, c := range cases {
		if shouldRetryChatErrMsg(c) {
			t.Errorf("expected fail-fast (no retry) for: %q", c)
		}
	}
}

func TestShouldRetryChatErrMsg_Retryable(t *testing.T) {
	// Transient — must retry (return true). Cases below mirror the EXACT
	// errMsg strings produced by miniChat / miniAnthropic (see
	// credential_probe_v2.go lines 478-551), not made-up formats, so the
	// test guards the real production paths.
	cases := []string{
		// miniChat / miniAnthropic 5xx fall-through:
		"chat status 500: internal server error",
		"chat status 502: bad gateway",
		"chat status 503: service unavailable",
		"chat status 504: gateway timeout",
		"messages status 502: bad gateway",
		// miniChat / miniAnthropic 408/425 fall-through (chat status NNN):
		"chat status 408: request timeout",
		"chat status 425: too early",
		// miniChat / miniAnthropic special-case 429 (NO "chat status" prefix):
		"429 rate limited",
		"messages status 429: too many requests",
		// miniChat / miniAnthropic network error fall-through:
		"chat unreachable: dial tcp: connection refused",
		"chat unreachable: context deadline exceeded",
		"messages unreachable: dial tcp: connection refused",
		// miniChat / miniAnthropic build-request error:
		"build chat request: some weird error",
		"build messages request: some weird error",
	}
	for _, c := range cases {
		if !shouldRetryChatErrMsg(c) {
			t.Errorf("expected retry for: %q", c)
		}
	}
}

func TestShouldRetryChatErrMsg_Empty(t *testing.T) {
	// Empty errMsg after a failed chat is unusual; the policy is to
	// retry rather than silently declare success.
	if !shouldRetryChatErrMsg("") {
		t.Error("empty errMsg: should be retryable (defensive — don't silently succeed)")
	}
}

func TestExtractStatusCode(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"chat status 503: ...", 503, true},
		{"chat status 429: rate limited", 429, true},
		{"messages status 502: bad gateway", 502, true},
		{"messages status 408: timeout", 408, true},
		// Single-digit "status 0" is not a parseable HTTP status; rejected.
		{"chat status 0: zero", 0, false},
		{"no status here", 0, false},
		{"status 12: too short", 0, false},
		{"status abc: not digits", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := extractStatusCode(c.in)
		if ok != c.ok {
			t.Errorf("extractStatusCode(%q) ok: got %v want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("extractStatusCode(%q) code: got %d want %d", c.in, got, c.want)
		}
	}
}

// Round-trip: shouldRetryChatErrMsg + IsProbeRetryableStatus agree.
// A status that is retryable per the shared classifier should also be
// retryable through the errMsg path; a status that is non-retryable
// should also be fail-fast. This test guards against the two
// classifiers drifting out of sync.
func TestShouldRetryChatErrMsg_AgreesWithClassifier(t *testing.T) {
	statuses := []int{
		200, 204, 301, 400, 401, 402, 403, 404, 408, 418, 422, 425, 429,
		500, 502, 503, 504, 599,
	}
	for _, code := range statuses {
		errMsg := "chat status " + strconv.Itoa(code) + ": sample"
		got := shouldRetryChatErrMsg(errMsg)
		want := probeutil.IsProbeRetryableStatus(code)
		if got != want {
			t.Errorf("status %d: shouldRetryChatErrMsg=%v, classifier=%v (must agree)",
				code, got, want)
		}
	}
}
