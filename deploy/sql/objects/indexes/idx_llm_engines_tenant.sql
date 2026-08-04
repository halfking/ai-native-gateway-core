--
-- Name: idx_llm_engines_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_llm_engines_tenant ON public.prompt_injection_llm_engines USING btree (tenant_id, enabled, priority DESC);

