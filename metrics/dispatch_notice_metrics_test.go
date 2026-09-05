package metrics

import (
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestDispatchNoticeDroppedMetricRegistered 验证计数器已在默认注册表注册，
// 且只声明 notice_kind / reason 两个低基数 label。
// label_cardinality_guard_test.go 的 TestNoHighCardinalityLabels 扫描同一
// 默认注册表，本指标自动纳入其禁用 label（request_id/model/tenant_id…）
// 门禁；这里按同一模式显式复核声明形状。
func TestDispatchNoticeDroppedMetricRegistered(t *testing.T) {
	descs := collectDeclaredDescs(t)
	found := false
	for _, ds := range descs {
		if parseFQName(ds) != "llmgw_dispatch_notice_dropped_total" {
			continue
		}
		found = true
		labels := parseVarLabels(ds)
		sort.Strings(labels)
		want := []string{"notice_kind", "reason"}
		if len(labels) != len(want) || labels[0] != want[0] || labels[1] != want[1] {
			t.Fatalf("llmgw_dispatch_notice_dropped_total labels = %v, want %v", labels, want)
		}
	}
	if !found {
		t.Fatal("llmgw_dispatch_notice_dropped_total is not registered in the default registry")
	}
}

// TestRecordDispatchNoticeDroppedIncrementsPerKindAndReason 每个
// kind × reason 组合独立递增，互不串桶。
func TestRecordDispatchNoticeDroppedIncrementsPerKindAndReason(t *testing.T) {
	cases := []struct{ kind, reason string }{
		{"retry", DispatchNoticeDropReasonNonStreaming},
		{"retry", DispatchNoticeDropReasonPreStreamUninit},
		{"node_switch", DispatchNoticeDropReasonNonStreaming},
		{"node_switch", DispatchNoticeDropReasonPreStreamUninit},
		{"model_switch", DispatchNoticeDropReasonNonStreaming},
		{"queued", DispatchNoticeDropReasonPreStreamUninit},
		{"scheduled", DispatchNoticeDropReasonNonStreaming},
	}
	for _, c := range cases {
		before := readCounterVec(t, DispatchNoticeDroppedTotal.WithLabelValues(c.kind, c.reason))
		RecordDispatchNoticeDropped(c.kind, c.reason)
		RecordDispatchNoticeDropped(c.kind, c.reason)
		if got := readCounterVec(t, DispatchNoticeDroppedTotal.WithLabelValues(c.kind, c.reason)); got != before+2 {
			t.Fatalf("kind=%s reason=%s: counter %v -> %v, want +2", c.kind, c.reason, before, got)
		}
	}
}

// TestRecordDispatchNoticeDroppedNormalizesUnknownValues 基数守卫：闭集
// 之外的 kind/reason 一律归一到 "unknown"，label 值域不随输入增长 ——
// 即便调用方误传 requestID / model 等高基数值也只会落进同一个桶。
func TestRecordDispatchNoticeDroppedNormalizesUnknownValues(t *testing.T) {
	const unknown = "unknown"
	beforeKind := readCounterVec(t, DispatchNoticeDroppedTotal.WithLabelValues(unknown, DispatchNoticeDropReasonPreStreamUninit))
	RecordDispatchNoticeDropped("req_01JGHDCWSZQZS8D9D5H41A7VWN", DispatchNoticeDropReasonPreStreamUninit)
	if got := readCounterVec(t, DispatchNoticeDroppedTotal.WithLabelValues(unknown, DispatchNoticeDropReasonPreStreamUninit)); got != beforeKind+1 {
		t.Fatalf("unknown kind bucket: %v -> %v, want +1", beforeKind, got)
	}

	beforeReason := readCounterVec(t, DispatchNoticeDroppedTotal.WithLabelValues("retry", unknown))
	RecordDispatchNoticeDropped("retry", "z-ai/glm-5.2")
	if got := readCounterVec(t, DispatchNoticeDroppedTotal.WithLabelValues("retry", unknown)); got != beforeReason+1 {
		t.Fatalf("unknown reason bucket: %v -> %v, want +1", beforeReason, got)
	}
}

// TestDispatchNoticeDroppedSeriesPreseeded 预热检查：闭集 5 kind ×
// 2 reason 共 10 条 0 值序列在 Gather 输出中全部可见，丢弃率面板从进程
// 启动起就有完整分子（不依赖第一次真实丢弃把序列拉出来）。
func TestDispatchNoticeDroppedSeriesPreseeded(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	seen := map[[2]string]bool{}
	for _, family := range families {
		if family.GetName() != "llmgw_dispatch_notice_dropped_total" {
			continue
		}
		for _, m := range family.GetMetric() {
			var kind, reason string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "notice_kind":
					kind = l.GetValue()
				case "reason":
					reason = l.GetValue()
				}
			}
			seen[[2]string{kind, reason}] = true
		}
	}
	for kind := range dispatchNoticeDropKindAllowlist {
		for reason := range dispatchNoticeDropReasonAllowlist {
			if !seen[[2]string{kind, reason}] {
				t.Errorf("preseeded series notice_kind=%s reason=%s missing from gather output", kind, reason)
			}
		}
	}
}
