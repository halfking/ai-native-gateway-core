--
-- Name: idx_model_offers_available; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_available ON public.model_offers_legacy USING btree (available) WHERE (available = true);

