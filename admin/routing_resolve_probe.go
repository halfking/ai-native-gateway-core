package admin

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
)

func (h *Handler) persistResolveProbe(ctx context.Context, model string, candidates []resolveProbeCandidate) {
	if h.db == nil || len(candidates) == 0 {
		return
	}
	planned := make([]map[string]interface{}, 0, len(candidates))
	blocked := make([]map[string]interface{}, 0)
	var chosenID *int
	for _, c := range candidates {
		entry := map[string]interface{}{
			"provider_id":   c.ProviderID,
			"credential_id": c.CredentialID,
			"raw_model":     c.ModelName,
			"tier":          c.Tier,
		}
		planned = append(planned, entry)
		if !c.Routable {
			entry["reason"] = c.BlockReason
			blocked = append(blocked, entry)
		} else if chosenID == nil {
			id := c.CredentialID
			chosenID = &id
		}
	}
	trace := map[string]interface{}{
		"planned_candidates": planned,
		"blocked_candidates": blocked,
		"probe":              true,
		"source":             "resolve_api",
	}
	traceJSON, _ := json.Marshal(trace)
	reqID := uuid.New()
	_, err := h.db.Exec(ctx, `
		INSERT INTO routing_decision_log (
			ts, request_id, model, client_model, canonical_model,
			chosen_credential_id, candidates_tried, success,
			resolution_path, decision_trace
		) VALUES (
			now(), $1, $2, $2, $2,
			$3, $4, $5,
			'resolve_probe', $6::jsonb
		)
	`, reqID, model, chosenID, len(candidates), chosenID != nil, string(traceJSON))
	if err != nil {
		slog.Warn("resolve probe persist failed", "model", model, "error", err)
		return
	}
	globalFunnelCache.invalidateModel(model)
}

type resolveProbeCandidate struct {
	ProviderID   int
	CredentialID int
	ModelName    string
	Tier         int
	Routable     bool
	BlockReason  string
}
