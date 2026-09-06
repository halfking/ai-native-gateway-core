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
func (d *Decider) recommendWithOptimizer(ctx context.Context, recommended []ScoredCandidate, task TaskType, profile Profile, apiKeyID int, sessionID, clientType string) []ScoredCandidate {
	if d.optimizer == nil || len(recommended) < 2 {
		return recommended
	}
	routingCtx := routingopt.RoutingContext{
		TaskType:  string(task),
		Profile:   string(profile),
		UserID:    apiKeyID,
		SessionID: sessionID,
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

// recordFeedbackAsync fires the plugin's RecordFeedback hook in the
// background with a short timeout. Fresh decisions only — session-cache
// hits reuse an earlier decision and must not double-count feedback.
func (d *Decider) recordFeedbackAsync(decision *Decision, apiKeyID int, sessionID, clientType string) {
	if d.optimizer == nil || decision == nil {
		return
	}
	fb := routingopt.RoutingFeedback{
		RequestID:         feedbackRequestID(apiKeyID, decision.DecidedAt),
		TaskType:          string(decision.TaskType),
		PredictedProvider: decision.ChosenModel,
		ActualProvider:    decision.ChosenModel,
		IsSuccess:         true, // decision produced; upstream outcome backfill is Week 2
		UserID:            apiKeyID,
		SessionID:         sessionID,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), feedbackWriteTimeout)
		defer cancel()
		if err := d.optimizer.RecordFeedback(ctx, fb); err != nil {
			slog.WarnContext(ctx, "optimizer.RecordFeedback failed", "err", err)
		}
	}()
}

// feedbackRequestID derives a stable-enough identifier for feedback rows when
// the relay-layer request id is not plumbed into Decide (Week 2: take the
// real request id from context).
func feedbackRequestID(apiKeyID int, decidedAt time.Time) string {
	return fmt.Sprintf("auto-%d-%d", apiKeyID, decidedAt.UnixNano())
}
