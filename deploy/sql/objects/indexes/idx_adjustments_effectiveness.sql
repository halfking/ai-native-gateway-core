--
-- Name: idx_adjustments_effectiveness; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_adjustments_effectiveness ON public.intent_analysis_adjustments USING btree (effectiveness_score DESC, tenant_id) WHERE (effectiveness_score IS NOT NULL);

