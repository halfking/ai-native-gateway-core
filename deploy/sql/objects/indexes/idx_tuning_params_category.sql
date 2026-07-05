--
-- Name: idx_tuning_params_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_params_category ON public.tuning_params USING btree (category, enabled) WHERE (enabled = true);

