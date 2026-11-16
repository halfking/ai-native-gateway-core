package autoupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

// newEchoForAdminAPI 造一个挂好自更新路由的 echo 实例。
func newEchoForAdminAPI(api *AdminAPI) *echo.Echo {
	e := echo.New()
	api.RegisterRoutes(e.Group("/api/admin/releases"))
	return e
}

// containsAll 判断 body 是否同时包含所有片段。
func containsAll(body string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(body, p) {
			return false
		}
	}
	return true
}

// ── 提醒生命周期闭环 ─────────────────────────────────────────────────────
//
// ★ 这组判据钉的是「提醒行 ≡ 安装任务日志行」这个承诺是否真的成立。
//
//   提醒生命周期：
//     检索器发现新版 ⇒ 生成一行 status=pending（这就是「提醒」）
//        ├─ 点「立即升级」成功 ⇒ 该行推进到 success ⇒ 不再是待办
//        └─ 升级失败/回滚     ⇒ 该行落到 failed/rolled_back ⇒ **仍是待办**
//
//   若升级器另起一行（不推进提醒行），提醒行就永远停在 pending：
//   升级成功了它还显示「待处理」；而回滚之后它又是一条永不消失的僵尸提醒。

// installArtifactFixture 造一套「旧二进制 + 制品服务 + store」。
func installArtifactFixture(t *testing.T, serviceName string) (*stubStore, *Executor, string, string) {
	t.Helper()
	root := t.TempDir()
	binPath := filepath.Join(root, "llm-gateway-go")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 旧二进制：复制测试二进制（体积足够）
	oldBytes, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(binPath, oldBytes, 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	// 旧版本真源
	if err := os.WriteFile(filepath.Join(dataDir, "version.json"),
		[]byte(`{"version":"2.6.0","git_sha":"oldsha","build_seq":3040}`), 0o644); err != nil {
		t.Fatalf("write version.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "VERSION"), []byte("2.6.0\n"), 0o644); err != nil {
		t.Fatalf("write VERSION: %v", err)
	}

	artifact := filepath.Join(root, "artifact.bin")
	sum := writeFakeBinary(t, artifact, 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, artifact)
	}))
	t.Cleanup(srv.Close)

	store := newStubStore()
	if err := store.CreateRelease(context.Background(), &Release{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
		ImageDigest: sum, ImageTag: "2.7.0", Title: "2.7.0", CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed release: %v", err)
	}

	installer := NewInstaller(binPath, filepath.Join(root, "backups"), dataDir)
	installer.SetMinBinarySize(1024)
	if serviceName != "" {
		// ★ 指向一个**不存在**的 systemd unit ⇒ restartService 必然失败
		//   ⇒ 稳定触发「新二进制起不来 ⇒ 回滚」这条路径。
		installer.SetServiceName(serviceName)
	}

	exec := NewExecutor(store, NewDownloader(filepath.Join(root, "downloads")), installer,
		func() (string, int) { return "2.6.0", 3040 },
		WithArtifactURLTemplate(srv.URL+"/{version}"),
		WithExecutorInstanceID("gw-test"))

	return store, exec, binPath, dataDir
}

// waitJob 等安装任务落到终态。
func waitJob(t *testing.T, exec *Executor) *InstallJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		cur := exec.Current()
		if cur != nil && isTerminalStatus(cur.Status) {
			return cur
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("install job did not reach a terminal state; last=%+v", exec.Current())
	return nil
}

// TestReminderRow_ClosesOnSuccessfulInstall 提醒行闭环（成功路径）：
// 检索器生成的 pending 行，安装成功后必须变成 success。
//
// 反面：升级器另起一行 ⇒ 提醒行永远 pending ⇒ 升级完成了界面还显示「待处理」。
func TestReminderRow_ClosesOnSuccessfulInstall(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
		BinaryURL: "https://example.test/bin", BinarySHA256: "sha",
	}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))
	checker.CheckOnce(context.Background())

	history, _, _ := store.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	if len(history) != 1 || history[0].Status != StatusPending {
		t.Fatalf("expected exactly 1 pending reminder, got %+v", history)
	}
	reminderID := history[0].LogID
	if reminderID <= 0 {
		t.Fatal("reminder row must carry its upgrade_logs.id — otherwise it cannot be advanced")
	}

	store2, exec, _, _ := installArtifactFixture(t, "")
	// 把检索器生成的提醒行搬到执行器用的 store 上（模拟同一个库）
	store2.logs = append(store2.logs, history...)
	exec.instanceID = "gw-test"

	if _, err := exec.Start(context.Background(), InstallRequest{Version: "2.7.0"}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	job := waitJob(t, exec)
	if job.Status != StatusSuccess {
		t.Fatalf("job status = %q error = %q steps = %v", job.Status, job.Error, job.Steps)
	}

	after, _, _ := store2.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	if len(after) != 1 {
		t.Fatalf("expected the reminder row to be reused (not duplicated), got %d rows", len(after))
	}
	if after[0].LogID != reminderID {
		t.Errorf("reminder row id changed: %d -> %d (the install must advance the SAME row)", reminderID, after[0].LogID)
	}
	if after[0].Status != StatusSuccess {
		t.Errorf("reminder row status = %q, want %q — a successful upgrade must close the reminder", after[0].Status, StatusSuccess)
	}
	if IsActionableStatus(after[0].Status) {
		t.Error("a successfully installed version must no longer be actionable")
	}
}

// TestReminderRow_StaysActionableAfterRollback 提醒行闭环（回滚路径）：
// 新二进制起不来 ⇒ 回滚 ⇒ 节点仍在旧版本上 ⇒ 新版**仍待处理** ⇒ 提醒不能消失。
//
// ★ 这是「最需要提醒的场景」：刚装的新版本把节点搞坏了。
//
//	若回滚后提醒没了 ⇒ 节点永远停在旧二进制上，且再也没人知道该处理。
func TestReminderRow_StaysActionableAfterRollback(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
	}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))
	checker.CheckOnce(context.Background())
	history, _, _ := store.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	if len(history) != 1 {
		t.Fatalf("expected 1 reminder row, got %d", len(history))
	}

	store2, exec, binPath, dataDir := installArtifactFixture(t, "llmgw-selfupdate-nonexistent-unit-test")
	store2.logs = append(store2.logs, history...)
	exec.instanceID = "gw-test"

	if _, err := exec.Start(context.Background(), InstallRequest{Version: "2.7.0"}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	job := waitJob(t, exec)
	if job.Status == StatusSuccess {
		t.Fatal("expected the install to fail (service unit does not exist)")
	}
	if job.Status != StatusRollback {
		t.Errorf("job status = %q, want %q", job.Status, StatusRollback)
	}

	after, _, _ := store2.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	if len(after) != 1 {
		t.Fatalf("expected the reminder row to be reused, got %d rows", len(after))
	}
	if !IsActionableStatus(after[0].Status) {
		t.Errorf("after rollback the reminder must still be actionable, got status %q", after[0].Status)
	}

	// ★ 版本身份不得漂移：回滚后 version.json 必须还是旧版本。
	raw, err := os.ReadFile(filepath.Join(dataDir, "version.json"))
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode version.json: %v", err)
	}
	if info.Version != "2.6.0" {
		t.Errorf("version.json version = %q, want 2.6.0 — after rollback the node must report the OLD version", info.Version)
	}

	// 二进制也必须还原成旧的（不能是只换了版本号的假象）
	inst := NewInstaller(binPath, filepath.Join(t.TempDir(), "b"), dataDir)
	if got := inst.getCurrentVersion(); got != "2.6.0" {
		t.Errorf("installer current version = %q, want 2.6.0", got)
	}
}

// TestInstaller_RollbackRestoresVersionMetadata 直接钉安装器的回滚还原：
// version.json 是**真源**，回滚必须把它一起还原。
//
// ★ 反面：只还原二进制、留着新版 version.json ⇒ 网关顶着新版号跑旧二进制
//
//	⇒ Checker 拿错误基线去问远端 ⇒ 远端答「已是最新」
//	⇒ **该节点永远停在旧二进制上，且再也不会被提醒**。
func TestInstaller_RollbackRestoresVersionMetadata(t *testing.T) {
	root := t.TempDir()
	binPath := filepath.Join(root, "llm-gateway-go")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oldBytes, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(binPath, oldBytes, 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	oldJSON := []byte(`{"version":"2.6.0","git_sha":"oldsha","build_seq":3040}`)
	if err := os.WriteFile(filepath.Join(dataDir, "version.json"), oldJSON, 0o644); err != nil {
		t.Fatalf("write version.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "VERSION"), []byte("2.6.0\n"), 0o644); err != nil {
		t.Fatalf("write VERSION: %v", err)
	}
	oldBinarySum := sha256Hex(binPath)

	newBinary := filepath.Join(root, "new-binary")
	writeFakeBinary(t, newBinary, 1<<20)

	inst := NewInstaller(binPath, filepath.Join(root, "backups"), dataDir)
	inst.SetMinBinarySize(1024)
	// 指向不存在的 systemd unit ⇒ 重启必然失败 ⇒ 触发回滚分支。
	inst.SetServiceName("llmgw-selfupdate-nonexistent-unit-test")

	result, err := inst.InstallWithOptions(context.Background(), newBinary,
		&Release{Version: "2.7.0"}, InstallOptions{
			VersionJSON: []byte(`{"version":"2.7.0","git_sha":"newsha","build_seq":3100}`),
		})
	if err == nil {
		t.Fatal("expected install to fail because the service cannot be restarted")
	}
	if result.Success {
		t.Fatal("result must not report success")
	}
	if !result.RolledBack {
		t.Error("result.RolledBack must be true so the caller can keep the reminder actionable")
	}

	// version.json 必须还原成旧内容
	raw, err := os.ReadFile(filepath.Join(dataDir, "version.json"))
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	if string(raw) != string(oldJSON) {
		t.Errorf("version.json not restored:\n got: %s\nwant: %s", raw, oldJSON)
	}
	// VERSION 也要还原
	legacy, err := os.ReadFile(filepath.Join(dataDir, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	if string(legacy) != "2.6.0\n" {
		t.Errorf("VERSION = %q, want 2.6.0", string(legacy))
	}
	// 二进制必须还原成旧的
	if sha256Hex(binPath) != oldBinarySum {
		t.Error("binary was not rolled back to the previous one")
	}
}

// TestNotifications_ListIncludesRolledBackReminders 提醒列表口径：
// 回滚后的提醒**必须仍在列表里**（它仍是待办），成功的才撤下。
// TestChecker_ReRemindsAfterManualRollback 装成功之后又被**手工回滚**到旧版本 ⇒
// 必须重新提醒（上一轮的 success 不该把提醒永久压掉）。
//
// ★ 场景：运维装了 2.7.0，发现有问题，于是手工回滚到 2.6.0。
//
//	此时远端说「有 2.7.0」⇒ 若检索器因为历史上已有一条 success 行就不提醒，
//	⇒ 节点就永远停在 2.6.0，且再也没人知道该升 2.7.0。
func TestChecker_ReRemindsAfterManualRollback(t *testing.T) {
	store := newStubStore()
	// 历史上装成功过 2.7.0
	id, err := store.CreateUpgradeLog(context.Background(), "gw-test", "2.6.0", "2.7.0")
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	if err := store.UpdateUpgradeLog(context.Background(), id, StatusSuccess, "", time.Now()); err != nil {
		t.Fatalf("update log: %v", err)
	}

	source := &stubSource{release: &RemoteRelease{Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))

	result := checker.CheckOnce(context.Background())
	if !result.Notified {
		t.Fatal("a version that was rolled back manually must be re-announced")
	}
	history, _, _ := store.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	actionable := 0
	for _, h := range history {
		if IsActionableStatus(h.Status) {
			actionable++
		}
	}
	if actionable == 0 {
		t.Fatal("expected an actionable reminder row after the manual rollback")
	}
}

func TestNotifications_ListIncludesRolledBackReminders(t *testing.T) {
	store := newStubStore()
	api := NewAdminAPI(store, nil, nil, nil)

	// 造三行：pending / rolled_back / success
	for _, st := range []string{StatusPending, StatusRollback, StatusSuccess} {
		id, err := store.CreateUpgradeLog(context.Background(), "gw-test", "2.6.0", "2.7.0")
		if err != nil {
			t.Fatalf("create log: %v", err)
		}
		if err := store.UpdateUpgradeLog(context.Background(), id, st, "", time.Now()); err != nil {
			t.Fatalf("update log: %v", err)
		}
	}

	ee := newEchoForAdminAPI(api)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/releases/notifications", nil)
	rec := httptest.NewRecorder()
	ee.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !containsAll(body, `"status":"pending"`, `"status":"rolled_back"`) {
		t.Errorf("notifications must include pending AND rolled_back rows, got %s", body)
	}
	if containsAll(body, `"status":"success"`) {
		t.Errorf("a successfully installed version must not stay in the reminder list, got %s", body)
	}
}
