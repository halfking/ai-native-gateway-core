--
-- Name: idx_model_offer_events_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offer_events_run ON public.model_offer_events USING btree (run_id) WHERE (run_id IS NOT NULL);

