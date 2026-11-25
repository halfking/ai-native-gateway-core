package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) handleReportRollupV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	section := r.PathValue("section")
	if section != "summary" && section != "dimensions" && section != "export" && section != "daily" {
		writeError(w, http.StatusNotFound, "unknown report-rollup resource")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	start, end, err := projectedReportRange(r, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, filter, err := reportViewFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.URL.Query().Get("traffic_scope") {
	case "", "all":
	case "business":
		filter.BusinessOnly = true
	default:
		writeError(w, http.StatusBadRequest, "traffic_scope must be business or all")
		return
	}
	trafficScope := "all"
	if filter.BusinessOnly || view == reportrollup.ViewInternal || view == reportrollup.ViewKey {
		trafficScope = "business"
	}
	var group reportrollup.GrainGroup
	if section == "export" {
		group, err = reportrollup.ParseGrainGroup(r.URL.Query().Get("group"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	daily := reportDailyDetail(r)
	params := r.URL.Query()
	scope := reportrollup.ProjectedSummaryScope(params.Get("summary_scope"))
	if scope != "" && (section != "summary" || daily ||
		(scope != reportrollup.ProjectedSummaryTotals && scope != reportrollup.ProjectedSummaryDistribution)) {
		writeError(w, http.StatusBadRequest, "summary_scope must be totals or distribution on a summary without daily detail")
		return
	}
	var personPage int32
	if params.Has("person_page") {
		page, err := strconv.ParseInt(params.Get("person_page"), 10, 32)
		if err != nil || page < 1 || view != reportrollup.ViewInternal || scope != reportrollup.ProjectedSummaryDistribution {
			writeError(w, http.StatusBadRequest, "person_page must be a positive integer on an internal distribution summary")
			return
		}
		personPage = int32(page)
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	if section == "daily" {
		h.handleProjectedDailyPage(ctx, w, r, start, end, view, filter, trafficScope)
		return
	}
	if section == "dimensions" {
		var dims *reportrollup.DimensionOptions
		var coverage reportrollup.ProjectionCoverage
		err := h.readProjectedReport(ctx, start, end, func(q reportrollup.Querier, p reportrollup.ProjectionCoverage) error {
			coverage = p
			var err error
			dims, err = reportrollup.LoadProjectedDimensionOptions(ctx, q, view, filter, p)
			return err
		})
		if err != nil {
			writeProjectedReportError(w, err)
			return
		}
		fillReportDimensionNames(dims, h.dimensionNames(ctx))
		writeJSON(w, http.StatusOK, map[string]any{"dimensions": dims, "source": reportrollup.ProjectedSource, "projection": coverage, "traffic_scope": trafficScope})
		return
	}
	var rep *reportrollup.GrainReport
	if section == "export" {
		daily = reportExportDetail(r)
	}
	var names reportrollup.Names
	switch {
	case scope == "":
		names = h.dimensionNames(ctx)
	case scope == reportrollup.ProjectedSummaryDistribution && (view == reportrollup.ViewProvider || view == reportrollup.ViewCredential):
		names.Providers = h.providerNames(ctx)
	}
	err = h.readProjectedReport(ctx, start, end, func(q reportrollup.Querier, p reportrollup.ProjectionCoverage) error {
		var err error
		switch {
		case personPage > 0:
			rep, err = reportrollup.BuildProjectedPersonPage(ctx, q, start, end, filter, p, personPage)
		case scope == "":
			rep, err = reportrollup.BuildProjectedReport(ctx, q, start, end, view, filter, names, daily, p)
		default:
			rep, err = reportrollup.BuildProjectedSummary(ctx, q, start, end, view, filter, names, p, scope)
		}
		return err
	})
	if err != nil {
		writeProjectedReportError(w, err)
		return
	}
	if section == "export" {
		writeReportRollupWorkbook(w, rep, group, daily, start, end)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "traffic_scope": trafficScope})
}

func (h *Handler) handleProjectedDailyPage(ctx context.Context, w http.ResponseWriter, r *http.Request, start, end time.Time, view reportrollup.View, filter reportrollup.GrainFilter, trafficScope string) {
	params := r.URL.Query()
	dayValue := params.Get("day")
	day, err := settings.ParseSystemDate(dayValue)
	if err != nil || day.Format(time.DateOnly) != dayValue || day.Before(start) || day.After(end) {
		writeError(w, http.StatusBadRequest, "day must be a YYYY-MM-DD date in the report range")
		return
	}
	group, err := reportrollup.ParseGrainGroup(params.Get("group"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var number int64 = 1
	if params.Has("page") {
		number, err = strconv.ParseInt(params.Get("page"), 10, 32)
		if err != nil || number < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return
		}
	}
	var coverage reportrollup.ProjectionCoverage
	var page reportrollup.ProjectedDailyPage
	if err := h.readProjectedReport(ctx, day, day, func(q reportrollup.Querier, p reportrollup.ProjectionCoverage) error {
		coverage = p
		var err error
		page, err = reportrollup.BuildProjectedDailyPage(ctx, q, view, filter, p, group, int(number))
		return err
	}); err != nil {
		writeProjectedReportError(w, err)
		return
	}
	h.fillProjectedDailyLabels(ctx, group, page.Rows)
	writeJSON(w, http.StatusOK, map[string]any{"day": day.Format(time.DateOnly), "group": group,
		"page": page, "projection": coverage, "source": reportrollup.ProjectedSource, "traffic_scope": trafficScope})
}

func (h *Handler) fillProjectedDailyLabels(ctx context.Context, group reportrollup.GrainGroup, rows []reportrollup.DailyGroupRow) {
	if len(rows) == 0 {
		return
	}
	var query string
	switch group {
	case reportrollup.GrainGroupProvider:
		query = `SELECT id, display_name FROM providers WHERE id=ANY($1::bigint[])`
	case reportrollup.GrainGroupCredential:
		query = `SELECT id, label FROM credentials WHERE id=ANY($1::bigint[])`
	case reportrollup.GrainGroupAPIKey:
		query = `SELECT id, COALESCE(NULLIF(key_alias, ''), key_prefix) FROM api_keys WHERE id=ANY($1::bigint[])`
	default:
		return
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i], _ = strconv.ParseInt(row.Key, 10, 64)
	}
	labels := h.idLabelMap(ctx, query, ids)
	for i := range rows {
		rows[i].Name = labels[ids[i]]
	}
}

func projectedReportRange(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	params := r.URL.Query()
	if !params.Has("days") {
		return reportRange(r)
	}
	days, err := strconv.Atoi(params.Get("days"))
	if err != nil || days < 1 || days > 366 || params.Has("start") || params.Has("end") {
		return time.Time{}, time.Time{}, fmt.Errorf("days must be between 1 and 366 and cannot be combined with start or end")
	}
	end := settings.SystemDayStart(now).AddDate(0, 0, -1)
	return end.AddDate(0, 0, 1-days), end, nil
}

func (h *Handler) readProjectedReport(ctx context.Context, start, end time.Time, read func(reportrollup.Querier, reportrollup.ProjectionCoverage) error) error {
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL jit = off"); err != nil {
		return fmt.Errorf("disable jit: %w", err)
	}
	coverage, err := reportrollup.ReadProjectionCoverage(ctx, tx, start, end, time.Now())
	if err != nil {
		return err
	}
	return read(tx, coverage)
}

func writeProjectedReportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reportrollup.ErrProjectionBackfill):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case reportDegraded(err):
		writeError(w, http.StatusServiceUnavailable, "request metrics projection schema is unavailable")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "request metrics report timed out")
	default:
		slog.Error("projected report rollup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "request metrics report failed")
	}
}
