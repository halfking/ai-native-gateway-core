--
-- Name: idx_rsedm_error_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rsedm_error_bucket ON public.request_stats_error_drill_minute USING btree (error_kind, bucket DESC);

