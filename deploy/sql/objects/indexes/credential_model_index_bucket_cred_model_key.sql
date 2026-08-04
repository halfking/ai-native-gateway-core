--
-- Name: credential_model_index_bucket_cred_model_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX credential_model_index_bucket_cred_model_key ON ONLY public.credential_model_index USING btree (bucket, credential_id, raw_model);

