--
-- Name: idx_rhc_severity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rhc_severity ON public.routing_health_checks USING btree (severity, created_at DESC);

