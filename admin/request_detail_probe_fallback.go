package admin

// request_detail_probe_fallback.go — when business session_turns miss a
// request_id, try session_turns_probe_hot so isolated probes are still
// recoverable as metadata (source=probe_lane). Bodies are intentionally
// empty: the probe hot table has request_preview only.
//
// Honest boundary: probe PG has no full body; metadata comes from probe_hot
// (+ optional hotzone file mirror). Business restore still uses turns+bodies.

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

// probeLaneDetailQuerier is the narrow surface used by the probe fallback
// (satisfied by *pgxpool.Pool and pgxmock pools).
type probeLaneDetailQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func isIdleMarkerRequestID(requestID string) bool {
	return strings.HasPrefix(strings.TrimSpace(requestID), "idle-")
}

func (h *Handler) loadProbeLaneDetail(ctx context.Context, requestID string) (*requestdetail.Detail, error) {
	if h == nil || h.db == nil || !probelane.PhysicalEnabled() {
		return nil, requestdetail.ErrNotFound
	}
	return loadProbeLaneDetailFromQuerier(ctx, h.db, requestID)
}

func loadProbeLaneDetailFromQuerier(ctx context.Context, db probeLaneDetailQuerier, requestID string) (*requestdetail.Detail, error) {
	if db == nil || strings.TrimSpace(requestID) == "" {
		return nil, requestdetail.ErrNotFound
	}
	var (
		rid, tenantID                      string
		success                            bool
		clientModel, outboundModel         *string
		latencyMs                          *int
		gwSessionID, preview               *string
	)
	err := db.QueryRow(ctx, `
		SELECT request_id, tenant_id, success,
		       client_model, outbound_model,
		       latency_ms, gw_session_id, request_preview
		  FROM public.session_turns_probe_hot
		 WHERE request_id = $1
		 LIMIT 1
	`, requestID).Scan(
		&rid, &tenantID, &success,
		&clientModel, &outboundModel,
		&latencyMs, &gwSessionID, &preview,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, requestdetail.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	status := "failure"
	if success {
		status = "success"
	}
	meta := requestdetail.Meta{
		RequestID:   rid,
		TenantID:    tenantID,
		GwSessionID: gwSessionID,
		ClientModel: clientModel,
		Status:      &status,
		Success:     &success,
		LatencyMs:   latencyMs,
		BodyStatus:  "unavailable",
	}
	if outboundModel != nil && *outboundModel != "" && (clientModel == nil || *clientModel == "") {
		meta.ClientModel = outboundModel
	}
	warning := "source=probe_lane: metadata from session_turns_probe_hot; full request/response bodies are not stored on the probe lane"
	if preview != nil && strings.TrimSpace(*preview) != "" {
		warning += "; request_preview is available via /api/admin/probe/lane/requests"
	}
	return &requestdetail.Detail{
		Source:      requestdetail.SourceProbeLane,
		Persistence: requestdetail.PersistencePersisted,
		Meta:        meta,
		Bodies:      &requestdetail.Bodies{},
		Warning:     warning,
	}, nil
}
