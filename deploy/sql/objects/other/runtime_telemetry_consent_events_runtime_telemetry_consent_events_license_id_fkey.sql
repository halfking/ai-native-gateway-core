--
-- Name: runtime_telemetry_consent_events runtime_telemetry_consent_events_license_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_telemetry_consent_events
    ADD CONSTRAINT runtime_telemetry_consent_events_license_id_fkey FOREIGN KEY (license_id) REFERENCES public.licenses(id) ON DELETE CASCADE;

