package audit

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

var _ pipeline.Hook = (*AuditLogHook)(nil)

// InMemorySink tests
func TestInMemorySink_WriteAndRead(t *testing.T) {
	sink := NewInMemorySink()
	events := []*Event{{RequestID: "r1"}, {RequestID: "r2"}}
	if err := sink.Write(events); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if got := len(sink.Events()); got != 2 {
		t.Errorf("expected 2 events, got %d", got)
	}
}

func TestInMemorySink_CloseIsNoop(t *testing.T) {
	sink := NewInMemorySink()
	if err := sink.Close(); err != nil {
		t.Errorf("Close should not error: %v", err)
	}
}

// BatchWriter tests
func TestBatchWriter_Append(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 10, 1*time.Hour)
	defer w.Close()

	w.Append(&Event{RequestID: "r1"})
	if got := w.BufferedCount(); got != 1 {
		t.Errorf("expected 1 buffered, got %d", got)
	}
}

func TestBatchWriter_FlushOnSize(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 3, 1*time.Hour)
	defer w.Close()

	for i := 0; i < 3; i++ {
		w.Append(&Event{RequestID: "r"})
	}
	// 等后台 goroutine 处理
	time.Sleep(100 * time.Millisecond)
	if got := len(sink.Events()); got != 3 {
		t.Errorf("expected 3 flushed, got %d", got)
	}
}

func TestBatchWriter_FlushOnInterval(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 100, 100*time.Millisecond)
	defer w.Close()

	w.Append(&Event{RequestID: "r"})
	time.Sleep(250 * time.Millisecond) // 等定时 flush
	if got := len(sink.Events()); got != 1 {
		t.Errorf("expected 1 flushed, got %d", got)
	}
}

func TestBatchWriter_CloseFlushes(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 100, 1*time.Hour)
	w.Append(&Event{RequestID: "r1"})
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if got := len(sink.Events()); got != 1 {
		t.Errorf("expected 1 flushed on close, got %d", got)
	}
}

// AuditLogHook tests
func TestAuditLogHook_AllowAction(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 10, 1*time.Hour)
	defer w.Close()
	hook := NewAuditLogHook(w)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Envelope = &domain.RequestEnvelope{RequestID: "req-1"}
	env.TenantID = "tenant-a"
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if got := w.BufferedCount(); got != 1 {
		t.Errorf("expected 1 buffered, got %d", got)
	}
}

func TestAuditLogHook_DenyAction(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 100, 1*time.Hour)
	defer w.Close()
	hook := NewAuditLogHook(w)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Envelope = &domain.RequestEnvelope{RequestID: "req-1"}
	env.Error = &testError{msg: "blocked"}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	_ = w.Close()
	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Action != "deny" {
		t.Errorf("expected action 'deny', got %q", events[0].Action)
	}
	if events[0].Error == "" {
		t.Error("expected error in event")
	}
}

func TestAuditLogHook_ModifyAction(t *testing.T) {
	sink := NewInMemorySink()
	w := NewBatchWriter(sink, 100, 1*time.Hour)
	defer w.Close()
	hook := NewAuditLogHook(w)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Envelope = &domain.RequestEnvelope{RequestID: "req-1"}
	env.StatusCode = 200
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	_ = w.Close()
	events := sink.Events()
	if events[0].Action != "modify" {
		t.Errorf("expected action 'modify', got %q", events[0].Action)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
