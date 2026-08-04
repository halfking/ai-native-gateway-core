--
-- Name: idx_gi_refresh_token; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_refresh_token ON public.gateway_instances USING btree (refresh_token);

