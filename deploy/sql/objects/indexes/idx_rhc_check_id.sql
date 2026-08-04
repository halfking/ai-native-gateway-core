--
-- Name: idx_rhc_check_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rhc_check_id ON public.routing_health_checks USING btree (check_id);

