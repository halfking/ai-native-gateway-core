package credentialstate

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
// for the 2026-06-22 audit: per-model kinds (network, rate_limit,
// concurrent, timeout, upstream_down, stream_timeout) MUST update
// credential_model_bindings (the production router's source of truth
// via v_routable_credential_models) — not credentials.availability_state.
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
func TestWriteOnError_PerModelKind_UpdatesCMBNotCredentials(t *testing.T) {
	cases := []struct {
		name string
		kind errorsx.ErrorKind
	}{
		{"network", errorsx.KindNetwork},
		{"rate_limit", errorsx.KindRateLimit},
		{"concurrent", errorsx.KindConcurrent},
		{"timeout", errorsx.KindTimeout},
		{"upstream_down", errorsx.KindUpstreamDown},
		{"stream_timeout", errorsx.KindStreamTimeout},
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
			mockDB.ExpectExec(`UPDATE model_offers`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
		{"auth", errorsx.KindAuth, 3},
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
// Model-level errors (KindNetwork) now route through writeModelLevelFailureOnly,
// which does NOT update credentials.availability_state.
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
	err := w.WriteOnError(context.Background(), 42, "", Failure{Kind: errorsx.KindNetwork})
	if err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
