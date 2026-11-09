package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
)

const (
	strictColdBodyBytes = 1 << 20
	strictColdMetaBytes = 256 << 10
	strictColdTurns     = 10
	strictColdMessages  = 2048
)

func strictColdRead(ctx context.Context) bool {
	_, enabled := contextbudget.SpecFromContext(ctx)
	return enabled
}

func boundedLatestOutboundSQL(finalFull bool) string {
	predicate, order := "", "turn_no DESC, ts DESC"
	if finalFull {
		predicate, order = "AND kind = 'final_full'", "ts DESC"
	}
	return `WITH latest AS (
 SELECT outbound_body FROM public.session_bodies_unified
 WHERE tenant_id = $1 AND session_id = $2 ` + predicate + `
 AND outbound_body IS NOT NULL
 ORDER BY ` + order + ` LIMIT 1
)
SELECT CASE WHEN COALESCE(octet_length(outbound_body::text),0) <= $3 THEN outbound_body ELSE NULL END,
 COALESCE(octet_length(outbound_body::text) > $3,FALSE) AS oversized
FROM latest`
}

func (r *TurnReader) loadLatestOutboundBounded(ctx context.Context, tenantID, sessionID string) ([]Message, error) {
	var raw []byte
	var oversized bool
	err := r.db.QueryRow(ctx, boundedLatestOutboundSQL(true), tenantID, sessionID, strictColdBodyBytes).Scan(&raw, &oversized)
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, boundedLatestOutboundSQL(false), tenantID, sessionID, strictColdBodyBytes).Scan(&raw, &oversized)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query bounded latest outbound: %w", err)
	}
	if oversized || len(raw) > strictColdBodyBytes {
		return nil, errors.New("latest outbound exceeds bounded recovery capacity")
	}
	return decodeColdMessages(raw)
}

const boundedChainSQL = `WITH recent AS (
 SELECT b.turn_no,b.ts,b.request_delta,b.response_delta
 FROM public.session_bodies_unified b
 WHERE b.tenant_id=$1 AND b.session_id=$2
 ORDER BY b.turn_no DESC,b.ts DESC LIMIT $3
), budgeted AS (
 SELECT *,SUM(COALESCE(octet_length(request_delta::text),0)+COALESCE(octet_length(response_delta::text),0))
 OVER (ORDER BY turn_no DESC,ts DESC ROWS UNBOUNDED PRECEDING) AS cumulative_bytes
 FROM recent
)
SELECT turn_no,
 CASE WHEN COALESCE(octet_length(request_delta::text),0)+COALESCE(octet_length(response_delta::text),0) <= $4 AND cumulative_bytes <= $4 THEN request_delta ELSE NULL END,
 CASE WHEN cumulative_bytes <= $4 THEN response_delta ELSE NULL END,
 COALESCE(cumulative_bytes > $4,FALSE) AS oversized
FROM budgeted ORDER BY turn_no ASC,ts ASC`

func (r *TurnReader) loadChainBounded(ctx context.Context, tenantID, sessionID string, lastN int) ([]Message, error) {
	if lastN == 0 || lastN > strictColdTurns {
		lastN = strictColdTurns
	}
	rows, err := r.db.Query(ctx, boundedChainSQL, tenantID, sessionID, lastN, strictColdBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("query bounded turn chain: %w", err)
	}
	defer rows.Close()
	var chain []Message
	total, count := 0, 0
	for rows.Next() {
		var turnNo int
		var req, resp []byte
		var oversized bool
		if err := rows.Scan(&turnNo, &req, &resp, &oversized); err != nil {
			return nil, fmt.Errorf("scan bounded turn: %w", err)
		}
		count++
		total += len(req) + len(resp)
		if oversized || total > strictColdBodyBytes || count > lastN {
			return nil, errors.New("turn chain exceeds bounded recovery capacity")
		}
		for _, raw := range [][]byte{req, resp} {
			messages, err := decodeColdMessages(raw)
			if err != nil {
				return nil, fmt.Errorf("decode bounded turn %d: %w", turnNo, err)
			}
			if len(chain)+len(messages) > strictColdMessages {
				return nil, errors.New("turn chain message count exceeds bounded recovery capacity")
			}
			chain = append(chain, messages...)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read bounded turn chain: %w", err)
	}
	return chain, nil
}

func decodeColdMessages(raw []byte) ([]Message, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if len(raw) > strictColdBodyBytes {
		return nil, errors.New("message body exceeds bounded recovery capacity")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if token, err := dec.Token(); err != nil || token != json.Delim('[') {
		return nil, errors.New("bounded message body must be an array")
	}
	var messages []Message
	for dec.More() {
		if len(messages) >= strictColdMessages {
			return nil, errors.New("message count exceeds bounded recovery capacity")
		}
		var msg Message
		if err := dec.Decode(&msg); err != nil {
			return nil, fmt.Errorf("decode bounded message: %w", err)
		}
		messages = append(messages, msg)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing bounded message data")
	}
	return messages, nil
}
