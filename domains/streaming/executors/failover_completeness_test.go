package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestFailoverKindCoverage_EveryRetryableKindIsHandled documents which error
// kinds the candidate loop hands off on, and how.
//
// A failed candidate advances to the next one through one of three routes:
//
//	explicit continue @ IsCredentialFatal   — auth / quota
//	explicit continue @ isTransientFailoverKind — transient upstream
//	falling off the end of the loop body    — everything else
//
// All three reach the next candidate, which is why every IsRetryable kind fails
// over today even though two of them (KindNetwork, KindConcurrent) are in
// neither predicate. This test records that fact so the gap is visible rather
// than accidental, and so a future edit that makes the explicit branches
// load-bearing (see TestCandidateLoopFailsOverForEveryRetryableKind) has a list
// to work from.
func TestFailoverKindCoverage_EveryRetryableKindIsHandled(t *testing.T) {
	retryable := []errorsx.ErrorKind{
		errorsx.KindTransient,
		errorsx.KindTimeout,
		errorsx.KindNetwork,
		errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded,
		errorsx.KindConcurrent,
		errorsx.KindStreamTimeout,
	}

	// Kinds that reach the next candidate only by falling off the end of the
	// loop body. Keeping them enumerated makes the implicit path auditable.
	implicitOnly := map[errorsx.ErrorKind]bool{
		errorsx.KindNetwork:    true,
		errorsx.KindConcurrent: true,
	}

	for _, kind := range retryable {
		t.Run(string(kind), func(t *testing.T) {
			if !errorsx.IsRetryable(kind) {
				t.Fatalf("test list is stale: %q is no longer IsRetryable", kind)
			}
			explicit := isTransientFailoverKind(kind) || errorsx.IsCredentialFatal(kind)
			switch {
			case explicit && implicitOnly[kind]:
				t.Errorf("%q is now handled explicitly — remove it from "+
					"implicitOnly so the list stays truthful", kind)
			case !explicit && !implicitOnly[kind]:
				t.Errorf("%q is IsRetryable but reaches the next candidate only "+
					"by falling off the end of the loop body. That works today, "+
					"but it is undocumented: either add it to "+
					"isTransientFailoverKind or to implicitOnly with a reason.",
					kind)
			}
		})
	}
}
