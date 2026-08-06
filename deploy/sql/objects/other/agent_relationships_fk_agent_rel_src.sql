--
-- Name: agent_relationships fk_agent_rel_src; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_relationships
    ADD CONSTRAINT fk_agent_rel_src FOREIGN KEY (src_agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

