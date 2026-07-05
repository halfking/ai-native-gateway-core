--
-- Name: idx_passive_probe_reviewing; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_passive_probe_reviewing ON public.passive_probe_state USING btree (in_reviewing, reviewing_until) WHERE (in_reviewing = true);

