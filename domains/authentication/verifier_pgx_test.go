package authentication

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// mockPool wraps pgxmock.PgxPoolIface to implement our DBQuerier interface.
type mockPool struct {
	pgxmock.PgxPoolIface
}

func (m *mockPool) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := m.PgxPoolIface.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func newMockPool(t *testing.T) *mockPool {
	t.Helper()
	p, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	return &mockPool{PgxPoolIface: p}
}

func TestKeyVerifier_Verify_Cache(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	kv.setCache("sk-test", &KeyInfo{ID: 1, TenantID: "tenant-x", KeyPrefix: "sk-1"})
	got, err := kv.Verify(context.Background(), "sk-test")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.ID != 1 || got.TenantID != "tenant-x" {
		t.Fatalf("got %+v", got)
	}
}

func TestKeyVerifier_Verify_Cache_StaleKeyPrefix(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	// KeyPrefix 为空 → cache 应被视为 stale
	kv.setCache("sk-stale", &KeyInfo{ID: 1, TenantID: "tenant-x", KeyPrefix: ""})

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "application_id", "application_code", "key_prefix",
		"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
		"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias",
	}).AddRow(1, "tenant-x", 1, "app1", "sk-1", nil, nil, nil, nil, nil, "default", nil, "active", nil)
	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
	// UPDATE last_used_at 异步触发
	mp.ExpectExec(`UPDATE api_keys`).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	got, err := kv.Verify(context.Background(), "sk-stale")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.KeyPrefix != "sk-1" {
		t.Fatalf("KeyPrefix = %q", got.KeyPrefix)
	}
	// 等待异步 UPDATE
	time.Sleep(50 * time.Millisecond)
}

func TestKeyVerifier_Verify_DBEmpty(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "application_id", "application_code", "key_prefix",
		"default_client_profile", "owner_user", "rate_limit_rpm", "rate_limit_concurrent",
		"rate_limit_tpm", "key_tier", "budget_usd", "status", "key_alias",
	})
	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	_, err := kv.Verify(context.Background(), "sk-bad")
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Fatalf("err = %v, want InvalidKeyError", err)
	}
}

func TestKeyVerifier_Verify_GenericDBError(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errDB{msg: "connection failed"})

	_, err := kv.Verify(context.Background(), "sk-bad")
	if err == nil {
		t.Fatal("expected error")
	}
}

type errDB struct{ msg string }

func (e errDB) Error() string { return e.msg }

func TestKeyVerifier_LookupKeyMeta_Disabled(t *testing.T) {
	kv := NewKeyVerifier() // 没 SetDB
	got, err := kv.LookupKeyMeta(context.Background(), "sk-x")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for disabled, got %+v", got)
	}
}

func TestKeyVerifier_LookupKeyMeta_EmptyKey(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")
	got, err := kv.LookupKeyMeta(context.Background(), "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty key, got %+v", got)
	}
}

func TestKeyVerifier_LookupKeyMeta_NoRows(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	rows := pgxmock.NewRows([]string{
		"id", "key_prefix", "owner_user", "status", "enabled",
		"code", "default_client_profile", "tenant_id", "application_id",
	})
	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	got, err := kv.LookupKeyMeta(context.Background(), "sk-x")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for no rows, got %+v", got)
	}
}

func TestKeyVerifier_LookupKeyMeta_OK(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	rows := pgxmock.NewRows([]string{
		"id", "key_prefix", "owner_user", "status", "enabled",
		"code", "default_client_profile", "tenant_id", "application_id",
	}).AddRow(1, "sk-1", nil, "active", true, "app1", nil, "tenant-x", 7)
	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	got, err := kv.LookupKeyMeta(context.Background(), "sk-x")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.ID != 1 || got.TenantID != "tenant-x" {
		t.Fatalf("got %+v", got)
	}
}

func TestKeyVerifier_LookupKeyMeta_DBError(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errDB{msg: "boom"})

	_, err := kv.LookupKeyMeta(context.Background(), "sk-x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeyVerifier_CheckBudget_Disabled(t *testing.T) {
	kv := NewKeyVerifier()
	if err := kv.CheckBudget(context.Background(), 1); err != nil {
		t.Fatalf("disabled CheckBudget: %v", err)
	}
}

func TestKeyVerifier_CheckBudget_NoBudget(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	rows := pgxmock.NewRows([]string{"budget_usd"}).AddRow(nil)
	mp.ExpectQuery(`SELECT budget_usd`).
		WithArgs(1).
		WillReturnRows(rows)

	if err := kv.CheckBudget(context.Background(), 1); err != nil {
		t.Fatalf("err = %v, want nil for nil budget", err)
	}
}

func TestKeyVerifier_CheckBudget_DBError(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	mp.ExpectQuery(`SELECT budget_usd`).
		WithArgs(1).
		WillReturnError(errDB{msg: "boom"})

	err := kv.CheckBudget(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeyVerifier_CheckBudget_Exceeded(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	b := 100.0
	rows1 := pgxmock.NewRows([]string{"budget_usd"}).AddRow(&b)
	mp.ExpectQuery(`SELECT budget_usd`).
		WithArgs(1).
		WillReturnRows(rows1)

	rows2 := pgxmock.NewRows([]string{"sum"}).AddRow(150.0)
	mp.ExpectQuery(`SELECT COALESCE`).
		WithArgs(1).
		WillReturnRows(rows2)

	err := kv.CheckBudget(context.Background(), 1)
	if _, ok := err.(*BudgetExceededError); !ok {
		t.Fatalf("err = %v, want BudgetExceededError", err)
	}
}

func TestKeyVerifier_CheckBudget_OK(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()
	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "secret")

	b := 100.0
	rows1 := pgxmock.NewRows([]string{"budget_usd"}).AddRow(&b)
	mp.ExpectQuery(`SELECT budget_usd`).
		WithArgs(1).
		WillReturnRows(rows1)

	rows2 := pgxmock.NewRows([]string{"sum"}).AddRow(50.0)
	mp.ExpectQuery(`SELECT COALESCE`).
		WithArgs(1).
		WillReturnRows(rows2)

	if err := kv.CheckBudget(context.Background(), 1); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestSetDB_EnablesVerifier(t *testing.T) {
	kv := NewKeyVerifier()
	if kv.Enabled() {
		t.Fatal("should be disabled before SetDB")
	}
	// 用一个真实 *pgxpool.Pool 适配器（nil pool 即可测试 Enabled）
	kv.SetDB(nil, "secret")
	// dbPool is non-nil interface holding nil pool
	// Enabled() returns true if dbPool != nil && secretKey != ""
	// We cannot easily construct a *pgxpool.Pool with pgxmock, so check secretKey path:
	if !kv.Enabled() {
		t.Fatal("should be enabled after SetDB")
	}
}

func TestSetCache_OverwriteAndEviction(t *testing.T) {
	kv := NewKeyVerifier()
	kv.ttl = time.Millisecond
	// Insert 10001 keys to trigger eviction
	for i := 0; i < 10001; i++ {
		kv.setCache(string(rune(i)), &KeyInfo{ID: i})
	}
	// Don't fail — just ensure no panic; some keys will be evicted
	_ = kv
}

func TestGetCache_NotExpired(t *testing.T) {
	kv := NewKeyVerifier()
	kv.setCache("k", &KeyInfo{ID: 42})
	got := kv.getCache("k")
	if got == nil || got.ID != 42 {
		t.Fatalf("got %+v", got)
	}
}
