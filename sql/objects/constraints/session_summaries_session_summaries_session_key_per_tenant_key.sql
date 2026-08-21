--
-- Name: session_summaries session_summaries_session_key_per_tenant_key; Type: CONSTRAINT; Schema: public; Owner: -
--
-- T11-P0 v2: 跨租户 session_key 唯一性约束。NOT VALID 加上去，不阻塞已有写。
-- 运维可手动 VALIDATE。

ALTER TABLE ONLY public.session_summaries
    ADD CONSTRAINT session_summaries_session_key_per_tenant_key UNIQUE (tenant_id, session_key)
    NOT VALID;