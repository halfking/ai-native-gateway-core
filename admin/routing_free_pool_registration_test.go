package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func newFreePoolRegistrationMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func expectFreePoolRegistrationStart(mock pgxmock.PgxPoolIface, providerID, credentialID int, key any) {
	mock.ExpectBegin()
	mock.ExpectExec("(?s)INSERT INTO provider_catalog.*'restricted'").
		WithArgs("test-free", "Test Free", "openai-completions", "https://free.example/v1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO providers").
		WithArgs("test-free", "Test Free", "openai-completions", "https://free.example/v1", "").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(providerID))
	mock.ExpectQuery("INSERT INTO credentials").
		WithArgs(providerID, "test-free-free-key", key, "manual", "", `["free-pool","source:manual","catalog:test-free"]`, 0).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(credentialID))
}

func freePoolRegistrationConfig() freeProviderConfig {
	return freeProviderConfig{
		catalogCode: "test-free", displayName: "Test Free", baseURL: "https://free.example/v1",
		protocol: "openai-completions", apiKey: "sk-test", acquisitionMode: "manual",
		models: []string{" model-a ", "model-a", ""},
	}
}

func TestPersistFreePoolRegistration_ReturnsUpsertIDsAndCommitsOffers(t *testing.T) {
	mock := newFreePoolRegistrationMock(t)
	h := newTestHandler(t)
	cfg := freePoolRegistrationConfig()
	cfg.extraKeys = []string{"sk-extra"}

	expectFreePoolRegistrationStart(mock, 42, 73, pgxmock.AnyArg())
	mock.ExpectExec("INSERT INTO credential_keys").WithArgs(73, 1, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("SELECT id FROM models_canonical").WithArgs("model-a").WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec("INSERT INTO model_offers").WithArgs(73, (*int)(nil), "model-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE credential_model_bindings").WithArgs(73, []string{"model-a"}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	providerID, credentialID, models, err := h.persistFreePoolRegistration(context.Background(), mock, cfg)
	if err != nil {
		t.Fatalf("persistFreePoolRegistration: %v", err)
	}
	if providerID != 42 || credentialID != 73 || models != 1 {
		t.Fatalf("returned provider=%d credential=%d models=%d, want 42/73/1", providerID, credentialID, models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistFreePoolRegistration_KeylessStillCreatesCredential(t *testing.T) {
	mock := newFreePoolRegistrationMock(t)
	h := newTestHandler(t)
	cfg := freePoolRegistrationConfig()
	cfg.apiKey = ""
	cfg.models = nil

	expectFreePoolRegistrationStart(mock, 5, 9, nil)
	mock.ExpectCommit()
	providerID, credentialID, models, err := h.persistFreePoolRegistration(context.Background(), mock, cfg)
	if err != nil || providerID != 5 || credentialID != 9 || models != 0 {
		t.Fatalf("keyless registration = %d/%d/%d, %v", providerID, credentialID, models, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistFreePoolRegistration_OfferFailureRollsBackEntireRegistration(t *testing.T) {
	mock := newFreePoolRegistrationMock(t)
	h := newTestHandler(t)
	cfg := freePoolRegistrationConfig()

	expectFreePoolRegistrationStart(mock, 42, 73, pgxmock.AnyArg())
	mock.ExpectQuery("SELECT id FROM models_canonical").WithArgs("model-a").WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec("INSERT INTO model_offers").WithArgs(73, (*int)(nil), "model-a").
		WillReturnError(errors.New("offer write failed"))
	mock.ExpectRollback()

	providerID, credentialID, models, err := h.persistFreePoolRegistration(context.Background(), mock, cfg)
	if err == nil || !strings.Contains(err.Error(), "upsert offer") {
		t.Fatalf("offer error = %v, want upsert offer failure", err)
	}
	if providerID != 0 || credentialID != 0 || models != 0 {
		t.Fatalf("failure returned committed-looking IDs: %d/%d/%d", providerID, credentialID, models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistFreePoolRegistration_CatalogFailureRollsBack(t *testing.T) {
	mock := newFreePoolRegistrationMock(t)
	h := newTestHandler(t)
	cfg := freePoolRegistrationConfig()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO provider_catalog").
		WithArgs("test-free", "Test Free", "openai-completions", "https://free.example/v1").
		WillReturnError(errors.New("catalog write failed"))
	mock.ExpectRollback()
	_, _, _, err := h.persistFreePoolRegistration(context.Background(), mock, cfg)
	if err == nil || !strings.Contains(err.Error(), "upsert provider catalog") {
		t.Fatalf("catalog error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistFreePoolRegistration_CredentialFailureRollsBackProvider(t *testing.T) {
	mock := newFreePoolRegistrationMock(t)
	h := newTestHandler(t)
	cfg := freePoolRegistrationConfig()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO provider_catalog").
		WithArgs("test-free", "Test Free", "openai-completions", "https://free.example/v1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO providers").
		WithArgs("test-free", "Test Free", "openai-completions", "https://free.example/v1", "").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(42))
	mock.ExpectQuery("INSERT INTO credentials").
		WithArgs(42, "test-free-free-key", pgxmock.AnyArg(), "manual", "", `["free-pool","source:manual","catalog:test-free"]`, 0).
		WillReturnError(errors.New("credential write failed"))
	mock.ExpectRollback()
	_, _, _, err := h.persistFreePoolRegistration(context.Background(), mock, cfg)
	if err == nil || !strings.Contains(err.Error(), "upsert credential") {
		t.Fatalf("credential error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
