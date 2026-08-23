--
-- Name: credentials_revision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credentials_revision_idx ON public.credentials USING btree (revision);
