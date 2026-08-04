--
-- Name: idx_model_aliases_lower_raw_name_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_aliases_lower_raw_name_status ON public.model_aliases USING btree (lower(raw_name), status) WHERE (status = 'active'::text);

