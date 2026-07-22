package streaming

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestKeepaliveSender_SendKeepalive(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	recorder := httptest.NewRecorder()
	sender := NewKeepaliveSender(recorder, 1, logger)

	if sender == nil {
		t.Fatal("expected non-nil sender")
	}

	err := sender.sendKeepalive()
	if err != nil {
		t.Fatalf("sendKeepalive failed: %v", err)
	}

	output := recorder.Body.String()
	if !strings.Contains(output, "event: keepalive") {
		t.Errorf("expected 'event: keepalive', got: %s", output)
	}
	if !strings.Contains(output, "data:") {
		t.Errorf("expected 'data:', got: %s", output)
	}
	if !strings.Contains(output, "\"type\":\"keepalive\"") {
		t.Errorf("expected keepalive type in data, got: %s", output)
	}
}

func TestKeepaliveSender_SendNodeSwitch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	recorder := httptest.NewRecorder()
	sender := NewKeepaliveSender(recorder, 1, logger)

	err := sender.SendNodeSwitch("node-1", "node-2", 2, "timeout")
	if err != nil {
		t.Fatalf("SendNodeSwitch failed: %v", err)
	}

	output := recorder.Body.String()
	if !strings.Contains(output, "event: node_switch") {
		t.Errorf("expected 'event: node_switch', got: %s", output)
	}
	if !strings.Contains(output, "node-1") {
		t.Errorf("expected 'node-1', got: %s", output)
	}
	if !strings.Contains(output, "node-2") {
		t.Errorf("expected 'node-2', got: %s", output)
	}
}

func TestKeepaliveSender_AutoSend(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	recorder := httptest.NewRecorder()
	sender := NewKeepaliveSender(recorder, 1, logger) // 1 second interval

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	sender.Start(ctx)
	defer sender.Stop()

	// Wait for at least 2 keepalives
	time.Sleep(2200 * time.Millisecond)

	output := recorder.Body.String()
	count := strings.Count(output, "event: keepalive")
	if count < 2 {
		t.Errorf("expected at least 2 keepalives, got %d. Output: %s", count, output)
	}
}

func TestKeepaliveSender_NilSafety(t *testing.T) {
	var sender *KeepaliveSender = nil

	// Should not panic
	sender.Start(context.Background())
	sender.Stop()
	err := sender.sendKeepalive()
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	err = sender.SendNodeSwitch("a", "b", 1, "test")
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}
