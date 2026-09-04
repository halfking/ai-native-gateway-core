package logger

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistentLogger(t *testing.T) {
	// 创建临时日志目录
	tmpDir := t.TempDir()

	config := PersistentLoggerConfig{
		LogDir:     tmpDir,
		MaxSize:    1,
		MaxBackups: 3,
		MaxAge:     7,
		Compress:   false,
		InstanceID: "test-instance-123",
	}

	logger, err := NewPersistentLogger(config)
	if err != nil {
		t.Fatalf("Failed to create persistent logger: %v", err)
	}
	defer logger.Close()

	// 测试不同级别的日志
	logger.LogCritical("database connection failed", "error", "connection timeout")
	logger.LogError("request failed", "status", 500)
	logger.LogPanic("unexpected panic", "panic: runtime error", "goroutine", 42)
	logger.LogShutdown("graceful shutdown", true, "uptime", "1h30m")
	logger.LogAudit("config_changed", "field", "max_connections", "old", 100, "new", 200)

	// 同步缓冲区
	logger.Sync()

	// 验证日志文件存在
	expectedFiles := []string{
		"critical.log",
		"error.log",
		"panic.log",
		"shutdown.log",
		"audit.log",
	}

	for _, filename := range expectedFiles {
		logPath := filepath.Join(tmpDir, filename)
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			t.Errorf("Log file %s not created", filename)
		}
	}

	// 验证关键日志内容
	criticalLog := filepath.Join(tmpDir, "critical.log")
	content, err := os.ReadFile(criticalLog)
	if err != nil {
		t.Fatalf("Failed to read critical log: %v", err)
	}

	if !strings.Contains(string(content), "database connection failed") {
		t.Error("Critical log does not contain expected message")
	}
	if !strings.Contains(string(content), "test-instance-123") {
		t.Error("Critical log does not contain instance ID")
	}
}

func TestPersistentLoggerAbnormalExit(t *testing.T) {
	tmpDir := t.TempDir()

	config := PersistentLoggerConfig{
		LogDir:     tmpDir,
		InstanceID: "test-abnormal",
	}

	logger, err := NewPersistentLogger(config)
	if err != nil {
		t.Fatalf("Failed to create persistent logger: %v", err)
	}
	defer logger.Close()

	logger.LogAbnormalExit("signal", "received SIGTERM")
	logger.Sync()

	shutdownLog := filepath.Join(tmpDir, "shutdown.log")
	file, err := os.Open(shutdownLog)
	if err != nil {
		t.Fatalf("Failed to open shutdown log: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	foundAbnormalExit := false
	foundStartup := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "abnormal_exit") {
			foundAbnormalExit = true
		}
		if strings.Contains(line, "service_startup") {
			foundStartup = true
		}
	}

	if !foundStartup {
		t.Error("Startup event not logged")
	}
	if !foundAbnormalExit {
		t.Error("Abnormal exit event not logged")
	}
}

func TestPersistentLoggerClose(t *testing.T) {
	tmpDir := t.TempDir()

	config := PersistentLoggerConfig{
		LogDir: tmpDir,
	}

	logger, err := NewPersistentLogger(config)
	if err != nil {
		t.Fatalf("Failed to create persistent logger: %v", err)
	}

	logger.LogError("test message", "key", "value")

	if err := logger.Close(); err != nil {
		t.Errorf("Failed to close logger: %v", err)
	}

	// 再次关闭应该不会报错（虽然可能会有错误返回）
	logger.Close()
}

func TestGlobalPersistentLogger(t *testing.T) {
	tmpDir := t.TempDir()

	config := PersistentLoggerConfig{
		LogDir: tmpDir,
	}

	// 第一次初始化
	logger1, err := InitPersistentLogger(config)
	if err != nil {
		t.Fatalf("Failed to init persistent logger: %v", err)
	}
	defer logger1.Close()

	// 第二次初始化应该返回同一个实例
	logger2, err := InitPersistentLogger(config)
	if err != nil {
		t.Fatalf("Second init failed: %v", err)
	}

	if logger1 != logger2 {
		t.Error("Global persistent logger not singleton")
	}

	// GetPersistentLogger应该返回同一个实例
	logger3 := GetPersistentLogger()
	if logger1 != logger3 {
		t.Error("GetPersistentLogger returns different instance")
	}
}
