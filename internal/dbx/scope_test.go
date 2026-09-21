package dbx

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestScopeConfigValidation(t *testing.T) {
	bad := []ScopeConfig{
		{},
		{TenantGUC: "app.current_tenant"}, // missing rest
		{TenantGUC: "app current tenant", RoleGUC: "app.current_role", BypassGUC: "app.bypass_rls", SuperAdminValue: "s", BypassValue: "true"},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d: invalid config accepted", i)
		}
	}
	if err := testScope.Validate(); err != nil {
		t.Errorf("testScope: %v", err)
	}
}

func TestWithTenantTxSetsGUC(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	runner, err := NewScopeRunner(testScope, mock)
	if err != nil {
		t.Fatalf("NewScopeRunner: %v", err)
	}

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec("SELECT set_config($1, $2, true)").
		WithArgs("app.current_tenant", "tenant-a").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectCommit()

	var called bool
	err = runner.WithTenantTx(context.Background(), "tenant-a", func(_ context.Context, _ pgx.Tx) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithTenantTx: %v", err)
	}
	if !called {
		t.Error("fn was not invoked")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithTenantReadOnlyTx(t *testing.T) {
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()
	runner, _ := NewScopeRunner(testScope, mock)

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec("SELECT set_config($1, $2, true)").
		WithArgs("app.current_tenant", "tenant-a").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectCommit()

	if err := runner.WithTenantReadOnlyTx(context.Background(), "tenant-a", func(context.Context, pgx.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("WithTenantReadOnlyTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithTenantTxFailsClosedOnMissingScope(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	runner, _ := NewScopeRunner(testScope, mock)

	err := runner.WithTenantTx(context.Background(), "", func(context.Context, pgx.Tx) error {
		t.Fatal("fn must not run without scope")
		return nil
	})
	if !errors.Is(err, ErrMissingScope) {
		t.Fatalf("err = %v, want ErrMissingScope", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("no transaction should have been opened: %v", err)
	}
}

func TestWithTenantTxRollsBackOnError(t *testing.T) {
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()
	runner, _ := NewScopeRunner(testScope, mock)

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec("SELECT set_config($1, $2, true)").
		WithArgs("app.current_tenant", "tenant-a").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectRollback()

	boom := errors.New("boom")
	err := runner.WithTenantTx(context.Background(), "tenant-a", func(context.Context, pgx.Tx) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithSuperAdminTxSetsRoleAndBypass(t *testing.T) {
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()
	runner, _ := NewScopeRunner(testScope, mock)

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec("SELECT set_config($1, $2, true)").
		WithArgs("app.current_role", "super_admin").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("SELECT set_config($1, $2, true)").
		WithArgs("app.bypass_rls", "true").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectCommit()

	if err := runner.WithSuperAdminTx(context.Background(), func(context.Context, pgx.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("WithSuperAdminTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestScopeRunnerRejectsNilBeginner(t *testing.T) {
	if _, err := NewScopeRunner(testScope, nil); err == nil {
		t.Error("nil TxBeginner accepted")
	}
}
