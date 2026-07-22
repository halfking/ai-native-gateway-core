package bg

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// TestRunOne_NoRoutableModels_InsertsFailedPlaceholder is the regression
// guard for the 2026-07-22 deadlock fix.  Before the fix, runOne returned
// fmt.Errorf("credential %d has no routable models") without inserting a
// self_check_runs row, which meant pickDueCredential kept re-picking the
// same broken credential every 5-min tick forever.
//
// After the fix, runOne must:
//   1. call insertRun to create a self_check_runs row
//   2. call finalizeRun with status='failed' and a non-empty error_detail
//      so completed_at advances (unblocking the next due-credential pick)
//   3. still return a non-nil error so cycleOnce logs it
func TestRunOne_NoRoutableModels_InsertsFailedPlaceholder(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 2 // the credential that triggered the bug on prod 154

	// pickModels returns empty: 3 tier queries all return nothing.
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}))
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnError(scErrNoRows)
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}))

	// insertRun: INSERT INTO self_check_runs (...) RETURNING id
	// Args: ($1=cred-N, $2=startedAt time.Time).
	mock.ExpectQuery("INSERT INTO self_check_runs").
		WithArgs(fmt.Sprintf("cred-%d", credID), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(42)))

	// finalizeRun: UPDATE self_check_runs SET ... WHERE id=$1
	mock.ExpectExec("UPDATE self_check_runs SET").
		WithArgs(
			int64(42),                     // run id
			pgxmock.AnyArg(),              // completed_at
			pgxmock.AnyArg(),              // duration_ms
			"failed",                      // status
			0,                             // rounds_total
			0,                             // rounds_success
			false,                         // had_tool_call
			0,                             // total_tokens
			0,                             // avg_latency_ms
			"none",                        // error_type — must satisfy 338 CHECK on prod
			pgxmock.AnyArg(),              // error_detail
			"random",                      // selection_strategy — must satisfy 341 CHECK
			pgxmock.AnyArg(),              // attempted_models jsonb
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := w.runOne(context.Background(), credID)
	if err == nil {
		t.Fatalf("runOne: expected error (credential %d has no routable models) but got nil", credID)
	}
	// Cycle must still propagate the error so cycleOnce logs it.
	if err.Error() != fmt.Sprintf("credential %d has no routable models", credID) {
		t.Errorf("runOne error: got %q want %q", err.Error(),
			fmt.Sprintf("credential %d has no routable models", credID))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestRunOne_PickModelsError_DoesNotInsertPlaceholder verifies the OTHER
// failure path of runOne: when pickModels itself errors (e.g. SQL fails),
// we must NOT insert a placeholder row, because the failed run would be
// attributed to a credential we couldn't even analyze.
func TestRunOne_PickModelsError_DoesNotInsertPlaceholder(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 3
	const dbErr = "simulated pg boom"

	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnError(errors.New(dbErr))

	err := w.runOne(context.Background(), credID)
	if err == nil {
		t.Fatal("runOne: expected error from pickModels but got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickDueCredential_PerCredentialLastAt is the regression guard for
// the second half of the 2026-07-22 fix: the LATERAL subquery must group
// last_at by credential_id (via model_name = 'cred-<id>'), NOT by
// tenant_id.  Pre-fix, all credentials sharing tenant_id='default' had
// the same last_at, so the worker kept picking c.id=2 forever.
func TestPickDueCredential_PerCredentialLastAt(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	// The query now references model_name='cred-' || c.id::text.  We don't
	// care which credential is returned (pgxmock matches on SQL substring),
	// only that the query doesn't reference tenant_id anymore — that's the
	// regression we are guarding.  Args: ($1 = "%d seconds" interval).
	mock.ExpectQuery("model_name = 'cred-'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(11))

	got, ok, err := w.pickDueCredential(context.Background())
	if err != nil {
		t.Fatalf("pickDueCredential: %v", err)
	}
	if !ok {
		t.Fatal("pickDueCredential: expected ok=true")
	}
	if got != 11 {
		t.Errorf("credential id: got %d want 11", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickDueCredential_NoDueCredentials returns (0, false, nil) when no
// credential has an aged-out last_at.  This is the path that keeps the
// worker quiet between cycles once all credentials are healthy and
// recently probed.
func TestPickDueCredential_NoDueCredentials(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	mock.ExpectQuery("model_name = 'cred-'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(scErrNoRows)

	id, ok, err := w.pickDueCredential(context.Background())
	if err != nil {
		t.Fatalf("pickDueCredential: %v", err)
	}
	if ok {
		t.Errorf("pickDueCredential: expected ok=false (no due creds), got id=%d", id)
	}
	if id != 0 {
		t.Errorf("pickDueCredential: expected id=0, got %d", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}