--
-- Name: idx_ped_unresolved; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ped_unresolved ON public.provider_error_details USING btree (resolved) WHERE (NOT resolved);

