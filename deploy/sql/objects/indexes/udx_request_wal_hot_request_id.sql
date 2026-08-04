--
-- Name: udx_request_wal_hot_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX udx_request_wal_hot_request_id ON public.request_wal_hot USING btree (request_id);


--
-- Name: INDEX udx_request_wal_hot_request_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON INDEX public.udx_request_wal_hot_request_id IS 'Enforces one request_wal_hot row per request so the early (arrival) and later (post-routing) CreateInitial calls collapse onto the same row instead of orphaning a pending row. Added by migration 461 (2026-07-27).';

