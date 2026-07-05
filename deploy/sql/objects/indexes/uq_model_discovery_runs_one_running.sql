--
-- Name: uq_model_discovery_runs_one_running; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_model_discovery_runs_one_running ON public.model_discovery_runs USING btree (tenant_id) WHERE (status = 'running'::text);

