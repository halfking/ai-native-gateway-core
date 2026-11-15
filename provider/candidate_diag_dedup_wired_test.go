package provider

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestLogCandidateDiagnosticDbEmptyDedupWired —— 接线级验证（变异臂）：
// 生产出口 logCandidateDiagnostic 真的走了闸——同键 3 连打只出 1 条日志；
// 冷却窗过后再打放行且附 suppressed_repeats。没有这条，闸就只是"存在"
// 而不是"在用"（§「门有牙」纪律）。
func TestLogCandidateDiagnosticDbEmptyDedupWired(t *testing.T) {
	orig := candidateDiagLogGateInstance
	candidateDiagLogGateInstance = newCandidateDiagLogGate(50 * time.Millisecond)
	defer func() { candidateDiagLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	attrs := []any{"model", "gemini-2.5-flash", "tenant_id", "default", "offers_total", 3}
	for i := 0; i < 3; i++ {
		logCandidateDiagnostic("db_empty", attrs...)
	}
	first := buf.String()
	if n := strings.Count(first, "candidate_diag] db_empty"); n != 1 {
		t.Fatalf("window repeats must collapse to 1 log line, got %d in:\n%s", n, first)
	}

	time.Sleep(60 * time.Millisecond)
	logCandidateDiagnostic("db_empty", attrs...)
	second := buf.String()[len(first):]
	if n := strings.Count(second, "candidate_diag] db_empty"); n != 1 {
		t.Fatalf("post-cooldown call must be logged exactly once, got %d in:\n%s", n, second)
	}
	if !strings.Contains(second, "suppressed_repeats=2") {
		t.Fatalf("post-cooldown line must carry suppressed_repeats=2, got:\n%s", second)
	}
}

// TestLogCandidateDiagnosticOtherEventsNotGated —— 去重只对 db_empty 生效：
// 其余事件（如 enrich_empty）逐条保留，不因本文件的存在被误伤。
func TestLogCandidateDiagnosticOtherEventsNotGated(t *testing.T) {
	orig := candidateDiagLogGateInstance
	candidateDiagLogGateInstance = newCandidateDiagLogGate(1 * time.Hour)
	defer func() { candidateDiagLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	for i := 0; i < 3; i++ {
		logCandidateDiagnostic("enrich_empty", "model", "m", "tenant_id", "t")
	}
	if n := strings.Count(buf.String(), "candidate_diag] enrich_empty"); n != 3 {
		t.Fatalf("non-db_empty events must not be gated, want 3 lines got %d", n)
	}
}
