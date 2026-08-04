--
-- Name: idx_credential_probe_model_log_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probe_model_log_created ON public.credential_probe_model_log USING btree (created_at);

