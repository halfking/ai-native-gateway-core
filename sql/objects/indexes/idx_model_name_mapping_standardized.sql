--
-- Name: idx_model_name_mapping_standardized; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_name_mapping_standardized ON public.model_name_mapping USING btree (standardized_name);

