package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

func TestResolveLogsTrafficLanePredicateDefaultBusiness(t *testing.T) {
	got := resolveLogsTrafficLanePredicate("", false, "rl")
	if !strings.Contains(got, probekind.SQLStageList()) {
		t.Fatalf("default must embed SQLStageList: %s", got)
	}
	if !strings.Contains(got, "NOT IN") {
		t.Fatalf("default must exclude probe stages: %s", got)
	}
	gotBiz := resolveLogsTrafficLanePredicate("business", false, "rl")
	if gotBiz != got {
		t.Fatalf("lane=business must match empty default\n got: %s\nwant: %s", gotBiz, got)
	}
}

func TestResolveLogsTrafficLanePredicateProbeOnly(t *testing.T) {
	got := resolveLogsTrafficLanePredicate("probe", false, "rl")
	if !strings.Contains(got, "IN ("+probekind.SQLStageList()+")") {
		t.Fatalf("lane=probe must IN probe stages: %s", got)
	}
	if strings.Contains(got, "NOT IN") {
		t.Fatalf("lane=probe must not use NOT IN: %s", got)
	}
	gotAlias := resolveLogsTrafficLanePredicate("probe_history", false, "rl")
	if gotAlias != got {
		t.Fatalf("lane=probe_history must equal lane=probe\n got: %s\nwant: %s", gotAlias, got)
	}
}

func TestIsLogsProbeHistoryLane(t *testing.T) {
	if !isLogsProbeHistoryLane("probe") || !isLogsProbeHistoryLane("probe_history") {
		t.Fatal("probe and probe_history must both be history lanes")
	}
	if isLogsProbeHistoryLane("business") || isLogsProbeHistoryLane("all") || isLogsProbeHistoryLane("") {
		t.Fatal("business/all/empty must not be history lanes")
	}
}

func TestResolveLogsTrafficLanePredicateAllOrInclude(t *testing.T) {
	if got := resolveLogsTrafficLanePredicate("all", false, "rl"); got != "" {
		t.Fatalf("lane=all want empty, got %q", got)
	}
	if got := resolveLogsTrafficLanePredicate("", true, "rl"); got != "" {
		t.Fatalf("include_probe want empty, got %q", got)
	}
	if got := resolveLogsTrafficLanePredicate("probe", true, "rl"); got != "" {
		t.Fatalf("include_probe overrides lane=probe, got %q", got)
	}
}

func TestListLogsProbeHistorySourceTagInSource(t *testing.T) {
	// 钉住 listLogs 对历史探测 lane 回写 source/lane 的接线（字符串门：
	// 行为单测需真库；此处防「响应标签被误删」）。
	src, err := os.ReadFile("logs.go")
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `resp["source"] = "history"`) {
		t.Error("listLogs must tag source=history for probe history lane")
	}
	if !strings.Contains(body, `resp["lane"] = "probe_history"`) {
		t.Error("listLogs must echo lane=probe_history for probe history lane")
	}
	if !strings.Contains(body, "isLogsProbeHistoryLane(lane)") {
		t.Error("listLogs must gate the history tag on isLogsProbeHistoryLane")
	}
}

// TestLogsQueryScopeKeepsProbeHistoryLaneExclusive 钉住 lane=probe|probe_history
// 不被并入 includeProbe（2026-11-25 第 28 轮）：历史探测车道是「仅历史探测行」，
// 不是「含探测」。曾经的回归形状＝handler 为提前选超时把 lane 也 or 进 includeProbe
// ⇒ 谓词被摘（resolveLogsTrafficLanePredicate 返回 ""）⇒ 历史探测视图退化成全量。
func TestLogsQueryScopeKeepsProbeHistoryLaneExclusive(t *testing.T) {
	probeFilter := probeRequestFilter("rl")
	businessFilter := businessRequestFilter("rl")
	cases := []struct {
		name      string
		lane      string
		incParam  bool
		wantUnion bool
		wantPred  string
	}{
		{"lane=probe ⇒ 仅历史探测", "probe", false, false, probeFilter},
		{"lane=probe_history ⇒ 仅历史探测", "probe_history", false, false, probeFilter},
		{"缺省 ⇒ 仅业务", "", false, false, businessFilter},
		{"lane=business ⇒ 仅业务", "business", false, false, businessFilter},
		{"lane=all ⇒ union 全量", "all", false, true, ""},
		{"include_probe=1 ⇒ union 全量", "", true, true, ""},
		{"include_probe=1 显式压过 lane=probe（既有语义）", "probe", true, true, ""},
	}
	for _, tc := range cases {
		includeProbe, unionScope := logsQueryScope(tc.lane, tc.incParam)
		if unionScope != tc.wantUnion {
			t.Errorf("%s: unionScope = %v, want %v", tc.name, unionScope, tc.wantUnion)
		}
		if got := resolveLogsTrafficLanePredicate(tc.lane, includeProbe, "rl"); got != tc.wantPred {
			t.Errorf("%s: 车道谓词不对\n got: %s\nwant: %s", tc.name, got, tc.wantPred)
		}
	}
}

// TestListLogsWiresScopeThroughLogsQueryScope 是接线门（字符串门）：handler 必须经
// logsQueryScope 取 includeProbe / unionScope，且不得回退成「lane 也 or 进 includeProbe」。
// 判据自证：把下面三处任一处改回 af2bb4970 的写法 ⇒ 本门红。
func TestListLogsWiresScopeThroughLogsQueryScope(t *testing.T) {
	src, err := os.ReadFile("logs.go")
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "includeProbe, unionScope := logsQueryScope(lane, queryBool(r, \"include_probe\"))") {
		t.Error("listLogs 必须经 logsQueryScope 解析 includeProbe / unionScope")
	}
	if strings.Contains(body, `lane == "probe" || lane == "probe_history"`) {
		t.Error("listLogs 不得把 lane=probe|probe_history 并进 includeProbe（历史探测视图会退化成全量）")
	}
	for _, needle := range []string{
		"if unionScope {\n\t\tqueryTimeout = 120 * time.Second",
		"if unionScope && !overrideWindow {",
		`resp["window_clamp_hours"] = int(logsProbeWindowCap.Hours())`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("listLogs 的超时/窗口上限必须跟随 unionScope，缺：%q", needle)
		}
	}
}
