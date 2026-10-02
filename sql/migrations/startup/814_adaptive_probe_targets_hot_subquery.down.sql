-- Migration 814 down: 恢复 814 应用前的视图形态（038 建立后经后续演进的
-- 版本，以 814 前一刻 pg_get_viewdef 为准；注意 038 原始体与此不同——
-- 投影/类型标注在后续轮次演进过）。恢复后的 recent_passive_failures 恒 0
-- （0-8h 行在 hot，见 814 头注）——down 仅用于契约回退演练，不应长期驻留。
DO $$
BEGIN
    IF to_regclass('public.v_adaptive_probe_targets') IS NULL THEN
        RAISE NOTICE '814 down: view absent; nothing to restore';
        RETURN;
    END IF;

    EXECUTE $view$
CREATE OR REPLACE VIEW public.v_adaptive_probe_targets AS
 SELECT cmb.id AS binding_id,
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(mps.consecutive_failures, 0) AS consecutive_failures,
    COALESCE(mps.consecutive_successes, 0) AS consecutive_successes,
    COALESCE(mps.state, 'unknown'::text) AS probe_state,
    mps.last_attempt_at,
    mps.next_retry_at,
    EXTRACT(epoch FROM (now() - COALESCE(mps.last_attempt_at, (now() - '01:00:00'::interval)))) AS age_secs,
    ( SELECT count(*) AS count
           FROM public.candidate_failure_logs cfl
          WHERE ((cfl.credential_id = cmb.credential_id) AND (cfl.raw_model_name = pm.raw_model_name) AND (cfl.ts > (now() - '00:05:00'::interval)))) AS recent_passive_failures
   FROM ((((public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)))
     JOIN public.credentials c ON ((c.id = cmb.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
     LEFT JOIN public.model_probe_state mps ON (((mps.credential_id = cmb.credential_id) AND (mps.raw_model_name = pm.raw_model_name))))
  WHERE ((COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.availability_state, 'ready'::text) <> 'suspended'::text) AND (COALESCE(c.quota_state, 'ok'::text) <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(cmb.unavailable_reason, ''::text) !~~ 'manual%'::text) AND (COALESCE(mps.state, 'unknown'::text) <> 'broken_confirmed'::text));
$view$;
END $$;
