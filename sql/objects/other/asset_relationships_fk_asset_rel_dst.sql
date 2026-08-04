--
-- Name: asset_relationships fk_asset_rel_dst; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_relationships
    ADD CONSTRAINT fk_asset_rel_dst FOREIGN KEY (dst_kind, dst_ref_id) REFERENCES public.assets(kind, ref_id) ON DELETE CASCADE;

