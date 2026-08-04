--
-- Name: credential_model_index_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_index ATTACH PARTITION public.credential_model_index_2026_08 FOR VALUES FROM ('2026-08-01 08:00:00+08') TO ('2026-09-01 08:00:00+08');

