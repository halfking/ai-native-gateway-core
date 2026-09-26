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
	"errors"
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

// NewSessionDetailV2APIWithDB 是 newSessionDetailV2APIWithDB 的导出别名，
// 供跨包单测（tests/session_identity_contract/...）注入 pgxmock。
// 与 NewSessionDetailV2API 的 *pgxpool.Pool 接受范围等价，仅多了一步
// 接口收缩，便于 pgxmock / 自实现 Store 接入。
func NewSessionDetailV2APIWithDB(pool sessionDetailV2DB) *SessionDetailV2API {
	return newSessionDetailV2APIWithDB(pool)
}

// SessionV2 表示 public.sessions 表的记录
type SessionV2 struct {
	// ID 是 sessions.id 数值主键（SessionPK，contract_freeze §1.5 对客户端
	// 不可见）。Scan 仍需要该字段，但 JSON 序列化必须跳过 —— 12h 审计 F-2：
	// 旧 tag "id" 把 SessionPK 带进每个详情响应，与 §1.5 相悖。
	ID                  int64      `json:"-"`
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
	// ID 是 session_turns.id 数值主键（turn PK）。与 SessionPK（§1.5）
	// 同理不对客户端可见——轮次对外恒以 turn_no 定位（web TS 类型
	// TurnDetail/TurnListItem 与脚本消费面均不读该字段，2026-09-26 R69
	// 12h 审计 N-1 复核）。Scan 仍需要该列，JSON 序列化剔除。
	ID                     int64     `json:"-"`
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

	// IDKind 显式标注本响应对应的身份契约类别（contract_freeze §1）。
	// V2 会话详情以 session_id（sessions.session_id）为主键 —— request_id /
	// attempt_id / gw_session_id / SessionPK 各自有独立用途，互不替代。
	IDKind     string `json:"id_kind"`
	PrimaryKey string `json:"primary_key"`
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
		// 兼容旧客户端：部分调用方仍按 V1 习惯传 gw_session_id（request_logs.gw_session_id）。
		// 通过 resolveSessionID 反向解析到 sessions.session_id；两侧都为空则按 bad request 处理。
		sessionID = r.URL.Query().Get("gw_session_id")
	}
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

	// 把客户端传入的标识（session_id 或 gw_session_id）解析为 V2 服务端
	// session_id；解析失败按 404 处理，不暴露内部错误细节。
	resolvedSessionID, err := api.resolveSessionID(ctx, sessionID, tenantID)
	if err != nil {
		if err == errSessionNotFound {
			writeExportJSONError(w, http.StatusNotFound, "session not found")
			return
		}
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("resolve session id: %v", err))
		return
	}

	detail, err := api.querySessionDetail(ctx, resolvedSessionID, tenantID, limit, offset)
	if err != nil {
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", err))
		return
	}

	if detail.Session == nil {
		writeExportJSONError(w, http.StatusNotFound, "session not found")
		return
	}

	// contract_freeze §1.6：V2 详情的主键恒为 session_id，绝不暴露 SessionPK。
	detail.IDKind = "session_id"
	detail.PrimaryKey = resolvedSessionID

	response := map[string]any{
		"session":       detail.Session,
		"turns":         detail.Turns,
		"total_turns":   detail.TotalTurns,
		"focus_turn_no": focusTurnNo,
		"limit":         limit,
		"offset":        offset,
		"id_kind":       detail.IDKind,
		"primary_key":   detail.PrimaryKey,
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

// resolveSessionID 把客户端传入的标识解析为 V2 服务端 session_id。
//
// 5 类 ID 互不替代（contract_freeze §1.6）：session_id 是 sessions 表的
// 文本 surrogate，gw_session_id 是客户端逻辑文本标识 —— 两者字面值可能重合
// 也可能不同。本 helper 必须**仅**返回 session_id，绝不暴露 SessionPK（数值
// 主键对客户端不可见）。策略：
//
//  1. 若 input 已等于某行 sessions.session_id（同租户） → 直接返回。
//  2. 否则尝试经 request_logs.gw_session_id → sessions.primary_request_id
//     的反向映射；只允许唯一解析（命中多行返回 error，禁止把 gw_session_id
//     隐式覆盖多个会话）。
//  3. 两步都查不到 → 返回 errSessionNotFound，让上游按 404 处理。
//
// RLS：所有查询都带 tenant_id 谓词；不绕过 sessions / request_logs 的现有
// RLS 策略。sessionDetailV2DB 接口未暴露 Exec/QueryRow with bypass_rls，
// 因此本 helper 无法也无法绕过租户隔离。
func (api *SessionDetailV2API) resolveSessionID(
	ctx context.Context,
	input, tenantID string,
) (string, error) {
	if input == "" {
		return "", fmt.Errorf("empty session identifier")
	}
	if tenantID == "" {
		return "", fmt.Errorf("empty tenant_id")
	}

	// 1. 直接命中 sessions.session_id（最常见路径：V2 writer 直接写入
	//    sessions.session_id := gw_session_id）。
	var directHit string
	err := api.pool.QueryRow(ctx, `
		SELECT session_id FROM public.sessions
		WHERE session_id = $1 AND tenant_id = $2
		LIMIT 1
	`, input, tenantID).Scan(&directHit)
	if err == nil {
		return directHit, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("resolveSessionID direct: %w", err)
	}

	// 2. 反向映射：request_logs.gw_session_id → sessions.primary_request_id →
	//    session_id。仅在 direct miss 时执行。取全部 DISTINCT session_id：
	//    恰好 1 个才返回；0 个按未找到处理；>1 个说明该 gw_session_id 跨
	//    多个会话 —— 显式拒绝。12h 审计 F-1：初版注释承诺"多命中返回
	//    error"但实现是 ORDER BY ts LIMIT 1 静默取最早请求的会话，既违背
	//    承诺，也可能在 primary_request_id 恰非最早请求时漏配。
	rows, err := api.pool.Query(ctx, `
		SELECT DISTINCT s.session_id
		FROM public.sessions s
		WHERE s.tenant_id = $1
		  AND s.primary_request_id IN (
		      SELECT request_id FROM request_logs
		      WHERE tenant_id = $1 AND gw_session_id = $2
		  )
	`, tenantID, input)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errSessionNotFound
		}
		return "", fmt.Errorf("resolveSessionID reverse: %w", err)
	}
	defer rows.Close()
	candidates := make([]string, 0, 1)
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return "", fmt.Errorf("resolveSessionID reverse scan: %w", err)
		}
		candidates = append(candidates, sid)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("resolveSessionID reverse rows: %w", err)
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", errSessionNotFound
	default:
		return "", fmt.Errorf(
			"gw_session_id %q maps to %d sessions (tenant %s); refusing ambiguous resolution",
			input, len(candidates), tenantID)
	}
}

// errSessionNotFound 由 resolveSessionID 返回，调用方按 404 处理。
var errSessionNotFound = errors.New("session not found")
