package logger_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

// standardFactory 构造规范层 handler（与 cmd/gateway 注入的工厂同构）。
func standardFactory(w io.Writer, level slog.Level) slog.Handler {
	h, err := logging.NewStandardHandler(w, logging.StandardOptions{
		Service: logging.ServiceName(),
		Version: logging.ServiceVersion(),
		Env:     logging.ServiceEnv(),
		Level:   level,
		// 保留名优先：规范层注入的 instance_id 会覆盖业务侧传的同名属性，
		// 所以要显式把它交给规范层，而不是指望业务属性留下来。
		InstanceID: "inst-1",
	})
	if err != nil {
		return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	}
	return h
}

// TestPersistentLoggerEmitsStandardFields 锁住「持久化日志与主日志同一套字段」。
//
// 回归背景：critical/error/panic/shutdown/audit 五个文件此前走裸 JSONHandler——
// 字段名是 time（不是 timestamp）、时间是本地时区、且没有
// service/version/env/instance_id/severity_text/severity_number。
// 事故复盘时读这些文件的正是人和 AI，形态必须与主日志一致（rule 71 §7.5）。
func TestPersistentLoggerEmitsStandardFields(t *testing.T) {
	dir := t.TempDir()
	// 用 NewPersistentLogger 而非 InitPersistentLogger：
	// 后者是 sync.Once 单例，同一测试二进制里第二次 init 不生效。
	pl, err := logger.NewPersistentLogger(logger.PersistentLoggerConfig{
		LogDir:         dir,
		MaxSize:        1,
		MaxBackups:     1,
		MaxAge:         1,
		Compress:       false,
		InstanceID:     "inst-1",
		HandlerFactory: standardFactory,
	})
	if err != nil {
		t.Fatalf("InitPersistentLogger: %v", err)
	}
	defer func() { _ = pl.Close() }()

	pl.LogError("something failed", "error", "boom")

	raw, err := os.ReadFile(filepath.Join(dir, "error.log"))
	if err != nil {
		t.Fatalf("read error.log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		t.Fatal("error.log 为空")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &obj); err != nil {
		t.Fatalf("不是合法 JSON: %s -- %v", lines[0], err)
	}
	for _, key := range []string{
		"timestamp", "severity_text", "severity_number", "level",
		"msg", "service", "version", "env", "instance_id",
	} {
		v, ok := obj[key]
		if !ok {
			t.Errorf("缺必填字段 %q: %s", key, lines[0])
			continue
		}
		if v == nil || v == "" {
			t.Errorf("必填字段 %q 为空: %s", key, lines[0])
		}
	}
	// 顶层必须是单层：instance 曾经是 slog.Group（嵌套对象）。
	if _, nested := obj["instance"].(map[string]any); nested {
		t.Errorf("顶层仍有嵌套对象 instance: %s", lines[0])
	}
	if obj["instance_id"] != "inst-1" {
		t.Errorf("instance_id = %v, want inst-1", obj["instance_id"])
	}
}

// TestPersistentLoggerWithoutFactoryStillWorks 保证不注入工厂时不崩。
//
// 不注入工厂 = 裸 JSONHandler（不合规但可用）——持久化日志不能因为
// 规范层的问题就整个不写。
func TestPersistentLoggerWithoutFactoryStillWorks(t *testing.T) {
	dir := t.TempDir()
	// 用 NewPersistentLogger 而非 InitPersistentLogger：
	// 后者是 sync.Once 单例，同一测试二进制里第二次 init 不生效。
	pl, err := logger.NewPersistentLogger(logger.PersistentLoggerConfig{
		LogDir:     dir,
		MaxSize:    1,
		MaxBackups: 1,
		MaxAge:     1,
		Compress:   false,
		InstanceID: "inst-2",
	})
	if err != nil {
		t.Fatalf("InitPersistentLogger: %v", err)
	}
	defer func() { _ = pl.Close() }()

	pl.LogCritical("critical path", "detail", "x")

	raw, err := os.ReadFile(filepath.Join(dir, "critical.log"))
	if err != nil {
		t.Fatalf("read critical.log: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		t.Fatal("critical.log 为空")
	}
}
