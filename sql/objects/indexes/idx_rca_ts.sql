--
-- Name: idx_rca_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_ts ON public.request_context_attrs USING btree (ts);

