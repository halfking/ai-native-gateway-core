package autoupdate

// env_contract_test.go — 升级配置合同单测：
// 「保存主机上原来的配置，只处理新增或强制更新的配置」。
//
// 覆盖：
//  1. 新增键：主机缺失 ⇒ 追加，原行不动
//  2. 已有键非强制 ⇒ 原值保留（kept 记录），文件里该行逐字节不变
//  3. 已有键强制 ⇒ 覆盖值部分，键名/行首/行尾注释保留
//  4. 应用前必有备份；全部保留（no-op）时不落备份不写盘
//  5. 安装失败（迁移失败路径）⇒ env 随快照还原
//  6. 手动回滚还原最新配置备份

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStubBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, append([]byte{0xCF, 0xFA, 0xED, 0xFE}, make([]byte, 2<<20)...), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
}

func newTestInstaller(t *testing.T, envContent string) (*Installer, string, string) {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	backupDir := filepath.Join(root, "backups")
	dlDir := filepath.Join(root, "downloads")
	for _, dir := range []string{dataDir, backupDir, dlDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	binPath := filepath.Join(dataDir, "llm-gateway-go")
	writeStubBinary(t, binPath)
	if envContent != "" {
		if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(envContent), 0o640); err != nil {
			t.Fatalf("write env: %v", err)
		}
	}
	inst := NewInstaller(binPath, backupDir, dataDir)
	inst.SetMinBinarySize(1 << 20)
	inst.SetServiceName("")
	return inst, dataDir, backupDir
}

func TestApplyConfigUpdatesPreservesAndAdds(t *testing.T) {
	inst, dataDir, _ := newTestInstaller(t, "EXISTING_KEY=keep-me\n# comment stays\nexport EXPORTED=old-value # trailing\n")
	envPath := filepath.Join(dataDir, ".env")

	backup, applied, kept, err := inst.applyConfigUpdates([]EnvConfigUpdate{
		{Key: "EXISTING_KEY", Value: "would-overwrite"},            // 非强制 ⇒ 保留
		{Key: "NEW_KEY", Value: "fresh"},                            // 新增 ⇒ 追加
		{Key: "EXPORTED", Value: "new-value", Forced: true},         // 强制 ⇒ 覆盖
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if backup == "" {
		t.Fatalf("backup must be created when file changes")
	}
	if len(applied) != 2 || applied[0] != "NEW_KEY" || applied[1] != "EXPORTED" {
		t.Fatalf("applied = %v", applied)
	}
	if len(kept) != 1 || kept[0] != "EXISTING_KEY" {
		t.Fatalf("kept = %v", kept)
	}

	out := mustReadFile(t, envPath)
	lines := strings.Split(string(out), "\n")
	if !contains(string(out), "EXISTING_KEY=keep-me\n") {
		t.Fatalf("existing value must be preserved verbatim, got:\n%s", out)
	}
	if !contains(string(out), "# comment stays\n") {
		t.Fatalf("comment line must survive, got:\n%s", out)
	}
	if !contains(string(out), "export EXPORTED=new-value # trailing\n") {
		t.Fatalf("forced update must keep export prefix and trailing comment, got:\n%s", out)
	}
	if !contains(string(out), "NEW_KEY=fresh") {
		t.Fatalf("new key must be appended, got:\n%s", out)
	}
	_ = lines

	// 备份内容 = 改动前原文。
	backupContent := mustReadFile(t, backup)
	if string(backupContent) != "EXISTING_KEY=keep-me\n# comment stays\nexport EXPORTED=old-value # trailing\n" {
		t.Fatalf("backup content mismatch:\n%s", backupContent)
	}
}

func TestApplyConfigUpdatesAllKeptIsNoop(t *testing.T) {
	inst, dataDir, backupDir := newTestInstaller(t, "A=1\nB=2\n")
	envPath := filepath.Join(dataDir, ".env")
	before := mustReadFile(t, envPath)

	backup, applied, kept, err := inst.applyConfigUpdates([]EnvConfigUpdate{
		{Key: "A", Value: "x"},
		{Key: "B", Value: "y"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if backup != "" || len(applied) != 0 || len(kept) != 2 {
		t.Fatalf("no-op expected: backup=%q applied=%v kept=%v", backup, applied, kept)
	}
	after := mustReadFile(t, envPath)
	if string(before) != string(after) {
		t.Fatalf("file must be untouched on all-kept")
	}
	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 0 {
		t.Fatalf("no-op must not create backups, got %d", len(entries))
	}
}

func TestApplyConfigUpdatesMissingEnvFileCreates(t *testing.T) {
	inst, dataDir, _ := newTestInstaller(t, "")
	envPath := filepath.Join(dataDir, ".env")
	backup, applied, _, err := inst.applyConfigUpdates([]EnvConfigUpdate{{Key: "K", Value: "v"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if backup != "" || len(applied) != 1 {
		t.Fatalf("backup=%q applied=%v", backup, applied)
	}
	if !contains(string(mustReadFile(t, envPath)), "K=v") {
		t.Fatalf("env must be created with K=v")
	}
}

func TestInstallRollbackRestoresEnvOnMigrationFailure(t *testing.T) {
	inst, dataDir, _ := newTestInstaller(t, "PRESERVE=original\n")

	// 迁移命令必败 ⇒ 触发安装失败自动回滚路径。
	inst.SetMigration("/nonexistent-migrate-bin")
	inst.SetServiceName("")

	// InstallWithOptions 直接消费 downloadPath；给一个真实存在的桩文件。
	stub := filepath.Join(filepath.Dir(dataDir), "stub")
	writeStubBinary(t, stub)
	res, err := inst.InstallWithOptions(t.Context(), stub,
		&Release{Version: "9.9.9"}, InstallOptions{
			ConfigUpdates: []EnvConfigUpdate{
				{Key: "ADDED", Value: "1"},
				{Key: "PRESERVE", Value: "2", Forced: true},
			},
		})
	if err == nil {
		t.Fatalf("migration must fail")
	}
	if !res.RolledBack {
		t.Fatalf("must report RolledBack, result=%+v", res)
	}
	env := string(mustReadFile(t, filepath.Join(dataDir, ".env")))
	if env != "PRESERVE=original\n" {
		t.Fatalf("env must be restored verbatim after failed install, got:\n%s", env)
	}
	if res.ConfigBackupPath == "" {
		t.Fatalf("config backup path must be recorded")
	}
}

func TestManualRollbackRestoresEnvBackup(t *testing.T) {
	inst, dataDir, backupDir := newTestInstaller(t, "A=1\n")
	envPath := filepath.Join(dataDir, ".env")

	// 模拟一次升级：强制改写 A 并新增 B（此时生成备份）。
	_, _, _, err := inst.applyConfigUpdates([]EnvConfigUpdate{
		{Key: "A", Value: "2", Forced: true},
		{Key: "B", Value: "x"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !contains(string(mustReadFile(t, envPath)), "A=2") {
		t.Fatalf("forced write expected")
	}

	// 回滚前置：备份目录里要有目标版本的二进制备份（findBackup 依版本名匹配）。
	writeStubBinary(t, filepath.Join(backupDir, "llm-gateway-go_1.0.0_20261129_000000"))

	// 手动回滚：还原二进制 + 最新配置备份。
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthSrv.Close()
	rb := NewRollback(filepath.Join(dataDir, "llm-gateway-go"), backupDir, dataDir)
	rb.SetServiceName("")
	rb.SetEnvFile(envPath)
	rb.SetHealthURL(healthSrv.URL + "/healthz")
	if _, err := rb.Rollback(t.Context(), "1.0.0"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	env := string(mustReadFile(t, envPath))
	if !contains(env, "A=1") || contains(env, "B=") {
		t.Fatalf("env backup must be restored (A=1, no B), got:\n%s", env)
	}
}
