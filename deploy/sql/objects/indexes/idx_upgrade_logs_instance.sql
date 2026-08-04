--
-- Name: idx_upgrade_logs_instance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_upgrade_logs_instance ON public.upgrade_logs USING btree (instance_id, started_at DESC);

