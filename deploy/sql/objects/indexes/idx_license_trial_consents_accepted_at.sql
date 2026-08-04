--
-- Name: idx_license_trial_consents_accepted_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_license_trial_consents_accepted_at ON public.license_trial_consents USING btree (accepted_at DESC);

