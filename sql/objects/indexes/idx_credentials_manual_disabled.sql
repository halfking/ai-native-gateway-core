--
-- Name: idx_credentials_manual_disabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_manual_disabled ON public.credentials USING btree (manual_disabled) WHERE (manual_disabled = true);

