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
