--
-- Name: tenant_model_policies_active; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.tenant_model_policies_active AS
 SELECT id,
    tenant_id,
    canonical_name,
    reason,
    created_by,
    created_at,
    updated_at
   FROM public.tenant_model_policies
  WHERE (deleted_at IS NULL);

