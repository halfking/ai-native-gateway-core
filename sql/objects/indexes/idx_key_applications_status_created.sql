--
-- Name: idx_key_applications_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_applications_status_created ON public.key_applications USING btree (status, created_at DESC);

