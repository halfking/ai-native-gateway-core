--
-- Name: idx_cc_instance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cc_instance ON public.center_commands USING btree (instance_id, issued_at DESC);

