package autoroute

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// feedbackWriteTimeout bounds the background feedback write so a slow plugin
// never accumulates goroutines on the hot path.
const feedbackWriteTimeout = 200 * time.Millisecond

// maxConcurrentFeedbackWrites caps the number of in-flight feedback
// goroutines. Each write issues up to ~4 DB statements against the shared
// gateway pool (feedback insert + affinity upsert + annotation lookup), so
// an unbounded spawn-per-decision at high RPS starves request-path queries
// (2026-09-07 audit P1). Excess feedback is dropped — it is best-effort
// training data, never worth displacing user traffic.
const maxConcurrentFeedbackWrites = 32

// feedbackWriteSlots is the counting semaphore for recordFeedbackAsync.
var feedbackWriteSlots = make(chan struct{}, maxConcurrentFeedbackWrites)

// optimizer_bridge.go — P2.2: bridges autoroute candidate types to the
// routingopt plugin DTOs and back. autoroute → routingopt is a one-way
// import (routingopt never imports autoroute), so the conversion lives here.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.2

// toOptimizerCandidates converts scored candidates into the plugin DTO.
// The normalised multi-objective dimensions are derived from the existing
// ScoringBreakdown (0-100 scales) so the plugin needs no cohort math:
//
//	Cost         = 1 - PriceScore/100     (higher price score = cheaper)
//	Latency      = 1 - SpeedScore/100     (higher speed score = faster)
//	Availability = Reliability/100
func toOptimizerCandidates(in []ScoredCandidate) []routingopt.ModelCandidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]routingopt.ModelCandidate, 0, len(in))
	for _, sc := range in {
		out = append(out, routingopt.ModelCandidate{
			CanonicalName: sc.Candidate.CanonicalName,
			CredentialID:  int(sc.Candidate.CredentialID),
			RawModel:      sc.Candidate.RawModel,
			Score:         sc.Breakdown.Composite,
			Cost:          clamp01(1 - sc.Breakdown.PriceScore/100),
			Latency:       clamp01(1 - sc.Breakdown.SpeedScore/100),
			Availability:  clamp01(sc.Breakdown.Reliability / 100),
		})
	}
	return out
}

// applyOptimizerRanking maps the plugin's re-ranked DTO list back onto the
// original scored candidates (by identity triple), preserving Breakdown and
// all Candidate fields. Safety rules:
//
//   - nil/empty reranked, or a length mismatch → original unchanged
//   - DTO entries with no matching original are skipped
//   - an empty result (plugin dropped everything) → original unchanged,
//     so Decide never surfaces "no candidates" because of the plugin
func applyOptimizerRanking(original []ScoredCandidate, reranked []routingopt.ModelCandidate) []ScoredCandidate {
	if len(original) == 0 || len(reranked) == 0 || len(reranked) > len(original) {
		return original
	}
	byKey := make(map[candidateKey]int, len(original))
	for i, sc := range original {
		byKey[candidateKeyOf(sc)] = i
	}
	out := make([]ScoredCandidate, 0, len(reranked))
	seen := make(map[int]struct{}, len(reranked))
	for _, mc := range reranked {
		idx, ok := byKey[candidateKey{canonical: mc.CanonicalName, credID: mc.CredentialID, raw: mc.RawModel}]
		if !ok {
			continue
		}
		if _, dup := seen[idx]; dup {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, original[idx])
	}
	if len(out) == 0 {
		return original
	}
	return out
}

type candidateKey struct {
	canonical string
	credID    int
	raw       string
}

func candidateKeyOf(sc ScoredCandidate) candidateKey {
	return candidateKey{
		canonical: sc.Candidate.CanonicalName,
		credID:    int(sc.Candidate.CredentialID),
		raw:       sc.Candidate.RawModel,
	}
}

// recommendWithOptimizer runs the plugin's RecommendModel hook when wired.
// Errors are logged and the original order is kept — the plugin must never
// break routing. Called from Decide between index.Recommend and the
// explicit-default/override strategies so admin pins keep precedence.
//
// P2.5: cls/sigs also build the MLRouteFeatures for the ONNX re-ranker;
// the feature conversion only runs when an optimizer is wired, so the
// no-plugin hot path stays identical to the pre-P2.2 baseline.
func (d *Decider) recommendWithOptimizer(ctx context.Context, recommended []ScoredCandidate, cls *Classification, sigs ClassificationSignals, profile Profile, apiKeyID int, sessionID, clientType string) []ScoredCandidate {
	if d.optimizer == nil || len(recommended) < 2 {
		return recommended
	}
	// A/B testing (P2.5): control-group requests skip the optimizer entirely
	// and get byte-identical baseline rule-engine routing. The optional
	// interface keeps the Decider decoupled from the gate implementation.
	if ab, ok := d.optimizer.(interface {
		EvaluateAB(sessionID string, apiKeyID int) bool
	}); ok && !ab.EvaluateAB(sessionID, apiKeyID) {
		return recommended
	}
	task := cls.Primary
	routingCtx := routingopt.RoutingContext{
		TaskType:  string(task),
		Profile:   string(profile),
		UserID:    apiKeyID,
		SessionID: sessionID,
		Features:  toMLRouteFeatures(cls, sigs, string(profile)),
	}
	rerankedAny, err := d.optimizer.RecommendModel(ctx, toOptimizerCandidates(recommended), routingCtx)
	if err != nil {
		slog.WarnContext(ctx, "optimizer.RecommendModel failed, keeping index order",
			"err", err, "task_type", task, "profile", profile)
		return recommended
	}
	reranked, ok := rerankedAny.([]routingopt.ModelCandidate)
	if !ok {
		return recommended
	}
	return applyOptimizerRanking(recommended, reranked)
}

// toMLRouteFeatures converts the classification result and structured
// features (schema v1) into the ONNX input contract consumed by
// routingopt.MLSelector. Pure conversion — no I/O, no content access beyond
// the non-reversible StructuredFeatures extraction.
func toMLRouteFeatures(cls *Classification, sigs ClassificationSignals, profile string) *routingopt.MLRouteFeatures {
	sf := ExtractStructuredFeatures(sigs, profile)
	return &routingopt.MLRouteFeatures{
		TaskType:               string(cls.Primary),
		Profile:                profile,
		Classifier:             cls.Classifier,
		Confidence:             cls.Confidence,
		DetectedLanguage:       sf.DetectedLanguage,
		PromptLengthBucket:     sf.PromptLengthBucket,
		ContextLengthBucket:    sf.ContextLengthBucket,
		TurnCountBucket:        sf.TurnCountBucket,
		HasCodeIndicator:       sf.HasCodeIndicator,
		HasMathIndicator:       sf.HasMathIndicator,
		HasTableIndicator:      sf.HasTableIndicator,
		HasMultimediaIndicator: sf.HasMultimediaIndicator,
		IntentCategory:         sf.IntentCategory,
		DomainHint:             sf.DomainHint,
		ComplexityBucket:       sf.ComplexityBucket,
		LatencySensitive:       sf.LatencySensitive,
		CostSensitive:          sf.CostSensitive,
	}
}

// recordFeedbackAsync fires the plugin's RecordFeedback hook in the
// background with a short timeout. Fresh decisions only — session-cache
// hits reuse an earlier decision and must not double-count feedback.
//
// 2026-09-08 audit (Track A) — real-outcome backfill: when the relay layer
// injected the real X-Request-Id into the Decide context (maybeResolveAuto →
// WithRequestID), the feedback is STASHED in the outcome registry
// (outcome_feedback.go) instead of being written here; the request-completion
// path calls ReportRoutingOutcome with the terminal success/latency/cost and
// the stashed row is written with the REAL outcome. Requests without a
// correlation id keep the legacy behaviour: an immediate decision-time write
// with the placeholder IsSuccess=true (the old open-loop semantics).
func (d *Decider) recordFeedbackAsync(ctx context.Context, decision *Decision, apiKeyID int, sessionID, clientType string) {
	if d.optimizer == nil || decision == nil {
		return
	}
	// R37 (R35-R2 灰度口径): synthetic rounds (goal-% shadow rounds, internal
	// title/summary loopbacks) are real auto decisions but never become
	// training/observation signal. Gating here covers BOTH paths below — the
	// stash (which would otherwise surface as expired/matched) and the legacy
	// no-correlation-id placeholder write.
	if IsSyntheticActor(originActorFromContext(ctx)) {
		return
	}
	if requestID := requestIDFromContext(ctx); requestID != "" {
		stashPendingFeedback(requestID, d.optimizer, newRoutingFeedback(requestID, decision, apiKeyID, sessionID))
		return
	}
	// No correlation id (internal callers, tests): the outcome can never be
	// matched, so write the legacy placeholder immediately.
	dispatchFeedback(d.optimizer, newRoutingFeedback(feedbackRequestID(apiKeyID, decision.DecidedAt), decision, apiKeyID, sessionID))
}

// newRoutingFeedback builds the feedback DTO for one fresh decision.
// IsSuccess starts as the decision-time placeholder (true); it is only
// authoritative for the no-correlation-id legacy path — stashed entries are
// overwritten by ReportRoutingOutcome before the row is written.
func newRoutingFeedback(requestID string, decision *Decision, apiKeyID int, sessionID string) *routingopt.RoutingFeedback {
	return &routingopt.RoutingFeedback{
		RequestID:         requestID,
		TaskType:          string(decision.TaskType),
		PredictedProvider: decision.ChosenModel,
		ActualProvider:    decision.ChosenModel,
		// Classifier confidence at decision time (Classification.Confidence
		// via Decision.Confidence) — replaces the integrator's hard-coded 0.8.
		Confidence: decision.Confidence,
		IsSuccess:  true, // placeholder; ReportRoutingOutcome fills the real outcome
		UserID:     apiKeyID,
		SessionID:  sessionID,
	}
}

// feedbackRequestID derives a stable-enough identifier for feedback rows when
// the relay-layer request id was not plumbed into Decide. Correlatable
// requests use the real X-Request-Id (see recordFeedbackAsync) so human
// annotations keyed by request_id now match, and the outcome registry can
// backfill the real result.
func feedbackRequestID(apiKeyID int, decidedAt time.Time) string {
	return fmt.Sprintf("auto-%d-%d", apiKeyID, decidedAt.UnixNano())
}
