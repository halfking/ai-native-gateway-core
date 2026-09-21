package executors

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestMain 静音默认 slog，避免 BenchmarkPlanByTier 的生产路径 slog.Info
// （router_scoring.go:215-216，10% 抽样 LOAD_SCORE_V2）污染 ns/op 测量。
// 测试运行模式（`go test`）仍保留默认 slog 行为——只在 bench 标志下丢弃。
func TestMain(m *testing.M) {
	if os.Getenv("KEEP_SLOG") != "1" {
		// 仅在 bench 模式下丢弃 slog 输出
		_ = os.Setenv("LOG_LEVEL", "error")
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}

// R48 §2 D 基准先行：BenchmarkPlanByTier(n=10, s=50)。
//
// 目标：
//   1) 记录 R48 A2/B 改造**前**的 planByTier 全链路 p99（含
//      calculateLoadScore × N candidate + StickyLoad.Info × N × 2 + envFloat × N × 6）
//   2) 改造完成后同一命令复跑，作为 A2/B 收益量化证据
//
// 与 router_two_layer_cost_test.go 已存在的 twoLayerCreds() 共享测试助手。
// 设计：n = 候选数（默认 10 = D01 §三 "n=10"），s = 活跃会话数（默认 50 = D01 §三 "s=50"）。

// benchCandSet generates n provider.Candidate with a mix of priority/standard
// (matching twoLayerCreds semantics), each carrying ConcurrencyLimit = 4
// and varied ManualPriority for the priority bucket to exercise lottery weight.
func benchCandSet(n int) []provider.Candidate {
	lim := 4
	out := make([]provider.Candidate, 0, n)
	for i := 0; i < n; i++ {
		c := provider.Candidate{
			CredentialID:    i + 1,
			ProviderID:     1,
			RawModel:       "m",
			StandardizedName: fmt.Sprintf("model-%d", i+1),
			Tier:           2,
			Weight:         100,
			ConcurrencyLimit: &lim,
			QuotaState:      "ok",
		}
		// ~2/3 priority, 1/3 standard — exercise the two-layer partition path
		if i*3 < n*2 {
			c.Priority = true
			c.ManualPriority = (i % 3) + 1
		}
		out = append(out, c)
	}
	return out
}

// benchLiveLoad implements the Router.LiveLoad interface for n candidates
// with `s` total in-flight sessions distributed by a deterministic hash
// (so bench runs are reproducible — rand.NewSource is seeded).
type benchLiveLoad struct {
	concurrent map[credModelKey]int64
}

func (b *benchLiveLoad) GetLiveConcurrent(credID int64, model string) int64 {
	return b.concurrent[credModelKey{CredID: credID, Model: model}]
}

// benchStickyLoad implements StickyLoadView for bench purposes.
// Info() returns a deterministic per-credential session count and last-activity
// timestamp derived from the same seed as benchLiveLoad, so planByTier takes
// the same hot path as production. Refresh() is a no-op (Refresh throttling is
// not the bottleneck under test).
type benchStickyLoad struct {
	sessions  map[int]int
	updatedAt map[int]int64 // unix ms; 0 = no signal
}

func (b *benchStickyLoad) Refresh(_ []int) {}

func (b *benchStickyLoad) Info(credentialID int) StickyLoadInfo {
	return StickyLoadInfo{
		Sessions:       b.sessions[credentialID],
		LastActivityMs: b.updatedAt[credentialID],
	}
}

// Snapshot 实现 StickyLoadView 接口（R48 §4 B 方案）。
// 从预填充的 b.sessions/b.updatedAt 直接构造 map，无锁——与 Info() 等价语义。
func (b *benchStickyLoad) Snapshot(credIDs []int) map[int]StickyLoadInfo {
	out := make(map[int]StickyLoadInfo, len(credIDs))
	for _, id := range credIDs {
		if id <= 0 {
			continue
		}
		out[id] = StickyLoadInfo{
			Sessions:       b.sessions[id],
			LastActivityMs: b.updatedAt[id],
		}
	}
	return out
}

func (b *benchStickyLoad) ObserveSession(credentialID int, _ string) {}

func (b *benchStickyLoad) ObserveActivity(_ int) {}

// buildBenchRouter constructs a Router seeded with n candidates × s sessions
// across the live-load view + sticky view. Sessions are distributed so that
// `s` total sessions are spread roughly evenly (with jitter) across n creds,
// and a small subset is "recent" so recentRequestPenalty contributes variance.
func buildBenchRouter(b *testing.B, n, s int) *Router {
	b.Helper()
	rng := rand.New(rand.NewSource(1)) // deterministic
	r := NewRouter(nil, nil)

	ll := &benchLiveLoad{concurrent: make(map[credModelKey]int64, n)}
	sl := &benchStickyLoad{
		sessions:  make(map[int]int, n),
		updatedAt: make(map[int]int64, n),
	}
	// Distribute s sessions across n creds with jitter
	base := s / n
	rem := s % n
	for i := 1; i <= n; i++ {
		share := base
		if i <= rem {
			share++
		}
		// small jitter (≤10%) keeps the bench realistic without being noisy
		jitter := rng.Intn(share/10 + 1)
		share += jitter
		ll.concurrent[credModelKey{CredID: int64(i), Model: "m"}] = int64(share)
		sl.sessions[i] = share
		// mark ~half as recent (within recency horizon)
		if rng.Intn(2) == 0 {
			sl.updatedAt[i] = 1_000_000_000_000 // some fixed past ms
		}
	}
	r.LiveLoad = ll
	r.StickyLoad = sl
	return r
}

// BenchmarkPlanByTier_n10_s50 是 D01 §三 plan 中 n=10 s=50 的命名基准。
// 它**不**通过 PlanCandidates（避免 V2/冷却/URS 等旁路），直接走 planByTier
// 内循环——A2/B 改造前后 planByTier 自身 p99 是最干净的热路径指标。
//
// 用法：
//   go test -bench=BenchmarkPlanByTier_n10_s50 -benchtime=10s -count=5 -run=^$ \
//     ./domains/streaming/executors/
//   go test -bench=BenchmarkPlanByTier_n10_s50 -benchtime=10s -count=5 -run=^$ \
//     -cpuprofile=/tmp/pbby.cpu ./domains/streaming/executors/   # CPU profile
func BenchmarkPlanByTier_n10_s50(b *testing.B) {
	const n = 10
	const s = 50
	r := buildBenchRouter(b, n, s)
	cands := benchCandSet(n)
	policy := &provider.Policy{TierFallbackMax: 4}
	stratIn := StrategyInput{
		Policy:           policy,
		EgressPreference: nil,
		TenantID:         "bench-tenant",
		Canonical:        "bench-model",
		RequestID:        "bench-req",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := r.planByTier(context.Background(), cands, policy, stratIn)
		if len(out) == 0 {
			b.Fatalf("planByTier returned empty on iter %d", i)
		}
	}
}

// BenchmarkPlanByTierScale 在 n=2/5/10/20 四档上跑，量化候选数×会话数对热路径的影响。
// 用法：
//   go test -bench=BenchmarkPlanByTierScale -benchtime=5s -count=3 -run=^$ \
//     ./domains/streaming/executors/
func BenchmarkPlanByTierScale(b *testing.B) {
	policy := &provider.Policy{TierFallbackMax: 4}
	stratIn := StrategyInput{
		Policy:    policy,
		TenantID:  "bench-tenant",
		Canonical: "bench-model",
		RequestID: "bench-req",
	}
	cases := []struct{ n, s int }{
		{2, 10},
		{5, 25},
		{10, 50},
		{20, 100},
	}
	for _, tc := range cases {
		tc := tc
		name := "n=" + strconv.Itoa(tc.n) + "_s=" + strconv.Itoa(tc.s)
		b.Run(name, func(b *testing.B) {
			r := buildBenchRouter(b, tc.n, tc.s)
			cands := benchCandSet(tc.n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out := r.planByTier(context.Background(), cands, policy, stratIn)
				if len(out) == 0 {
					b.Fatalf("planByTier returned empty on iter %d (case %s)", i, name)
				}
			}
		})
	}
}
