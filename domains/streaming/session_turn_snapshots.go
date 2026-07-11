package streaming

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

type sessionTurnSnapshotWriter struct {
	db *pgxpool.Pool
}

type sessionTurnSnapshot struct {
	TenantID            string
	SessionID           string
	RequestID           string
	OriginalSend        []byte
	OriginalReceive     []byte
	CompressedSend      []byte
	CompressedReceive   []byte
	SecuredSend         []byte
	SecuredReceive      []byte
	CompressionStrategy string
	CompressionMeta     map[string]any
	SecurityTags        []string
	RangeStart          int
	RangeEnd            int
	SummaryMarker       string
	OriginalTokens      int
	CompressedTokens    int
	SecuredTokens       int
	StreamCompleted     bool
}

func newSessionTurnSnapshotWriter(db *pgxpool.Pool) *sessionTurnSnapshotWriter {
	if db == nil {
		return nil
	}
	return &sessionTurnSnapshotWriter{db: db}
}

func (w *sessionTurnSnapshotWriter) write(ctx context.Context, snapshot sessionTurnSnapshot) {
	if w == nil || w.db == nil || snapshot.SessionID == "" || snapshot.RequestID == "" {
		return
	}
	if snapshot.TenantID == "" {
		snapshot.TenantID = "default"
	}

	go func() {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := w.persist(writeCtx, snapshot); err != nil {
			slog.Warn("session turn snapshot write failed", "request_id", snapshot.RequestID, "session_id", snapshot.SessionID, "error", err)
		}
	}()
}

func (w *sessionTurnSnapshotWriter) persist(ctx context.Context, snapshot sessionTurnSnapshot) error {
	meta, err := json.Marshal(snapshot.CompressionMeta)
	if err != nil {
		meta = []byte(`{}`)
	}
	originalSend, originalSendRef := snapshotBody(snapshot.OriginalSend, nil)
	originalReceive, originalReceiveRef := snapshotBody(snapshot.OriginalReceive, nil)
	compressedSend, compressedSendRef := snapshotBody(snapshot.CompressedSend, snapshot.OriginalSend)
	compressedReceive, compressedReceiveRef := snapshotBody(snapshot.CompressedReceive, snapshot.OriginalReceive)
	securedSend, securedSendRef := snapshotBody(snapshot.SecuredSend, snapshot.CompressedSend)
	securedReceive, securedReceiveRef := snapshotBody(snapshot.SecuredReceive, snapshot.CompressedReceive)

	_, err = w.db.Exec(ctx, `
		WITH locked AS (
			SELECT pg_advisory_xact_lock(hashtext($1 || ':' || $2))
		), next_turn AS (
			SELECT COALESCE(MAX(turn_no), 0) + 1 AS turn_no
			FROM session_turn_snapshots, locked
			WHERE tenant_id = $1 AND gw_session_id = $2
		)
		INSERT INTO session_turn_snapshots (
			tenant_id, gw_session_id, turn_no, request_id, expires_at,
			original_send, original_receive, compressed_send, compressed_receive, secured_send, secured_receive,
			original_send_ref, original_receive_ref, compressed_send_ref, compressed_receive_ref, secured_send_ref, secured_receive_ref,
			compression_strategy, compression_meta, security_tags,
			compressed_range_start, compressed_range_end, summary_marker,
			token_original, token_compressed, token_secured, stream_completed
		)
			SELECT $1, $2, next_turn.turn_no, $3, NOW() + make_interval(hours => $4),
				$5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb,
				$11, $12, $13, $14, $15, $16,
				$17, $18::jsonb, $19::text[],
				$20,
				CASE WHEN $20 IS NULL THEN NULL ELSE next_turn.turn_no END,
				$21,
				$22, $23, $24, $25

		FROM next_turn
		ON CONFLICT (tenant_id, request_id) DO UPDATE SET
			original_receive = EXCLUDED.original_receive,
			compressed_receive = EXCLUDED.compressed_receive,
			secured_receive = EXCLUDED.secured_receive,
			original_receive_ref = EXCLUDED.original_receive_ref,
			compressed_receive_ref = EXCLUDED.compressed_receive_ref,
			secured_receive_ref = EXCLUDED.secured_receive_ref,
			compression_meta = EXCLUDED.compression_meta,
			security_tags = EXCLUDED.security_tags,
			stream_completed = EXCLUDED.stream_completed,
			updated_at = NOW()
	`,
		snapshot.TenantID, snapshot.SessionID, snapshot.RequestID, snapshotTTLHours(),
		jsonOrNil(originalSend), jsonOrNil(originalReceive), jsonOrNil(compressedSend), jsonOrNil(compressedReceive), jsonOrNil(securedSend), jsonOrNil(securedReceive),
		originalSendRef, originalReceiveRef, compressedSendRef, compressedReceiveRef, securedSendRef, securedReceiveRef,
		snapshot.CompressionStrategy, meta, snapshot.SecurityTags,
		nullableInt(snapshot.RangeStart), nullableString(snapshot.SummaryMarker),
		snapshot.OriginalTokens, snapshot.CompressedTokens, snapshot.SecuredTokens, snapshot.StreamCompleted,
	)
	return err
}

func snapshotBody(body, sameAs []byte) ([]byte, *string) {
	if len(body) == 0 {
		return nil, nil
	}
	if len(sameAs) > 0 && string(body) == string(sameAs) {
		ref := snapshotHash(body)
		return nil, &ref
	}
	return body, nil
}

func snapshotHash(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func jsonOrNil(body []byte) any {
	if len(body) == 0 || !json.Valid(body) {
		return nil
	}
	return body
}

func snapshotTTLHours() int {
	const defaultHours = 168
	if settings.Global == nil {
		return defaultHours
	}
	sp := settings.Global.Spec("compression.snapshot_ttl_hours")
	if sp == nil {
		return defaultHours
	}
	value, _, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || len(value) == 0 {
		return defaultHours
	}
	var hours int
	if json.Unmarshal(value, &hours) != nil || hours < 1 || hours > 8760 {
		return defaultHours
	}
	return hours
}

func snapshotTokenEstimate(body []byte) int {
	return int(float64(len(body)) / 3.5)
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullableInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}
