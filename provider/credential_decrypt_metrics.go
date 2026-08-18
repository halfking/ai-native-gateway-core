package provider

import (
	"errors"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// 2026-08-18 credential-17 incident (154): a raw-Fernet-binary
// secret_ciphertext made every RevealAPIKey call fail with
// "cannot decrypt: unknown format" while the only signal was WARN logs.
// This counter promotes that failure mode to a first-class Prometheus
// series so alerting can catch it before routing degrades.
//
// Label policy (GW-00, metrics/label_cardinality_guard_test.go):
//   - credential_id is FORBIDDEN as a label (per-credential cardinality);
//     identify the credential via the WARN log line instead.
//   - reason is a closed vocabulary — never raw error strings.
//
// Naming: this counts RevealAPIKey failures across the whole key-reveal
// path (DB lookup, keyring, decryption), not just DecryptAny outcomes.
// Use the reason label to slice by failure mode.
var (
	credentialRevealMetricsOnce sync.Once
	credentialRevealFailures    *prometheus.CounterVec
)

// Closed reason vocabulary for credential_reveal_failure_total.
//
//   - unknown_format : DecryptAny returned "cannot decrypt: unknown format";
//                      includes the 2026-08-18 incident signature.
//   - decrypt_error  : other decryption failure (AES-GCM auth fail, Fernet
//                      signature, etc.).
//   - cached         : reveal hit the negative cache from an earlier fresh
//                      failure within decryptFailureCacheTTL. Recorded once
//                      per cache lookup to make cache amplification visible.
//   - not_found      : credential row missing or disabled.
//   - not_configured : credential reveal not configured (no DB / keyring /
//                      fernet key).
//   - rotation       : cache generation invalidated reveal.
//   - other          : unclassified error (DB connectivity, etc.).
const (
	revealFailUnknownFormat = "unknown_format"
	revealFailDecryptError  = "decrypt_error"
	revealFailCached        = "cached"
	revealFailNotFound      = "not_found"
	revealFailNotConfigured = "not_configured"
	revealFailRotation      = "rotation"
	revealFailOther         = "other"
)

// Reveal-path sentinel aliases. The canonical definitions live in the
// secret package as ErrReveal*; these lowercase aliases let the
// classifyRevealFailure switch stay compact without rewriting every test
// reference. New call sites should prefer the exported secret.ErrReveal*
// symbols directly.
var (
	errRevealUnknownFormat = secret.ErrRevealUnknownFormat
	errRevealDecrypt       = secret.ErrRevealDecrypt
	errRevealNotFound      = secret.ErrRevealNotFound
	errRevealNotConfigured = secret.ErrRevealNotConfigured
	errRevealRotation      = secret.ErrRevealRotation
	errRevealCached        = secret.ErrRevealCached
)

func registerCredentialRevealMetrics() {
	credentialRevealMetricsOnce.Do(func() {
		credentialRevealFailures = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_credential_reveal_failure_total",
				Help: "Credential API-key reveal failures. " +
					"reason: unknown_format|decrypt_error|cached|not_found|not_configured|rotation|other. " +
					"Absence means no reveal failures in the sample window.",
			},
			[]string{"provider_id", "reason"},
		)
		prometheus.MustRegister(credentialRevealFailures)
	})
}

func init() { registerCredentialRevealMetrics() }

// classifyRevealFailure maps a reveal error onto the closed reason
// vocabulary. We resolve against package-local sentinels (errReveal*)
// rather than error-string matching so a future error-text refactor
// cannot silently demote incidents to "other".
//
// Cause precedence: cached < cause. A cached unknown-format error still
// reports the original incident signature, because classifyRevealFailure
// unwraps errRevealCached and recurses. Callers pass fresh errors
// directly; cached wrappers arrive already wrapped.
func classifyRevealFailure(err error) string {
	if err == nil {
		return revealFailOther
	}
	switch {
	case errors.Is(err, errRevealUnknownFormat):
		return revealFailUnknownFormat
	case errors.Is(err, errRevealNotConfigured):
		return revealFailNotConfigured
	case errors.Is(err, errRevealNotFound):
		return revealFailNotFound
	case errors.Is(err, errRevealRotation):
		return revealFailRotation
	case errors.Is(err, errRevealCached):
		if cause := errors.Unwrap(err); cause != nil && cause != err {
			return classifyRevealFailure(cause)
		}
		return revealFailCached
	case errors.Is(err, errRevealDecrypt):
		return revealFailDecryptError
	default:
		return revealFailOther
	}
}

// recordCredentialRevealFailure increments the counter once, with the
// caller-supplied providerID and a closed-vocabulary reason. Use this
// for fresh (uncached) reveal failures.
func recordCredentialRevealFailure(providerID int, err error) {
	credentialRevealFailures.
		WithLabelValues(strconv.Itoa(providerID), classifyRevealFailure(err)).
		Inc()
}

// recordCredentialRevealCachedHit increments the "cached" bucket once
// per cache lookup that returns a previously cached failure, AND the
// original-cause bucket if causeReason is non-empty and distinct from
// "cached". This keeps two independent time series in sync: the cached
// bucket exposes cache amplification (hits/min for broken credentials),
// while the cause bucket tracks root-cause frequency independent of
// whether the failure is fresh or cached.
//
// A "cached" causeReason is folded only into the cached bucket (not
// double-counted); an empty causeReason (defensive default from older
// cache entries) does the same.
func recordCredentialRevealCachedHit(providerID int, causeReason string) {
	pid := strconv.Itoa(providerID)
	credentialRevealFailures.WithLabelValues(pid, revealFailCached).Inc()
	if causeReason != "" && causeReason != revealFailCached {
		credentialRevealFailures.WithLabelValues(pid, causeReason).Inc()
	}
}
