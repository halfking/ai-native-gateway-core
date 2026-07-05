--
-- Name: idx_credentials_auto_limit; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_auto_limit ON public.credentials USING btree (concurrency_limit_auto) WHERE (concurrency_limit_auto IS NOT NULL);

