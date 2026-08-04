--
-- Name: asset_relationships fk_asset_rel_src; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_relationships
    ADD CONSTRAINT fk_asset_rel_src FOREIGN KEY (src_kind, src_ref_id) REFERENCES public.assets(kind, ref_id) ON DELETE CASCADE;

