package main

// R37 (2026-09-17) 钉桩：BrokenProbeReviver 是 legacy 探测调度器的
// housekeeping（broken_confirmed → recovering 重排队）。默认新模式下
// model_probe_state 冻结，无门控启动会周期性击穿凭据恢复守卫并清空路由
// 排除（全是对死表的操作）。启动必须包在 useNewProbeMode() 的跳过分支里
// ——同 selfCheckWorker 双门先例。源码形状断言。

import (
	"os"
	"strings"
	"testing"
)

func TestBrokenProbeReviverGatedByUseNewProbeMode(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(src)
	idx := strings.Index(source, "bg.NewBrokenProbeReviver")
	if idx < 0 {
		t.Fatal("BrokenProbeReviver wiring missing from main.go")
	}
	// 启动点之前必须出现 useNewProbeMode 跳过分支（与 selfCheckWorker 同款）。
	head := source[max(0, idx-1500):idx]
	if !strings.Contains(head, "useNewProbeMode()") || !strings.Contains(head, "brokenProbeReviver skipped") {
		t.Fatal("BrokenProbeReviver must be skipped when useNewProbeMode() is true (frozen legacy table)")
	}
}
