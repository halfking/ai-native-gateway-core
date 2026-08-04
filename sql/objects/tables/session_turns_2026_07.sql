--
-- Name: session_turns_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns ATTACH PARTITION public.session_turns_2026_07 FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

