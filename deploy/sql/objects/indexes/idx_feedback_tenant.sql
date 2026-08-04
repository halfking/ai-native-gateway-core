--
-- Name: idx_feedback_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_tenant ON public.intent_classification_feedback USING btree (tenant_id, created_at DESC);

