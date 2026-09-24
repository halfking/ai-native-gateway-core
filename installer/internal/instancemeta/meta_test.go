package instancemeta

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_FullFile covers the happy path: wizard finished, activation.json
// has every field, Load returns them verbatim. This is what the launcher
// web UI will see in steady state.
func TestLoad_FullFile(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := map[string]any{
		"instance_id":    "inst-2026-abc123",
		"instance_token": "redacted-not-loaded",
		"device_code":    "DEV-XK4F-9021",
		"ip_address":     "10.20.30.40",
		"mode":           "full",
		"status":         "activated",
		"activated_at":   "2026-09-24T08:30:00Z",
		"expires_at":     "2027-09-24T08:30:00Z",
		"error":          "",
	}
	b, _ := json.Marshal(full)
	if err := os.WriteFile(filepath.Join(stateDir, FileName), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.InstallMode != "full" {
		t.Errorf("InstallMode = %q, want full", m.InstallMode)
	}
	if m.InstanceID != "inst-2026-abc123" {
		t.Errorf("InstanceID = %q", m.InstanceID)
	}
	if m.DeviceCode != "DEV-XK4F-9021" {
		t.Errorf("DeviceCode = %q", m.DeviceCode)
	}
	if m.IPAddress != "10.20.30.40" {
		t.Errorf("IPAddress = %q", m.IPAddress)
	}
	if m.ActivationStatus != "activated" {
		t.Errorf("ActivationStatus = %q", m.ActivationStatus)
	}
	if m.ActivatedAt != "2026-09-24T08:30:00Z" {
		t.Errorf("ActivatedAt = %q", m.ActivatedAt)
	}
	if m.IsZero() {
		t.Error("IsZero() = true on a populated Meta")
	}
}

// TestLoad_MissingFile covers the "wizard hasn't run / lite mode / pre-
// provisioning" path. Must return os.ErrNotExist verbatim so callers can
// errors.Is(err, os.ErrNotExist) to distinguish from JSON corruption.
func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
	if !m.IsZero() {
		t.Errorf("Meta should be zero value when file missing, got %+v", m)
	}
}

// TestLoad_MissingStateDir is the "install dir exists but state/ subdir
// never created" variant — still ErrNotExist, still zero Meta, no panic.
func TestLoad_MissingStateDir(t *testing.T) {
	dir := t.TempDir() // state/ not created
	m, err := Load(dir)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
	if !m.IsZero() {
		t.Errorf("Meta should be zero, got %+v", m)
	}
}

// TestLoad_BrokenJSON covers the "file exists but is invalid JSON" path.
// Must return a non-nil error (NOT os.ErrNotExist — it's a different
// failure mode) and a zero Meta. Crucially, MUST NOT panic — the launcher
// startup depends on Load being safe to call from a StatusProvider.
func TestLoad_BrokenJSON(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Truncated mid-key — valid JSON parsers will reject this with
	// "unexpected end of JSON input" or similar.
	bad := []byte(`{"mode":"full","status":"act`)
	if err := os.WriteFile(filepath.Join(stateDir, FileName), bad, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(dir)
	if err == nil {
		t.Fatal("expected non-nil error for broken JSON")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("broken JSON must NOT be reported as ErrNotExist, got %v", err)
	}
	if !m.IsZero() {
		t.Errorf("Meta should be zero on parse error, got %+v", m)
	}
}

// TestLoad_PartialFile covers the "wizard mid-flight" case — activation
// started but hasn't recorded IP/instance_id yet. Other fields must still
// come through correctly.
func TestLoad_PartialFile(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	partial := map[string]any{
		"mode":   "lite",
		"status": "trial",
		// device_code, ip_address, instance_id intentionally omitted
		"activated_at": "2026-09-24T09:00:00Z",
	}
	b, _ := json.Marshal(partial)
	if err := os.WriteFile(filepath.Join(stateDir, FileName), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.InstallMode != "lite" {
		t.Errorf("InstallMode = %q, want lite", m.InstallMode)
	}
	if m.ActivationStatus != "trial" {
		t.Errorf("ActivationStatus = %q, want trial", m.ActivationStatus)
	}
	if m.ActivatedAt != "2026-09-24T09:00:00Z" {
		t.Errorf("ActivatedAt = %q", m.ActivatedAt)
	}
	if m.IPAddress != "" {
		t.Errorf("IPAddress should be empty, got %q", m.IPAddress)
	}
	if m.InstanceID != "" {
		t.Errorf("InstanceID should be empty, got %q", m.InstanceID)
	}
	if m.DeviceCode != "" {
		t.Errorf("DeviceCode should be empty, got %q", m.DeviceCode)
	}
}

// TestLoad_EmptyInstallDir is the "caller hasn't resolved installDir yet"
// path. Must return ErrNotExist without panicking or touching the FS.
func TestLoad_EmptyInstallDir(t *testing.T) {
	m, err := Load("")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
	if !m.IsZero() {
		t.Errorf("Meta should be zero, got %+v", m)
	}
}

// TestLoad_IgnoresUnknownFields ensures forward-compat: when agent B adds
// new keys (e.g. license_tier, seat_count), the launcher keeps rendering
// the existing fields rather than failing the JSON parse.
func TestLoad_IgnoresUnknownFields(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	withExtras := map[string]any{
		"mode":         "full",
		"status":       "activated",
		"instance_id":  "inst-future",
		"license_tier": "enterprise", // future field
		"seat_count":   42,
	}
	b, _ := json.Marshal(withExtras)
	if err := os.WriteFile(filepath.Join(stateDir, FileName), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v (unknown fields must not break the parse)", err)
	}
	if m.InstallMode != "full" || m.InstanceID != "inst-future" {
		t.Errorf("Meta = %+v", m)
	}
}

// TestLoad_DoesNotExposeInstanceToken is a security regression guard: the
// token is on disk in activation.json (wizard wrote it for the
// enrollment pipeline), but the meta returned to the launcher / web UI
// must NEVER carry it. If this test fails, the launcher's /status
// endpoint would be leaking a credential via JSON.
func TestLoad_DoesNotExposeInstanceToken(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"mode":           "full",
		"status":         "activated",
		"instance_token": "super-secret-jwt-xyz",
	})
	if err := os.WriteFile(filepath.Join(stateDir, FileName), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Meta struct must not have a token field. We can't directly assert
	// "field absent" in Go, but we CAN serialize and check the wire
	// shape carries no token.
	b2, _ := json.Marshal(m)
	if strings.Contains(string(b2), "super-secret-jwt-xyz") {
		t.Fatalf("Meta JSON leaks instance_token: %s", b2)
	}
	if strings.Contains(strings.ToLower(string(b2)), "token") {
		t.Fatalf("Meta JSON contains unexpected 'token' field: %s", b2)
	}
}