--
-- Name: idx_cmi_pressure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmi_pressure ON public.credential_model_index USING btree (bucket, pressure_ratio DESC);

