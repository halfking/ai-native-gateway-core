package autoupdate

// maintain_live_test.go — 网关自更新 ↔ ai-native-maintain 发版中心 全链路联测。
//
// 前后流程一致性（2026-11-28）：网关进程内自更新此前只检索 license-authority，
// 与 maintain 编译发版完全脱节。maintain 侧现在提供 /api/v1/updates/latest+
// manifest 兼容端点（数据真源 = 发版中心），本测试用**真实 HTTPRemoteSource +
// Checker + Executor + Installer + Rollback** 打通：
//
//	检索 → 下载裸网关二进制（sha256 硬门）→ 备份替换 → version.json/VERSION
//	重写 → 手动回滚（还原二进制 + version.json 真源）。
//
// 需要一个真实 maintain 实例且已发布带 platform=gateway 制品的版本：
//
//	MAINTAIN_LIVE_BASE_URL=http://127.0.0.1:18082 MAINTAIN_LIVE_OLD_VERSION=2.0.0 \
//	  go test ./autoupdate/ -run TestMaintainLiveSelfUpdate -v
//
// 未设置环境变量时整卡跳过（与 RUN_REAL_DEPLOY 门控同风格）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintainLiveSelfUpdate(t *testing.T) {
	baseURL := os.Getenv("MAINTAIN_LIVE_BASE_URL")
	if baseURL == "" {
		t.Skip("MAINTAIN_LIVE_BASE_URL not set — live maintain server required")
	}
	oldVersion := os.Getenv("MAINTAIN_LIVE_OLD_VERSION")
	if oldVersion == "" {
		oldVersion = "2.0.0"
	}

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	binPath := filepath.Join(dataDir, "llm-gateway-go")
	dlDir := filepath.Join(root, "downloads")
	backupDir := filepath.Join(root, "backups")
	for _, dir := range []string{dataDir, dlDir, backupDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// 升级配置合同基线：主机原配置必须升级后逐字保留（既有键保值）。
	// LIVE_FORCED 预置旧值，供发布物 forced 指令覆盖；LIVE_NEW 由发布物新增。
	originalEnv := "LIVE_KEEP=original-value\nLIVE_FORCED=before\n# host comment\n"
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(originalEnv), 0o640); err != nil {
		t.Fatalf("write host .env: %v", err)
	}

	// 旧二进制：≥1MiB + Mach-O 魔数，过了 installer 的结构校验。
	oldBinary := append([]byte{0xCF, 0xFA, 0xED, 0xFE}, make([]byte, 2<<20)...)
	if err := os.WriteFile(binPath, oldBinary, 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	oldJSON := fmt.Sprintf(`{"version":%q,"git_tag":"v%s","build_seq":10,"module":"llm-gateway-go"}`, oldVersion, oldVersion)
	if err := os.WriteFile(filepath.Join(dataDir, "version.json"), []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write version.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "VERSION"), []byte(oldVersion+"\n"), 0o644); err != nil {
		t.Fatalf("write VERSION: %v", err)
	}

	// ── 1. 检索：真实 HTTPRemoteSource 打 maintain /api/v1/updates/latest ──
	versionFn := VersionProvider(func() (string, int) { return oldVersion, 10 })
	checker := NewChecker(newFakeStore(), NewHTTPRemoteSource(baseURL), versionFn, ChannelStable,
		WithInstanceID("maintain-live-test"))
	result := checker.CheckOnce(context.Background())
	if result.Error != "" {
		t.Fatalf("check error: %s", result.Error)
	}
	if result.UpToDate || result.Latest == nil {
		t.Fatalf("expected an available update against live maintain, got up_to_date=%v latest=%+v", result.UpToDate, result.Latest)
	}
	rel := result.Latest
	if rel.Version == "" || rel.BinaryURL == "" || rel.BinarySHA256 == "" {
		t.Fatalf("RemoteRelease incomplete — maintain manifest 未出票或缺 sha256: %+v", rel)
	}
	if rel.ReleaseNotes == "" {
		t.Fatalf("release_notes 为空——修订说明未贯通")
	}
	// 升级配置合同：maintain 发版中心必须把结构化配置变更下发到 manifest。
	// 期望 LIVE_NEW（新增）、LIVE_KEEP（保值）、LIVE_FORCED（强制覆盖）。
	var wantNew, wantKeep, wantForced *EnvConfigUpdate
	for i := range rel.ConfigUpdates {
		switch rel.ConfigUpdates[i].Key {
		case "LIVE_NEW":
			wantNew = &rel.ConfigUpdates[i]
		case "LIVE_KEEP":
			wantKeep = &rel.ConfigUpdates[i]
		case "LIVE_FORCED":
			wantForced = &rel.ConfigUpdates[i]
		}
	}
	if wantNew == nil || wantKeep == nil || wantForced == nil {
		t.Fatalf("manifest config_updates 缺 LIVE_NEW/LIVE_KEEP/LIVE_FORCED: %+v", rel.ConfigUpdates)
	}
	if wantForced.Forced != true || wantKeep.Forced != false || wantNew.Forced != false {
		t.Fatalf("forced 标记不符: %+v", rel.ConfigUpdates)
	}
	t.Logf("检索到新版: version=%s build_seq=%d sha256=%s notes=%.40q config_updates=%d条", rel.Version, rel.BuildSeq, rel.BinarySHA256, rel.ReleaseNotes, len(rel.ConfigUpdates))

	// ── 2. 安装：Executor 走 artifact 模式下载 + sha256 校验 + 备份替换 ──
	store := newFakeStore()
	installer := NewInstaller(binPath, backupDir, dataDir)
	installer.SetMinBinarySize(1 << 20)
	installer.SetServiceName("") // 测试环境无 systemd；重启步骤显式跳过
	executor := NewExecutor(store, NewDownloader(dlDir), installer, versionFn,
		WithArtifactURLTemplate(baseURL+"/releases/{version}/llm-gateway-go"),
		WithExecutorInstanceID("maintain-live-test"),
	)
	noRestart := false
	if _, err := executor.Start(context.Background(), InstallRequest{
		Version:       rel.Version,
		Mode:          InstallModeArtifact,
		BinaryURL:     rel.BinaryURL,
		SHA256:        rel.BinarySHA256,
		ConfigUpdates: rel.ConfigUpdates,
		Restart:       &noRestart,
	}); err != nil {
		t.Fatalf("executor start: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		job := executor.Current()
		if job == nil {
			t.Fatalf("no install job")
		}
		if isTerminalStatus(job.Status) {
			if job.Status != StatusSuccess {
				t.Fatalf("install ended %s: %s (steps=%d)", job.Status, job.Error, len(job.Steps))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("install timed out in status %s", job.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 版本身份必须随二进制一起换新（version.json 是真源）。
	newJSON := mustReadFile(t, filepath.Join(dataDir, "version.json"))
	if !contains(string(newJSON), rel.Version) {
		t.Fatalf("version.json not updated to %s: %s", rel.Version, newJSON)
	}
	backupFiles, err := os.ReadDir(backupDir)
	if err != nil || len(backupFiles) == 0 {
		t.Fatalf("no backup created: err=%v files=%d", err, len(backupFiles))
	}

	// 升级配置合同：原配置保值 + 新增写入 + 强制覆盖，且备份落盘。
	envAfter := string(mustReadFile(t, filepath.Join(dataDir, ".env")))
	if !contains(envAfter, "LIVE_KEEP=original-value\n") {
		t.Fatalf("既有键原值必须保留（未 forced），got:\n%s", envAfter)
	}
	if !contains(envAfter, "# host comment\n") {
		t.Fatalf("注释行必须保留，got:\n%s", envAfter)
	}
	if !contains(envAfter, "LIVE_NEW="+wantNew.Value+"\n") {
		t.Fatalf("新增键必须写入，got:\n%s", envAfter)
	}
	if !contains(envAfter, "LIVE_FORCED="+wantForced.Value+"\n") {
		t.Fatalf("强制键必须覆盖，got:\n%s", envAfter)
	}
	envBackups := 0
	for _, f := range backupFiles {
		if strings.HasPrefix(f.Name(), ".env_") {
			envBackups++
		}
	}
	if envBackups == 0 {
		t.Fatalf("配置改动必须先落备份（backupDir 缺 .env_*）: %v", backupFiles)
	}
	t.Logf("安装成功: version.json → %s, 二进制备份 %s, 配置备份 %d 份", rel.Version, backupFiles[0].Name(), envBackups)

	// ── 3. 手动回滚：还原二进制 + version.json（修复前只还原 VERSION ⇒ 版本身份漂移）──
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	rollback := NewRollback(binPath, backupDir, dataDir)
	rollback.SetServiceName("")
	rollback.SetHealthURL(health.URL + "/healthz")
	rbResult, err := rollback.Rollback(context.Background(), oldVersion)
	if err != nil {
		t.Fatalf("rollback: %v result=%+v", err, rbResult)
	}
	rolledJSON := mustReadFile(t, filepath.Join(dataDir, "version.json"))
	if !contains(string(rolledJSON), oldVersion) {
		// 回滚后版本真源必须指回旧版，否则 Checker 拿新版本号当基线问远端
		// ⇒ 「已是最新」⇒ 节点永停旧二进制（版本身份漂移）。
		t.Fatalf("version.json not restored to %s after rollback: %s", oldVersion, rolledJSON)
	}
	rolledVersionFile := mustReadFile(t, filepath.Join(dataDir, "VERSION"))
	if !contains(string(rolledVersionFile), oldVersion) {
		t.Fatalf("VERSION not restored: %s", rolledVersionFile)
	}
	// 回滚侧配置合同：env 必须还原成升级前原样（强制键回旧值、新增键消失）。
	rolledEnv := string(mustReadFile(t, filepath.Join(dataDir, ".env")))
	if rolledEnv != originalEnv {
		t.Fatalf("env 必须还原为升级前原样，got:\n%s", rolledEnv)
	}
	t.Logf("回滚成功: version.json/VERSION → %s，二进制与配置均已还原", oldVersion)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
