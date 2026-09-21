package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// 轮次元数据 SQL（时间存 Unix 秒；metadata 列暂无对应业务字段，写入为 NULL）。
const (
	upsertTurnSQL = `
INSERT INTO session_turns (tenant_id, session_id, turn_no, ts, compression_strategy, prompt_tokens, completion_tokens)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, session_id, turn_no) DO UPDATE SET
	ts = excluded.ts,
	compression_strategy = excluded.compression_strategy,
	prompt_tokens = excluded.prompt_tokens,
	completion_tokens = excluded.completion_tokens;`

	selectTurnsSQL = `
SELECT tenant_id, session_id, turn_no, ts, compression_strategy, prompt_tokens, completion_tokens
FROM session_turns
WHERE tenant_id = ? AND session_id = ?
ORDER BY turn_no ASC;`

	// 会话存储解耦 v3：turn 特征层（PG17 session_turn_details 的 SQLite
	// 投影）。request_id 唯一索引守护重放幂等；quality_flags/attachments
	// 以 JSON 文本落库，空值 NULL。
	upsertTurnDetailsSQL = `
INSERT INTO session_turn_details (
	tenant_id, session_id, turn_no, request_id, ts,
	model, provider, credential_id, success, status_code, error_kind,
	latency_ms, cost_usd, client_model, request_type, request_class,
	quality_flags, attachments
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, session_id, turn_no) DO UPDATE SET
	request_id = excluded.request_id,
	ts = excluded.ts,
	model = excluded.model,
	provider = excluded.provider,
	credential_id = excluded.credential_id,
	success = excluded.success,
	status_code = excluded.status_code,
	error_kind = excluded.error_kind,
	latency_ms = excluded.latency_ms,
	cost_usd = excluded.cost_usd,
	client_model = excluded.client_model,
	request_type = excluded.request_type,
	request_class = excluded.request_class,
	quality_flags = excluded.quality_flags,
	attachments = excluded.attachments;`
)

// SQLiteTurnsStore 基于 SQLite 的会话轮次元数据存储，实现 storage.TurnsStore。
type SQLiteTurnsStore struct {
	db *sql.DB
}

// 编译期断言：确保实现 storage.TurnsStore 接口。
var _ storage.TurnsStore = (*SQLiteTurnsStore)(nil)

// NewSQLiteTurnsStore 创建轮次元数据存储，db 一般来自 OpenSQLite。
func NewSQLiteTurnsStore(db *sql.DB) *SQLiteTurnsStore {
	return &SQLiteTurnsStore{db: db}
}

// WriteTurnMeta 写入单轮元数据；同一 (tenant, session, turn) 重复写入时
// 按 UPSERT 覆盖为最新值。Timestamp 为零值时自动取当前时间。
func (s *SQLiteTurnsStore) WriteTurnMeta(ctx context.Context, meta *storage.TurnMeta) error {
	if meta == nil {
		return errors.New("sqlite: 轮次元数据不能为空")
	}
	ts := meta.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	if _, err := s.db.ExecContext(ctx, upsertTurnSQL,
		meta.TenantID, meta.SessionID, meta.TurnNo, ts.Unix(),
		meta.CompressionStrategy, meta.PromptTokens, meta.CompletionTokens); err != nil {
		return fmt.Errorf("sqlite: 写入轮次元数据 %s/%s#%d 失败: %w", meta.TenantID, meta.SessionID, meta.TurnNo, err)
	}
	return nil
}

// GetTurnsMeta 返回某会话的全部轮次元数据，按 turn_no 升序；
// 无数据时返回空切片（非 nil）。
func (s *SQLiteTurnsStore) GetTurnsMeta(ctx context.Context, tenantID, sessionID string) ([]*storage.TurnMeta, error) {
	rows, err := s.db.QueryContext(ctx, selectTurnsSQL, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 查询轮次元数据 %s/%s 失败: %w", tenantID, sessionID, err)
	}
	defer rows.Close()

	metas := make([]*storage.TurnMeta, 0)
	for rows.Next() {
		var (
			m        storage.TurnMeta
			ts       sql.NullInt64
			strategy sql.NullString
		)
		if err := rows.Scan(&m.TenantID, &m.SessionID, &m.TurnNo, &ts, &strategy, &m.PromptTokens, &m.CompletionTokens); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描轮次元数据行失败: %w", err)
		}
		if ts.Valid {
			m.Timestamp = time.Unix(ts.Int64, 0).UTC()
		}
		m.CompressionStrategy = strategy.String
		metas = append(metas, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历轮次元数据失败: %w", err)
	}
	return metas, nil
}

// WriteTurnDetails 写入单轮特征层（会话存储解耦 v3）；同一
// (tenant, session, turn) 重复写入时 UPSERT 覆盖为最新值。零值转 NULL；
// QualityFlags/Attachments 以 JSON 文本落库。Timestamp 零值自动取当前时间。
func (s *SQLiteTurnsStore) WriteTurnDetails(ctx context.Context, d *storage.TurnDetails) error {
	if d == nil {
		return errors.New("sqlite: 轮次特征不能为空")
	}
	if d.RequestID == "" {
		return errors.New("sqlite: 轮次特征缺 request_id")
	}
	ts := d.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	var success interface{}
	if d.Success != nil {
		success = boolToInt(*d.Success)
	}
	qualityFlags, err := jsonText(d.QualityFlags)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 quality_flags 失败: %w", err)
	}
	attachments, err := jsonTextRaw(d.Attachments)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 attachments 失败: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, upsertTurnDetailsSQL,
		d.TenantID, d.SessionID, d.TurnNo, d.RequestID, ts.Unix(),
		nilIfEmpty(d.Model), nilIfEmpty(d.Provider), nilIfEmpty(d.CredentialID),
		success, nilIfZero(d.StatusCode), nilIfEmpty(d.ErrorKind),
		nilIfZero(d.LatencyMs), nilIfZeroF(d.CostUSD),
		nilIfEmpty(d.ClientModel), nilIfEmpty(d.RequestType), nilIfEmpty(d.RequestClass),
		qualityFlags, attachments,
	); err != nil {
		return fmt.Errorf("sqlite: 写入轮次特征 %s/%s#%d 失败: %w", d.TenantID, d.SessionID, d.TurnNo, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nilIfZeroF(v float64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func jsonText(vals []string) (interface{}, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func jsonTextRaw(raw json.RawMessage) (interface{}, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return string(raw), nil
}
