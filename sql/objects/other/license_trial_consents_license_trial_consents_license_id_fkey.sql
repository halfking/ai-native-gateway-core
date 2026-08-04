--
-- Name: license_trial_consents license_trial_consents_license_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_trial_consents
    ADD CONSTRAINT license_trial_consents_license_id_fkey FOREIGN KEY (license_id) REFERENCES public.licenses(id) ON DELETE CASCADE;

