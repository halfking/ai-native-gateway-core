package admin

import (
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func TestForceEnableBindingsWholeCredential(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec(`(?s)UPDATE credential_model_bindings.*SET available = TRUE,.*unavailable_reason = NULL,.*unavailable_at = NULL,.*unavailable_recover_at = NULL,.*WHERE credential_id = \$1\s*$`).
		WithArgs(42).WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	rows, err := forceEnableBindings(t.Context(), mock, 42, "")
	if err != nil || rows != 3 {
		t.Fatalf("whole credential must restore its bindings, got rows=%d err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestForceEnableBindingsSingleModel(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec(`(?s)UPDATE credential_model_bindings cmb.*WHERE cmb.credential_id = \$1.*pm.id = cmb.provider_model_id.*pm.raw_model_name = \$2`).
		WithArgs(42, "minimax-m3").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	rows, err := forceEnableBindings(t.Context(), mock, 42, "minimax-m3")
	if err != nil || rows != 1 {
		t.Fatalf("single model reset rows=%d err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestForceEnableBindingsPropagatesDatabaseFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	want := errors.New("binding update failed")
	mock.ExpectExec(`UPDATE credential_model_bindings`).WithArgs(42).WillReturnError(want)
	rows, err := forceEnableBindings(t.Context(), mock, 42, "")
	if !errors.Is(err, want) || rows != 0 {
		t.Fatalf("database failure must abort recovery, got rows=%d err=%v", rows, err)
	}
}
