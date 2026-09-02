// Package strategy tests (2026-09-01) — parallel runner.
//
// 覆盖 RunParallelWithBody 的核心不变量：
//   - 多 strategy 并行 fan-out
//   - 按 byteLevelDensity 选最优
//   - 平局取更短字节
//   - 所有候选失败 → 返回原 body（never-worse）
//   - 失败 fail-open 不影响兄弟策略
//   - CandidateCount / WinnerName 字段正确填充
//   - 顺序链 RunWithBody 行为完全不变（回归保护）

package strategy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// 候选策略集合：3 个独立压缩方向，刻意让 density 分数不同。
//   - lite：保留语义但含 error/exception 关键词 → density 0.5+0.3=0.8
//   - caveman：保留代码片段但内容短 → density 0.5+0.3=0.8，字节数更少
//   - toolfocused：保留 URL → density 0.5+0.2=0.7（应被淘汰）
func TestRunParallel_PicksHighestDensity(t *testing.T) {
	reg := NewRegistry()
	body := []byte(`{"messages":[{"role":"system","content":"you are helpful"},{"role":"user","content":"explain this"}]}`)

	lite := &stubStrategy{
		name: "lite", enabled: true, guardStage: "lite",
		out:     []byte(`{"messages":[{"role":"system","content":"you are helpful"},{"role":"user","content":"explain this error code 500"}]}`),
		applied: true,
	}
	caveman := &stubStrategy{
		name: "caveman", enabled: true, guardStage: "caveman",
		out:     []byte(`class Foo { function bar() { return 42; } }`), // 含代码关键词 + 较短
		applied: true,
	}
	tool := &stubStrategy{
		name: "toolfocused", enabled: true, guardStage: "toolfocused",
		out:     []byte(`see https://example.com/docs for details`), // 仅 URL
		applied: true,
	}
	reg.MustRegister(lite)
	reg.MustRegister(caveman)
	reg.MustRegister(tool)

	runner := NewRunner(reg)
	out, stats, err := runner.RunParallelWithBody(context.Background(), nil, body)
	if err != nil {
		t.Fatalf("nil selector should be noop, got err: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("nil selector: body should be unchanged, got %q", string(out))
	}

	out, stats, err = runner.RunParallelWithBody(context.Background(), &ManualSelector{policy: Policy{Names: []string{"lite", "caveman", "toolfocused"}}}, body)
	if err != nil {
		t.Fatalf("RunParallelWithBody err: %v", err)
	}
	if stats.CandidateCount != 3 {
		t.Errorf("CandidateCount = %d, want 3", stats.CandidateCount)
	}
	if stats.WinnerName != "caveman" && stats.WinnerName != "lite" {
		t.Errorf("WinnerName = %q, want caveman or lite (both score 0.8); toolfocused (0.7) must lose", stats.WinnerName)
	}
	if stats.WinnerName == "caveman" {
		// 平局取更短字节：caveman 比 lite 短 → 应胜出。
		if string(out) != string(caveman.out) {
			t.Errorf("winner body mismatch: got %q want caveman.out %q", string(out), string(caveman.out))
		}
	}
	if !contains(stats.AppliedNames, stats.WinnerName) {
		t.Errorf("AppliedNames %v missing winner %q", stats.AppliedNames, stats.WinnerName)
	}
}

// 所有候选失败 → never-worse：返回原 body。
func TestRunParallel_AllCandidatesFail_ReturnsOriginalBody(t *testing.T) {
	reg := NewRegistry()
	body := []byte(`original body`)

	a := &stubStrategy{name: "a", enabled: true, out: nil, applied: false, err: errors.New("boom")}
	b := &stubStrategy{name: "b", enabled: true, out: nil, applied: false, err: errors.New("boom")}
	reg.MustRegister(a)
	reg.MustRegister(b)

	runner := NewRunner(reg)
	out, stats, err := runner.RunParallelWithBody(context.Background(),
		&ManualSelector{policy: Policy{Names: []string{"a", "b"}}}, body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("never-worse violated: got %q want original", string(out))
	}
	if stats.CandidateCount != 2 {
		t.Errorf("CandidateCount = %d, want 2", stats.CandidateCount)
	}
	if stats.WinnerName != "" {
		t.Errorf("WinnerName should be empty when no candidates succeed, got %q", stats.WinnerName)
	}
	if !contains(stats.FailedNames, "a") || !contains(stats.FailedNames, "b") {
		t.Errorf("FailedNames should include a,b; got %v", stats.FailedNames)
	}
}

// 一个失败 + 一个成功 → 仍应返回成功者的结果。
func TestRunParallel_PartialFailure_FailsOpen(t *testing.T) {
	reg := NewRegistry()
	body := []byte(`{"messages":[{"role":"user","content":"long payload to compress"}]}`)

	a := &stubStrategy{name: "a", enabled: true, out: nil, applied: false, err: errors.New("boom")}
	b := &stubStrategy{
		name: "b", enabled: true, guardStage: "b",
		out:     []byte(`{"messages":[{"role":"user","content":"short"}]}`),
		applied: true,
	}
	reg.MustRegister(a)
	reg.MustRegister(b)

	runner := NewRunner(reg)
	out, stats, err := runner.RunParallelWithBody(context.Background(),
		&ManualSelector{policy: Policy{Names: []string{"a", "b"}}}, body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != `{"messages":[{"role":"user","content":"short"}]}` {
		t.Errorf("partial fail: body = %q want b.out", string(out))
	}
	if stats.WinnerName != "b" {
		t.Errorf("WinnerName = %q want b", stats.WinnerName)
	}
	if !contains(stats.FailedNames, "a") {
		t.Errorf("FailedNames missing a; got %v", stats.FailedNames)
	}
}

// 单 strategy → 退化为顺序执行（无 goroutine 开销），但仍填充 WinnerName。
func TestRunParallel_SingleStrategy(t *testing.T) {
	reg := NewRegistry()
	body := []byte(`x`)
	s := &stubStrategy{name: "solo", enabled: true, out: []byte(`y`), applied: true}
	reg.MustRegister(s)
	runner := NewRunner(reg)
	out, stats, err := runner.RunParallelWithBody(context.Background(),
		&ManualSelector{policy: Policy{Names: []string{"solo"}}}, body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != "y" {
		t.Errorf("single: body = %q want y", string(out))
	}
	if stats.CandidateCount != 1 {
		t.Errorf("CandidateCount = %d want 1", stats.CandidateCount)
	}
	if stats.WinnerName != "solo" {
		t.Errorf("WinnerName = %q want solo", stats.WinnerName)
	}
}

// 顺序链 RunWithBody 行为完全不变（回归保护）。
// 此处不重复覆盖 RunWithBody 的全部测试（strategy_test.go 已覆盖），
// 仅校验：在启用 RunParallelWithBody 后，RunWithBody 仍返回原行为。
func TestRunWithBody_StillSequential_AfterParallelAdded(t *testing.T) {
	reg := NewRegistry()
	body := []byte(`{"a":1}`)
	a := &stubStrategy{name: "a", enabled: true, out: []byte(`{"a":2}`), applied: true}
	b := &stubStrategy{name: "b", enabled: true, out: []byte(`{"a":3}`), applied: true}
	reg.MustRegister(a)
	reg.MustRegister(b)

	runner := NewRunner(reg)
	out, stats, err := runner.RunWithBody(context.Background(),
		&ManualSelector{policy: Policy{Names: []string{"a", "b"}}}, body)
	if err != nil {
		t.Fatalf("RunWithBody err: %v", err)
	}
	// 顺序链：a → b，b.out 是最终结果。
	if string(out) != `{"a":3}` {
		t.Errorf("sequential: body = %q want b.out", string(out))
	}
	if stats.CandidateCount != 0 {
		t.Errorf("sequential CandidateCount should be 0, got %d", stats.CandidateCount)
	}
	if stats.WinnerName != "" {
		t.Errorf("sequential WinnerName should be empty, got %q", stats.WinnerName)
	}
}

// 含 mutex 的 stubStrategy 验证并发 Apply 安全（已有 sync.Mutex）。
func TestStubStrategy_ApplyIsGoroutineSafe(t *testing.T) {
	s := &stubStrategy{name: "x", enabled: true, out: []byte("ok"), applied: true}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = s.Apply(context.Background(), []byte("in"))
		}()
	}
	wg.Wait()
	if s.Calls() != 50 {
		t.Errorf("calls = %d want 50", s.Calls())
	}
}

func TestRunner_PanicFailsOpenSequentialAndParallel(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "sequential", true: "parallel"}[parallel], func(t *testing.T) {
			reg := NewRegistry()
			panicker := &panicStrategy{name: "panic"}
			good := &stubStrategy{name: "good", enabled: true, applied: true, out: []byte("ok")}
			reg.MustRegister(panicker)
			reg.MustRegister(good)
			runner := NewRunner(reg)
			sel := &staticSelector{chosen: []Strategy{panicker, good}}
			var out []byte
			var stats RunStats
			var err error
			if parallel {
				out, stats, err = runner.RunParallelWithBody(context.Background(), sel, []byte("input"))
			} else {
				out, stats, err = runner.RunWithBody(context.Background(), sel, []byte("input"))
			}
			if err != nil {
				t.Fatalf("panic must fail open, got %v", err)
			}
			if string(out) != "ok" {
				t.Fatalf("panic should not abort siblings/chain, got %q", out)
			}
			if !contains(stats.FailedNames, "panic") {
				t.Fatalf("FailedNames = %v, want panic", stats.FailedNames)
			}
		})
	}
}

func TestRunParallel_NilCandidateIsSkipped(t *testing.T) {
	reg := NewRegistry()
	good := &stubStrategy{name: "good", enabled: true, applied: true, out: []byte("ok")}
	reg.MustRegister(good)
	runner := NewRunner(reg)
	out, stats, err := runner.RunParallelWithBody(context.Background(),
		&staticSelector{chosen: []Strategy{nil, good}}, []byte("input"))
	if err != nil {
		t.Fatalf("nil candidate should fail open, got %v", err)
	}
	if string(out) != "ok" || !contains(stats.SkippedNames, "<nil>") {
		t.Fatalf("nil candidate handling: out=%q skipped=%v", out, stats.SkippedNames)
	}
}

func TestRunParallel_ClonesCandidateInput(t *testing.T) {
	reg := NewRegistry()
	first := &mutatingStrategy{name: "first", marker: '1'}
	second := &mutatingStrategy{name: "second", marker: '2'}
	reg.MustRegister(first)
	reg.MustRegister(second)
	runner := NewRunner(reg)
	body := []byte("abc")
	out, _, err := runner.RunParallelWithBody(context.Background(),
		&staticSelector{chosen: []Strategy{first, second}}, body)
	if err != nil {
		t.Fatalf("clone ownership run: %v", err)
	}
	if body[0] != 'a' {
		t.Fatalf("runner exposed caller body to candidate mutation: %q", body)
	}
	if len(out) != 2 || (out[0] != '1' && out[0] != '2') {
		t.Fatalf("unexpected cloned candidate output: %q", out)
	}
}

func TestRunner_GuardGrowthIsRejectedAfterCustomGuard(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "sequential", true: "parallel"}[parallel], func(t *testing.T) {
			reg := NewRegistry()
			s := &stubStrategy{name: "grow", enabled: true, applied: true, out: []byte("ab"), guardStage: "grow"}
			reg.MustRegister(s)
			runner := NewRunner(reg)
			runner.SetGuard(func(_, processed []byte, _ string) ([]byte, bool) {
				return append(processed, []byte("expanded")...), false
			})
			var out []byte
			var stats RunStats
			if parallel {
				out, stats, _ = runner.RunParallelWithBody(context.Background(),
					&staticSelector{chosen: []Strategy{s}}, []byte("input"))
			} else {
				out, stats, _ = runner.RunWithBody(context.Background(),
					&staticSelector{chosen: []Strategy{s}}, []byte("input"))
			}
			if string(out) != "input" || !contains(stats.TruncatedBy, "aggregate") {
				t.Fatalf("custom guard growth must revert: out=%q truncated=%v", out, stats.TruncatedBy)
			}
		})
	}
}

func TestRunner_CancellationStopsFurtherSequentialStages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := NewRegistry()
	first := &cancelStrategy{name: "first", cancel: cancel}
	second := &stubStrategy{name: "second", enabled: true, applied: true, out: []byte("bad")}
	reg.MustRegister(first)
	reg.MustRegister(second)
	runner := NewRunner(reg)
	out, _, err := runner.RunWithBody(ctx, &staticSelector{chosen: []Strategy{first, second}}, []byte("input"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	if second.Calls() != 0 || string(out) != "input" {
		t.Fatalf("cancellation should stop chain: second calls=%d out=%q", second.Calls(), out)
	}
}

func TestRunParallel_PreCanceledSkipsCandidates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reg := NewRegistry()
	s := &stubStrategy{name: "candidate", enabled: true, applied: true, out: []byte("ok")}
	reg.MustRegister(s)
	runner := NewRunner(reg)
	out, _, err := runner.RunParallelWithBody(ctx, &staticSelector{chosen: []Strategy{s}}, []byte("input"))
	if !errors.Is(err, context.Canceled) || s.Calls() != 0 || string(out) != "input" {
		t.Fatalf("pre-cancel handling: err=%v calls=%d out=%q", err, s.Calls(), out)
	}
}

type staticSelector struct{ chosen []Strategy }

func (s *staticSelector) Select(context.Context, []Strategy, []byte) []Strategy { return s.chosen }

type panicStrategy struct{ name string }

func (s *panicStrategy) Name() string                                        { return s.name }
func (s *panicStrategy) Description() string                                 { return "panic test strategy" }
func (s *panicStrategy) Enabled() bool                                       { return true }
func (s *panicStrategy) GuardStage() string                                  { return "" }
func (s *panicStrategy) Apply(context.Context, []byte) ([]byte, bool, error) { panic("boom") }

type mutatingStrategy struct {
	name   string
	marker byte
}

func (s *mutatingStrategy) Name() string        { return s.name }
func (s *mutatingStrategy) Description() string { return "mutation test strategy" }
func (s *mutatingStrategy) Enabled() bool       { return true }
func (s *mutatingStrategy) GuardStage() string  { return "" }
func (s *mutatingStrategy) Apply(_ context.Context, in []byte) ([]byte, bool, error) {
	in[0] = s.marker
	return in[:len(in)-1], true, nil
}

type cancelStrategy struct {
	name   string
	cancel context.CancelFunc
}

func (s *cancelStrategy) Name() string        { return s.name }
func (s *cancelStrategy) Description() string { return "cancellation test strategy" }
func (s *cancelStrategy) Enabled() bool       { return true }
func (s *cancelStrategy) GuardStage() string  { return "" }
func (s *cancelStrategy) Apply(_ context.Context, in []byte) ([]byte, bool, error) {
	s.cancel()
	return in, true, nil
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, target) {
			return true
		}
	}
	return false
}
