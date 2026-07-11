package admin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

func loadTurnSnapshotsForCompare(ctx context.Context, q pgx.Tx, sessionID, tenantID string) ([]TurnView, bool) {
	rows, err := q.Query(ctx, `
		SELECT turn_no, request_id, created_at, compression_strategy, summary_marker,
			original_send, original_receive, compressed_send, compressed_receive, secured_send, secured_receive,
			original_send_ref, original_receive_ref, compressed_send_ref, compressed_receive_ref, secured_send_ref, secured_receive_ref,
			security_tags, compressed_range_start, compressed_range_end,
			token_original, token_compressed, token_secured, stream_completed
		FROM session_turn_snapshots
		WHERE tenant_id = $1 AND gw_session_id = $2 AND expires_at > NOW()
		ORDER BY turn_no ASC
	`, tenantID, sessionID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	turns := make([]TurnView, 0)
	for rows.Next() {
		var row struct {
			turn                                                                                                            int
			requestID                                                                                                       string
			createdAt                                                                                                       time.Time
			strategy                                                                                                        *string
			summaryMarker                                                                                                   *string
			originalSend, originalReceive, compressedSend, compressedReceive, securedSend, securedReceive                   []byte
			originalSendRef, originalReceiveRef, compressedSendRef, compressedReceiveRef, securedSendRef, securedReceiveRef *string
			securityTags                                                                                                    []string
			rangeStart, rangeEnd                                                                                            *int
			originalTokens, compressedTokens, securedTokens                                                                 int
			streamCompleted                                                                                                 bool
		}
		if err := rows.Scan(
			&row.turn, &row.requestID, &row.createdAt, &row.strategy, &row.summaryMarker,
			&row.originalSend, &row.originalReceive, &row.compressedSend, &row.compressedReceive, &row.securedSend, &row.securedReceive,
			&row.originalSendRef, &row.originalReceiveRef, &row.compressedSendRef, &row.compressedReceiveRef, &row.securedSendRef, &row.securedReceiveRef,
			&row.securityTags, &row.rangeStart, &row.rangeEnd,
			&row.originalTokens, &row.compressedTokens, &row.securedTokens, &row.streamCompleted,
		); err != nil {
			continue
		}
		compressedSend := bodyOrFallback(row.compressedSend, row.compressedSendRef, row.originalSend)
		compressedReceive := bodyOrFallback(row.compressedReceive, row.compressedReceiveRef, row.originalReceive)
		turn := TurnView{
			Turn:          row.turn,
			RequestID:     row.requestID,
			Ts:            row.createdAt.Format(time.RFC3339),
			Strategy:      derefString(row.strategy),
			SummaryMarker: derefString(row.summaryMarker),
			Original: TurnStage{
				Send:    latestUserMessageFromJSON(row.originalSend),
				Receive: responseTextFromJSON(row.originalReceive),
				Tokens:  row.originalTokens,
			},
			Compressed: TurnStage{
				Send:    bodyTextOrReference(compressedSend, nil, nil),
				Receive: bodyTextOrReference(compressedReceive, nil, nil),
				Tokens:  row.compressedTokens,
			},
			Secured: TurnStage{
				Send:        bodyTextOrReference(bodyOrFallback(row.securedSend, row.securedSendRef, compressedSend), nil, nil),
				Receive:     bodyTextOrReference(bodyOrFallback(row.securedReceive, row.securedReceiveRef, compressedReceive), nil, nil),
				Tokens:      row.securedTokens,
				AppliedTags: row.securityTags,
			},
		}
		if row.rangeStart != nil {
			turn.Compressed.RangeStart = *row.rangeStart
		}
		if row.rangeEnd != nil {
			turn.Compressed.RangeEnd = *row.rangeEnd
		}
		if !row.streamCompleted {
			turn.Secured.AppliedTags = append(turn.Secured.AppliedTags, "stream_incomplete")
		}
		turns = append(turns, turn)
	}
	return turns, len(turns) > 0
}

func latestUserMessageFromJSON(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	value := string(body)
	return latestUserMessage(&value)
}

func responseTextFromJSON(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	value := string(body)
	return firstAssistantFromResponse(&value)
}

func bodyOrFallback(body []byte, reference *string, fallback []byte) []byte {
	if len(body) == 0 && reference != nil {
		return fallback
	}
	return body
}

func bodyTextOrReference(body []byte, reference *string, fallback []byte) string {
	if len(body) == 0 && reference != nil {
		body = fallback
	}
	if len(body) == 0 {
		return ""
	}
	var generic map[string]json.RawMessage
	if json.Unmarshal(body, &generic) == nil {
		if _, ok := generic["messages"]; ok {
			return string(body)
		}
	}
	return string(body)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
