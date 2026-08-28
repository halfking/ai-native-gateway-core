// Package strategy tests (GW-10 Phase 1)
//
// 覆盖 Registry / ManualSelector / Runner 三个核心组件 + Adapter 集成。
package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// stubStrategy 测试用最小 Strategy 实现。
//   - guardStage 非空时 runner 会套守卫
//   - out 长度由测试控制，可用于构造"输出 ≥ 输入"触发 NeverWorse regressed
type stubStrategy struct {
	name        string
	description string
	enabled     bool
	guardStage  string
	out         []byte
	applied     bool
	err         error
	calls       int
	mu          sync.Mutex
}

func (s *stubStrategy) Name() string        { return s.name }
func (s *stubStrategy) Description() string { return s.description }
func (s *stubStrategy) Enabled() bool       { return s.enabled }
func (s *stubStrategy) GuardStage() string  { return s.guardStage }
func (s *stubStrategy) Apply(_ context.Context, in []byte) ([]byte, bool, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.err != nil {
		return in, false, s.err
	}
	return s.out, s.applied, nil
}
func (s *stubStrategy) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// ── Registry tests ─────────────────────────────────────────────────

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true}
	if err := reg.Register(a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.Get("a") != a {
		t.Error("Get should return same instance")
	}
	if got := reg.Names(); len(got) != 1 || got[0] != "a" {
		t.Errorf("Names = %v, want [a]", got)
	}
}

func TestRegistry_DuplicateRejected(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&stubStrategy{name: "x"})
	if err := reg.Register(&stubStrategy{name: "x"}); err == nil {
		t.Error("expected duplicate error")
	}
}

func TestRegistry_NilOrEmptyRejected(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(nil); err == nil {
		t.Error("nil should be rejected")
	}
	if err := reg.Register(&stubStrategy{name: ""}); err == nil {
		t.Error("empty name should be rejected")
	}
}

func TestRegistry_MustRegisterPanics(t *testing.T) {
	reg := NewRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustRegister on duplicate should panic")
		}
	}()
	reg.MustRegister(&stubStrategy{name: "y"})
	reg.MustRegister(&stubStrategy{name: "y"})
}

func TestRegistry_SnapshotPreservesOrder(t *testing.T) {
	reg := NewRegistry()
	for _, n := range []string{"z", "a", "m"} {
		_ = reg.Register(&stubStrategy{name: n})
	}
	got := reg.Snapshot()
	if len(got) != 3 || got[0].Name() != "z" || got[1].Name() != "a" || got[2].Name() != "m" {
		t.Errorf("Snapshot order wrong: %v", namesOf(got))
	}
}

// ── ManualSelector tests ───────────────────────────────────────────

func TestManualSelector_OffEmptyPolicy(t *testing.T) {
	sel := NewManualSelector(Policy{})
	all := []Strategy{
		&stubStrategy{name: "a", enabled: true},
		&stubStrategy{name: "b", enabled: true},
	}
	got := sel.Select(context.Background(), all, nil)
	if got != nil {
		t.Errorf("off selector must return nil; got %v", namesOf(got))
	}
}

func TestManualSelector_AllExpandsToRegistry(t *testing.T) {
	// UnknownMode="ignore" + Names 为空 = "all" 语义。
	// Selector 不过滤 Enabled — Runner 负责，便于动态启用语义。
	sel := NewManualSelector(Policy{UnknownMode: "ignore"})
	all := []Strategy{
		&stubStrategy{name: "a", enabled: true},
		&stubStrategy{name: "b", enabled: false},
		&stubStrategy{name: "c", enabled: true},
	}
	gotNames := namesOf(sel.Select(context.Background(), all, nil))
	if len(gotNames) != 3 {
		t.Errorf("all selector must return all strategies; got %v", gotNames)
	}
}

func TestManualSelector_NamedOrderRespected(t *testing.T) {
	sel := NewManualSelector(Policy{Names: []string{"c", "a"}, UnknownMode: "ignore"})
	all := []Strategy{
		&stubStrategy{name: "a", enabled: true},
		&stubStrategy{name: "b", enabled: true},
		&stubStrategy{name: "c", enabled: true},
	}
	gotNames := namesOf(sel.Select(context.Background(), all, nil))
	if len(gotNames) != 2 || gotNames[0] != "c" || gotNames[1] != "a" {
		t.Errorf("policy order not preserved: got %v, want [c a]", gotNames)
	}
}

func TestManualSelector_UnknownIgnored(t *testing.T) {
	sel := NewManualSelector(Policy{Names: []string{"a", "ghost"}, UnknownMode: "ignore"})
	all := []Strategy{
		&stubStrategy{name: "a", enabled: true},
	}
	gotNames := namesOf(sel.Select(context.Background(), all, nil))
	if len(gotNames) != 1 || gotNames[0] != "a" {
		t.Errorf("ghost must be ignored; got %v", gotNames)
	}
}

func TestManualSelector_DuplicateInPolicyDeduped(t *testing.T) {
	sel := NewManualSelector(Policy{Names: []string{"a", "a", "b"}, UnknownMode: "ignore"})
	all := []Strategy{
		&stubStrategy{name: "a", enabled: true},
		&stubStrategy{name: "b", enabled: true},
	}
	gotNames := namesOf(sel.Select(context.Background(), all, nil))
	if len(gotNames) != 2 {
		t.Errorf("dup dedup failed: %v", gotNames)
	}
}

// ── ResolvePolicy tests ────────────────────────────────────────────

func TestResolvePolicy(t *testing.T) {
	cases := []struct {
		spec    string
		wantLen int
	}{
		{"", 0},
		{"off", 0},
		{"all", 0},
		{"lite", 1},
		{"lite,caveman", 2},
		{" lite , caveman , toolfocused ", 3},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			p, err := ResolvePolicy(tc.spec)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(p.Names) != tc.wantLen {
				t.Errorf("len(Names) = %d, want %d (spec=%q)", len(p.Names), tc.wantLen, tc.spec)
			}
		})
	}
}

// TestResolvePolicy_RejectsReservedKeywords 验证 off/all 不能与其他名称混用
// （修复 C3：避免 "lite,off" → 静默 no-op）。
func TestResolvePolicy_RejectsReservedKeywords(t *testing.T) {
	cases := []string{
		"lite,off",
		"off,lite",
		"all,off",
		"lite,all,caveman",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			_, err := ResolvePolicy(spec)
			if err == nil {
				t.Errorf("expected error for spec %q", spec)
			}
			if !errors.Is(err, ErrInvalidPolicySpec) {
				t.Errorf("err should wrap ErrInvalidPolicySpec; got %v", err)
			}
		})
	}
}

// ── Runner tests ───────────────────────────────────────────────────

func TestRunner_NilSelectorReturnsBody(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&stubStrategy{name: "a", enabled: true, applied: true, out: []byte("x")})
	r := NewRunner(reg)
	body := []byte(`{"x":1}`)
	out, stats, err := r.RunWithBody(context.Background(), nil, body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("nil selector must return original body")
	}
	if stats.BytesOut != len(body) {
		t.Errorf("BytesOut should equal BytesIn on off path")
	}
}

func TestRunner_ChainAppliesInOrder(t *testing.T) {
	// a 把 6-byte "start" 压成 4-byte "a-out"；b 再压成 2-byte "b-out"。
	// 总压缩链长 6 → 4 → 2。
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: true, out: []byte("a-out"), guardStage: "a"}
	b := &stubStrategy{name: "b", enabled: true, applied: true, out: []byte("b-ou"), guardStage: "b"}
	_ = reg.Register(a)
	_ = reg.Register(b)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"a", "b"}, UnknownMode: "ignore"})
	out, stats, err := r.RunWithBody(context.Background(), sel, []byte("start!"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != "b-ou" {
		t.Errorf("final body = %q, want b-ou", out)
	}
	if a.Calls() != 1 || b.Calls() != 1 {
		t.Errorf("each strategy should run exactly once; a=%d b=%d", a.Calls(), b.Calls())
	}
	if len(stats.AppliedNames) != 2 {
		t.Errorf("AppliedNames = %v", stats.AppliedNames)
	}
}

func TestRunner_NeverWorseRegression(t *testing.T) {
	// a: 6-byte input "start!" → 4-byte "aaaa" (压缩 OK)
	// b: 4-byte "aaaa"        → 8-byte "bbbbbbbb" (膨胀 → 回退)
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: true, out: []byte("aaaa"), guardStage: "a"}
	b := &stubStrategy{name: "b", enabled: true, applied: true, out: []byte("bbbbbbbb"), guardStage: "b"}
	_ = reg.Register(a)
	_ = reg.Register(b)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"a", "b"}, UnknownMode: "ignore"})
	out, stats, _ := r.RunWithBody(context.Background(), sel, []byte("start!"))
	if string(out) != "aaaa" {
		t.Errorf("regressed body must revert to a's output; got %q", out)
	}
	if len(stats.TruncatedBy) != 1 || stats.TruncatedBy[0] != "b" {
		t.Errorf("TruncatedBy = %v, want [b]", stats.TruncatedBy)
	}
	if b.Calls() != 1 {
		t.Errorf("b should still be invoked once (to surface regressed); calls=%d", b.Calls())
	}
}

func TestRunner_AppliedFalseSkipped(t *testing.T) {
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: false, out: []byte("x")}
	_ = reg.Register(a)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"a"}, UnknownMode: "ignore"})
	_, stats, _ := r.RunWithBody(context.Background(), sel, []byte("input"))
	if len(stats.AppliedNames) != 0 {
		t.Errorf("applied=false should not be recorded as Applied; got %v", stats.AppliedNames)
	}
	if len(stats.SkippedNames) != 1 {
		t.Errorf("expected one skipped; got %v", stats.SkippedNames)
	}
}

func TestRunner_ApplyErrorFailsOpen(t *testing.T) {
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: true, out: []byte("aaaa"), guardStage: "a"}
	b := &stubStrategy{name: "b", enabled: true, applied: true, err: errors.New("boom"), guardStage: "b"}
	_ = reg.Register(a)
	_ = reg.Register(b)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"a", "b"}, UnknownMode: "ignore"})
	out, stats, err := r.RunWithBody(context.Background(), sel, []byte("start!"))
	if err != nil {
		t.Fatalf("optional strategy path must fail open: %v", err)
	}
	if len(stats.FailedNames) != 1 || stats.FailedNames[0] != "b" {
		t.Errorf("FailedNames = %v, want [b]", stats.FailedNames)
	}
	if string(out) != "aaaa" {
		t.Errorf("error body must be the body as of last successful apply; got %q", out)
	}
	// N3: 即使中途失败，BytesOut 也应反映当前链长，便于观测部分压缩效果。
	if stats.BytesOut != 4 {
		t.Errorf("BytesOut on error path = %d, want 4 (length of a's output)", stats.BytesOut)
	}
	// AppliedNames 应包含 a（成功应用的）；b 不在（出错就 abort，未 Applied）。
	if len(stats.AppliedNames) != 1 || stats.AppliedNames[0] != "a" {
		t.Errorf("AppliedNames = %v, want [a]", stats.AppliedNames)
	}
}

// TestRunner_MultiStageRegressionAllRecorded 验证多个 strategy 同时触发
// NeverWorse 时，TruncatedBy 应包含所有受影响的 strategy（修复 N7）。
func TestRunner_MultiStageRegressionAllRecorded(t *testing.T) {
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: true, out: []byte("aaaa"), guardStage: "a"}
	b := &stubStrategy{name: "b", enabled: true, applied: true, out: []byte("bbbbbbbb"), guardStage: "b"}
	c := &stubStrategy{name: "c", enabled: true, applied: true, out: []byte("cccc"), guardStage: "c"}
	_ = reg.Register(a)
	_ = reg.Register(b)
	_ = reg.Register(c)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"a", "b", "c"}, UnknownMode: "ignore"})
	// "start!"(6) → "aaaa"(4) 压缩 → "bbbbbbbb"(8) 膨胀 → "cccc"(4) 膨胀
	// b 触发回归（4→8）；c 拿到 a 的输出（4 字节）作 input，"cccc" 也是 4 字节
	// 走 NeverWorse 路径：len(processed) < len(raw) 才接受，4 < 4 false → 也视为膨胀。
	out, stats, _ := r.RunWithBody(context.Background(), sel, []byte("start!"))
	if string(out) != "aaaa" {
		t.Errorf("final body = %q, want aaaa (regressed twice)", out)
	}
	if len(stats.TruncatedBy) != 2 || stats.TruncatedBy[0] != "b" || stats.TruncatedBy[1] != "c" {
		t.Errorf("TruncatedBy = %v, want [b c]", stats.TruncatedBy)
	}
}

func TestRunner_DisabledStrategySkippedAtApply(t *testing.T) {
	reg := NewRegistry()
	disabled := &stubStrategy{name: "x", enabled: false, applied: true, out: []byte("never")}
	_ = reg.Register(disabled)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"x"}, UnknownMode: "ignore"})
	_, stats, _ := r.RunWithBody(context.Background(), sel, []byte("input!"))
	if disabled.Calls() != 0 {
		t.Errorf("disabled strategy must not be invoked; calls=%d", disabled.Calls())
	}
	if len(stats.AppliedNames) != 0 {
		t.Errorf("no Applied; got %v", stats.AppliedNames)
	}
}

func TestRunner_AggregateGuardRevertsUnguardedGrowth(t *testing.T) {
	reg := NewRegistry()
	grow := &stubStrategy{name: "g", enabled: true, applied: true, out: []byte("longer-output"), guardStage: ""}
	_ = reg.Register(grow)
	r := NewRunner(reg)
	sel := NewManualSelector(Policy{Names: []string{"g"}, UnknownMode: "ignore"})
	out, stats, _ := r.RunWithBody(context.Background(), sel, []byte("in"))
	if string(out) != "in" {
		t.Errorf("aggregate guard must revert growth; got %q", out)
	}
	if len(stats.TruncatedBy) != 1 || stats.TruncatedBy[0] != "aggregate" {
		t.Errorf("TruncatedBy = %v, want [aggregate]", stats.TruncatedBy)
	}
}

func TestRunner_SetGuardIsUsed(t *testing.T) {
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: true, out: []byte("bbbbbbbb"), guardStage: "a"}
	_ = reg.Register(a)
	r := NewRunner(reg)

	// 自定义守卫：始终 reject（regressed=true）。
	r.SetGuard(func(raw, processed []byte, stage string) ([]byte, bool) {
		return raw, true
	})
	sel := NewManualSelector(Policy{Names: []string{"a"}, UnknownMode: "ignore"})
	out, stats, _ := r.RunWithBody(context.Background(), sel, []byte("start"))
	if string(out) != "start" {
		t.Errorf("custom guard should revert; got %q", out)
	}
	if len(stats.TruncatedBy) != 1 || stats.TruncatedBy[0] != "a" {
		t.Errorf("TruncatedBy = %v, want [a]", stats.TruncatedBy)
	}
}

// TestRunner_SetGuardConcurrentNoDataRace 修复 C1：SetGuard 与 RunWithBody
// 并发调用，atomic.Value 保证无 data race。在 -race 跑器下应通过。
func TestRunner_SetGuardConcurrentNoDataRace(t *testing.T) {
	reg := NewRegistry()
	a := &stubStrategy{name: "a", enabled: true, applied: true, out: []byte("a-out"), guardStage: "a"}
	_ = reg.Register(a)
	r := NewRunner(reg)

	// 并发 writer: 切换 guard。
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				r.SetGuard(defaultNeverWorse)
				r.SetGuard(func(raw, processed []byte, stage string) ([]byte, bool) {
					if len(processed) < len(raw) {
						return processed, false
					}
					return raw, true
				})
			}
		}
	}()

	// 并发 reader: 跑 RunWithBody。
	for i := 0; i < 100; i++ {
		sel := NewManualSelector(Policy{Names: []string{"a"}, UnknownMode: "ignore"})
		_, _, _ = r.RunWithBody(context.Background(), sel, []byte("input"))
	}
	close(stop)
	// 至少：调用没 panic；具体 result 不强求（guard 切换期间结果不确定，但不应崩溃）。
}

// ── Adapter 集成测试 ────────────────────────────────────────────────

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestLiteAdapter_EnabledFlagControlsApply(t *testing.T) {
	body := mustMarshal(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello world   \n\n\n\n"},
		},
	})
	disabled := &LiteAdapter{On: false}
	out, applied, err := disabled.Apply(context.Background(), body)
	if err != nil || applied || string(out) != string(body) {
		t.Errorf("disabled adapter must no-op; applied=%v out=%q", applied, out)
	}
	enabled := &LiteAdapter{On: true}
	out, applied, err = enabled.Apply(context.Background(), body)
	if err != nil {
		t.Fatalf("enabled Apply: %v", err)
	}
	if !applied {
		t.Errorf("expected lite to apply on whitespace-heavy body")
	}
	if strings.Contains(string(out), "\n\n\n\n") {
		t.Errorf("lite should fold 4+ newlines; got %q", out)
	}
}

func TestCavemanAdapter_EnabledFlagControlsApply(t *testing.T) {
	body := mustMarshal(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Sure, I would be happy to help. Could you please tell me more?"},
		},
	})
	a := &CavemanAdapter{On: true}
	out, applied, err := a.Apply(context.Background(), body)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !applied {
		t.Errorf("caveman should apply on pleasantries-rich body")
	}
	if len(out) >= len(body) {
		t.Errorf("caveman output should be smaller; before=%d after=%d", len(body), len(out))
	}
}

func TestToolFocusedAdapter_EnabledFlagControlsApply(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "import mod")
	}
	body := mustMarshal(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Join(lines, "\n")},
		},
	})
	a := &ToolFocusedAdapter{On: true}
	out, applied, err := a.Apply(context.Background(), body)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !applied {
		t.Errorf("toolfocused should apply on code-heavy tool result")
	}
	if !strings.Contains(string(out), "lines elided") {
		t.Errorf("expected elision marker; got %q", out)
	}
}

// ── 工具函数 ────────────────────────────────────────────────────────

func namesOf(ss []Strategy) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s == nil {
			continue
		}
		out = append(out, s.Name())
	}
	return out
}
