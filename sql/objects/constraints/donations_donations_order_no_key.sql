--
-- Name: donations donations_order_no_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.donations
    ADD CONSTRAINT donations_order_no_key UNIQUE (order_no);

