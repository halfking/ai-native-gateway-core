--
-- Name: idx_credentials_effective_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_effective_at ON public.credentials USING btree (effective_at) WHERE (effective_at IS NOT NULL);

