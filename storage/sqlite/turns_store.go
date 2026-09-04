package sqlite

import (
	"context"
	"database/sql"
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
