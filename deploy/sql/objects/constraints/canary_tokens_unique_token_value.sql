--
-- Name: canary_tokens unique_token_value; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.canary_tokens
    ADD CONSTRAINT unique_token_value UNIQUE (token_value);

