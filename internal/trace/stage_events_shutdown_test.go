package trace

// 2026-10-17：关停态（连接池已关闭）不得被渲染成 stage events 留存故障。
//
// # 与 URSM 快照留存同一个根因
//
//	cmd/gateway/main.go 早期注册的 defer 在 main **返回那一刻**关掉共享池，
//	而关停 goroutine 此时可能还在排空后台 worker
//	⇒ 每次正常部署都会产出一条 "cleanup failed (non-fatal)" 的 WARN。
//
// 生产证据（2026-10-15 18:28:17.853460+08，245 canary-8781）：
//
//	18:28:17.853460  WARN trace: stage events retention cleanup failed (non-fatal)  error=closed pool
//	18:28:17.853705  INFO materialized_view_refresher stopped   ← 证明处在关停序列中
//
// 留存并没有坏（下一实例启动时会补做），所以这是**噪声冒充故障**。
//
// ★ 负向对照是本文件成立的前提：TestStageEventsRetentionRealErrorStillWarns ——
// 真错误必须继续 Warn。没有它，「把任何失败都归为关停」能骗过全部关停用例，
// 而那正是把真故障静音的形状。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/puddle/v2"
)

func captureStageEventsLogs(t *testing.T) func() []map[string]any {
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
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("capture buffer holds a non-JSON line %q: %v", line, err)
			}
			recs = append(recs, rec)
		}
		return recs
	}
}

func stageEventsWorker(t *testing.T, db *fakeStageEventsRetentionDB) *StageEventsRetentionWorker {
	t.Helper()
	w := NewStageEventsRetentionWorker(nil, DefaultStageEventsRetentionConfig())
	w.db = db
	return w
}

// TestStageEventsRetentionShutdownIsNotWarned：关停态必须降到 Debug，
// 且**明确打一行关停标记**（只断言「没有 WARN」的话，把 CleanupOnce 删掉也能过）。
func TestStageEventsRetentionShutdownIsNotWarned(t *testing.T) {
	db := &fakeStageEventsRetentionDB{execErr: puddle.ErrClosedPool}
	w := stageEventsWorker(t, db)
	logs := captureStageEventsLogs(t)

	w.CleanupOnce(context.Background())

	var marker map[string]any
	for _, rec := range logs() {
		if rec["level"] == "WARN" {
			t.Fatalf("shutdown must not log at WARN; got msg=%v", rec["msg"])
		}
		if rec["msg"] == "trace: stage events retention skipped on shutdown; database pool already closed" {
			marker = rec
		}
	}
	if marker == nil {
		t.Fatalf("the shutdown marker must be emitted; got records %v", logs())
	}
	if marker["level"] != "DEBUG" {
		t.Fatalf("level = %v, want DEBUG", marker["level"])
	}
	if marker["retention"] != DefaultStageEventsRetentionConfig().Retention.String() {
		t.Fatalf("retention = %v, want %s",
			marker["retention"], DefaultStageEventsRetentionConfig().Retention.String())
	}
}

// TestStageEventsRetentionRealErrorStillWarns 是**阴性对照**：
// 任何非关停的错误必须继续 Warn —— 否则「关停」成了吞错的万能借口。
func TestStageEventsRetentionRealErrorStillWarns(t *testing.T) {
	db := &fakeStageEventsRetentionDB{
		execErr: errors.New("canceling statement due to statement timeout"),
	}
	w := stageEventsWorker(t, db)
	logs := captureStageEventsLogs(t)

	w.CleanupOnce(context.Background())

	var saw bool
	for _, rec := range logs() {
		if rec["level"] == "WARN" &&
			rec["msg"] == "trace: stage events retention cleanup failed (non-fatal)" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("a genuine failure must still WARN; got records %v", logs())
	}
}

// TestStageEventsRetentionSuccessStillLogs 补一条正向对照：修完之后，
// 正常删到东西的那条 Info 不能被顺手改掉。
func TestStageEventsRetentionSuccessStillLogs(t *testing.T) {
	db := &fakeStageEventsRetentionDB{batchResults: []int64{12}}
	w := stageEventsWorker(t, db)
	logs := captureStageEventsLogs(t)

	w.CleanupOnce(context.Background())

	var saw bool
	for _, rec := range logs() {
		if rec["msg"] == "trace: stage events retention removed expired rows" && rec["deleted"] == float64(12) {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("a successful round must still log the deletion; got records %v", logs())
	}
}
