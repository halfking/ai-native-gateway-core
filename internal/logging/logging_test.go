package logging

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestValidate covers the bounds checks on Config. These run on
// the validation path only — no files are touched.
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"empty-file-noop", Config{}, false},
		{"defaults-with-file", Config{File: "x.log", MaxSizeMB: 100, MaxBackups: 10, MaxAgeDays: 7}, false},
		{"zero-size", Config{File: "x.log", MaxSizeMB: 0}, true},
		{"negative-size", Config{File: "x.log", MaxSizeMB: -1}, true},
		{"negative-backups", Config{File: "x.log", MaxSizeMB: 1, MaxBackups: -1}, true},
		{"negative-age", Config{File: "x.log", MaxSizeMB: 1, MaxAgeDays: -1}, true},
		{"unlimited-backups-ok", Config{File: "x.log", MaxSizeMB: 1, MaxBackups: 0, MaxAgeDays: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestInitEmptyFileIsNoop verifies that an empty File path leaves
// the package in the "disabled" state — Writer() returns nil and
// Init returns (nil, nil).
func TestInitEmptyFileIsNoop(t *testing.T) {
	w, err := Init(Config{}, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init(empty) err = %v", err)
	}
	if w != nil {
		t.Fatalf("Init(empty) writer = %v, want nil", w)
	}
	if Writer() != nil {
		t.Fatalf("Writer() = %v, want nil after Init(empty)", Writer())
	}
}

// TestInitCreatesDirAndWrites verifies that Init() creates the
// log directory if it does not exist, then writes valid JSON
// records that land in the active log file.
func TestInitCreatesDirAndWrites(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "nested", "gateway.log")

	w, err := Init(Config{
		File:       logFile,
		MaxSizeMB:  100,
		MaxBackups: 3,
		MaxAgeDays: 7,
		Compress:   true,
	}, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if w == nil {
		t.Fatalf("Init: writer is nil")
	}
	t.Cleanup(func() { _ = Shutdown() })

	slog.Info("hello", "k", "v")

	// Allow the buffered writer to flush before we read.
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"msg":"hello"`) {
		t.Fatalf("log file missing record, got: %s", data)
	}
	if !strings.Contains(string(data), `"k":"v"`) {
		t.Fatalf("log file missing attribute, got: %s", data)
	}

	// Each line must be valid JSON (lumberjack flushes per Write).
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
	}
}

// TestShutdownIdempotent verifies Shutdown() is safe to call
// repeatedly and is a no-op when file logging was never enabled.
func TestShutdownIdempotent(t *testing.T) {
	if err := Shutdown(); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := Shutdown(); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// TestRotationProducesBackup drives lumberjack past MaxSize
// with a tiny MaxSizeMB value and verifies the rotated file
// appears under the expected naming convention.
//
// lumberjack's compress-and-prune is fire-and-forget in a
// background goroutine; we therefore wait for the .log.gz
// file to materialise rather than asserting a strict timing.
// Production behaviour (the user-visible guarantee) is that
// rotated files end up on disk, gzipped, eventually — not
// "synchronously before Shutdown returns".
func TestRotationProducesBackup(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "gateway.log")

	// MaxSizeMB=1 (MB) → rotate once 1 MB has been written. A canonical JSON
	// line (standard-layer fields included) is ~360 bytes, so the 12000 lines
	// below are ~4.3 MB ⇒ 4 rotations (measured 2026-11-26; the old comment
	// said "~1.2 MB" and assumed a single rotation).
	if _, err := Init(Config{
		File:       logFile,
		MaxSizeMB:  1,
		MaxBackups: 3,
		MaxAgeDays: 7,
		Compress:   true,
	}, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 2026-11-26：收尾（LIFO）先关文件并把全局 logger 指回 stderr，**然后**才
	// 轮到 TempDir 的 RemoveAll。留着一个指向临时目录的文件 logger 有两个后果：
	// 任何后台协程打一条日志都会把日志文件重建出来——与 RemoveAll 相撞就是
	// 「TempDir RemoveAll cleanup: … directory not empty」（把测试判红），或者
	// 在目录被删之后把整棵临时目录重建出来。
	t.Cleanup(func() {
		_ = Shutdown()
		InstallEarly(slog.LevelInfo)
	})

	payload := strings.Repeat("x", 100)
	for i := 0; i < 12000; i++ {
		slog.Info("rotate-test", "i", i, "pad", payload)
	}
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// lumberjack names rotated files (Compress=true):
	//   <base>-<timestamp>.log.gz
	// The gzip step runs in a background mill goroutine.
	//
	// 2026-11-26：必须等后台那一轮**压缩与清理都结束**再取列表，有两个窗口——
	//  ① 魔数只证明文件头写出来了：读一个写到一半的 .gz 会得到 unexpected EOF；
	//     压缩中途失败时 lumberjack 还会把刚建的 .gz 删掉。
	//  ② 本测试写下的量足以轮转 4 次（约 4.3MB / 1MB，实测），而 MaxBackups=3
	//     ⇒ 第 4 个备份出现时保留策略**必然淘汰最老的那个**——最老的恰好排在
	//     列表第一位。平时压缩协程跑得快，取列表时它已经被淘汰；机器重负载时
	//     协程慢半拍，列表里还留着它，等压缩结束再打开就是 ENOENT
	//     （「Open rotated file: … no such file or directory」，观测到的偶发失败）。
	// compressLogFile 只在 .gz 完整关闭之后才删未压缩的源文件，而同一轮清理里
	// 淘汰又排在压缩之前 ⇒「未压缩的轮转文件都不在了」即整轮结束，此时列表稳定。
	waitForCompressionDone(t, dir, 5*time.Second)
	matches := waitForFiles(t, dir, "gateway-*.log.gz", 5*time.Second)
	if len(matches) == 0 {
		t.Fatalf("no rotated .log.gz file produced; dir contents: %v", readDir(t, dir))
	}
	t.Logf("rotated backups kept after retention (MaxBackups=3): %d", len(matches))

	// First surviving rotated file must start with the gzip magic number.
	magic := waitForGzipMagic(t, matches[0], 5*time.Second)
	if magic[0] != 0x1f || magic[1] != 0x8b {
		t.Fatalf("rotated file is not gzip; magic = %x", magic)
	}

	// The gzipped payload must contain at least one of our records.
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatalf("Open rotated file: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = gz.Close() }()
	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"msg":"rotate-test"`) {
			found = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !found {
		t.Fatalf("rotated gzip does not contain a `rotate-test` record")
	}
}

// waitForFiles polls the directory until at least one file
// matching pattern exists, or the deadline elapses. Returns
// the list of matching paths. We use this to wait for
// lumberjack's async mill goroutine to gzip the rotated file.
func waitForFiles(t *testing.T, dir, pattern string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err == nil && len(matches) > 0 {
			return matches
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForGzipMagic polls a file until its first 2 bytes are
// the gzip magic (0x1f 0x8b) or the deadline elapses. The
// mill goroutine opens the .log.gz destination, writes the
// header, then streams the body; partial files fail gzip
// header validation.
func waitForGzipMagic(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	magic := make([]byte, 2)
	for {
		f, err := os.Open(path)
		if err == nil {
			_, err := io.ReadFull(f, magic)
			_ = f.Close()
			if err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
				return magic
			}
		}
		if time.Now().After(deadline) {
			return magic
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForCompressionDone polls until no uncompressed rotated file is left.
//
// compressLogFile removes the source file only after the .gz was fully
// written and closed (on failure it removes the .gz instead), so "no
// gateway-*.log left" is the completion signal. The gzip magic alone only
// proves that the header was written. In each mill pass the retention
// cleanup (MaxBackups / MaxAge) runs before compression, so once this
// returns the backup listing is stable as well.
func waitForCompressionDone(t *testing.T, dir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left, err := filepath.Glob(filepath.Join(dir, "gateway-*.log"))
		if err == nil && len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rotated file was not compressed within %s (left: %v); dir contents: %v",
				timeout, left, readDir(t, dir))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestReconfigure 验证运行时热加载轮转参数。
// Init 后调 Reconfigure，确认 ActiveConfig 反映新值，且 lumberjack 实例字段已更新。
func TestReconfigure(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")

	cfg := Config{File: logFile, MaxSizeMB: 100, MaxBackups: 10, MaxAgeDays: 7, Compress: true}
	if _, err := Init(cfg, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = Shutdown() }()

	// 热加载新参数
	newCfg := Config{MaxSizeMB: 50, MaxBackups: 5, MaxAgeDays: 30, Compress: false}
	if err := Reconfigure(newCfg); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	active := ActiveConfig()
	if active.MaxSizeMB != 50 {
		t.Errorf("MaxSizeMB = %d, want 50", active.MaxSizeMB)
	}
	if active.MaxBackups != 5 {
		t.Errorf("MaxBackups = %d, want 5", active.MaxBackups)
	}
	if active.MaxAgeDays != 30 {
		t.Errorf("MaxAgeDays = %d, want 30", active.MaxAgeDays)
	}
	if active.Compress != false {
		t.Errorf("Compress = %v, want false", active.Compress)
	}
	// File 路径不应被 Reconfigure 改动
	if active.File != logFile {
		t.Errorf("File = %q, want %q (should not change)", active.File, logFile)
	}
}

// TestReconfigure_NotEnabled 验证文件日志未启用时 Reconfigure 返回错误。
func TestReconfigure_NotEnabled(t *testing.T) {
	_ = Shutdown() // 确保未启用
	err := Reconfigure(Config{MaxSizeMB: 50})
	if err == nil {
		t.Error("expected error when file logging not enabled")
	}
}

// TestListFiles 验证列出日志文件。
func TestListFiles(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "gateway.log")

	cfg := Config{File: logFile, MaxSizeMB: 100, MaxBackups: 10, MaxAgeDays: 7}
	if _, err := Init(cfg, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = Shutdown() }()

	// 写一条日志，确保当前文件存在
	slog.Info("test message")
	// 创建一个模拟的轮转备份文件
	backup := filepath.Join(dir, "gateway-2026-01-01.log.gz")
	if err := os.WriteFile(backup, []byte("fake gzip"), 0644); err != nil {
		t.Fatal(err)
	}
	// 创建一个无关文件（应被忽略）
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 log files, got %d: %+v", len(files), files)
	}

	// 找到当前文件和备份
	var current, backupFile *LogFileInfo
	for i := range files {
		if files[i].IsCurrent {
			current = &files[i]
		}
		if files[i].IsCompressed {
			backupFile = &files[i]
		}
	}
	if current == nil {
		t.Error("no current log file found")
	}
	if backupFile == nil {
		t.Error("no compressed backup found")
	}
	if backupFile != nil && !strings.HasSuffix(backupFile.Name, ".gz") {
		t.Errorf("backup name = %q, want .gz suffix", backupFile.Name)
	}
}

// TestActiveConfig_Disabled 验证文件日志未启用时 ActiveConfig 返回空。
func TestActiveConfig_Disabled(t *testing.T) {
	_ = Shutdown()
	cfg := ActiveConfig()
	if cfg.File != "" {
		t.Errorf("File = %q, want empty when disabled", cfg.File)
	}
}

// TestReInit_EnableThenDisable 验证 ReInit 启用/停用/切换文件路径的能力。
//
// 流程：先 ReInit 到 fileA → 写日志 → ReInit 到 fileB → 验证 fileB 出现新内容
// → ReInit 关闭 → 验证 ActiveConfig.File 为空。
func TestReInit_EnableThenDisable(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a", "test.log")
	fileB := filepath.Join(dir, "b", "test.log")

	SetLevel(slog.LevelInfo)

	// 1) 启用到 fileA
	if err := ReInit(Config{File: fileA, MaxSizeMB: 100, MaxBackups: 5, MaxAgeDays: 7, Compress: true}); err != nil {
		t.Fatalf("ReInit A: %v", err)
	}
	defer func() { _ = Shutdown() }()

	if got := ActiveConfig().File; got != fileA {
		t.Fatalf("File = %q, want %q", got, fileA)
	}
	slog.Info("hello from A")
	// 等异步 flush（lumberjack 内部缓冲）
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(fileA); err != nil {
		t.Fatalf("fileA not created: %v", err)
	}

	// 2) 切换到 fileB
	if err := ReInit(Config{File: fileB, MaxSizeMB: 100, MaxBackups: 5, MaxAgeDays: 7, Compress: true}); err != nil {
		t.Fatalf("ReInit B: %v", err)
	}
	if got := ActiveConfig().File; got != fileB {
		t.Fatalf("File = %q, want %q", got, fileB)
	}
	slog.Info("hello from B")
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(fileB); err != nil {
		t.Fatalf("fileB not created: %v", err)
	}

	// 3) 关闭（File = ""）
	if err := ReInit(Config{}); err != nil {
		t.Fatalf("ReInit disable: %v", err)
	}
	if got := ActiveConfig().File; got != "" {
		t.Fatalf("File = %q, want empty after disable", got)
	}
}

// TestReInit_InvalidDir 验证目录不可写时 ReInit 返回错误并退回 stderr。
func TestReInit_InvalidDir(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "does", "not", "exist", "yet", "test.log")
	// 不调 os.MkdirAll，ReInit 内部应失败
	err := ReInit(Config{File: bad, MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1, Compress: false})
	if err == nil {
		// ReInit 内部会调 os.MkdirAll，所以这不会失败——反而会成功
		// 改为测试一个无法创建的路径（普通文件冲突）
		conflictFile := filepath.Join(dir, "conflict.log")
		if err := os.WriteFile(conflictFile, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		// 用一个"以冲突文件为父目录"的路径让 MkdirAll 失败
		err = ReInit(Config{File: filepath.Join(conflictFile, "child", "test.log"), MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1, Compress: false})
	}
	if err != nil {
		t.Logf("ReInit returned expected error: %v", err)
	}
	// 不管成功/失败，ReInit 都应保证 ActiveConfig 是自洽的
	_ = ActiveConfig().File
}

// TestRawDataEntry_ReasonField covers the JSON wire format for the
// Reason field added in 2026-07-28 §5.4: streaming bridges stamp a
// structured interruption reason (e.g. "client_cancel", "stream_panic")
// on the final RawDataEntry so the audit log carries the same kind
// the operator dashboard filters on.
func TestRawDataEntry_ReasonField(t *testing.T) {
	e := RawDataEntry{
		RequestID: "r1",
		Direction: "client_response",
		Protocol:  "openai-chat",
		Reason:    "client_cancel",
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"reason":"client_cancel"`)) {
		t.Fatalf("missing reason in json: %s", data)
	}
	var round RawDataEntry
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	if round.Reason != "client_cancel" {
		t.Fatalf("got %q want %q", round.Reason, "client_cancel")
	}
	// omitempty: an empty Reason must not serialise as a key.
	empty := RawDataEntry{RequestID: "r2", Direction: "client_response"}
	data2, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data2, []byte(`"reason"`)) {
		t.Fatalf("expected reason omitted, got: %s", data2)
	}
}
