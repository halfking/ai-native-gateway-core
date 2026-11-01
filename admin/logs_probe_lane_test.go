package admin

import (
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
