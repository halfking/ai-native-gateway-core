package upgrader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyOfflinePackage(t *testing.T) {
	// 这是一个集成测试，需要真实的环境，暂时跳过
	t.Skip("需要真实环境和权限，仅作为文档示例")

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	os.MkdirAll(filepath.Join(dataDir, "app"), 0755)
	os.MkdirAll(filepath.Join(dataDir, "backups"), 0755)

	// 创建模拟的离线包（实际需要真实的 tar.gz）
	packagePath := filepath.Join(tmpDir, "upgrade.tar.gz")
	backupDir := filepath.Join(dataDir, "backups")
	composeFile := filepath.Join(dataDir, "compose.yml")
	containerName := "kx-citus"

	applier := NewOfflineApplier(dataDir, backupDir, composeFile, containerName)
	err := applier.ApplyOfflinePackage(packagePath, dataDir)
	if err == nil {
		t.Error("期望失败（因为是模拟包），但成功了")
	}
}

func TestCheckUpdate(t *testing.T) {
	// 这个测试需要真实的主控端 API，暂时跳过
	t.Skip("需要真实的主控端 API")

	masterURL := "https://llm.kxpms.cn"
	currentVersion := "v1.0.0"
	channel := "stable"

	release, err := CheckUpdate(masterURL, currentVersion, channel)
	if err != nil {
		t.Logf("CheckUpdate 返回错误（预期，因为 API 可能不可用）: %v", err)
	}

	if release != nil {
		t.Logf("发现更新: %s", release.Version)
	} else {
		t.Logf("当前已是最新版本")
	}
}

func TestRollback(t *testing.T) {
	// 这是一个集成测试，需要真实的环境和备份，暂时跳过
	t.Skip("需要真实环境、备份文件和权限")

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	masterURL := "https://llm.kxpms.cn"

	os.MkdirAll(filepath.Join(dataDir, "backups"), 0755)

	err := Rollback(dataDir, masterURL)
	if err == nil {
		t.Error("期望失败（因为没有备份），但成功了")
	}
}
