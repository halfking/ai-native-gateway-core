package main

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// 2026-11-01 12h 审计轮：补发工具的**失败计数**。
//
// ★ 背景 ★：原实现对每类失败（build / marshal / insert）只 log.Printf 后
// continue，收尾行只打印 inserted / skipped ⇒ ⇒ 失败既不在数字里、也不影响
// 退出码 ⇒ ⇒ 工具可以「打印 done: inserted=100」而实际 9999 条全失败。
//
// ★ 这组用例钉住「失败必须被计数」★

// TestInsertChunkCountsSuccessfulInserts 钉住成功路径的计数。
func TestInsertChunkCountsSuccessfulInserts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO outbox_events").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	inserted, skipped, failed := insertChunk(db, []cand{{
		tenantID:   "tenant-1",
		sessionID:  "sess-1",
		userID:     "user-1",
		occurredAt: time.Now().UTC(),
	}})
	if inserted != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("inserted=%d skipped=%d failed=%d，期望 1/0/0", inserted, skipped, failed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestInsertChunkCountsFailures 钉住失败路径的计数：
// ★ 一条 INSERT 失败 ⇒ failed=1 ⇒ 收尾行会显示它 ⇒ 退出码会非零 ★。
func TestInsertChunkCountsFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO outbox_events").
		WillReturnError(errors.New("duplicate key value violates unique constraint"))
	mock.ExpectCommit()

	inserted, skipped, failed := insertChunk(db, []cand{{
		tenantID:   "tenant-1",
		sessionID:  "sess-1",
		userID:     "user-1",
		occurredAt: time.Now().UTC(),
	}})
	if inserted != 0 || skipped != 0 || failed != 1 {
		t.Fatalf("inserted=%d skipped=%d failed=%d，期望 0/0/1（失败必须被计数）", inserted, skipped, failed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestInsertChunkCountsSkippedConflicts 钉住「已存在」的计数：
// ON CONFLICT DO NOTHING ⇒ rowsAffected=0 ⇒ skipped（不是 failed）。
func TestInsertChunkCountsSkippedConflicts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO outbox_events").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	inserted, skipped, failed := insertChunk(db, []cand{{
		tenantID:   "tenant-1",
		sessionID:  "sess-1",
		userID:     "user-1",
		occurredAt: time.Now().UTC(),
	}})
	if inserted != 0 || skipped != 1 || failed != 0 {
		t.Fatalf("inserted=%d skipped=%d failed=%d，期望 0/1/0", inserted, skipped, failed)
	}
}
