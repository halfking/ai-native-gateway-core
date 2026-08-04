--
-- Name: agent_relationships pk_agent_relationships; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_relationships
    ADD CONSTRAINT pk_agent_relationships PRIMARY KEY (src_agent_id, dst_agent_id, rel);

