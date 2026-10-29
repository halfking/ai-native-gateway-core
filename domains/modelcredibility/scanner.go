// scanner.go — 扫描编排：把探测、参考数据、判定、落盘串起来。
//
// scanner.go — scan orchestration: wire the prober, the reference data, the
// verdict engine and persistence together.
//
// # 一次扫描做了什么（每个 凭据×模型 节点）
//
//  1. 读该节点最近一次**成功**的智商实测准确率（没有 ⇒ 能力维度缺失）。
//  2. 读同款 canonical 模型的同伴分布。
//  3. 读平台级校准（有缓存，避免每次扫描都全表拟合）。
//  4. 读该节点上一次的签名基线（用于漂移检测）。
//  5. 直连上游发一次标定请求，取回身份/签名信号。
//  6. 判定 → 落库。
//
// # 为什么校准要缓存
//
// 校准拟合要扫全量 model_iq_runs（按 canonical 聚合），每扫描一个节点
// 都重算一遍是纯浪费。校准本身是**平台级慢变量**（模型集合变了才变），
// 所以按 TTL 缓存即可。
package modelcredibility

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// CalibrationCacheTTL 是校准结果的缓存时长。
const CalibrationCacheTTL = 30 * time.Minute

// Scanner 是节点扫描器。
type Scanner struct {
	Store  *Store
	Prober Prober
	Config Config

	calMu       sync.Mutex
	calCache    Calibration
	calCachedAt time.Time
	calPoints   int
}

// NewScanner 创建扫描器。
func NewScanner(store *Store, prober Prober, cfg Config) *Scanner {
	return &Scanner{Store: store, Prober: prober, Config: cfg}
}

// Calibration 返回当前生效的校准（带缓存）。
//
// 缓存过期或从未算过时重新拟合。拟合不可用时返回的 Calibration.Usable 为
// false —— 调用方据此把参考基线维度记为「未测量」。
func (s *Scanner) Calibration(ctx context.Context) (Calibration, error) {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	if !s.calCachedAt.IsZero() && time.Since(s.calCachedAt) < CalibrationCacheTTL {
		return s.calCache, nil
	}
	points, err := s.Store.LoadCalibrationPoints(ctx)
	if err != nil {
		return s.calCache, err
	}
	cal := FitCalibration(points, s.Config.Calibration)
	cal.ProviderN = countDistinctProviders(points)
	s.calCache = cal
	s.calCachedAt = time.Now()
	s.calPoints = len(points)
	slog.Info("model credibility: calibration refreshed",
		"points", len(points), "usable", cal.Usable(s.Config.Calibration),
		"slope", cal.Slope, "r_squared", cal.RSquared, "providers", cal.ProviderN)
	return cal, nil
}

// countDistinctProviders 统计校准点覆盖的供应商数。
func countDistinctProviders(points []CalibrationPoint) int {
	seen := map[int64]struct{}{}
	for _, p := range points {
		for _, pid := range p.ProviderIDs {
			seen[pid] = struct{}{}
		}
	}
	return len(seen)
}

// ScanNode 扫描一个节点并返回判定结果。
//
// ★ 探测失败**不**返回 error：探测失败是一个**结论**（证据不足），
// 不是调用失败。调用方要拿到 Result（status=failed）才能落库留痕。
// 只有参考数据读取失败才返回 error（那是真的没跑成）。
func (s *Scanner) ScanNode(ctx context.Context, target NodeTarget) (Result, error) {
	if s.Store == nil {
		return Result{}, fmt.Errorf("scanner: store is nil")
	}
	if s.Prober == nil {
		return Result{}, fmt.Errorf("scanner: prober is nil")
	}

	in := Input{Target: target, Now: nowFunc()}

	// 能力实测：只取 status='success' 的行。
	if m, ok, err := s.Store.LatestAccuracy(ctx, target.CredentialID, target.RawModelName); err != nil {
		return Result{}, fmt.Errorf("load latest accuracy: %w", err)
	} else if ok {
		in.Accuracy = m.Accuracy
		in.HasAccuracy = true
	}

	// 同伴分布。
	if target.CanonicalID > 0 {
		ref, err := s.Store.LoadPeerReference(ctx, target.CanonicalID)
		if err != nil {
			return Result{}, fmt.Errorf("load peer reference: %w", err)
		}
		in.Peer = ref
		if ref.CanonicalName != "" {
			in.CanonicalName = ref.CanonicalName
		}
	}

	// 外部基线。
	if target.CanonicalID > 0 {
		iq, err := s.Store.StandardIQ(ctx, target.CanonicalID)
		if err != nil {
			return Result{}, fmt.Errorf("load standard iq: %w", err)
		}
		in.StandardIQ = iq
	}

	// 校准。
	cal, err := s.Calibration(ctx)
	if err != nil {
		// 校准失败不致命：参考基线维度按缺失处理。
		slog.Warn("model credibility: calibration unavailable", "error", err)
	} else {
		in.Calibration = cal
	}

	// 上一次签名基线（含 verdict / verified 连击，供共识门）。
	prev, err := s.Store.LoadPrevious(ctx, target.CredentialID, target.RawModelName)
	if err != nil {
		return Result{}, fmt.Errorf("load previous verdict: %w", err)
	}
	in.Previous = prev

	// 被动完整性信号（真实流量侧的 model_mismatch / fingerprint_drift）。
	// ★ 读取失败**降级不致命**：被动通道拿不到时主动判定照常
	// （211 实测网关连库角色可能没有该表读权限）。空信号 ≠ 读失败，
	// 两者在 evidence 里分别留痕。
	passive, perr := s.Store.LoadPassiveSignals(ctx, target.CredentialID, target.RawModelName)
	if perr != nil {
		slog.Warn("model credibility: passive signals unavailable (degraded)",
			"credential_id", target.CredentialID, "model", target.RawModelName, "error", perr)
		in.PassiveReadFailed = true
	} else {
		in.Passive = passive
	}

	// 身份探测。
	obs, probeErr := s.Prober.Probe(ctx, target)
	in.Observed = obs
	in.ProbeErr = probeErr

	res := Evaluate(in, s.Config)
	if err := s.Store.SaveResult(ctx, res); err != nil {
		return res, fmt.Errorf("save credibility result: %w", err)
	}
	return res, nil
}

// ScanTargets 批量扫描，逐个节点判定。
//
// ★ 逐个串行执行（不并发）：探测会消耗真实 token，而且同一供应商的
// 并发探测容易被上游限流，反而拿不到有效观测。宁可慢，也要拿到干净数据。
func (s *Scanner) ScanTargets(ctx context.Context, targets []NodeTarget) ([]Result, error) {
	out := make([]Result, 0, len(targets))
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := s.ScanNode(ctx, t)
		if err != nil {
			slog.Warn("model credibility: scan node failed", "credential_id", t.CredentialID,
				"model", t.RawModelName, "error", err)
			continue
		}
		out = append(out, res)
	}
	return out, nil
}
