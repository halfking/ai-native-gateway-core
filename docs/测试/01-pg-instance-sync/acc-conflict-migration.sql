BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

LOCK TABLE public.gateway_run_bindings, public.workcell_checkpoints
  IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.gateway_run_bindings) OR
     EXISTS (SELECT 1 FROM public.workcell_checkpoints) THEN
    RAISE EXCEPTION 'conflict tables are no longer empty';
  END IF;
END
$$;

ALTER TABLE public.gateway_run_bindings
  DROP CONSTRAINT gateway_run_bindings_status_check;
ALTER TABLE public.gateway_run_bindings
  ALTER COLUMN binding_id DROP DEFAULT,
  ALTER COLUMN created_at SET NOT NULL,
  ALTER COLUMN execution_id TYPE text USING execution_id::text,
  ALTER COLUMN status TYPE text USING status::text,
  ALTER COLUMN status SET DEFAULT 'pending'::text,
  ALTER COLUMN status SET NOT NULL,
  ALTER COLUMN updated_at SET NOT NULL,
  ALTER COLUMN workcell_id TYPE text USING workcell_id::text;
ALTER TABLE public.gateway_run_bindings
  ADD CONSTRAINT gateway_run_bindings_status_check
  CHECK (status = ANY (ARRAY[
    'pending'::varchar::text, 'running'::varchar::text,
    'done'::varchar::text, 'aborted'::varchar::text,
    'paused'::varchar::text
  ]));

ALTER TABLE public.workcell_checkpoints
  ALTER COLUMN id DROP DEFAULT,
  -- UUID/timestamp values have no safe bigint mapping. The lock + empty-table
  -- assertion above is mandatory; these CASE expressions fail on any value.
  ALTER COLUMN id TYPE bigint USING
    CASE WHEN id IS NULL THEN NULL::bigint ELSE id::text::bigint END,
  ALTER COLUMN id SET DEFAULT
    nextval('public.workcell_checkpoints_id_seq'::regclass),
  ALTER COLUMN phase TYPE text USING phase::text,
  ALTER COLUMN snapshot_ts DROP DEFAULT,
  ALTER COLUMN snapshot_ts TYPE bigint USING
    CASE WHEN snapshot_ts IS NULL THEN NULL::bigint
      ELSE snapshot_ts::text::bigint END,
  ALTER COLUMN snapshot_ts SET NOT NULL,
  ALTER COLUMN state SET NOT NULL,
  ALTER COLUMN workcell_id TYPE text USING workcell_id::text;
ALTER SEQUENCE public.workcell_checkpoints_id_seq OWNER TO acc_app;
ALTER SEQUENCE public.workcell_checkpoints_id_seq
  OWNED BY public.workcell_checkpoints.id;

DROP VIEW public.online_employees;
CREATE VIEW public.online_employees AS
SELECT id, name, type, device_id, gateway_id, status, capabilities, metadata,
  created_at, updated_at, last_heartbeat_at, lifecycle_tier,
  prompt_template_id, tools_whitelist, llm_routing_hint, idle_ttl_sec,
  sandbox_profile, spawn_command, last_spawn_at, tenant_id, reported_skills,
  skill_levels, device_hostname, device_ip, device_platform, device_arch,
  openclaw_version, models, role_key, runtime_type, runbook_path,
  agent_framework, agent_type, display_name, title, skills, source, enabled,
  is_fallback, last_seen, last_activity, org_id, agent_visibility, host_id,
  workspace_path, casdoor_user_id, casdoor_org_id, llm_provider, llm_model,
  total_tokens_used, total_cost_usd, current_task_id
FROM public.employees
WHERE status::text = 'online'::text
ORDER BY last_heartbeat_at DESC;
ALTER VIEW public.online_employees OWNER TO acc_app;

DROP POLICY tenant_isolation ON public.bidding_failures;
CREATE POLICY tenant_isolation ON public.bidding_failures
  AS PERMISSIVE FOR ALL TO public
  USING (
    workcell_id::text IN (
      SELECT bidding_sessions.workcell_id
      FROM public.bidding_sessions
      WHERE bidding_sessions.org_id::text =
        current_setting('app.current_org_id'::text, true)
    )
  );
COMMIT;
