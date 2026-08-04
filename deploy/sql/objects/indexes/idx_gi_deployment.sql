--
-- Name: idx_gi_deployment; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_deployment ON public.gateway_instances USING btree (deployment_id);

