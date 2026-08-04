--
-- Name: idx_rca_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_tenant_ts ON public.request_context_attrs USING btree (tenant_id, ts DESC);

