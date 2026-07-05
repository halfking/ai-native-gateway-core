--
-- Name: idx_tmp_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_canonical ON public.tenant_model_policies USING btree (canonical_name);

