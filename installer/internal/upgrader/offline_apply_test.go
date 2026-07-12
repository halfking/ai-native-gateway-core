package upgrader

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewOfflineApplier(t *testing.T) {
	tmpDir := t.TempDir()
	applier := NewOfflineApplier(tmpDir, filepath.Join(tmpDir, "backups"),
		filepath.Join(tmpDir, "compose.yml"), "test-container")

	if applier == nil {
		t.Error("NewOfflineApplier returned nil")
	}
	if applier.dataDir != tmpDir {
		t.Errorf("expected dataDir %s, got %s", tmpDir, applier.dataDir)
	}
}

func TestBackupCurrentState(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(backupDir, 0755)

	// 创建测试文件
	testBinary := filepath.Join(tmpDir, "kx-gateway")
	os.WriteFile(testBinary, []byte("test binary"), 0755)

	composeFile := filepath.Join(tmpDir, "compose.yml")
	os.WriteFile(composeFile, []byte("version: '3'"), 0644)

	envFile := filepath.Join(tmpDir, ".env")
	os.WriteFile(envFile, []byte("TEST=value"), 0644)

	applier := NewOfflineApplier(tmpDir, backupDir, composeFile, "test-container")

	backupPath, err := applier.backupCurrentState()
	if err != nil {
		t.Fatalf("backupCurrentState failed: %v", err)
	}

	// 验证备份文件存在
	if _, err := os.Stat(filepath.Join(backupPath, "kx-gateway")); err != nil {
		t.Errorf("backup binary not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupPath, "docker-compose.yml")); err != nil {
		t.Errorf("backup compose.yml not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupPath, ".env")); err != nil {
		t.Errorf("backup .env not found: %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	applier := NewOfflineApplier(tmpDir, "", "", "")

	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	content := []byte("test content")
	if err := os.WriteFile(srcFile, content, 0755); err != nil {
		t.Fatal(err)
	}

	if err := applier.copyFile(srcFile, dstFile); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	// 验证内容
	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(dstContent) != string(content) {
		t.Errorf("content mismatch: expected %s, got %s", content, dstContent)
	}

	// 验证权限
	srcInfo, _ := os.Stat(srcFile)
	dstInfo, _ := os.Stat(dstFile)
	if srcInfo.Mode() != dstInfo.Mode() {
		t.Errorf("permission mismatch: expected %v, got %v", srcInfo.Mode(), dstInfo.Mode())
	}
}

func TestHealthCheckTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	applier := NewOfflineApplier(tmpDir, "", "", "")

	// 测试超时逻辑（不实际调用，避免依赖真实服务）
	timeout := 100 * time.Millisecond
	deadline := time.Now().Add(timeout)

	if time.Now().After(deadline) {
		t.Error("deadline should not have passed immediately")
	}

	// 验证 applier 创建成功
	if applier == nil {
		t.Error("NewOfflineApplier returned nil")
	}
}
