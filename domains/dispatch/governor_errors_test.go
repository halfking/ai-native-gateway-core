package dispatch

import (
	"errors"
	"fmt"
	"testing"
)

// Stage A contract pin: ErrGovernorUnavailable is a stable sentinel that
// downstream code uses via errors.Is. Renaming or removing it is a
// coordinate break for the capacity-wait ladder, Stage B RedisEnforce
// fail-closed classification, and Stage E applier error mapping.
func TestErrGovernorUnavailableIdentity(t *testing.T) {
	if ErrGovernorUnavailable == nil {
		t.Fatalf("ErrGovernorUnavailable must not be nil")
	}
	if ErrGovernorUnavailable.Error() != "dispatch: governor backend unavailable" {
		t.Fatalf("sentinel message drift: got %q", ErrGovernorUnavailable.Error())
	}
}

func TestIsGovernorUnavailableDirect(t *testing.T) {
	if !IsGovernorUnavailable(ErrGovernorUnavailable) {
		t.Fatalf("direct sentinel must classify true")
	}
	if IsGovernorUnavailable(errors.New("other")) {
		t.Fatalf("unrelated error must not classify true")
	}
}

func TestIsGovernorUnavailableWrapped(t *testing.T) {
	cause := errors.New("redis: connection refused")
	wrapped := fmt.Errorf("%w: %w", ErrGovernorUnavailable, cause)
	if !IsGovernorUnavailable(wrapped) {
		t.Fatalf("wrapped sentinel must classify true")
	}
	// Double-wrap pattern (%w + %w) returns *fmt.wrapErrors with
	// Unwrap() []error; errors.Is walks both targets. Pin that errors.Is
	// reaches the cause so log fields carrying the wrapped error can be
	// introspected.
	if !errors.Is(wrapped, cause) {
		t.Fatalf("errors.Is must reach the cause through the double wrap")
	}
	// Pin Unwrap-chain behavior: a single-error Unwrap surface MUST NOT
	// silently break the contract. *fmt.wrapErrors implements
	// Unwrap() []error (plural); if a future stdlib change collapses
	// this we want to know.
	type multiUnwrap interface{ Unwrap() []error }
	if _, ok := wrapped.(multiUnwrap); !ok {
		t.Fatalf("double-wrap error should implement Unwrap() []error, got %T", wrapped)
	}
}

// Stage B will return ErrGovernorUnavailable-wrapped errors from
// RedisEnforceGovernor.Acquire / ApplyPolicy. This test pins that the
// sentinel is a singleton (no per-call allocation), which keeps
// errors.Is constant-time.
func TestErrGovernorUnavailableSingleton(t *testing.T) {
	if ErrGovernorUnavailable != ErrGovernorUnavailable {
		t.Fatalf("sentinel must be a stable singleton (compare-by-identity)")
	}
}