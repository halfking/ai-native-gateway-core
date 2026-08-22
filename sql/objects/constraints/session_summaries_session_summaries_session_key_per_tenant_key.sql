--
-- Name: session_summaries session_summaries_session_key_per_tenant_key; Type: CONSTRAINT; Schema: public; Owner: -
--
-- T11-P0 v2: 跨租户 session_key 唯一性约束。
-- 注：PostgreSQL 不允许 UNIQUE ... NOT VALID（仅 FK/CHECK 支持），故直接 ADD
-- CONSTRAINT；在 session_key 已唯一时校验很快。预检由 migration 560 的 DO $$ 块完成。

ALTER TABLE ONLY public.session_summaries
    ADD CONSTRAINT session_summaries_session_key_per_tenant_key UNIQUE (tenant_id, session_key);
