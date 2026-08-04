--
-- Name: routing_health_checks routing_health_checks_check_id_unique_per_entity; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_health_checks
    ADD CONSTRAINT routing_health_checks_check_id_unique_per_entity UNIQUE (check_id, entity_type, entity_id);

