package sessionforensics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrorDBUnavailable 当 DB 连接不可用时返回。
var ErrorDBUnavailable = errors.New("sessionforensics: db unavailable")

// Store is the minimal interface a sessionforensics client needs to read.
// *pgxpool.Pool satisfies it directly via PgxStore.
//
// The Query method returns a RowIterator so tests can implement mock rows
// without depending on github.com/jackc/pgx/v5.
type Store interface {
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Query(ctx context.Context, sql string, args ...any) (RowIterator, error)
}

// Row 是扫描单行的最简接口。
type Row interface {
	Scan(...any) error
}

// RowIterator 是多行结果集的最简接口。
type RowIterator interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// PgxStore is a thin adapter from *pgxpool.Pool to Store.
type PgxStore struct {
	Pool *pgxpool.Pool
}

func (s *PgxStore) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return s.Pool.QueryRow(ctx, sql, args...)
}
func (s *PgxStore) Query(ctx context.Context, sql string, args ...any) (RowIterator, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRowAdapter{rows: rows}, nil
}

// pgxRowAdapter 把 pgx.Rows 包成 RowIterator。
type pgxRowAdapter struct {
	rows pgx.Rows
}

func (r *pgxRowAdapter) Next() bool { return r.rows.Next() }
func (r *pgxRowAdapter) Scan(dest ...any) error {
	return r.rows.Scan(dest...)
}
func (r *pgxRowAdapter) Err() error   { return r.rows.Err() }
func (r *pgxRowAdapter) Close() error { r.rows.Close(); return nil }

// Exporter 从 PostgreSQL (or its mock) 拉取 sessions。
// 它与 admin/session_export.go 的 buildExport 共享同样的 SQL，但返回强
// 类型 *SessionPack，方便测试和序列化。
type Exporter struct {
	store Store
}

// NewExporter 以 *pgxpool.Pool 为参数创建 Exporter。
func NewExporter(pool *pgxpool.Pool) *Exporter {
	if pool == nil {
		return &Exporter{store: nil}
	}
	return &Exporter{store: &PgxStore{Pool: pool}}
}

// NewExporterWithStore 接收任意 Store（mock 测试用）。
func NewExporterWithStore(s Store) *Exporter {
	return &Exporter{store: s}
}

// ExportFromTx 是 ExportSession 的事务版本，admin/session_export.go 用
// withTenantTx 设置 RLS 后调用，避免重复 SQL。
//
//	tx 必须是已经设置过 tenant_id GUC 的 read-only tx（admin/tenant_ctx.go）。
func (e *Exporter) ExportFromTx(ctx context.Context, tx pgx.Tx, sessionID, tenantID string) (*SessionPack, error) {
	if e == nil || tx == nil {
		return nil, ErrorDBUnavailable
	}
	if sessionID == "" {
		return nil, fmt.Errorf("sessionforensics: missing session_id")
	}
	if tenantID == "" {
		tenantID = "default"
	}

	pack := &SessionPack{
		SessionMeta: SessionMeta{
			ID: sessionID, TenantID: tenantID, Source: "admin",
			ExportedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Messages:    []ExportMessage{},
		Attachments: []ExportAttachment{},
	}
	turn := 0
	seenAtt := map[string]struct{}{}

	rows, err := tx.Query(ctx, `
		SELECT
			rl.id::text, rl.role, rl.parent_request_id,
			rl.compression_reason, rl.compression_strategy, rl.compression_meta,
			rl.attachments, rl.ts,
			COALESCE(rb.request_body, ''::jsonb) AS request_body,
			COALESCE(rb.response_body, ''::jsonb) AS response_body,
			rl.client_model, rl.outbound_model
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
		WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2
		ORDER BY rl.ts ASC, rl.id ASC
	`, sessionID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}

	for rows.Next() {
		var (
			id, role, parentID, reason, strategy string
			compMeta, attachments                []byte
			createdAt                            time.Time
			reqBody, respBody                    *string
			clientModel, outboundModel           *string
		)
		if err := rows.Scan(&id, &role, &parentID, &reason, &strategy,
			&compMeta, &attachments, &createdAt, &reqBody, &respBody,
			&clientModel, &outboundModel); err != nil {
			slog.Warn("scan row failed", "session", sessionID, "err", err)
			continue
		}
		turn++
		msg := ExportMessage{
			Turn:                turn,
			Role:                role,
			ParentRequestID:     parentID,
			CompressionReason:   reason,
			CompressionStrategy: strategy,
			CreatedAt:           createdAt.UTC().Format(time.RFC3339),
		}
		if len(compMeta) > 0 {
			_ = json.Unmarshal(compMeta, &msg.CompressionMeta)
		}
		if respBody != nil && *respBody != "" {
			msg.Content = *respBody
		} else if reqBody != nil {
			msg.Content = *reqBody
		}
		pack.Messages = append(pack.Messages, msg)

		if len(attachments) > 0 {
			var atts []ExportAttachment
			if json.Unmarshal(attachments, &atts) == nil {
				for _, a := range atts {
					key := a.Path + "|" + a.Name
					if _, ok := seenAtt[key]; ok {
						continue
					}
					seenAtt[key] = struct{}{}
					pack.Attachments = append(pack.Attachments, a)
				}
			}
		}
		if clientModel != nil && pack.SessionMeta.Instance == "" {
			pack.SessionMeta.Instance = *clientModel
		}
		_ = outboundModel
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iter: %w", err)
	}

	// 查 title + summary（不依赖 RLS，可在事务外做，但这里仍在事务内安全）
	var title, summary *string
	if err := tx.QueryRow(ctx, `
		SELECT title, summary
		FROM session_summaries
		WHERE session_key = $1
		LIMIT 1
	`, sessionID).Scan(&title, &summary); err == nil {
		if title != nil {
			pack.SessionMeta.Title = *title
		}
		if summary != nil {
			pack.Summary = *summary
			pack.ResumeBrief.LastObjective = *summary
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("summaries lookup failed", "session", sessionID, "err", err)
	}

	if len(pack.Messages) == 0 && pack.Summary == "" && pack.SessionMeta.Title == "" {
		return nil, ErrSessionNotFound
	}
	return pack, nil
}

// ExportSession pulls a full session by ID and tenant. If tenantID is empty,
// "default" is used.
//
// Returns ErrorDBUnavailable when store is nil or DB is down.
// Returns ErrSessionNotFound when no rows exist for the session_id.
//
// 这个函数是 admin/session_export.go handleExport 的等价纯函数实现。
// admin 包可以复用本函数避免 SQL 重复。
func (e *Exporter) ExportSession(ctx context.Context, sessionID, tenantID string) (*SessionPack, error) {
	if e == nil || e.store == nil {
		return nil, ErrorDBUnavailable
	}
	if sessionID == "" {
		return nil, fmt.Errorf("sessionforensics: missing session_id")
	}
	if tenantID == "" {
		tenantID = "default"
	}

	pack := &SessionPack{
		SessionMeta: SessionMeta{
			ID: sessionID, TenantID: tenantID, Source: "local",
			ExportedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Messages:    []ExportMessage{},
		Attachments: []ExportAttachment{},
	}
	turn := 0

	rows, err := e.store.Query(ctx, `
		SELECT
			rl.id::text, rl.role, rl.parent_request_id,
			rl.compression_reason, rl.compression_strategy, rl.compression_meta,
			rl.attachments, rl.ts,
			COALESCE(rb.request_body, ''::jsonb) AS request_body,
			COALESCE(rb.response_body, ''::jsonb) AS response_body,
			rl.client_model, rl.outbound_model
 		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
		WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2
		ORDER BY rl.ts ASC, rl.id ASC
	`, sessionID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	seenAtt := map[string]struct{}{}
	for rows.Next() {
		var (
			id, role, parentID, reason, strategy string
			compMeta, attachments                []byte
			createdAt                            time.Time
			reqBody, respBody                    *string
			clientModel, outboundModel           *string
		)
		if err := rows.Scan(&id, &role, &parentID, &reason, &strategy,
			&compMeta, &attachments, &createdAt, &reqBody, &respBody,
			&clientModel, &outboundModel); err != nil {
			slog.Warn("scan row failed", "session", sessionID, "err", err)
			continue
		}
		turn++
		msg := ExportMessage{
			Turn:                turn,
			Role:                role,
			ParentRequestID:     parentID,
			CompressionReason:   reason,
			CompressionStrategy: strategy,
			CreatedAt:           createdAt.UTC().Format(time.RFC3339),
		}
		if len(compMeta) > 0 {
			_ = json.Unmarshal(compMeta, &msg.CompressionMeta)
		}
		if respBody != nil && *respBody != "" {
			msg.Content = *respBody
		} else if reqBody != nil {
			msg.Content = *reqBody
		}
		pack.Messages = append(pack.Messages, msg)

		if len(attachments) > 0 {
			var atts []ExportAttachment
			if json.Unmarshal(attachments, &atts) == nil {
				for _, a := range atts {
					key := a.Path + "|" + a.Name
					if _, ok := seenAtt[key]; ok {
						continue
					}
					seenAtt[key] = struct{}{}
					pack.Attachments = append(pack.Attachments, a)
				}
			}
		}
		if clientModel != nil && pack.SessionMeta.Instance == "" {
			pack.SessionMeta.Instance = *clientModel
		}
		_ = outboundModel
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iter: %w", err)
	}

	// Lookup title + summary from session_summaries
	var title, summary *string
	if err := e.store.QueryRow(ctx, `
		SELECT title, summary
		FROM session_summaries
		WHERE session_key = $1
		LIMIT 1
	`, sessionID).Scan(&title, &summary); err == nil {
		if title != nil {
			pack.SessionMeta.Title = *title
		}
		if summary != nil {
			pack.Summary = *summary
			pack.ResumeBrief.LastObjective = *summary
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("summaries lookup failed", "session", sessionID, "err", err)
	}

	if len(pack.Messages) == 0 && pack.Summary == "" && pack.SessionMeta.Title == "" {
		return nil, ErrSessionNotFound
	}
	return pack, nil
}

// ErrSessionNotFound 表示 request_logs 中无此 session 的轮次。
var ErrSessionNotFound = errors.New("sessionforensics: session not found")

// ListRecentSessions 拉取最近活跃的 session_id 列表（运维平台列表展示用）。
// limit <= 0 时默认 50；可以加 since 参数后续扩展。
func (e *Exporter) ListRecentSessions(ctx context.Context, tenantID string, limit int) ([]SessionAudit, error) {
	if e == nil || e.store == nil {
		return nil, ErrorDBUnavailable
	}
	if tenantID == "" {
		tenantID = "default"
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := e.store.Query(ctx, `
		SELECT
			gw_session_id,
			COUNT(*) AS turns,
			COUNT(*) FILTER (WHERE compression_strategy IS NOT NULL AND compression_strategy <> '') AS compression_hits,
			COUNT(*) FILTER (WHERE gw_session_id IS NULL OR gw_session_id = '') AS missing_sid,
			COUNT(*) FILTER (WHERE prompt_tokens IS NOT NULL) AS pt_ok,
			COALESCE(SUM(prompt_tokens), 0) AS total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS total_resp_tokens,
			COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
			MIN(ts::text) AS earliest_at,
			MAX(ts::text) AS latest_at,
			array_agg(DISTINCT client_model) FILTER (WHERE client_model IS NOT NULL) AS models
		FROM request_logs
		WHERE tenant_id = $1
		GROUP BY gw_session_id
		ORDER BY latest_at DESC NULLS LAST
		LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]SessionAudit, 0, limit)
	now := time.Now().UTC()
	for rows.Next() {
		var (
			sid              *string
			turns, hits, mis int
			ptOk             int
			tpTokens         int64
			trTokens         int64
			tcost            float64
			earliest, lates  *string
			models           []string
		)
		if err := rows.Scan(&sid, &turns, &hits, &mis, &ptOk,
			&tpTokens, &trTokens, &tcost, &earliest, &lates, &models); err != nil {
			continue
		}
		if sid == nil || *sid == "" {
			continue
		}
		a := SessionAudit{
			SessionID:         *sid,
			HasCompression:    hits > 0,
			CompressionHits:   hits,
			TotalTurns:        turns,
			HasSessionID:      true,
			MissingSessionIDs: mis,
			ModelsUsed:        models,
			TotalPromptTokens: int(tpTokens),
			TotalRespTokens:   int(trTokens),
			TotalCostUSD:      tcost,
			AuditAt:           now,
		}
		if earliest != nil {
			a.EarliestAt = *earliest
		}
		if lates != nil {
			a.LatestAt = *lates
		}
		out = append(out, a)
	}
	return out, nil
}

// UpsertSummary 在 session_summaries 中 UPSERT title + summary（CI / 离线工具
// 拿回数据后写回）；如果现有的 title / summary 已经足够好（来自 LLM），跳过。
//
// 这里只写 session_summaries，不动 session_titles（后者由 admin 异步触发）。
//
// 不修改聚合字段（first_request_at / last_request_at / total_cost_usd 等），
// 这些字段由 request_logs 触发器自动维护；只覆盖用户语义相关的 title/summary
// / key_topics/user_intent。
func (e *Exporter) UpsertSummary(ctx context.Context, sessionID string, result SummaryResult) error {
	if e == nil || e.store == nil {
		return ErrorDBUnavailable
	}
	if sessionID == "" {
		return fmt.Errorf("sessionforensics: missing session_id")
	}
	if result.Title == "" && result.Summary == "" {
		return fmt.Errorf("sessionforensics: empty title and summary, refusing to drop existing data")
	}
	topics := result.KeyTopics
	if topics == nil {
		topics = []string{}
	}
	topicsArray := "{" + joinForPGArray(topics) + "}"
	userIntent := result.UserIntent
	if userIntent == "" {
		userIntent = "auto"
	}

	// INSERT/UPSERT 不需要 RETURNING；用 Exec 接口
	_, err := e.store.Query(ctx, `
		INSERT INTO session_summaries
			(session_key, tenant_id, first_request_at, last_request_at,
			 title, summary, key_topics, user_intent)
		VALUES ($1, 'default', NOW(), NOW(),
				NULLIF($2, ''), NULLIF($3, ''), $4::text[], NULLIF($5, ''))
		ON CONFLICT (session_key) DO UPDATE SET
			title      = COALESCE(EXCLUDED.title,      session_summaries.title),
			summary    = COALESCE(EXCLUDED.summary,    session_summaries.summary),
			key_topics = COALESCE(EXCLUDED.key_topics, session_summaries.key_topics),
			user_intent= COALESCE(EXCLUDED.user_intent,session_summaries.user_intent)
	`, sessionID, result.Title, result.Summary, topicsArray, userIntent)
	return err
}

// joinForPGArray 把 []string 格式化为 PG text[] 字面量（`a,b,c`）。
// 注意：只允许 7-bit ASCII；中文等会被 Postgres 当作 raw text。
func joinForPGArray(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += pgQuote(x)
	}
	return out
}

func pgQuote(s string) string {
	out := ""
	for _, r := range s {
		if r == ',' || r == '{' || r == '}' || r == '"' || r == '\\' {
			out += string('\\')
		}
		out += string(r)
	}
	return out
}
