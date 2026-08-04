--
-- Name: idx_adjustments_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_adjustments_tenant ON public.intent_analysis_adjustments USING btree (tenant_id, created_at DESC);

