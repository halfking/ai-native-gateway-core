--
-- Name: ops_node_registrations ops_node_registrations_instance_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_node_registrations
    ADD CONSTRAINT ops_node_registrations_instance_id_key UNIQUE (instance_id);

