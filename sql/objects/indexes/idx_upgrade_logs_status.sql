--
-- Name: idx_upgrade_logs_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_upgrade_logs_status ON public.upgrade_logs USING btree (status, started_at DESC);

