// Package admin - session_detail_v2.go
//
// 会话详情 API v2 — 从 session_* 表查询会话的完整轮次数据
//
//   GET /api/admin/sessions/detail?session_id=xxx&tenant=xxx[&turn_no=N][&limit=20][&offset=0]
//
// 返回格式：
//   {
//     "session": { /* sessions表记录 */ },
//     "turns": [ /* session_turns + session_bodies JOIN结果 */ ],
//     "total_turns": 5
//   }
//
// 仅 super 用户可用。

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionDetailV2API 提供会话详情查询端点
type SessionDetailV2API struct {
	pool *pgxpool.Pool
}

// NewSessionDetailV2API 构造函数
func NewSessionDetailV2API(pool *pgxpool.Pool) *SessionDetailV2API {
	return &SessionDetailV2API{pool: pool}
}

// SessionV2 表示 gateway.sessions 表的记录
type SessionV2 struct {
	ID                   int64      `json:"id"`
	SessionID            string     `json:"session_id"`
	TenantID             string     `json:"tenant_id"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ClosedAt             *time.Time `json:"closed_at,omitempty"`
	Status               string     `json:"status"`
	TotalTurns           int        `json:"total_turns"`
	TotalTokens          int        `json:"total_tokens"`
	TotalCostUSD         float64    `json:"total_cost_usd"`
	LastTurnNo           *int       `json:"last_turn_no,omitempty"`
	LastRequestSummary   *string    `json:"last_request_summary,omitempty"`
	LastResponseSummary  *string    `json:"last_response_summary,omitempty"`
	LastModel            *string    `json:"last_model,omitempty"`
	LastProvider         *string    `json:"last_provider,omitempty"`
	TaskType             *string    `json:"task_type,omitempty"`
	ClientType           *string    `json:"client_type,omitempty"`
	Topic                *string    `json:"topic,omitempty"`
	Intent               *string    `json:"intent,omitempty"`
	PrimaryRequestID     *string    `json:"primary_request_id,omitempty"`
	TurnLogsSummary      any        `json:"turn_logs_summary,omitempty"`
}

// SessionTurnV2 表示 session_turns + session_bodies 的 JOIN 结果
type SessionTurnV2 struct {
	ID                    int64   `json:"id"`
	SessionID             string  `json:"session_id"`
	TurnNo                int     `json:"turn_no"`
	TenantID              string  `json:"tenant_id"`
	RequestID             string  `json:"request_id"`
	Ts                    time.Time `json:"ts"`
	SubmitMode            string  `json:"submit_mode"`
	CompressionApplied    bool    `json:"compression_applied"`
	CompressionStrategy   *string `json:"compression_strategy,omitempty"`
	CompressionMeta       any     `json:"compression_meta,omitempty"`
	CompressionTokensSaved *int   `json:"compression_tokens_saved,omitempty"`
	InjectionVerdict      string  `json:"injection_verdict"`
	OutputVerdict         string  `json:"output_verdict"`
	Model                 *string `json:"model,omitempty"`
	Provider              *string `json:"provider,omitempty"`
	CredentialID          *string `json:"credential_id,omitempty"`
	PromptTokens          *int    `json:"prompt_tokens,omitempty"`
	CompletionTokens      *int    `json:"completion_tokens,omitempty"`
	CacheReadTokens       *int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens      *int    `json:"cache_write_tokens,omitempty"`
	CostUSD               *float64 `json:"cost_usd,omitempty"`
	LatencyMs             *int    `json:"latency_ms,omitempty"`
	StatusCode            *int    `json:"status_code,omitempty"`
	Success               *bool   `json:"success,omitempty"`
	ErrorKind             *string `json:"error_kind,omitempty"`
	SourceKind            string  `json:"source_kind"`
	Quality               string  `json:"quality"`
	
	// From session_bodies table
	RequestDelta          any     `json:"request_delta,omitempty"`
	ResponseDelta         any     `json:"response_delta,omitempty"`
	OutboundBody          any     `json:"outbound_body,omitempty"`
	RequestAttachments    any     `json:"request_attachments,omitempty"`
	ResponseAttachments   any     `json:"response_attachments,omitempty"`
}

// SessionDetailV2Response 是 API 返回的完整响应
type SessionDetailV2Response struct {
	Session    *SessionV2       `json:"session"`
	Turns      []SessionTurnV2  `json:"turns"`
	TotalTurns int              `json:"total_turns"`
}

func (api *SessionDetailV2API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if api.pool == nil {
		writeExportJSONError(w, http.StatusServiceUnavailable, "session detail v2 API requires database")
		return
	}
	if r.URL.Path != "/api/admin/sessions/detail" {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeExportJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = "default"
	}

	// Optional: turn_no for highlighting/scrolling
	var focusTurnNo *int
	if tn := r.URL.Query().Get("turn_no"); tn != "" {
		if n, err := strconv.Atoi(tn); err == nil && n > 0 {
			focusTurnNo = &n
		}
	}

	// Pagination
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if s := r.URL.Query().Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			offset = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	detail, err := api.querySessionDetail(ctx, sessionID, tenantID, limit, offset)
	if err != nil {
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", err))
		return
	}

	if detail.Session == nil {
		writeExportJSONError(w, http.StatusNotFound, "session not found")
		return
	}

	response := map[string]any{
		"session":      detail.Session,
		"turns":        detail.Turns,
		"total_turns":  detail.TotalTurns,
		"focus_turn_no": focusTurnNo,
		"limit":        limit,
		"offset":       offset,
	}

	writeExportJSON(w, http.StatusOK, response)
}

func (api *SessionDetailV2API) querySessionDetail(
	ctx context.Context,
	sessionID, tenantID string,
	limit, offset int,
) (*SessionDetailV2Response, error) {
	// 1. Query session metadata from gateway.sessions
	session, err := api.querySession(ctx, sessionID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}
	if session == nil {
		return &SessionDetailV2Response{}, nil
	}

	// 2. Query turns with bodies (JOIN session_turns + session_bodies)
	turns, err := api.queryTurns(ctx, sessionID, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}

	return &SessionDetailV2Response{
		Session:    session,
		Turns:      turns,
		TotalTurns: session.TotalTurns,
	}, nil
}

func (api *SessionDetailV2API) querySession(
	ctx context.Context,
	sessionID, tenantID string,
) (*SessionV2, error) {
	query := `
		SELECT 
			id, session_id, tenant_id, created_at, updated_at, closed_at, status,
			total_turns, total_tokens, total_cost_usd,
			last_turn_no, last_request_summary, last_response_summary,
			last_model, last_provider,
			task_type, client_type, topic, intent,
			primary_request_id, turn_logs_summary
		FROM gateway.sessions
		WHERE session_id = $1 AND tenant_id = $2
		ORDER BY partition_date DESC
		LIMIT 1
	`

	var s SessionV2
	var turnLogsSummaryRaw []byte
	err := api.pool.QueryRow(ctx, query, sessionID, tenantID).Scan(
		&s.ID, &s.SessionID, &s.TenantID, &s.CreatedAt, &s.UpdatedAt, &s.ClosedAt, &s.Status,
		&s.TotalTurns, &s.TotalTokens, &s.TotalCostUSD,
		&s.LastTurnNo, &s.LastRequestSummary, &s.LastResponseSummary,
		&s.LastModel, &s.LastProvider,
		&s.TaskType, &s.ClientType, &s.Topic, &s.Intent,
		&s.PrimaryRequestID, &turnLogsSummaryRaw,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, err
	}

	// Parse turn_logs_summary JSONB
	if len(turnLogsSummaryRaw) > 0 {
		var summary any
		if err := json.Unmarshal(turnLogsSummaryRaw, &summary); err == nil {
			s.TurnLogsSummary = summary
		}
	}

	return &s, nil
}

func (api *SessionDetailV2API) queryTurns(
	ctx context.Context,
	sessionID, tenantID string,
	limit, offset int,
) ([]SessionTurnV2, error) {
	// JOIN session_turns with session_bodies on (session_id, turn_no)
	// Order by turn_no DESC (most recent first)
	query := `
		SELECT 
			t.id, t.session_id, t.turn_no, t.tenant_id, t.request_id, t.ts,
			t.submit_mode, t.compression_applied, t.compression_strategy, 
			t.compression_meta, t.compression_tokens_saved,
			t.injection_verdict, t.output_verdict,
			t.model, t.provider, t.credential_id,
			t.prompt_tokens, t.completion_tokens, t.cache_read_tokens, t.cache_write_tokens,
			t.cost_usd, t.latency_ms, t.status_code, t.success, t.error_kind,
			t.source_kind, t.quality,
			b.request_delta, b.response_delta, b.outbound_body,
			b.request_attachments, b.response_attachments
		FROM gateway.session_turns t
		LEFT JOIN gateway.session_bodies b 
			ON t.session_id = b.session_id 
			AND t.turn_no = b.turn_no
			AND t.partition_date = b.partition_date
		WHERE t.session_id = $1 AND t.tenant_id = $2
		ORDER BY t.turn_no DESC
		LIMIT $3 OFFSET $4
	`

	rows, err := api.pool.Query(ctx, query, sessionID, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []SessionTurnV2
	for rows.Next() {
		var t SessionTurnV2
		var compressionMetaRaw, requestDeltaRaw, responseDeltaRaw, outboundBodyRaw []byte
		var requestAttachmentsRaw, responseAttachmentsRaw []byte

		err := rows.Scan(
			&t.ID, &t.SessionID, &t.TurnNo, &t.TenantID, &t.RequestID, &t.Ts,
			&t.SubmitMode, &t.CompressionApplied, &t.CompressionStrategy,
			&compressionMetaRaw, &t.CompressionTokensSaved,
			&t.InjectionVerdict, &t.OutputVerdict,
			&t.Model, &t.Provider, &t.CredentialID,
			&t.PromptTokens, &t.CompletionTokens, &t.CacheReadTokens, &t.CacheWriteTokens,
			&t.CostUSD, &t.LatencyMs, &t.StatusCode, &t.Success, &t.ErrorKind,
			&t.SourceKind, &t.Quality,
			&requestDeltaRaw, &responseDeltaRaw, &outboundBodyRaw,
			&requestAttachmentsRaw, &responseAttachmentsRaw,
		)
		if err != nil {
			return nil, err
		}

		// Parse JSONB fields
		if len(compressionMetaRaw) > 0 {
			var meta any
			if err := json.Unmarshal(compressionMetaRaw, &meta); err == nil {
				t.CompressionMeta = meta
			}
		}
		if len(requestDeltaRaw) > 0 {
			var delta any
			if err := json.Unmarshal(requestDeltaRaw, &delta); err == nil {
				t.RequestDelta = delta
			}
		}
		if len(responseDeltaRaw) > 0 {
			var delta any
			if err := json.Unmarshal(responseDeltaRaw, &delta); err == nil {
				t.ResponseDelta = delta
			}
		}
		if len(outboundBodyRaw) > 0 {
			var body any
			if err := json.Unmarshal(outboundBodyRaw, &body); err == nil {
				t.OutboundBody = body
			}
		}
		if len(requestAttachmentsRaw) > 0 {
			var attachments any
			if err := json.Unmarshal(requestAttachmentsRaw, &attachments); err == nil {
				t.RequestAttachments = attachments
			}
		}
		if len(responseAttachmentsRaw) > 0 {
			var attachments any
			if err := json.Unmarshal(responseAttachmentsRaw, &attachments); err == nil {
				t.ResponseAttachments = attachments
			}
		}

		turns = append(turns, t)
	}

	return turns, rows.Err()
}
