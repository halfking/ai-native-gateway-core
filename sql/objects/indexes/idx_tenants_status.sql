--
-- Name: idx_tenants_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenants_status ON public.tenants USING btree (status);

