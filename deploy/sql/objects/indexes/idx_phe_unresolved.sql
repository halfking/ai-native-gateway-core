--
-- Name: idx_phe_unresolved; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_phe_unresolved ON public.provider_health_events USING btree (resolved_at) WHERE (resolved_at IS NULL);

