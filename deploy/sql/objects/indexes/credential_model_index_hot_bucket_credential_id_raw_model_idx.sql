--
-- Name: credential_model_index_hot_bucket_credential_id_raw_model_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX credential_model_index_hot_bucket_credential_id_raw_model_idx ON public.credential_model_index_hot USING btree (bucket, credential_id, raw_model);

