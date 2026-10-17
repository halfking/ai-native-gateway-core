package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLevelSplitHandlerRoutesByLevel 钉住 configureLogging 的分流契约。
//
// 背景（2026-10-17 实测 245）：本服务此前从不调用 slog.SetDefault，
// Go 默认处理器把所有级别写 stderr，于是 quality-service.error.log 里
// 混进 358 万行 INFO，而同期真实错误只有 74 条 —— 告警信号被淹没。
// 详见 main.go configureLogging 的注释。
//
// 这条判据与被测函数同层（直接驱动 levelSplitHandler），并且覆盖
// With / WithGroup：这两个方法若只返回单侧 handler，就会把后续日志
// 全导到一条流里，而主路径（logger.New → slog.Default().With("module",…)）
// 恰好**每次都经过 With**，所以它是必经之路而非边角。
func TestLevelSplitHandlerRoutesByLevel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	h := &levelSplitHandler{
		info:  slog.NewJSONHandler(&stdout, opts),
		error: slog.NewJSONHandler(&stderr, opts),
	}
	l := slog.New(h)

	l.Debug("d", "k", 1)
	l.Info("i", "k", 2)
	l.Warn("w", "k", 3)
	l.Error("e", "k", 4)

	// Info/Debug 进 stdout。
	for _, want := range []string{`"msg":"d"`, `"msg":"i"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %s:\n%s", want, stdout.String())
		}
	}
	// Warn/Error 进 stderr。
	for _, want := range []string{`"msg":"w"`, `"msg":"e"`} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %s:\n%s", want, stderr.String())
		}
	}
	// 两条流互不串门——这是本修复的全部意义。
	for _, bad := range []string{`"msg":"d"`, `"msg":"i"`} {
		if strings.Contains(stderr.String(), bad) {
			t.Errorf("stderr must not receive %s:\n%s", bad, stderr.String())
		}
	}
	for _, bad := range []string{`"msg":"w"`, `"msg":"e"`} {
		if strings.Contains(stdout.String(), bad) {
			t.Errorf("stdout must not receive %s:\n%s", bad, stdout.String())
		}
	}

	// With / WithGroup 之后分流必须依然成立（主路径每次都过 With）。
	l.With("module", "quality-service").Info("with-info")
	l.With("module", "quality-service").Error("with-error")
	l.WithGroup("g").With("x", "y").Info("grouped-info")
	l.WithGroup("g").With("x", "y").Error("grouped-error")

	if !strings.Contains(stdout.String(), "with-info") || !strings.Contains(stdout.String(), "grouped-info") {
		t.Errorf("With/WithGroup lost the stdout side:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "with-error") || strings.Contains(stdout.String(), "grouped-error") {
		t.Errorf("With/WithGroup leaked errors into stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "with-error") || !strings.Contains(stderr.String(), "grouped-error") {
		t.Errorf("With/WithGroup lost the stderr side:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "with-info") || strings.Contains(stderr.String(), "grouped-info") {
		t.Errorf("With/WithGroup leaked info into stderr:\n%s", stderr.String())
	}
}

// TestLevelSplitHandlerRespectsLevelFilter 确认两条流共用同一个级别门：
// -log-level=error 时 Info 既不该进 stdout，也不该进 stderr。
func TestLevelSplitHandlerRespectsLevelFilter(t *testing.T) {
	var stdout, stderr bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelError}
	h := &levelSplitHandler{
		info:  slog.NewJSONHandler(&stdout, opts),
		error: slog.NewJSONHandler(&stderr, opts),
	}
	l := slog.New(h)

	l.Info("filtered-info")
	l.Error("kept-error")

	if strings.Contains(stdout.String(), "filtered-info") {
		t.Errorf("info must be filtered at -log-level=error:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "kept-error") {
		t.Errorf("error must survive the filter:\n%s", stderr.String())
	}
}

// TestParseLogLevel 钉住 -log-level 的取值解析：未知值必须落到 info
// 而不是静默变成「什么都不记」或「全都记」。
func TestParseLogLevel(t *testing.T) {
	if got := parseLogLevel("debug"); got != slog.LevelDebug {
		t.Errorf("parseLogLevel(debug) = %v", got)
	}
	if got := parseLogLevel("  WARN "); got != slog.LevelWarn {
		t.Errorf("parseLogLevel(\"  WARN \") = %v, want warn", got)
	}
	if got := parseLogLevel("error"); got != slog.LevelError {
		t.Errorf("parseLogLevel(error) = %v", got)
	}
	if got := parseLogLevel("nonsense"); got != slog.LevelInfo {
		t.Errorf("parseLogLevel(nonsense) = %v, want the safe default info", got)
	}
}
