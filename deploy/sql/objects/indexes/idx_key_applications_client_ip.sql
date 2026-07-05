--
-- Name: idx_key_applications_client_ip; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_applications_client_ip ON public.key_applications USING btree (client_ip, created_at DESC);

