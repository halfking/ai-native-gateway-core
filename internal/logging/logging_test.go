package logging

import (
	"bufio"
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

	// MaxSizeMB=1 (MB) → rotate after ~1 MB written. We write
	// ~1.2 MB of structured lines to force at least one rotation.
	if _, err := Init(Config{
		File:       logFile,
		MaxSizeMB:  1,
		MaxBackups: 3,
		MaxAgeDays: 7,
		Compress:   true,
	}, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	// Each line ~120 bytes → ~10k lines per MB.
	payload := strings.Repeat("x", 100)
	for i := 0; i < 12000; i++ {
		slog.Info("rotate-test", "i", i, "pad", payload)
	}
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// lumberjack names rotated files (Compress=true):
	//   <base>-<timestamp>.log.gz
	// The gzip step runs in a background mill goroutine; we
	// wait up to 5s for it to materialise. Without the wait,
	// the test would race the goroutine on fast machines.
	matches := waitForFiles(t, dir, "gateway-*.log.gz", 5*time.Second)
	if len(matches) == 0 {
		t.Fatalf("no rotated .log.gz file produced; dir contents: %v", readDir(t, dir))
	}

	// First rotated file must start with the gzip magic number.
	// If the mill goroutine is still running, the file may be
	// partially written; we retry with a short backoff.
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
	defer gz.Close()
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
			f.Close()
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
	defer Shutdown()

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
	Shutdown() // 确保未启用
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
	defer Shutdown()

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
	Shutdown()
	cfg := ActiveConfig()
	if cfg.File != "" {
		t.Errorf("File = %q, want empty when disabled", cfg.File)
	}
}
