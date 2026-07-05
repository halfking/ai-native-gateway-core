--
-- Name: idx_tuning_signals_lowq; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_lowq ON public.tuning_signals USING btree (task_type, ts DESC) WHERE ((quality_score < 0.5) AND (classifier = 'heuristic'::text));

