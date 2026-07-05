--
-- Name: idx_cred_quota_recover; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cred_quota_recover ON public.credentials USING btree (quota_recover_at) WHERE ((quota_state = 'periodic_exhausted'::text) AND (quota_recover_at IS NOT NULL));

