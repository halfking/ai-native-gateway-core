--
-- Name: idx_model_offers_standardized_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_standardized_name ON public.model_offers_legacy USING btree (standardized_name) WHERE (standardized_name IS NOT NULL);

