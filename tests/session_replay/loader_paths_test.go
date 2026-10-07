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
	// manifest 指向一个不存在的绝对路径（导出机上的布局），LoadAll 应回退到
	// manifest 所在目录取同名文件。用「临时目录下必然不存在的子目录」构造
	// 绝对路径：写死 /tmp/... 在 Windows 上 filepath.IsAbs 恒 false，回退
	// 分支永不触发（2026-10-07 跨平台修正，语义不变）。
	missing := filepath.Join(dir, "not_exported_here", "session_a.json")
	manifest := `{"session_files":[{"session_id":"session-a","file":"` + filepath.ToSlash(missing) + `"}]}`
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
	// filepath.ToSlash：Windows 反斜杠路径直接嵌 JSON 会产生非法转义
	// （\U/\T），ToSlash 后正反斜杠文件系统都能打开。
	manifest := `{"session_files":[{"session_id":"session-b","file":"` + filepath.ToSlash(external) + `"}]}`
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
