package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestRecordLastKind_MostDiagnosticWins pins the 2026-08-09 fix for lastKind
// attribution.
//
// Execute walks every candidate, so a request that fails outright can produce
// several different kinds. lastKind used to be plain last-writer-wins, which
// meant the client was told about whichever candidate happened to be last in
// the list:
//
//	candidates [quota-exhausted, transiently-down]
//	  before → "No available provider" (transient); the real cause, every
//	           credential out of quota, was dropped
//	  after  → quota_permanent, which names the actual operator action
//
// The reverse was equally wrong: one 5xx on the final candidate masked a
// genuine all-credentials-exhausted event.
func TestRecordLastKind_MostDiagnosticWins(t *testing.T) {
	tests := []struct {
		name     string
		current  errorsx.ErrorKind
		incoming errorsx.ErrorKind
		want     errorsx.ErrorKind
	}{
		// The headline case: transient must not overwrite credential-fatal.
		{
			name:     "quota then transient keeps quota",
			current:  errorsx.KindQuotaPermanent,
			incoming: errorsx.KindTransient,
			want:     errorsx.KindQuotaPermanent,
		},
		{
			name:     "transient then quota upgrades to quota",
			current:  errorsx.KindTransient,
			incoming: errorsx.KindQuotaPermanent,
			want:     errorsx.KindQuotaPermanent,
		},
		{
			name:     "auth outranks upstream_down regardless of order",
			current:  errorsx.KindUpstreamDown,
			incoming: errorsx.KindAuth,
			want:     errorsx.KindAuth,
		},
		{
			name:     "overload does not mask auth",
			current:  errorsx.KindAuth,
			incoming: errorsx.KindUpstreamOverloaded,
			want:     errorsx.KindAuth,
		},

		// Binding-level outranks transient but yields to credential-fatal.
		{
			name:     "model_not_found beats transient",
			current:  errorsx.KindTransient,
			incoming: errorsx.KindModelNotFound,
			want:     errorsx.KindModelNotFound,
		},
		{
			name:     "quota beats model_not_found",
			current:  errorsx.KindModelNotFound,
			incoming: errorsx.KindQuotaBalance,
			want:     errorsx.KindQuotaBalance,
		},

		// Client-caused outranks transient: the caller can act on it.
		{
			name:     "context_length beats timeout",
			current:  errorsx.KindTimeout,
			incoming: errorsx.KindContextLength,
			want:     errorsx.KindContextLength,
		},
		{
			name:     "model_not_found beats content_filter",
			current:  errorsx.KindContentFilter,
			incoming: errorsx.KindModelNotFound,
			want:     errorsx.KindModelNotFound,
		},

		// Ties keep the earlier kind so the reported cause is stable across
		// retry rounds.
		{
			name:     "equal rank keeps the first",
			current:  errorsx.KindTimeout,
			incoming: errorsx.KindUpstreamDown,
			want:     errorsx.KindTimeout,
		},
		{
			name:     "two credential-fatal kinds keep the first",
			current:  errorsx.KindQuotaPeriodic,
			incoming: errorsx.KindAuth,
			want:     errorsx.KindQuotaPeriodic,
		},

		// Empty is "no information" in either direction.
		{
			name:     "empty incoming does not clear a known kind",
			current:  errorsx.KindQuotaPermanent,
			incoming: "",
			want:     errorsx.KindQuotaPermanent,
		},
		{
			name:     "first kind is taken even when transient",
			current:  "",
			incoming: errorsx.KindTransient,
			want:     errorsx.KindTransient,
		},
		{
			name:     "both empty stays empty",
			current:  "",
			incoming: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recordLastKind(tt.current, tt.incoming); got != tt.want {
				t.Errorf("recordLastKind(%q, %q) = %q, want %q",
					tt.current, tt.incoming, got, tt.want)
			}
		})
	}
}

// TestRecordLastKind_CandidateWalkOrderIndependence asserts the property that
// motivated the fix: the kind reported for a fully-failed request must not
// depend on the order the router happened to return candidates in.
//
// Feeding the same multiset of failures in both directions must land on the
// same kind. Under the old last-writer-wins behaviour these two walks
// disagreed, which is why the same outage was reported as "quota exhausted" or
// "No available provider" depending on scheduling.
func TestRecordLastKind_CandidateWalkOrderIndependence(t *testing.T) {
	walk := func(kinds ...errorsx.ErrorKind) errorsx.ErrorKind {
		var acc errorsx.ErrorKind
		for _, k := range kinds {
			acc = recordLastKind(acc, k)
		}
		return acc
	}

	forward := walk(
		errorsx.KindTransient,
		errorsx.KindQuotaPermanent,
		errorsx.KindUpstreamDown,
	)
	reverse := walk(
		errorsx.KindUpstreamDown,
		errorsx.KindQuotaPermanent,
		errorsx.KindTransient,
	)

	if forward != reverse {
		t.Errorf("walk order changed the reported kind: forward=%q reverse=%q",
			forward, reverse)
	}
	if forward != errorsx.KindQuotaPermanent {
		t.Errorf("reported kind = %q, want %q — the credential-fatal cause must "+
			"survive a walk that also saw transient failures",
			forward, errorsx.KindQuotaPermanent)
	}
}

// TestKindDiagnosticRank_CredentialFatalOutranksEverything guards the ranking
// itself against drift. It derives the credential-fatal set from
// errorsx.IsCredentialFatal so a newly added quota/auth kind is ranked
// correctly without touching this test.
func TestKindDiagnosticRank_CredentialFatalOutranksEverything(t *testing.T) {
	transient := []errorsx.ErrorKind{
		errorsx.KindTransient,
		errorsx.KindTimeout,
		errorsx.KindNetwork,
		errorsx.KindRateLimit,
		errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded,
		errorsx.KindStreamTimeout,
		errorsx.KindConcurrent,
		errorsx.KindEmptyResponse,
	}
	credentialFatal := []errorsx.ErrorKind{
		errorsx.KindAuth,
		errorsx.KindAuthRevoked,
		errorsx.KindQuota,
		errorsx.KindQuotaPeriodic,
		errorsx.KindQuotaBalance,
		errorsx.KindQuotaPermanent,
	}

	for _, k := range credentialFatal {
		if !errorsx.IsCredentialFatal(k) {
			t.Fatalf("test list is stale: %q is no longer credential-fatal", k)
		}
		for _, tr := range transient {
			if kindDiagnosticRank(k) <= kindDiagnosticRank(tr) {
				t.Errorf("rank(%q)=%d must exceed rank(%q)=%d — a transient "+
					"failure would mask a broken credential",
					k, kindDiagnosticRank(k), tr, kindDiagnosticRank(tr))
			}
		}
	}

	// Transient kinds must all share the floor, otherwise ties between them
	// stop being order-stable.
	for _, tr := range transient {
		if got := kindDiagnosticRank(tr); got != 0 {
			t.Errorf("rank(%q) = %d, want 0", tr, got)
		}
	}
}
