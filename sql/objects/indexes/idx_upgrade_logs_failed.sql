--
-- Name: idx_upgrade_logs_failed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_upgrade_logs_failed ON public.upgrade_logs USING btree (started_at DESC) WHERE (status = 'failed'::text);

