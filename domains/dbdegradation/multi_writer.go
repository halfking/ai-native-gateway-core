package dbdegradation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// MultiBackupWriter fans out writes to multiple BackupWriter backends.
// Used in cmd/gateway/main.go to wire both the disk FileWriter (durable
// spool for crash recovery) and the in-memory RingBuffer (fast online
// dump/replay) into the same telemetry fallback path.
//
// Behavior:
//   - All writes are attempted; failures from any backend are logged but
//     do not block the others.
//   - The first non-nil error from each backend is collected and joined
//     via errors.Join so callers see the full picture when they want to
//     surface it (e.g. for the FailPermanent counter).
type MultiBackupWriter struct {
	writers []BackupWriter
}

// NewMultiBackupWriter composes one or more BackupWriter backends.
// Order is preserved: writes are attempted in slice order, but the
// implementation does NOT short-circuit on first error.
func NewMultiBackupWriter(writers ...BackupWriter) *MultiBackupWriter {
	// Filter out nils so callers can pass conditional writers without
	// wrapping nil checks themselves.
	nonNil := make([]BackupWriter, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			nonNil = append(nonNil, w)
		}
	}
	return &MultiBackupWriter{writers: nonNil}
}

// WriteRequestLog fans out to every backend.
func (m *MultiBackupWriter) WriteRequestLog(ctx context.Context, key string, payload any) error {
	if m == nil || len(m.writers) == 0 {
		return fmt.Errorf("multi backup writer not configured")
	}
	var errs []error
	for _, w := range m.writers {
		if err := w.WriteRequestLog(ctx, key, payload); err != nil {
			errs = append(errs, fmt.Errorf("%T: %w", w, err))
			slog.Warn("multi backup writer: backend failed",
				"backend", fmt.Sprintf("%T", w),
				"record_type", "request_log",
				"key", key,
				"error", err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// WriteRequestWAL fans out to every backend.
func (m *MultiBackupWriter) WriteRequestWAL(ctx context.Context, key string, payload any) error {
	if m == nil || len(m.writers) == 0 {
		return fmt.Errorf("multi backup writer not configured")
	}
	var errs []error
	for _, w := range m.writers {
		if err := w.WriteRequestWAL(ctx, key, payload); err != nil {
			errs = append(errs, fmt.Errorf("%T: %w", w, err))
			slog.Warn("multi backup writer: backend failed",
				"backend", fmt.Sprintf("%T", w),
				"record_type", "request_wal",
				"key", key,
				"error", err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
