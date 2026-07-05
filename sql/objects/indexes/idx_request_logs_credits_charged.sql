--
-- Name: idx_request_logs_credits_charged; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_credits_charged ON ONLY public.request_logs USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));

