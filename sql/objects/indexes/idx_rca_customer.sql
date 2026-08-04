--
-- Name: idx_rca_customer; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_customer ON public.request_context_attrs USING btree (customer_id);

