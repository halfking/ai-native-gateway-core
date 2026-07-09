package admin

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
)

func (h *Handler) persistResolveProbe(ctx context.Context, model string, candidates []resolveProbeCandidate) {
	// 添加 panic 恢复，确保不会因为 probe 逻辑阻断主流程
	defer func() {
		if r := recover(); r != nil {
			slog.Error("persistResolveProbe panic recovered", "model", model, "panic", r)
		}
	}()

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
	// INSERT directly targets routing_decision_log_hot (the canonical
	// write target per the 2026-07 data-lifecycle architecture — never
	// the parent table, which would let PG auto-route rows into a monthly
	// partition that cannot be UPDATEd/DELETEd later).
	_, err := h.db.Exec(ctx, `
		INSERT INTO routing_decision_log_hot (
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
		// 改为 Error 级别并记录详细信息，但不阻断主流程
		slog.Error("resolve probe persist failed - table may not exist, check migration 346",
			"model", model,
			"error", err.Error(),
			"candidates_count", len(candidates))
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
