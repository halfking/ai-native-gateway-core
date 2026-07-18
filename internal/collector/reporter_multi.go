package collector

import (
	"context"
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
	for _, r := range m.reporters {
		if r == nil {
			continue
		}
		if err := r.Report(ctx, payload); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
