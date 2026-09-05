package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// node_health.go — 会话优化 v4 §13.5 节点恢复时间线（timeline only）
//
//	GET /api/admin/node-health/{credential_id}/timeline?since=24h
//
// 真源：node_probe_runs（探测完成后审计行）。不返回 request_body / headers。

const (
	nodeHealthDefaultSince = 24 * time.Hour
	nodeHealthMaxSince     = 7 * 24 * time.Hour
	nodeHealthTimelineCap  = 200
)

// nodeRecoveryEvent matches the SPA NodeRecoveryEvent contract.
type nodeRecoveryEvent struct {
	CredentialID string  `json:"credential_id"`
	EventType    string  `json:"event_type"`
	OccurredAt   string  `json:"occurred_at"`
	DurationMs   *int    `json:"duration_ms,omitempty"`
	ReasonCode   *string `json:"reason_code,omitempty"`
	Note         *string `json:"note,omitempty"`
}

type nodeRecoveryTimelineResponse struct {
	CredentialID      string              `json:"credential_id"`
	ObservationStatus string              `json:"observation_status"`
	Events            []nodeRecoveryEvent `json:"events"`
}

type probeRunRow struct {
	CredentialID   int64
	RawModelName   string
	TriggerKind    string
	Success        bool
	DirectErrCode  *string
	GatewayErrCode *string
	DurationMs     int
	StartedAt      time.Time
	CompletedAt    *time.Time
}

func parseTimelineSince(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nodeHealthDefaultSince, nil
	}
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days < 1 {
			return 0, fmt.Errorf("invalid since: %q", raw)
		}
		d := time.Duration(days) * 24 * time.Hour
		if d > nodeHealthMaxSince {
			d = nodeHealthMaxSince
		}
		return d, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid since: %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid since: %q", raw)
	}
	if d > nodeHealthMaxSince {
		d = nodeHealthMaxSince
	}
	return d, nil
}

func mapProbeRunToEvent(row probeRunRow) nodeRecoveryEvent {
	credID := strconv.FormatInt(row.CredentialID, 10)
	eventType := "failed"
	if row.Success {
		eventType = "recovered"
		if row.TriggerKind == "credential_recovery" {
			eventType = "reconnected"
		}
	}
	occurred := row.StartedAt
	if row.CompletedAt != nil && !row.CompletedAt.IsZero() {
		occurred = *row.CompletedAt
	}
	ev := nodeRecoveryEvent{
		CredentialID: credID,
		EventType:    eventType,
		OccurredAt:   occurred.UTC().Format(time.RFC3339Nano),
	}
	if row.DurationMs > 0 {
		ms := row.DurationMs
		ev.DurationMs = &ms
	}
	reason := firstNonEmptyPtr(row.DirectErrCode, row.GatewayErrCode)
	if reason != nil && *reason != "" && *reason != "none" {
		ev.ReasonCode = reason
	}
	if note := formatProbeNote(row.RawModelName, row.TriggerKind); note != "" {
		ev.Note = &note
	}
	return ev
}

func firstNonEmptyPtr(a, b *string) *string {
	if a != nil && strings.TrimSpace(*a) != "" {
		return a
	}
	if b != nil && strings.TrimSpace(*b) != "" {
		return b
	}
	return nil
}

func formatProbeNote(model, trigger string) string {
	model = strings.TrimSpace(model)
	trigger = strings.TrimSpace(trigger)
	switch {
	case model != "" && trigger != "":
		return model + " · " + trigger
	case model != "":
		return model
	case trigger != "":
		return trigger
	default:
		return ""
	}
}

// handleNodeHealthTimeline serves GET /api/admin/node-health/{credential_id}/timeline.
func (h *Handler) handleNodeHealthTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rawID := r.PathValue("credential_id")
	credID, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid credential_id")
		return
	}
	since, err := parseTimelineSince(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	events, err := h.loadNodeHealthTimeline(r.Context(), credID, since)
	if err != nil {
		slog.Error("node-health timeline query failed", "credential_id", credID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "node-health timeline unavailable")
		return
	}
	if events == nil {
		events = []nodeRecoveryEvent{}
	}
	writeJSON(w, http.StatusOK, nodeRecoveryTimelineResponse{
		CredentialID:      strconv.FormatInt(credID, 10),
		ObservationStatus: "complete",
		Events:            events,
	})
}

func (h *Handler) loadNodeHealthTimeline(ctx context.Context, credID int64, since time.Duration) ([]nodeRecoveryEvent, error) {
	cutoff := time.Now().UTC().Add(-since)
	rows, err := h.db.Query(ctx, `
		SELECT credential_id, raw_model_name, trigger_kind, success,
		       direct_err_code, gateway_err_code, duration_ms,
		       started_at, completed_at
		FROM node_probe_runs
		WHERE credential_id = $1
		  AND started_at >= $2
		ORDER BY started_at DESC
		LIMIT $3`,
		credID, cutoff, nodeHealthTimelineCap,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]nodeRecoveryEvent, 0, 32)
	for rows.Next() {
		var row probeRunRow
		if err := rows.Scan(
			&row.CredentialID,
			&row.RawModelName,
			&row.TriggerKind,
			&row.Success,
			&row.DirectErrCode,
			&row.GatewayErrCode,
			&row.DurationMs,
			&row.StartedAt,
			&row.CompletedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, mapProbeRunToEvent(row))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Chronological ascending for the timeline UI.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
