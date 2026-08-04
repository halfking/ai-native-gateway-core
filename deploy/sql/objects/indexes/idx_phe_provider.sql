--
-- Name: idx_phe_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_phe_provider ON public.provider_health_events USING btree (provider_id, created_at DESC);

