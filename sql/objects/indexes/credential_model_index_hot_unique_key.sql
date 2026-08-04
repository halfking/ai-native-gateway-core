--
-- Name: credential_model_index_hot_unique_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX credential_model_index_hot_unique_key ON public.credential_model_index_hot USING btree (bucket, credential_id, raw_model);

