package stats

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMinuteAccumulatorNotWiredInGateway 钉住 12h 审计发现：
// NewMinuteAccumulator 全仓无生产调用方；探测分钟表的生产写入者是
// bg.StatsMinuteRollup（含探测臂）。本门防止后人「看到 MinuteAccumulator
// 有 probeMain 分流」就以为生产已接线。
func TestMinuteAccumulatorNotWiredInGateway(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	mainPath := filepath.Join(root, "cmd/gateway/main.go")
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "NewMinuteAccumulator(") {
		t.Fatal("cmd/gateway/main.go must NOT call NewMinuteAccumulator — " +
			"production minute writer is bg.NewStatsMinuteRollup " +
			"(see bg/stats_minute_probe_rollup.go for the probe arm)")
	}
	if !strings.Contains(body, "NewStatsMinuteRollup(") {
		t.Fatal("cmd/gateway/main.go must wire bg.NewStatsMinuteRollup")
	}
	if !strings.Contains(body, "probelane.AssertWithQueryRow(") {
		t.Fatal("cmd/gateway/main.go must call probelane.AssertWithQueryRow when starting the data plane " +
			"(lane-on + missing tables ⇒ PersistTurn silent-fail)")
	}
}
