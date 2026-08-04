--
-- Name: idx_cmi_archive_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmi_archive_bucket ON ONLY public.credential_model_index_archive USING btree (bucket DESC);

