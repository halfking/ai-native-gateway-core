--
-- Name: idx_credentials_tags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_tags ON public.credentials USING gin (tags) WHERE (tags IS NOT NULL);

