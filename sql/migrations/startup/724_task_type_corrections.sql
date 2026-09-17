-- 721_task_type_corrections.sql
-- taskprofile module (2026-09-18): per-request human corrections of the
-- AUTO task-type assignment.
--
-- Background: the P2.1 annotation workflow (training_human_annotations)
-- labels the chosen PROVIDER; the auto TASK TYPE itself had no structured
-- human-correction channel. This table closes that loop:
--   - admin POST /api/admin/task-profile/corrections writes one row per
--     request (auto_task_type read from auto_route_selections_all);
--   - routingopt.PostClassify blends correction accuracy into confidence
--     damping (human weight x2, same convention as routing_feedback_log);
--   - taskprofile.Suggest escalates tiers for task types with a high
--     correction rate (OmniRoute taskFit, inverted).
--
-- Migration safety:
--   - New standalone table; no existing object is altered.
--   - Idempotent: IF NOT EXISTS everywhere.
--   - No RLS: same family as routing_feedback_log (670) — gateway-runtime/
--     admin table without tenant dimension.
--   - agrees is precomputed at write time so the aggregate stays a plain
--     GROUP BY and the verdict is frozen against later taxonomy renames.

BEGIN;

CREATE TABLE IF NOT EXISTS public.task_type_corrections (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id            text NOT NULL UNIQUE,
    auto_task_type        text NOT NULL,
    human_task_type       text NOT NULL,
    agrees                boolean NOT NULL,
    classifier_confidence double precision,
    profile               text,
    annotator             text NOT NULL,
    reason                text NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_type_corrections_auto_type
    ON public.task_type_corrections (auto_task_type, created_at DESC);

COMMENT ON TABLE public.task_type_corrections IS
    'taskprofile: 人工对 auto 任务类型分配的逐请求修正（agrees=auto与human一致）';
COMMENT ON COLUMN public.task_type_corrections.agrees IS
    'true=人工确认 auto 分类, false=人工改判; 写入时预计算';

COMMIT;
