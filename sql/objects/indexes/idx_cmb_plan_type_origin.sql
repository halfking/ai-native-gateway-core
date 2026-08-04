--
-- Name: idx_cmb_plan_type_origin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_plan_type_origin ON public.credential_model_bindings USING btree (plan_type_origin);

