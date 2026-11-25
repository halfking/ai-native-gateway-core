package dashboardapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func HandleProjectedSessionDashboard(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool, section metrics.SessionDashboardSection) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "GET required", "")
		return
	}
	params, auth, ok := prepareDashboardRequest(w, r, db)
	if !ok {
		return
	}
	requested := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	platform := auth.UserRole == "super_admin" || auth.UserRole == "admin_key"
	if !CheckTenantAccess(auth, requested) || (!platform && auth.TenantID == "") {
		writeErrorJSON(w, http.StatusForbidden, ErrCodeForbidden, "tenant scope required", "")
		return
	}
	tenant, owner, role, bypass := auth.TenantID, "", "tenant_admin", "false"
	switch {
	case platform:
		tenant, role, bypass = requested, "super_admin", "true"
	case auth.UserRole == "tenant_admin":
	case auth.IsJWT && auth.Username != "":
		owner = auth.Username
	default:
		writeErrorJSON(w, http.StatusForbidden, ErrCodeForbidden, "session owner scope required", "")
		return
	}
	window, err := parseSessionWindow(r, params)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), "")
		return
	}
	now := time.Now()
	query, days := projectedSessionWindow(window, now)
	query.Tenant, query.Owner = tenant, owner
	query.Page, query.Size = params.Page, params.Size

	ctx, cancel := GetRequestContext(r, 25*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		writeProjectedSessionError(ctx, w, http.StatusServiceUnavailable, ErrCodeDatabaseError, "database unavailable", err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT set_config('app.current_tenant',$1,true), set_config('app.current_role',$2,true),
		set_config('app.bypass_rls',$3,true)`, tenant, role, bypass)
	if err != nil {
		writeProjectedSessionError(ctx, w, http.StatusInternalServerError, ErrCodeDatabaseError, "session scope unavailable", err)
		return
	}
	result, err := metrics.ReadSessionDashboard(ctx, tx, query, section)
	if err != nil {
		writeProjectedSessionError(ctx, w, http.StatusInternalServerError, ErrCodeDatabaseError, "session statistics unavailable", err)
		return
	}
	if !result.Ready {
		writeErrorJSON(w, http.StatusServiceUnavailable, ErrCodeDataNotReady, "request_metrics projection is building", "")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeProjectedSessionError(ctx, w, http.StatusInternalServerError, ErrCodeDatabaseError, "session statistics unavailable", err)
		return
	}
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(result.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		writeProjectedSessionError(ctx, w, http.StatusInternalServerError, ErrCodeInternal, "invalid session statistics", err)
		return
	}
	probeScope := "excluded"
	if query.IncludeProbes {
		probeScope = "included"
	}
	data["source"] = "request_metrics"
	data["lifecycle_source"] = "session_dim"
	data["probe_scope"] = probeScope
	data["window_mode"] = "calendar"
	data["timezone"] = query.Timezone
	data["period_start"], data["period_end"] = query.Start.UTC(), query.End.UTC()
	data["period"] = map[string]any{"start": query.Start.UTC(), "end": query.End.UTC(), "days": days}
	data["generated_at"] = now
	data["updated_at"], data["pending_since"] = result.UpdatedAt, result.PendingSince
	metadata := &Metadata{GeneratedAt: now, TookMs: time.Since(now).Milliseconds()}
	if section == metrics.SessionDashboardActive {
		total, ok := data["total_active"].(json.Number)
		if !ok {
			writeErrorJSON(w, http.StatusInternalServerError, ErrCodeInternal, "invalid active session total", "")
			return
		}
		n, err := total.Int64()
		if err != nil {
			writeProjectedSessionError(ctx, w, http.StatusInternalServerError, ErrCodeInternal, "invalid active session total", err)
			return
		}
		metadata.Page, metadata.Size, metadata.Total = query.Page, query.Size, int(n)
		metadata.Pages = (metadata.Total + metadata.Size - 1) / metadata.Size
	}
	writeSuccessJSON(w, data, metadata)
}

func writeProjectedSessionError(ctx context.Context, w http.ResponseWriter, status int, code, message string, err error) {
	if ctx.Err() == context.Canceled {
		w.WriteHeader(499) // Client closed the request.
		return
	}
	slog.ErrorContext(ctx, message, "code", code, "error", err)
	writeErrorJSON(w, status, code, message, "")
}

func projectedSessionWindow(window sessionWindow, now time.Time) (metrics.SessionDashboardQuery, int) {
	location := settings.SystemTimezoneLocation()
	start, end, days := window.startUTC.In(location), window.endUTC.In(location), window.days
	if window.calendar {
		first := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		boundary := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
		days = int(boundary.Sub(first).Hours() / 24)
	} else {
		today := now.In(location)
		midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
		start, end = midnight.AddDate(0, 0, 1-days), midnight.AddDate(0, 0, 1)
	}
	asOf := now
	if end.Before(asOf) {
		asOf = end
	}
	recent := asOf.Add(-24 * time.Hour)
	if recent.Before(start) {
		recent = start
	}
	return metrics.SessionDashboardQuery{
		Start: start, End: end, PrevStart: start.AddDate(0, 0, -days),
		RecentStart: recent, AsOf: asOf,
		Timezone: settings.SystemTimezoneName(), IncludeProbes: window.includeProbes,
	}, days
}
