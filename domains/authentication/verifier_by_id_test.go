package authentication

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func verifyByIDRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "tenant_id", "application_id", "application_code", "key_prefix",
		"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
		"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias",
		"customer_id",
	}).AddRow(7, "tenant-a", 3, "app1", "sk-7", nil, nil, nil, nil, nil, "default", nil, "active", nil, nil)
}

func TestKeyVerifier_VerifyByID(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	mp.ExpectQuery(`WHERE ak.id = \$1`).
		WithArgs(7).
		WillReturnRows(verifyByIDRows())

	info, err := kv.VerifyByID(context.Background(), 7)
	if err != nil {
		t.Fatalf("VerifyByID: %v", err)
	}
	if info.ID != 7 || info.TenantID != "tenant-a" || info.ApplicationID != 3 {
		t.Fatalf("info = %+v", info)
	}
	if err := mp.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestKeyVerifier_VerifyByID_RevokedFailsClosed(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	mp.ExpectQuery(`WHERE ak.id = \$1`).
		WithArgs(8).
		WillReturnError(pgx.ErrNoRows)

	if _, err := kv.VerifyByID(context.Background(), 8); err == nil {
		t.Fatal("revoked/expired key must fail closed")
	}
}

func TestKeyVerifier_VerifyByID_InvalidIDAndDisabled(t *testing.T) {
	kv := NewKeyVerifier()
	if _, err := kv.VerifyByID(context.Background(), 0); err == nil {
		t.Fatal("id<=0 must be rejected")
	}
	kv.setDBQuerier(newMockPool(t), "")
	if _, err := kv.VerifyByID(context.Background(), 1); err == nil {
		t.Fatal("disabled verifier must fail closed")
	}
}
