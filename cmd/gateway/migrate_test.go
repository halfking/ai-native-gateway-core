package main

import (
	"encoding/json"
	"testing"
)

// TestRunMigrateNoDB: empty DB URL → exit 0, status="noop", valid JSON stdout.
// (DatabaseURL empty triggers db.Open returning nil, nil — Gateway existing convention.)
func TestRunMigrateNoDB(t *testing.T) {
	exitCode, stdout := runMigrateWithCapture("")
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for empty DB, got %d", exitCode)
	}
	var report migrationReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout not valid JSON: %v; got: %s", err, stdout.String())
	}
	if report.Status != "noop" {
		t.Fatalf("expected status noop, got %q", report.Status)
	}
}