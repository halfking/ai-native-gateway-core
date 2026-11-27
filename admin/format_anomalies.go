package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/internal/jsoncol"
)

type FormatAnomalyRecord struct {
	ID              int64          `json:"id"`
	DetectedAt      time.Time      `json:"detected_at"`
	RequestID       string         `json:"request_id"`
	ProviderID      *int           `json:"provider_id,omitempty"`
	ProviderCode    *string        `json:"provider_code,omitempty"`
	ClientModel     *string        `json:"client_model,omitempty"`
	OutboundModel   *string        `json:"outbound_model,omitempty"`
	AnomalyType     string         `json:"anomaly_type"`
	Severity        string         `json:"severity"`
	UsageSource     *string        `json:"usage_source,omitempty"`
	ExpectedTokens  *int           `json:"expected_tokens,omitempty"`
	ActualTokens    *int           `json:"actual_tokens,omitempty"`
	ContentSize     *int           `json:"content_size_bytes,omitempty"`
	Structure       map[string]any `json:"response_structure,omitempty"`
	ResponseSample  *string        `json:"response_sample,omitempty"`
	Resolved        bool           `json:"resolved"`
	ResolvedAt      *time.Time     `json:"resolved_at,omitempty"`
	ResolutionNotes *string        `json:"resolution_notes,omitempty"`
	TenantID        *string        `json:"tenant_id,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type FormatAnomalySummary struct {
	Hour            time.Time `json:"hour"`
	ProviderCode    *string   `json:"provider_code,omitempty"`
	ClientModel     *string   `json:"client_model,omitempty"`
	AnomalyType     string    `json:"anomaly_type"`
	Severity        string    `json:"severity"`
	AnomalyCount    int       `json:"anomaly_count"`
	AffectedReqs    int       `json:"affected_requests"`
	AvgContentSize  *float64  `json:"avg_content_size,omitempty"`
	AvgExpTokens    *float64  `json:"avg_expected_tokens,omitempty"`
	AvgActualTokens *float64  `json:"avg_actual_tokens,omitempty"`
	ResolvedCount   int       `json:"resolved_count"`
}

func (h *Handler) handleFormatAnomalies(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 批 157 (2026-10-16): tenant isolation. super_admin/admin_key → ""
	// (bypass RLS, see all); tenant_admin → own tenantID (RLS policy
	// filters to that tenant). The three read helpers below dispatch
	// via withTenantOrAllReadOnlyTx.
	tenantID := formatAnomalyTenantID(r)

	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	providerCode := strings.TrimSpace(queryString(r, "provider"))
	clientModel := strings.TrimSpace(queryString(r, "model"))
	anomalyType := strings.TrimSpace(queryString(r, "anomaly_type"))
	unresolvedOnly := queryBool(r, "unresolved_only")

	where := []string{"1=1"}
	args := []any{}
	argPos := 1
	if providerCode != "" {
		where = append(where, "COALESCE(rfa.provider_code, p.code) = $"+strconv.Itoa(argPos))
		args = append(args, providerCode)
		argPos++
	}
	if clientModel != "" {
		where = append(where, "rfa.client_model = $"+strconv.Itoa(argPos))
		args = append(args, clientModel)
		argPos++
	}
	if anomalyType != "" {
		where = append(where, "rfa.anomaly_type = $"+strconv.Itoa(argPos))
		args = append(args, anomalyType)
		argPos++
	}
	if unresolvedOnly {
		where = append(where, "NOT rfa.resolved")
	}
	whereSQL := strings.Join(where, " AND ")

	countQuery := `
		SELECT COUNT(*)
		FROM response_format_anomalies rfa
		LEFT JOIN providers p ON p.id = rfa.provider_id
		WHERE ` + whereSQL
	var total int
	if err := withTenantOrAllReadOnlyTx(r.Context(), h.db, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), countQuery, args...).Scan(&total)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "count query failed")
		return
	}

	listQuery := `
		SELECT
			rfa.id,
			rfa.detected_at,
			rfa.request_id,
			rfa.provider_id,
			COALESCE(rfa.provider_code, p.code) AS provider_code,
			rfa.client_model,
			rfa.outbound_model,
			rfa.anomaly_type,
			rfa.severity,
			rfa.usage_source,
			rfa.expected_tokens,
			rfa.actual_tokens,
			rfa.content_size_bytes,
			rfa.response_structure,
			rfa.response_sample,
			rfa.resolved,
			rfa.resolved_at,
			rfa.resolution_notes,
			rfa.tenant_id,
			rfa.created_at
		FROM response_format_anomalies rfa
		LEFT JOIN providers p ON p.id = rfa.provider_id
		WHERE ` + whereSQL + `
		ORDER BY rfa.detected_at DESC
		LIMIT $` + strconv.Itoa(argPos) + ` OFFSET $` + strconv.Itoa(argPos+1)
	args = append(args, limit, offset)

	items := make([]FormatAnomalyRecord, 0, limit)
	if err := withTenantOrAllReadOnlyTx(r.Context(), h.db, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), listQuery, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var item FormatAnomalyRecord
			var structureRaw []byte
			if err := rows.Scan(
				&item.ID,
				&item.DetectedAt,
				&item.RequestID,
				&item.ProviderID,
				&item.ProviderCode,
				&item.ClientModel,
				&item.OutboundModel,
				&item.AnomalyType,
				&item.Severity,
				&item.UsageSource,
				&item.ExpectedTokens,
				&item.ActualTokens,
				&item.ContentSize,
				&structureRaw,
				&item.ResponseSample,
				&item.Resolved,
				&item.ResolvedAt,
				&item.ResolutionNotes,
				&item.TenantID,
				&item.CreatedAt,
			); err != nil {
				return err
			}
			jsoncol.Decode("admin.formatAnomalies.list/structure", structureRaw, &item.Structure)
			items = append(items, item)
		}
		return rows.Err()
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "list query failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"anomalies": items,
		"count":     total,
		"limit":     limit,
		"offset":    offset,
	})
}

func (h *Handler) handleFormatAnomalySummary(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 批 157 (2026-10-16): tenant isolation, parallel to handleFormatAnomalies.
	tenantID := formatAnomalyTenantID(r)

	hours := queryInt(r, "hours", 24)
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}

	summaries := make([]FormatAnomalySummary, 0)
	if err := withTenantOrAllReadOnlyTx(r.Context(), h.db, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT
				DATE_TRUNC('hour', detected_at) AS hour,
				provider_code,
				client_model,
				anomaly_type,
				severity,
				COUNT(*) AS anomaly_count,
				COUNT(DISTINCT request_id) AS affected_requests,
				AVG(content_size_bytes) AS avg_content_size,
				AVG(expected_tokens) AS avg_expected_tokens,
				AVG(actual_tokens) AS avg_actual_tokens,
				COUNT(*) FILTER (WHERE resolved) AS resolved_count
			FROM response_format_anomalies
			WHERE detected_at > NOW() - ($1::int * INTERVAL '1 hour')
			GROUP BY 1, 2, 3, 4, 5
			ORDER BY hour DESC, anomaly_count DESC
			LIMIT 200
		`, hours)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item FormatAnomalySummary
			if err := rows.Scan(
				&item.Hour,
				&item.ProviderCode,
				&item.ClientModel,
				&item.AnomalyType,
				&item.Severity,
				&item.AnomalyCount,
				&item.AffectedReqs,
				&item.AvgContentSize,
				&item.AvgExpTokens,
				&item.AvgActualTokens,
				&item.ResolvedCount,
			); err != nil {
				return err
			}
			summaries = append(summaries, item)
		}
		return rows.Err()
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "summary query failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summaries": summaries,
		"count":     len(summaries),
		"hours":     hours,
	})
}

// FormatAnomalyCounts 徽标计数（2026-11-20）。
// Unresolved 是**全表**未解决数（tenant RLS 内），不受列表过滤/分页影响——
// 与 request-anomalies/count 的 Counts.Unresolved 同语义，两个数据源相加
// 即菜单徽标的总数（useFormatAnomalyBadge 双源合取）。
type FormatAnomalyCounts struct {
	Unresolved int `json:"unresolved"`
	NewToday   int `json:"new_today"`
}

// handleFormatAnomalyCount GET /api/admin/format-anomalies/count。
// NewToday 按 detected_at 的**进程本地时区**日界统计，与 reqprobe.Counts
// 的 new_today（Today() = Local 时区 YYYY-MM-DD）口径一致。
func (h *Handler) handleFormatAnomalyCount(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenantID := formatAnomalyTenantID(r)
	// NewToday 的日界与 reqprobe.Today() 同口径：进程本地时区的今天零点。
	y, m, d := time.Now().Local().Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, time.Local).UTC()
	var counts FormatAnomalyCounts
	if err := withTenantOrAllReadOnlyTx(r.Context(), h.db, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT
				COUNT(*) FILTER (WHERE NOT resolved),
				COUNT(*) FILTER (WHERE NOT resolved AND detected_at >= $1)
			FROM response_format_anomalies
		`, dayStart).Scan(&counts.Unresolved, &counts.NewToday)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "count query failed")
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

func (h *Handler) handleFormatAnomalySubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/format-anomalies/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// 2026-11-20: /format-anomalies/count —— 响应格式异常的徽标计数端点。
	// 此前菜单「格式异常监控」徽标只读 request-anomalies/count（请求侧），
	// 与页面主体的响应格式异常表是两个数据源 ⇒ 数字对不上（owner 报障）。
	if len(parts) == 1 && parts[0] == "count" {
		h.handleFormatAnomalyCount(w, r)
		return
	}
	if len(parts) != 2 || parts[1] != "resolve" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 批 157 (2026-10-16): the resolve write path stays super_admin-only.
	// tenant_admin has read-only access to the anomalies via RLS, but
	// cannot mark them resolved. The middleware accepts both roles; this
	// gate enforces the write restriction at the subrouter. Checked
	// before the db-nil guard so a misconfigured deployment doesn't
	// leak 503 to a role we already want to reject with 403.
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "super_admin role required to resolve format anomalies")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid anomaly id")
		return
	}
	var body struct {
		ResolutionNotes string `json:"resolution_notes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	var ct pgconn.CommandTag
	if err := withAllTenantTx(r.Context(), h.db, func(tx pgx.Tx) error {
		var err error
		ct, err = tx.Exec(r.Context(), `
			UPDATE response_format_anomalies
			SET resolved = TRUE,
			    resolved_at = NOW(),
			    resolution_notes = $2
			WHERE id = $1
			  AND ($3 = '' OR tenant_id = $3)
		`, id, body.ResolutionNotes, formatAnomalyTenantID(r))
		return err
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "anomaly not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "anomaly marked as resolved",
	})
}
