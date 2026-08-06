--
-- Name: ops_node_registrations ops_node_registrations_license_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_node_registrations
    ADD CONSTRAINT ops_node_registrations_license_id_fkey FOREIGN KEY (license_id) REFERENCES public.licenses(id) ON DELETE SET NULL;

