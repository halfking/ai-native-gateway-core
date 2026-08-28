--
-- Name: credential_model_capabilities credential_model_capabilities_credential_model_binding_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_capabilities
    ADD CONSTRAINT credential_model_capabilities_credential_model_binding_id_fkey FOREIGN KEY (credential_model_binding_id) REFERENCES public.credential_model_bindings(id) ON DELETE CASCADE;
