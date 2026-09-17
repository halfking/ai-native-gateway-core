package provider

// R37 (2026-09-17) 钉桩（合并版）：
// ① 路由候选的 broken_confirmed 排除必须经 brokenPairExcludeSQL（内部走
//    internal/probemode.GuardStateTable 按探测模式选源）——冻结旧表会让
//    排除失效，坏死 (credential, model) 对持续被路由选中；主排除与
//    AND FALSE 死门内的 sibling 排除两处都应走同一 helper。
// ② defaultAsyncExitSuspicious 在新模式下必须跳过对旧表的死写
//    （probemode.Enabled() 早退）。

import (
	"os"
	"strings"
	"testing"
)

func TestRoutingExclusionUsesModeAwareHelper(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	source := string(src)
	n := strings.Count(source, "brokenPairExcludeSQL(")
	if n < 3 { // helper 定义 + 主排除 + sibling 排除
		t.Fatalf("routing broken-model exclusion sites must use brokenPairExcludeSQL, found %d references", n)
	}
	if strings.Contains(source, "FROM model_probe_state mps") {
		t.Fatal("routing exclusion regressed to hardcoded frozen model_probe_state")
	}
}

func TestAsyncExitSuspiciousSkipsUnderNewProbeMode(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	const fn = "func (c *Client) defaultAsyncExitSuspicious"
	start := strings.Index(string(src), fn)
	if start < 0 {
		t.Fatal("defaultAsyncExitSuspicious not found")
	}
	end := strings.Index(string(src)[start:], "\nfunc ")
	body := string(src)[start:]
	if end >= 0 {
		body = string(src)[start : start+end]
	}
	if !strings.Contains(body, "probemode.Enabled()") {
		t.Fatal("defaultAsyncExitSuspicious must early-return under new probe mode (frozen legacy table)")
	}
}
