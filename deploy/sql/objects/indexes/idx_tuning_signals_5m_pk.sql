--
-- Name: idx_tuning_signals_5m_pk; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tuning_signals_5m_pk ON public.tuning_signals_5m USING btree (bucket, task_type, classifier);

