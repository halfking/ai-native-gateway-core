package autoroute

// cache_revalidate_test.go — 会话意图缓存的**可用性重校验**钉测
// （2026-11-16 auto 模式可用性轮）。
//
// ★ 这条测试守的是 magpie 对照结论里最关键的那一条：
//
//	magpie internal/gateway/affinity.go:599-600
//	  case pl.order[at].Rest != nil:
//	      a.Why = "resting"
//
// —— 「粘性目标正在休息 ⇒ 不用它，会话自然让给下一个」。
//
// 本仓的会话意图缓存此前**只有分类一致性判据**（role/kind/workType/drift），
// 没有可用性判据：缓存命中即无条件复用 (ChosenModel, CredentialID)。
// 于是一个已经死了的模型/凭据会被整个会话一直钉着，直到 TTL 到期
// （211 实测：会话 gw_455af85c-… 连续 55 轮全落在 glm-5.2，最后一轮 203ms
// 直接失败）—— 用户看到的就是「中间断掉，表现为原来的模型不可用」。
//
// 本测试用 CachedChoiceValidator 缝（无 DB 也能跑）钉住三条语义：
//  1. 目标不可路由 ⇒ 缓存作废 + 重新决策（不再复用那个死模型）；
//  2. 目标仍可路由 ⇒ 继续复用（会话一致性不被破坏）；
//  3. 重校验本身不可用 ⇒ 放行（DB 抖动不许变成全站 auto 失败）。

import (
	"context"
	"testing"
	"time"
)

// staleIntentClassifier always classifies as code, so the task type never
// drifts and the cache-hit branch is the only thing under test.
type staleIntentClassifier struct{}

func (staleIntentClassifier) Classify(context.Context, ClassificationSignals) (*Classification, error) {
	return &Classification{Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic"}, nil
}

func (staleIntentClassifier) Name() string { return "stale-intent" }

// TestDecide_CachedChoiceDead_ReDecides pins behaviour #1: a cached choice
// that is no longer routable must be dropped, and the request re-decided
// against the **current** pool.
//
// The pool change is not decoration: the autoroute index drops bindings whose
// credential was suspended / quota-exhausted (index.go:604-615), so a dead
// model leaves the pool while the session cache still points at it. Without
// the revalidation the session keeps asking for a model that is no longer
// there — that is the reported "原来的模型不可用".
func TestDecide_CachedChoiceDead_ReDecides(t *testing.T) {
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "dead-model", CredentialID: 1}},
		{Candidate: Candidate{CanonicalName: "live-model", CredentialID: 2}},
	}}
	d := NewDecider(staleIntentClassifier{}, nil, idx, nil)
	d.intentCache = NewSessionIntentCache(10 * time.Minute)

	// First decision: caches "dead-model" for the session.
	first, err := d.Decide(context.Background(), ClassificationSignals{}, 1, "", "", "sess-1")
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if first.ChosenModel != "dead-model" {
		t.Fatalf("first decision model = %q, want dead-model", first.ChosenModel)
	}

	// The model dies: its binding leaves the routing pool, and the cached
	// choice is no longer routable.
	idx.cands = []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "live-model", CredentialID: 2}},
	}
	d.CachedChoiceValidator = func(context.Context, int64, string) bool { return false }

	// Second decision for the same session must NOT reuse the dead choice.
	second, err := d.Decide(context.Background(), ClassificationSignals{}, 1, "", "", "sess-1")
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if second.CacheReused {
		t.Fatalf("dead cached choice must not be reused (model=%q)", second.ChosenModel)
	}
	if second.ChosenModel != "live-model" {
		t.Fatalf("re-decision must pick a live model, got %q", second.ChosenModel)
	}
}

// TestDecide_CachedChoiceAlive_Reuses pins behaviour #2: a still-routable
// cached choice is reused (session consistency is not sacrificed).
func TestDecide_CachedChoiceAlive_Reuses(t *testing.T) {
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "live-model", CredentialID: 2}},
	}}
	d := NewDecider(staleIntentClassifier{}, nil, idx, nil)
	d.intentCache = NewSessionIntentCache(10 * time.Minute)

	if _, err := d.Decide(context.Background(), ClassificationSignals{}, 1, "", "", "sess-2"); err != nil {
		t.Fatalf("first decide: %v", err)
	}

	validatorCalls := 0
	d.CachedChoiceValidator = func(context.Context, int64, string) bool {
		validatorCalls++
		return true
	}

	second, err := d.Decide(context.Background(), ClassificationSignals{}, 1, "", "", "sess-2")
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if !second.CacheReused {
		t.Fatalf("a live cached choice must be reused (model=%q)", second.ChosenModel)
	}
	if validatorCalls == 0 {
		t.Fatal("the availability revalidation must actually run on a cache hit")
	}
}

// TestDecide_CachedChoiceRevalidationUnavailable_FailsOpen pins behaviour #3:
// when the revalidation itself cannot answer, the cached choice is kept —
// a database hiccup must not turn every auto session into a fresh decision.
func TestDecide_CachedChoiceRevalidationUnavailable_FailsOpen(t *testing.T) {
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "live-model", CredentialID: 2}},
	}}
	d := NewDecider(staleIntentClassifier{}, nil, idx, nil)
	d.intentCache = NewSessionIntentCache(10 * time.Minute)

	if _, err := d.Decide(context.Background(), ClassificationSignals{}, 1, "", "", "sess-3"); err != nil {
		t.Fatalf("first decide: %v", err)
	}

	// No pool and no validator ⇒ the default path fails open.
	second, err := d.Decide(context.Background(), ClassificationSignals{}, 1, "", "", "sess-3")
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if !second.CacheReused {
		t.Fatalf("revalidation must fail open (no pool ⇒ keep the cached choice), model=%q", second.ChosenModel)
	}
}
