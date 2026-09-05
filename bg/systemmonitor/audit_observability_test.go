package systemmonitor

import (
	"context"
	"strings"
	"testing"
)

func TestWriteAuditReportsDisabledBackend(t *testing.T) {
	sm := &SystemMonitor{}
	err := sm.writeAudit(context.Background(), &Task{ID: 1}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "audit backend is disabled") {
		t.Fatalf("writeAudit error = %v, want disabled backend error", err)
	}
}
