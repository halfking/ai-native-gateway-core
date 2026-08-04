--
-- Name: idx_cc_instance_command; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_cc_instance_command ON public.center_commands USING btree (instance_id, command_id);

