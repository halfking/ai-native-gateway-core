package metrics

import (
	"sort"
	"testing"
)

// TestPluginResourceMetricsRegistered 验证 §5.3 指标族已在默认注册表注册，
// 且只声明低基数 label（plugin_id / mode / kind）。label_cardinality_guard_test.go
// 的 TestNoHighCardinalityLabels 扫描同一默认注册表，本指标自动纳入其禁用
// label（request_id/model/tenant_id…）门禁；这里按同一模式显式复核声明形状。
func TestPluginResourceMetricsRegistered(t *testing.T) {
	want := map[string][]string{
		"llmgw_plugin_memory_usage_bytes":     {"plugin_id"},
		"llmgw_plugin_memory_limit_bytes":     {"plugin_id"},
		"llmgw_plugin_cpu_usage_percent":      {"plugin_id"},
		"llmgw_plugin_threads":                {"plugin_id"},
		"llmgw_plugin_limiter_mode_info":      {"mode", "plugin_id"},
		"llmgw_plugin_limit_violations_total": {"kind", "plugin_id"},
		"llmgw_plugin_oom_killed_total":       {"plugin_id"},
	}
	descs := collectDeclaredDescs(t)
	seen := map[string][]string{}
	for _, ds := range descs {
		fq := parseFQName(ds)
		if _, ok := want[fq]; ok {
			seen[fq] = parseVarLabels(ds)
		}
	}
	for fq, wantLabels := range want {
		got, ok := seen[fq]
		if !ok {
			t.Errorf("%s is not registered in the default registry", fq)
			continue
		}
		sort.Strings(got)
		sort.Strings(wantLabels)
		if len(got) != len(wantLabels) {
			t.Errorf("%s labels = %v, want %v", fq, got, wantLabels)
			continue
		}
		for i := range got {
			if got[i] != wantLabels[i] {
				t.Errorf("%s labels = %v, want %v", fq, got, wantLabels)
				break
			}
		}
	}
}

// TestRecordPluginLimitViolationNormalizesKind 未知 kind 归一为 unknown，
// 空插件 id 归一为 unknown（防意外空串序列）。
func TestRecordPluginLimitViolationNormalizesKind(t *testing.T) {
	before := readCounterVec(t, PluginLimitViolationsTotal.WithLabelValues("demo", "unknown"))
	RecordPluginLimitViolation("demo", "not-a-kind")
	RecordPluginLimitViolation("", PluginLimitKindMemory)
	if after := readCounterVec(t, PluginLimitViolationsTotal.WithLabelValues("demo", "unknown")); after != before+1 {
		t.Errorf("unknown kind not normalized: %v -> %v", before, after)
	}
	if after := readCounterVec(t, PluginLimitViolationsTotal.WithLabelValues("unknown", PluginLimitKindMemory)); after != 1 {
		t.Errorf("empty plugin id not normalized to 'unknown': got %v", after)
	}
	// 闭集 kind 正常入桶
	RecordPluginLimitViolation("demo", PluginLimitKindCPU)
	if after := readCounterVec(t, PluginLimitViolationsTotal.WithLabelValues("demo", PluginLimitKindCPU)); after != 1 {
		t.Errorf("closed-set kind not counted: got %v", after)
	}
}

// TestRecordPluginOOMKillDelta 仅正增量入表，0 增量不产生样本变更。
func TestRecordPluginOOMKillDelta(t *testing.T) {
	before := readCounterVec(t, PluginOOMKilledTotal.WithLabelValues("demo"))
	RecordPluginOOMKill("demo", 0)
	if after := readCounterVec(t, PluginOOMKilledTotal.WithLabelValues("demo")); after != before {
		t.Errorf("zero delta should not change counter: %v -> %v", before, after)
	}
	RecordPluginOOMKill("demo", 2)
	if after := readCounterVec(t, PluginOOMKilledTotal.WithLabelValues("demo")); after != before+2 {
		t.Errorf("delta 2 not applied: %v -> %v", before, after)
	}
}
