--
-- Name: idx_request_logs_provider_quality; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_quality ON ONLY public.request_logs USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);

