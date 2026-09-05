-- 541_candidate_binding_scope_revision.down.sql

BEGIN;

DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_insert ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_update ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_delete ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision ON public.credential_model_bindings;

DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_insert();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_update();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision_delete();
DROP FUNCTION IF EXISTS public.bump_candidate_binding_scope_revision();

DROP TABLE IF EXISTS public.candidate_binding_scope_revision;

COMMIT;
