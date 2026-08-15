package pending

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/pashagolub/pgxmock/v4"
)

func testAADKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	key := [32]byte{9}
	kr, err := secret.NewKeyring(map[string][32]byte{"d1": key}, "d1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return kr
}

// TestPGSource_Get_CompletedDecrypts：durable 任务表 completed 行的
// result_ciphertext 用 durable-result 域 AAD 解密后作为 pending Response
// 返回（doc 18 §12.1：Redis 丢失时从任务表回源返回规范化结果）。
func TestPGSource_Get_CompletedDecrypts(t *testing.T) {
	kr := testAADKeyring(t)
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-9", RequestHash: "reqhash-9"}
	env, keyID, err := secret.EncryptWithAAD([]byte(`{"result":"body"}`), kr, secret.AADDomainDurableResult, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, result_ciphertext, result_hash, content_type, completed_at, reason_code`).
		WithArgs("sess-1", "req-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "result_ciphertext", "result_hash",
			"content_type", "completed_at", "reason_code",
		}).AddRow("task-9", "req-1", "tenant-1", "completed", env, "reqhash-9",
			"application/json", now, ""))

	src := NewPGSource(mock, kr)
	r, found, err := src.Get(context.Background(), "sess-1", "req-1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if r.Status != StatusCompleted || r.Body != `{"result":"body"}` || r.ContentType != "application/json" {
		t.Fatalf("response = %+v", r)
	}
	if r.TenantID != "tenant-1" || r.RequestID != "req-1" {
		t.Fatalf("ids = %+v", r)
	}
	if r.CompletedAt != now.Unix() {
		t.Fatalf("completedAt = %d, want %d", r.CompletedAt, now.Unix())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if keyID != "d1" {
		t.Fatalf("keyID = %q, want d1", keyID)
	}
}

// TestPGSource_Get_NonTerminalStatus：非终态任务回源返回 in_progress 投影
// （无正文）；failed/expired/canceled 返回 failed + reason。
func TestPGSource_Get_NonTerminalStatus(t *testing.T) {
	kr := testAADKeyring(t)
	cases := []struct {
		dbStatus  string
		reason    string
		wantSt    Status
		wantMsgOk bool
	}{
		{"running", "", StatusInProgress, false},
		{"retry_scheduled", "waiting_recovery", StatusInProgress, false},
		{"failed", "survival_expired", StatusFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.dbStatus, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			t.Cleanup(mock.Close)
			mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, result_ciphertext, result_hash, content_type, completed_at, reason_code`).
				WithArgs("sess-1", "req-1").
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "request_id", "tenant_id", "status", "result_ciphertext", "result_hash",
					"content_type", "completed_at", "reason_code",
				}).AddRow("task-9", "req-1", "tenant-1", tc.dbStatus, nil, "reqhash-9",
					"", nil, tc.reason))

			src := NewPGSource(mock, kr)
			r, found, err := src.Get(context.Background(), "sess-1", "req-1")
			if err != nil || !found {
				t.Fatalf("Get: found=%v err=%v", found, err)
			}
			if r.Status != tc.wantSt {
				t.Fatalf("status = %q, want %q", r.Status, tc.wantSt)
			}
			if r.Body != "" {
				t.Fatalf("non-completed must not carry body, got %q", r.Body)
			}
			if tc.wantMsgOk && r.ErrorMessage != tc.reason {
				t.Fatalf("errorMessage = %q, want %q", r.ErrorMessage, tc.reason)
			}
		})
	}
}

// TestPGSource_Get_NotFound：无行返回 (nil,false,nil)，与 Store 语义对齐。
func TestPGSource_Get_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status`).
		WithArgs("sess-x", "req-x").
		WillReturnError(pgx.ErrNoRows)

	src := NewPGSource(mock, testAADKeyring(t))
	r, found, err := src.Get(context.Background(), "sess-x", "req-x")
	if r != nil || found || err != nil {
		t.Fatalf("Get: r=%v found=%v err=%v", r, found, err)
	}
}

// TestPGSource_Get_FailClosed：无密钥、密文篡改/AAD 绑定不匹配必须报错，
// 不得把任务当作可读或未命中（doc 18 §11.2 fail closed）。
func TestPGSource_Get_FailClosed(t *testing.T) {
	kr := testAADKeyring(t)
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-9", RequestHash: "reqhash-9"}
	env, _, err := secret.EncryptWithAAD([]byte("body"), kr, secret.AADDomainDurableResult, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	cases := []struct {
		name       string
		kr         *secret.Keyring
		ciphertext string
	}{
		{"nil keyring", nil, env},
		{"tampered ciphertext", kr, env[:len(env)-4] + "AAAA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			t.Cleanup(mock.Close)
			mock.ExpectQuery(`SELECT id, request_id, tenant_id, status`).
				WithArgs("sess-1", "req-1").
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "request_id", "tenant_id", "status", "result_ciphertext", "result_hash",
					"content_type", "completed_at", "reason_code",
				}).AddRow("task-9", "req-1", "tenant-1", "completed", tc.ciphertext, "reqhash-9",
					"application/json", time.Now(), ""))

			src := NewPGSource(mock, tc.kr)
			r, found, err := src.Get(context.Background(), "sess-1", "req-1")
			if err == nil {
				t.Fatalf("must fail closed, got r=%+v found=%v", r, found)
			}
		})
	}
}

// TestPGSource_GetLatest：按最近更新时间取会话最新任务并解密。
func TestPGSource_GetLatest(t *testing.T) {
	kr := testAADKeyring(t)
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-7", RequestHash: "reqhash-7"}
	env, _, err := secret.EncryptWithAAD([]byte("latest-body"), kr, secret.AADDomainDurableResult, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, result_ciphertext, result_hash, content_type, completed_at, reason_code`).
		WithArgs("sess-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "result_ciphertext", "result_hash",
			"content_type", "completed_at", "reason_code",
		}).AddRow("task-7", "req-1", "tenant-1", "completed", env, "reqhash-7",
			"application/json", time.Now(), ""))

	src := NewPGSource(mock, kr)
	r, rid, found, err := src.GetLatest(context.Background(), "sess-1")
	if err != nil || !found {
		t.Fatalf("GetLatest: found=%v err=%v", found, err)
	}
	if r.Body != "latest-body" || rid != "req-1" {
		t.Fatalf("r=%+v rid=%q", r, rid)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestPGSource_RealPostgresFallback verifies the Wave 1 read path against a
// real PostgreSQL parser and transaction. Wave 2 owns the permanent schema, so
// this test creates a transaction-local table with exactly the columns queried
// by PGSource and always rolls the transaction back.
func TestPGSource_RealPostgresFallback(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL fallback integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && err != pgx.ErrTxClosed {
			t.Errorf("rollback integration transaction: %v", err)
		}
	}()

	_, err = tx.Exec(ctx, `
		CREATE TEMP TABLE durable_llm_tasks (
			id text PRIMARY KEY,
			request_id text NOT NULL,
			session_id text NOT NULL,
			tenant_id text NOT NULL,
			status text NOT NULL,
			result_ciphertext text,
			result_hash text NOT NULL,
			content_type text NOT NULL DEFAULT '',
			completed_at timestamptz,
			reason_code text NOT NULL DEFAULT '',
			updated_at timestamptz NOT NULL
		) ON COMMIT DROP
	`)
	if err != nil {
		t.Fatalf("create transaction-local durable task table: %v", err)
	}

	kr := testAADKeyring(t)
	completedAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	binding := secret.AADBinding{TenantID: "tenant-real", TaskID: "task-completed", RequestHash: "hash-completed"}
	ciphertext, _, err := secret.EncryptWithAAD(
		[]byte(`{"result":"from-postgres"}`),
		kr,
		secret.AADDomainDurableResult,
		binding,
	)
	if err != nil {
		t.Fatalf("encrypt completed result: %v", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO durable_llm_tasks
			(id, request_id, session_id, tenant_id, status, result_ciphertext,
			 result_hash, content_type, completed_at, reason_code, updated_at)
		VALUES
			($1, $2, $3, $4, 'completed', $5, $6, 'application/json', $7, '', $7),
			('task-running', 'req-running', 'sess-real', 'tenant-real', 'retry_scheduled',
			 NULL, 'hash-running', '', NULL, 'waiting_recovery', $8)
	`, binding.TaskID, "req-completed", "sess-real", binding.TenantID, ciphertext,
		binding.RequestHash, completedAt, completedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("seed transaction-local durable tasks: %v", err)
	}

	store := NewStoreWithFallback(nil, 0, NewPGSource(tx, kr))
	completed, found, err := store.Get(ctx, "sess-real", "req-completed")
	if err != nil || !found {
		t.Fatalf("completed fallback: found=%v err=%v", found, err)
	}
	if completed.Status != StatusCompleted || completed.Body != `{"result":"from-postgres"}` {
		t.Fatalf("completed fallback response = %+v", completed)
	}
	if completed.ContentType != "application/json" || completed.CompletedAt != completedAt.Unix() {
		t.Fatalf("completed fallback metadata = %+v", completed)
	}

	running, found, err := store.Get(ctx, "sess-real", "req-running")
	if err != nil || !found {
		t.Fatalf("in-progress fallback: found=%v err=%v", found, err)
	}
	if running.Status != StatusInProgress || running.Body != "" {
		t.Fatalf("in-progress fallback response = %+v", running)
	}

	latest, requestID, found, err := store.GetLatest(ctx, "sess-real")
	if err != nil || !found {
		t.Fatalf("latest fallback: found=%v err=%v", found, err)
	}
	if requestID != "req-running" || latest.Status != StatusInProgress {
		t.Fatalf("latest fallback: requestID=%q response=%+v", requestID, latest)
	}

	missing, found, err := store.Get(ctx, "sess-real", "req-missing")
	if err != nil || found || missing != nil {
		t.Fatalf("missing fallback: response=%+v found=%v err=%v", missing, found, err)
	}
}
