--
-- Name: idx_cmb_available_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_available_tier ON public.credential_model_bindings USING btree (routing_tier, weight DESC, success_rate DESC NULLS LAST);

