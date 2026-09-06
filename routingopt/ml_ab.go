package routingopt

// ml_ab.go — P2.5/A/B: 流量分配门 + ML运行统计 + 模型热更新。
//
// ABGate: 确定性分桶（FNV-1a over sessionID/apiKeyID），同一会话始终落在
// 同一组，保证用户体验稳定。实现 settings.RoutingOptFeatureFlags 中早已
// 声明但未接线的 ABTestEnabled/ABTestPercentage 语义：
//   treatment组 → 走优化器（含ML重排序）
//   control组   → 原样规则引擎（recommendWithOptimizer短路）
//
// MLStats: 无锁计数器，供 /api/admin/routing-opt/ml 诊断端点读取。
//
// 热更新: MLReranker.StartAutoReload轮询manifest+model的mtime/size，
// 变化时构建新MLSelector原子换入（MLSelector.lifeMu保证旧session在
// 在途推理结束后才销毁）。ROUTING_ML_RELOAD_SECONDS控制，0=禁用。

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// ABGate
// ---------------------------------------------------------------------------

// ABGate splits traffic deterministically between treatment (optimizer+ML)
// and control (baseline rule engine). Safe for concurrent use.
type ABGate struct {
	pctBasisPoints int // 0..10000, treatment share ×100

	treatment atomic.Int64
	control   atomic.Int64
	evaluated atomic.Int64
}

// NewABGate builds a gate from a fraction (0.0-1.0). Values outside the
// range are clamped; >=1 means always-treatment (gate effectively off).
func NewABGate(pct float64) *ABGate {
	switch {
	case pct < 0:
		pct = 0
	case pct > 1:
		pct = 1
	}
	return &ABGate{pctBasisPoints: int(pct * 10000)}
}

// Treatment reports whether this request belongs to the treatment group.
// Bucketing is stable per (sessionID, apiKeyID): the same session always
// gets the same experience. No session and no key → deterministic on 0.
func (g *ABGate) Treatment(sessionID string, apiKeyID int) bool {
	g.evaluated.Add(1)
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	_, _ = fmt.Fprintf(h, "|%d", apiKeyID)
	bucket := int(h.Sum32() % 10000)
	isTreatment := bucket < g.pctBasisPoints
	if isTreatment {
		g.treatment.Add(1)
	} else {
		g.control.Add(1)
	}
	return isTreatment
}

// Snapshot returns counters for the admin diagnostics endpoint.
func (g *ABGate) Snapshot() map[string]any {
	if g == nil {
		return nil
	}
	t := g.treatment.Load()
	c := g.control.Load()
	total := t + c
	var sharePct float64
	if total > 0 {
		sharePct = float64(t) / float64(total) * 100
	}
	return map[string]any{
		"treatment_count":     t,
		"control_count":       c,
		"evaluated":           g.evaluated.Load(),
		"treatment_basis_bps": g.pctBasisPoints,
		"observed_treatment_pct": round2(sharePct),
	}
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// ---------------------------------------------------------------------------
// MLStats
// ---------------------------------------------------------------------------

// MLStats aggregates the ML re-ranker's runtime behaviour for diagnostics.
type MLStats struct {
	Predictions   atomic.Int64 // successful ONNX inferences
	Failures      atomic.Int64 // Predict errors (fallback to rule order)
	LowConfidence atomic.Int64 // max(prob) below threshold, prediction ignored
	NoMatch       atomic.Int64 // predicted label matched no candidate
	Boosts        atomic.Int64 // candidates actually reordered
	TotalLatencyNs atomic.Int64
}

// Record accumulates one Rerank outcome.
func (s *MLStats) Record(success, lowConf, noMatch, boosted bool, latency time.Duration) {
	if s == nil {
		return
	}
	s.TotalLatencyNs.Add(int64(latency))
	switch {
	case !success:
		s.Failures.Add(1)
	case lowConf:
		s.LowConfidence.Add(1)
	case noMatch:
		s.NoMatch.Add(1)
	default:
		s.Predictions.Add(1)
		if boosted {
			s.Boosts.Add(1)
		}
	}
}

// Snapshot renders counters for the admin diagnostics endpoint.
func (s *MLStats) Snapshot() map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"predictions_served":   s.Predictions.Load(),
		"failures":             s.Failures.Load(),
		"low_confidence_skips": s.LowConfidence.Load(),
		"no_match_skips":       s.NoMatch.Load(),
		"candidates_boosted":   s.Boosts.Load(),
		"avg_latency_us": round2(float64(s.TotalLatencyNs.Load()) / 1000 /
			max64(1, float64(s.Predictions.Load()+s.LowConfidence.Load()+s.NoMatch.Load()))),
	}
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Hot reload
// ---------------------------------------------------------------------------

// StartAutoReload polls the manifest + model files and atomically swaps in a
// fresh MLSelector whenever either changes (size+mtime). Runs until ctx is
// cancelled. interval <= 0 disables. Errors are logged and retried on the
// next tick — the currently loaded model keeps serving.
func (r *MLReranker) StartAutoReload(ctx context.Context, cfg MLSelectorConfig,
	interval time.Duration) {
	if r == nil || interval <= 0 {
		return
	}
	// Baseline signature is taken synchronously so changes made before (or
	// concurrently with) this call are never swallowed by the goroutine.
	sig := mlFileSignature(cfg.ManifestPath)
	go func() {
		slog.InfoContext(ctx, "routingopt: ML model auto-reload enabled",
			"interval", interval, "manifest", cfg.ManifestPath)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			next := mlFileSignature(cfg.ManifestPath)
			if next == sig {
				continue
			}
			slog.InfoContext(ctx, "routingopt: ML model change detected, reloading",
				"manifest", cfg.ManifestPath)
			newSel, err := NewMLSelector(ctx, cfg)
			if err != nil {
				slog.WarnContext(ctx, "routingopt: ML reload failed, keeping current model",
					"err", err)
				continue
			}
			old := r.SwapSelector(newSel)
			sig = next
			if old != nil {
				_ = old.Close()
			}
			slog.InfoContext(ctx, "routingopt: ML model reloaded")
		}
	}()
}

// mlFileSignature summarizes the manifest and its model file for change
// detection. Missing files yield "?" so the first successful load flips it.
func mlFileSignature(manifestPath string) string {
	sig := fileSig(manifestPath)
	if sig == "?" {
		return sig
	}
	m, err := LoadMLManifest(manifestPath)
	if err != nil {
		return sig
	}
	return sig + "|" + fileSig(m.ModelPath(manifestPath))
}

func fileSig(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "?"
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}
