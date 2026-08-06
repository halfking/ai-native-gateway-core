--
-- Name: provider_quality_configs provider_quality_configs_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_configs
    ADD CONSTRAINT provider_quality_configs_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id) ON DELETE CASCADE;

