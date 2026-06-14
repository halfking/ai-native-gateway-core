package autoroute

// Candidate is the per-credential × per-model snapshot consumed by Score().
//
// Populated from credential_model_index (5min rolled-up) plus the
// canonical model's static attributes (tags, context_window).
//
// All fields are 0 when unknown — Score() handles zero values safely
// (zero pressure ratio → max score; zero success_rate → middle score
// to avoid penalising unknown credentials unfairly).
type Candidate struct {
	// Identity
	CredentialID  int64
	CanonicalID   int
	CanonicalName string
	RawModel      string

	// Static (from models_canonical + provider_offers)
	BillingMode        string
	UnitPriceInPer1M   float64
	UnitPriceOutPer1M  float64
	ContextWindow      int
	Tags               []string // e.g. ["reasoning", "code", "agent"]

	// Dynamic (from credential_model_index last 5min bucket)
	SuccessRate      float64 // 0.0 - 1.0
	P95LatencyMs     int
	ActiveSessions   int
	ConcurrencyLimit int

	// Computed inputs (filled by Index.Recommend before calling Score)
	// PressureRatio is candidate-level: ActiveSessions / ConcurrencyLimit
	// (0 when ConcurrencyLimit is 0 — i.e. unknown → no pressure penalty).
	PressureRatio float64

	// TaskMatchScore is 0.0-1.0 reflecting how well the candidate's
	// Tags intersect with the task type's required tags. Pre-computed
	// by Index.Recommend to avoid recomputing per profile.
	TaskMatchScore float64
}

// ScoringBreakdown is the per-dimension score output (each 0-100)
// plus the weighted composite. Surfaced in X-Gw-Auto-Decision header
// for observability and stored in request_logs.auto_decision JSONB.
type ScoringBreakdown struct {
	PriceScore     float64 `json:"price_score"`
	SpeedScore     float64 `json:"speed_score"`
	StabilityScore float64 `json:"stability_score"`
	MatchScore     float64 `json:"match_score"`
	PressureScore  float64 `json:"pressure_score"`
	ContextFit     float64 `json:"context_fit"`
	Composite      float64 `json:"composite"`
}

// ScoredCandidate pairs a candidate with its breakdown — returned by
// the decider for the top-N list stored in auto_decision.
type ScoredCandidate struct {
	Candidate Candidate       `json:"candidate"`
	Breakdown ScoringBreakdown `json:"breakdown"`
}

// CostContext provides the cohort-level reference values used for
// normalisation. Without these, the per-candidate price/speed scores
// would be raw values (CNY/token or milliseconds) instead of 0-100.
//
// Populated by Index.Recommend over the candidate set BEFORE calling
// Score(), so candidates are scored against the same baseline.
type CostContext struct {
	// PriceP75 is the 75th-percentile blended cost across all candidates
	// for this task type. Used as the denominator for price normalisation.
	// Candidates cheaper than P75 score higher (closer to 100).
	PriceP75 float64

	// SpeedP95 is the slowest P95 latency across the cohort (i.e. the
	// "ceiling" of slowness). Faster candidates score higher.
	SpeedP95 float64

	// HasMixedPrices is true when the cohort contains both USD and CNY
	// candidates. When true, PriceP75 is computed in USD after FX conversion.
	HasMixedPrices bool
}

// Score computes the 6-dimension composite score for a candidate.
//
// All per-dimension scores are 0-100 (higher = better):
//
//   PriceScore     : inverse of normalised price (cheaper → higher)
//   SpeedScore     : inverse of normalised latency (faster → higher)
//   StabilityScore : direct success_rate × 100
//   MatchScore     : passed-through from candidate.TaskMatchScore × 100
//   PressureScore  : (1 - pressure_ratio) × 100 (less load → higher)
//   ContextFit     : context_window / max(estimated_tokens, 4096), capped 1.0
//
// Composite is the weighted sum (weights × score / Sum(weights)).
//
// Parameters:
//
//   c              : the candidate to score
//   sigs           : the request classification signals (for context fit)
//   profile        : determines which weights to apply
//   costCtx        : cohort-level baselines for normalisation
//
// Returns the ScoringBreakdown (all 0-100 fields plus Composite).
func Score(c Candidate, sigs ClassificationSignals, profile Profile, costCtx CostContext) ScoringBreakdown {
	w := WeightsFor(profile)
	wsum := w.Sum()
	if wsum <= 0 {
		wsum = 100
	}

	price := scorePrice(c, costCtx)
	speed := scoreSpeed(c, costCtx)
	stability := scoreStability(c)
	match := scoreMatch(c) * 100
	pressure := scorePressure(c)
	ctx := scoreContextFit(c, sigs.EstimatedTokens)

	composite := (price*w.Price +
		speed*w.Speed +
		stability*w.Stability +
		match*w.Match +
		pressure*w.Pressure +
		ctx*w.ContextFit) / wsum

	return ScoringBreakdown{
		PriceScore:     price,
		SpeedScore:     speed,
		StabilityScore: stability,
		MatchScore:     match,
		PressureScore:  pressure,
		ContextFit:     ctx,
		Composite:      composite,
	}
}

// scorePrice normalises cost against the cohort P75.
//
// Formula:
//
//   blended = unit_price_in + unit_price_out  (assumes 1:1 input/output)
//   if blended == 0                  → 100 (free)
//   if costCtx.PriceP75 == 0         → 100 (no cohort baseline)
//   ratio = blended / PriceP75       (1.0 means at P75, lower better)
//   score = max(0, min(100, 100 * (1.5 - ratio)))
//
//   ratio 0.5 (half of P75) → 100
//   ratio 1.0 (at P75)     → 50
//   ratio 1.5 (50% more)   → 0
func scorePrice(c Candidate, costCtx CostContext) float64 {
	blended := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
	if blended <= 0 {
		return 100 // free or unknown
	}
	if costCtx.PriceP75 <= 0 {
		return 80 // no baseline → assume middle
	}
	ratio := blended / costCtx.PriceP75
	score := 100 * (1.5 - ratio)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

// scoreSpeed normalises latency against the cohort worst P95.
//
// Formula:
//
//   if P95LatencyMs <= 0          → 80 (unknown → middle)
//   if costCtx.SpeedP95 <= 0      → 80
//   ratio = P95LatencyMs / SpeedP95
//   score = 100 * (1 - ratio)      (faster → higher)
//
// Capped [0, 100].
func scoreSpeed(c Candidate, costCtx CostContext) float64 {
	if c.P95LatencyMs <= 0 {
		return 80
	}
	if costCtx.SpeedP95 <= 0 {
		return 80
	}
	ratio := float64(c.P95LatencyMs) / float64(costCtx.SpeedP95)
	score := 100 * (1 - ratio)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

// scoreStability maps success_rate (0-1) to a 0-100 score.
//
// Floor at 30 to avoid penalising candidates with zero history
// (cold start). Cap at 100.
func scoreStability(c Candidate) float64 {
	r := c.SuccessRate
	if r <= 0 {
		return 50 // unknown → middle (not penalised)
	}
	if r > 1 {
		r = 1
	}
	score := r * 100
	if score < 30 {
		score = 30
	}
	return score
}

// scoreMatch returns the precomputed TaskMatchScore (0-1).
//
// TaskMatchScore is computed in Index.Recommend from tag intersection
// (see scoring.go TaskMatch); here we just clamp.
func scoreMatch(c Candidate) float64 {
	m := c.TaskMatchScore
	if m < 0 {
		m = 0
	}
	if m > 1 {
		m = 1
	}
	return m
}

// scorePressure computes (1 - pressure_ratio) × 100.
//
// If PressureRatio is unknown (0), returns 100 (no penalty).
// Capped [0, 100].
func scorePressure(c Candidate) float64 {
	if c.PressureRatio <= 0 {
		return 100 // unknown → no pressure
	}
	if c.PressureRatio > 1 {
		c.PressureRatio = 1
	}
	return (1 - c.PressureRatio) * 100
}

// scoreContextFit computes how well the candidate's context window
// fits the request's estimated token count.
//
// Formula:
//
//   fit = context_window / max(estimated_tokens, 4096)
//   score = min(100, fit × 100)
//
//   fit >= 1.0 (context fits comfortably) → 100
//   fit 0.5 (context half the requirement)  → 50
//   fit 0.0 (context window unknown)       → 50 (middle)
func scoreContextFit(c Candidate, estTokens int) float64 {
	if c.ContextWindow <= 0 {
		return 50
	}
	denom := estTokens
	if denom < 4096 {
		denom = 4096
	}
	fit := float64(c.ContextWindow) / float64(denom)
	score := fit * 100
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score
}

// TaskMatchScore computes the 0-1 match score between a candidate's tags
// and a task type's required tags.
//
// Task → required tags mapping (curated based on common model
// capabilities):
//
//   reasoning    : ["reasoning", "math", "logic"]
//   code         : ["code", "programming"]
//   agent        : ["agent", "tool_use", "function_call"]
//   creative     : ["creative", "writing"]
//   long_context : ["long_context", "128k", "200k", "512k", "1m"]
//   vision       : ["vision", "multimodal"]
//   function_call: ["function_call", "tool_use"]
//   chat         : []   (no specific tags required)
//
// Match formula:
//
//   hits = |required ∩ candidate.tags|  (case-insensitive substring match)
//   score = hits / |required|           (1.0 when all required tags hit)
//
// Tag matching is substring-based so "code" matches "code_completion"
// and "code-review". This is loose but resilient to taxonomy drift.
func TaskMatchScore(task TaskType, candidateTags []string) float64 {
	required := requiredTagsForTask(task)
	if len(required) == 0 {
		return 0.5 // chat or unknown → middle (don't gate)
	}
	hits := 0
	for _, r := range required {
		for _, c := range candidateTags {
			if containsFold(c, r) {
				hits++
				break
			}
		}
	}
	return float64(hits) / float64(len(required))
}

// requiredTagsForTask returns the tag set that qualifies a model as
// suitable for the given task type.
func requiredTagsForTask(task TaskType) []string {
	switch task {
	case TaskReasoning:
		return []string{"reasoning", "math", "logic"}
	case TaskCode:
		return []string{"code", "programming"}
	case TaskAgent:
		return []string{"agent", "tool_use", "function_call"}
	case TaskCreative:
		return []string{"creative", "writing"}
	case TaskLongContext:
		return []string{"long_context", "128k", "200k", "512k", "1m"}
	case TaskVision:
		return []string{"vision", "multimodal"}
	case TaskFunctionCall:
		return []string{"function_call", "tool_use"}
	case TaskChat:
		return nil // no specific tags required
	default:
		return nil
	}
}

// containsFold reports whether substr is a case-insensitive substring of s.
// Empty substr never matches.
func containsFold(s, substr string) bool {
	if substr == "" {
		return false
	}
	ls := lowerASCII(s)
	lb := lowerASCII(substr)
	return indexOf(ls, lb) >= 0
}

// lowerASCII is a hand-rolled ASCII-only lowercase (avoids strings.ToLower
// allocation in this hot path).
func lowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

// indexOf is a hand-rolled strings.Index (avoids the import on the hot path).
func indexOf(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}