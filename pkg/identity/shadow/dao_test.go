package shadow

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// newMockDAO 返回 DAO + sqlmock + fake-match-expectations helper。
func newMockDAO(t *testing.T) (*DAO, sqlmock.Sqlmock, *sql.DB) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewDAO(db), mock, db
}

func TestGetByProvider_NoRows(t *testing.T) {
	dao, mock, _ := newMockDAO(t)
	const q = `
SELECT su.shadow_user_id, su.canonical_user_id, su.status,
       su.display_name, su.primary_email, su.created_at, su.updated_at
FROM shadow_user_providers sp
JOIN shadow_users su ON su.shadow_user_id = sp.shadow_user_id
WHERE sp.provider = $1 AND sp.subject = $2 AND sp.tenant_id = $3
LIMIT 1`
	mock.ExpectQuery(q).WithArgs("memora", "u-1", "default").
		WillReturnRows(sqlmock.NewRows([]string{"shadow_user_id", "canonical_user_id", "status", "display_name", "primary_email", "created_at", "updated_at"}))

	su, err := dao.GetByProvider(context.Background(), "memora", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if su != nil {
		t.Errorf("expected nil, got %+v", su)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRecord_NewMapping(t *testing.T) {
	dao, mock, _ := newMockDAO(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3, 0))`).
		WithArgs("memora", "u-1", "default").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// 1. SELECT existing
	mock.ExpectQuery(`SELECT shadow_user_id FROM shadow_user_providers
WHERE provider = $1 AND subject = $2 AND tenant_id = $3`).
		WithArgs("memora", "u-1", "default").
		WillReturnRows(sqlmock.NewRows([]string{"shadow_user_id"}))
	// 2. INSERT shadow_users
	mock.ExpectExec(`INSERT INTO shadow_users (shadow_user_id, canonical_user_id, status)
VALUES ($1, $2, 'active')
ON CONFLICT (shadow_user_id) DO NOTHING`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	// 3. INSERT shadow_user_providers
	mock.ExpectExec(`INSERT INTO shadow_user_providers
(provider, subject, tenant_id, shadow_user_id, external_id, metadata, linked_at, last_seen_at)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb, $7, $8)`).
		WithArgs("memora", "u-1", "default", sqlmock.AnyArg(), "", "{}", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	// 4. audit insert (after commit, separate connection)
	mock.ExpectExec(`INSERT INTO shadow_audit (actor_project, action, target_provider, target_subject, target_shadow_id, metadata)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb)`).
		WithArgs("memora", "auto_create", "memora", "u-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	shadowID, canonicalID, isNew, err := dao.Record(context.Background(), ShadowProvider{
		Provider: "memora",
		Subject:  "u-1",
		Metadata: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Error("expected isNew=true")
	}
	if shadowID == "" || canonicalID == "" {
		t.Errorf("ids empty: %s / %s", shadowID, canonicalID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRecord_ExistingMapping(t *testing.T) {
	dao, mock, _ := newMockDAO(t)
	existing := "11111111-1111-1111-1111-111111111111"
	canonical := "22222222-2222-2222-2222-222222222222"

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3, 0))`).
		WithArgs("memora", "u-1", "default").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT shadow_user_id FROM shadow_user_providers
WHERE provider = $1 AND subject = $2 AND tenant_id = $3`).
		WithArgs("memora", "u-1", "default").
		WillReturnRows(sqlmock.NewRows([]string{"shadow_user_id"}).AddRow(existing))
	mock.ExpectExec(`UPDATE shadow_user_providers
SET last_seen_at = $1, external_id = NULLIF($2, ''), metadata = $3::jsonb
WHERE provider = $4 AND subject = $5 AND tenant_id = $6`).
		WithArgs(sqlmock.AnyArg(), "", "{}", "memora", "u-1", "default").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT canonical_user_id FROM shadow_users WHERE shadow_user_id = $1`).
		WithArgs(existing).
		WillReturnRows(sqlmock.NewRows([]string{"canonical_user_id"}).AddRow(canonical))
	mock.ExpectCommit()
	// audit
	mock.ExpectExec(`INSERT INTO shadow_audit (actor_project, action, target_provider, target_subject, target_shadow_id, metadata)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb)`).
		WithArgs("memora", "link", "memora", "u-1", existing, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	gotShadow, gotCanonical, isNew, err := dao.Record(context.Background(), ShadowProvider{
		Provider: "memora",
		Subject:  "u-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Error("expected isNew=false for existing")
	}
	if gotShadow != existing || gotCanonical != canonical {
		t.Errorf("got %s/%s, want %s/%s", gotShadow, gotCanonical, existing, canonical)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRecord_RejectsEmptyProvider(t *testing.T) {
	dao, _, _ := newMockDAO(t)
	if _, _, _, err := dao.Record(context.Background(), ShadowProvider{Subject: "u-1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestReconcileOrphans_RejectsZeroDuration(t *testing.T) {
	dao, _, _ := newMockDAO(t)
	if _, err := dao.ReconcileOrphans(context.Background(), 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateProfile_RejectsEmptyID(t *testing.T) {
	dao, _, _ := newMockDAO(t)
	if err := dao.UpdateProfile(context.Background(), "", "x", "y", "active"); err == nil {
		t.Fatal("expected error")
	}
}

// TestRecord_RealPG 端到端真实 PG 集成测试（需 IDENTITY_SHADOW_DSN 环境变量）。
//
// 运行方式：
//
//	IDENTITY_SHADOW_DSN="postgres://kxuser:kxpass@127.0.0.1:5432/identity_shadow?sslmode=disable" \
//	  go test -tags=integration -run TestRecord_RealPG ./shadow/...
func TestRecord_RealPG(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dsn := os.Getenv("IDENTITY_SHADOW_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_SHADOW_DSN not set, skipping real-PG test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dao := NewDAO(db)
	ctx := context.Background()

	provider := "test-realpg"
	subject := "u-realpg-" + time.Now().Format("20060102150405.000")
	tenantID := "default"

	// Record 1: 新建
	shadowID, canonicalID, isNew, err := dao.Record(ctx, ShadowProvider{
		Provider:   provider,
		Subject:    subject,
		TenantID:   tenantID,
		ExternalID: "ext-1",
		Metadata:   `{"src":"test"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || shadowID == "" || canonicalID == "" {
		t.Errorf("unexpected: isNew=%v shadowID=%s canonical=%s", isNew, shadowID, canonicalID)
	}

	// Record 2: 同 (provider, subject, tenant) → isNew=false, id 不变
	_, canonicalID2, isNew2, err := dao.Record(ctx, ShadowProvider{
		Provider:   provider,
		Subject:    subject,
		TenantID:   tenantID,
		ExternalID: "ext-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 {
		t.Error("expected isNew=false")
	}
	if canonicalID2 != canonicalID {
		t.Errorf("canonical drifted: %s vs %s", canonicalID, canonicalID2)
	}

	// GetByProvider 回查
	su, err := dao.GetByProvider(ctx, provider, subject, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if su == nil || su.ShadowUserID != shadowID {
		t.Errorf("get mismatch: %+v", su)
	}

	// UpdateProfile
	if err := dao.UpdateProfile(ctx, shadowID, "Alice", "[email protected]", "active"); err != nil {
		t.Fatal(err)
	}

	// 清理
	_, _ = db.ExecContext(ctx, "DELETE FROM shadow_user_providers WHERE provider=$1 AND subject=$2", provider, subject)
	_, _ = db.ExecContext(ctx, "DELETE FROM shadow_users WHERE shadow_user_id=$1", shadowID)
}
