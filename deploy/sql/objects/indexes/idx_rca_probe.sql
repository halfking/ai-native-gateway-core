--
-- Name: idx_rca_probe; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_probe ON public.request_context_attrs USING btree (is_probe) WHERE (is_probe = true);

