// Package credential — writer_probe_closeout_test.go
//
// Pins the probe-recovery closeout changes in writer.go (2026-09-13):
//   - P6: soft-degrade writes (network/timeout/transient/overloaded) must
//     carry the suspended/auth_failed guard so they can no longer clobber
//     the suspension evidence of a stuck credential;
//   - P7: an upstream Retry-After is clamped to a 24h maximum;
//   - P2 companion: RestoreOnSuccess resets the probe backoff ladder.
package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/pashagolub/pgxmock/v4"
)

func TestCoolingDurationClampsOversizedRetryAfter(t *testing.T) {
	// A misbehaving (or hostile) upstream must not pin a credential out of
	// routing for an unbounded time via Retry-After.
	if got := coolingDuration(errorsx.KindRateLimit, 400*24*time.Hour); got != maxCoolingDuration {
		t.Fatalf("oversized Retry-After not clamped: got %s want %s", got, maxCoolingDuration)
	}
	if got := coolingDuration(errorsx.KindNetwork, 25*time.Hour); got != maxCoolingDuration {
		t.Fatalf("25h Retry-After not clamped: got %s want %s", got, maxCoolingDuration)
	}
	// Legitimate windows pass through untouched.
	if got := coolingDuration(errorsx.KindNetwork, 90*time.Second); got != 90*time.Second {
		t.Fatalf("legitimate Retry-After altered: got %s", got)
	}
	// Negative/zero Retry-After falls through to the kind table.
	if got := coolingDuration(errorsx.KindNetwork, -1*time.Second); got != 120*time.Second {
		t.Fatalf("negative Retry-After should fall back to kind table, got %s", got)
	}
}

// TestWriteOnError_SoftDegradeGuardsSuspendedStates asserts the soft-degrade
// path only rewrites reason columns while the credential is NOT in
// suspended/auth_failed. Before the guard, a passive-observer network note
// overwrote state_reason_detail on suspended rows, destroying the only
// evidence of why the credential was stuck (prod creds 9/21).
func TestWriteOnError_SoftDegradeGuardsSuspendedStates(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()
	w := &Writer{dbPool: mockDB}

	// The matcher regex requires the guard clause — if the code drops it the
	// expectation no longer matches and the test fails.
	mockDB.ExpectExec(`UPDATE credentials .* availability_state NOT IN \('suspended', 'auth_failed'\)`).
		WithArgs("network", pgxmock.AnyArg(), 9).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := w.WriteOnError(t.Context(), 9, "mimo-v2.5-pro", Failure{
		Kind:   errorsx.KindNetwork,
		Detail: "passive_probe_review_failed: transient on mimo-v2.5-pro (6 errors)",
	})
	if err != nil {
		t.Fatalf("WriteOnError soft-degrade failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("soft-degrade SQL missing suspended/auth_failed guard: %v", err)
	}
}

// TestRestoreOnSuccessResetsProbeBackoffLadder asserts that a successful
// real request clears probe_consecutive_failures so a credential the vendor
// unblocked immediately regains its fast probe cadence.
func TestRestoreOnSuccessResetsProbeBackoffLadder(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()
	w := &Writer{dbPool: mockDB}

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs(31).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs(31).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	// P2 companion: probe ladder reset statement.
	mockDB.ExpectExec(`UPDATE credentials .* probe_consecutive_failures = 0`).
		WithArgs(31).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`UPDATE credential_model_bindings`).
		WithArgs(31).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()

	if err := w.RestoreOnSuccess(t.Context(), 31, ""); err != nil {
		t.Fatalf("RestoreOnSuccess failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("RestoreOnSuccess SQL mismatch: %v", err)
	}
}
