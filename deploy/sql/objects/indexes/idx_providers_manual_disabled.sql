--
-- Name: idx_providers_manual_disabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_manual_disabled ON public.providers USING btree (manual_disabled) WHERE (manual_disabled = true);

