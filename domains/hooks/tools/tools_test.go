package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestMetaToolInterceptor_InjectsInterceptedAt(t *testing.T) {
	m := NewMetaToolInterceptor("")
	ctx := Context{
		Calls:    []*ToolCall{{ID: "1", Name: "test"}},
		TenantID: "t1",
	}
	out, err := m.Intercept(ctx)
	if err != nil {
		t.Fatalf("Intercept failed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 call, got %d", len(out))
	}
	ts, ok := out[0].Meta["_intercepted_at"].(time.Time)
	if !ok {
		t.Fatal("expected _intercepted_at to be time.Time")
	}
	if ts.IsZero() {
		t.Error("_intercepted_at should be non-zero")
	}
}

func TestMetaToolInterceptor_InjectsTenantID(t *testing.T) {
	m := NewMetaToolInterceptor("")
	ctx := Context{
		Calls:    []*ToolCall{{ID: "1", Name: "test"}},
		TenantID: "tenant-x",
	}
	out, _ := m.Intercept(ctx)
	if got := out[0].Meta["_tenant_id"]; got != "tenant-x" {
		t.Errorf("expected tenant-x, got %v", got)
	}
}

func TestMetaToolInterceptor_PreservesExistingMeta(t *testing.T) {
	m := NewMetaToolInterceptor("")
	ctx := Context{
		Calls: []*ToolCall{{ID: "1", Name: "test", Meta: map[string]any{"custom": "value"}}},
	}
	out, _ := m.Intercept(ctx)
	if got := out[0].Meta["custom"]; got != "value" {
		t.Errorf("existing meta should be preserved, got %v", got)
	}
}

func TestMetaToolInterceptor_NilMetaAllocated(t *testing.T) {
	m := NewMetaToolInterceptor("")
	ctx := Context{
		Calls: []*ToolCall{{ID: "1", Name: "test", Meta: nil}},
	}
	out, _ := m.Intercept(ctx)
	if out[0].Meta == nil {
		t.Fatal("Meta should be allocated")
	}
}

func TestToolInterceptionHook_RunsInterceptorsInOrder(t *testing.T) {
	rec := &recordingInterceptor{}
	hook := NewToolInterceptionHook(
		NewMetaToolInterceptor(""),
		rec,
	)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t1"
	env.Metadata = map[string]any{
		"tool_calls": []*ToolCall{{ID: "1", Name: "test"}},
	}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if rec.count != 1 {
		t.Errorf("expected 1 recording call, got %d", rec.count)
	}
	if !rec.sawInjected {
		t.Error("recording interceptor should see meta injected by first interceptor")
	}
}

func TestToolInterceptionHook_SkipsWhenNoToolCalls(t *testing.T) {
	hook := NewToolInterceptionHook(NewMetaToolInterceptor(""))
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if hook.Enabled(context.Background(), env) {
		t.Error("should be disabled when no tool_calls")
	}
}

func TestToolInterceptionHook_PropagatesInterceptorError(t *testing.T) {
	hook := NewToolInterceptionHook(
		&failingInterceptor{err: errors.New("blocked")},
		&recordingInterceptor{},
	)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t1"
	env.Metadata = map[string]any{
		"tool_calls": []*ToolCall{{ID: "1"}},
	}
	err := hook.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error")
	}
}

type recordingInterceptor struct {
	count        int
	sawInjected  bool
}

func (r *recordingInterceptor) Name() string { return "recording" }

func (r *recordingInterceptor) Intercept(ctx Context) ([]*ToolCall, error) {
	r.count++
	for _, c := range ctx.Calls {
		if _, ok := c.Meta["_intercepted_at"]; ok {
			r.sawInjected = true
		}
	}
	return ctx.Calls, nil
}

type failingInterceptor struct{ err error }

func (f *failingInterceptor) Name() string { return "failing" }

func (f *failingInterceptor) Intercept(ctx Context) ([]*ToolCall, error) {
	return nil, f.err
}
