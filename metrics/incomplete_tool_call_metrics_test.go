package metrics

import (
	"sort"
	"testing"

	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/prometheus/client_golang/prometheus"
)

func TestRecordIncompleteToolCallMetricContract(t *testing.T) {
	reg := prometheus.NewRegistry()
	recorder := &PrometheusRecorder{
		incompleteToolCallTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_incomplete_tool_call_total",
				Help: "Test counter",
			},
			[]string{"provider_family", "reason"},
		),
	}
	reg.MustRegister(recorder.incompleteToolCallTotal)

	descs := make(chan *prometheus.Desc, 1)
	go func() {
		reg.Describe(descs)
		close(descs)
	}()
	foundDescriptor := false
	for desc := range descs {
		if got := parseFQName(desc.String()); got != "llm_gateway_incomplete_tool_call_total" {
			continue
		}
		foundDescriptor = true
		labels := parseVarLabels(desc.String())
		sort.Strings(labels)
		want := []string{"provider_family", "reason"}
		if len(labels) != len(want) {
			t.Fatalf("incomplete tool call descriptor labels = %v, want %v", labels, want)
		}
		for i := range want {
			if labels[i] != want[i] {
				t.Fatalf("incomplete tool call descriptor labels = %v, want %v", labels, want)
			}
		}
		break
	}
	if !foundDescriptor {
		t.Fatal("llm_gateway_incomplete_tool_call_total descriptor was not registered")
	}

	tests := []struct {
		name   string
		model  string
		reason string
	}{
		{
			name:   "vendor prefixed date variant",
			model:  "anthropic/Claude-Sonnet-4-20250514",
			reason: "incomplete_tool_call_interrupted",
		},
		{
			name:   "vendor prefixed short date variant",
			model:  "bedrock/Claude-Opus-4-250514",
			reason: "incomplete_tool_call_after_done",
		},
		{
			name:   "empty model",
			model:  "",
			reason: "reason passed through unchanged",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder.RecordIncompleteToolCall(tt.model, tt.reason)
		})
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	if len(families) != 1 || families[0].GetName() != "llm_gateway_incomplete_tool_call_total" {
		t.Fatalf("gathered metric families = %v, want only llm_gateway_incomplete_tool_call_total", families)
	}

	got := make(map[string]string, len(families[0].GetMetric()))
	for _, metric := range families[0].GetMetric() {
		labels := metric.GetLabel()
		if len(labels) != 2 {
			t.Fatalf("metric label count = %d, want 2", len(labels))
		}

		var family, reason string
		for _, label := range labels {
			switch label.GetName() {
			case "provider_family":
				family = label.GetValue()
			case "reason":
				reason = label.GetValue()
			default:
				t.Fatalf("unexpected metric label %q", label.GetName())
			}
		}
		got[reason] = family
	}

	for _, tt := range tests {
		wantFamily := modelname.NormalizeRouteKey(tt.model)
		if wantFamily == "" {
			wantFamily = "unknown"
		}
		if gotFamily, ok := got[tt.reason]; !ok || gotFamily != wantFamily {
			t.Errorf("metric labels for model %q = provider_family=%q, reason=%q; want provider_family=%q, reason=%q", tt.model, gotFamily, tt.reason, wantFamily, tt.reason)
		}
	}
}
