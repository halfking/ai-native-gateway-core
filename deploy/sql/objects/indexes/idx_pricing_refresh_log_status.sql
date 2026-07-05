--
-- Name: idx_pricing_refresh_log_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_refresh_log_status ON public.pricing_refresh_log USING btree (status);

