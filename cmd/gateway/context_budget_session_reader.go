package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
)

type outboundSessionReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Select the same latest row as the legacy query FIRST. An oversized latest
// body is returned as a flag, never hidden by filtering down to an old body.
func boundedLastOutboundSQL() string {
	return `WITH bounded_latest(body,msg_count,token_est,hashes,meta) AS (` + lastOutboundSessionSQL + `)
SELECT CASE WHEN COALESCE(octet_length(body::text),0) <= $3 THEN body ELSE NULL END,
 msg_count,token_est,
 CASE WHEN COALESCE(octet_length(hashes::text),0)+COALESCE(octet_length(meta::text),0) <= $4 THEN hashes ELSE NULL END,
 CASE WHEN COALESCE(octet_length(hashes::text),0)+COALESCE(octet_length(meta::text),0) <= $4 THEN meta ELSE NULL END,
 COALESCE(octet_length(body::text),0)>$3 OR COALESCE(octet_length(hashes::text),0)+COALESCE(octet_length(meta::text),0)>$4 AS oversized
FROM bounded_latest`
}

type boundedOutboundRow struct {
	row pgx.Row
}

func (r boundedOutboundRow) Scan(dest ...any) error {
	var oversized bool
	if err := r.row.Scan(append(dest, &oversized)...); err != nil {
		return err
	}
	if oversized {
		return errors.New("latest session body exceeds bounded recovery capacity")
	}
	return nil
}

func readBoundedLastOutbound(ctx context.Context, pool outboundSessionReader, tenantID, sessionID string, bodyLimit, metadataLimit int) (*compression.LastOutboundRow, error) {
	if bodyLimit <= 0 || bodyLimit > 1<<20 || metadataLimit <= 0 || metadataLimit > 256<<10 {
		return nil, errors.New("invalid bounded session read limits")
	}
	row := pool.QueryRow(ctx, boundedLastOutboundSQL(), sessionID, tenantID, bodyLimit, metadataLimit)
	return scanLastOutboundRow(boundedOutboundRow{row: row})
}

func readLastOutboundSession(ctx context.Context, pool outboundSessionReader, tenantID, sessionID string) (*compression.LastOutboundRow, error) {
	if _, strict := contextbudget.SpecFromContext(ctx); strict {
		return readBoundedLastOutbound(ctx, pool, tenantID, sessionID, 1<<20, 256<<10)
	}
	return scanLastOutboundRow(pool.QueryRow(ctx, lastOutboundSessionSQL, sessionID, tenantID))
}

// This explicit interface prevents generic old DB adapters from being used as
// supposedly bounded fallbacks merely because a strict request carries ctx.
func (a *pgBackendAdapter) LastOutboundForSessionBounded(ctx context.Context, tenantID, sessionID string, bodyLimit, metadataLimit int) (*compression.LastOutboundRow, error) {
	if a.dbConn == nil || a.dbConn.Pool() == nil {
		return nil, errors.New("db pool not configured")
	}
	return readBoundedLastOutbound(ctx, a.dbConn.Pool(), tenantID, sessionID, bodyLimit, metadataLimit)
}
