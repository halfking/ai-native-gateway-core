package executors

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestCalculateLoadScore_StickySnapshot_Preferred（钉 R48 §4 B）：
// 当 StrategyInput.StickySnapshot 非 nil 且包含目标凭据时，
// stickySessionPenalty/recentRequestPenalty 必须走快照路径——
// 即读快照里的 Sessions/LastActivityMs，而不是 r.StickyLoad.Info。
//
// 验证方法：构造两个 candidate 相同的场景，分别用
//   (a) 快照注入 Sessions=8, LastActivityMs=now-1s
//   (b) r.StickyLoad 真实 Info() 返回 Sessions=0, LastActivityMs=0
// 两个 score 必须显著不同——证明走快照路径生效。
//
// 注意：setenv 必须在 NewRouter 之前调用，否则 Router.EnvWeights（启动期预解析）
// 会拿到默认值，导致 setenv 不生效。
func TestCalculateLoadScore_StickySnapshot_Preferred(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0.5")

	lim := 4
	c := provider.Candidate{
		CredentialID:    1,
		ProviderID:     1,
		RawModel:       "m",
		StandardizedName: "m",
		Tier:           2,
		Weight:         100,
		ConcurrencyLimit: &lim,
		QuotaState:      "ok",
	}
	r := NewRouter(nil, nil)
	r.StickyLoad = &benchStickyLoad{
		sessions:  map[int]int{1: 0},
		updatedAt: map[int]int64{1: 0},
	}
	ctx := context.Background()
	weights := DefaultLoadScoreWeights()

	// (a) 走快照路径——sticky Session=8/4 容量 = 2.0 → clamp 1.0
	snap := map[int]StickyLoadInfo{
		1: {Sessions: 8, LastActivityMs: time.Now().UnixMilli() - 1000},
	}
	scoreWithSnap := calculateLoadScore(c, r, ctx, weights, StrategyInput{
		LoadScoreWeights: weights,
		StickySnapshot:   snap,
	})

	// (b) 走 Info() 路径——sticky Session=0 → stickyPenalty=0
	snapEmpty := map[int]StickyLoadInfo{
		1: {Sessions: 0, LastActivityMs: 0},
	}
	scoreNoStick := calculateLoadScore(c, r, ctx, weights, StrategyInput{
		LoadScoreWeights: weights,
		StickySnapshot:   snapEmpty,
	})

	if scoreWithSnap <= scoreNoStick {
		t.Fatalf("snapshot with sticky=8 must produce HIGHER score (more penalty) than snap with 0; got snap=%.4f noStick=%.4f", scoreWithSnap, scoreNoStick)
	}

	// 期望 sticky penalty = 0.5 * 1.0 (clamp) = 0.5
	expectedDelta := 0.5
	actualDelta := scoreWithSnap - scoreNoStick
	if actualDelta < expectedDelta*0.9 || actualDelta > expectedDelta*1.1 {
		t.Errorf("delta=%.4f, expected ~%.4f (sticky 0.5 × clamp 1.0)", actualDelta, expectedDelta)
	}
}

// TestCalculateLoadScore_StickySnapshot_RecencyPath 钉 recency 维度：
// 快照里 LastActivityMs 在地平线内 → recencyPenalty > 0。
func TestCalculateLoadScore_StickySnapshot_RecencyPath(t *testing.T) {
	// setenv 必须在 NewRouter 前
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0.5")
	t.Setenv("LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS", "30")

	lim := 4
	c := provider.Candidate{
		CredentialID:    1,
		ProviderID:     1,
		RawModel:       "m",
		StandardizedName: "m",
		Tier:           2,
		Weight:         100,
		ConcurrencyLimit: &lim,
		QuotaState:      "ok",
	}
	r := NewRouter(nil, nil)
	r.StickyLoad = &benchStickyLoad{
		sessions:  map[int]int{1: 0},
		updatedAt: map[int]int64{1: 0},
	}
	ctx := context.Background()
	weights := DefaultLoadScoreWeights()

	// 最近活跃（10s 前）：recencyPenalty ≈ 0.5 * (1 - 10/30) = 0.333
	now := time.Now().UnixMilli()
	scoreRecent := calculateLoadScore(c, r, ctx, weights, StrategyInput{
		LoadScoreWeights: weights,
		StickySnapshot: map[int]StickyLoadInfo{
			1: {Sessions: 0, LastActivityMs: now - 10_000},
		},
	})
	// 无活跃：recencyPenalty = 0
	scoreIdle := calculateLoadScore(c, r, ctx, weights, StrategyInput{
		LoadScoreWeights: weights,
		StickySnapshot: map[int]StickyLoadInfo{
			1: {Sessions: 0, LastActivityMs: 0},
		},
	})

	if scoreRecent <= scoreIdle {
		t.Fatalf("recent activity must produce HIGHER score than idle; recent=%.4f idle=%.4f", scoreRecent, scoreIdle)
	}
}

// TestStickyLoadTracker_Snapshot_Atomicity 钉 StickyLoadTracker.Snapshot 实现：
// 单次锁内完成 n 凭据快照；每个被请求的 id 必须出现在 map 里（即使值为 0）。
// 这保证调用方 map[id] 直接读不会 panic，且语义与 Info() 一致
// （无快照=memory 计数，无 activity=0）。
func TestStickyLoadTracker_Snapshot_Atomicity(t *testing.T) {
	tr := NewStickyLoadTracker()
	t.Cleanup(func() { tr.Close() })

	// session 1 + 2 active
	tr.ObserveSession(1, "sess-A")
	tr.ObserveSession(2, "sess-B")
	// 触发一次 Refresh 让 snapshot 落定
	tr.Refresh([]int{1, 2, 3})

	snap := tr.Snapshot([]int{1, 2, 3, 4})
	// 所有被请求的 id 都必须出现在 map 里（map 读安全）
	for _, id := range []int{1, 2, 3, 4} {
		if _, ok := snap[id]; !ok {
			t.Errorf("credential %d must appear in snapshot map (empty info allowed)", id)
		}
	}
	// 1 和 2 应该反映 active sessions（≥1）
	if snap[1].Sessions < 1 {
		t.Errorf("cred 1 should have sessions, got %d", snap[1].Sessions)
	}
	if snap[2].Sessions < 1 {
		t.Errorf("cred 2 should have sessions, got %d", snap[2].Sessions)
	}
	// 3 没有 sessions 应为 0
	if snap[3].Sessions != 0 {
		t.Errorf("cred 3 has no session; expected 0, got %d", snap[3].Sessions)
	}
	// 4 不在 id 列表（虽然也请求了）——都请求了，必须有
	if snap[4].Sessions != 0 {
		t.Errorf("cred 4 has no session; expected 0, got %d", snap[4].Sessions)
	}
}

// TestStickyLoadTracker_Snapshot_NilSafe 钉 nil receiver / nil id list 安全。
func TestStickyLoadTracker_Snapshot_NilSafe(t *testing.T) {
	var tr *StickyLoadTracker
	got := tr.Snapshot([]int{1, 2})
	if got == nil {
		t.Errorf("nil receiver must return non-nil empty map (avoid caller nil-check)")
	}
	if len(got) != 0 {
		t.Errorf("nil receiver must return empty map, got %d entries", len(got))
	}
	tr2 := NewStickyLoadTracker()
	got = tr2.Snapshot(nil)
	if got == nil {
		t.Errorf("nil id list must return non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("nil id list must return empty map, got %d entries", len(got))
	}
	got = tr2.Snapshot([]int{0, -1})
	if got == nil {
		t.Errorf("negative id list must return non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("non-positive ids must be filtered, got %d entries", len(got))
	}
}
