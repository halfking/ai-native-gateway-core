package metricsstore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

// Processor publishes the terminal request projection outside the request path.
// New facts are queued transactionally by the database, while a separate cursor
// works through history. A late arrival never depends on the historical cursor.
type Processor struct {
	db *sql.DB
}

func NewProcessor(db *sql.DB) *Processor {
	return &Processor{db: db}
}

const lockProjectionSQL = `
	SELECT singleton FROM public.request_metrics_projection_state
	WHERE singleton FOR UPDATE SKIP LOCKED`

const projectBatchSQL = `
WITH state AS (
	SELECT cursor_fact_id, cursor_occurred_at
	FROM public.request_metrics_projection_state
	WHERE singleton AND NOT backfill_complete
), history AS MATERIALIZED (
	SELECT f.*
	FROM public.usage_facts f
	WHERE (f.fact_id, f.occurred_at) > (
		SELECT cursor_fact_id, cursor_occurred_at FROM state
	)
	ORDER BY f.fact_id, f.occurred_at LIMIT $1
), pending AS MATERIALIZED (
	SELECT fact_id, occurred_at
	FROM public.request_metrics_pending
	ORDER BY fact_id, occurred_at LIMIT $1
), candidates AS (
	SELECT * FROM history
	UNION ALL
	SELECT f.* FROM pending p
	JOIN public.usage_facts f USING (fact_id, occurred_at)
), latest AS (
	SELECT DISTINCT ON (tenant_id, request_id, occurred_at) *
	FROM candidates
	ORDER BY tenant_id, request_id, occurred_at, revision DESC, finalized_at DESC, fact_id DESC
), normalized AS (
	SELECT f.*, coalesce(nullif(upper(btrim(f.cost_currency)),''), CASE
		-- The terminal inbox writer copies Event.CostUSD into cost_amount.
		-- That writer emits revision 1 and a stable terminal event key.
		-- An old zero without a currency may be an absent CostUSD pointer.
		WHEN f.source='stats_event_inbox' AND f.revision=1
		 AND f.event_id=f.request_id||':request_terminal:0' AND f.request_id<>''
		 AND f.cost_amount<>0 THEN 'USD' END) AS normalized_cost_currency
	FROM latest f
), published AS (
	INSERT INTO public.request_metrics AS rm (
		request_id, fact_request_id, tenant_id, occurred_at, occurred_date, billing_date,
		provider_id, credential_id, credential_label, provider_code, provider_name, outbound_model, canonical_id, raw_model_name,
		user_id, person_key, client_id, client_model, agent_name, client_ip, identity_hash, task_type, owner_user,
		session_id, task_id, api_key_id, api_key_prefix, api_key_owner_user, application_id, application_code, work_type, session_intent, compression_applied, client_cancelled,
		protocol, modality, attachment_count,
		prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, cache_input_tokens,
		reasoning_tokens, total_tokens, image_tokens, audio_tokens, video_tokens,
		cost_cents, cost_amount, cost_currency, credits_charged, status, traffic_class,
		error_kind, latency_ms, ttft_ms, usage_source, source_revision, source_finalized_at, source_fact_id, source)
	SELECT chr(1) || jsonb_build_array(f.tenant_id,f.request_id)::text, f.request_id,
		f.tenant_id, f.occurred_at, (f.occurred_at AT TIME ZONE 'UTC')::date,
		(f.occurred_at AT TIME ZONE 'UTC')::date,
		f.provider_id, f.credential_id, NULLIF(cred.label,''), p.code, COALESCE(NULLIF(p.display_name,''),p.code),
		COALESCE(mc.canonical_name, NULLIF(f.raw_model_name,'')),
		f.canonical_id, NULLIF(f.raw_model_name,''),
		NULLIF(f.end_user_id,''),
		COALESCE(NULLIF(CASE WHEN f.end_user_id LIKE '{%' THEN substring(f.end_user_id from '"device_id"\s*:\s*"([^"]*)"')
		          ELSE f.end_user_id END,''), NULLIF(f.person_hash,'')),
		NULLIF(c.virtual_client_id,''), NULLIF(l.client_model,''), COALESCE(NULLIF(c.agent_name,''), NULLIF(l.agent_name,'')),
		COALESCE(c.client_ip,l.client_ip), COALESCE(NULLIF(c.identity_hash,''), NULLIF(l.identity_hash,'')),
		NULLIF(l.task_type,''), COALESCE(NULLIF(c.owner_user,''), NULLIF(k.owner_user,'')),
		COALESCE(NULLIF(c.gw_session_id,''), NULLIF(l.gw_session_id,'')),
		COALESCE(NULLIF(c.gw_task_id,''), NULLIF(l.gw_task_id,'')), f.api_key_id, k.key_prefix, COALESCE(NULLIF(l.api_key_owner_user,''),NULLIF(k.owner_user,'')), f.application_id, NULLIF(c.application_code,''),
		NULLIF(l.work_type,''), NULLIF(btrim(s.user_intent),''), l.compression_applied, l.client_cancelled,
		COALESCE(NULLIF(l.client_protocol,''), NULLIF(l.egress_protocol,'')),
		CASE WHEN COALESCE(l.attachment_count,0)>0
		        OR (f.image_tokens>0)::int + (f.audio_tokens>0)::int + (f.video_tokens>0)::int > 1 THEN 'multimodal'
		     WHEN f.image_tokens>0 THEN 'image'
		     WHEN f.audio_tokens>0 THEN 'audio'
		     WHEN f.video_tokens>0 THEN 'video'
		     ELSE 'text' END,
		COALESCE(l.attachment_count,0),
		f.prompt_tokens, f.completion_tokens, f.cache_read_tokens, f.cache_write_tokens,
		CASE WHEN coalesce(f.usage_source,'') NOT IN ('llm','corrected')
		          OR least(f.prompt_tokens,f.cache_read_tokens,f.cache_write_tokens) < 0 THEN NULL
		     WHEN f.cache_read_tokens=0 AND f.cache_write_tokens=0 THEN f.prompt_tokens
		     WHEN lower(btrim(l.egress_protocol))='anthropic-messages'
		          THEN f.prompt_tokens+f.cache_read_tokens+f.cache_write_tokens
		     WHEN lower(btrim(l.egress_protocol)) IN ('openai-completions','openai-responses','gemini-generate')
		          AND f.cache_write_tokens=0 AND f.cache_read_tokens<=f.prompt_tokens THEN f.prompt_tokens
		     ELSE NULL END,
		f.reasoning_tokens, f.total_tokens, f.image_tokens, f.audio_tokens, f.video_tokens,
		CASE WHEN f.normalized_cost_currency='USD' THEN round(f.cost_amount*100)::bigint ELSE 0 END,
		f.cost_amount, f.normalized_cost_currency, f.credits_charged, f.status, f.traffic_class,
		f.error_kind, NULLIF(f.latency_ms,0), NULLIF(f.ttft_ms,0), NULLIF(f.usage_source,''), f.revision, f.finalized_at, f.fact_id, 'async_processor'
	FROM normalized f
	LEFT JOIN public.providers p ON p.id=f.provider_id
	LEFT JOIN public.credentials cred ON cred.id=f.credential_id AND cred.provider_id=f.provider_id
	LEFT JOIN public.api_keys k ON k.id=f.api_key_id AND k.tenant_id=f.tenant_id
	LEFT JOIN public.models_canonical mc ON mc.id=f.canonical_id
	LEFT JOIN LATERAL (
		SELECT a.gw_session_id, a.gw_task_id, a.virtual_client_id, a.owner_user, a.application_code, a.agent_name, a.client_ip, a.identity_hash
		FROM public.request_context_attrs a
		WHERE a.tenant_id=f.tenant_id AND a.request_id=f.request_id
		  AND a.ts >= f.occurred_at - interval '1 day' AND a.ts < f.occurred_at + interval '1 day'
		ORDER BY a.ts DESC LIMIT 1
	) c ON true
	LEFT JOIN LATERAL (
		SELECT r.gw_session_id, r.gw_task_id, r.attachment_count, r.client_protocol, r.egress_protocol, r.client_model, r.task_type, r.agent_name, r.client_ip, r.identity_hash, r.work_type, r.api_key_owner_user,
		       NULLIF(btrim(r.compression_strategy),'') IS NOT NULL AS compression_applied,
		       CASE WHEN r.stream_interrupted IS FALSE THEN false
		            WHEN r.stream_interrupted IS TRUE THEN lower(COALESCE(NULLIF(btrim(r.failure_detail_code),''),NULLIF(btrim(r.error_kind),''))) IN ('client_cancel','client_disconnected') END AS client_cancelled
		FROM public.request_logs_with_current_month r
		WHERE r.tenant_id=f.tenant_id AND r.request_id=f.request_id
		  AND r.ts >= f.occurred_at - interval '1 day' AND r.ts < f.occurred_at + interval '1 day'
		ORDER BY r.ts DESC LIMIT 1
	) l ON true
	LEFT JOIN public.session_summaries s ON s.tenant_id=f.tenant_id
		AND s.session_key=COALESCE(NULLIF(c.gw_session_id,''),NULLIF(l.gw_session_id,''))
	ON CONFLICT (request_id, occurred_at) DO UPDATE SET
		provider_id=EXCLUDED.provider_id, credential_id=EXCLUDED.credential_id, credential_label=EXCLUDED.credential_label,
		provider_code=EXCLUDED.provider_code, provider_name=EXCLUDED.provider_name,
		outbound_model=EXCLUDED.outbound_model, canonical_id=EXCLUDED.canonical_id,
		raw_model_name=EXCLUDED.raw_model_name, user_id=EXCLUDED.user_id,
		person_key=EXCLUDED.person_key, client_id=EXCLUDED.client_id,
		client_model=EXCLUDED.client_model, agent_name=EXCLUDED.agent_name, client_ip=EXCLUDED.client_ip,
		identity_hash=EXCLUDED.identity_hash, task_type=EXCLUDED.task_type, owner_user=EXCLUDED.owner_user,
		session_id=EXCLUDED.session_id, task_id=EXCLUDED.task_id, api_key_owner_user=EXCLUDED.api_key_owner_user,
		api_key_id=EXCLUDED.api_key_id, api_key_prefix=EXCLUDED.api_key_prefix,
		application_id=EXCLUDED.application_id, application_code=EXCLUDED.application_code,
		work_type=EXCLUDED.work_type, session_intent=EXCLUDED.session_intent, compression_applied=EXCLUDED.compression_applied, client_cancelled=EXCLUDED.client_cancelled,
		protocol=EXCLUDED.protocol, modality=EXCLUDED.modality, attachment_count=EXCLUDED.attachment_count,
		prompt_tokens=EXCLUDED.prompt_tokens, completion_tokens=EXCLUDED.completion_tokens,
		cached_tokens=EXCLUDED.cached_tokens, cache_write_tokens=EXCLUDED.cache_write_tokens, cache_input_tokens=EXCLUDED.cache_input_tokens,
		reasoning_tokens=EXCLUDED.reasoning_tokens, total_tokens=EXCLUDED.total_tokens,
		image_tokens=EXCLUDED.image_tokens, audio_tokens=EXCLUDED.audio_tokens, video_tokens=EXCLUDED.video_tokens,
		cost_cents=EXCLUDED.cost_cents, cost_amount=EXCLUDED.cost_amount, cost_currency=EXCLUDED.cost_currency,
		credits_charged=EXCLUDED.credits_charged, status=EXCLUDED.status, traffic_class=EXCLUDED.traffic_class,
		error_kind=EXCLUDED.error_kind, latency_ms=EXCLUDED.latency_ms, ttft_ms=EXCLUDED.ttft_ms,
		usage_source=EXCLUDED.usage_source, source_revision=EXCLUDED.source_revision,
		source_finalized_at=EXCLUDED.source_finalized_at, source_fact_id=EXCLUDED.source_fact_id,
		source=EXCLUDED.source, updated_at=clock_timestamp()
	WHERE rm.source_revision IS NULL OR (EXCLUDED.source_revision, EXCLUDED.source_finalized_at, EXCLUDED.source_fact_id)
		> (rm.source_revision, COALESCE(rm.source_finalized_at, '-infinity'::timestamptz), rm.source_fact_id)
		OR (rm.source_fact_id=EXCLUDED.source_fact_id AND rm.cost_currency IS NULL AND EXCLUDED.cost_currency='USD')
	RETURNING 1
), acknowledged AS (
	DELETE FROM public.request_metrics_pending q USING pending p
	WHERE (q.fact_id, q.occurred_at)=(p.fact_id,p.occurred_at)
	RETURNING 1
), head AS (
	SELECT fact_id, occurred_at FROM history ORDER BY fact_id DESC, occurred_at DESC LIMIT 1
), advanced AS (
	UPDATE public.request_metrics_projection_state s
	SET cursor_fact_id=COALESCE((SELECT fact_id FROM head),s.cursor_fact_id),
	    cursor_occurred_at=COALESCE((SELECT occurred_at FROM head),s.cursor_occurred_at),
	    backfill_complete=s.backfill_complete OR (SELECT count(*) FROM history)<$1,
	    updated_at=clock_timestamp()
	WHERE singleton
	RETURNING 1
)
SELECT (SELECT count(*) FROM history)+(SELECT count(*) FROM acknowledged)
`

func (p *Processor) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var locked bool
	if err := tx.QueryRowContext(ctx, lockProjectionSQL).Scan(&locked); errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var processed int
	if err := tx.QueryRowContext(ctx, projectBatchSQL, batchSize).Scan(&processed); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return processed, nil
}

func (p *Processor) Run(ctx context.Context, cfg Config) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			delay := cfg.Interval
			if p.processRound(ctx, cfg) {
				delay = min(delay, time.Second)
			}
			timer.Reset(delay)
		}
	}
}

// processRound returns true when it ended on a full batch and should retry soon.
func (p *Processor) processRound(ctx context.Context, cfg Config) bool {
	for range cfg.MaxRoundsPerTick {
		if ctx.Err() != nil {
			return false
		}
		job, cancel := context.WithTimeout(ctx, 30*time.Second)
		processed, err := p.ProcessBatch(job, cfg.BatchSize)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("request metrics projection failed", "error", err)
			}
			return false
		}
		if processed < cfg.BatchSize {
			return false
		}
	}
	return true
}

func (p *Processor) RunOnce(ctx context.Context, batchSize int) (int, error) {
	return p.ProcessBatch(ctx, batchSize)
}
