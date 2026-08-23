-- 566_candidate_binding_scope_revision_canonical.down.sql

BEGIN;

DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_pm_update ON public.provider_models;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_insert ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_update ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_delete ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical ON public.credential_model_bindings;

DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_canonical_pm_update();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_canonical_insert();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_canonical_update();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_canonical_delete();

DROP TABLE IF EXISTS public.candidate_binding_scope_revision_canonical;

COMMIT;
