package dbdegradation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// stubWriter is a minimal BackupWriter used by MultiBackupWriter tests.
// It records write counts and can be configured to fail.
type stubWriter struct {
	count   atomic.Int64
	failErr error
}

func (s *stubWriter) WriteRequestLog(ctx context.Context, key string, payload any) error {
	s.count.Add(1)
	return s.failErr
}
func (s *stubWriter) WriteRequestWAL(ctx context.Context, key string, payload any) error {
	s.count.Add(1)
	return s.failErr
}

func TestMultiBackupWriter_FansOutToAll(t *testing.T) {
	a := &stubWriter{}
	b := &stubWriter{}
	m := NewMultiBackupWriter(a, b)

	if err := m.WriteRequestLog(context.Background(), "k", "payload"); err != nil {
		t.Fatalf("WriteRequestLog: %v", err)
	}
	if err := m.WriteRequestWAL(context.Background(), "k", "payload"); err != nil {
		t.Fatalf("WriteRequestWAL: %v", err)
	}

	if a.count.Load() != 2 {
		t.Errorf("a.count = %d, want 2", a.count.Load())
	}
	if b.count.Load() != 2 {
		t.Errorf("b.count = %d, want 2", b.count.Load())
	}
}

func TestMultiBackupWriter_PartialFailure(t *testing.T) {
	a := &stubWriter{}
	b := &stubWriter{failErr: errors.New("disk full")}
	m := NewMultiBackupWriter(a, b)

	err := m.WriteRequestLog(context.Background(), "k", "payload")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, b.failErr) {
		t.Errorf("joined error should wrap %v, got %v", b.failErr, err)
	}
	// Backend 'a' should still have been attempted.
	if a.count.Load() != 1 {
		t.Errorf("a.count = %d, want 1 (no short-circuit)", a.count.Load())
	}
	if b.count.Load() != 1 {
		t.Errorf("b.count = %d, want 1", b.count.Load())
	}
}

func TestMultiBackupWriter_FiltersNil(t *testing.T) {
	a := &stubWriter{}
	m := NewMultiBackupWriter(nil, a, nil)
	if err := m.WriteRequestLog(context.Background(), "k", "p"); err != nil {
		t.Fatalf("WriteRequestLog: %v", err)
	}
	if a.count.Load() != 1 {
		t.Errorf("a.count = %d, want 1", a.count.Load())
	}
}

func TestMultiBackupWriter_Empty(t *testing.T) {
	m := NewMultiBackupWriter()
	if err := m.WriteRequestLog(context.Background(), "k", "p"); err == nil {
		t.Errorf("empty multi writer should return error")
	}
}
