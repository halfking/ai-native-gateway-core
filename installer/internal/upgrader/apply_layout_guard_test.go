package upgrader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyUpdateRefusesComposeLayout 钉住布局守卫：docker compose 安装
// （installer 的现役产物）上在线二进制升级必须 fail-fast 并指路离线包，
// 不能去替换一个没在运行的 /usr/local/bin/kx-gateway。
func TestApplyUpdateRefusesComposeLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := ApplyUpdate(&Release{
		Version:     "v0.0.0-guard-probe",
		DownloadURL: "http://127.0.0.1:1/never-fetched",
		SHA256:      strings.Repeat("0", 64),
	}, dir, "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected compose-layout fail-fast error, got nil")
	}
	if !strings.Contains(err.Error(), "离线升级包") {
		t.Fatalf("error must point to the offline package path, got: %v", err)
	}
}
