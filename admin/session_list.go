package admin

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Session List & Summary API (v4, 2026-06-21) ───────────────────────
// GET /api/admin/sessions?tenant_id=default&page=1&size=20&q=&status=
// Returns aggregated sessions from request_logs grouped by gw_session_id.

type SessionSummary struct {
	SessionID    string `json:"session_id"`
	TenantID     string `json:"tenant_id"`
	MsgCount     int    `json:"msg_count"`
	RequestCount int    `json:"request_count"`
	TokenTotal   int    `json:"token_total"`
	ModelUsed    string `json:"model_used"`

	TimeStart string `json:"time_start"`
	TimeEnd   string `json:"time_end"`
	Duration  string `json:"duration"`

	IsCompressed bool   `json:"is_compressed"`
	CompressionStrategy string `json:"compression_strategy,omitempty"`

	FirstUserMsg string `json:"first_user_msg,omitempty"`
	LastResponse string `json:"last_response,omitempty"`
	ErrorCount   int    `json:"error_count"`
	SuccessRate  float64 `json:"success_rate"`
}

type SessionListResponse struct {
	Sessions []SessionSummary `json:"sessions"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	Size     int              `json:"size"`
	Pages    int              `json:"pages"`
}

// SessionListAPI handles session list endpoints.
type SessionListAPI struct {
	db *pgxpool.Pool
}

func NewSessionListAPI(db *pgxpool.Pool) *SessionListAPI {
	return &SessionListAPI{db: db}
}

// HandleList handles GET /api/admin/sessions
func (api *SessionListAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenantID := EffectiveTenantID(r)
	page := parseIntParam(r.URL.Query().Get("page"), 1)
	size := parseIntParam(r.URL.Query().Get("size"), 20)
	searchQ := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	hours := parseIntParam(r.URL.Query().Get("hours"), 72)

	if size < 1 || size > 100 {
		size = 20
	}
	if page < 1 {
		page = 1
	}
	if hours < 1 || hours > 8760 {
		hours = 72
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var resp *SessionListResponse
	err := withTenantTx(ctx, api.db, tenantID, func(tx pgx.Tx) error {
		var txErr error
		resp, txErr = api.loadSessions(ctx, tx, tenantID, page, size, searchQ, status, hours)
		return txErr
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status": "error",
			"message": "Failed to load sessions",
			"error":   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (api *SessionListAPI) loadSessions(
	ctx context.Context, q pgx.Tx, tenantID string,
	page, size int, searchQ, status string, hours int,
) (*SessionListResponse, error) {
	offset := (page - 1) * size

	// Count total distinct sessions
	countQuery := `
		SELECT COUNT(DISTINCT gw_session_id)
		FROM request_logs
		WHERE gw_session_id IS NOT NULL 
		  AND gw_session_id != ''
		  AND tenant_id = $1
		  AND ts >= NOW() - ($2 || ' hours')::interval
	`
	argsCount := []interface{}{tenantID, fmt.Sprintf("%d", hours)}
	if searchQ != "" {
		countQuery += ` AND gw_session_id ILIKE '%' || $3 || '%'`
		argsCount = append(argsCount, searchQ)
	}

	var total int
	if err := q.QueryRow(ctx, countQuery, argsCount...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count sessions: %w", err)
	}

	// Query session summaries using aggregation
	query := `
		SELECT 
			gw_session_id,
			COUNT(*) as request_count,
			COUNT(*) FILTER (WHERE NOT success) as error_count,
			COUNT(*) FILTER (WHERE compression_strategy IS NOT NULL AND compression_strategy != '') > 0 as is_compressed,
			MIN(ts) as time_start,
			MAX(ts) as time_end,
			MIN(client_model) as model_used
		FROM request_logs
		WHERE gw_session_id IS NOT NULL 
		  AND gw_session_id != ''
		  AND tenant_id = $1
		  AND ts >= NOW() - ($2 || ' hours')::interval
	`
	args := []interface{}{tenantID, fmt.Sprintf("%d", hours)}

	if searchQ != "" {
		query += ` AND gw_session_id ILIKE '%' || $3 || '%'`
		args = append(args, searchQ)
	}

	query += ` GROUP BY gw_session_id ORDER BY MAX(ts) DESC`

	// Get total before pagination
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", size, offset)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]SessionSummary, 0, size)
	for rows.Next() {
		var (
			sessionID                     string
			reqCount, errCount            int
			compressed                    bool
			startTime, endTime            time.Time
			model                         *string
		)
		if err := rows.Scan(&sessionID, &reqCount, &errCount, &compressed, &startTime, &endTime, &model); err != nil {
			continue
		}

		modelUsed := ""
		if model != nil {
			modelUsed = *model
		}

		successCount := reqCount - errCount
		successRate := 0.0
		if reqCount > 0 {
			successRate = float64(successCount) / float64(reqCount) * 100
		}

		duration := endTime.Sub(startTime)
		durStr := formatDuration(duration)

		sessions = append(sessions, SessionSummary{
			SessionID:     sessionID,
			TenantID:      tenantID,
			RequestCount:  reqCount,
			ErrorCount:    errCount,
			SuccessRate:   successRate,
			IsCompressed:  compressed,
			ModelUsed:     modelUsed,
			TimeStart:     startTime.Format("2006-01-02 15:04:05"),
			TimeEnd:       endTime.Format("2006-01-02 15:04:05"),
			Duration:      durStr,
		})
	}

	pages := (total + size - 1) / size

	return &SessionListResponse{
		Sessions: sessions,
		Total:    total,
		Page:     page,
		Size:     size,
		Pages:    pages,
	}, nil
}

func parseIntParam(s string, def int) int {
	if s == "" {
		return def
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return def
	}
	return v
}

func formatDuration(d time.Duration) string {
	// Audit P2 fix (2026-06-22): use round-to-nearest-day instead of
	// floor division so 23h59m -> "1d" (was "24h" because
	// 23.98/24 = 0.99 floor 0, falling through to hours branch).
	// Use math.Round to round to nearest whole day.
	if d.Hours() >= 24 {
		days := int(math.Round(d.Hours() / 24))
		if days < 1 {
			days = 1
		}
		return fmt.Sprintf("%dd", days)
	}
	if d.Hours() >= 1 {
		return fmt.Sprintf("%.0fh", d.Hours())
	}
	if d.Minutes() >= 1 {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// ── Session Detail API (v4, 2026-06-21) ────────────────────────────────
// GET /api/admin/sessions/:id/detail?tenant_id=default
// Returns detailed session info with request logs.

type SessionDetail struct {
	SessionSummary
	RequestLogs []RequestLogBrief `json:"request_logs"`
}

type RequestLogBrief struct {
	RequestID      string `json:"request_id"`
	Time           string `json:"time"`
	ClientModel    string `json:"client_model"`
	OutboundModel  string `json:"outbound_model"`
	Success        bool   `json:"success"`
	PromptTokens   int    `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens    int    `json:"total_tokens"`
	LatencyMs      int    `json:"latency_ms"`
	CompressionStrategy string `json:"compression_strategy,omitempty"`
}

// HandleDetail handles GET /api/admin/sessions/:id/detail
func (api *SessionListAPI) HandleDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.PathValue("id")
	tenantID := EffectiveTenantID(r)

	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "session_id is required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var detail *SessionDetail
	err := withTenantTx(ctx, api.db, tenantID, func(tx pgx.Tx) error {
		var txErr error
		detail, txErr = api.loadSessionDetail(ctx, tx, sessionID, tenantID)
		return txErr
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to load session detail",
			"error":   err.Error(),
		})
		return
	}
	if detail == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"status":    "error",
			"message":   "Session not found",
			"session_id": sessionID,
		})
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

func (api *SessionListAPI) loadSessionDetail(ctx context.Context, q pgx.Tx, sessionID, tenantID string) (*SessionDetail, error) {
	// Get session summary
	query := `
		SELECT 
			COUNT(*) as request_count,
			COUNT(*) FILTER (WHERE NOT success) as error_count,
			COUNT(*) FILTER (WHERE compression_strategy IS NOT NULL AND compression_strategy != '') > 0 as is_compressed,
			MAX(compression_strategy) as compression_strategy,
			MIN(ts) as time_start,
			MAX(ts) as time_end,
			MIN(client_model) as model_used
		FROM request_logs
		WHERE gw_session_id = $1 AND tenant_id = $2
	`

	var (
		reqCount, errCount int
		compressed         bool
		compressionStr     *string
		startTime, endTime time.Time
		model              *string
	)

	err := q.QueryRow(ctx, query, sessionID, tenantID).Scan(
		&reqCount, &errCount, &compressed,
		&compressionStr, &startTime, &endTime, &model,
	)
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}
	if reqCount == 0 {
		return nil, nil
	}

	modelUsed := ""
	if model != nil {
		modelUsed = *model
	}
	cs := ""
	if compressionStr != nil {
		cs = *compressionStr
	}

	successCount := reqCount - errCount
	successRate := 0.0
	if reqCount > 0 {
		successRate = float64(successCount) / float64(reqCount) * 100
	}

	summary := SessionSummary{
		SessionID:           sessionID,
		TenantID:            tenantID,
		RequestCount:        reqCount,
		ErrorCount:          errCount,
		SuccessRate:         successRate,
		IsCompressed:        compressed,
		CompressionStrategy: cs,
		ModelUsed:           modelUsed,
		TimeStart:           startTime.Format("2006-01-02 15:04:05"),
		TimeEnd:             endTime.Format("2006-01-02 15:04:05"),
		Duration:            formatDuration(endTime.Sub(startTime)),
	}

	// Get request logs
	logQuery := `
		SELECT request_id, ts, client_model, outbound_model, success,
		       prompt_tokens, completion_tokens, total_tokens, latency_ms,
		       compression_strategy
		FROM request_logs
		WHERE gw_session_id = $1 AND tenant_id = $2
		ORDER BY ts ASC
		LIMIT 500
	`

	rows, err := q.Query(ctx, logQuery, sessionID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query request logs: %w", err)
	}
	defer rows.Close()

	logs := make([]RequestLogBrief, 0)
	for rows.Next() {
		var (
			rid, cModel, oModel string
			ts                  time.Time
			ok                  bool
			pTokens, cTokens, totalTokens, lat int
			cs                  *string
		)
		if err := rows.Scan(&rid, &ts, &cModel, &oModel, &ok,
			&pTokens, &cTokens, &totalTokens, &lat, &cs); err != nil {
			continue
		}
		csStr := ""
		if cs != nil {
			csStr = *cs
		}
		logs = append(logs, RequestLogBrief{
			RequestID:      rid,
			Time:           ts.Format("2006-01-02 15:04:05"),
			ClientModel:    cModel,
			OutboundModel:  oModel,
			Success:        ok,
			PromptTokens:   pTokens,
			CompletionTokens: cTokens,
			TotalTokens:    totalTokens,
			LatencyMs:      lat,
			CompressionStrategy: csStr,
		})
	}

	return &SessionDetail{
		SessionSummary: summary,
		RequestLogs:    logs,
	}, nil
}
