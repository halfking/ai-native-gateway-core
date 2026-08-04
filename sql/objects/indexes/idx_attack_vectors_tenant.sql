--
-- Name: idx_attack_vectors_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_attack_vectors_tenant ON public.injection_attack_vectors USING btree (tenant_id, severity DESC);

