package bg

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// R46 F6: 行级保留期 worker 的删除边界——request_logs 按 ts 逐行清理；
// sessions/turns 以会话为单位（超龄会话连同全部 turns 删除，活跃会话的
// 历史轮次保留）。
func TestLiteRetentionWorker_RunOnceDeletesExpiredRows(t *testing.T) {
	db, err := sqlite.OpenSQLite(":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable in this build: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	old := now - 8*24*3600 // 8 天前（默认 7d 保留之外）
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	// 旧行 + 新行（request_logs）
	mustExec(`INSERT INTO request_logs (request_id, tenant_id, session_id, ts, method, path) VALUES
		('req-old', 't1', 'sess-old', ?, 'POST', '/v1/chat'),
		('req-new', 't1', 'sess-new', ?, 'POST', '/v1/chat')`, old, now)
	// 超龄会话（2 turns）+ 活跃会话（1 个超龄 turn 仍要保留）
	mustExec(`INSERT INTO sessions (id, tenant_id, created_at, updated_at) VALUES
		('sess-old', 't1', ?, ?),
		('sess-new', 't1', ?, ?)`, old, old, now, now)
	mustExec(`INSERT INTO session_turns (tenant_id, session_id, turn_no, ts) VALUES
		('t1', 'sess-old', 1, ?),
		('t1', 'sess-old', 2, ?),
		('t1', 'sess-new', 1, ?)`, old, old, old)

	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	logsDel, sessDel, turnsDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if logsDel != 1 {
		t.Fatalf("expected 1 old request_log deleted, got %d", logsDel)
	}
	if sessDel != 1 {
		t.Fatalf("expected 1 old session deleted, got %d", sessDel)
	}
	if turnsDel != 2 {
		t.Fatalf("expected 2 turns of the expired session deleted, got %d", turnsDel)
	}

	// 边界核对：新行/活跃会话/活跃会话的超龄 turn 全部保留。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected 1 remaining request_log, got %d (err=%v)", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id='sess-new'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected active session to survive, got %d (err=%v)", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turns WHERE session_id='sess-new'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected active session's old turn to survive, got %d (err=%v)", n, err)
	}
}

// 幂等：第二跑零删除、零报错。
func TestLiteRetentionWorker_RunOnceIdempotent(t *testing.T) {
	db, err := sqlite.OpenSQLite(":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable in this build: %v", err)
	}
	defer db.Close()

	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, tenant_id, session_id, ts, method, path) VALUES
		('req-old', 't1', NULL, ?, 'POST', '/v1/chat')`, old); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	if _, _, _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	logsDel, sessDel, turnsDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if logsDel != 0 || sessDel != 0 || turnsDel != 0 {
		t.Fatalf("expected second sweep to be a no-op, got logs=%d sessions=%d turns=%d",
			logsDel, sessDel, turnsDel)
	}
}

// R47：清理期间被新写入复活的超龄会话必须整体豁免（会话与全部 turns
// 都保留）——updated_at 谓词在删除时刻求值，而非沿用 sweep 开始时的
// 收集快照（旧实现缝隙内复活会丢光历史 turns）。
func TestLiteRetentionWorker_RevivedSessionKeepsTurns(t *testing.T) {
	db, err := sqlite.OpenSQLite(":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable in this build: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	old := now - 8*24*3600
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO sessions (id, tenant_id, created_at, updated_at) VALUES ('sess-old', 't1', ?, ?)`, old, old)
	mustExec(`INSERT INTO session_turns (tenant_id, session_id, turn_no, ts) VALUES ('t1', 'sess-old', 1, ?)`, old)
	mustExec(`INSERT INTO request_logs (request_id, tenant_id, ts, method, path) VALUES ('req-old', 't1', ?, 'POST', '/v1/chat')`, old)

	// 复活：updated_at 刷到当下（等价于 sweep 缝隙内新 turn 写入的 bump）。
	mustExec(`UPDATE sessions SET updated_at = ? WHERE id = 'sess-old'`, now)

	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	logsDel, sessDel, turnsDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if sessDel != 0 || turnsDel != 0 {
		t.Fatalf("revived session must be exempt entirely, got sessions=%d turns=%d", sessDel, turnsDel)
	}
	if logsDel != 1 {
		t.Fatalf("expected old request_log still swept, got %d", logsDel)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turns WHERE session_id='sess-old'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected revived session's turns to survive, got %d (err=%v)", n, err)
	}
}
