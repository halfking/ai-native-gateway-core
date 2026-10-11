-- 02 — 本地侧修正（外科补充腿）
-- 对账文档：同目录 README.md §4。全部幂等/additive；每节标注 creator 或来源。
-- 与 01（canonical 文件腿）配套执行：psql -X -v ON_ERROR_STOP=1 -f 02-local-fix-extra.sql "$DB"
\set ON_ERROR_STOP on

-- ╭ B1. sessions v2 列（creator 456；467 的 title/user_tags 已含于 456 形态）────
ALTER TABLE public.sessions
    ADD COLUMN IF NOT EXISTS last_full_request JSONB,
    ADD COLUMN IF NOT EXISTS last_full_response JSONB,
    ADD COLUMN IF NOT EXISTS last_full_payload_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS summary TEXT,
    ADD COLUMN IF NOT EXISTS summary_model TEXT,
    ADD COLUMN IF NOT EXISTS summary_generated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS summary_quality TEXT;
ALTER TABLE public.sessions ADD COLUMN IF NOT EXISTS user_tags text[];

-- ╭ B2. handoff_pending_confirmations → 已并入 01 腿的 527 文件投递（列+状态扩宽+restore 索引）

-- ╭ B3. prompt_injection_detections（creator 315；本地 0 行，直接 NOT NULL）────
ALTER TABLE public.prompt_injection_detections ADD COLUMN IF NOT EXISTS detection_score INT;
ALTER TABLE public.prompt_injection_detections ADD COLUMN IF NOT EXISTS matched_rules JSONB;
ALTER TABLE public.prompt_injection_detections ADD COLUMN IF NOT EXISTS matched_rules_count INT;
ALTER TABLE public.prompt_injection_detections ADD COLUMN IF NOT EXISTS detection_layers JSONB;
DO $$BEGIN
  ALTER TABLE public.prompt_injection_detections ALTER COLUMN detection_score SET NOT NULL;
  ALTER TABLE public.prompt_injection_detections ALTER COLUMN matched_rules_count SET NOT NULL;
EXCEPTION WHEN others THEN RAISE WARNING 'pid set not null: %', SQLERRM; END$$;

-- ╭ B4. request_logs_hot 续传族列（creator 603，逐列照抄）────────────────────
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS cached_response_id BIGINT;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS context_size_tokens INTEGER;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS continuation_keywords TEXT[];
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS effective_timeout_seconds INTEGER;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS is_continuation BOOLEAN DEFAULT FALSE;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS keepalive_sent_count INTEGER DEFAULT 0;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS node_switch_count INTEGER DEFAULT 0;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS timeout_mode VARCHAR(50);

-- ╭ B5. 默认值对齐（creator 479 / 804）────────────────────────────────────
ALTER TABLE public.credentials ALTER COLUMN concurrency_mode SET DEFAULT 'concurrency';
-- 479 canonical 终态含 SET NOT NULL + 4 值 CHECK（审计 P1-3 更正：生产=canonical，本地欠账）
DO $$BEGIN
  ALTER TABLE public.credentials ALTER COLUMN concurrency_mode SET NOT NULL;
EXCEPTION WHEN others THEN RAISE WARNING 'concurrency_mode set not null: %', SQLERRM; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='credentials_concurrency_mode_check') THEN
  ALTER TABLE public.credentials ADD CONSTRAINT credentials_concurrency_mode_check
    CHECK ((concurrency_mode = ANY (ARRAY['concurrency'::text, 'rpm'::text, 'tpm'::text, 'disabled'::text])));
END IF; END$$;
ALTER TABLE public.credential_model_bindings ALTER COLUMN context_window_source SET DEFAULT 'catalog';

-- ╭ B6. free_resource_catalog.tos_verdict 维持生产形态（nullable DEFAULT 'caution'）
ALTER TABLE public.free_resource_catalog ALTER COLUMN tos_verdict DROP NOT NULL;
ALTER TABLE public.free_resource_catalog ALTER COLUMN tos_verdict SET DEFAULT 'caution';

-- ╭ B7. auto_combo_templates（列集/默认/唯一 = 生产实况，creator 缺失 prod-manual）──
ALTER TABLE public.auto_combo_templates ALTER COLUMN variant SET DEFAULT 'cheap';
ALTER TABLE public.auto_combo_templates ALTER COLUMN free_type_filter SET DEFAULT '{}'::text[];
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS template_key text;
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS display_name_en text;
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS notes text;
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS sort_by text DEFAULT 'health'::text;
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS denylist_codes text[] DEFAULT '{}'::text[];
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS pool_keys text[] DEFAULT '{}'::text[];
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS tier_preference text[] DEFAULT ARRAY['standard'::text, 'premium'::text, 'keyless'::text];
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS capability_modalities text[] DEFAULT ARRAY['text-to-text'::text];
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS capability_image_generation boolean;
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS capability_tool_use boolean;
ALTER TABLE public.auto_combo_templates ADD COLUMN IF NOT EXISTS capability_vision boolean;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='auto_combo_templates_tenant_id_template_key_key') THEN
  ALTER TABLE public.auto_combo_templates ADD CONSTRAINT auto_combo_templates_tenant_id_template_key_key UNIQUE (tenant_id, template_key); END IF; END$$;

-- ╭ B8. free_resource_catalog 能力批列（生产实况，creator 缺失 prod-manual）──────
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS allowlist_in_auto_combo boolean DEFAULT false;
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS api_endpoint text;
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS capability_modalities text[] DEFAULT ARRAY['text-to-text'::text];
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS capability_tool_use boolean DEFAULT false;
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS capability_vision boolean DEFAULT false;
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS last_probe_at timestamptz;
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS notes text;
ALTER TABLE public.free_resource_catalog ADD COLUMN IF NOT EXISTS signup_url text;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='free_resource_catalog_tenant_id_provider_code_model_id_key') THEN
  ALTER TABLE public.free_resource_catalog ADD CONSTRAINT free_resource_catalog_tenant_id_provider_code_model_id_key UNIQUE (tenant_id, provider_code, model_id); END IF; END$$;

-- ╭ B9. stats 两表（creator 544 / 539；resolution 检查=548 值域生产实况）────────
ALTER TABLE public.stats_adjustments ADD COLUMN IF NOT EXISTS adjustment_type text NOT NULL DEFAULT 'reconciliation';
ALTER TABLE public.stats_adjustments ADD COLUMN IF NOT EXISTS metric_name text;
ALTER TABLE public.stats_reconciliation_diffs ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'default';
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='stats_reconciliation_diffs_resolution_check') THEN
  ALTER TABLE public.stats_reconciliation_diffs ADD CONSTRAINT stats_reconciliation_diffs_resolution_check CHECK ((resolution = ANY (ARRAY['open'::text, 'auto_repair_pending'::text, 'auto_repaired'::text, 'phantom_open'::text, 'rejected'::text, 'approved'::text, 'adjusted'::text])));
END IF; END$$;

-- ╭ B10. request_state_transitions 约束族（creator 511+，生产实况=已应用 canonical。
--       本地 8695 行：逐条 DO 包裹 + NOT VALID + 独立 VALIDATE，违约降级 WARNING 不中断）

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='chk_request_state_transitions_attempt_no') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT chk_request_state_transitions_attempt_no CHECK (((attempt_no IS NULL) OR (attempt_no >= 1))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT chk_request_state_transitions_attempt_no; EXCEPTION WHEN others THEN RAISE WARNING 'validate chk_request_state_transitions_attempt_no: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='chk_request_state_transitions_transition_type') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT chk_request_state_transitions_transition_type CHECK ((transition_type = ANY (ARRAY['route'::text, 'node_switch'::text, 'retry'::text, 'error'::text, 'state'::text, 'survival_accepted'::text, 'survival_running'::text, 'survival_waiting'::text, 'survival_connection_detached'::text, 'survival_retry'::text, 'survival_recovered'::text, 'survival_completed'::text, 'survival_permanent_failed'::text, 'survival_expired'::text, 'survival_cancelled'::text, 'survival_resume_safety_blocked'::text]))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT chk_request_state_transitions_transition_type; EXCEPTION WHEN others THEN RAISE WARNING 'validate chk_request_state_transitions_transition_type: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_attempt_ref_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_attempt_ref_chk CHECK (((event_type IS NULL) OR (((attempt_id IS NULL) AND (attempt_no IS NULL)) OR ((attempt_id IS NOT NULL) AND (length(btrim(attempt_id)) > 0) AND (attempt_no IS NOT NULL) AND (attempt_no > 0))))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_attempt_ref_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_attempt_ref_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_canceled_outcome_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_canceled_outcome_chk CHECK (((event_type IS NULL) OR (event_type <> 'request_canceled'::text) OR ((outcome IS NOT NULL) AND (outcome = 'canceled'::text)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_canceled_outcome_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_canceled_outcome_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_degraded_event_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_degraded_event_chk CHECK (((event_type IS NULL) OR (event_type <> 'observation_degraded'::text) OR ((observation_status IS NOT NULL) AND (observation_status = 'observation_degraded'::text)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_degraded_event_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_degraded_event_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_event_fields_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_event_fields_chk CHECK (((event_type IS NULL) OR (event_type <> ALL (ARRAY['credential_selected'::text, 'node_enqueued'::text, 'node_selected'::text])) OR ((credential_id IS NOT NULL) AND (credential_id > 0)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_event_fields_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_event_fields_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_event_kind_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_event_kind_chk CHECK (((transition_type IS NOT NULL) OR (event_type IS NOT NULL))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_event_kind_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_event_kind_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_event_type_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_event_type_chk CHECK (((event_type IS NULL) OR (event_type = ANY (ARRAY['request_received'::text, 'route_resolved'::text, 'model_enqueued'::text, 'credential_selected'::text, 'node_enqueued'::text, 'node_selected'::text, 'attempt_started'::text, 'first_byte'::text, 'attempt_succeeded'::text, 'attempt_failed'::text, 'retry_scheduled'::text, 'node_switched'::text, 'model_switched'::text, 'request_succeeded'::text, 'request_failed'::text, 'request_canceled'::text, 'observation_degraded'::text])))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_event_type_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_event_type_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_http_status_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_http_status_chk CHECK (((http_status IS NULL) OR ((http_status >= 100) AND (http_status <= 599)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_http_status_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_http_status_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_journey_no_metadata_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_journey_no_metadata_chk CHECK (((event_type IS NULL) OR ((metadata IS NULL) AND (from_state IS NULL) AND (to_state IS NULL)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_journey_no_metadata_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_journey_no_metadata_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_journey_required_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_journey_required_chk CHECK (((event_type IS NULL) OR ((length(btrim(tenant_id)) > 0) AND (gateway_instance_id IS NOT NULL) AND (length(btrim(gateway_instance_id)) > 0) AND (length(btrim(request_id)) > 0) AND (seq IS NOT NULL) AND (seq > 0) AND (stage IS NOT NULL) AND (occurred_at IS NOT NULL) AND (observation_status IS NOT NULL)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_journey_required_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_journey_required_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_journey_stage_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_journey_stage_chk CHECK (((stage IS NULL) OR (stage = ANY (ARRAY['received'::text, 'routing'::text, 'model_queue'::text, 'credential_queue'::text, 'node_selection'::text, 'upstream'::text, 'streaming'::text, 'retrying'::text, 'terminal'::text])))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_journey_stage_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_journey_stage_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_node_health_status_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_node_health_status_chk CHECK (((node_health_status IS NULL) OR (node_health_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'suspect'::text, 'degraded'::text, 'cooling'::text, 'probing'::text, 'recovering'::text, 'quarantined'::text, 'disabled'::text, 'unhealthy'::text])))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_node_health_status_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_node_health_status_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_observation_status_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_observation_status_chk CHECK (((observation_status IS NULL) OR (observation_status = ANY (ARRAY['complete'::text, 'observation_degraded'::text])))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_observation_status_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_observation_status_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_outcome_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_outcome_chk CHECK (((outcome IS NULL) OR (outcome = ANY (ARRAY['success'::text, 'failure'::text, 'canceled'::text])))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_outcome_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_outcome_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_provider_credential_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_provider_credential_chk CHECK ((((provider_id IS NULL) OR (provider_id > 0)) AND ((credential_id IS NULL) OR (credential_id > 0)) AND ((from_credential_id IS NULL) OR (from_credential_id > 0)) AND ((to_credential_id IS NULL) OR (to_credential_id > 0)))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_provider_credential_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_provider_credential_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_retry_at_event_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_retry_at_event_chk CHECK (((retry_at IS NULL) OR (event_type = 'retry_scheduled'::text))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_retry_at_event_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_retry_at_event_chk: %', SQLERRM; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='request_state_transitions_switch_fields_chk') THEN
  ALTER TABLE IF EXISTS public.request_state_transitions ADD CONSTRAINT request_state_transitions_switch_fields_chk CHECK (((event_type IS NULL) OR (((event_type <> 'node_switched'::text) OR ((from_credential_id IS NOT NULL) AND (from_credential_id > 0) AND (to_credential_id IS NOT NULL) AND (to_credential_id > 0))) AND ((event_type <> 'model_switched'::text) OR ((from_model IS NOT NULL) AND (length(btrim(from_model)) > 0) AND (to_model IS NOT NULL) AND (length(btrim(to_model)) > 0)))))) NOT VALID;
END IF; END$$;
DO $$BEGIN ALTER TABLE public.request_state_transitions VALIDATE CONSTRAINT request_state_transitions_switch_fields_chk; EXCEPTION WHEN others THEN RAISE WARNING 'validate request_state_transitions_switch_fields_chk: %', SQLERRM; END$$;

-- ╭ B11. session_bodies/session_turns canonical 租户唯一（creator 476；旧键保留不删）

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_turns_tenant_request_partition_key' AND conrelid='session_turns'::regclass) THEN ALTER TABLE public.session_turns ADD CONSTRAINT session_turns_tenant_request_partition_key UNIQUE (tenant_id, request_id, partition_date); END IF; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_turns_tenant_session_turn_partition_key' AND conrelid='session_turns'::regclass) THEN ALTER TABLE public.session_turns ADD CONSTRAINT session_turns_tenant_session_turn_partition_key UNIQUE (tenant_id, session_id, turn_no, partition_date); END IF; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_bodies_tenant_request_partition_key' AND conrelid='session_bodies'::regclass) THEN ALTER TABLE public.session_bodies ADD CONSTRAINT session_bodies_tenant_request_partition_key UNIQUE (tenant_id, request_id, partition_date); END IF; END$$;

DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_bodies_tenant_session_turn_partition_key' AND conrelid='session_bodies'::regclass) THEN ALTER TABLE public.session_bodies ADD CONSTRAINT session_bodies_tenant_session_turn_partition_key UNIQUE (tenant_id, session_id, turn_no, partition_date); END IF; END$$;

-- ╭ C. 缺失索引（creator 见注释；生产定义内嵌，加 IF NOT EXISTS）──────────────

-- creator 478p2
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_auto_task_ts ON public.request_logs_hot USING btree (task_type, ts DESC) WHERE (is_auto_request IS TRUE);

-- creator 511
CREATE INDEX IF NOT EXISTS idx_state_transitions_created ON public.request_state_transitions USING btree (created_at);

-- creator 476
CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_session_turn ON public.session_turns USING btree (tenant_id, session_id, turn_no DESC);

-- creator 476
CREATE INDEX IF NOT EXISTS idx_session_bodies_tenant_session_turn ON public.session_bodies USING btree (tenant_id, session_id, turn_no DESC);

-- creator 614
CREATE INDEX IF NOT EXISTS idx_session_bodies_hot_lookup ON public.session_bodies_hot USING btree (session_id, turn_no, tenant_id);

-- creator 614
CREATE INDEX IF NOT EXISTS idx_session_bodies_hot_request ON public.session_bodies_hot USING btree (request_id, tenant_id);

-- creator 526
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_ts ON public.session_turns_hot USING btree (ts, id, partition_date);

-- creator 526
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_session_turn ON public.session_turns_hot USING btree (tenant_id, session_id, turn_no DESC);

-- creator 526
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_t0_arrived ON public.session_turns_hot USING btree (tenant_id, t0_arrived_at DESC) WHERE (t0_arrived_at IS NOT NULL);

-- creator 526
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_attachment_count ON public.session_turns_hot USING btree (tenant_id, attachment_count) WHERE (attachment_count > 0);

-- creator 526
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_multimodal_types ON public.session_turns_hot USING gin (multimodal_types) WHERE (multimodal_types <> '{}'::text[]);

-- creator 718
CREATE INDEX IF NOT EXISTS candidate_failure_logs_hot_session_id_ts_idx ON public.candidate_failure_logs_hot USING btree (session_id, ts DESC);

-- creator 544
CREATE INDEX IF NOT EXISTS idx_stats_adjustments_type ON public.stats_adjustments USING btree (tenant_id, adjustment_type, month_start DESC);

-- creator 548
CREATE UNIQUE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_run_tenant_dim_metric ON public.stats_reconciliation_diffs USING btree (run_id, tenant_id, dimension_type, dimension_key, metric);

-- creator 539
CREATE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_tenant ON public.stats_reconciliation_diffs USING btree (tenant_id, created_at DESC);

-- idx_handoff_pending_proposal_created_at：纯性能索引，本轮暂缓（后续按 518/621/478p2 形态补）
-- idx_handoff_pending_restore：由 01 腿 527 文件覆盖（本腿不重复）
-- idx_ped_resolved_updated_at：纯性能索引，本轮暂缓（后续按 518/621/478p2 形态补）
-- idx_request_logs_hot_gw_session_id：纯性能索引，本轮暂缓（后续按 518/621/478p2 形态补）
-- creator 474：session_turns 附件计数索引（本地分区父表，自动级联子分区）
CREATE INDEX IF NOT EXISTS idx_session_turns_attachment_count_gw
  ON public.session_turns(attachment_count)
  WHERE attachment_count > 0;

-- ╭ C2. 605 谓词修复（TSV✗ chan✗「从未登记」实例；本地真缺陷）──────────────
DROP INDEX IF EXISTS public.idx_request_logs_provider_tool_calls;
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
    ON public.request_logs (provider_id, ts DESC)
    WHERE tool_calls IS NOT NULL
      AND jsonb_typeof(tool_calls) = 'array'
      AND jsonb_array_length(tool_calls) > 0;

-- ╭ C3. providers FK 校验（§3.4：本地 NOT VALID、生产 VALID；小表零锁险）
DO $$BEGIN
  ALTER TABLE public.providers VALIDATE CONSTRAINT providers_proxy_subscription_id_fkey;
EXCEPTION WHEN others THEN RAISE WARNING 'validate providers fkey: %', SQLERRM; END$$;

-- ╭ D. 缺失函数（来源标注）───────────────────────────────────────────────

-- 来源: 830.skip（生产手工分区的配套分区器，本地代码 URSM 路径在调）
CREATE OR REPLACE FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(p_date date)
 RETURNS void
 LANGUAGE plpgsql
 SET "TimeZone" TO 'Asia/Shanghai'
AS $function$
DECLARE
    pname    TEXT := format('ursm_node_snapshot_min_%s', to_char(p_date, 'YYYYMMDD'));
    start_ts TIMESTAMPTZ := p_date::timestamptz;
    end_ts   TIMESTAMPTZ := (p_date + INTERVAL '1 day')::timestamptz;
BEGIN
    -- 幂等短路：分区已挂接则无事可做。
    IF EXISTS (
        SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 并发串行化：boot ensure / installer / 升级通道 / 24h tick 在
    -- 252 共享库上会并发。
    PERFORM pg_advisory_xact_lock(
        hashtext('ensure_ursm_node_snapshot_min_daily_partition:' || pname));

    -- 二次短路：拿到锁后可能已被别的入口建好。
    -- ★ 短路条件必须是「已挂在**本父表**下」，不是「同名对象存在」。
    --   本地 PG 17.11 往返实测（up→down→up）暴露的静默故障：
    --   `ursm_node_snapshot_min_YYYYMMDD` 仍挂在 _post825 下时，
    --   按名字短路会让新建的父表**一个分区都没有**；随后每次写入都报
    --      ERROR: no partition of relation "ursm_node_snapshot_min" found for row
    --   而迁移 RC=0、ensure 返回成功、日志无任何异常 ——
    --   静默 + 致命，且要等到真的写入才暴露。
    IF EXISTS (
        SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 同名对象存在却不挂在本父表下 ⇒ 明确报错，绝不静默跳过。
    --   「宁可吵，也不要安静地留一个零分区的分区父表」。
    IF to_regclass('public.' || pname) IS NOT NULL THEN
        RAISE EXCEPTION
            '分区名 % 已被占用，但它不是 public.ursm_node_snapshot_min 的子分区。'
            '若直接跳过，新建的父表将没有任何分区，之后每次写入都会报 '
            'no partition of relation found，而迁移仍会报成功。'
            '请先 DROP 或改名该对象（常见来源：830.down 保留的 _post825）。',
            pname;
    END IF;

    -- 本表无 DEFAULT 分区 ⇒ 不存在 750 踩过的「DEFAULT 越界行」约束校验，
    -- 可以直接 PARTITION OF。lock_timeout 沿用同事务设置（5s 由调用方钉）。
    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.ursm_node_snapshot_min
         FOR VALUES FROM (%L) TO (%L)',
        pname, start_ts, end_ts);
END;
$function$;
-- 来源: baseline 契约函数（baseline_ensure_functions_contract_test 引用，repo creator 失落，取生产定义）
CREATE OR REPLACE FUNCTION public.ensure_handoff_logs_partitions(p_month timestamp with time zone DEFAULT date_trunc('month'::text, now()))
 RETURNS integer
 LANGUAGE plpgsql
AS $function$
DECLARE
    m0 timestamp := date_trunc('month', p_month AT TIME ZONE 'Asia/Shanghai');
    m1 timestamp := m0 + interval '1 month';
    m2 timestamp := m0 + interval '2 months';
    cur_name  text := 'handoff_logs_' || to_char(p_month, 'YYYY_MM');
    next_name text := 'handoff_logs_' || to_char(p_month + interval '1 month', 'YYYY_MM');
    created int := 0;
BEGIN
    IF to_regclass('public.' || cur_name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.handoff_logs FOR VALUES FROM (%L) TO (%L) USING columnar WITH (autovacuum_enabled=''true'', autovacuum_vacuum_scale_factor=''0.05'', autovacuum_vacuum_threshold=''10'', autovacuum_analyze_scale_factor=''0.02'', autovacuum_analyze_threshold=''50'')',
            cur_name, m0, m1);
        EXECUTE format('CREATE INDEX %I ON public.%I (session_id, created_at DESC)',     cur_name || '_session_idx',      cur_name);
        EXECUTE format('CREATE INDEX %I ON public.%I (tenant_id, created_at DESC)',      cur_name || '_tenant_idx',       cur_name);
        EXECUTE format('CREATE INDEX %I ON public.%I (trigger_reason, created_at DESC)', cur_name || '_trigger_mode_idx', cur_name);
        created := created + 1;
    END IF;

    IF to_regclass('public.' || next_name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.handoff_logs FOR VALUES FROM (%L) TO (%L) USING columnar WITH (autovacuum_enabled=''true'', autovacuum_vacuum_scale_factor=''0.05'', autovacuum_vacuum_threshold=''10'', autovacuum_analyze_scale_factor=''0.02'', autovacuum_analyze_threshold=''50'')',
            next_name, m1, m2);
        EXECUTE format('CREATE INDEX %I ON public.%I (session_id, created_at DESC)',     next_name || '_session_idx',      next_name);
        EXECUTE format('CREATE INDEX %I ON public.%I (tenant_id, created_at DESC)',      next_name || '_tenant_idx',       next_name);
        EXECUTE format('CREATE INDEX %I ON public.%I (trigger_reason, created_at DESC)', next_name || '_trigger_mode_idx', next_name);
        created := created + 1;
    END IF;

    RETURN created;
END;
$function$;
-- 来源: 580
CREATE OR REPLACE FUNCTION public.archive_session_module_executions(retention_days integer DEFAULT 7)
 RETURNS TABLE(archived_count bigint)
 LANGUAGE plpgsql
AS $function$
DECLARE
    v_count BIGINT;
BEGIN
    -- 将过期数据插入分区表
    WITH archived AS (
        DELETE FROM session_module_executions_hot
        WHERE created_at < NOW() - (retention_days || ' days')::INTERVAL
        RETURNING *
    )
    INSERT INTO session_module_executions
    SELECT * FROM archived;
    
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN QUERY SELECT v_count;
END;
$function$;
-- 来源: 642
CREATE OR REPLACE FUNCTION public.replace_credential_model_index_row()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    lock_key bigint;
BEGIN
    -- Normalize empty / NULL raw_model to the sentinel. Real
    -- raw_model values are slugified like 'gpt-4o', 'claude-opus-4-7',
    -- etc., and never start with '__', so this rewrite is safe.
    IF NEW.raw_model IS NULL OR NEW.raw_model = '' THEN
        NEW.raw_model := '__empty_raw_model__';
    END IF;

    -- Serialize concurrent rollups targeting the same
    -- (bucket, credential_id, raw_model) tuple. The lock is
    -- released automatically at COMMIT/ROLLBACK.
    lock_key := hashtext(
        NEW.bucket::text || ':' ||
        NEW.credential_id::text || ':' ||
        NEW.raw_model
    )::bigint;
    PERFORM pg_advisory_xact_lock(lock_key);

    DELETE FROM public.credential_model_index_hot
    WHERE bucket = NEW.bucket
      AND credential_id = NEW.credential_id
      AND raw_model = NEW.raw_model;

    RETURN NEW;
END;
$function$;
-- 来源: 433
CREATE OR REPLACE FUNCTION public.get_system_snapshot(p_instance_id text DEFAULT NULL::text, p_hours_ago integer DEFAULT 1)
 RETURNS TABLE(metric_timestamp timestamp with time zone, cpu_usage_pct real, mem_usage_pct real, disk_usage_pct real, current_concurrency integer, tps real, p50_ms real, p99_ms real, success_pct real)
 LANGUAGE plpgsql
 STABLE
AS $function$
BEGIN
    RETURN QUERY
    SELECT
        rm.timestamp AS metric_timestamp,
        rm.cpu_usage_pct,
        (rm.mem_used_mb::float / NULLIF(rm.mem_total_mb, 0) * 100)::REAL AS mem_usage_pct,
        (rm.disk_used_gb::float / NULLIF(rm.disk_total_gb, 0) * 100)::REAL AS disk_usage_pct,
        rm.current_concurrency,
        rm.last_5min_tps,
        rm.last_5min_p50_ms,
        rm.last_5min_p99_ms,
        rm.last_5min_success_pct
    FROM runtime_metrics rm
    WHERE rm.timestamp >= now() - (p_hours_ago || ' hours')::INTERVAL
      AND (p_instance_id IS NULL OR rm.instance_id = p_instance_id)
    ORDER BY rm.timestamp DESC;
END;
$function$;
-- 来源: creator 失落（644.down 痕迹），取生产定义
CREATE OR REPLACE FUNCTION public.sanitize_model_integrity_jsonb()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    v_text text;
BEGIN
    IF NEW.request_payload IS NOT NULL
       AND NEW.request_payload::text !~ '^[0-9]+'
       AND NEW.request_payload::text ~ '\\(?![bfnrtu"/\\])' THEN
        v_text := NEW.request_payload::text;
        v_text := regexp_replace(v_text, '\\(?![bfnrtu"/\\])', '\\u005c', 'g');
        BEGIN
            NEW.request_payload := v_text::jsonb;
        EXCEPTION WHEN others THEN
            -- Leave as-is; the INSERT will fail with the original
            -- jsonb syntax error so operators see the row in logs.
            NEW.request_payload := NULL;
        END;
    END IF;

    IF NEW.response_payload IS NOT NULL
       AND NEW.response_payload::text !~ '^[0-9]+'
       AND NEW.response_payload::text ~ '\\(?![bfnrtu"/\\])' THEN
        v_text := NEW.response_payload::text;
        v_text := regexp_replace(v_text, '\\(?![bfnrtu"/\\])', '\\u005c', 'g');
        BEGIN
            NEW.response_payload := v_text::jsonb;
        EXCEPTION WHEN others THEN
            NEW.response_payload := NULL;
        END;
    END IF;

    RETURN NEW;
END;
$function$;
-- 来源: creator 失落（655.down 痕迹），取生产定义
CREATE OR REPLACE FUNCTION public.sync_session_task_id()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
  -- 只处理有 session_id 和 task_id 的请求
  IF NEW.gw_session_id IS NOT NULL 
     AND TRIM(NEW.gw_session_id) != ''
     AND NEW.gw_task_id IS NOT NULL 
     AND TRIM(NEW.gw_task_id) != '' THEN
    
    -- 如果 session_summaries 中该会话的 gw_task_id 为空，则更新
    UPDATE session_summaries
    SET gw_task_id = NEW.gw_task_id,
        updated_at = NOW()
    WHERE session_key = NEW.gw_session_id
      AND gw_task_id IS NULL;
  END IF;
  
  RETURN NEW;
END;
$function$;
-- ╭ D2. 缺失视图（302/364 creator 5 个 + prod-manual 报表 4 个）───────────────

-- 来源: 302
CREATE OR REPLACE VIEW public.v_unified_probe_failing AS
SELECT mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, ''::text) AS outbound_model_name,
    COALESCE(p.base_url, ''::text) AS base_url,
    COALESCE(p.protocol, 'openai-completions'::text) AS protocol,
    mps.consecutive_failures,
    mps.last_err_code,
    mps.next_retry_at,
    'failing'::text AS priority,
    3 AS priority_order
   FROM (((model_probe_state mps
     JOIN credentials c ON ((c.id = mps.credential_id)))
     JOIN providers p ON ((p.id = c.provider_id)))
     JOIN provider_models pm ON ((pm.raw_model_name = mps.raw_model_name)))
  WHERE ((mps.state = 'failing'::text) AND (mps.probe_priority = 'failing'::text) AND (mps.next_retry_at <= now()) AND (COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (EXISTS ( SELECT 1
           FROM credential_model_bindings cmb
          WHERE ((cmb.credential_id = mps.credential_id) AND (cmb.provider_model_id = pm.id)))))
  ORDER BY mps.next_retry_at
 LIMIT 20;

-- 来源: 302
CREATE OR REPLACE VIEW public.v_unified_probe_suspicious AS
SELECT mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, ''::text) AS outbound_model_name,
    COALESCE(p.base_url, ''::text) AS base_url,
    COALESCE(p.protocol, 'openai-completions'::text) AS protocol,
    mps.marked_suspicious_at,
    mps.last_unavailable_reason,
    'suspicious'::text AS priority,
    2 AS priority_order
   FROM (((model_probe_state mps
     JOIN credentials c ON ((c.id = mps.credential_id)))
     JOIN providers p ON ((p.id = c.provider_id)))
     JOIN provider_models pm ON ((pm.raw_model_name = mps.raw_model_name)))
  WHERE ((mps.state = 'suspicious'::text) AND (mps.probe_priority = 'suspicious'::text) AND (mps.next_retry_at <= now()) AND (COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (EXISTS ( SELECT 1
           FROM credential_model_bindings cmb
          WHERE ((cmb.credential_id = mps.credential_id) AND (cmb.provider_model_id = pm.id)))))
  ORDER BY mps.marked_suspicious_at
 LIMIT 30;

-- 来源: 302
CREATE OR REPLACE VIEW public.v_unified_probe_urgent AS
SELECT mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, ''::text) AS outbound_model_name,
    COALESCE(p.base_url, ''::text) AS base_url,
    COALESCE(p.protocol, 'openai-completions'::text) AS protocol,
    mps.marked_suspicious_at,
    mps.last_unavailable_reason,
    'urgent'::text AS priority,
    1 AS priority_order
   FROM (((model_probe_state mps
     JOIN credentials c ON ((c.id = mps.credential_id)))
     JOIN providers p ON ((p.id = c.provider_id)))
     JOIN provider_models pm ON ((pm.raw_model_name = mps.raw_model_name)))
  WHERE ((mps.state = 'suspicious'::text) AND (mps.probe_priority = 'urgent'::text) AND (mps.next_retry_at <= now()) AND (COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (EXISTS ( SELECT 1
           FROM credential_model_bindings cmb
          WHERE ((cmb.credential_id = mps.credential_id) AND (cmb.provider_model_id = pm.id)))))
  ORDER BY mps.marked_suspicious_at
 LIMIT 20;

-- 来源: 302
CREATE OR REPLACE VIEW public.v_unified_probe_watchdog AS
SELECT mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, ''::text) AS outbound_model_name,
    COALESCE(p.base_url, ''::text) AS base_url,
    COALESCE(p.protocol, 'openai-completions'::text) AS protocol,
    mps.last_verified_at,
    mps.verification_interval,
    mps.success_rate_7d,
    'watchdog'::text AS priority,
    4 AS priority_order
   FROM (((model_probe_state mps
     JOIN credentials c ON ((c.id = mps.credential_id)))
     JOIN providers p ON ((p.id = c.provider_id)))
     JOIN provider_models pm ON ((pm.raw_model_name = mps.raw_model_name)))
  WHERE ((mps.state = 'healthy'::text) AND (mps.probe_priority = 'watchdog'::text) AND ((mps.last_verified_at + mps.verification_interval) <= now()) AND (COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (EXISTS ( SELECT 1
           FROM credential_model_bindings cmb
          WHERE ((cmb.credential_id = mps.credential_id) AND (cmb.provider_model_id = pm.id)))))
  ORDER BY mps.last_verified_at
 LIMIT 50;

-- 来源: 364
CREATE OR REPLACE VIEW public.prompt_injection_stats_enhanced AS
SELECT tenant_id,
    count(*) AS total_detections,
    count(*) FILTER (WHERE (blocked = true)) AS blocked_count,
    count(*) FILTER (WHERE ((risk_level)::text = 'critical'::text)) AS critical_count,
    count(*) FILTER (WHERE ((risk_level)::text = 'high'::text)) AS high_count,
    count(*) FILTER (WHERE ((risk_level)::text = 'medium'::text)) AS medium_count,
    count(*) FILTER (WHERE ((risk_level)::text = 'low'::text)) AS low_count,
    count(*) FILTER (WHERE ((action_taken)::text = 'approve'::text)) AS approval_count,
    count(*) FILTER (WHERE ((action_taken)::text = 'replace'::text)) AS replaced_count,
    count(*) FILTER (WHERE ((action_taken)::text = 'terminate'::text)) AS terminated_count,
    count(*) FILTER (WHERE (canary_token_leaked IS NOT NULL)) AS canary_leak_count,
    avg(detection_score) AS avg_score,
    max(detection_score) AS max_score,
    avg(llm_confidence) AS avg_llm_confidence,
    count(DISTINCT session_key) AS affected_sessions
   FROM prompt_injection_detections d
  WHERE (detected_at >= CURRENT_DATE)
  GROUP BY tenant_id;

-- 来源: prod-manual 报表
CREATE OR REPLACE VIEW public.v_task_summary AS
SELECT tenant_id,
    gw_task_id,
    gw_project_id,
    count(DISTINCT session_key) AS session_count,
    min(first_request_at) AS task_started_at,
    max(last_request_at) AS task_last_activity_at,
    (EXTRACT(epoch FROM (max(last_request_at) - min(first_request_at))))::integer AS task_duration_seconds,
    sum(request_count) AS total_requests,
    sum(success_count) AS total_success,
    sum(error_count) AS total_errors,
    sum(total_cost_usd) AS total_cost_usd,
    sum(total_tokens) AS total_tokens,
    (avg(avg_latency_ms))::integer AS avg_latency_ms,
        CASE
            WHEN bool_and(((session_status)::text = 'completed'::text)) THEN 'completed'::text
            WHEN bool_or(((session_status)::text = 'active'::text)) THEN 'in_progress'::text
            ELSE 'abandoned'::text
        END AS task_status,
    array_agg(title ORDER BY first_request_at) FILTER (WHERE (title IS NOT NULL)) AS session_titles,
    array_agg(session_key ORDER BY first_request_at) AS session_keys
   FROM session_summaries s
  WHERE (gw_task_id IS NOT NULL)
  GROUP BY tenant_id, gw_task_id, gw_project_id;

-- 来源: prod-manual 报表
CREATE OR REPLACE VIEW public.v_daily_session_costs AS
SELECT tenant_id,
    gw_project_id,
    gw_task_id,
    date(first_request_at) AS date,
    count(DISTINCT session_key) AS session_count,
    sum(request_count) AS total_requests,
    sum(total_cost_usd) AS total_cost_usd,
    sum(total_tokens) AS total_tokens,
    (avg(avg_latency_ms))::integer AS avg_latency_ms
   FROM session_summaries s
  GROUP BY tenant_id, gw_project_id, gw_task_id, (date(first_request_at));

-- 来源: prod-manual 报表
CREATE OR REPLACE VIEW public.v_project_summary AS
SELECT tenant_id,
    gw_project_id,
    count(DISTINCT gw_task_id) AS task_count,
    count(DISTINCT session_key) AS session_count,
    min(first_request_at) AS project_started_at,
    max(last_request_at) AS project_last_activity_at,
    (EXTRACT(epoch FROM (max(last_request_at) - min(first_request_at))))::integer AS project_duration_seconds,
    sum(request_count) AS total_requests,
    sum(success_count) AS total_success,
    sum(error_count) AS total_errors,
    sum(total_cost_usd) AS total_cost_usd,
    sum(total_tokens) AS total_tokens,
    (avg(avg_latency_ms))::integer AS avg_latency_ms,
        CASE
            WHEN bool_and(((session_status)::text = 'completed'::text)) THEN 'completed'::text
            WHEN bool_or(((session_status)::text = 'active'::text)) THEN 'in_progress'::text
            ELSE 'stalled'::text
        END AS project_status
   FROM session_summaries s
  WHERE (gw_project_id IS NOT NULL)
  GROUP BY tenant_id, gw_project_id;

-- 来源: prod-manual 报表
CREATE OR REPLACE VIEW public.v_session_task_id_coverage AS
SELECT count(*) AS total_sessions,
    count(gw_task_id) AS sessions_with_task_id,
    (count(*) - count(gw_task_id)) AS sessions_without_task_id,
    round(((100.0 * (count(gw_task_id))::numeric) / (count(*))::numeric), 2) AS coverage_percentage
   FROM session_summaries;


-- ╭ D3. 缺失触发器（审计 P1-4 更正：触发器并非零差异；来源=生产实况，函数在 D 节已建/本地已在）
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='trg_credential_model_index_hot_replace' AND tgrelid='credential_model_index_hot'::regclass) THEN
  CREATE TRIGGER trg_credential_model_index_hot_replace BEFORE INSERT ON public.credential_model_index_hot
    FOR EACH ROW EXECUTE FUNCTION replace_credential_model_index_row();
END IF; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='trg_sync_session_task_id' AND tgrelid='request_logs_hot'::regclass) THEN
  CREATE TRIGGER trg_sync_session_task_id AFTER INSERT ON public.request_logs_hot
    FOR EACH ROW EXECUTE FUNCTION sync_session_task_id();
END IF; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='model_offers_insert' AND tgrelid='model_offers'::regclass) THEN
  CREATE TRIGGER model_offers_insert INSTEAD OF INSERT ON public.model_offers
    FOR EACH ROW EXECUTE FUNCTION model_offers_insert_trigger();
END IF; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='model_offers_update' AND tgrelid='model_offers'::regclass) THEN
  CREATE TRIGGER model_offers_update INSTEAD OF UPDATE ON public.model_offers
    FOR EACH ROW EXECUTE FUNCTION model_offers_update_trigger();
END IF; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='model_offers_delete' AND tgrelid='model_offers'::regclass) THEN
  CREATE TRIGGER model_offers_delete INSTEAD OF DELETE ON public.model_offers
    FOR EACH ROW EXECUTE FUNCTION model_offers_delete_trigger();
END IF; END$$;
