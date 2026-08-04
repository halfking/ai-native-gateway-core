--
-- Name: credential_probe_configs credential_probe_configs_credential_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probe_configs
    ADD CONSTRAINT credential_probe_configs_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES public.credentials(id);

