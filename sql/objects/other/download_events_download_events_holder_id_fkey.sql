--
-- Name: download_events download_events_holder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.download_events
    ADD CONSTRAINT download_events_holder_id_fkey FOREIGN KEY (holder_id) REFERENCES public.license_holders(id) ON DELETE SET NULL;

