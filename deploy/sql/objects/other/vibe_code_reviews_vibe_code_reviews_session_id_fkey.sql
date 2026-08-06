--
-- Name: vibe_code_reviews vibe_code_reviews_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vibe_code_reviews
    ADD CONSTRAINT vibe_code_reviews_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.vibe_coding_sessions(id) ON DELETE SET NULL;

