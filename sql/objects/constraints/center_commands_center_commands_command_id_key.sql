--
-- Name: center_commands center_commands_command_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.center_commands
    ADD CONSTRAINT center_commands_command_id_key UNIQUE (command_id);

