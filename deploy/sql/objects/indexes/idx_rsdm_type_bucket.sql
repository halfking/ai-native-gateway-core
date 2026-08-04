--
-- Name: idx_rsdm_type_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rsdm_type_bucket ON public.request_stats_dim_minute USING btree (dim_type, bucket DESC);

