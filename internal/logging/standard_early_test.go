package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// captureStderr 替换 os.Stderr 为管道，测试结束时还原。
func captureStderr(t *testing.T) func() []byte {
	t.Helper()
	savedLogger := slog.Default()
	savedStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = savedStderr
		slog.SetDefault(savedLogger)
	})
	return func() []byte {
		_ = w.Close()
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read pipe: %v", err)
		}
		_ = r.Close()
		return out
	}
}

// assertStandardLine 断言一行日志带齐 rule 71 的必填字段。
func assertStandardLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &obj); err != nil {
		t.Fatalf("不是合法 JSON: %q -- %v", line, err)
	}
	for _, key := range []string{
		"timestamp", "severity_text", "severity_number", "level",
		"service", "version", "env", "instance_id",
	} {
		v, ok := obj[key]
		if !ok {
			t.Errorf("缺必填字段 %q: %s", key, line)
			continue
		}
		if v == nil || v == "" {
			t.Errorf("必填字段 %q 为空: %s", key, line)
		}
	}
	return obj
}

// TestInitWithoutFileStillInstallsStandardLayer 锁住「不落盘也要有规范层」。
//
// 回归背景：Init 在 cfg.File == "" 时曾是 no-op（直接 return），
// 于是 slog 留在默认的 **TextHandler** 上 ⇒ 打出来的行是
// `2026/11/09 12:00:00 INFO msg k=v` 这种纯文本，
// 既没有 service/version/env/instance_id，也没有 severity_text/severity_number。
//
// 而「不配置 LLM_GATEWAY_LOG_FILE」是**正常配置**（容器化部署由采集器收 stdout），
// 因此这条路径必须同样合规。
func TestInitWithoutFileStillInstallsStandardLayer(t *testing.T) {
	read := captureStderr(t)
	if _, err := Init(Config{File: ""}, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	slog.Info("no-file-mode")
	out := read()
	if len(out) == 0 {
		t.Fatal("没有输出")
	}
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	assertStandardLine(t, first)
}

// TestInstallEarlyInstallsStandardLayer 锁住「配置加载之前也能有规范层」。
//
// 回归背景：cmd/gateway 的 loadHardBlockPolicy() 在 config.Load() 之前打日志，
// 而规范层此前要等 logging.Init（config.Load() 之后）才装 ⇒ 早期那几行是旧格式，
// 同一进程两种格式。
func TestInstallEarlyInstallsStandardLayer(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Info("early-window")
	out := read()
	if len(out) == 0 {
		t.Fatal("没有输出")
	}
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	assertStandardLine(t, first)
}

// TestEarlyInstallThenFullInitKeepsStandardLayer 锁住「先早装、再装文件管线，
// 规范层不能被退回」。
//
// 这是真实的启动顺序：InstallEarly（stderr）→ config.Load() → Init（落盘管线）。
// 若后者没有再次叠加规范层，落盘与 stderr 的形态就会分叉。
func TestEarlyInstallThenFullInitKeepsStandardLayer(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Info("early-line")

	dir := t.TempDir()
	if _, err := Init(Config{
		File:       dir + "/gateway.log",
		MaxSizeMB:  1,
		MaxBackups: 1,
		MaxAgeDays: 1,
		Compress:   false,
	}, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = Shutdown() }()
	slog.Info("after-file-init")

	out := read()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("输出行数不足: %d (%q)", len(lines), out)
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		assertStandardLine(t, line)
	}
}

// TestStandardLevelIsLowercaseAlias 锁住 level 必须是 severity_text 的小写形态。
//
// 下游 LogQL 用 level=~"error|fatal" 过滤；大写会静默漏掉全部错误日志。
func TestStandardLevelIsLowercaseAlias(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Error("boom")
	out := read()
	obj := assertStandardLine(t, strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	if obj["severity_text"] != "ERROR" {
		t.Errorf("severity_text = %v, want ERROR", obj["severity_text"])
	}
	if obj["level"] != "error" {
		t.Errorf("level = %v, want error（小写）", obj["level"])
	}
	if obj["severity_number"] != float64(17) {
		t.Errorf("severity_number = %v, want 17", obj["severity_number"])
	}
}

// TestStandardRedactsEarlyWindow 确认早装窗口同样脱敏。
func TestStandardRedactsEarlyWindow(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Info("secret", "password", "hunter2")
	out := read()
	if bytes.Contains(out, []byte("hunter2")) {
		t.Errorf("早期窗口未脱敏: %q", out)
	}
}
