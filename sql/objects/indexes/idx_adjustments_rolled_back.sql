--
-- Name: idx_adjustments_rolled_back; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_adjustments_rolled_back ON public.intent_analysis_adjustments USING btree (tenant_id, rolled_back_at DESC) WHERE (status = 'rolled_back'::text);

