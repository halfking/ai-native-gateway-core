--
-- Name: credential_probes credential_probes_credential_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probes
    ADD CONSTRAINT credential_probes_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES public.credentials(id);

