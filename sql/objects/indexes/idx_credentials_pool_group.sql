--
-- Name: idx_credentials_pool_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_pool_group ON public.credentials USING btree (pool_group) WHERE (pool_group IS NOT NULL);

