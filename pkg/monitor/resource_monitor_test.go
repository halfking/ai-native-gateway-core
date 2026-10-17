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

// 回归：Stop 先于 Start 调用后，Start 必须是 no-op；重复 Stop 安全。
// 旧实现会在未启动的 Stop 里关闭 stopDone，Start 后的采集循环退出时
// 再次 close 同一通道会 panic，且该循环永不退出（goroutine 泄漏）。
func TestStopBeforeStartThenStart(t *testing.T) {
	tmpDir := t.TempDir()

	monitor, err := NewResourceMonitor(ResourceMonitorConfig{
		LogDir:   tmpDir,
		Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create resource monitor: %v", err)
	}

	if err := monitor.Stop(); err != nil {
		t.Fatalf("Stop before Start failed: %v", err)
	}
	if err := monitor.Stop(); err != nil {
		t.Fatalf("Second Stop failed: %v", err)
	}

	monitor.Start() // 必须保持 no-op，不得启动采集循环
	monitor.Start()

	if snapshot := monitor.GetLastSnapshot(); snapshot != nil {
		t.Error("No snapshot should be collected after Stop-before-Start lifecycle")
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

	// 等待采集几次。goroutine 判据是电平判定（不走趋势窗口），阈值 5 对任何
	// Go 测试进程都必然触发，因此这里可以断言而不是只打日志——原先只 t.Log，
	// 是一条永远不会红的判据（详见 resource_monitor_leakwindow_test.go 的说明）。
	deadline := time.Now().Add(5 * time.Second)
	var goroutineAlerts int
	for time.Now().Before(deadline) && goroutineAlerts == 0 {
		time.Sleep(200 * time.Millisecond)
		for _, a := range monitor.GetAlerts() {
			if a.AlertType == "goroutine_leak_suspected" {
				goroutineAlerts++
			}
		}
	}
	if goroutineAlerts == 0 {
		t.Fatalf("goroutine 阈值设为 5，测试进程 goroutine 必然超过，却 5s 内未触发任何 goroutine_leak_suspected")
	}
}
