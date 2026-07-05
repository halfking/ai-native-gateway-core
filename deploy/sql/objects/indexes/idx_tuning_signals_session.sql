--
-- Name: idx_tuning_signals_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_session ON public.tuning_signals USING btree (session_id, ts DESC) WHERE (session_id IS NOT NULL);

