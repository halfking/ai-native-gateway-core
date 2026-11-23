package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) handleRequestReconciliation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	params := r.URL.Query()
	scope, ok := requestReconciliationScope(r, params.Get("tenant_id"))
	if !ok {
		writeError(w, http.StatusForbidden, "report scope unavailable")
		return
	}
	query, err := parseRequestReconciliationQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if reconciliation.ReportRequiresPlatformAccess(query.Section) && !scope.Platform {
		writeError(w, http.StatusForbidden, "report section requires platform access")
		return
	}
	if query.Section == "internal" && scope.TenantID == "" {
		writeError(w, http.StatusBadRequest, "internal reconciliation requires tenant_id")
		return
	}
	format := params.Get("format")
	if format != "" && format != "json" && format != "csv" && format != "csv-summary" {
		writeError(w, http.StatusBadRequest, "format must be json, csv, or csv-summary")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	report, err := reconciliation.ReadReport(ctx, h.db, scope, query)
	if err != nil {
		slog.WarnContext(ctx, "request reconciliation report read failed", "section", query.Section, "error", err)
		writeError(w, http.StatusServiceUnavailable, "report data unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if format == "csv" || format == "csv-summary" {
		var data []byte
		name := query.Section
		if format == "csv-summary" {
			name += "-summary"
			data, err = requestReconciliationSummaryCSV(report.Summary)
			w.Header().Set("X-Report-Export", "summary")
		} else {
			data, err = requestReconciliationCSV(report)
			w.Header().Set("X-Report-Export", "page")
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "report export unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="request-reconciliation-`+name+`.csv"`)
		w.Header().Set("X-Report-Version", report.Version)
		w.Header().Set("X-Report-Read-At", report.ReadAt.UTC().Format(time.RFC3339Nano))
		w.Header().Set("X-Report-Start", report.Start.Format(time.RFC3339Nano))
		w.Header().Set("X-Report-End", report.End.Format(time.RFC3339Nano))
		w.Header().Set("X-Report-Timezone", report.Timezone)
		if report.Day != "" {
			w.Header().Set("X-Report-Day", report.Day)
		}
		if report.Month != "" {
			w.Header().Set("X-Report-Month", report.Month)
		}
		w.Header().Set("X-Report-Coverage", report.Coverage)
		w.Header().Set("X-Report-Limit", strconv.Itoa(report.Limit))
		w.Header().Set("X-Report-Offset", strconv.Itoa(report.Offset))
		_, _ = w.Write(data)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func requestReconciliationSummaryCSV(raw json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var data bytes.Buffer
	out := csv.NewWriter(&data)
	if err := out.Write([]string{"metric", "value"}); err != nil {
		return nil, err
	}
	for _, key := range keys {
		value := string(fields[key])
		if value == "null" {
			value = ""
		}
		if err := out.Write([]string{key, value}); err != nil {
			return nil, err
		}
	}
	out.Flush()
	return data.Bytes(), out.Error()
}

func requestReconciliationScope(r *http.Request, requestedTenant string) (reconciliation.ReportScope, bool) {
	requestedTenant = strings.TrimSpace(requestedTenant)
	if IsSuperAdminOrLegacy(r) {
		return reconciliation.ReportScope{Platform: true, TenantID: requestedTenant}, true
	}
	if auth := GetAuthContext(r); auth != nil && auth.Role == "tenant_admin" && strings.TrimSpace(auth.TenantID) != "" {
		if requestedTenant == "" || requestedTenant == auth.TenantID {
			return reconciliation.ReportScope{TenantID: auth.TenantID}, true
		}
	}
	return reconciliation.ReportScope{}, false
}

func parseRequestReconciliationQuery(r *http.Request) (reconciliation.ReportQuery, error) {
	params := r.URL.Query()
	query := reconciliation.ReportQuery{Section: params.Get("section"), Timezone: params.Get("timezone"), Day: params.Get("day"), Month: params.Get("month"), Limit: 200}
	if query.Section == "" {
		query.Section = "requests"
	}
	if len(reconciliation.ReportColumns(query.Section)) == 0 {
		return query, errors.New("section must be requests, attempts, postings, sessions, coverage, or internal")
	}
	calendar := params.Has("day") || params.Has("month")
	if reconciliation.ReportRequiresCalendarPeriod(query.Section) && !calendar {
		return query, errors.New("section requires day=YYYY-MM-DD or month=YYYY-MM")
	}
	if query.Timezone == "" && calendar {
		if params.Has("day") {
			query.Timezone = settings.SystemTimezoneName()
		} else {
			query.Timezone = reconciliation.DefaultMonthTimezone
		}
	}
	if query.Timezone == "" || query.Timezone == "Local" {
		return query, errors.New("timezone must be explicit (IANA name or UTC)")
	}
	var err error
	if calendar {
		if (params.Has("day") && params.Has("month")) || params.Has("start") || params.Has("end") {
			return query, errors.New("day, month, and explicit start/end are mutually exclusive")
		}
		if params.Has("day") {
			query.Start, query.End, err = reconciliation.NaturalDay(query.Day, query.Timezone)
		} else {
			query.Start, query.End, err = reconciliation.NaturalMonth(query.Month, query.Timezone)
		}
		if err != nil {
			return query, err
		}
	} else {
		if _, err := time.LoadLocation(query.Timezone); err != nil {
			return query, errors.New("invalid IANA timezone")
		}
		query.Start, err = time.Parse(time.RFC3339Nano, params.Get("start"))
		if err != nil {
			return query, errors.New("start must be an RFC3339 timestamp")
		}
		query.End, err = time.Parse(time.RFC3339Nano, params.Get("end"))
		if err != nil || !query.End.After(query.Start) || query.End.Sub(query.Start) > 366*24*time.Hour {
			return query, errors.New("end must be after start with a window of at most 366 days")
		}
	}
	if limit, set := params["limit"]; set {
		query.Limit, err = strconv.Atoi(limit[0])
		if err != nil || query.Limit < 1 || query.Limit > 2000 {
			return query, errors.New("limit must be between 1 and 2000")
		}
	}
	if offset, set := params["offset"]; set {
		query.Offset, err = strconv.Atoi(offset[0])
		if err != nil || query.Offset < 0 {
			return query, errors.New("offset must be a non-negative integer")
		}
	}
	return query, nil
}

func requestReconciliationCSV(report reconciliation.Report) ([]byte, error) {
	return reconciliationRowsCSV(reconciliation.ReportColumns(report.Section), report.Rows)
}

func reconciliationRowsCSV(columns []string, rows []json.RawMessage) ([]byte, error) {
	var data bytes.Buffer
	out := csv.NewWriter(&data)
	if err := out.Write(columns); err != nil {
		return nil, err
	}
	for _, raw := range rows {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		fields := make([]string, len(columns))
		for i, name := range columns {
			value := row[name]
			if len(value) == 0 || string(value) == "null" {
				continue
			}
			if value[0] != '"' {
				fields[i] = string(value)
				continue
			}
			if err := json.Unmarshal(value, &fields[i]); err != nil {
				return nil, err
			}
			// Amount strings are produced from numeric values by PostgreSQL.
			// Protect user-controlled identifiers and labels in spreadsheets.
			switch name {
			case "estimated_amount", "reported_amount", "internal_amount", "invoice_amount", "bill_amount_cents", "captured_estimate", "captured_reported", "comparable_estimate", "comparable_reported", "estimated_minus_invoiced_amount", "reported_minus_invoiced_amount":
			default:
				trimmed := strings.TrimLeft(fields[i], " \t\r\n")
				if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
					fields[i] = "'" + fields[i]
				}
			}
		}
		if err := out.Write(fields); err != nil {
			return nil, err
		}
	}
	out.Flush()
	return data.Bytes(), out.Error()
}
