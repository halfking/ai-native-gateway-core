--
-- Name: idx_tuning_signals_5m_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_5m_task_ts ON public.tuning_signals_5m USING btree (task_type, classifier, bucket DESC);

