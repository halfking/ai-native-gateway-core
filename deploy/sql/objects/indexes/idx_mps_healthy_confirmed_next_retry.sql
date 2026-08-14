--
-- Name: idx_mps_healthy_confirmed_next_retry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_healthy_confirmed_next_retry
    ON public.model_probe_state USING btree (next_retry_at)
    WHERE (state = 'healthy_confirmed'::text);
