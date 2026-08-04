--
-- Name: idx_feedback_user_behavior; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_feedback_user_behavior ON public.intent_classification_feedback USING btree (user_retry_count DESC, tenant_id) WHERE (user_retry_count > 0);

