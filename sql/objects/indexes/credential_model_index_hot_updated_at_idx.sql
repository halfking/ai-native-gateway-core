--
-- Name: credential_model_index_hot_updated_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credential_model_index_hot_updated_at_idx ON public.credential_model_index_hot USING btree (updated_at DESC);

