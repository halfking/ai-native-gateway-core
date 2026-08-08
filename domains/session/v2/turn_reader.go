// Package v2: TurnReader 从 session_turns + session_bodies 重建 SessionState
// 用于 L3 冷启动与全景按需重建。
package v2

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type turnReaderDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type TurnReader struct{ db turnReaderDB }

func NewTurnReader(db *pgxpool.Pool) *TurnReader {
	if db == nil {
		return &TurnReader{}
	}
	return newTurnReader(db)
}

func newTurnReader(db turnReaderDB) *TurnReader { return &TurnReader{db: db} }

// LoadLatestOutbound returns the exact message body most recently forwarded to
// the upstream model. Unlike LoadChain, this preserves gateway compression
// summaries and markers stored in session_bodies.outbound_body.
func (r *TurnReader) LoadLatestOutbound(ctx context.Context, tenantID, sessionID string) ([]Message, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var raw []byte
	err := r.db.QueryRow(ctx, `
		SELECT outbound_body
		FROM gateway.session_bodies
		WHERE tenant_id = $1 AND session_id = $2
		  AND outbound_body IS NOT NULL
		ORDER BY turn_no DESC, ts DESC
		LIMIT 1
	`, tenantID, sessionID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query latest outbound body: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var msgs []Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, fmt.Errorf("unmarshal latest outbound body: %w", err)
	}
	return msgs, nil
}

// LoadChain 返回最近 N 轮拼接后的消息（用于 L3 冷启动）。
// 不持久化 panorama；仅按需计算。
func (r *TurnReader) LoadChain(ctx context.Context, tenantID, sessionID string, lastN int) ([]Message, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	if lastN <= 0 {
		lastN = 10
	}
	query := `
        SELECT b.turn_no, b.request_delta, b.response_delta
        FROM gateway.session_bodies b
        WHERE b.tenant_id = $1 AND b.session_id = $2
        ORDER BY b.turn_no ASC
    `
	rows, err := r.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query bodies: %w", err)
	}
	defer rows.Close()
	type turn struct {
		turnNo                      int
		requestDelta, responseDelta []byte
	}
	var turns []turn
	for rows.Next() {
		var t turn
		if err := rows.Scan(&t.turnNo, &t.requestDelta, &t.responseDelta); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	if len(turns) > lastN {
		turns = turns[len(turns)-lastN:]
	}
	var chain []Message
	for _, t := range turns {
		var reqMsgs []Message
		if len(t.requestDelta) > 0 {
			if err := json.Unmarshal(t.requestDelta, &reqMsgs); err != nil {
				return nil, fmt.Errorf("unmarshal request turn %d: %w", t.turnNo, err)
			}
		}
		chain = append(chain, reqMsgs...)
		var respMsgs []Message
		if len(t.responseDelta) > 0 {
			if err := json.Unmarshal(t.responseDelta, &respMsgs); err != nil {
				return nil, fmt.Errorf("unmarshal response turn %d: %w", t.turnNo, err)
			}
		}
		chain = append(chain, respMsgs...)
	}
	return chain, nil
}
