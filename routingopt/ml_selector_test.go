package routingopt

// ml_selector_test.go — P2.5 ML选择器测试。
//
// 纯单元测试（manifest解析、候选匹配、禁用语义）总是运行；
// 需要ONNX Runtime共享库的集成测试在库不可用时跳过（t.Skip），
// 保证无原生依赖的环境（CI/开发机）同样全绿。
//
// 本地启用集成测试:
//
//	export ONNXRUNTIME_SHARED_LIBRARY_PATH=/path/to/libonnxruntime.dylib
//	go test ./routingopt/ -run TestMLSelector
//
// 库版本: onnxruntime 1.29.0（与 vendored onnxruntime_go v1.36.0 匹配）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const mlFixtureDir = "testdata/ml_fixture"

func writeTempManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMLManifestFromFixture(t *testing.T) {
	m, err := LoadMLManifest(filepath.Join(mlFixtureDir, "manifest.json"))
	if err != nil {
		t.Fatalf("fixture manifest should load: %v", err)
	}
	if m.SchemaVersion != MLManifestSchemaV1 {
		t.Errorf("schema_version = %q", m.SchemaVersion)
	}
	if m.NumInputs() != 17 {
		t.Errorf("NumInputs = %d, want 17 (schema v1)", m.NumInputs())
	}
	if len(m.Features.Categorical) != 10 || len(m.Features.Boolean) != 6 ||
		len(m.Features.Numeric) != 1 {
		t.Errorf("feature columns = %d/%d/%d, want 10/6/1",
			len(m.Features.Categorical), len(m.Features.Boolean), len(m.Features.Numeric))
	}
	if len(m.LabelClasses) < 2 {
		t.Errorf("label_classes = %v", m.LabelClasses)
	}
	// 输入顺序: cat → bool → num
	names := m.InputNames()
	if names[0] != m.Features.Categorical[0] ||
		names[len(m.Features.Categorical)] != m.Features.Boolean[0] ||
		names[len(names)-1] != m.Features.Numeric[0] {
		t.Errorf("InputNames order broken: %v", names)
	}
	// ONNX文件解析为相对manifest目录
	if want := filepath.Join(mlFixtureDir, m.ModelFile); m.ModelPath(filepath.Join(mlFixtureDir, "manifest.json")) != want {
		t.Errorf("ModelPath = %s, want %s", m.ModelPath("x"), want)
	}
}

func TestLoadMLManifestRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"bad schema":  `{"schema_version":"v9","model_file":"m.onnx","label_classes":["a","b"],"features":{"categorical":["x"]}}`,
		"no model":    `{"schema_version":"v1","label_classes":["a","b"],"features":{"categorical":["x"]}}`,
		"one label":   `{"schema_version":"v1","model_file":"m.onnx","label_classes":["a"],"features":{"categorical":["x"]}}`,
		"no features": `{"schema_version":"v1","model_file":"m.onnx","label_classes":["a","b"],"features":{}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadMLManifest(writeTempManifest(t, content)); err == nil {
				t.Fatalf("invalid manifest accepted")
			}
		})
	}
	if _, err := LoadMLManifest(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing manifest accepted")
	}
}

func TestMatchCandidateIndex(t *testing.T) {
	cands := []ModelCandidate{
		{CanonicalName: "glm-4.6"},
		{CanonicalName: "claude-3.5-sonnet", RawModel: "claude-3-5-sonnet-2"},
		{CanonicalName: "gpt-4-2024-11-20"},
	}
	cases := []struct {
		label string
		want  int
	}{
		{"glm-4.6", 0},
		{"CLAude-3.5-Sonnet", 1}, // 大小写不敏感精确匹配
		{"gpt-4", 2},             // 前缀兼容（无版本label vs 带日期候选）
		{"unknown-provider", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := MatchCandidateIndex(tc.label, cands); got != tc.want {
			t.Errorf("MatchCandidateIndex(%q) = %d, want %d", tc.label, got, tc.want)
		}
	}
	if got := MatchCandidateIndex("x", nil); got != -1 {
		t.Errorf("empty candidate list should return -1, got %d", got)
	}
}

func TestMLRerankerNilIsDisabled(t *testing.T) {
	var r *MLReranker
	if r.Enabled() {
		t.Fatal("nil reranker must be disabled")
	}
	cands := []ModelCandidate{{CanonicalName: "a"}, {CanonicalName: "b"}}
	out, pred := r.Rerank(context.Background(), cands, MLRouteFeatures{})
	if len(out) != 2 || pred != nil {
		t.Fatal("disabled reranker must pass through candidates unchanged")
	}
}

// ortLibPath resolves the shared library for integration tests; empty means
// unavailable and the caller skips.
func ortLibPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("ONNXRUNTIME_SHARED_LIBRARY_PATH=%s does not exist", p)
		}
		return p
	}
	return "" // empty → selector uses OS default search; init failure skips
}

// TestMLSelectorEndToEnd runs the real ONNX fixture model. Skips when the
// runtime library cannot be initialized (no native deps in CI).
func TestMLSelectorEndToEnd(t *testing.T) {
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
		if errors.Is(err, ErrMLUnavailable) {
			t.Skipf("ONNX runtime unavailable: %v", err)
		}
		t.Fatalf("unexpected selector error: %v", err)
	}
	t.Cleanup(func() { _ = sel.Close() })

	pred, err := sel.Predict(context.Background(), MLRouteFeatures{
		TaskType: "chat", Profile: "quality", Classifier: "heuristic",
		Confidence: 0.85, DetectedLanguage: "en",
		PromptLengthBucket: "m", ContextLengthBucket: "none",
		TurnCountBucket: "single", IntentCategory: "question",
		DomainHint: "general", ComplexityBucket: "simple",
		HasTableIndicator: true,
	})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if pred.Label == "" {
		t.Fatal("empty prediction label")
	}
	if _, ok := pred.Probabilities[pred.Label]; !ok {
		t.Fatalf("probabilities missing predicted label %q: %v", pred.Label, pred.Probabilities)
	}
	var sum float64
	for _, p := range pred.Probabilities {
		sum += p
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("probabilities sum to %v, want ~1.0", sum)
	}
	if pred.Elapsed > 50*time.Millisecond {
		t.Errorf("inference took %v, hot-path budget is <10ms p99", pred.Elapsed)
	}

	// 同一session可重复推理（互斥锁串行化下的第二次调用）
	if _, err := sel.Predict(context.Background(), MLRouteFeatures{TaskType: "code"}); err != nil {
		t.Fatalf("second Predict failed: %v", err)
	}
}

// TestMLSelectorUnavailableModel verifies the graceful-failure contract:
// missing model → ErrMLUnavailable, never a panic.
func TestMLSelectorUnavailableModel(t *testing.T) {
	manifestPath := writeTempManifest(t,
		`{"schema_version":"v1","model_file":"nope.onnx","label_classes":["a","b"],"features":{"categorical":["x"],"boolean":[],"numeric":[]}}`)
	_, err := NewMLSelector(context.Background(), MLSelectorConfig{
		ManifestPath:   manifestPath,
		ORTLibraryPath: ortLibPath(t),
	})
	if err == nil {
		t.Fatal("missing model must fail")
	}
	if !errors.Is(err, ErrMLUnavailable) {
		t.Errorf("error should wrap ErrMLUnavailable, got: %v", err)
	}
}
