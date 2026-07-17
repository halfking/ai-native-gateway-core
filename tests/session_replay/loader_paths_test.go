package session_replay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllFallsBackToManifestDirectory(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session_a.json")
	if err := os.WriteFile(sessionFile, []byte(`{"session_meta":{"id":"session-a"},"turns":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"session_files":[{"session_id":"session-a","file":"/tmp/session_export/sessions/session_a.json"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABS_SESSIONS_DIR", dir)

	sessions, err := LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Meta.ID != "session-a" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestLoadAllKeepsExistingExplicitPath(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "session_b.json")
	if err := os.WriteFile(external, []byte(`{"session_meta":{"id":"session-b"},"turns":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"session_files":[{"session_id":"session-b","file":"` + external + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABS_SESSIONS_DIR", dir)

	sessions, err := LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Meta.ID != "session-b" {
		t.Fatalf("sessions = %#v", sessions)
	}
}
