package pending

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
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
	body := []byte(`{"result":"body"}`)
	requestHash := "request-hash-9"
	resultHash := fmt.Sprintf("%x", sha256.Sum256(body))
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-9", RequestHash: requestHash}
	env, keyID, err := secret.EncryptWithAAD(body, kr, secret.AADDomainDurableResult, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, request_hash, result_ciphertext, result_hash, content_type, completed_at, reason_code, fencing_token, result_version, next_retry_at, attempt_count, expires_at`).
		WithArgs("sess-1", "req-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "request_hash", "result_ciphertext", "result_hash",
			"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
			"next_retry_at", "attempt_count", "expires_at",
		}).AddRow("task-9", "req-1", "tenant-1", "completed", requestHash, env, resultHash,
			"application/json", now, nil, int64(7), int64(3), nil, 4, now.Add(time.Hour)))

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
	if r.RequestHash != requestHash || r.ResultHash != resultHash {
		t.Fatalf("hashes = request:%q result:%q", r.RequestHash, r.ResultHash)
	}
	if r.TaskID != "task-9" || r.FencingToken != 7 || r.ResultVersion != 3 || r.AttemptCount != 4 || !r.Durable {
		t.Fatalf("durable metadata = %+v", r)
	}
	if r.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("expiresAt = %d, want %d", r.ExpiresAt, now.Add(time.Hour).Unix())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if keyID != "d1" {
		t.Fatalf("keyID = %q, want d1", keyID)
	}
}

// TestPGSource_Get_StatusProjection verifies nullable nonterminal columns and
// the durable terminal-state mapping exposed through the legacy pending API.
func TestPGSource_Get_StatusProjection(t *testing.T) {
	kr := testAADKeyring(t)
	nextRetryAt := time.Date(2026, 8, 15, 12, 5, 0, 0, time.UTC)
	cases := []struct {
		dbStatus   string
		reason     any
		wantStatus Status
		wantReason string
		wantRetry  int64
	}{
		{"running", nil, StatusInProgress, "", 0},
		{"waiting_recovery", nil, StatusInProgress, "", nextRetryAt.Unix()},
		{"retry_scheduled", nil, StatusInProgress, "", nextRetryAt.Unix()},
		{"permanent_failed", "offers_exhausted", StatusFailed, "offers_exhausted", 0},
		{"expired", "survival_expired", StatusFailed, "survival_expired", 0},
		{"cancelled", "client_cancelled", StatusFailed, "client_cancelled", 0},
		{"resume_safety_blocked", "semantic_commit", StatusFailed, "semantic_commit", 0},
	}
	for _, tc := range cases {
		t.Run(tc.dbStatus, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			t.Cleanup(mock.Close)
			var retry any
			if tc.wantRetry != 0 {
				retry = nextRetryAt
			}
			mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, request_hash, result_ciphertext, result_hash, content_type, completed_at, reason_code, fencing_token, result_version, next_retry_at, attempt_count, expires_at`).
				WithArgs("sess-1", "req-1").
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "request_id", "tenant_id", "status", "request_hash", "result_ciphertext", "result_hash",
					"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
					"next_retry_at", "attempt_count", "expires_at",
				}).AddRow("task-9", "req-1", "tenant-1", tc.dbStatus, "request-hash-9", nil, nil,
					nil, nil, tc.reason, int64(7), int64(2), retry, 3, nil))

			src := NewPGSource(mock, kr)
			r, found, err := src.Get(context.Background(), "sess-1", "req-1")
			if err != nil || !found {
				t.Fatalf("Get: found=%v err=%v", found, err)
			}
			if r.Status != tc.wantStatus || r.ErrorMessage != tc.wantReason || r.NextRetryAt != tc.wantRetry {
				t.Fatalf("response = %+v", r)
			}
			if r.Body != "" || r.ContentType != "" || r.ResultHash != "" {
				t.Fatalf("nullable non-result fields must remain empty: %+v", r)
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
	body := []byte("body")
	requestHash := "request-hash-9"
	resultHash := fmt.Sprintf("%x", sha256.Sum256(body))
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-9", RequestHash: requestHash}
	env, _, err := secret.EncryptWithAAD(body, kr, secret.AADDomainDurableResult, binding)
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
			mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, request_hash`).
				WithArgs("sess-1", "req-1").
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "request_id", "tenant_id", "status", "request_hash", "result_ciphertext", "result_hash",
					"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
					"next_retry_at", "attempt_count", "expires_at",
				}).AddRow("task-9", "req-1", "tenant-1", "completed", requestHash, tc.ciphertext, resultHash,
					"application/json", time.Now(), nil, int64(1), int64(1), nil, 1, nil))

			src := NewPGSource(mock, tc.kr)
			r, found, err := src.Get(context.Background(), "sess-1", "req-1")
			if err == nil {
				t.Fatalf("must fail closed, got r=%+v found=%v", r, found)
			}
		})
	}
}

func TestPGSource_Get_ResultHashMismatchFailsClosed(t *testing.T) {
	kr := testAADKeyring(t)
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-9", RequestHash: "request-hash-9"}
	env, _, err := secret.EncryptWithAAD([]byte("body"), kr, secret.AADDomainDurableResult, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, request_hash`).
		WithArgs("sess-1", "req-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "request_hash", "result_ciphertext", "result_hash",
			"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
			"next_retry_at", "attempt_count", "expires_at",
		}).AddRow("task-9", "req-1", "tenant-1", "completed", "request-hash-9", env,
			"0000000000000000000000000000000000000000000000000000000000000000",
			"application/json", time.Now(), nil, int64(1), int64(1), nil, 1, nil))

	r, found, err := NewPGSource(mock, kr).Get(context.Background(), "sess-1", "req-1")
	if err == nil || !strings.Contains(err.Error(), "result hash mismatch") {
		t.Fatalf("Get: r=%+v found=%v err=%v", r, found, err)
	}
}

// TestPGSource_GetLatest：按最近更新时间取会话最新任务并解密。
func TestPGSource_GetLatest(t *testing.T) {
	kr := testAADKeyring(t)
	body := []byte("latest-body")
	requestHash := "request-hash-7"
	resultHash := fmt.Sprintf("%x", sha256.Sum256(body))
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-7", RequestHash: requestHash}
	env, _, err := secret.EncryptWithAAD(body, kr, secret.AADDomainDurableResult, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, request_hash, result_ciphertext, result_hash, content_type, completed_at, reason_code, fencing_token, result_version, next_retry_at, attempt_count, expires_at`).
		WithArgs("sess-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "request_hash", "result_ciphertext", "result_hash",
			"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
			"next_retry_at", "attempt_count", "expires_at",
		}).AddRow("task-7", "req-1", "tenant-1", "completed", requestHash, env, resultHash,
			"application/json", time.Now(), nil, int64(2), int64(1), nil, 1, nil))

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
