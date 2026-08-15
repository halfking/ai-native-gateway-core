package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

func encryptedOutboxResult(t *testing.T, taskID, requestHash, body string) (string, string) {
	t.Helper()
	kr := testKeyring(t)
	envelope, _, err := secret.EncryptWithAAD([]byte(body), kr, secret.AADDomainDurableResult,
		secret.AADBinding{TenantID: "tenant-1", TaskID: taskID, RequestHash: requestHash})
	if err != nil {
		t.Fatalf("encrypt result: %v", err)
	}
	hash := sha256.Sum256([]byte(body))
	return envelope, hex.EncodeToString(hash[:])
}

func outboxRows(taskID, envelope, resultHash string, now time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"task_id", "result_version", "tenant_id", "request_id", "session_id", "status",
		"result_ciphertext", "request_hash", "result_hash", "content_type", "reason_code",
		"fencing_token", "completed_at", "expires_at",
	}).AddRow(taskID, int64(1), "tenant-1", "req-1", "sess-1", "completed",
		envelope, "request-hash", resultHash, "application/json", "", int64(4), now, now.Add(time.Hour))
}

func TestStore_ProjectPendingOutbox_Completed(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()
	envelope, resultHash := encryptedOutboxResult(t, "task-1", "request-hash", `{"ok":true}`)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM durable_pending_outbox`).
		WithArgs(8, now).
		WillReturnRows(outboxRows("task-1", envelope, resultHash, now))
	mock.ExpectExec(`DELETE FROM durable_pending_outbox`).
		WithArgs("task-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	mr := miniredis.RunT(t)
	pendingStore := pending.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	count, err := store.ProjectPendingOutbox(context.Background(), pendingStore, 8, now)
	if err != nil || count != 1 {
		t.Fatalf("project: count=%d err=%v", count, err)
	}
	got, found, err := pendingStore.Get(context.Background(), "sess-1", "req-1")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Body != `{"ok":true}` || got.TaskID != "task-1" || got.ResultVersion != 1 || got.ResultHash != resultHash {
		t.Fatalf("projected = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStore_ProjectPendingOutbox_HashMismatchRetried(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()
	envelope, _ := encryptedOutboxResult(t, "task-1", "request-hash", "body")

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM durable_pending_outbox`).
		WithArgs(8, now).
		WillReturnRows(outboxRows("task-1", envelope, "wrong-hash", now))
	mock.ExpectExec(`UPDATE durable_pending_outbox`).
		WithArgs("task-1", "result hash mismatch", now.Add(time.Minute)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	mr := miniredis.RunT(t)
	pendingStore := pending.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	count, err := store.ProjectPendingOutbox(context.Background(), pendingStore, 8, now)
	if err != nil || count != 0 {
		t.Fatalf("project: count=%d err=%v", count, err)
	}
	if mr.Exists("pending_response:sess-1:req-1") {
		t.Fatal("hash mismatch must not project unverified body")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// durable_* 投影观测契约（doc 18 §15.3）：成功投递按终态计数，失败按原因
// 计数——对比 terminal 转换可探测投影丢失。
func gatherProjectionMetric(t *testing.T, name, labelValue string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == labelValue && m.GetCounter() != nil {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestStore_ProjectPendingOutbox_Metrics(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()
	envelope, resultHash := encryptedOutboxResult(t, "task-metrics", "request-hash", `{"ok":true}`)
	projectedBefore := gatherProjectionMetric(t, "durable_pending_projections_total", "completed")

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM durable_pending_outbox`).
		WithArgs(8, now).
		WillReturnRows(outboxRows("task-metrics", envelope, resultHash, now))
	mock.ExpectExec(`DELETE FROM durable_pending_outbox`).
		WithArgs("task-metrics").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	mr := miniredis.RunT(t)
	pendingStore := pending.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	if _, err := store.ProjectPendingOutbox(context.Background(), pendingStore, 8, now); err != nil {
		t.Fatalf("project: %v", err)
	}
	if got := gatherProjectionMetric(t, "durable_pending_projections_total", "completed"); got < projectedBefore+1 {
		t.Fatalf("durable_pending_projections_total{completed} = %v, want >= %v", got, projectedBefore+1)
	}
}

func TestStore_ProjectPendingOutbox_ErrorMetricOnHashMismatch(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now()
	envelope, _ := encryptedOutboxResult(t, "task-errmetrics", "request-hash", "body")
	errBefore := gatherProjectionMetric(t, "durable_pending_projection_errors_total", "result_hash")

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM durable_pending_outbox`).
		WithArgs(8, now).
		WillReturnRows(outboxRows("task-errmetrics", envelope, "wrong-hash", now))
	mock.ExpectExec(`UPDATE durable_pending_outbox`).
		WithArgs("task-errmetrics", "result hash mismatch", now.Add(time.Minute)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	mr := miniredis.RunT(t)
	pendingStore := pending.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	if _, err := store.ProjectPendingOutbox(context.Background(), pendingStore, 8, now); err != nil {
		t.Fatalf("project: %v", err)
	}
	if got := gatherProjectionMetric(t, "durable_pending_projection_errors_total", "result_hash"); got < errBefore+1 {
		t.Fatalf("durable_pending_projection_errors_total{result_hash} = %v, want >= %v", got, errBefore+1)
	}
}
