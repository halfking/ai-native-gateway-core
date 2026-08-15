package sessionv2mirror

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestSessionDimWriter_NilPoolNoop(t *testing.T) {
	w := NewSessionDimWriter(nil)
	sid := "gw_x"
	// nil pool：报错但不 panic，供装配端容错
	if err := w.UpsertSessionDim(context.Background(), &telemetry.RequestLogEntry{GwSessionID: &sid}); err == nil {
		t.Fatal("nil pool should surface an error")
	}
}

func TestSessionDimWriter_NilEntryNoop(t *testing.T) {
	w := NewSessionDimWriter(nil)
	if err := w.UpsertSessionDim(context.Background(), nil); err != nil {
		t.Fatalf("nil entry should be a no-op, got %v", err)
	}
	sid := ""
	if err := w.UpsertSessionDim(context.Background(), &telemetry.RequestLogEntry{GwSessionID: &sid}); err != nil {
		t.Fatalf("empty session id should be a no-op, got %v", err)
	}
}

func TestCoalesceStr(t *testing.T) {
	a, b := "app", "prefix"
	if got := coalesceStr(&a, &b); got != &a {
		t.Fatalf("non-empty first should win, got %v", *got)
	}
	empty := ""
	if got := coalesceStr(&empty, &b); got != &b || *got != "prefix" {
		t.Fatalf("empty first should fall back, got %v", got)
	}
	if got := coalesceStr(nil, &b); got != &b {
		t.Fatalf("nil first should fall back, got %v", got)
	}
}

// 接口契约：*SessionDimWriter 满足 DimWriter，可注入 PersistHook。
var _ DimWriter = (*SessionDimWriter)(nil)
