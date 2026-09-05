package autoroute

// affinity.go — learned task→model affinity: pure computation.
//
// Ref: docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md
//
// This file holds only pure functions (no I/O) so the maths is unit-testable
// without a database. The DB-backed snapshot lives in affinity_store.go and
// the rollup that produces the numbers lives in bg/auto_route_affinity_worker.go.
//
// What problem this solves
//
//	Selection currently scores candidates on intent match, price, channel
//	quality and reliability — all of which are properties of the *channel*,
//	not of how well a given model actually performs the task it was picked
//	for. Nothing observes outcomes and feeds them back, so a model that
//	reliably does badly on `code` keeps getting picked for `code`.
//
//	Affinity is that missing term: an observed, per-(task_type, profile, model)
//	score in [0,100] with 50 meaning "no opinion".
//
// Why the maths is deliberately conservative
//
//	Naively ranking by observed mean reward is badly behaved on small samples:
//	one lucky success would put a model at rank 1 with n=1. Three guards:
//
//	  1. Bayesian shrinkage toward the neutral prior. Weight w = n/(n+K) with
//	     K=30, so affinity only approaches the observed mean once the sample
//	     is large relative to K. At n=1, w≈0.03 — effectively no opinion.
//	  2. A hard minimum sample count below which affinity is exactly neutral.
//	  3. A clamp on how far affinity may travel from neutral, so this single
//	     dimension can never dominate the composite score.
//
//	Staleness decay handles the opposite failure: a model measured well six
//	months ago should not coast on that forever, especially after a provider
//	silently changes what sits behind a model name.

import (
	"context"
	"hash/fnv"
	"math"
	"time"
)

// requestIDCtxKey carries the request id through DecideV2 → RecommendV2 so the
// affinity explore bucket can hash on it without threading a new parameter
// through six function signatures (Decide/DecideV2/DecideWithFeatureFlags and
// their callers). Set by maybeResolveAuto via WithRequestID.
type requestIDCtxKey struct{}
type workTypeCtxKey struct{}

// WithRequestID returns a context carrying the request id for affinity sampling.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDCtxKey{}, requestID)
}

// requestIDFromContext reads the request id set by WithRequestID, or "".
func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// WithWorkType returns a context carrying a validated concrete work type key.
func WithWorkType(ctx context.Context, workType string) context.Context {
	if workType == "" {
		return ctx
	}
	return context.WithValue(ctx, workTypeCtxKey{}, workType)
}

func workTypeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(workTypeCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// AffinityMode controls how much influence the learned affinity has.
type AffinityMode string

const (
	// AffinityOff disables affinity entirely: no scoring contribution and
	// nothing recorded on the selection row.
	AffinityOff AffinityMode = "off"

	// AffinityShadow computes and records affinity but does NOT let it change
	// the selection. This is the default: it lets a deployment accumulate and
	// validate the data before any routing behaviour shifts.
	AffinityShadow AffinityMode = "shadow"

	// AffinityOn applies affinity as a scoring dimension.
	AffinityOn AffinityMode = "on"
)

// ParseAffinityMode maps a config string to a mode, defaulting to shadow.
// Unknown values deliberately fall back to shadow rather than on: a typo in
// configuration must never silently start changing production routing.
func ParseAffinityMode(s string) AffinityMode {
	switch s {
	case string(AffinityOff):
		return AffinityOff
	case string(AffinityOn):
		return AffinityOn
	case string(AffinityShadow):
		return AffinityShadow
	default:
		return AffinityShadow
	}
}

// Affinity tuning constants. These are the knobs a reviewer is most likely to
// want to reason about, so they are named rather than inlined.
const (
	// AffinityNeutral is the "no opinion" score. Shrinkage pulls toward it and
	// unknown models sit exactly on it.
	AffinityNeutral = 50.0

	// AffinityShrinkageK is the pseudo-count in w = n/(n+K). At n=K the
	// observed mean carries half the weight. 30 is chosen so a model needs
	// roughly a few dozen real requests on a task before it can move much.
	AffinityShrinkageK = 30.0

	// AffinityMinSamples is the hard floor: below this, affinity is exactly
	// neutral regardless of how good the observed reward looks.
	AffinityMinSamples = 10

	// AffinityMaxDeviation caps |affinity - neutral| at read time, keeping the
	// dimension bounded to [10, 90] so it can inform but not dictate.
	AffinityMaxDeviation = 40.0

	// AffinityEMAAlpha weights the newest window in the exponential moving
	// average. 0.15 ≈ a 12-window memory: responsive to real regressions
	// without thrashing on noise.
	AffinityEMAAlpha = 0.15

	// AffinityStaleAfter is how long a row may go unsampled before decay starts.
	AffinityStaleAfter = 7 * 24 * time.Hour

	// AffinityStaleDecayPerDay is the fraction of the remaining deviation from
	// neutral shed per stale day (5%/day).
	AffinityStaleDecayPerDay = 0.05

	// AffinityDefaultExploreRatio is the share of traffic that deliberately
	// ignores affinity so alternatives keep being measured. Without this the
	// ranking self-reinforces: the current leader gets all the traffic, so it
	// is the only model with fresh data, so it stays leader on stale evidence.
	AffinityDefaultExploreRatio = 0.10
)

// AffinityRecord is one learned (task_type, profile, model) row.
type AffinityRecord struct {
	TaskType       TaskType
	Profile        Profile
	CanonicalID    int64
	CanonicalModel string
	TenantID       string

	SampleCount  int
	SuccessCount int
	SuccessRate  float64
	AvgReward    float64
	EMAReward    float64
	AvgLatencyMs int
	AvgCostUSD   float64
	AvgHealth    float64

	// Affinity is the shrunk, stored score in [0,100]. Use EffectiveAffinity
	// for the value that should actually feed scoring.
	Affinity   float64
	Rank       int
	Confidence float64

	LastSampledAt time.Time
	UpdatedAt     time.Time
}

// EffectiveAffinity returns the affinity value safe to feed into scoring at
// time `now`, applying (in order): the minimum-sample floor, staleness decay,
// and the deviation clamp.
//
// Order matters. Decay must apply before the clamp, otherwise a stale extreme
// value would be clamped to the boundary and then decay from there, letting
// stale data hold the boundary longer than intended.
func (r AffinityRecord) EffectiveAffinity(now time.Time) float64 {
	if r.SampleCount < AffinityMinSamples {
		return AffinityNeutral
	}
	a := DecayAffinity(r.Affinity, r.LastSampledAt, now)
	return ClampAffinity(a)
}

// ClampAffinity bounds affinity to neutral ± AffinityMaxDeviation.
func ClampAffinity(a float64) float64 {
	if math.IsNaN(a) || math.IsInf(a, 0) {
		return AffinityNeutral
	}
	lo := AffinityNeutral - AffinityMaxDeviation
	hi := AffinityNeutral + AffinityMaxDeviation
	if a < lo {
		return lo
	}
	if a > hi {
		return hi
	}
	return a
}

// ShrinkAffinity converts an observed mean reward in [0,1] plus a sample count
// into a 0-100 affinity, pulled toward neutral in proportion to how little
// evidence supports it.
//
//	w        = n / (n + K)
//	shrunk   = w*reward + (1-w)*0.5
//	affinity = shrunk * 100
//
// n=0 yields exactly neutral; n→∞ yields reward*100.
func ShrinkAffinity(avgReward float64, sampleCount int) float64 {
	if sampleCount <= 0 {
		return AffinityNeutral
	}
	if math.IsNaN(avgReward) || math.IsInf(avgReward, 0) {
		return AffinityNeutral
	}
	r := clamp01(avgReward)
	n := float64(sampleCount)
	w := n / (n + AffinityShrinkageK)
	shrunk := w*r + (1.0-w)*0.5
	return shrunk * 100.0
}

// AffinityConfidence reports sample adequacy w = n/(n+K) in [0,1]. This is
// stored so an operator can tell "high score, trustworthy" from "high score,
// barely measured" without recomputing.
func AffinityConfidence(sampleCount int) float64 {
	if sampleCount <= 0 {
		return 0
	}
	n := float64(sampleCount)
	return n / (n + AffinityShrinkageK)
}

// UpdateEMA folds a new window mean into the running EMA. A zero-valued
// previous EMA is treated as "no history" and seeds from the new value, so the
// first window is not dragged halfway to zero.
func UpdateEMA(prevEMA, newValue float64) float64 {
	if math.IsNaN(newValue) || math.IsInf(newValue, 0) {
		return prevEMA
	}
	nv := clamp01(newValue)
	if prevEMA <= 0 {
		return nv
	}
	return AffinityEMAAlpha*nv + (1.0-AffinityEMAAlpha)*clamp01(prevEMA)
}

// DecayAffinity pulls a stale score back toward neutral. Rows sampled within
// AffinityStaleAfter are untouched; beyond that each further day sheds
// AffinityStaleDecayPerDay of the remaining deviation.
//
// This is the safety net for a stopped worker: if the loop dies, affinity
// converges to "no opinion" rather than pinning routing to a frozen verdict.
func DecayAffinity(affinity float64, lastSampledAt, now time.Time) float64 {
	if lastSampledAt.IsZero() {
		return affinity
	}
	idle := now.Sub(lastSampledAt)
	if idle <= AffinityStaleAfter {
		return affinity
	}
	staleDays := (idle - AffinityStaleAfter).Hours() / 24.0
	if staleDays <= 0 {
		return affinity
	}
	retained := math.Pow(1.0-AffinityStaleDecayPerDay, staleDays)
	if retained < 0 {
		retained = 0
	}
	return AffinityNeutral + (affinity-AffinityNeutral)*retained
}

// ShouldExplore decides whether a request bypasses affinity so alternatives
// keep accruing evidence.
//
// The decision is a deterministic hash of requestID rather than a PRNG call:
// the same request replayed or re-derived lands in the same bucket, which
// keeps `explore` on the recorded row honest and makes tests reproducible.
func ShouldExplore(requestID string, ratio float64) bool {
	if ratio <= 0 || requestID == "" {
		return false
	}
	if ratio >= 1 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(requestID))
	// Distinct salt from AssignStrategy's hashing so explore selection is not
	// correlated with A/B strategy assignment.
	bucket := (h.Sum32() ^ 0x9e3779b9) % 10000
	return float64(bucket) < ratio*10000.0
}

// RoutingRewardWeights weights the routing-attributed reward.
//
// Deliberately NOT the same thing as session health_score. That score includes
// dimensions a model cannot be held responsible for — `abandoned` (a one-shot
// API call is normal, not a failure), prompt injection, PII and toxicity (all
// driven by user input), and compliance. Charging those to whichever model
// happened to serve the session would bias the ranking by traffic mix rather
// than by model quality.
type RoutingRewardWeights struct {
	Success float64 // 0.45 — correctness dominates
	Latency float64 // 0.20
	Cost    float64 // 0.15
	Health  float64 // 0.10 — routing-only subset of session health
	Retry   float64 // 0.10 — churn within the session
}

// DefaultRoutingRewardWeights returns the standard weighting (sums to 1.0).
func DefaultRoutingRewardWeights() RoutingRewardWeights {
	return RoutingRewardWeights{
		Success: 0.45,
		Latency: 0.20,
		Cost:    0.15,
		Health:  0.10,
		Retry:   0.10,
	}
}

// RewardInput is the per-selection outcome used to compute reward.
type RewardInput struct {
	// Success: 1.0 full success, 0.0 failure, 0.5 partial (e.g. stream cut off).
	Success float64

	LatencyMs int
	// P95BaselineMs is the task-type cohort p95 (across models), NOT this
	// model's own p95 — comparing a model against itself always scores ~neutral
	// and cannot distinguish fast from slow models.
	P95BaselineMs int

	CostUSD float64
	// P75BaselineCost is likewise the task-type cohort p75.
	P75BaselineCost float64

	// HealthComponent is the routing-only session health in [0,1], or -1 when
	// the session has not settled yet (treated as neutral 0.5).
	HealthComponent float64

	// RetryRatio in [0,1]: share of this session's requests that were retries
	// against this same model.
	RetryRatio float64
}

// ComputeRoutingReward produces the reward in [0,1] used to rank models.
//
// Unknown inputs resolve to neutral 0.5 rather than 0, so "not measured" is
// never mistaken for "measured as bad".
func ComputeRoutingReward(in RewardInput) float64 {
	return ComputeRoutingRewardWithWeights(in, DefaultRoutingRewardWeights())
}

// ComputeRoutingRewardWithWeights is ComputeRoutingReward with explicit weights.
func ComputeRoutingRewardWithWeights(in RewardInput, w RoutingRewardWeights) float64 {
	success := clamp01(in.Success)

	latencyScore := 0.5
	if in.P95BaselineMs > 0 && in.LatencyMs > 0 {
		latencyScore = 1.0 - clamp01(float64(in.LatencyMs)/float64(in.P95BaselineMs))
	}

	costScore := 0.5
	if in.P75BaselineCost > 0 && in.CostUSD > 0 {
		costScore = 1.0 - clamp01(in.CostUSD/in.P75BaselineCost)
	}

	health := 0.5
	if in.HealthComponent >= 0 {
		health = clamp01(in.HealthComponent)
	}

	retryScore := 1.0 - clamp01(in.RetryRatio)

	total := w.Success + w.Latency + w.Cost + w.Health + w.Retry
	if total <= 0 {
		return 0.5
	}

	sum := w.Success*success +
		w.Latency*latencyScore +
		w.Cost*costScore +
		w.Health*health +
		w.Retry*retryScore

	return clamp01(sum / total)
}

// SessionAttributionThreshold is the share of a session's requests a model must
// have served before session-level health is attributed to it.
//
// Sessions routinely span several models. Without this gate, a model that
// handled one call in a twenty-call session would inherit the full session
// verdict — including the consequences of other models' failures.
const SessionAttributionThreshold = 0.80

// ShouldAttributeSession reports whether session-level health may be charged to
// a model that served modelRequests of totalRequests.
func ShouldAttributeSession(modelRequests, totalRequests int) bool {
	if totalRequests <= 0 || modelRequests <= 0 {
		return false
	}
	return float64(modelRequests)/float64(totalRequests) >= SessionAttributionThreshold
}
