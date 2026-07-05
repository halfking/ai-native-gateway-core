--
-- Name: idx_mti_canonical_bkt; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mti_canonical_bkt ON public.model_task_index USING btree (canonical_id, bucket DESC);

