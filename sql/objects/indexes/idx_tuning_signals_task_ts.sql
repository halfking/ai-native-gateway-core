--
-- Name: idx_tuning_signals_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_task_ts ON public.tuning_signals USING btree (task_type, ts DESC);

