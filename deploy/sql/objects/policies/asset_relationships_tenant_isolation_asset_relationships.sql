--
-- Name: asset_relationships tenant_isolation_asset_relationships; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_asset_relationships ON public.asset_relationships USING (((EXISTS ( SELECT 1
   FROM public.assets a_src
  WHERE ((a_src.kind = asset_relationships.src_kind) AND (a_src.ref_id = asset_relationships.src_ref_id) AND (a_src.tenant_id = public.get_current_tenant())))) AND (EXISTS ( SELECT 1
   FROM public.assets a_dst
  WHERE ((a_dst.kind = asset_relationships.dst_kind) AND (a_dst.ref_id = asset_relationships.dst_ref_id) AND (a_dst.tenant_id = public.get_current_tenant()))))));

