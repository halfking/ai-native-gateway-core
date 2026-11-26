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
	if section != "summary" && section != "dimensions" && section != "export" {
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
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
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
	names := h.dimensionNames(ctx)
	daily := reportDailyDetail(r)
	if section == "export" {
		daily = reportExportDetail(r)
	}
	err = h.readProjectedReport(ctx, start, end, func(q reportrollup.Querier, p reportrollup.ProjectionCoverage) error {
		var err error
		rep, err = reportrollup.BuildProjectedReport(ctx, q, start, end, view, filter, names, daily, p)
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
