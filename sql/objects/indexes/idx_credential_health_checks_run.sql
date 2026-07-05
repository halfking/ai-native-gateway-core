--
-- Name: idx_credential_health_checks_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_health_checks_run ON public.credential_health_checks USING btree (run_id, created_at DESC);

