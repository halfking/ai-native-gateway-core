--
-- Name: vibe_coding_sessions vibe_coding_sessions_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vibe_coding_sessions
    ADD CONSTRAINT vibe_coding_sessions_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.vibe_coding_projects(id) ON DELETE SET NULL;

