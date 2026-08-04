--
-- Name: licenses licenses_holder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.licenses
    ADD CONSTRAINT licenses_holder_id_fkey FOREIGN KEY (holder_id) REFERENCES public.license_holders(id) ON DELETE SET NULL;

