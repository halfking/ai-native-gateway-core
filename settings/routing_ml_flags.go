package settings

import "os"

// routing_ml_flags.go — P2.5: ONNX ML路由重排序的配置项。
//
// ML重排序是RealOptimizer的可选增强，必须同时满足:
//   ROUTING_OPT_ENABLED=true（P2.2插件总开关）
//   ROUTING_ML_ENABLED=true（本组开关）
// 任何构造失败（模型/manifest/共享库缺失）都会降级为纯规则排序，
// 只记Warn日志，绝不阻塞网关启动。
//
// 设计: docs/ml/p2.5-go-onnx-inference.md

// RoutingMLFlags controls the P2.5 ONNX ML re-ranker.
type RoutingMLFlags struct {
	// Enabled is the ML re-ranker master switch.
	// Requires ROUTING_OPT_ENABLED=true to take effect.
	Enabled bool

	// ManifestPath points at manifest.json exported by ml-training
	// (model.onnx is resolved relative to it).
	ManifestPath string

	// ORTLibraryPath is the onnxruntime shared library path
	// (libonnxruntime.dylib / .so / .dll, v1.29.0 C API).
	// Empty → ONNXRUNTIME_SHARED_LIBRARY_PATH → OS default search.
	ORTLibraryPath string

	// MinConfidence is the probability threshold below which the ML
	// prediction is ignored (rule-engine order kept). 0.5-0.7 recommended
	// until real-data accuracy is validated.
	MinConfidence float64

	// IntraOpNumThreads bounds per-inference CPU use (1 = hot-path safe).
	IntraOpNumThreads int

	// ReloadSeconds is the model hot-reload poll interval. 0 (default)
	// disables auto-reload; the startup model serves until restart.
	// Recommended: 30 for A/B production runs.
	ReloadSeconds int
}

// GetRoutingMLFlags reads ROUTING_ML_* environment variables with safe
// defaults (ML disabled).
func GetRoutingMLFlags() *RoutingMLFlags {
	return &RoutingMLFlags{
		Enabled:           envBool("ROUTING_ML_ENABLED", false),
		ManifestPath:      envString("ROUTING_ML_MANIFEST_PATH", ""),
		ORTLibraryPath:    envString("ROUTING_ML_ORT_LIB_PATH", ""),
		MinConfidence:     envFloat("ROUTING_ML_MIN_CONFIDENCE", 0.6),
		IntraOpNumThreads: envInt("ROUTING_ML_INTRA_OP_THREADS", 1),
		ReloadSeconds:     envInt("ROUTING_ML_RELOAD_SECONDS", 0),
	}
}

// envString reads an env var, returning defaultValue when unset/empty.
func envString(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
