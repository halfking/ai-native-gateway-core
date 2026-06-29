// Package probeutil — retry.go
//
// Shared retry policy for all credential/model probe paths.
//
// 2026-06-29 audit: a single failed probe must not decide a credential's
// health. Transient network blips, momentary 5xx, or upstream LB hiccups
// would otherwise mark healthy credentials as unhealthy, causing
// `no_candidates` cascade. The previous implementation in
// bg/credential_probe_v2.probeCredential ran step1 (GET /v1/models) and
// step2 (mini chat) exactly once before declaring the credential dead.
//
// probeRetryDelays: 0s, 2s, 5s — three attempts total. The 0s first attempt
// keeps the happy path identical (no extra latency for healthy credentials).
// The 2s + 5s backoff is deliberately short: it is meant to absorb blips,
// not to wait for upstream recovery. Long backoff (10/15/30s) lives in
// bg/model_probe.probeWithRetry and serves a different purpose — waiting
// for an already-broken provider to come back online.
//
// isProbeRetryableStatus classifies HTTP status codes per the 2026-06-29
// decision matrix:
//
//	retryable:    408, 425, 429, 5xx
//	non-retryable: 400, 401, 402, 403, 404, 422  (clear business failure;
//	                retrying will not change the answer)
//
// isProbeCtxCancel distinguishes stop signals (parent context canceled)
// from real network errors so the retry loop does not paper over a
// graceful shutdown or a downstream timeout.
package probeutil

import (
	"context"
	"errors"
	"time"
)

// ProbeRetryDelays is the short-jitter retry schedule for credential/model
// probes. The first entry is 0 (no delay before the first attempt) so the
// happy path is unchanged.
//
// Exported so the admin /check-health entry can share the same schedule
// with the background CredentialProbeV2 cycle.
var ProbeRetryDelays = []time.Duration{0, 2 * time.Second, 5 * time.Second}

// IsProbeRetryableStatus reports whether the given HTTP status code (or 0
// for "no response at all") is worth retrying with the short-jitter
// schedule.
//
// Policy (2026-06-29 audit, operator-approved):
//
//	Fail fast (clear business failure, retrying won't change the answer):
//	    400, 401, 402, 403, 404, 422
//	Retry (transient — network blip, upstream overload, brief gateway hiccup):
//	    0 (no response), 408, 425, 429, 5xx,
//	    AND any other 4xx not in the fail-fast set.
//
// The "any other 4xx" default errs on the side of retrying: a single probe
// must not declare a credential dead (audit mandate). Statuses like 405/409/
// 410/418/428/451 are uncommon enough from a probe endpoint that a 2s+5s
// retry is cheaper than a false-unhealthy cascade. The classifier only
// short-circuits to fail-fast on the six statuses that are unambiguous
// business failures.
func IsProbeRetryableStatus(code int) bool {
	switch code {
	case 0: // no status — network error, DNS failure, timeout, connection refused
		return true
	case 400, 401, 402, 403, 404, 422:
		// Clear business failure: bad request, bad auth, payment required,
		// forbidden, model not found, validation error. Retrying will not
		// change the answer, so fail fast to avoid masking real problems.
		return false
	}
	// Everything else retries: 408/425/429, all 5xx, and any 4xx not in
	// the fail-fast set above. Status codes outside 4xx/5xx (1xx/2xx/3xx/
	// 6xx+) are not probe failure outcomes — a 2xx is consumed as success
	// by the caller before reaching here — so we keep them non-retryable
	// defensively (a future caller bug surfacing 200 here should not loop).
	if code >= 400 && code <= 599 {
		return true
	}
	return false
}

// IsProbeCtxCancel reports whether err indicates the parent context was
// canceled or its deadline was exceeded mid-request. In that case the
// retry loop must abort — retrying would mask a graceful shutdown or
// a downstream timeout (e.g. the 5-min cycle timeout in
// CredentialProbeV2.cycleAll).
func IsProbeCtxCancel(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// SleepWithCtx waits for d, returning early if ctx is canceled. Returns
// true if the wait completed normally, false if the context aborted.
func SleepWithCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		// No-op; still check ctx to give callers a fast exit point.
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
