// Package v2: TurnReader 从 session_turns + session_bodies 重建 SessionState
// 用于 L3 冷启动与全景按需重建。
package v2

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type TurnReader struct{ db *pgxpool.Pool }

func NewTurnReader(db *pgxpool.Pool) *TurnReader { return &TurnReader{db: db} }

// LoadChain 返回最近 N 轮拼接后的消息（用于 L3 冷启动）。
// 不持久化 panorama；仅按需计算。
func (r *TurnReader) LoadChain(ctx context.Context, tenantID, sessionID string, lastN int) ([]Message, error) {
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
