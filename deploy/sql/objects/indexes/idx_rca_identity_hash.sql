--
-- Name: idx_rca_identity_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_identity_hash ON public.request_context_attrs USING btree (identity_hash);

