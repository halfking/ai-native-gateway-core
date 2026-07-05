--
-- Name: idx_mti_task_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mti_task_bucket ON public.model_task_index USING btree (task_type, bucket DESC);

