--
-- Name: idx_detections_risk; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_detections_risk ON public.prompt_injection_detections USING btree (tenant_id, risk_level) WHERE (blocked = true);

