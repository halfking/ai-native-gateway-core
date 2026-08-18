package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// readReason reads the value of the {provider_id, reason} child via the
// default Gatherer. Does NOT Reset() (Reset would clobber the increments
// the test just made) — instead each test takes a baseline snapshot
// before acting and asserts on the delta.
func readReason(t *testing.T, providerID int, reasonName string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "llmgw_credential_reveal_failure_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			var pid, reason string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "provider_id":
					pid = l.GetValue()
				case "reason":
					reason = l.GetValue()
				}
			}
			if pid == fmt.Sprintf("%d", providerID) && reason == reasonName {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestClassifyRevealFailureClosedVocabulary(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unknown_format sentinel", errRevealUnknownFormat, revealFailUnknownFormat},
		{"unknown_format wrapped", fmt.Errorf("wrap: %w", errRevealUnknownFormat), revealFailUnknownFormat},
		{"decrypt sentinel", errRevealDecrypt, revealFailDecryptError},
		{"not_found sentinel", errRevealNotFound, revealFailNotFound},
		{"not_configured sentinel", errRevealNotConfigured, revealFailNotConfigured},
		{"rotation sentinel", errRevealRotation, revealFailRotation},

		// cached unwraps to the wrapped cause so the original incident
		// signature survives amplification. This is the regression guard
		// for the 2026-08-18 incident.
		{"cached unknown_format", fmt.Errorf("%w (x=1): %w", errRevealCached, errRevealUnknownFormat), revealFailUnknownFormat},
		{"cached not_found", fmt.Errorf("%w: %w", errRevealCached, errRevealNotFound), revealFailNotFound},
		{"cached bare", errRevealCached, revealFailCached},

		// text-only errors are NOT promoted to a known bucket. The audit
		// pass specifically removed substring matching so that secret
		// package error refactors cannot silently demote incidents.
		{"text only", errors.New("cannot decrypt: unknown format"), revealFailOther},
		{"nil", nil, revealFailOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classifyRevealFailure(tc.err))
		})
	}
}

func TestRecordCredentialRevealFailureIncrementsPerProviderReason(t *testing.T) {
	// Clear all counters so the test is order-independent against any
	// state left by sibling tests in the package. Reset() zeroes values
	// but keeps the child registered, so we still need the baseline
	// snapshot below in case a future test runs in the same process.
	credentialRevealFailures.Reset()

	base587Unknown := readReason(t, 587, revealFailUnknownFormat)
	base587Cached := readReason(t, 587, revealFailCached)
	base588Other := readReason(t, 588, revealFailOther)

	recordCredentialRevealFailure(587, errRevealUnknownFormat)
	recordCredentialRevealFailure(587, errRevealUnknownFormat)
	// errRevealCached alone (no wrapped cause) classifies as "cached".
	recordCredentialRevealFailure(587, errRevealCached)
	recordCredentialRevealFailure(588, errors.New("db: connection refused"))

	require.Equal(t, base587Unknown+2, readReason(t, 587, revealFailUnknownFormat))
	// The bare errRevealCached entry bumps the cached bucket.
	require.Equal(t, base587Cached+1, readReason(t, 587, revealFailCached))
	require.Equal(t, base588Other+1, readReason(t, 588, revealFailOther))
}

func TestRecordCredentialRevealCachedHitCountsCacheAmplification(t *testing.T) {
	credentialRevealFailures.Reset()

	for i := 0; i < 5; i++ {
		recordCredentialRevealCachedHit(587, revealFailUnknownFormat)
	}
	require.Equal(t, 5.0, readReason(t, 587, revealFailCached))
	// cached-hit path ALSO bumps the underlying cause so root-cause
	// frequency stays accurate across fresh vs. cached. Without this,
	// the 2026-08-18 incident would under-report unknown_format hits by
	// the amplification factor (60+/min on 154 vs. 1 fresh failure).
	require.Equal(t, 5.0, readReason(t, 587, revealFailUnknownFormat))
}

func TestRecordCredentialRevealCachedHitDedupesReasonBucket(t *testing.T) {
	credentialRevealFailures.Reset()
	// If the cached reason is itself "cached" we should not double-count
	// it into both buckets; empty reason is treated the same way.
	recordCredentialRevealCachedHit(587, revealFailCached)
	recordCredentialRevealCachedHit(587, "")
	require.Equal(t, 2.0, readReason(t, 587, revealFailCached))
	require.Equal(t, 0.0, readReason(t, 587, revealFailUnknownFormat))
}

// TestRevealFailureMetric_NoCredentialLabel is the GW-00 cardinality
// guard for this specific metric. Registry.Describe sees the declared
// Desc regardless of whether any WithLabelValues has been called, so the
// assertion runs even on a fresh test binary.
func TestRevealFailureMetric_NoCredentialLabel(t *testing.T) {
	desc := <-describeCh(t, "llmgw_credential_reveal_failure_total")
	require.Contains(t, desc.String(), "fqName: \"llmgw_credential_reveal_failure_total\"")
	str := desc.String()
	require.False(t, strings.Contains(str, "credential_id"),
		"credential_id must not be a label: %s", str)
	require.False(t, strings.Contains(str, "credential_label"),
		"credential_label must not be a label: %s", str)
}

func describeCh(t *testing.T, want string) <-chan *prometheus.Desc {
	t.Helper()
	ch := make(chan *prometheus.Desc, 32)
	go func() {
		defer close(ch)
		ch2 := make(chan *prometheus.Desc, 256)
		go func() {
			prometheus.DefaultRegisterer.(*prometheus.Registry).Describe(ch2)
			close(ch2)
		}()
		for d := range ch2 {
			if strings.Contains(d.String(), "fqName: \""+want+"\"") {
				ch <- d
				return
			}
		}
	}()
	return ch
}
