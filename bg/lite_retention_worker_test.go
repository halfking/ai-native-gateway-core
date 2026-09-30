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
	logsDel, sessDel, turnsDel, detDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if detDel != 0 {
		t.Fatalf("expected 0 details deleted (fixture has none), got %d", detDel)
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
	if _, _, _, _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	logsDel, sessDel, turnsDel, detDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if logsDel != 0 || sessDel != 0 || turnsDel != 0 || detDel != 0 {
		t.Fatalf("expected second sweep to be a no-op, got logs=%d sessions=%d turns=%d details=%d",
			logsDel, sessDel, turnsDel, detDel)
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
	logsDel, sessDel, turnsDel, detDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if sessDel != 0 || turnsDel != 0 || detDel != 0 {
		t.Fatalf("revived session must be exempt entirely, got sessions=%d turns=%d details=%d",
			sessDel, turnsDel, detDel)
	}
	if logsDel != 1 {
		t.Fatalf("expected old request_log still swept, got %d", logsDel)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turns WHERE session_id='sess-old'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected revived session's turns to survive, got %d (err=%v)", n, err)
	}
}

// R78：session_turn_details 必须与 session_turns 一起被清理。
//
// 判据是**语义量**不是代理量：该表与 session_turns 之间无外键
// （schema.go:56-76 的 PRIMARY KEY/UNIQUE 均不含 FK），所以「删掉会话」
// 不会连带删掉特征行。缺陷形态正是「会话和 turns 都没了、details 全留着」，
// 因此本用例同时钉住两点：(1) 超龄会话的 details 被删干净；(2) 活跃会话
// 的 details 仍在。若只断言「行数变化」，一个按 ts 而非按会话清理、或按
// 错误租户清理的实现也能凑出相同的总数——所以活跃侧必须逐行核对。
func TestLiteRetentionWorker_DetailsFollowsSessionRetention(t *testing.T) {
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
			t.Fatalf("exec %s: %v", q, args)
		}
	}
	mustExec(`INSERT INTO sessions (id, tenant_id, created_at, updated_at) VALUES
		('sess-old', 't1', ?, ?),
		('sess-new', 't1', ?, ?)`, old, old, now, now)
	mustExec(`INSERT INTO session_turns (tenant_id, session_id, turn_no, ts) VALUES
		('t1', 'sess-old', 1, ?),
		('t1', 'sess-old', 2, ?),
		('t1', 'sess-new', 1, ?)`, old, old, now)
	mustExec(`INSERT INTO session_turn_details
		(tenant_id, session_id, turn_no, request_id, ts, model) VALUES
		('t1', 'sess-old', 1, 'req-old-1', ?, 'gpt-x'),
		('t1', 'sess-old', 2, 'req-old-2', ?, 'gpt-x'),
		('t1', 'sess-new', 1, 'req-new-1', ?, 'gpt-x')`, old, old, now)

	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	_, sessDel, turnsDel, detDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if sessDel != 1 || turnsDel != 2 {
		t.Fatalf("sanity: expected 1 session + 2 turns deleted, got sessions=%d turns=%d", sessDel, turnsDel)
	}
	if detDel != 2 {
		t.Fatalf("expected 2 details rows of the expired session deleted, got %d", detDel)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turn_details WHERE session_id='sess-old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired session must leave no orphaned details, got %d (err=%v)", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turn_details WHERE session_id='sess-new'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("active session's details must survive, got %d (err=%v)", n, err)
	}
}

// 存量孤儿必须被回收：修复前已产生的「会话行已删、特征行留下」组合，
// 永远等不到可匹配的 sessions 行，只补 EXISTS 谓词清不掉它们。
//
// 判据钉住「存活的那一行是谁」而不是行数——一个按 ts 而非按孤儿关系
// 清理的实现，在本夹具里恰好也是删 1 行，断言必须能分辨二者。
func TestLiteRetentionWorker_ReclaimsOrphanDetails(t *testing.T) {
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
			t.Fatalf("exec %s: %v", q, args)
		}
	}
	// sess-gone 没有任何 sessions 行：这就是修复前遗留的孤儿形态。
	mustExec(`INSERT INTO sessions (id, tenant_id, created_at, updated_at)
		VALUES ('sess-live', 't1', ?, ?)`, now, now)
	mustExec(`INSERT INTO session_turn_details
		(tenant_id, session_id, turn_no, request_id, ts) VALUES
		('t1', 'sess-gone', 1, 'req-orphan', ?),
		('t1', 'sess-live', 1, 'req-live', ?)`, old, now)

	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	_, _, _, detDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if detDel != 1 {
		t.Fatalf("expected the orphaned details row reclaimed, got %d", detDel)
	}
	var remaining string
	if err := db.QueryRow(`SELECT request_id FROM session_turn_details`).Scan(&remaining); err != nil {
		t.Fatalf("expected exactly one surviving row: %v", err)
	}
	if remaining != "req-live" {
		t.Fatalf("survivor must be the live session's row req-live, got %q", remaining)
	}
}

// 近期孤儿不得被回收：ts 谓词是安全边界。write-side 不变量保证 details
// 必有 session 行，但若写序将来变化，这条兜底最多漏删、绝不能误删新数据。
func TestLiteRetentionWorker_KeepsRecentOrphanDetails(t *testing.T) {
	db, err := sqlite.OpenSQLite(":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable in this build: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO session_turn_details
		(tenant_id, session_id, turn_no, request_id, ts)
		VALUES ('t1', 'sess-gone', 1, 'req-fresh', ?)`, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	_, _, _, detDel, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if detDel != 0 {
		t.Fatalf("recent orphan must not be reclaimed, got %d", detDel)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turn_details`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected the recent orphan to survive, got %d (err=%v)", n, err)
	}
}
