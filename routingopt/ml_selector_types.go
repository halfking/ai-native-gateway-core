package routingopt

// ml_selector_types.go — ML 选择器的纯数据契约（无 cgo 依赖）。
//
// onnxruntime_go 是纯 cgo 绑定：CGO_ENABLED=0 下该包没有任何可编译文件，
// 任何无条件 import 都会让网关无法出包（2026-09-24 打包审计 N-1）。因此
// ML 实现按 build tag 隔离 —— ml_selector.go 持有 cgo 实现（//go:build cgo），
// ml_selector_nocgo.go 是降级桩（//go:build !cgo，按既有设计原则返回
// ErrMLUnavailable 回落纯规则排序）。本文件承载两侧共用的类型与哨兵错误，
// 保证两种构建下的公开签名逐字一致。

import (
	"errors"
	"time"
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
