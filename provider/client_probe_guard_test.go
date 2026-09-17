package provider

// R37 (2026-09-17) 钉桩：
// ① 路由候选排除（broken_confirmed NOT EXISTS）必须读 v_node_probe_state_compat
//    ——冻结旧表会让排除失效，坏死 (credential, model) 对持续被路由选中。
// ② defaultAsyncExitSuspicious 对 model_probe_state 的 UPDATE 必须被
//    legacyProbeMode() 门控（新模式下旧表停更，写它只是改死表）。
// ③ legacyProbeMode 与 cmd/gateway useNewProbeMode 语义一致（默认新模式）。

import (
	"os"
	"strings"
	"testing"
)

func TestRoutingExclusionReadsCompatView(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	source := string(src)
	if !strings.Contains(source, "FROM v_node_probe_state_compat mps\n") {
		t.Fatal("routing broken-model exclusion must read v_node_probe_state_compat")
	}
	// mps_sibling 排除位于 AND FALSE 故意停用的死门内（R37 复核登记），
	// 不在断言范围；活代码路径不得再直读冻结旧表。
	if strings.Contains(source, "FROM model_probe_state mps\n") {
		t.Fatal("routing exclusion regressed to reading frozen model_probe_state")
	}
}

func TestAsyncExitSuspiciousDBGatedByLegacyMode(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	source := string(src)
	const fn = "func (c *Client) defaultAsyncExitSuspicious"
	start := strings.Index(source, fn)
	if start < 0 {
		t.Fatal("defaultAsyncExitSuspicious not found")
	}
	end := strings.Index(source[start:], "\nfunc ")
	body := source[start:]
	if end >= 0 {
		body = source[start : start+end]
	}
	if !strings.Contains(body, "legacyProbeMode()") {
		t.Fatal("defaultAsyncExitSuspicious UPDATE on model_probe_state must be gated by legacyProbeMode() (frozen under new probe mode)")
	}
}

func TestLegacyProbeModeSemantics(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false}, // 默认 = 新模式
		{"true", false}, {"1", false}, {"yes", false}, {"on", false}, {"TRUE", false},
		{"false", true}, {"0", true}, {"off", true},
	}
	for _, c := range cases {
		t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", c.env)
		if got := legacyProbeMode(); got != c.want {
			t.Errorf("LLM_GATEWAY_USE_NEW_PROBE_MODE=%q: legacyProbeMode()=%v, want %v", c.env, got, c.want)
		}
	}
	_ = os.Unsetenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
}
