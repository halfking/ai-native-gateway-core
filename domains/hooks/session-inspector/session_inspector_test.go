package sessioninspector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// ---------- TokenLimitInspector ----------

func TestTokenLimitInspector_Exceeds(t *testing.T) {
	i := NewTokenLimitInspector(1000)
	snap := &SessionSnapshot{SessionID: "s", TokenCount: 1500}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1", len(findings))
	}
	if findings[0].Code != "TOKEN_LIMIT_EXCEEDED" {
		t.Fatalf("code=%q", findings[0].Code)
	}
	if findings[0].Severity != SeverityWarning {
		t.Fatalf("severity=%q", findings[0].Severity)
	}
	if findings[0].InspectorName != "token_limit" {
		t.Fatalf("inspector=%q", findings[0].InspectorName)
	}
	if findings[0].DetectedAt.IsZero() {
		t.Fatal("DetectedAt should be set")
	}
}

func TestTokenLimitInspector_Within(t *testing.T) {
	i := NewTokenLimitInspector(1000)
	snap := &SessionSnapshot{SessionID: "s", TokenCount: 500}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatalf("expected nil findings, got %+v", findings)
	}
}

func TestTokenLimitInspector_DefaultMax(t *testing.T) {
	i := NewTokenLimitInspector(0) // 默认 100000
	snap := &SessionSnapshot{SessionID: "s", TokenCount: 50000}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatal("expected nil for default max")
	}
}

func TestTokenLimitInspector_NilSnap(t *testing.T) {
	i := NewTokenLimitInspector(100)
	findings, err := i.Inspect(nil)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatalf("expected nil for nil snap, got %+v", findings)
	}
}

// ---------- InactiveInspector ----------

func TestInactiveInspector_Idle(t *testing.T) {
	i := NewInactiveInspector(time.Minute)
	snap := &SessionSnapshot{
		SessionID:    "s",
		LastActiveAt: time.Now().Add(-10 * time.Minute),
	}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d", len(findings))
	}
	if findings[0].Code != "SESSION_IDLE" {
		t.Fatalf("code=%q", findings[0].Code)
	}
	if findings[0].Severity != SeverityInfo {
		t.Fatalf("severity=%q", findings[0].Severity)
	}
}

func TestInactiveInspector_Active(t *testing.T) {
	i := NewInactiveInspector(time.Hour)
	snap := &SessionSnapshot{
		SessionID:    "s",
		LastActiveAt: time.Now().Add(-1 * time.Minute),
	}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatalf("expected nil, got %+v", findings)
	}
}

func TestInactiveInspector_NoLastActive(t *testing.T) {
	i := NewInactiveInspector(time.Minute)
	snap := &SessionSnapshot{SessionID: "s"} // LastActiveAt 零值
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatalf("expected skip, got %+v", findings)
	}
}

func TestInactiveInspector_DefaultIdle(t *testing.T) {
	i := NewInactiveInspector(0)
	snap := &SessionSnapshot{
		SessionID:    "s",
		LastActiveAt: time.Now().Add(-1 * time.Minute),
	}
	// 默认 30 分钟，所以 1 分钟闲置不应触发
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatal("expected nil with default 30min")
	}
}

// ---------- HighFrequencyInspector ----------

func TestHighFrequencyInspector_Exceeds(t *testing.T) {
	i := NewHighFrequencyInspector(60)
	snap := &SessionSnapshot{SessionID: "s", RequestCount: 120}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d", len(findings))
	}
	if findings[0].Code != "HIGH_REQUEST_RATE" {
		t.Fatalf("code=%q", findings[0].Code)
	}
}

func TestHighFrequencyInspector_Normal(t *testing.T) {
	i := NewHighFrequencyInspector(60)
	snap := &SessionSnapshot{SessionID: "s", RequestCount: 30}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatalf("expected nil, got %+v", findings)
	}
}

func TestHighFrequencyInspector_DefaultMax(t *testing.T) {
	i := NewHighFrequencyInspector(0) // 默认 60
	snap := &SessionSnapshot{SessionID: "s", RequestCount: 30}
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if findings != nil {
		t.Fatal("expected nil with default 60")
	}
}

// ---------- InspectorHook ----------

func TestHook_Name_Priority(t *testing.T) {
	h := NewInspectorHook()
	if h.Name() != "session.inspect" {
		t.Fatalf("Name=%q", h.Name())
	}
	if h.Priority() != 100 {
		t.Fatalf("Priority=%d", h.Priority())
	}
}

func TestHook_Enabled_RequiresSessionID(t *testing.T) {
	h := NewInspectorHook()
	if !h.Enabled(context.Background(), &domain.PipelineRequest{SessionID: "abc"}) {
		t.Fatal("expected Enabled=true with SessionID")
	}
	if h.Enabled(context.Background(), &domain.PipelineRequest{SessionID: ""}) {
		t.Fatal("expected Enabled=false with empty SessionID")
	}
	if h.Enabled(context.Background(), nil) {
		t.Fatal("expected Enabled=false with nil env")
	}
}

func TestHook_Execute_ChainsMultipleInspectors(t *testing.T) {
	tok := NewTokenLimitInspector(100)
	inactive := NewInactiveInspector(time.Minute)
	hi := NewHighFrequencyInspector(10)
	h := NewInspectorHook(tok, inactive, hi)

	env := &domain.PipelineRequest{
		SessionID: "s1",
		TenantID:  "t1",
		Metadata: map[string]any{
			MetaKeyRequestCount: 100,
			MetaKeyTokenCount:   500,
			MetaKeyLastActiveAt: time.Now().Add(-2 * time.Hour),
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	raw, ok := env.Metadata[MetaKeySessionFindings]
	if !ok {
		t.Fatal("expected session_findings in metadata")
	}
	findings, ok := raw.([]*Finding)
	if !ok {
		t.Fatalf("expected []*Finding, got %T", raw)
	}
	if len(findings) != 3 {
		t.Fatalf("findings=%d, want 3 (one per inspector)", len(findings))
	}

	// 验证 findings 来自不同的 inspector
	seen := map[string]bool{}
	for _, f := range findings {
		seen[f.InspectorName] = true
	}
	for _, name := range []string{"token_limit", "inactive", "high_frequency"} {
		if !seen[name] {
			t.Fatalf("missing finding from %q", name)
		}
	}
}

func TestHook_Execute_NoFindings(t *testing.T) {
	h := NewInspectorHook(
		NewTokenLimitInspector(10000),
		NewInactiveInspector(time.Hour),
		NewHighFrequencyInspector(1000),
	)
	env := &domain.PipelineRequest{
		SessionID: "s1",
		Metadata: map[string]any{
			MetaKeyRequestCount: 5,
			MetaKeyTokenCount:   100,
			MetaKeyLastActiveAt: time.Now(),
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	raw := env.Metadata[MetaKeySessionFindings]
	findings, _ := raw.([]*Finding)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(findings))
	}
}

func TestHook_Execute_NoSessionID_NoOp(t *testing.T) {
	h := NewInspectorHook(NewTokenLimitInspector(10))
	env := &domain.PipelineRequest{} // 无 SessionID
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := env.Metadata[MetaKeySessionFindings]; ok {
		t.Fatal("expected no findings when no SessionID")
	}
}

func TestHook_Execute_PropagatesInspectorError(t *testing.T) {
	bad := &fakeInspector{name: "boom", err: errors.New("kaboom")}
	h := NewInspectorHook(bad)
	env := &domain.PipelineRequest{SessionID: "s"}
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error from broken inspector")
	}
}

func TestHook_Execute_NoInspectors(t *testing.T) {
	h := NewInspectorHook()
	env := &domain.PipelineRequest{SessionID: "s"}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	raw, ok := env.Metadata[MetaKeySessionFindings]
	if !ok {
		t.Fatal("expected empty findings slice in metadata")
	}
	findings, _ := raw.([]*Finding)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(findings))
	}
}

func TestHook_OnError_Swallows(t *testing.T) {
	h := NewInspectorHook()
	err := h.OnError(context.Background(), &domain.PipelineRequest{}, errors.New("x"))
	if err != nil {
		t.Fatalf("OnError should swallow, got: %v", err)
	}
}

func TestHook_Add_Inspectors(t *testing.T) {
	h := NewInspectorHook()
	if got := h.Inspectors(); len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
	h.Add(NewTokenLimitInspector(100))
	h.Add(nil) // nil 应被忽略
	if got := h.Inspectors(); len(got) != 1 {
		t.Fatalf("expected 1 inspector, got %d", len(got))
	}
}

// ---------- Snapshot 字段传递 ----------

func TestHook_SnapshotFieldPropagation(t *testing.T) {
	cap := &captureInspector{}
	h := NewInspectorHook(cap)

	env := &domain.PipelineRequest{
		SessionID: "s-xyz",
		TenantID:  "t-9",
		Metadata: map[string]any{
			MetaKeyRequestCount: 42,
			MetaKeyTokenCount:   7777,
			MetaKeyLastActiveAt: time.Now().Truncate(time.Second),
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	captured := cap.captured
	if captured == nil {
		t.Fatal("inspector never received snapshot")
	}
	if captured.SessionID != "s-xyz" {
		t.Fatalf("SessionID=%q", captured.SessionID)
	}
	if captured.TenantID != "t-9" {
		t.Fatalf("TenantID=%q", captured.TenantID)
	}
	if captured.RequestCount != 42 {
		t.Fatalf("RequestCount=%d", captured.RequestCount)
	}
	if captured.TokenCount != 7777 {
		t.Fatalf("TokenCount=%d", captured.TokenCount)
	}
	if captured.LastActiveAt.IsZero() {
		t.Fatal("LastActiveAt not propagated")
	}
}

func TestHook_SnapshotDefaultsOnMissingMetadata(t *testing.T) {
	cap := &captureInspector{}
	h := NewInspectorHook(cap)

	env := &domain.PipelineRequest{SessionID: "s"}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	captured := cap.captured
	if captured == nil {
		t.Fatal("inspector never received snapshot")
	}
	if captured.RequestCount != 0 || captured.TokenCount != 0 {
		t.Fatal("expected zero counters when metadata missing")
	}
	if !captured.LastActiveAt.IsZero() {
		t.Fatal("expected zero LastActiveAt when metadata missing")
	}
}

func TestHook_ImplementsPipelineHook(t *testing.T) {
	var _ interface {
		Name() string
		Execute(context.Context, *domain.PipelineRequest) error
		Priority() int
		Enabled(context.Context, *domain.PipelineRequest) bool
		OnError(context.Context, *domain.PipelineRequest, error) error
	} = (*InspectorHook)(nil)
}

// ---------- helpers ----------

type fakeInspector struct {
	name string
	err  error
}

func (f *fakeInspector) Name() string { return f.name }
func (f *fakeInspector) Inspect(*SessionSnapshot) ([]*Finding, error) {
	return nil, f.err
}

type captureInspector struct {
	captured *SessionSnapshot
}

func (c *captureInspector) Name() string { return "capture" }
func (c *captureInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap != nil {
		cp := *snap
		c.captured = &cp
	}
	return nil, nil
}
