package telemetry

import (
	"testing"
	"time"
)

func TestNormalizeRequestClassAndDueAt(t *testing.T) {
	due := time.Unix(1800000000, 0).UTC()
	scheduled := "scheduled"
	immediate := "immediate"
	invalid := "deferred"

	tests := []struct {
		name      string
		entry     RequestLogEntry
		wantClass string
		wantDue   bool
	}{
		{
			name:      "keeps valid scheduled request",
			entry:     RequestLogEntry{RequestClass: &scheduled, DueAt: &due},
			wantClass: "scheduled",
			wantDue:   true,
		},
		{
			name:      "normalizes scheduled without due time",
			entry:     RequestLogEntry{RequestClass: &scheduled},
			wantClass: "immediate",
		},
		{
			name:      "normalizes immediate with due time",
			entry:     RequestLogEntry{RequestClass: &immediate, DueAt: &due},
			wantClass: "immediate",
		},
		{
			name:      "normalizes invalid class",
			entry:     RequestLogEntry{RequestClass: &invalid, DueAt: &due},
			wantClass: "immediate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizeRequestClassAndDueAt(&tt.entry, false)
			if tt.entry.RequestClass == nil || *tt.entry.RequestClass != tt.wantClass {
				t.Fatalf("RequestClass = %v, want %q", tt.entry.RequestClass, tt.wantClass)
			}
			if (tt.entry.DueAt != nil) != tt.wantDue {
				t.Fatalf("DueAt = %v, want due present=%t", tt.entry.DueAt, tt.wantDue)
			}
		})
	}
}

func TestRequestClassArgRejectsUnknownClass(t *testing.T) {
	invalid := "deferred"
	if got := requestClassArg(&invalid); got != "immediate" {
		t.Fatalf("requestClassArg(invalid) = %q, want immediate", got)
	}
}
