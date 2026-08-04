--
-- Name: idx_phe_severity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_phe_severity ON public.provider_health_events USING btree (severity, created_at DESC);

