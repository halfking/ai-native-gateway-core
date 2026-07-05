--
-- Name: idx_credential_quota_usage_quota; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_quota_usage_quota ON public.credential_quota_usage USING btree (quota_id, window_started_at DESC);

