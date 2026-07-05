--
-- Name: idx_credentials_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_expires_at ON public.credentials USING btree (expires_at) WHERE (expires_at IS NOT NULL);

