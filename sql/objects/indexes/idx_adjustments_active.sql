--
-- Name: idx_adjustments_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_adjustments_active ON public.intent_analysis_adjustments USING btree (tenant_id, status) WHERE (status = 'active'::text);

