--
-- Name: idx_billing_orders_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_billing_orders_status ON public.billing_orders USING btree (status, created_at DESC);

