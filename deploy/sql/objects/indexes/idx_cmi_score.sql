--
-- Name: idx_cmi_score; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmi_score ON public.credential_model_index USING btree (canonical_id, score_smart DESC, bucket DESC);

