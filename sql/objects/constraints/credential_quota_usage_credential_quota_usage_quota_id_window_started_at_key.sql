--
-- Name: credential_quota_usage credential_quota_usage_quota_id_window_started_at_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quota_usage
    ADD CONSTRAINT credential_quota_usage_quota_id_window_started_at_key UNIQUE (quota_id, window_started_at);

