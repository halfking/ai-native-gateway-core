package routingopt

// ml_ab_test.go — P2.5阶段4增量测试：ABGate分桶、MLStats、热更新。
//
// 纯单元测试（ABGate/Stats/Swap语义）总是运行；
// 涉及ONNX的测试在共享库不可用时跳过（同ml_selector_test.go约定）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestABGateDeterministicBuckets(t *testing.T) {
	g := NewABGate(0.3)
	// 确定性：同一(sessionID, apiKeyID)永远同一组
	for i := 0; i < 50; i++ {
		if got, want := g.Treatment("sess-1", 7), g.Treatment("sess-1", 7); got != want {
			t.Fatalf("Treatment not deterministic: %v vs %v", got, want)
		}
	}
	// 分桶近似pct：10000个不同会话，观测比例应在30%±5%
	g2 := NewABGate(0.3)
	treatment := 0
	for i := 0; i < 10000; i++ {
		if g2.Treatment("s", 1000+i) {
			treatment++
		}
	}
	if treatment < 2500 || treatment > 3500 {
		t.Errorf("treatment share = %d/10000, want ~3000±500", treatment)
	}
}

func TestABGateClamping(t *testing.T) {
	always := NewABGate(5.0) // >1 → clamp到1.0
	for i := 0; i < 20; i++ {
		if !always.Treatment("s", i) {
			t.Fatal("pct>1 must always be treatment")
		}
	}
	never := NewABGate(-1) // <0 → clamp到0.0
	for i := 0; i < 20; i++ {
		if never.Treatment("s", i) {
			t.Fatal("pct<0 must never be treatment")
		}
	}
}

func TestABGateSnapshot(t *testing.T) {
	g := NewABGate(0.5)
	_ = g.Treatment("a", 1)
	_ = g.Treatment("b", 2)
	snap := g.Snapshot()
	if snap["evaluated"].(int64) != 2 {
		t.Errorf("evaluated = %v", snap["evaluated"])
	}
	if snap["treatment_basis_bps"].(int) != 5000 {
		t.Errorf("basis_bps = %v", snap["treatment_basis_bps"])
	}
	var nilGate *ABGate
	if nilGate.Snapshot() != nil {
		t.Error("nil gate snapshot should be nil")
	}
}

func TestMLStatsRecord(t *testing.T) {
	var s MLStats
	s.Record(true, false, false, true, time.Millisecond)    // served+boost
	s.Record(true, false, false, false, time.Millisecond)   // served
	s.Record(true, true, false, false, time.Millisecond)    // low confidence
	s.Record(true, false, true, false, time.Millisecond)    // no match
	s.Record(false, false, false, false, time.Millisecond)  // failure
	snap := s.Snapshot()
	if snap["predictions_served"].(int64) != 2 {
		t.Errorf("predictions_served = %v", snap["predictions_served"])
	}
	if snap["candidates_boosted"].(int64) != 1 {
		t.Errorf("candidates_boosted = %v", snap["candidates_boosted"])
	}
	if snap["failures"].(int64) != 1 || snap["low_confidence_skips"].(int64) != 1 ||
		snap["no_match_skips"].(int64) != 1 {
		t.Errorf("skip counters wrong: %v", snap)
	}
	var nilStats *MLStats
	nilStats.Record(true, false, false, false, 0) // 不应panic
	if nilStats.Snapshot() != nil {
		t.Error("nil stats snapshot should be nil")
	}
}

// TestMLRerankerSwapSelector验证热更新的交换语义（无ORT依赖路径）。
func TestMLRerankerSwapSelector(t *testing.T) {
	r := NewMLReranker(nil, 0.6)
	if r.Enabled() {
		t.Fatal("reranker with nil selector must be disabled")
	}
	cands := []ModelCandidate{{CanonicalName: "a"}, {CanonicalName: "b"}}
	out, _ := r.Rerank(context.Background(), cands, MLRouteFeatures{})
	if len(out) != 2 {
		t.Fatal("disabled reranker must pass through")
	}
	var nilR *MLReranker
	if old := nilR.SwapSelector(nil); old != nil {
		t.Error("nil reranker swap should return nil")
	}
}

// TestEvaluateAB无gate时恒为treatment（保持既有行为）。
func TestEvaluateABWithoutGate(t *testing.T) {
	o := &RealOptimizer{}
	if !o.EvaluateAB("sess", 1) {
		t.Fatal("no gate → always treatment")
	}
	o.WithABGate(NewABGate(0))
	if o.EvaluateAB("sess", 1) {
		t.Fatal("pct=0 gate → never treatment")
	}
}

// --- ONNX依赖的集成测试 ---

// TestMLRerankerStatsWithFixture：真实模型下统计计数与低置信度回退。
func TestMLRerankerStatsWithFixture(t *testing.T) {
	fixture := filepath.Join(mlFixtureDir, "manifest.json")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	sel, err := NewMLSelector(context.Background(), MLSelectorConfig{
		ManifestPath:      fixture,
		ORTLibraryPath:    ortLibPath(t),
		IntraOpNumThreads: 1,
	})
	if err != nil {
		t.Skipf("ONNX runtime unavailable: %v", err)
	}
	t.Cleanup(func() { _ = sel.Close() })

	cands := []ModelCandidate{
		{CanonicalName: "gpt-4"},
		{CanonicalName: "claude-3.5-sonnet"},
		{CanonicalName: "glm-4.6"},
		{CanonicalName: "deepseek-v3"},
	}
	feat := MLRouteFeatures{
		TaskType: "chat", Profile: "smart", Classifier: "v3_heuristic",
		Confidence: 0.85, DetectedLanguage: "en",
		PromptLengthBucket: "m", ContextLengthBucket: "xs",
		TurnCountBucket: "single", IntentCategory: "question",
		DomainHint: "general", ComplexityBucket: "simple",
		HasTableIndicator: true,
	}
	r := NewMLReranker(sel, 0.0) // 阈值0：只要匹配就提升
	out, pred := r.Rerank(context.Background(), cands, feat)
	if pred == nil {
		t.Fatal("expected a prediction")
	}
	if pred.MaxProbability < r.MinConfidence {
		t.Fatal("threshold 0 must never skip")
	}
	if len(out) != len(cands) {
		t.Fatalf("rerank changed candidate count: %d → %d", len(cands), len(out))
	}
	if r.Stats.Snapshot()["predictions_served"].(int64) < 1 {
		t.Errorf("expected ≥1 served prediction, got %v", r.Stats.Snapshot())
	}

	// 低置信度路径：阈值提到2.0（概率不可能达到）→ 保持原序且计数
	rLow := NewMLReranker(sel, 2.0)
	outLow, predLow := rLow.Rerank(context.Background(), cands, feat)
	if predLow == nil || len(outLow) != len(cands) {
		t.Fatal("low-confidence path must return prediction and unchanged order")
	}
	if rLow.Stats.Snapshot()["low_confidence_skips"].(int64) != 1 {
		t.Errorf("expected 1 low-confidence skip, got %v", rLow.Stats.Snapshot())
	}
}

// TestMLHotReload：轮询检测模型文件变化并原子换入新session。
func TestMLHotReload(t *testing.T) {
	fixture := filepath.Join(mlFixtureDir, "manifest.json")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	tmp := t.TempDir()
	tmpManifest := filepath.Join(tmp, "manifest.json")
	tmpModel := filepath.Join(tmp, "model.onnx")
	for src, dst := range map[string]string{
		fixture:                                        tmpManifest,
		filepath.Join(mlFixtureDir, "model.onnx"):      tmpModel,
	} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := MLSelectorConfig{
		ManifestPath:      tmpManifest,
		ORTLibraryPath:    ortLibPath(t),
		IntraOpNumThreads: 1,
	}
	sel, err := NewMLSelector(context.Background(), cfg)
	if err != nil {
		t.Skipf("ONNX runtime unavailable: %v", err)
	}
	t.Cleanup(func() { _ = sel.Close() })
	r := NewMLReranker(sel, 0.6)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.StartAutoReload(ctx, cfg, 50*time.Millisecond)

	// 触发mtime变化（模型内容相同的touch也会改变签名）
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tmpModel, future, future); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.Selector() != sel {
			// 已换入新selector；旧selector此时已Close（StartAutoReload负责）
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	newSel := r.Selector()
	if newSel == sel {
		t.Fatal("selector was not swapped after model file change")
	}
	if _, err := newSel.Predict(context.Background(), MLRouteFeatures{TaskType: "chat"}); err != nil {
		t.Fatalf("new selector must be usable after hot swap: %v", err)
	}
}
