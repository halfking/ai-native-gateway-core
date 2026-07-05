--
-- Name: idx_model_offer_events_cred_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offer_events_cred_ts ON public.model_offer_events USING btree (credential_id, ts DESC);

