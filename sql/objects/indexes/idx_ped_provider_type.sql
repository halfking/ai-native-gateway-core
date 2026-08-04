--
-- Name: idx_ped_provider_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ped_provider_type ON public.provider_error_details USING btree (provider_id, error_type);

