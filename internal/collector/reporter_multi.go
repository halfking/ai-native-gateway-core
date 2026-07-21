package collector

import (
	"context"
	"log/slog"
)

// MultiReporter fans out to multiple reporters (e.g., HTTPReporter + LocalDBReporter).
// Continues on individual failures to ensure one sink's unavailability doesn't block others.
type MultiReporter struct {
	reporters []Reporter
}

// NewMultiReporter creates a reporter that writes to all provided reporters.
func NewMultiReporter(reporters ...Reporter) *MultiReporter {
	return &MultiReporter{reporters: reporters}
}

// Report implements Reporter by calling all nested reporters.
// Returns the first non-nil error encountered, but continues attempting all reporters.
func (m *MultiReporter) Report(ctx context.Context, payload []byte) error {
	var firstErr error
	for i, r := range m.reporters {
		if r == nil {
			continue
		}
		if err := r.Report(ctx, payload); err != nil {
			// 2026-07-22: 诊断 22P02 来源 - 记录每个 reporter 的错误
			reporterType := "unknown"
			switch r.(type) {
			case *HTTPReporter:
				reporterType = "http"
			case *LocalDBReporter:
				reporterType = "local_db"
			}
			slog.Warn("multi_reporter: nested reporter failed",
				"index", i,
				"type", reporterType,
				"error", err)
			
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
