--
-- Name: idx_credentials_free_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_free_source ON public.credentials USING btree (acquisition_source) WHERE ((pool_group = 'free'::text) AND (acquisition_source IS NOT NULL));

