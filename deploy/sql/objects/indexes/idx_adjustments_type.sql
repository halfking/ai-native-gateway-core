--
-- Name: idx_adjustments_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_adjustments_type ON public.intent_analysis_adjustments USING btree (adjustment_type, status, created_at DESC);

