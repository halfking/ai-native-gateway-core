--
-- Name: idx_mccb_recent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mccb_recent ON public.maas_credit_consumption_buckets USING btree (bucket_start DESC);

