package pending

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func resultSHA256(body string) string {
	hash := sha256.Sum256([]byte(body))
	return hex.EncodeToString(hash[:])
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
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, result_ciphertext,\s*request_hash, result_hash, content_type, completed_at, reason_code,\s*fencing_token, result_version`).
		WithArgs("sess-1", "req-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "result_ciphertext", "request_hash", "result_hash",
			"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
		}).AddRow("task-9", "req-1", "tenant-1", "completed", env, "reqhash-9", resultSHA256(`{"result":"body"}`),
			"application/json", now, "", int64(3), int64(1)))

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
		{"resume_safety_blocked", "resume_safety_blocked", StatusFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.dbStatus, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			t.Cleanup(mock.Close)
			mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, result_ciphertext,\s*request_hash, result_hash, content_type, completed_at, reason_code,\s*fencing_token, result_version`).
				WithArgs("sess-1", "req-1").
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "request_id", "tenant_id", "status", "result_ciphertext", "request_hash", "result_hash",
					"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
				}).AddRow("task-9", "req-1", "tenant-1", tc.dbStatus, nil, "reqhash-9", "",
					"", nil, tc.reason, int64(3), nil))

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
		resultHash string
	}{
		{"nil keyring", nil, env, resultSHA256("body")},
		{"tampered ciphertext", kr, env[:len(env)-4] + "AAAA", resultSHA256("body")},
		{"result hash mismatch", kr, env, "wrong-hash"},
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
					"id", "request_id", "tenant_id", "status", "result_ciphertext", "request_hash", "result_hash",
					"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
				}).AddRow("task-9", "req-1", "tenant-1", "completed", tc.ciphertext, "reqhash-9", tc.resultHash,
					"application/json", time.Now(), "", int64(3), int64(1)))

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
	mock.ExpectQuery(`SELECT id, request_id, tenant_id, status, result_ciphertext,\s*request_hash, result_hash, content_type, completed_at, reason_code,\s*fencing_token, result_version`).
		WithArgs("sess-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "request_id", "tenant_id", "status", "result_ciphertext", "request_hash", "result_hash",
			"content_type", "completed_at", "reason_code", "fencing_token", "result_version",
		}).AddRow("task-7", "req-1", "tenant-1", "completed", env, "reqhash-7", resultSHA256("latest-body"),
			"application/json", time.Now(), "", int64(4), int64(2)))

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
