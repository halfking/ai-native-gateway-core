--
-- Name: idx_provider_quality_rollup_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_quality_rollup_bucket ON public.provider_quality_rollup USING btree (bucket_start DESC);

