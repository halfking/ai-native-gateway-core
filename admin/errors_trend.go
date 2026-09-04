package admin

// errors_trend.go — 2026-09-05 审计闭环1：供应商错误趋势 API。
//
// GET /api/errors/trend
//   参数：hours（1/24/168，默认24）、granularity（minute/hour/day，默认按
//   hours 自动选择）、supplier、credential_id、error_type（可叠加过滤）。
//
// 读源（唯一事实源，V371）：
//   1. supplier_error_stats —— 预聚合表（分钟桶由 bg 聚合器每 5 分钟
//      UPSERT），趋势图默认走这里；
//   2. supplier_errors_unified —— 当窗口内 stats 尚无行（聚合器刚部署 /
//      窗口太新）时直接对 unified 明细聚合，响应标注 source=fallback。
//
// 响应形状对齐 docs/prompts/task3-error-display-optimization.md 的
// ErrorTrendResponse：time_series[] + summary + 按供应商/错误类型的 breakdown。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type errorsTrendDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type errorsTrendHandlers struct{ db errorsTrendDB }

type errorsTrendPoint struct {
	Timestamp      time.Time      `json:"timestamp"`
	ErrorCount     int            `json:"error_count"`
	UniqueRequests int            `json:"unique_requests"`
	AffectedUsers  int            `json:"affected_users"`
	BySupplier     map[string]int `json:"by_supplier,omitempty"`
	ByErrorType    map[string]int `json:"by_error_type,omitempty"`
}

type errorsTrendBreakdownRow struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type errorsTrendSummary struct {
	TotalErrors     int                      `json:"total_errors"`
	UniqueRequests  int                      `json:"unique_requests"`
	TopErrorTypes   []errorsTrendBreakdownRow `json:"top_error_types"`
	TopSuppliers    []errorsTrendBreakdownRow `json:"top_suppliers"`
	AffectedCreds   int                      `json:"affected_credentials"`
}

type errorsTrendResponse struct {
	Source      string             `json:"source"` // stats | fallback
	Granularity string             `json:"granularity"`
	Hours       int                `json:"hours"`
	Since       time.Time          `json:"since"`
	Until       time.Time          `json:"until"`
	TimeSeries  []errorsTrendPoint `json:"time_series"`
	Summary     errorsTrendSummary `json:"summary"`
}

func (h *errorsTrendHandlers) getErrorsTrend(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil {
		writeErrorWithCode(w, http.StatusServiceUnavailable, "db_not_configured", "database is not configured")
		return
	}
	q := r.URL.Query()
	hours, err := parseVendorErrorHours(q.Get("hours"))
	if err != nil {
		writeErrorWithCode(w, http.StatusBadRequest, "invalid_hours", err.Error())
		return
	}
	granularity := q.Get("granularity")
	if granularity == "" {
		switch {
		case hours <= 1:
			granularity = "minute"
		case hours <= 24:
			granularity = "hour"
		default:
			granularity = "day"
		}
	}
	if granularity != "minute" && granularity != "hour" && granularity != "day" {
		writeErrorWithCode(w, http.StatusBadRequest, "invalid_granularity", "granularity must be minute, hour, or day")
		return
	}

	var credentialID int64
	if raw := q.Get("credential_id"); raw != "" {
		credentialID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || credentialID <= 0 {
			writeErrorWithCode(w, http.StatusBadRequest, "invalid_credential_id", "credential_id must be a positive integer")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	until := time.Now()
	since := until.Add(-time.Duration(hours) * time.Hour)
	tenantID := EffectiveTenantIDAll(r)
	supplier := q.Get("supplier")
	errorType := q.Get("error_type")

	resp, err := h.loadFromStats(ctx, statsQuery{
		Granularity: granularity, Since: since, Until: until, TenantID: tenantID,
		Supplier: supplier, CredentialID: credentialID, ErrorType: errorType,
	})
	if err != nil {
		slog.Error("errors trend stats query failed", "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "trend_stats_query_failed", "failed to load error trend")
		return
	}
	if len(resp.TimeSeries) == 0 {
		// stats 尚无覆盖（聚合器刚部署 / 窗口过新）：直接对明细聚合兜底。
		resp, err = h.loadFromDetail(ctx, statsQuery{
			Granularity: granularity, Since: since, Until: until, TenantID: tenantID,
			Supplier: supplier, CredentialID: credentialID, ErrorType: errorType,
		})
		if err != nil {
			slog.Error("errors trend fallback query failed", "error", err)
			writeErrorWithCode(w, http.StatusInternalServerError, "trend_fallback_query_failed", "failed to load error trend")
			return
		}
	}
	resp.Granularity = granularity
	resp.Hours = hours
	resp.Since = since
	resp.Until = until
	writeJSON(w, http.StatusOK, resp)
}

// statsQuery 是趋势查询的过滤参数包。
type statsQuery struct {
	Granularity string
	Since, Until time.Time
	TenantID    string
	Supplier    string
	CredentialID int64
	ErrorType   string
}

// bucketInterval 把粒度词映射到 date_bin 间隔。
func bucketInterval(granularity string) string {
	switch granularity {
	case "minute":
		return "1 minute"
	case "hour":
		return "1 hour"
	default:
		return "1 day"
	}
}

func (h *errorsTrendHandlers) loadFromStats(ctx context.Context, q statsQuery) (*errorsTrendResponse, error) {
	rows, err := h.db.Query(ctx, `
		SELECT stat_time,
		       SUM(error_count)::int,
		       SUM(unique_requests)::int,
		       SUM(affected_users)::int,
		       jsonb_object_agg(supplier, error_count) FILTER (WHERE supplier <> ''),
		       jsonb_object_agg(error_type, error_count) FILTER (WHERE error_type <> '')
		FROM supplier_error_stats
		WHERE granularity = $1 AND stat_time >= $2 AND stat_time < $3
		  AND ($4 = '' OR $4 = 'all' OR supplier = $4)
		  AND ($5 = 0 OR credential_id = $5)
		  AND ($6 = '' OR $6 = 'all' OR error_type = $6)
		GROUP BY stat_time ORDER BY stat_time
	`, q.Granularity, q.Since, q.Until, q.Supplier, q.CredentialID, q.ErrorType)
	if err != nil {
		return nil, fmt.Errorf("query supplier_error_stats failed: %w", err)
	}
	defer rows.Close()
	resp := &errorsTrendResponse{Source: "stats", TimeSeries: []errorsTrendPoint{}}
	for rows.Next() {
		p, err := scanTrendPoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scan supplier_error_stats row failed: %w", err)
		}
		resp.TimeSeries = append(resp.TimeSeries, p)
		resp.Summary.TotalErrors += p.ErrorCount
		resp.Summary.UniqueRequests += p.UniqueRequests
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate supplier_error_stats failed: %w", err)
	}
	if err := h.loadBreakdowns(ctx, q, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// loadFromDetail 是 stats 空窗口时的明细兜底：直接对 supplier_errors_unified
// 按桶聚合（响应 source=fallback）。
func (h *errorsTrendHandlers) loadFromDetail(ctx context.Context, q statsQuery) (*errorsTrendResponse, error) {
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT date_bin('%s'::interval, occurred_at, '2000-01-01'::timestamptz) AS bucket,
		       COUNT(*)::int,
		       COUNT(DISTINCT request_id)::int,
		       SUM(affected_users)::int,
		       jsonb_object_agg(supplier, per_supplier) FILTER (WHERE supplier <> ''),
		       jsonb_object_agg(error_type, per_type) FILTER (WHERE error_type <> '')
		FROM (
		    SELECT occurred_at, supplier, error_type, request_id, affected_users,
		           1 AS per_supplier, 1 AS per_type
		    FROM supplier_errors_unified
		    WHERE occurred_at >= $1 AND occurred_at < $2
		      AND ($3 = '' OR $3 = 'all' OR supplier = $3)
		      AND ($4 = 0 OR credential_id = $4)
		      AND ($5 = '' OR $5 = 'all' OR error_type = $5)
		) d
		GROUP BY bucket ORDER BY bucket
	`, bucketInterval(q.Granularity)), q.Since, q.Until, q.Supplier, q.CredentialID, q.ErrorType)
	if err != nil {
		return nil, fmt.Errorf("query supplier_errors_unified failed: %w", err)
	}
	defer rows.Close()
	resp := &errorsTrendResponse{Source: "fallback", TimeSeries: []errorsTrendPoint{}}
	for rows.Next() {
		p, err := scanTrendPoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scan supplier_errors_unified row failed: %w", err)
		}
		resp.TimeSeries = append(resp.TimeSeries, p)
		resp.Summary.TotalErrors += p.ErrorCount
		resp.Summary.UniqueRequests += p.UniqueRequests
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate supplier_errors_unified failed: %w", err)
	}
	if err := h.loadBreakdowns(ctx, q, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// loadBreakdowns 填充 summary 的 top 错误类型 / top 供应商 / 受影响凭据数。
func (h *errorsTrendHandlers) loadBreakdowns(ctx context.Context, q statsQuery, resp *errorsTrendResponse) error {
	rows, err := h.db.Query(ctx, `
		SELECT 'type' AS kind, COALESCE(NULLIF(error_type, ''), 'unknown') AS key,
		       COUNT(*)::int AS n
		FROM supplier_errors_unified
		WHERE occurred_at >= $1 AND occurred_at < $2
		  AND ($3 = '' OR $3 = 'all' OR supplier = $3)
		  AND ($4 = 0 OR credential_id = $4)
		  AND ($5 = '' OR $5 = 'all' OR error_type = $5)
		GROUP BY key
		UNION ALL
		SELECT 'supplier', COALESCE(NULLIF(supplier, ''), 'unknown'), COUNT(*)::int
		FROM supplier_errors_unified
		WHERE occurred_at >= $1 AND occurred_at < $2
		  AND ($3 = '' OR $3 = 'all' OR supplier = $3)
		  AND ($4 = 0 OR credential_id = $4)
		  AND ($5 = '' OR $5 = 'all' OR error_type = $5)
		GROUP BY supplier
		ORDER BY kind, n DESC
	`, q.Since, q.Until, q.Supplier, q.CredentialID, q.ErrorType)
	if err != nil {
		return fmt.Errorf("query trend breakdowns failed: %w", err)
	}
	defer rows.Close()
	resp.Summary.TopErrorTypes = []errorsTrendBreakdownRow{}
	resp.Summary.TopSuppliers = []errorsTrendBreakdownRow{}
	for rows.Next() {
		var kind, key string
		var n int
		if err := rows.Scan(&kind, &key, &n); err != nil {
			return fmt.Errorf("scan trend breakdown row failed: %w", err)
		}
		row := errorsTrendBreakdownRow{Key: key, Count: n}
		switch kind {
		case "type":
			if len(resp.Summary.TopErrorTypes) < 10 {
				resp.Summary.TopErrorTypes = append(resp.Summary.TopErrorTypes, row)
			}
		case "supplier":
			if len(resp.Summary.TopSuppliers) < 10 {
				resp.Summary.TopSuppliers = append(resp.Summary.TopSuppliers, row)
			}
		}
	}
	return rows.Err()
}

// scanTrendPoint 把一行趋势桶扫描为数据点。jsonb 聚合列经 []byte 中转
// （pgx jsonb → []byte 直扫稳定，map 目标在部分驱动路径不受支持）。
func scanTrendPoint(rows pgx.Rows) (errorsTrendPoint, error) {
	var p errorsTrendPoint
	var bySupplier, byErrorType []byte
	if err := rows.Scan(&p.Timestamp, &p.ErrorCount, &p.UniqueRequests, &p.AffectedUsers,
		&bySupplier, &byErrorType); err != nil {
		return p, err
	}
	if len(bySupplier) > 0 {
		_ = json.Unmarshal(bySupplier, &p.BySupplier)
	}
	if len(byErrorType) > 0 {
		_ = json.Unmarshal(byErrorType, &p.ByErrorType)
	}
	return p, nil
}
