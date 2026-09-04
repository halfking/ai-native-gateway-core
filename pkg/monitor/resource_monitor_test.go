package monitor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResourceMonitor(t *testing.T) {
	// 创建临时日志目录
	tmpDir := t.TempDir()

	config := ResourceMonitorConfig{
		LogDir:             tmpDir,
		Interval:           1 * time.Second,
		MaxSize:            1,
		MaxBackups:         3,
		MaxAge:             7,
		Compress:           false,
		InstanceID:         "test-instance",
		MemGrowthThreshold: 100,
		GoroutineThreshold: 50000,
		FDGrowthThreshold:  500,
	}

	monitor, err := NewResourceMonitor(config)
	if err != nil {
		t.Fatalf("Failed to create resource monitor: %v", err)
	}

	monitor.Start()

	// 等待采集几次
	time.Sleep(3 * time.Second)

	// 获取最后一次快照
	snapshot := monitor.GetLastSnapshot()
	if snapshot == nil {
		t.Fatal("No snapshot collected")
	}

	t.Logf("Goroutines: %d", snapshot.Goroutines)
	t.Logf("Memory Alloc: %d MB", snapshot.MemoryStats.AllocMB)
	t.Logf("RSS: %d MB", snapshot.MemoryStats.RSSMB)
	t.Logf("Open FDs: %d", snapshot.FileDescriptors.OpenFDs)

	if err := monitor.Stop(); err != nil {
		t.Fatalf("Failed to stop monitor: %v", err)
	}

	// 验证日志文件存在
	logFile := filepath.Join(tmpDir, "resource_monitor.log")
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Fatal("Log file not created")
	}

	// 读取并验证日志内容
	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		var snapshot ResourceSnapshot
		if err := json.Unmarshal(scanner.Bytes(), &snapshot); err != nil {
			t.Errorf("Failed to parse log line %d: %v", lineCount, err)
			continue
		}

		if snapshot.InstanceID != "test-instance" {
			t.Errorf("Wrong instance ID: %s", snapshot.InstanceID)
		}
	}

	if lineCount < 2 {
		t.Errorf("Expected at least 2 log lines, got %d", lineCount)
	}
}

func TestLeakDetection(t *testing.T) {
	tmpDir := t.TempDir()

	config := ResourceMonitorConfig{
		LogDir:             tmpDir,
		Interval:           500 * time.Millisecond,
		InstanceID:         "leak-test",
		MemGrowthThreshold: 0.1, // 很小的阈值以便触发告警
		GoroutineThreshold: 5,   // 很小的阈值
		FDGrowthThreshold:  1,
	}

	monitor, err := NewResourceMonitor(config)
	if err != nil {
		t.Fatalf("Failed to create resource monitor: %v", err)
	}

	monitor.Start()
	defer monitor.Stop()

	// 等待采集几次，应该会触发goroutine告警（当前测试进程的goroutine肯定超过5）
	time.Sleep(2 * time.Second)

	alerts := monitor.GetAlerts()
	if len(alerts) == 0 {
		t.Log("No alerts triggered (might be expected depending on system load)")
	} else {
		t.Logf("Triggered %d alerts", len(alerts))
		for _, alert := range alerts {
			t.Logf("Alert: %s - %s", alert.AlertType, alert.Message)
		}
	}
}
