--
-- Name: runtime_telemetry_preferences runtime_telemetry_preferences_license_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_telemetry_preferences
    ADD CONSTRAINT runtime_telemetry_preferences_license_id_fkey FOREIGN KEY (license_id) REFERENCES public.licenses(id) ON DELETE CASCADE;

