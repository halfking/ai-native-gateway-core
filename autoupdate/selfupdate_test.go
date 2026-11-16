package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── 测试替身 ────────────────────────────────────────────────────────────

// stubStore 是覆盖自更新链路所需写面的内存 Store。
type stubStore struct {
	mu       sync.Mutex
	releases map[string]*Release
	logs     []ReleaseStatus
	nextID   int64
}

func newStubStore() *stubStore {
	return &stubStore{releases: map[string]*Release{}}
}

func (s *stubStore) CreateRelease(ctx context.Context, rel *Release) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.releases[rel.Version]; exists {
		return fmt.Errorf("duplicate version %s", rel.Version)
	}
	s.nextID++
	rel.ID = s.nextID
	rel.CreatedAt = time.Now()
	s.releases[rel.Version] = rel
	return nil
}

func (s *stubStore) GetRelease(ctx context.Context, version string) (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, ok := s.releases[version]
	if !ok {
		return nil, fmt.Errorf("release not found: %s", version)
	}
	return rel, nil
}

func (s *stubStore) GetLatestRelease(ctx context.Context, channel Channel) (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Release
	for _, r := range s.releases {
		if r.Channel != channel || r.PublishedAt == nil {
			continue
		}
		if best == nil || r.BuildSeq > best.BuildSeq {
			best = r
		}
	}
	return best, nil
}

func (s *stubStore) GetLatestReleaseAfter(ctx context.Context, channel Channel, currentBuildSeq int) (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Release
	for _, r := range s.releases {
		if r.Channel != channel || r.PublishedAt == nil || r.BuildSeq <= currentBuildSeq {
			continue
		}
		if best == nil || r.BuildSeq > best.BuildSeq {
			best = r
		}
	}
	return best, nil
}

func (s *stubStore) ListReleases(ctx context.Context, channel Channel, offset, limit int) ([]Release, int, error) {
	return nil, 0, nil
}

func (s *stubStore) UpdateReleaseStatus(ctx context.Context, id int64, published bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.releases {
		if r.ID != id {
			continue
		}
		if published {
			now := time.Now()
			r.PublishedAt = &now
		} else {
			r.PublishedAt = nil
		}
		return nil
	}
	return fmt.Errorf("release id not found: %d", id)
}

func (s *stubStore) CreateGrayRule(ctx context.Context, rule *GrayReleaseRule) error { return nil }
func (s *stubStore) GetGrayRule(ctx context.Context, releaseID int64) (*GrayReleaseRule, error) {
	return nil, nil
}
func (s *stubStore) ListGrayRules(ctx context.Context, limit int) ([]GrayReleaseRuleView, error) {
	return nil, nil
}
func (s *stubStore) UpdateGrayPhase(ctx context.Context, releaseID int64, phase Phase, percent int) error {
	return nil
}
func (s *stubStore) UpdateGrayRuleStatus(ctx context.Context, releaseID int64, status string) error {
	return nil
}
func (s *stubStore) GetRolloutStats(ctx context.Context, version string) (RolloutStats, error) {
	return RolloutStats{}, nil
}

func (s *stubStore) CreateUpgradeLog(ctx context.Context, instanceID string, oldVer, newVer string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.logs = append(s.logs, ReleaseStatus{
		ReleaseID:  nil,
		LogID:      s.nextID,
		InstanceID: instanceID,
		Status:     StatusPending,
		Version:    newVer,
		StartedAt:  time.Now(),
	})
	return s.nextID, nil
}

func (s *stubStore) UpdateUpgradeLog(ctx context.Context, id int64, status string, errMsg string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.logs {
		if s.logs[i].LogID != id {
			continue
		}
		s.logs[i].Status = status
		s.logs[i].Error = errMsg
		if !completedAt.IsZero() {
			s.logs[i].CompletedAt = &completedAt
		}
		return nil
	}
	return fmt.Errorf("upgrade log id not found: %d", id)
}

func (s *stubStore) GetUpgradeHistory(ctx context.Context, instanceID string, offset, limit int) ([]ReleaseStatus, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReleaseStatus, 0, len(s.logs))
	for _, l := range s.logs {
		if instanceID != "" && l.InstanceID != instanceID {
			continue
		}
		out = append(out, l)
	}
	return out, len(out), nil
}

func (s *stubStore) GetInstanceStatus(ctx context.Context, instanceID string) (*ReleaseStatus, error) {
	return nil, nil
}
func (s *stubStore) UpdateInstanceStatus(ctx context.Context, status *ReleaseStatus) error {
	return nil
}
func (s *stubStore) RecordUpdateReport(ctx context.Context, report *UpdateReportData) error {
	return nil
}

// stubSource 是可控的远端版本源。
type stubSource struct {
	mu       sync.Mutex
	release  *RemoteRelease
	err      error
	callSeen int
}

func (s *stubSource) Latest(ctx context.Context, currentVersion string, currentBuildSeq int, channel Channel) (*RemoteRelease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callSeen++
	if s.err != nil {
		return nil, s.err
	}
	return s.release, nil
}

func (s *stubSource) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callSeen
}

// writeFakeBinary 写一个「看起来像二进制」的文件（ELF 魔数 + 填充）。
func writeFakeBinary(t *testing.T, path string, size int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	buf := make([]byte, size)
	copy(buf, []byte{0x7f, 'E', 'L', 'F'})
	if err := os.WriteFile(path, buf, 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return sha256Hex(path)
}

func sha256Hex(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ── 远端源 ──────────────────────────────────────────────────────────────

func TestHTTPRemoteSource_Latest_UpToDateReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/updates/latest") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"up_to_date":true}`))
	}))
	defer srv.Close()

	src := NewHTTPRemoteSource(srv.URL)
	rel, err := src.Latest(context.Background(), "2.6.0", 3040, ChannelStable)
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if rel != nil {
		t.Fatalf("expected nil release when up to date, got %+v", rel)
	}
}

func TestHTTPRemoteSource_Latest_NewVersionResolvesArtifact(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/updates/latest"):
			_, _ = w.Write([]byte(`{"version":"2.7.0","build_seq":3100,"channel":"stable","mandatory":true,"sha256":"abc","release_notes":"notes"}`))
		case strings.HasSuffix(r.URL.Path, "/api/v1/updates/manifest"):
			if r.URL.Query().Get("v") != "2.7.0" {
				t.Errorf("manifest version query = %q", r.URL.Query().Get("v"))
			}
			_, _ = w.Write([]byte(`{"version":"2.7.0","build_seq":3100,"binary_url":"https://example.test/bin/2.7.0","binary_sha256":"def","changelog":"cl"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	src := NewHTTPRemoteSource(srv.URL, WithTokenFunc(func() string { return "tok-123" }))
	rel, err := src.Latest(context.Background(), "2.6.0", 3040, ChannelStable)
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if rel == nil {
		t.Fatal("expected a release, got nil")
	}
	if rel.Version != "2.7.0" || rel.BuildSeq != 3100 {
		t.Fatalf("unexpected release identity: %+v", rel)
	}
	if !rel.Mandatory {
		t.Error("expected mandatory=true to be carried through")
	}
	if rel.BinaryURL != "https://example.test/bin/2.7.0" {
		t.Errorf("binary url not resolved: %q", rel.BinaryURL)
	}
	if rel.BinarySHA256 != "def" {
		t.Errorf("binary sha256 not resolved: %q", rel.BinarySHA256)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("authorization header = %q", gotAuth)
	}
}

func TestHTTPRemoteSource_Latest_UpstreamErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	src := NewHTTPRemoteSource(srv.URL)
	if _, err := src.Latest(context.Background(), "2.6.0", 3040, ChannelStable); err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestHTTPRemoteSource_Latest_EmptyBaseURLIsError(t *testing.T) {
	src := NewHTTPRemoteSource("")
	if _, err := src.Latest(context.Background(), "2.6.0", 3040, ChannelStable); err == nil {
		t.Fatal("expected error when base URL is not configured")
	}
}

// ── 检查器（自动检索 + 提醒）────────────────────────────────────────────

// stubAlertSink 记录告警投递。
type stubAlertSink struct {
	mu       sync.Mutex
	calls    int
	version  string
	severity string
}

func (s *stubAlertSink) RaiseUpdateAvailableAlert(ctx context.Context, instanceID, version, notes, severity string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.version = version
	s.severity = severity
	return 1, true, nil
}

func TestChecker_CheckOnce_RaisesOpsAlert(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable, Mandatory: true,
		ReleaseNotes: "security fix",
	}}
	sink := &stubAlertSink{}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"), WithAlertSink(sink))

	checker.CheckOnce(context.Background())

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.calls != 1 {
		t.Fatalf("expected 1 alert, got %d", sink.calls)
	}
	if sink.version != "2.7.0" {
		t.Errorf("alert version = %q", sink.version)
	}
	if sink.severity != "warning" {
		t.Errorf("mandatory release should raise a warning alert, got %q", sink.severity)
	}
}

func TestChecker_CheckOnce_NonMandatoryRaisesInfoAlert(t *testing.T) {
	sink := &stubAlertSink{}
	source := &stubSource{release: &RemoteRelease{Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable}}
	checker := NewChecker(newStubStore(), source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithAlertSink(sink))

	checker.CheckOnce(context.Background())

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.severity != "info" {
		t.Errorf("severity = %q, want info", sink.severity)
	}
}

func TestChecker_CheckOnce_PersistsReleaseAndCreatesReminder(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{
		Version:      "2.7.0",
		BuildSeq:     3100,
		Channel:      ChannelStable,
		Mandatory:    false,
		ReleaseNotes: "shiny",
		BinaryURL:    "https://example.test/bin",
		BinarySHA256: "sha",
	}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))

	result := checker.CheckOnce(context.Background())
	if result.Error != "" {
		t.Fatalf("unexpected check error: %s", result.Error)
	}
	if result.UpToDate {
		t.Fatal("expected up_to_date=false")
	}
	if !result.Notified {
		t.Fatal("expected a reminder to be created")
	}

	rel, err := store.GetRelease(context.Background(), "2.7.0")
	if err != nil {
		t.Fatalf("release was not persisted: %v", err)
	}
	if rel.PublishedAt == nil {
		t.Error("persisted release must be published so read paths can see it")
	}
	if rel.BuildSeq != 3100 {
		t.Errorf("build seq = %d", rel.BuildSeq)
	}

	history, _, err := store.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected exactly 1 reminder, got %d", len(history))
	}
	if history[0].Status != StatusPending {
		t.Errorf("reminder status = %q, want %q", history[0].Status, StatusPending)
	}
	if history[0].Version != "2.7.0" {
		t.Errorf("reminder version = %q", history[0].Version)
	}
}

func TestChecker_CheckOnce_DoesNotDuplicateReminder(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))

	first := checker.CheckOnce(context.Background())
	if !first.Notified {
		t.Fatal("first check must notify")
	}
	second := checker.CheckOnce(context.Background())
	if second.Notified {
		t.Fatal("second check must not notify again for the same version")
	}

	history, _, _ := store.GetUpgradeHistory(context.Background(), "gw-test", 0, 50)
	if len(history) != 1 {
		t.Fatalf("expected exactly 1 reminder after two checks, got %d", len(history))
	}
}

func TestChecker_CheckOnce_UpToDateCreatesNoReminder(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: nil}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable)

	result := checker.CheckOnce(context.Background())
	if !result.UpToDate {
		t.Fatal("expected up_to_date=true")
	}
	if result.Notified {
		t.Fatal("no reminder expected when up to date")
	}
	history, _, _ := store.GetUpgradeHistory(context.Background(), "", 0, 50)
	if len(history) != 0 {
		t.Fatalf("expected no reminders, got %d", len(history))
	}
}

func TestChecker_CheckOnce_SourceErrorIsReportedNotSwallowed(t *testing.T) {
	store := newStubStore()
	source := &stubSource{err: fmt.Errorf("network down")}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable)

	result := checker.CheckOnce(context.Background())
	if result.Error == "" {
		t.Fatal("expected the source error to be surfaced")
	}
	if result.UpToDate {
		t.Fatal("a failed check must not be reported as up-to-date")
	}
}

// failCreateStore 模拟「检索成功但本地落库失败」。
type failCreateStore struct {
	*stubStore
}

func (s *failCreateStore) CreateRelease(ctx context.Context, rel *Release) error {
	return fmt.Errorf("local store is read-only")
}

// TestChecker_CheckOnce_PersistFailureIsWarningNotError 钉住「致命 / 非致命」的区分：
// 落库失败**不改变**「有没有新版」的结论，所以它只能进 Warning。
//
// 若混进 Error，界面会把「本地没写进去」读成「连远端都没查成」，
// 于是「有更新」这一事实被一起抹掉 —— 运维因此跳过补丁。
func TestChecker_CheckOnce_PersistFailureIsWarningNotError(t *testing.T) {
	store := &failCreateStore{stubStore: newStubStore()}
	source := &stubSource{release: &RemoteRelease{Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable)

	result := checker.CheckOnce(context.Background())
	if result.Error != "" {
		t.Fatalf("persist failure must not be reported as a failed check: %q", result.Error)
	}
	if result.Warning == "" {
		t.Fatal("expected a warning for the persist failure")
	}
	if result.Latest == nil {
		t.Fatal("the discovered release must still be reported")
	}
	if result.UpToDate {
		t.Fatal("a discovered newer version must not be reported as up-to-date")
	}
}

// ── 安装器（校验契约是本轮根修的核心）──────────────────────────────────

// TestInstaller_VerifyBinary_AcceptsBinaryWithoutVersionFlag 是 F2 的回归钉测。
//
// 旧实现直接跑 `<binary> --version`，被装的二进制不支持该参数时校验恒失败
// ⇒ 安装 100% 回滚。测试二进制（go test 编译产物）对 --version 会因
// 「flag provided but not defined」而非零退出 —— 正好复刻旧实现踩的坑。
func TestInstaller_VerifyBinary_AcceptsBinaryWithoutVersionFlag(t *testing.T) {
	inst := NewInstaller("/tmp/does-not-matter", t.TempDir(), t.TempDir())
	inst.SetMinBinarySize(1024)

	if err := inst.verifyBinary(os.Args[0]); err != nil {
		t.Fatalf("binary that rejects --version must still pass verification: %v", err)
	}
}

func TestInstaller_VerifyBinary_RejectsNonExecutablePayload(t *testing.T) {
	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "error.html")
	// 一个下到的 404 HTML 页面：体积够大，但不是可执行格式。
	if err := os.WriteFile(htmlPath, []byte("<html><body>404 not found</body></html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	inst := NewInstaller("/tmp/x", dir, dir)
	inst.SetMinBinarySize(16)

	if err := inst.verifyBinary(htmlPath); err == nil {
		t.Fatal("expected non-executable payload to be rejected")
	}
}

func TestInstaller_VerifyBinary_RejectsTooSmallFile(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "tiny")
	if err := os.WriteFile(small, []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	inst := NewInstaller("/tmp/x", dir, dir)
	inst.SetMinBinarySize(1 << 20)

	if err := inst.verifyBinary(small); err == nil {
		t.Fatal("expected undersized file to be rejected")
	}
}

func TestInstaller_Install_BackupReplaceAndVersionMetadata(t *testing.T) {
	root := t.TempDir()
	binPath := filepath.Join(root, "llm-gateway-go")
	backupDir := filepath.Join(root, "backups")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 当前版本（真源 = version.json）
	if err := os.WriteFile(filepath.Join(dataDir, "version.json"), []byte(`{"version":"2.6.0","git_sha":"abc","build_seq":3040}`), 0o644); err != nil {
		t.Fatalf("write version.json: %v", err)
	}
	// 旧二进制（复制测试二进制，体积足够）
	oldBytes, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(binPath, oldBytes, 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}

	newBinary := filepath.Join(root, "new-binary")
	writeFakeBinary(t, newBinary, 1<<20)

	inst := NewInstaller(binPath, backupDir, dataDir)
	inst.SetMinBinarySize(1024)

	result, err := inst.InstallWithOptions(context.Background(), newBinary,
		&Release{Version: "2.7.0"}, InstallOptions{
			VersionJSON: []byte(`{"version":"2.7.0","git_sha":"def","build_seq":3100}`),
		})
	if err != nil {
		t.Fatalf("install failed: %v (result=%+v)", err, result)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.OldVersion != "2.6.0" {
		t.Errorf("old version = %q, want 2.6.0 (version.json is the SSOT)", result.OldVersion)
	}
	if result.BackupPath == "" || !fileExists(result.BackupPath) {
		t.Fatalf("backup not created: %q", result.BackupPath)
	}
	// 新二进制已就位
	if sha256Hex(binPath) != sha256Hex(newBinary) {
		t.Error("binary was not replaced")
	}
	// 版本真源已更新
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
	if info.Version != "2.7.0" {
		t.Errorf("version.json version = %q, want 2.7.0", info.Version)
	}
	// 兼容文件也写了
	legacy, err := os.ReadFile(filepath.Join(dataDir, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	if strings.TrimSpace(string(legacy)) != "2.7.0" {
		t.Errorf("VERSION = %q", strings.TrimSpace(string(legacy)))
	}
}

func TestInstaller_Install_RejectsGarbageAndKeepsOldBinary(t *testing.T) {
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
	before := sha256Hex(binPath)

	garbage := filepath.Join(root, "garbage")
	if err := os.WriteFile(garbage, []byte("<html>nope</html>"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	inst := NewInstaller(binPath, filepath.Join(root, "backups"), dataDir)
	inst.SetMinBinarySize(16)

	result, err := inst.InstallWithOptions(context.Background(), garbage, &Release{Version: "9.9.9"}, InstallOptions{})
	if err == nil {
		t.Fatal("expected install of a garbage payload to fail")
	}
	if result.Success {
		t.Fatal("result must not report success")
	}
	if sha256Hex(binPath) != before {
		t.Error("old binary must be left untouched after a failed verification")
	}
}

// ── 执行器（点击安装升级）───────────────────────────────────────────────

func TestExecutor_ArtifactInstallEndToEnd(t *testing.T) {
	root := t.TempDir()
	binPath := filepath.Join(root, "llm-gateway-go")
	dataDir := filepath.Join(root, "data")
	downloadDir := filepath.Join(root, "downloads")
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

	// 制品服务：提供「像二进制」的文件
	artifact := filepath.Join(root, "artifact.bin")
	sum := writeFakeBinary(t, artifact, 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, artifact)
	}))
	defer srv.Close()

	store := newStubStore()
	if err := store.CreateRelease(context.Background(), &Release{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
		ImageDigest: sum, ImageTag: "2.7.0", Title: "2.7.0", CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed release: %v", err)
	}

	installer := NewInstaller(binPath, filepath.Join(root, "backups"), dataDir)
	installer.SetMinBinarySize(1024)
	downloader := NewDownloader(downloadDir)

	exec := NewExecutor(store, downloader, installer, func() (string, int) { return "2.6.0", 3040 },
		WithArtifactURLTemplate(srv.URL+"/{version}"),
		WithExecutorInstanceID("gw-test"))

	job, err := exec.Start(context.Background(), InstallRequest{Version: "2.7.0"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if job.Status == StatusPending {
		// 任务在后台执行，等它落地
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			cur := exec.Current()
			if cur != nil && isTerminalStatus(cur.Status) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	cur := exec.Current()
	if cur == nil {
		t.Fatal("no job snapshot")
	}
	if cur.Status != StatusSuccess {
		t.Fatalf("job status = %q error = %q steps = %v", cur.Status, cur.Error, cur.Steps)
	}
	if sha256Hex(binPath) != sum {
		t.Error("binary was not replaced by the downloaded artifact")
	}
}

func TestExecutor_Start_RejectsConcurrentTask(t *testing.T) {
	root := t.TempDir()
	binPath := filepath.Join(root, "llm-gateway-go")
	if err := os.WriteFile(binPath, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := newStubStore()
	installer := NewInstaller(binPath, filepath.Join(root, "backups"), filepath.Join(root, "data"))
	exec := NewExecutor(store, NewDownloader(filepath.Join(root, "dl")), installer,
		func() (string, int) { return "2.6.0", 3040 })

	// 直接把任务状态置成运行中，模拟已有任务
	exec.mu.Lock()
	exec.current = &InstallJob{ID: "install-1", Status: StatusUpgrading}
	exec.mu.Unlock()

	if _, err := exec.Start(context.Background(), InstallRequest{Version: "2.7.0"}); err == nil {
		t.Fatal("expected concurrent install to be rejected")
	}
}

func TestExecutor_Start_RequiresVersion(t *testing.T) {
	exec := NewExecutor(newStubStore(), nil, nil, nil)
	if _, err := exec.Start(context.Background(), InstallRequest{}); err == nil {
		t.Fatal("expected missing version to be rejected")
	}
}

func TestExecutor_SourceMode_RequiresBuilder(t *testing.T) {
	exec := NewExecutor(newStubStore(), nil, nil, nil)
	if _, err := exec.Start(context.Background(), InstallRequest{
		Version: "2.7.0", Mode: InstallModeSource,
	}); err == nil {
		t.Fatal("expected source build without a builder to be rejected")
	}
}

// ── 源码构建器 ─────────────────────────────────────────────────────────

func TestSourceBuilder_Build_NonRepoDirIsError(t *testing.T) {
	builder := NewSourceBuilder(WithSourceRepoDir(t.TempDir()))
	if _, err := builder.Build(context.Background(), SourceBuildRequest{}); err == nil {
		t.Fatal("expected an error for a directory that is not a source tree")
	}
}

func TestSourceBuilder_Build_EmptyRepoDirIsError(t *testing.T) {
	builder := NewSourceBuilder()
	if _, err := builder.Build(context.Background(), SourceBuildRequest{}); err == nil {
		t.Fatal("expected an error when no repo dir is configured")
	}
}

// TestSourceBuilder_Build_FromGitRefUsesWorktree 是 F11 的回归钉测。
//
// 早期实现把 git 与 go 共用同一个执行入口 ⇒ `git worktree add` 被执行成
// `go worktree add` ⇒ 报「go worktree: unknown command」。
// 这条错只有**真跑一次源码构建**才会暴露，错误路径单测抓不到。
//
// 这里用**临时 git 仓库 + 一个极小的 go 程序**（秒级编译）把整条路径钉住：
// 检出 ref ⇒ 编译 ⇒ 产物可执行 ⇒ 工作树未被改动。
func TestSourceBuilder_Build_FromGitRefUsesWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}

	repo := t.TempDir()
	writeFile := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	writeFile("go.mod", "module demo\n\ngo 1.21\n")
	writeFile("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"demo\") }\n")

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "demo")

	builder := NewSourceBuilder(WithSourceRepoDir(repo))
	result, err := builder.Build(context.Background(), SourceBuildRequest{
		Ref:     "HEAD",
		Package: ".",
	})
	if err != nil {
		t.Fatalf("source build failed: %v", err)
	}
	if !result.UsedWorktree {
		t.Error("expected the build to use a detached git worktree")
	}
	if !fileExists(result.BinaryPath) {
		t.Fatalf("binary not produced at %s", result.BinaryPath)
	}
	// 用户工作树必须保持原样（不出现额外分支/改动）
	git("status", "--porcelain")
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput(); err != nil || len(out) != 0 {
		t.Errorf("source build must not dirty the user worktree: %s", out)
	}
}
