package dbx

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func TestDBContextLimitUpdater_UpdateContextLimit_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	updater := NewDBContextLimitUpdater(mock)

	const credentialID = 22
	const rawModel = "minimax-m3"
	const limit = 262144

	mock.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(credentialID, rawModel, limit).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	updated, err := updater.UpdateContextLimit(context.Background(), credentialID, rawModel, limit)
	if err != nil {
		t.Fatalf("UpdateContextLimit failed: %v", err)
	}
	if !updated {
		t.Fatal("expected a binding to be updated")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

func TestDBContextLimitUpdater_UpdateContextLimit_NoBinding(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	updater := NewDBContextLimitUpdater(mock)

	const credentialID = 99
	const rawModel = "unknown-model"
	const limit = 100000

	// No matching binding → 0 rows affected → silent no-op
	mock.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(credentialID, rawModel, limit).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	updated, err := updater.UpdateContextLimit(context.Background(), credentialID, rawModel, limit)
	if err != nil {
		t.Fatalf("UpdateContextLimit should succeed with 0 rows: %v", err)
	}
	if updated {
		t.Fatal("expected no binding to be updated")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

func TestDBContextLimitUpdater_UpdateContextLimit_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	updater := NewDBContextLimitUpdater(mock)

	const credentialID = 22
	const rawModel = "minimax-m3"
	const limit = 262144

	mock.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(credentialID, rawModel, limit).
		WillReturnError(errors.New("database connection failed"))

	_, err = updater.UpdateContextLimit(context.Background(), credentialID, rawModel, limit)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}
