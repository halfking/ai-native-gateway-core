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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionDetailV2DB 是 SessionDetailV2API 实际需要的数据库方法子集。
//
// 为什么不用 *pgxpool.Pool：pgxpool.Pool 字段类型是具体的 struct，无法
// 在单元测试中传入 pgxmock.PgxPoolIface；定义一个最小接口让 pgxmock 直接
// 满足即可（*pgxpool.Pool 也隐式满足本接口，因为它的方法集覆盖）。
//
// 注意：这里只暴露 SessionDetailV2API 实际调用的方法。增加新方法时
// 必须同步更新接口，否则编译失败 —— 这是有意设计的接口收缩，避免
// 详情页 API 偷偷用上 Exec/SendBatch 之类的副作用路径。
type sessionDetailV2DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// SessionDetailV2API 提供会话详情查询端点
type SessionDetailV2API struct {
	pool sessionDetailV2DB
}

// NewSessionDetailV2API 构造函数
func NewSessionDetailV2API(pool *pgxpool.Pool) *SessionDetailV2API {
	return &SessionDetailV2API{pool: pool}
}

// newSessionDetailV2APIWithDB 允许测试传入 pgxmock 池（pgxmock.PgxPoolIface
// 已实现 sessionDetailV2DB 接口的全部方法）。
func newSessionDetailV2APIWithDB(pool sessionDetailV2DB) *SessionDetailV2API {
	return &SessionDetailV2API{pool: pool}
}

// SessionV2 表示 public.sessions 表的记录
type SessionV2 struct {
	ID                  int64      `json:"id"`
	SessionID           string     `json:"session_id"`
	TenantID            string     `json:"tenant_id"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	ClosedAt            *time.Time `json:"closed_at,omitempty"`
	Status              string     `json:"status"`
	TotalTurns          int        `json:"total_turns"`
	TotalTokens         int        `json:"total_tokens"`
	TotalCostUSD        float64    `json:"total_cost_usd"`
	LastTurnNo          *int       `json:"last_turn_no,omitempty"`
	LastRequestSummary  *string    `json:"last_request_summary,omitempty"`
	LastResponseSummary *string    `json:"last_response_summary,omitempty"`
	LastModel           *string    `json:"last_model,omitempty"`
	LastProvider        *string    `json:"last_provider,omitempty"`
	TaskType            *string    `json:"task_type,omitempty"`
	ClientType          *string    `json:"client_type,omitempty"`
	Topic               *string    `json:"topic,omitempty"`
	Intent              *string    `json:"intent,omitempty"`
	PrimaryRequestID    *string    `json:"primary_request_id,omitempty"`
	TurnLogsSummary     any        `json:"turn_logs_summary,omitempty"`

	// SessionAnalysis 是 migration 567 的 session_analysis_metadata 读侧投影
	// （LEFT JOIN LATERAL 命中时非空）。见 session_meta_view.go 的字段定义与
	// payload 优先级规则。
	SessionAnalysis *SessionAnalysisView `json:"session_analysis,omitempty"`
}

// SessionTurnV2 表示 session_turns + session_bodies 的 JOIN 结果
type SessionTurnV2 struct {
	ID                     int64     `json:"id"`
	SessionID              string    `json:"session_id"`
	TurnNo                 int       `json:"turn_no"`
	TenantID               string    `json:"tenant_id"`
	RequestID              string    `json:"request_id"`
	Ts                     time.Time `json:"ts"`
	SubmitMode             string    `json:"submit_mode"`
	CompressionApplied     bool      `json:"compression_applied"`
	CompressionStrategy    *string   `json:"compression_strategy,omitempty"`
	CompressionMeta        any       `json:"compression_meta,omitempty"`
	CompressionTokensSaved *int      `json:"compression_tokens_saved,omitempty"`
	InjectionVerdict       string    `json:"injection_verdict"`
	OutputVerdict          string    `json:"output_verdict"`
	Model                  *string   `json:"model,omitempty"`
	Provider               *string   `json:"provider,omitempty"`
	CredentialID           *string   `json:"credential_id,omitempty"`
	PromptTokens           *int      `json:"prompt_tokens,omitempty"`
	CompletionTokens       *int      `json:"completion_tokens,omitempty"`
	CacheReadTokens        *int      `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens       *int      `json:"cache_write_tokens,omitempty"`
	CostUSD                *float64  `json:"cost_usd,omitempty"`
	LatencyMs              *int      `json:"latency_ms,omitempty"`
	StatusCode             *int      `json:"status_code,omitempty"`
	Success                *bool     `json:"success,omitempty"`
	ErrorKind              *string   `json:"error_kind,omitempty"`
	SourceKind             string    `json:"source_kind"`
	Quality                string    `json:"quality"`

	// From session_bodies table
	RequestDelta        any `json:"request_delta,omitempty"`
	ResponseDelta       any `json:"response_delta,omitempty"`
	OutboundBody        any `json:"outbound_body,omitempty"`
	RequestAttachments  any `json:"request_attachments,omitempty"`
	ResponseAttachments any `json:"response_attachments,omitempty"`
}

// SessionDetailV2Response 是 API 返回的完整响应
type SessionDetailV2Response struct {
	Session    *SessionV2      `json:"session"`
	Turns      []SessionTurnV2 `json:"turns"`
	TotalTurns int             `json:"total_turns"`
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

	// Resolve tenant from the authenticated scope. Non-platform roles are
	// pinned to their own tenant; only super/admin-key callers may select one.
	// This standalone handler is also callable directly in tests and by custom
	// muxes, so do not let GetTenantID's legacy default hide missing identity.
	if GetAuthContext(r) == nil {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	if tenantID == "" {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
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
		"session":       detail.Session,
		"turns":         detail.Turns,
		"total_turns":   detail.TotalTurns,
		"focus_turn_no": focusTurnNo,
		"limit":         limit,
		"offset":        offset,
	}

	writeExportJSON(w, http.StatusOK, response)
}

func (api *SessionDetailV2API) querySessionDetail(
	ctx context.Context,
	sessionID, tenantID string,
	limit, offset int,
) (*SessionDetailV2Response, error) {
	// 1. Query session metadata from public.sessions
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
	// 2026-08-26: LEFT JOIN LATERAL session_analysis_metadata（migration 567）
	// 暴露 SessionAnalysisView。沿用 session_meta_view.go 的 status='final'
	// 优先 + updated_at DESC 排序规则，保证 detail 页与列表页读到的同一份
	// payload 来自同一行。
	query := `
			SELECT
				id, session_id, tenant_id, created_at, updated_at, closed_at, status,
				total_turns, total_tokens, total_cost_usd,
				last_turn_no, last_request_summary, last_response_summary,
				last_model, last_provider,
				task_type, client_type, topic, intent,
				primary_request_id, turn_logs_summary,
				sam.status, sam.schema_version, sam.input_hash,
				sam.source_task_id, sam.updated_at, sam.payload
			FROM public.sessions s
			` + sessionAnalysisJoinSQL() + `
			WHERE s.session_id = $1 AND s.tenant_id = $2
			ORDER BY s.partition_date DESC
			LIMIT 1
	`

	var s SessionV2
	var turnLogsSummaryRaw []byte
	// session_analysis_metadata 来自 LEFT JOIN LATERAL, JOIN miss 时为 SQL NULL,
	// 必须用 *string 接收 (参见 turns 列表 / snapshot 同类修复)。
	var (
		saStatus, saSchemaVersion, saInputHash *string
		saSourceTaskID                         *string
		saUpdatedAt                            *time.Time
		saPayloadRaw                           []byte
	)
	err := api.pool.QueryRow(ctx, query, sessionID, tenantID).Scan(
		&s.ID, &s.SessionID, &s.TenantID, &s.CreatedAt, &s.UpdatedAt, &s.ClosedAt, &s.Status,
		&s.TotalTurns, &s.TotalTokens, &s.TotalCostUSD,
		&s.LastTurnNo, &s.LastRequestSummary, &s.LastResponseSummary,
		&s.LastModel, &s.LastProvider,
		&s.TaskType, &s.ClientType, &s.Topic, &s.Intent,
		&s.PrimaryRequestID, &turnLogsSummaryRaw,
		&saStatus, &saSchemaVersion, &saInputHash, &saSourceTaskID, &saUpdatedAt, &saPayloadRaw,
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

	// LEFT JOIN LATERAL 命中 → 填充 SessionAnalysisView。
	saStatusVal, saSchemaVal, saHashVal := "", "", ""
	if saStatus != nil {
		saStatusVal = *saStatus
	}
	if saSchemaVersion != nil {
		saSchemaVal = *saSchemaVersion
	}
	if saInputHash != nil {
		saHashVal = *saInputHash
	}
	if saStatusVal != "" {
		var view SessionAnalysisView
		scanSessionAnalysis(&view, saStatusVal, saSchemaVal, saHashVal, saSourceTaskID, saUpdatedAt, saPayloadRaw)
		s.SessionAnalysis = &view
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
		FROM public.session_turns_with_current_month t
		LEFT JOIN public.session_bodies_unified b
			ON t.tenant_id = b.tenant_id
			AND t.session_id = b.session_id
			AND t.turn_no = b.turn_no
			AND t.request_id = b.request_id
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

		// Parse JSONB fields. Decode failures are logged rather than silently
		// leaving the field null, which is indistinguishable from "not stored".
		t.CompressionMeta = decodeStoredJSON("compression_meta", t.RequestID, compressionMetaRaw)
		t.RequestDelta = decodeStoredJSON("request_delta", t.RequestID, requestDeltaRaw)
		t.ResponseDelta = decodeStoredJSON("response_delta", t.RequestID, responseDeltaRaw)
		t.OutboundBody = decodeStoredJSON("outbound_body", t.RequestID, outboundBodyRaw)
		t.RequestAttachments = decodeStoredJSON("request_attachments", t.RequestID, requestAttachmentsRaw)
		t.ResponseAttachments = decodeStoredJSON("response_attachments", t.RequestID, responseAttachmentsRaw)

		turns = append(turns, t)
	}

	return turns, rows.Err()
}
