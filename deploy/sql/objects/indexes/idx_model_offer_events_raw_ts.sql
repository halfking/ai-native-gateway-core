--
-- Name: idx_model_offer_events_raw_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offer_events_raw_ts ON public.model_offer_events USING btree (raw_model_name, ts DESC);

