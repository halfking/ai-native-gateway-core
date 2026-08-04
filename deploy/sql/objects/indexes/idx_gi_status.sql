--
-- Name: idx_gi_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_status ON public.gateway_instances USING btree (status);

