package errorplaybook

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
)

type streamEvent struct {
	EventID    string `json:"event_id"`
	TenantID   string `json:"tenant_id"`
	ProviderID int64  `json:"provider_id"`
	remediation.SSESignal
}

func (p *Remediation) runSSEAnalysis(ctx context.Context, until time.Time, interval time.Duration) {
	target := p.endpoint(remediation.SSEPluginID)
	if target.SocketPath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remediationRoundTime)
	defer cancel()
	start, end := remediationWindow(until, interval)
	scanCtx, stopScan := context.WithTimeout(ctx, remediationQueryTime)
	events, err := p.source.fetchStreamEvents(scanCtx, start, end, target.Version)
	stopScan()
	if err != nil {
		slog.Warn("error_playbook: SSE interruption aggregation failed", "error", err)
		return
	}
	for _, event := range events {
		if ctx.Err() != nil || p.endpoint(remediation.SSEPluginID) != target {
			return
		}
		if !event.SSESignal.Valid() {
			continue
		}
		rec, err := requestReview(ctx, target, remediation.SSEPluginID, event.SSESignal)
		if err != nil {
			slog.Warn("error_playbook: SSE recommendation rejected", "error", err)
			continue
		}
		if p.endpoint(remediation.SSEPluginID) != target {
			return
		}
		audit, err := json.Marshal(struct {
			streamEvent
			TemplateVersion string `json:"template_version"`
			Recommendation  string `json:"recommendation"`
		}{event, rec.TemplateVersion, rec.Recommendation})
		if err == nil {
			err = p.store.recordRecommendation(ctx, remediation.SSEPluginID, event.EventID, rec.TemplateVersion,
				time.Unix(event.WindowStartUnix, 0), audit)
		}
		if err != nil {
			slog.Warn("error_playbook: record SSE recommendation failed", "error", err)
		}
	}
}

func (s *SQLSource) fetchStreamEvents(ctx context.Context, start, end time.Time, version string) ([]streamEvent, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		return nil, err
	}
	// stream_reason is recorded by the dispatch executor when a typed
	// interruption occurred. Idle evidence can come from either an upstream
	// stall or an intermediary. It warrants checking both.
	rows, err := tx.QueryContext(ctx, `
		WITH evidence AS (
			SELECT tenant_id, provider_id, request_id,
			       EXTRACT(EPOCH FROM date_bin($3::int * interval '1 second', ts, timestamptz 'epoch'))::bigint AS bucket,
			       CASE
			         WHEN context->>'stream_reason' = 'upstream_error' THEN 'upstream'
			         WHEN context->>'stream_reason' IN ('chunk_timeout', 'stream_chunk_timeout', 'stream_timeout', 'first_byte_timeout') THEN 'idle'
			         WHEN context->>'stream_reason' IN ('network_error', 'upstream_read_error', 'connection_reset', 'eof_without_done') THEN 'network'
			       END AS category
			FROM candidate_failure_logs_hot
			WHERE ts >= $1 AND ts < $2 AND context ? 'stream_reason'
			  AND octet_length(tenant_id) BETWEEN 1 AND 256 AND btrim(tenant_id) <> ''
			  AND btrim(request_id) <> '' AND provider_id > 0
		), clusters AS (
			SELECT tenant_id, provider_id, bucket,
			       COUNT(DISTINCT request_id) AS requests,
			       COUNT(DISTINCT request_id) FILTER (WHERE category = 'upstream') AS upstream,
			       COUNT(DISTINCT request_id) FILTER (WHERE category = 'idle') AS idle,
			       COUNT(DISTINCT request_id) FILTER (WHERE category = 'network') AS network,
			       concat(bucket, '/', provider_id, '/', tenant_id) AS event_id
			FROM evidence
			WHERE category IS NOT NULL
			GROUP BY tenant_id, provider_id, bucket
			HAVING COUNT(DISTINCT request_id) >= $4
		)
		SELECT event_id, tenant_id, provider_id, bucket, requests, upstream, idle, network
		FROM clusters
		WHERE NOT EXISTS (
			SELECT 1 FROM error_playbook_executions e
			WHERE e.error_type = $5 AND e.executed_by = $5 AND e.created_at >= $1
			  AND e.context->>'event_id' = clusters.event_id AND e.context->>'template_version' = $6
		)
		ORDER BY bucket, requests DESC, tenant_id, provider_id
		LIMIT $7`, start, end, int64(remediation.Window/time.Second), remediation.MinRequests,
		recommendationActor(remediation.SSEPluginID), version, remediationBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []streamEvent
	for rows.Next() {
		event := streamEvent{SSESignal: remediation.SSESignal{SchemaVersion: remediation.SchemaVersion}}
		if err := rows.Scan(&event.EventID, &event.TenantID, &event.ProviderID, &event.WindowStartUnix,
			&event.DistinctRequests, &event.Upstream, &event.IdleTimeout, &event.Network); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
