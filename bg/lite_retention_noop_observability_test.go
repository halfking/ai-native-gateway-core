package bg

// 2026-10-18：lite 行级留存「零删除」这一轮的可观测性门。
//
// # 这道门保护的不变式
//
// LiteRetentionWorker 每 6 小时跑一次，默认保留期 7 天。稳态下
// **绝大多数轮次没有任何东西过期**，RunOnce 的总删除数必然为 0。
// 旧实现只在 total > 0 时打 "lite retention sweep"，于是
// 「worker 活着、无事可做」与「worker 根本没在跑」渲染成同一种沉默。
// 本 worker 无任何指标通道（全仓无 Prometheus 埋点），这几行 slog 就是
// 它唯一的可观测面——沉默即失明。
//
// # 触发这次修复的真实场景
//
// 2026-10-18 生产取证：待办「bg lite retention 是否真在跑」以
// **零日志**呈现。结论是它结构上不该跑——该 worker 只在
// cmd/gateway/storage_mode_init.go 的 initLiteStorageMode 内装配
//（329 行起，418 行处 `f.SQLiteDB() != nil` 才启），而 245 的 .env 与
// **运行中进程**环境都是 LLM_GATEWAY_STORAGE_MODE=full，两个日志文件
// 命中 0。也就是说「零日志」这次恰好是对的，但仍是无判据的巧合：
// 同样的零日志出现在某台真跑 lite 的机器上，现有代码无法自证。
//
// # 为什么断言打在「运行期日志记录」上而不是源码文本
//
// 源码文本断言会被注释里的字喂饱——文件头与 RunOnce 末尾的说明都写着
// "noop"/"mode"，用 not.toContain 读源码必然自曝（2026-10-14 批 116 的
// 同款教训）。日志记录是执行事实，注释改不掉它。
//
// # 为什么两个分支都要覆盖
//
// 只测「零删除时出声」会被一个「无论删没删都打同一行、mode 恒为 noop」
// 的实现骗过（判据变绿而 mode 语义已死）。故正侧断言 mode=="reclaimed"
// 且计数非零——它证明 mode 不是恒值常量。

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// captureSweepLogs 捕获运行期 slog 输出并返回解码 getter。
//
// getter 而非 Cleanup 回填：Cleanup 在测试体之后才跑，那样测试体读到
// 永远是 nil，判据会以「拿不到记录」的方式恒红，而失败原因看起来像
// 被测代码没打日志——量具自伤比假绿更难查。
func captureSweepLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return func() []map[string]any {
		var recs []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("decode slog line %q: %v", line, err)
			}
			recs = append(recs, m)
		}
		return recs
	}
}

func sweepsOf(recs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["msg"] == "lite retention sweep" {
			out = append(out, r)
		}
	}
	return out
}

func numOf(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("sweep record missing %q: %v", key, m)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("sweep record %q is %T, want number", key, v)
	}
	return f
}

// TestLiteRetentionSweepSpeaksOnZeroDeletion：表非空、但全都不超龄 ⇒ 仍必须出声。
func TestLiteRetentionSweepSpeaksOnZeroDeletion(t *testing.T) {
	db, err := sqlite.OpenSQLite(":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable in this build: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	// 前提仍在场：表非空。若这里建不出行，下面「零删除」就成了
	// 「对空表零删除」，判据会在被测逻辑整个不跑时照样变绿。
	mustExec(`INSERT INTO request_logs (request_id, tenant_id, session_id, ts, method, path) VALUES
		('req-fresh', 't1', 'sess-fresh', ?, 'POST', '/v1/chat')`, now)
	mustExec(`INSERT INTO sessions (id, tenant_id, created_at, updated_at) VALUES
		('sess-fresh', 't1', ?, ?)`, now, now)

	// 前提仍在场（正向）：这些行确实都还在。
	var live int
	if err := db.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&live); err != nil {
		t.Fatalf("precondition count: %v", err)
	}
	if live != 1 {
		t.Fatalf("precondition: want 1 fresh request_logs row, got %d", live)
	}

	logs := captureSweepLogs(t)
	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	delLogs, delSess, delTurns, delDetails, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if delLogs+delSess+delTurns+delDetails != 0 {
		t.Fatalf("precondition broken: expected zero deletions on fresh data, got %d/%d/%d/%d",
			delLogs, delSess, delTurns, delDetails)
	}

	recs := sweepsOf(logs())
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 sweep record on a zero-deletion round, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	if got := rec["mode"]; got != "noop" {
		t.Errorf("mode = %v, want \"noop\" on a round that reclaimed nothing", got)
	}
	for _, k := range []string{
		"request_logs_deleted", "sessions_deleted",
		"session_turns_deleted", "session_turn_details_deleted",
	} {
		if v := numOf(t, rec, k); v != 0 {
			t.Errorf("%s = %v, want 0 on a noop round", k, v)
		}
	}
	// retention/interval 必须随轮次带出，否则运维看到 noop 也无法判断
	// 「无事可做」的前提（按哪个 TTL、每 6h 还是别的周期判的）。
	if rec["retention"] != (7 * 24 * time.Hour).String() {
		t.Errorf("retention = %v, want %v", rec["retention"], (7 * 24 * time.Hour).String())
	}
	if rec["interval"] != defaultLiteRetentionInterval.String() {
		t.Errorf("interval = %v, want %v", rec["interval"], defaultLiteRetentionInterval.String())
	}
}

// TestLiteRetentionSweepModeIsNotConstant：正侧钉 mode 语义真实，
// 防「恒为 noop / 恒为 reclaimed」的退化实现蒙混过关。
func TestLiteRetentionSweepModeIsNotConstant(t *testing.T) {
	db, err := sqlite.OpenSQLite(":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable in this build: %v", err)
	}
	defer db.Close()

	old := time.Now().Unix() - 8*24*3600 // 8 天前，7d 保留期之外
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO request_logs (request_id, tenant_id, session_id, ts, method, path) VALUES
		('req-old', 't1', 'sess-old', ?, 'POST', '/v1/chat')`, old)

	logs := captureSweepLogs(t)
	w := NewLiteRetentionWorker(db, 7*24*time.Hour)
	if _, _, _, _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	recs := sweepsOf(logs())
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 sweep record, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	if got := rec["mode"]; got != "reclaimed" {
		t.Errorf("mode = %v, want \"reclaimed\" when rows were actually deleted", got)
	}
	if v := numOf(t, rec, "request_logs_deleted"); v < 1 {
		t.Errorf("request_logs_deleted = %v, want ≥1 on a reclaiming round", v)
	}
}
