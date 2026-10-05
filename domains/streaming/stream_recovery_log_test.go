package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// 2026-08-19: NextRecoveryAction / NextRecoveryActionCtx emits one
// survival_recovery_action slog per decision. Pin the L1 discard-and-replay
// case (the L1 transition is exactly the "Partial assistant output was
// discarded before a streaming retry" failure class) plus the L4 error
// envelope path so the new observer reads what we expect.

func TestNextRecoveryAction_EmitsStructuredLogOnDiscardReplay(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	state := &StreamRecoveryState{
		RecoveryNo:      1, // budget left
		SameNodeRetries: 2, // L0 already used (default cap)
		CommittedChunks: 0, // L1 fires
	}
	act := NextRecoveryActionCtx(context.Background(), state, DefaultStreamRecoveryConfig(),
		errors.New("eof_without_done: upstream closed mid-stream"))

	if act.Kind != RecoveryActionDiscardAndReplay {
		t.Fatalf("action=%s want %s", act.Kind.String(), RecoveryActionDiscardAndReplay.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"survival_recovery_action"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), `"survival_recovery_action"`) {
		t.Fatalf("expected survival_recovery_action slog line; got: %s", buf.String())
	}

	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			continue
		}
		if rec["msg"] == "survival_recovery_action" {
			if rec["action"] != "DiscardAndReplay" {
				t.Errorf("action=%v want DiscardAndReplay", rec["action"])
			}
			if rec["mode"] != "discard_replay" {
				t.Errorf("mode=%v want discard_replay", rec["mode"])
			}
			if rec["interrupt_class"] != string(InterruptEOFWithoutDone) {
				t.Errorf("interrupt_class=%v want %s", rec["interrupt_class"], InterruptEOFWithoutDone)
			}
			if _, ok := rec["committed_chunks"]; !ok {
				t.Errorf("missing committed_chunks in log line: %v", rec)
			}
			return
		}
	}
	t.Fatal("survival_recovery_action line not parsed from log buffer")
}

func TestNextRecoveryAction_EmitsWarnLevelOnErrorEnvelope(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// 401 → non_recoverable → RecoveryActionErrorEnvelope (warn-level).
	act := NextRecoveryActionCtx(context.Background(), &StreamRecoveryState{}, DefaultStreamRecoveryConfig(),
		errors.New("invalid api key provided (401)"))
	if act.Kind != RecoveryActionErrorEnvelope {
		t.Fatalf("action=%s want %s", act.Kind.String(), RecoveryActionErrorEnvelope.String())
	}
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("expected WARN-level entry for ErrorEnvelope; got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"action":"ErrorEnvelope"`) {
		t.Fatalf("expected action=ErrorEnvelope; got: %s", buf.String())
	}
}
