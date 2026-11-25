package errorplaybook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/controller"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
)

const CooldownActor = "plugin:" + remediation.PluginID

type cooldownEvent struct {
	EventID    string `json:"event_id"`
	TenantID   string `json:"tenant_id"`
	ProviderID int64  `json:"provider_id"`
	remediation.Signal
}

func (p *Remediation) runCooldown(ctx context.Context, until time.Time, interval time.Duration) {
	target := p.endpoint(remediation.PluginID)
	if target.SocketPath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remediationRoundTime)
	defer cancel()
	start, end := remediationWindow(until, interval)
	scanCtx, stopScan := context.WithTimeout(ctx, remediationQueryTime)
	events, err := p.source.fetchCooldownEvents(scanCtx, start, end, target.Version)
	stopScan()
	if err != nil {
		slog.Warn("error_playbook: cooldown aggregation failed", "error", err)
		return
	}
	for _, event := range events {
		if ctx.Err() != nil || p.endpoint(remediation.PluginID) != target {
			return
		}
		rec, err := requestCooldown(ctx, target, event.Signal)
		if err != nil {
			slog.Warn("error_playbook: cooldown recommendation rejected", "error", err)
			continue
		}
		if p.endpoint(remediation.PluginID) != target {
			return
		}
		if err := p.store.recordCooldown(ctx, event, rec); err != nil {
			slog.Warn("error_playbook: record cooldown recommendation failed", "error", err)
		}
	}
}

func requestCooldown(ctx context.Context, target controller.CapabilityEndpoint, signal remediation.Signal) (remediation.Suggestion, error) {
	if !signal.Valid() {
		return remediation.Suggestion{}, errors.New("invalid cooldown aggregate")
	}
	data, err := requestRecommendation(ctx, target, signal)
	if err != nil {
		return remediation.Suggestion{}, err
	}
	rec, err := remediation.DecodeSuggestion(bytes.NewReader(data))
	if err != nil || !rec.Valid(target.Version) {
		return remediation.Suggestion{}, errors.New("invalid cooldown recommendation")
	}
	return rec, nil
}

// fetchCooldownEvents reads closed UTC buckets. Only the SQL counts and window
// cross the socket. Distinct requests keep one request's retries from forming a cluster.
func (s *SQLSource) fetchCooldownEvents(ctx context.Context, start, end time.Time, version string) ([]cooldownEvent, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The platform worker aggregates across tenants. The transaction-local
	// setting mirrors the other candidate failure aggregators.
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		WITH clusters AS (
			SELECT tenant_id, provider_id,
			       EXTRACT(EPOCH FROM date_bin($3::int * interval '1 second', ts, timestamptz 'epoch'))::bigint AS bucket,
			       COUNT(*) FILTER (WHERE upstream_status_code = 429) AS n429,
			       COUNT(*) FILTER (WHERE upstream_status_code = 502) AS n502,
			       COUNT(DISTINCT request_id) AS requests
			FROM candidate_failure_logs_hot
			WHERE ts >= $1 AND ts < $2 AND upstream_status_code IN (429, 502)
			  AND context @> '{"upstream_http_status_observed":true}'::jsonb
			  AND octet_length(tenant_id) BETWEEN 1 AND 256 AND btrim(tenant_id) <> ''
			  AND btrim(request_id) <> '' AND provider_id > 0
			GROUP BY tenant_id, provider_id, bucket
			HAVING COUNT(DISTINCT request_id) >= $4
		), keyed AS (
			SELECT *, concat(bucket, '/', provider_id, '/', tenant_id) AS event_id FROM clusters
		)
		SELECT event_id, tenant_id, provider_id, bucket, n429, n502, requests
		FROM keyed
		WHERE NOT EXISTS (
			SELECT 1 FROM error_playbook_executions e
			WHERE e.error_type = $5 AND e.executed_by = $5 AND e.created_at >= $1
			  AND e.context->>'event_id' = keyed.event_id AND e.context->>'template_version' = $6
		)
		ORDER BY bucket, requests DESC, tenant_id, provider_id
		LIMIT $7`, start, end, int64(remediation.Window/time.Second), remediation.MinRequests, CooldownActor, version, remediationBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []cooldownEvent
	for rows.Next() {
		event := cooldownEvent{Signal: remediation.Signal{SchemaVersion: remediation.SchemaVersion}}
		if err := rows.Scan(&event.EventID, &event.TenantID, &event.ProviderID, &event.WindowStartUnix,
			&event.Count429, &event.Count502, &event.DistinctRequests); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLStore) recordCooldown(ctx context.Context, event cooldownEvent, rec remediation.Suggestion) error {
	// Signal and recommendation share a wire schema version. Keep one copy in
	// the audit context along with the trusted event identity.
	audit, err := json.Marshal(struct {
		cooldownEvent
		TemplateVersion string `json:"template_version"`
		Recommendation  string `json:"recommendation"`
		CooldownSeconds int    `json:"cooldown_seconds"`
	}{event, rec.TemplateVersion, rec.Recommendation, rec.CooldownSeconds})
	if err != nil {
		return err
	}
	return s.recordRecommendation(ctx, remediation.PluginID, event.EventID, rec.TemplateVersion,
		time.Unix(event.WindowStartUnix, 0), audit)
}
