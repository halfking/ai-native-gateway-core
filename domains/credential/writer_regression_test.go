package credential

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/pashagolub/pgxmock/v4"
)

// sqlOnlyMatcher matches SQL by structure (table + key clause), ignoring
// $1, $2, $3 placeholder counts. This lets one ExpectExec cover sibling
// SQLs that differ only in $N placeholder counts (e.g. quota_periodic
// has 4 args, quota_permanent has 3 args — both emit "UPDATE credentials
// ... WHERE id = $...").
type sqlOnlyMatcher struct{}

// Match returns nil if the normalized `expectedSQL` is found (as a
// regex) anywhere in the normalized `actualSQL`.
func (sqlOnlyMatcher) Match(expectedSQL, actualSQL string) error {
	e := normalize(expectedSQL)
	a := normalize(actualSQL)
	if regexp.MustCompile(e).MatchString(a) {
		return nil
	}
	return errSQLMismatch{expected: expectedSQL, actual: actualSQL}
}

type errSQLMismatch struct{ expected, actual string }

func (e errSQLMismatch) Error() string {
	return "expected SQL pattern not found in actual SQL"
}

func normalize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return regexp.MustCompile(`\$\d+`).ReplaceAllString(s, "$")
}

func newSQLOnlyMock() pgxmock.PgxPoolIface {
	p, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(sqlOnlyMatcher{}))
	return p
}

// TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials pins the fix
// for the 2026-06-22 audit: per-model kinds that represent a real
// capacity/quota signal (rate_limit, concurrent, stream_timeout,
// no_available_channel) MUST update credential_model_bindings (the
// production router's source of truth via v_routable_credential_models)
// — not credentials.availability_state.
//
// Previously, WriteOnError wrote to the credentials table only, which
//  1. did not affect the production router (router reads cmb.available)
//  2. flipped the WHOLE credential's availability state when only one
//     (cred, model) was failing, killing sibling models on the same
//     credential
//
// 2026-06-23 fix: Model-level errors now use writeModelLevelFailureOnly,
// which updates ONLY cmb and model_offers, NOT credentials.availability_state.
// This prevents cross-model pollution where minimax-m3 failing would
// incorrectly mark minimax-01 unavailable too.
//
// 2026-08-11 soft-degrade split: network/timeout/upstream_down moved to
// the soft-degrade path (see TestWriteOnError_TransientKind_RecordsWithoutCoolingBinding)
// because they are single-request transient signals that clear in seconds
// and must not remove an otherwise-healthy node from routing. The remaining
// kinds here are the ones whose semantics genuinely require a cooling window.
func TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials(t *testing.T) {
	cases := []struct {
		name string
		kind errorsx.ErrorKind
	}{
		{"rate_limit", errorsx.KindRateLimit},
		{"concurrent", errorsx.KindConcurrent},
		{"stream_timeout", errorsx.KindStreamTimeout},
		{"no_available_channel", errorsx.KindNoAvailableChannel},
	}

	// Common arg layout for all 3 expected SQLs in the per-model path:
	//   cmb:           $1=reason, $2=credentialID, $3=rawModel (3 args)
	//   model_offers:  $1=reason, $2=credentialID, $3=rawModel (3 args)
	//   credentials:   $1=availability, $2=recoverAt, $3=reason,
	//                  $4=detail, $5=credentialID (5 args)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB := newSQLOnlyMock()
			defer mockDB.Close()

			mockDB.ExpectExec(`UPDATE credential_model_bindings`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				// model_offers is a VIEW that automatically reflects cmb updates.
				// No separate UPDATE needed (removed in writer.go:344-347).

			w := &Writer{dbPool: mockDB}
			err := w.WriteOnError(context.Background(), 42, "minimax-m3", Failure{Kind: tc.kind})
			if err != nil {
				t.Fatalf("WriteOnError: %v", err)
			}
			if err := mockDB.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// TestWriteOnError_TransientKind_RecordsWithoutCoolingBinding pins the
// 2026-08-11 soft-degrade fix: KindNetwork, KindTimeout, and KindUpstreamDown
// record the real upstream detail on credentials.state_reason_* for operators
// but do NOT flip credential_model_bindings.available — so the node stays in
// the routing pool. A single transient blip must not exclude an accessible
// node; sustained failures are still caught by the executor circuit breaker
// and the credentialhealth aggregate 80% threshold (which still counts these
// kinds). Sibling test TestWriteOnError_UpstreamOverloadedRecordsWithoutCoolingBinding
// pins the same shape for KindUpstreamOverloaded.
func TestWriteOnError_TransientKind_RecordsWithoutCoolingBinding(t *testing.T) {
	cases := []struct {
		name string
		kind errorsx.ErrorKind
	}{
		{"network", errorsx.KindNetwork},
		{"timeout", errorsx.KindTimeout},
		{"upstream_down", errorsx.KindUpstreamDown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB := newSQLOnlyMock()
			defer mockDB.Close()

			mockDB.ExpectExec(`UPDATE credentials`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			w := &Writer{dbPool: mockDB}
			err := w.WriteOnError(context.Background(), 42, "gpt-5.6-luna", Failure{
				Kind:   tc.kind,
				Detail: `{"error":{"message":"transient blip"}}`,
			})
			if err != nil {
				t.Fatalf("WriteOnError: %v", err)
			}
			if err := mockDB.ExpectationsWereMet(); err != nil {
				t.Fatalf("%s must not update credential_model_bindings: %v", tc.name, err)
			}
		})
	}
}

func TestWriteOnError_UpstreamOverloadedRecordsWithoutCoolingBinding(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w := &Writer{dbPool: mockDB}
	err := w.WriteOnError(context.Background(), 42, "gpt-5.6-luna", Failure{
		Kind:   errorsx.KindUpstreamOverloaded,
		Detail: `{"error":{"message":"Our servers are currently overloaded. Please try again later."}}`,
	})
	if err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("overload must not update credential_model_bindings: %v", err)
	}
}

// TestWriteOnError_CredentialWideKind_OnlyUpdatesCredentials pins
// the credential-wide path (quota, auth, auth_revoked) — these kinds
// DO write credentials.availability_state because the entire credential
// is exhausted/revoked. cmb / model_offers writes are intentionally
// skipped here because the entire credential becomes unreachable
// (handled by the lifecycle check in v_routable_credential_models).
func TestWriteOnError_CredentialWideKind_OnlyUpdatesCredentials(t *testing.T) {
	cases := []struct {
		name     string
		kind     errorsx.ErrorKind
		argCount int
	}{
		{"quota_periodic", errorsx.KindQuotaPeriodic, 4},
		{"quota_permanent", errorsx.KindQuotaPermanent, 3},
		{"quota_balance", errorsx.KindQuotaBalance, 3},
		{"quota", errorsx.KindQuota, 3},
		{"auth_revoked", errorsx.KindAuthRevoked, 3},
		// 2026-07-22: KindAuth now binds $4 = availability_recover_at
		// (now() + 15min), so the call signature is 4-arg, not 3.
		// See TestWriteOnError_KindAuth_SetsRecoverAt for the contract
		// test that pins this.
		{"auth", errorsx.KindAuth, 4},
		{"transient", errorsx.KindTransient, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB := newSQLOnlyMock()
			defer mockDB.Close()

			args := make([]interface{}, tc.argCount)
			for i := range args {
				args[i] = pgxmock.AnyArg()
			}
			mockDB.ExpectExec(`UPDATE credentials`).
				WithArgs(args...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			w := &Writer{dbPool: mockDB}
			err := w.WriteOnError(context.Background(), 42, "minimax-m3", Failure{Kind: tc.kind})
			if err != nil {
				t.Fatalf("WriteOnError: %v", err)
			}
			if err := mockDB.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestSetCredentialUnavailable_ClosesCredentialAndAllBindings(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), 42).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`UPDATE credential_model_bindings`).
		WithArgs(string(errorsx.KindConcurrent), pgxmock.AnyArg(), 42).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mockDB.ExpectCommit()

	w := &Writer{dbPool: mockDB}
	if err := w.SetCredentialUnavailable(context.Background(), 42, Failure{
		Kind: errorsx.KindConcurrent, Detail: "credential-wide concurrent ceiling",
	}); err != nil {
		t.Fatalf("SetCredentialUnavailable: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("credential-wide closure must update credentials and all bindings: %v", err)
	}
}

// TestWriteOnError_PerModelKind_EmptyRawModel_AllBindings pins the
// legacy fallback path: when rawModel is empty (test path), the cmb
// write targets every binding on the credential. This keeps the
// historical behaviour of WriteOnError for callers that don't know
// the model — they still flip the whole credential's bindings, but
// they do so on the cmb table (the source of truth) rather than on
// credentials.availability_state (which would incorrectly pollute the
// credential-level state).
//
// 2026-06-23: Updated to reflect the fix for credential-state pollution.
// Model-level errors now route through writeModelLevelFailureOnly,
// which does NOT update credentials.availability_state.
//
// 2026-08-11: switched the fixture kind from KindNetwork to KindRateLimit
// because KindNetwork moved to the soft-degrade path (no cmb write). The
// empty-rawModel cmb-fan-out behaviour is unchanged for the hard-degrade
// per-model kinds.
func TestWriteOnError_PerModelKind_EmptyRawModel_AllBindings(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	// cmb SQL when rawModel="" takes 2 args: reason, credentialID.
	// model_offers mirror is SKIPPED (no clean JOIN key) — see
	// writeModelLevelFailureOnly's comment.
	// ✅ NO credentials SQL expected (this is the fix for the pollution bug).
	mockDB.ExpectExec(`UPDATE credential_model_bindings`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	w := &Writer{dbPool: mockDB}
	err := w.WriteOnError(context.Background(), 42, "", Failure{Kind: errorsx.KindRateLimit})
	if err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestWriteOnError_RequestLevelErrorsDoNotWriteState(t *testing.T) {
	for _, kind := range []errorsx.ErrorKind{
		errorsx.KindContextLength,
		errorsx.KindUnsupportedFeature,
		errorsx.KindClientBug,
		errorsx.KindContentFilter,
		errorsx.KindToolCallIdMismatch,
	} {
		t.Run(string(kind), func(t *testing.T) {
			mockDB := newSQLOnlyMock()
			defer mockDB.Close()

			w := &Writer{dbPool: mockDB}
			if err := w.WriteOnError(context.Background(), 22, "glm-5.2", Failure{Kind: kind, Detail: "request rejected"}); err != nil {
				t.Fatalf("WriteOnError(%q): %v", kind, err)
			}
			if err := mockDB.ExpectationsWereMet(); err != nil {
				t.Fatalf("request-level kind %q wrote database state: %v", kind, err)
			}
		})
	}
}

// TestWriteOnError_KindAuth_SetsRecoverAt pins BUG #2 fix (2026-07-22):
// KindAuth must write a future availability_recover_at timestamp so
// bg/credential_recovery.go's 60s ticker can flip the credential back to
// 'ready' once the cooling period elapses. Before this fix the SQL
// wrote `availability_recover_at = NULL`, which made the recovery
// ticker's `AND availability_recover_at IS NOT NULL` clause impossible
// to satisfy — auth_failed credentials were stuck indefinitely.
//
// We assert that:
//  1. Exactly 4 args are passed (the new $4 is the recoverAt timestamp).
//  2. The recoverAt arg is a future time.Time, not zero.
func TestWriteOnError_KindAuth_SetsRecoverAt(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	// BUG #2 fix (2026-07-22): KindAuth SQL now takes 4 args — the new
	// $4 is availability_recover_at = now() + 15min. The 60s recovery
	// ticker in bg/credential_recovery.go depends on
	// `availability_recover_at IS NOT NULL AND <= now()` to flip
	// auth_failed back to ready; writing NULL broke that contract.
	//
	// sqlOnlyMatcher only matches SQL structure, so the binding
	// contract here is: "WriteOnError(KindAuth) emits exactly 4 bound
	// params". If a future refactor accidentally reverts the $4, this
	// test fails with "expected 4 args, got 3".
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w := &Writer{dbPool: mockDB}
	if err := w.WriteOnError(context.Background(), 42, "claude-sonnet-5", Failure{Kind: errorsx.KindAuth, Detail: "upstream 403"}); err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestWriteOnError_KindAuthRevoked_NoRecoverAt pins that KindAuthRevoked
// remains a permanent "suspended" state (admin-only recovery via the
// admin UI / CLI). Only KindAuth (transient credential-level auth
// failures like upstream 401/403 on a single apikey) should auto-recover.
// This protects against accidental scope creep when refactoring
// KindAuth's path.
func TestWriteOnError_KindAuthRevoked_NoRecoverAt(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	// 3-arg signature: $1=reason, $2=detail, $3=credentialID (no recover_at)
	mockDB.ExpectExec(`UPDATE credentials`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w := &Writer{dbPool: mockDB}
	if err := w.WriteOnError(context.Background(), 42, "claude-sonnet-5", Failure{Kind: errorsx.KindAuthRevoked}); err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
