package observability

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// InMemoryTracer tests
func TestInMemoryTracer_StartSpanGeneratesIDs(t *testing.T) {
	tr := NewInMemoryTracer()
	span := tr.StartSpan("test", nil)
	if span.TraceID == "" {
		t.Error("expected non-empty TraceID")
	}
	if span.SpanID == "" {
		t.Error("expected non-empty SpanID")
	}
	if span.TraceID == span.SpanID {
		t.Error("TraceID and SpanID should be different")
	}
}

func TestInMemoryTracer_ParentChildShareTraceID(t *testing.T) {
	tr := NewInMemoryTracer()
	parent := tr.StartSpan("parent", nil)
	child := tr.StartSpan("child", parent)

	if child.TraceID != parent.TraceID {
		t.Errorf("expected same TraceID, got %q vs %q", child.TraceID, parent.TraceID)
	}
	if child.ParentID != parent.SpanID {
		t.Errorf("expected child.ParentID == parent.SpanID")
	}
	if child.SpanID == parent.SpanID {
		t.Error("child SpanID should differ from parent")
	}
}

func TestInMemoryTracer_FinishSpanRecordsEndTime(t *testing.T) {
	tr := NewInMemoryTracer()
	span := tr.StartSpan("test", nil)
	time.Sleep(10 * time.Millisecond)
	tr.FinishSpan(span)

	if span.EndTime.IsZero() {
		t.Error("EndTime should be set after FinishSpan")
	}
	if span.Duration() < 10*time.Millisecond {
		t.Errorf("expected duration >= 10ms, got %v", span.Duration())
	}
	if got := len(tr.Spans()); got != 1 {
		t.Errorf("expected 1 span stored, got %d", got)
	}
}

func TestInMemoryTracer_SpansReturnsCopy(t *testing.T) {
	tr := NewInMemoryTracer()
	span := tr.StartSpan("test", nil)
	tr.FinishSpan(span)

	// 验证返回的是切片副本：修改副本不影响内部存储
	spans := tr.Spans()
	spans[0] = nil // 修改副本的第一个元素
	original := tr.Spans()
	if original[0] == nil {
		t.Error("Spans() should return slice copy, not alias")
	}
	if original[0].Name != "test" {
		t.Errorf("expected original Name 'test', got %q", original[0].Name)
	}
}

func TestNoopTracer_StartSpan(t *testing.T) {
	tr := NewNoopTracer()
	span := tr.StartSpan("test", nil)
	if span.Name != "test" {
		t.Errorf("expected name 'test', got %q", span.Name)
	}
	if span.SpanID == "" {
		t.Error("expected non-empty SpanID")
	}
	tr.FinishSpan(span)
	if span.EndTime.IsZero() {
		t.Error("EndTime should be set")
	}
}

func TestNoopTracer_ParentChild(t *testing.T) {
	tr := NewNoopTracer()
	parent := tr.StartSpan("parent", nil)
	child := tr.StartSpan("child", parent)
	if child.TraceID != parent.TraceID {
		t.Error("child should share TraceID with parent")
	}
	if child.ParentID != parent.SpanID {
		t.Error("child.ParentID should equal parent.SpanID")
	}
}

func TestInMemoryTracer_Reset(t *testing.T) {
	tr := NewInMemoryTracer()
	span := tr.StartSpan("test", nil)
	tr.FinishSpan(span)
	if len(tr.Spans()) != 1 {
		t.Fatal("expected 1 span")
	}
	tr.Reset()
	if len(tr.Spans()) != 0 {
		t.Error("expected 0 spans after reset")
	}
}

func TestSpan_DurationBeforeFinish(t *testing.T) {
	span := &Span{Name: "test", StartTime: time.Now().Add(-100 * time.Millisecond)}
	d := span.Duration()
	if d < 100*time.Millisecond {
		t.Errorf("expected duration >= 100ms, got %v", d)
	}
}

// Registry tests
func TestRegistry_CounterAccumulate(t *testing.T) {
	r := NewRegistry()
	c1 := r.Counter("test", nil)
	c1.Inc()
	c1.Inc()
	c1.Add(3)
	if c1.Value != 5 {
		t.Errorf("expected 5, got %f", c1.Value)
	}
}

func TestRegistry_SameNameSameLabelsShares(t *testing.T) {
	r := NewRegistry()
	c1 := r.Counter("test", map[string]string{"k": "v"})
	c2 := r.Counter("test", map[string]string{"k": "v"})
	if c1 != c2 {
		t.Error("same name+labels should return same Counter")
	}
	c3 := r.Counter("test", map[string]string{"k": "other"})
	if c1 == c3 {
		t.Error("different labels should return different Counter")
	}
}

func TestRegistry_HistogramBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("test", []float64{10, 50, 100}, nil)
	h.Observe(20)
	// 20 <= 50 (counts[1]), 20 <= 100 (counts[2]), 20 <= +Inf (counts[3])
	if h.Counts[0] != 0 || h.Counts[1] != 1 || h.Counts[2] != 1 || h.Counts[3] != 1 {
		t.Errorf("unexpected counts: %v", h.Counts)
	}
	h.Observe(60)
	// 60 <= 100 (counts[2]), 60 <= +Inf (counts[3])
	if h.Counts[1] != 1 || h.Counts[2] != 2 || h.Counts[3] != 2 {
		t.Errorf("unexpected counts after second observe: %v", h.Counts)
	}
}

func TestRegistry_CountersReturnsCopy(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("test", nil)
	c.Inc()

	out := r.Counters()
	out["test"] = &Counter{Name: "fake", Value: 999}
	original := r.Counters()
	if original["test"].Name != "test" {
		t.Error("Counters() should return deep copy")
	}
}

func TestRegistry_HistogramsReturnsCopy(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("test", []float64{1, 2}, nil)
	h.Observe(0.5)

	out := r.Histograms()
	if len(out) == 0 {
		t.Fatal("expected at least one histogram")
	}
	for k, v := range out {
		if v.Name != "test" {
			t.Errorf("expected Name 'test', got %q", v.Name)
		}
		_ = k
	}
}

func TestRegistry_Reset(t *testing.T) {
	r := NewRegistry()
	r.Counter("c", nil)
	r.Histogram("h", []float64{1}, nil)
	r.Reset()
	if len(r.Counters()) != 0 {
		t.Error("expected 0 counters after reset")
	}
	if len(r.Histograms()) != 0 {
		t.Error("expected 0 histograms after reset")
	}
}

func TestRegistry_HistogramObserve(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("test", []float64{10, 100}, nil)
	h.Observe(5)
	if h.Sum != 5 || h.Count != 1 {
		t.Errorf("expected Sum=5 Count=1, got Sum=%f Count=%d", h.Sum, h.Count)
	}
}

// Hook tests
func TestTracingHook_StoresSpanInMetadata(t *testing.T) {
	tr := NewInMemoryTracer()
	hook := NewTracingHook(tr)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Envelope = &domain.RequestEnvelope{RequestID: "req-1"}
	env.TenantID = "tenant-a"
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	span, ok := env.Metadata["trace_span"].(*Span)
	if !ok {
		t.Fatal("expected *Span in metadata")
	}
	if span.Tags["tenant_id"] != "tenant-a" {
		t.Errorf("expected tenant_id tag, got %q", span.Tags["tenant_id"])
	}
	if span.Tags["request_id"] != "req-1" {
		t.Errorf("expected request_id tag, got %q", span.Tags["request_id"])
	}
}

func TestTracingHook_DisabledWhenNilEnv(t *testing.T) {
	hook := NewTracingHook(NewInMemoryTracer())
	if hook.Enabled(context.Background(), nil) {
		t.Error("should be disabled with nil env")
	}
}

func TestMetricsHook_AccumulatesRequestsTotal(t *testing.T) {
	r := NewRegistry()
	hook := NewMetricsHook(r)

	for i := 0; i < 3; i++ {
		env := domain.NewRequestEnvelope(context.Background(), nil)
		env.TenantID = "tenant-a"
		if err := hook.Execute(context.Background(), env); err != nil {
			t.Fatalf("Execute %d failed: %v", i, err)
		}
	}
	// 同 tenant_id + ok status 应该共享同一个 Counter
	counter := r.Counter("requests_total", map[string]string{
		"tenant_id": "tenant-a",
		"status":    "ok",
	})
	if counter.Value != 3 {
		t.Errorf("expected requests_total=3, got %f", counter.Value)
	}
}

func TestMetricsHook_StatusLabel(t *testing.T) {
	r := NewRegistry()
	hook := NewMetricsHook(r)

	// ok
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t"
	_ = hook.Execute(context.Background(), env)

	// error
	env2 := domain.NewRequestEnvelope(context.Background(), nil)
	env2.TenantID = "t"
	env2.Error = errTest
	_ = hook.Execute(context.Background(), env2)

	ok := false
	er := false
	for _, c := range r.Counters() {
		if c.Labels["status"] == "ok" {
			ok = true
		}
		if c.Labels["status"] == "error" {
			er = true
		}
	}
	if !ok || !er {
		t.Errorf("expected both status=ok and status=error counters, got ok=%v err=%v", ok, er)
	}
}

var errTest = &testErr{msg: "test"}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
