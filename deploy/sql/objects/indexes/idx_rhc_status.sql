--
-- Name: idx_rhc_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rhc_status ON public.routing_health_checks USING btree (status) WHERE (status = 'open'::text);

