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
		"customer_id", "is_system", "capabilities",
	}).AddRow(7, "tenant-a", 3, "app1", "sk-7", nil, nil, nil, nil, nil, "default", nil, "active", nil, nil, true, `["session.audit"]`)
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
	if !info.IsSystem {
		t.Fatal("VerifyByID must propagate api_keys.is_system for durable dispatch trust")
	}
	// ★ 2026-11-26 12h 审计轮：★ VerifyByID 的投影**原先漏了 capabilities** ⇒
	// 这条路径读出的 Capabilities 恒为 nil ⇒ 同一把 Key 在两条读路径上读出两副
	// 面孔 ⇒ 一旦 W4.3 把能力闸接上，走这条路径的调用方会一律判「无能力」。
	if len(info.Capabilities) != 1 || info.Capabilities[0] != "session.audit" {
		t.Fatalf("VerifyByID must propagate api_keys.capabilities, got %v", info.Capabilities)
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
