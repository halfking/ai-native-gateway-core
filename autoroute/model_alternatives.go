package autoroute

import (
	"context"
	"strings"
)

// ModelAlternativeRequest carries the already-classified request intent into a
// failure-time recommendation. Dispatch must not reclassify or use a hardcoded
// model sequence after the original model is exhausted.
type ModelAlternativeRequest struct {
	Task        TaskType
	Signals     ClassificationSignals
	Profile     Profile
	SessionID   string
	WorkType    string
	TriedModels []string
}

// RecommendModelAlternatives returns currently recommended canonical models,
// excluding models already exhausted by the dispatch request.
func (d *Decider) RecommendModelAlternatives(ctx context.Context, req ModelAlternativeRequest) ([]string, error) {
	if d == nil {
		return nil, ErrNoCandidates
	}
	idx, ok := d.index.(*Index)
	if !ok || idx == nil {
		return nil, ErrNoCandidates
	}
	if req.Task == "" {
		req.Task = TaskChat
	}
	if req.Profile == "" {
		req.Profile = ProfileSmart
	}

	topN := len(idx.Snapshot())
	if topN < 1 {
		topN = 1
	}
	recommended := idx.RecommendV2(ctx, req.Task, req.Signals, req.Profile, req.SessionID, topN)
	task, profile := string(req.Task), string(req.Profile)
	if d.overrideStore != nil {
		recommended = d.overrideStore.FilterBanned(recommended, task, profile)
	}
	if d.workTypeRouteStore != nil {
		pins := []string(nil)
		if d.overrideStore != nil {
			pins = d.overrideStore.GetPins(task, profile)
		}
		recommended = d.workTypeRouteStore.ApplyTierPolicyWithWorkType(recommended, task, req.WorkType, pins)
	}
	if d.overrideStore != nil {
		recommended = d.overrideStore.PromotePins(recommended, task, profile)
	}
	if len(recommended) == 0 {
		return nil, ErrNoCandidates
	}

	excluded := make(map[string]struct{}, len(req.TriedModels))
	for _, model := range req.TriedModels {
		if model = strings.TrimSpace(model); model != "" {
			excluded[model] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(recommended))
	models := make([]string, 0, len(recommended))
	for _, candidate := range recommended {
		model := strings.TrimSpace(candidate.Candidate.CanonicalName)
		if model == "" {
			continue
		}
		if _, skip := excluded[model]; skip {
			continue
		}
		if _, duplicate := seen[model]; duplicate {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, ErrNoCandidates
	}
	return models, nil
}
