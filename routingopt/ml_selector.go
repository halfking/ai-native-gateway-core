package routingopt

// ml_selector.go — P2.5: ONNX Runtime推理的AUTO路由ML选择器。
//
// 职责: 加载model.onnx + manifest.json，把路由上下文特征转换成17个命名
// ONNX输入并推理，返回预测provider与概率分布。
//
// 线程模型: AdvancedSession的Run绑定预分配的输入/输出张量，并发Run会
// 产生数据竞争，因此Predict用互斥锁串行化。单次推理为17特征小模型
// （<1ms），互斥锁不会威胁P99<10ms的hook预算。
//
// 失败模式（设计原则：ML永远不能破坏路由）:
//   - 共享库缺失/初始化失败 → NewMLSelector返回包装ErrMLUnavailable的错误
//   - 推理错误 → Predict返回错误，调用方保持规则引擎排序
// 设计文档: docs/ml/p2.5-go-onnx-inference.md

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

// ErrMLUnavailable reports that ONNX inference cannot run at all
// (missing shared library, missing model, failed init). Callers must fall
// back to rule-engine ordering when they see it (errors.Is).
var ErrMLUnavailable = errors.New("routingopt: ML inference unavailable")

// DefaultORTRuntimeVersion documents the ONNX Runtime release the vendored
// onnxruntime_go binding is built against (v1.29.0 C API).
const DefaultORTRuntimeVersion = "1.29.0"

// MLRouteFeatures carries the schema-v1 feature set the model was trained on.
// Structurally equivalent to autoroute.StructuredFeatures plus the routing
// context columns (task_type/profile/classifier/confidence); kept as a
// separate type so routingopt never imports autoroute.
type MLRouteFeatures struct {
	// Routing context (available at decision time).
	TaskType   string
	Profile    string
	Classifier string
	Confidence float64 // <0 encodes "missing" → NaN sentinel

	// Structured features (v1).
	DetectedLanguage       string
	PromptLengthBucket     string
	ContextLengthBucket    string
	TurnCountBucket        string
	HasCodeIndicator       bool
	HasMathIndicator       bool
	HasTableIndicator      bool
	HasMultimediaIndicator bool
	IntentCategory         string
	DomainHint             string
	ComplexityBucket       string
	LatencySensitive       bool
	CostSensitive          bool
}

// MLPrediction is one ONNX inference result.
type MLPrediction struct {
	// Label is the predicted provider/model (manifest label_classes entry).
	Label string

	// Probabilities maps every label class to its probability (sums to ~1).
	Probabilities map[string]float64

	// MaxProbability is Probabilities[Label].
	MaxProbability float64

	// Elapsed is the wall-clock inference time.
	Elapsed time.Duration
}

// MLSelectorConfig controls MLSelector construction.
type MLSelectorConfig struct {
	// ManifestPath points at manifest.json (model.onnx resolved from it).
	ManifestPath string

	// ORTLibraryPath is the onnxruntime shared library. Empty → try
	// ONNXRUNTIME_SHARED_LIBRARY_PATH, then OS-default search paths.
	ORTLibraryPath string

	// IntraOpNumThreads bounds CPU use per inference (0 = ORT default).
	// 1 keeps latency predictable in the routing hot path.
	IntraOpNumThreads int
}

// MLSelector is a ready-to-use ONNX inference session for AUTO routing.
type MLSelector struct {
	manifest *MLManifest

	mu          sync.Mutex // serializes Run over the shared tensors
	session     *ort.AdvancedSession
	catTensors  []*ort.StringTensor
	boolTensors []*ort.Tensor[int64]
	numTensors  []*ort.Tensor[float32]
	labelOut    *ort.StringTensor
	probOut     *ort.Tensor[float32]
}

// ortInitState guards the one-time global ONNX Runtime environment init.
var ortInitState struct {
	sync.Mutex
	initialized bool
	initErr     error
}

// initONNXRuntime initializes the global ORT environment exactly once.
// SetSharedLibraryPath must precede any other binding call, so the first
// caller wins and later callers reuse the outcome.
func initONNXRuntime(libPath string) error {
	ortInitState.Lock()
	defer ortInitState.Unlock()
	if ortInitState.initialized {
		return ortInitState.initErr
	}
	resolved := libPath
	if resolved == "" {
		resolved = os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH")
	}
	if resolved != "" {
		ort.SetSharedLibraryPath(resolved)
	}
	if err := ort.InitializeEnvironment(); err != nil {
		ortInitState.initErr = fmt.Errorf("%w: initialize onnxruntime (lib=%q): %v",
			ErrMLUnavailable, resolved, err)
		return ortInitState.initErr
	}
	ortInitState.initialized = true
	return nil
}

// NewMLSelector loads the manifest, initializes ONNX Runtime and creates the
// inference session with pre-allocated batch-1 tensors.
func NewMLSelector(ctx context.Context, cfg MLSelectorConfig) (*MLSelector, error) {
	if cfg.ManifestPath == "" {
		return nil, fmt.Errorf("%w: manifest path is empty", ErrMLUnavailable)
	}
	manifest, err := LoadMLManifest(cfg.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMLUnavailable, err)
	}
	modelPath := manifest.ModelPath(cfg.ManifestPath)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMLUnavailable, err)
	}
	if err := initONNXRuntime(cfg.ORTLibraryPath); err != nil {
		return nil, err
	}

	// Pre-allocate fixed batch-1 input tensors in manifest input order.
	catTensors := make([]*ort.StringTensor, 0, len(manifest.Features.Categorical))
	boolTensors := make([]*ort.Tensor[int64], 0, len(manifest.Features.Boolean))
	numTensors := make([]*ort.Tensor[float32], 0, len(manifest.Features.Numeric))
	var inputs []ort.Value
	defer func() {
		if err != nil {
			destroyTensors(catTensors, boolTensors, numTensors)
		}
	}()
	batch := ort.Shape{1, 1}
	for range manifest.Features.Categorical {
		t, e := ort.NewStringTensor(batch)
		if e != nil {
			return nil, fmt.Errorf("create categorical input: %w", e)
		}
		catTensors = append(catTensors, t)
		inputs = append(inputs, t)
	}
	for range manifest.Features.Boolean {
		t, e := ort.NewTensor(batch, []int64{mlBooleanMissing})
		if e != nil {
			return nil, fmt.Errorf("create boolean input: %w", e)
		}
		boolTensors = append(boolTensors, t)
		inputs = append(inputs, t)
	}
	for range manifest.Features.Numeric {
		t, e := ort.NewTensor(batch, []float32{float32(math.NaN())})
		if e != nil {
			return nil, fmt.Errorf("create numeric input: %w", e)
		}
		numTensors = append(numTensors, t)
		inputs = append(inputs, t)
	}

	nClasses := int64(len(manifest.LabelClasses))
	labelOut, err := ort.NewStringTensor(ort.Shape{1})
	if err != nil {
		return nil, fmt.Errorf("create label output: %w", err)
	}
	probOut, err := ort.NewTensor(ort.Shape{1, nClasses}, make([]float32, nClasses))
	if err != nil {
		return nil, fmt.Errorf("create probability output: %w", err)
	}
	outputs := []ort.Value{labelOut, probOut}
	outputNames := []string{manifest.OutputLabelName, manifest.OutputProbabilityName}

	var opts *ort.SessionOptions
	if cfg.IntraOpNumThreads > 0 {
		opts, err = ort.NewSessionOptions()
		if err != nil {
			return nil, fmt.Errorf("create session options: %w", err)
		}
		if e := opts.SetIntraOpNumThreads(cfg.IntraOpNumThreads); e != nil {
			opts.Destroy()
			return nil, fmt.Errorf("set intra-op threads: %w", e)
		}
	}
	session, err := ort.NewAdvancedSession(modelPath, manifest.InputNames(),
		outputNames, inputs, outputs, opts)
	if err != nil {
		return nil, fmt.Errorf("%w: create session from %s: %v",
			ErrMLUnavailable, modelPath, err)
	}

	return &MLSelector{
		manifest:    manifest,
		session:     session,
		catTensors:  catTensors,
		boolTensors: boolTensors,
		numTensors:  numTensors,
		labelOut:    labelOut,
		probOut:     probOut,
	}, nil
}

// Manifest exposes the loaded manifest (for tests and admin diagnostics).
func (s *MLSelector) Manifest() *MLManifest { return s.manifest }

// Predict runs one inference. Errors never panic the routing path — callers
// fall back to rule-engine ordering on any error.
func (s *MLSelector) Predict(ctx context.Context, f MLRouteFeatures) (*MLPrediction, error) {
	start := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Fill categorical inputs (unknown → "__missing__").
	catValues := []string{f.TaskType, f.Profile, f.Classifier, f.DetectedLanguage,
		f.PromptLengthBucket, f.ContextLengthBucket, f.TurnCountBucket,
		f.IntentCategory, f.DomainHint, f.ComplexityBucket}
	if len(catValues) != len(s.catTensors) {
		return nil, fmt.Errorf("feature/manifest mismatch: %d categorical values vs %d tensors",
			len(catValues), len(s.catTensors))
	}
	for i, t := range s.catTensors {
		v := catValues[i]
		if v == "" {
			v = mlMissingCategory
		}
		if err := t.SetContents([]string{v}); err != nil {
			return nil, fmt.Errorf("set categorical input %d: %w", i, err)
		}
	}

	// Boolean features: true=1, false=0 (no missing case at decision time).
	boolValues := []bool{f.HasCodeIndicator, f.HasMathIndicator, f.HasTableIndicator,
		f.HasMultimediaIndicator, f.LatencySensitive, f.CostSensitive}
	for i, t := range s.boolTensors {
		v := mlBooleanMissing
		if i < len(boolValues) && boolValues[i] {
			v = 1
		}
		t.GetData()[0] = v
	}

	// Numeric: negative confidence encodes "missing" → NaN (median-imputed in-graph).
	conf := float32(f.Confidence)
	if f.Confidence < 0 {
		conf = float32(math.NaN())
	}
	for _, t := range s.numTensors {
		t.GetData()[0] = conf
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.session.Run(); err != nil {
		return nil, fmt.Errorf("onnx run: %w", err)
	}

	labels, err := s.labelOut.GetContents()
	if err != nil {
		return nil, fmt.Errorf("read label output: %w", err)
	}
	probs := s.probOut.GetData()
	if len(labels) != 1 || len(probs) != len(s.manifest.LabelClasses) {
		return nil, fmt.Errorf("unexpected output shape: label=%d probs=%d (want 1/%d)",
			len(labels), len(probs), len(s.manifest.LabelClasses))
	}
	dist := make(map[string]float64, len(probs))
	var total float64
	for i, p := range probs {
		dist[s.manifest.LabelClasses[i]] = float64(p)
		total += float64(p)
	}
	if total <= 0 || math.IsNaN(total) {
		return nil, fmt.Errorf("degenerate probability distribution (sum=%v)", total)
	}
	return &MLPrediction{
		Label:          labels[0],
		Probabilities:  dist,
		MaxProbability: dist[labels[0]],
		Elapsed:        time.Since(start),
	}, nil
}

// Close releases the session and all tensors.
func (s *MLSelector) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		_ = s.session.Destroy()
		s.session = nil
	}
	destroyTensors(s.catTensors, s.boolTensors, s.numTensors)
	s.catTensors, s.boolTensors, s.numTensors = nil, nil, nil
	if s.labelOut != nil {
		_ = s.labelOut.Destroy()
		s.labelOut = nil
	}
	if s.probOut != nil {
		_ = s.probOut.Destroy()
		s.probOut = nil
	}
	return nil
}

func destroyTensors(cat []*ort.StringTensor, boolean []*ort.Tensor[int64],
	num []*ort.Tensor[float32]) {
	for _, t := range cat {
		_ = t.Destroy()
	}
	for _, t := range boolean {
		_ = t.Destroy()
	}
	for _, t := range num {
		_ = t.Destroy()
	}
}
