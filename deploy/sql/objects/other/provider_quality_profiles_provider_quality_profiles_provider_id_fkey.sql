--
-- Name: provider_quality_profiles provider_quality_profiles_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_profiles
    ADD CONSTRAINT provider_quality_profiles_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id) ON DELETE CASCADE;

