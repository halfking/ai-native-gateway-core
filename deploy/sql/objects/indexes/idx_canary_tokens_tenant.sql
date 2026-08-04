--
-- Name: idx_canary_tokens_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_canary_tokens_tenant ON public.canary_tokens USING btree (tenant_id, active);

