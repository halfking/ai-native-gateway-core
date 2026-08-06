--
-- Name: runtime_metrics runtime_metrics_license_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_metrics
    ADD CONSTRAINT runtime_metrics_license_id_fkey FOREIGN KEY (license_id) REFERENCES public.licenses(id) ON DELETE SET NULL;

