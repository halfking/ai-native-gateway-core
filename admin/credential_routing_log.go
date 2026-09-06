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

// credential_routing_log.go — merged routing-event timeline behind the
// /routing-v2/credentials "路由记录" tab (2026-09-07, spec in
// docs/FEATURE-REQ-credential-heatmap-routing-log.md §5).
//
// One endpoint unions three record families into a single time line:
//   - routing      routing_decision_log rows (per-request route selection)
//   - probe        model_probe_runs rows (self-test outcomes)
//   - state_change model_probe_runs.state_change IN (recovered, broke)
//     + routing_audit_log manual model toggles (online/offline)

// RoutingLogEntry is one merged timeline row.
type RoutingLogEntry struct {
	TS              string  `json:"ts"`
	Kind            string  `json:"kind"`             // routing | probe | state_change
	Change          *string `json:"change,omitempty"` // recovered | broke | online | offline
	Model           string  `json:"model"`
	CredentialID    *int    `json:"credential_id,omitempty"`
	CredentialLabel string  `json:"credential_label"`
	ProviderName    string  `json:"provider_name"`
	Success         *bool   `json:"success,omitempty"`
	Status          string  `json:"status"`
	LatencyMs       *int    `json:"latency_ms,omitempty"`
	ErrorCode       *string `json:"error_code,omitempty"`
	ErrorMessage    *string `json:"error_message,omitempty"`
	RequestID       *string `json:"request_id,omitempty"`
	Tier            *int    `json:"tier,omitempty"`
	Source          string  `json:"source"` // gateway | scheduler | manual | routing_4xx | ...
	Actor           *string `json:"actor,omitempty"`
}

// RoutingLogResponse is the response envelope for /api/credentials/routing-log.
type RoutingLogResponse struct {
	Meta    RoutingLogMeta    `json:"meta"`
	Entries []RoutingLogEntry `json:"entries"`
	Total   int64             `json:"total"`
}

// RoutingLogMeta echoes the query window for the UI.
type RoutingLogMeta struct {
	TimeStart  string `json:"time_start"`
	TimeEnd    string `json:"time_end"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Result     string `json:"result"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	DurationMs int64  `json:"duration_ms"`
}

// routingLogParams captures the request-derived filters for buildRoutingLogSQL.
type routingLogParams struct {
	TimeStart    time.Time
	TimeEnd      time.Time
	Kind         string // all | routing | probe | state_change
	Model        string // substring, case-insensitive; empty = no filter
	Result       string // all | success | failed
	CredentialID int    // 0 = no filter
	TenantID     string // empty = no tenant scoping
	Limit        int
	Offset       int
}

// handleCredentialRoutingLog serves GET /api/credentials/routing-log.
func (m *CredentialMonitorHandlers) handleCredentialRoutingLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.h == nil || m.h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	kind := queryString(r, "kind")
	if kind == "" {
		kind = "all"
	}
	switch kind {
	case "all", "routing", "probe", "state_change":
	default:
		writeError(w, http.StatusBadRequest, "invalid kind: must be all, routing, probe or state_change")
		return
	}

	result := queryString(r, "result")
	if result == "" {
		result = "all"
	}
	switch result {
	case "all", "success", "failed":
	default:
		writeError(w, http.StatusBadRequest, "invalid result: must be all, success or failed")
		return
	}

	timeStart, timeEnd, err := parseRoutingLogTimeRange(queryString(r, "time_start"), queryString(r, "time_end"), 24*time.Hour)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	limit := parseIntParam(queryString(r, "limit"), 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := parseIntParam(queryString(r, "offset"), 0)
	if offset < 0 {
		offset = 0
	}

	tenantID := ""
	if IsTenantAdmin(r) {
		tenantID = GetTenantID(r)
	}

	params := routingLogParams{
		TimeStart:    timeStart,
		TimeEnd:      timeEnd,
		Kind:         kind,
		Model:        strings.TrimSpace(queryString(r, "model")),
		Result:       result,
		CredentialID: parseIntParam(queryString(r, "credential_id"), 0),
		TenantID:     tenantID,
		Limit:        limit,
		Offset:       offset,
	}

	startedAt := time.Now()
	entries, total, err := runRoutingLogQuery(ctx, m.h.db, params)
	if err != nil {
		slog.Error("routing log query failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "routing log query failed: "+err.Error())
		return
	}

	resp := RoutingLogResponse{
		Entries: entries,
		Total:   total,
		Meta: RoutingLogMeta{
			TimeStart:  timeStart.Format(time.RFC3339),
			TimeEnd:    timeEnd.Format(time.RFC3339),
			Kind:       kind,
			Model:      params.Model,
			Result:     result,
			Limit:      limit,
			Offset:     offset,
			DurationMs: time.Since(startedAt).Milliseconds(),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseRoutingLogTimeRange parses an RFC3339 window, falling back to
// [now-default, now] when either bound is missing.
func parseRoutingLogTimeRange(startStr, endStr string, def time.Duration) (time.Time, time.Time, error) {
	now := time.Now()
	timeEnd := now
	timeStart := now.Add(-def)

	if startStr != "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return timeStart, timeEnd, fmt.Errorf("invalid time_start format (use RFC3339)")
		}
		timeStart = t
	}
	if endStr != "" {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return timeStart, timeEnd, fmt.Errorf("invalid time_end format (use RFC3339)")
		}
		timeEnd = t
	}
	if timeEnd.Before(timeStart) {
		return timeStart, timeEnd, fmt.Errorf("time_end must be after time_start")
	}
	return timeStart, timeEnd, nil
}

// buildRoutingLogSQL assembles the UNION query and bound args. Split out so
// tests can pin the SQL shape without a database.
//
// Filters are pushed into each branch (per-branch placeholders continue the
// shared arg numbering); the audit/state_change branch is dropped entirely
// when a success/failed result filter would make every row invisible anyway.
func buildRoutingLogSQL(p routingLogParams) (string, []any) {
	// Normalize defaults so direct callers (tests, tooling) behave like the
	// HTTP handler.
	if p.Kind == "" {
		p.Kind = "all"
	}
	if p.Result == "" {
		p.Result = "all"
	}

	args := []any{p.TimeStart, p.TimeEnd}
	nextArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	includeRouting := p.Kind == "all" || p.Kind == "routing"
	// The probe branch carries both kinds: full self-test runs for "probe",
	// consensus flips only (state_change filter below) for "state_change".
	includeProbe := p.Kind == "all" || p.Kind == "probe" || p.Kind == "state_change"
	// state_change timeline: probe consensus flips + manual toggles.
	includeStateChanges := p.Kind == "all" || p.Kind == "state_change"
	// A success/failed filter cannot match state_change rows (no success
	// semantics) — skip those branches instead of returning guaranteed noise.
	if p.Result != "all" {
		includeStateChanges = false
	}

	modelFilter := ""
	if p.Model != "" {
		pat := "%" + p.Model + "%"
		modelFilter = nextArg(pat)
	}
	resultFilter := ""
	if p.Result != "all" {
		resultFilter = nextArg(p.Result == "success")
	}
	credFilter := ""
	if p.CredentialID > 0 {
		credFilter = nextArg(p.CredentialID)
	}
	tenantFilter := ""
	if p.TenantID != "" {
		tenantFilter = nextArg(p.TenantID)
	}

	// successExprFor / tenantColFor map the branch timestamp column to its
	// success / tenant expression — the three branches are keyed by distinct
	// aliases, so derive both from the same marker to stay in sync.
	successExprFor := func(tsCol string) string {
		switch {
		case strings.HasPrefix(tsCol, "rdl."):
			return "rdl.success"
		case strings.HasPrefix(tsCol, "mpr."):
			return "(mpr.status = 'ok')"
		default:
			return "TRUE"
		}
	}
	tenantColFor := func(tsCol string) string {
		switch {
		case strings.HasPrefix(tsCol, "rdl."):
			return "rdl.tenant_id"
		case strings.HasPrefix(tsCol, "mpr."):
			return "mpr.tenant_id"
		default:
			return "al.tenant_id"
		}
	}

	branchWhere := func(tsCol, modelExpr, credExpr string, applyResult bool) string {
		clauses := []string{
			fmt.Sprintf("%s >= $1", tsCol),
			fmt.Sprintf("%s < $2", tsCol),
		}
		if modelFilter != "" && modelExpr != "" {
			clauses = append(clauses, fmt.Sprintf("%s ILIKE %s", modelExpr, modelFilter))
		}
		if resultFilter != "" && applyResult {
			clauses = append(clauses, fmt.Sprintf("%s = %s", successExprFor(tsCol), resultFilter))
		}
		if credFilter != "" && credExpr != "" {
			clauses = append(clauses, fmt.Sprintf("%s = %s", credExpr, credFilter))
		}
		if tenantFilter != "" {
			clauses = append(clauses, fmt.Sprintf("%s = %s", tenantColFor(tsCol), tenantFilter))
		}
		return strings.Join(clauses, " AND ")
	}

	branches := make([]string, 0, 3)

	if includeRouting {
		where := branchWhere("rdl.ts", "COALESCE(rdl.model, rdl.client_model, rdl.outbound_model)", "rdl.chosen_credential_id", true)
		branches = append(branches, fmt.Sprintf(`
		SELECT
			rdl.ts AS ts,
			'routing' AS kind,
			NULL::text AS change,
			COALESCE(rdl.model, rdl.client_model, rdl.outbound_model, '') AS model,
			rdl.chosen_credential_id AS credential_id,
			COALESCE(c.label, '') AS credential_label,
			COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
			rdl.success,
			CASE WHEN rdl.success THEN 'ok' ELSE 'failed' END AS status,
			rdl.latency_ms,
			rdl.error_class AS error_code,
			rdl.failure_detail_code AS error_message,
			rdl.request_id::text AS request_id,
			rdl.tier,
			'gateway' AS source,
			NULL::text AS actor
		FROM routing_decision_log rdl
		LEFT JOIN credentials c ON c.id = rdl.chosen_credential_id
		LEFT JOIN providers p ON p.id = rdl.chosen_provider_id
		WHERE %s`, where))
	}

	probeStateFilter := ""
	if p.Kind == "state_change" {
		probeStateFilter = " AND mpr.state_change IN ('recovered', 'broke')"
	}

	if includeProbe {
		where := branchWhere("mpr.created_at", "mpr.raw_model_name", "mpr.credential_id", true)
		branches = append(branches, fmt.Sprintf(`
		SELECT
			mpr.created_at AS ts,
			'probe' AS kind,
			NULL::text AS change,
			COALESCE(mpr.raw_model_name, '') AS model,
			mpr.credential_id AS credential_id,
			COALESCE(c.label, '') AS credential_label,
			COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
			(mpr.status = 'ok') AS success,
			COALESCE(mpr.status, 'unknown') AS status,
			mpr.latency_ms,
			mpr.error_code,
			mpr.error_message,
			NULL::text AS request_id,
			NULL::int AS tier,
			COALESCE(mpr.triggered_by, 'scheduler') AS source,
			NULL::text AS actor
		FROM model_probe_runs_with_current_month mpr
		LEFT JOIN credentials c ON c.id = mpr.credential_id
		LEFT JOIN providers p ON p.id = c.provider_id
		WHERE %s%s`, where, probeStateFilter))
	}

	if includeStateChanges {
		where := branchWhere("al.ts", "al.after_json->>'raw_model_name'", "(al.after_json->>'credential_id')::int", false)
		branches = append(branches, fmt.Sprintf(`
		SELECT
			al.ts AS ts,
			'state_change' AS kind,
			CASE al.action
				WHEN 'credential.model_toggle_online' THEN 'online'
				WHEN 'credential.model_toggle_offline' THEN 'offline'
			END AS change,
			COALESCE(al.after_json->>'raw_model_name', '') AS model,
			(al.after_json->>'credential_id')::int AS credential_id,
			COALESCE(c.label, '') AS credential_label,
			COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
			NULL::boolean AS success,
			COALESCE(al.action, 'manual_toggle') AS status,
			NULL::int AS latency_ms,
			NULL::text AS error_code,
			COALESCE(al.after_json->>'reason', '') AS error_message,
			NULL::text AS request_id,
			NULL::int AS tier,
			'manual' AS source,
			al.actor
		FROM routing_audit_log al
		LEFT JOIN credentials c ON c.id = (al.after_json->>'credential_id')::int
		LEFT JOIN providers p ON p.id = c.provider_id
		WHERE al.target_type = 'credential_model'
			AND al.action IN ('credential.model_toggle_online', 'credential.model_toggle_offline')
			AND %s`, where))
	}

	limitArg := nextArg(p.Limit)
	offsetArg := nextArg(p.Offset)

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total, u.*
		FROM (
			%s
		) u
		ORDER BY u.ts DESC
		LIMIT %s OFFSET %s
	`, strings.Join(branches, " UNION ALL "), limitArg, offsetArg)

	return query, args
}

// runRoutingLogQuery executes the merged routing-log query.
func runRoutingLogQuery(ctx context.Context, db pgxQueryer, p routingLogParams) ([]RoutingLogEntry, int64, error) {
	query, args := buildRoutingLogSQL(p)

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	entries := make([]RoutingLogEntry, 0, 64)
	var total int64
	for rows.Next() {
		var (
			totalArg  int64
			ts        time.Time
			e         RoutingLogEntry
			credLabel string
			provName  string
			status    string
			source    string
		)
		if err := rows.Scan(
			&totalArg, &ts, &e.Kind, &e.Change, &e.Model,
			&e.CredentialID, &credLabel, &provName, &e.Success,
			&status, &e.LatencyMs, &e.ErrorCode, &e.ErrorMessage,
			&e.RequestID, &e.Tier, &source, &e.Actor,
		); err != nil {
			slog.Warn("routing log row scan failed", "error", err.Error())
			continue
		}
		total = totalArg
		e.TS = ts.UTC().Format(time.RFC3339)
		e.CredentialLabel = credLabel
		e.ProviderName = provName
		e.Status = status
		e.Source = source
		entries = append(entries, e)
	}
	if rows.Err() != nil {
		return nil, 0, fmt.Errorf("rows iteration failed: %w", rows.Err())
	}
	return entries, total, nil
}
