--
-- Name: gray_release_rules gray_release_rules_release_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gray_release_rules
    ADD CONSTRAINT gray_release_rules_release_id_fkey FOREIGN KEY (release_id) REFERENCES public.releases(id) ON DELETE CASCADE;

