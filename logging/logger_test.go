package logging

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestNewLogger(t *testing.T) {
	config := DefaultConfig()
	config.Output = &bytes.Buffer{}

	logger, err := NewLogger(config)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	if logger == nil {
		t.Fatal("Logger should not be nil")
	}

	t.Log("NewLogger test passed")
}

func TestLogLevels(t *testing.T) {
	buf := &bytes.Buffer{}
	config := DefaultConfig()
	config.Output = buf

	logger, err := NewLogger(config)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.Info("test info")
	logger.Warn("test warn")
	logger.Error("test error")
	logger.Sync()

	if buf.Len() == 0 {
		t.Error("No logs written")
	}

	t.Logf("Logs written: %d bytes", buf.Len())
}

func TestWithContext(t *testing.T) {
	buf := &bytes.Buffer{}
	config := DefaultConfig()
	config.Output = buf

	logger, err := NewLogger(config)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	ctx := context.WithValue(context.Background(), "trace_id", "trace123")
	logger.WithContext(ctx).Info("test message")
	logger.Sync()

	if buf.Len() == 0 {
		t.Error("No logs written")
	}

	t.Log("WithContext test passed")
}

func TestLogRequest(t *testing.T) {
	buf := &bytes.Buffer{}
	config := DefaultConfig()
	config.Output = buf

	logger, err := NewLogger(config)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	ctx := context.Background()
	logger.LogRequest(ctx, "GET", "/api/test", 200, 100*time.Millisecond, nil)
	logger.Sync()

	if buf.Len() == 0 {
		t.Error("No logs written")
	}

	t.Log("LogRequest test passed")
}
