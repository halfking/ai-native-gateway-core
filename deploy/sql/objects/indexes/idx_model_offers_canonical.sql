--
-- Name: idx_model_offers_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_canonical ON public.model_offers_legacy USING btree (canonical_id);

