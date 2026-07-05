-- =============================================================================
-- 02-seed.sql — Initial seed data for llm_gateway database
-- =============================================================================
-- Reverse-engineered from production DB on 2026-06-24 using:
--   pg_dump --data-only --inserts --no-owner --no-privileges \
--           -t applications
--           -t internal_service_keys
--           -t key_applications
--           -t key_rpm_daily
--           -t local_models
--           -t local_runtimes
--           -t maas_settings
--           -t model_aliases
--           -t model_credit_rates
--           -t model_families
--           -t model_fingerprints
--           -t model_lifecycle_jobs
--           -t model_offers_legacy
--           -t model_reconcile_log
--           -t ops_model_offers_backup
--           -t pricing_plans
--           -t pricing_refresh_log
--           -t provider_catalog
--           -t provider_header_profiles
--           -t provider_quality_rollup
--           -t provider_scores
--           -t provider_settings
--           -t providers
--           -t request_envelope
--           -t route_decisions
--           -t routing_overrides
--           -t routing_policy
--           -t schema_migrations
--           -t settings_kv
--           -t sticky_sessions
--           -t subscription_plans
--           -t system_identity_pool
--           -t tenant_settings_kv
--           -t tool_categories
--           -t tool_registry
--           -t topup_packages
--           -t tuning_params
--           -t tuning_proposals
--           -t tuning_signals
--           -t work_type_config
--           -t work_type_model_route
--
-- Regenerate with: ./dump-seed.sh
--
-- Run order: AFTER 01-schema.sql. Idempotent: every INSERT is augmented with
-- `ON CONFLICT (<pk>) DO NOTHING` based on the table's primary key (composite
-- PKs use untargeted ON CONFLICT DO NOTHING). Re-runs are safe.
--
-- What is in this seed: small system-level config / lookup tables selected by
-- db_init::select_seed_tables (heuristic: small + system-config name pattern).
-- What is excluded: business data (users, tenants, api_keys, credentials,
-- audit logs, runtime / time-series data). See README.md for full list.
-- =============================================================================

-- Data for Name: applications; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: internal_service_keys; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: key_applications; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: key_rpm_daily; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: local_models; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: local_runtimes; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: maas_settings; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_aliases; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_credit_rates; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_families; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_fingerprints; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_lifecycle_jobs; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_offers_legacy; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: model_reconcile_log; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: ops_model_offers_backup; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: pricing_plans; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: pricing_refresh_log; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: provider_catalog; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: provider_header_profiles; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: provider_quality_rollup; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: provider_scores; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: provider_settings; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: providers; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: request_envelope; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: route_decisions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: routing_overrides; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: routing_policy; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: schema_migrations; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: settings_kv; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: sticky_sessions; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: subscription_plans; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: system_identity_pool; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: tenant_settings_kv; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: tool_categories; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: tool_registry; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: topup_packages; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: tuning_params; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: tuning_proposals; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: tuning_signals; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: work_type_config; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: work_type_model_route; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Name: applications_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.applications_id_seq', 12, true);


--
-- Name: local_models_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.local_models_id_seq', 1, false);


--
-- Name: local_runtimes_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.local_runtimes_id_seq', 1, false);


--
-- Name: model_aliases_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.model_aliases_id_seq', 249, true);


--
-- Name: model_fingerprints_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.model_fingerprints_id_seq', 1, false);


--
-- Name: model_lifecycle_jobs_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.model_lifecycle_jobs_id_seq', 1, false);


--
-- Name: model_offers_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.model_offers_id_seq', 1, false);


--
-- Name: model_reconcile_log_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.model_reconcile_log_id_seq', 1, false);


--
-- Name: ops_model_offers_backup_backup_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.ops_model_offers_backup_backup_id_seq', 1, false);


--
-- Name: pricing_plans_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.pricing_plans_id_seq', 208, true);


--
-- Name: pricing_refresh_log_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.pricing_refresh_log_id_seq', 1, false);


--
-- Name: provider_header_profiles_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.provider_header_profiles_id_seq', 1, false);


--
-- Name: provider_scores_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.provider_scores_id_seq', 1, false);


--
-- Name: provider_settings_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.provider_settings_id_seq', 7, true);


--
-- Name: providers_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.providers_id_seq', 2451, true);


--
-- Name: route_decisions_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.route_decisions_id_seq', 1, false);


--
-- Name: routing_overrides_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.routing_overrides_id_seq', 1, false);


--
-- Name: subscription_plans_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.subscription_plans_id_seq', 363, true);


--
-- Name: tool_registry_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.tool_registry_id_seq', 7, true);


--
-- Name: topup_packages_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.topup_packages_id_seq', 363, true);


--
-- Name: tuning_proposals_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.tuning_proposals_id_seq', 1, false);


--
-- Name: tuning_signals_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.tuning_signals_id_seq', 1, false);


--
-- Name: work_type_model_route_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.work_type_model_route_id_seq', 4545, true);


--
--


INSERT INTO public.applications VALUES (1, 'default', 'admin', 'System Admin', 'admin', 'confidential', true, 'Bootstrap admin application for gateway', '2026-06-11 16:38:22.38882+00', '2026-06-11 16:38:22.38882+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (3, 'default', 'hermes', 'Hermes Agent', NULL, 'internal', true, NULL, '2026-06-11 17:29:01.053784+00', '2026-06-11 17:29:01.053784+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (4, 'default', 'ide', 'IDE Workspace', NULL, 'internal', true, NULL, '2026-06-11 17:29:01.053784+00', '2026-06-11 17:29:01.053784+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (5, 'default', 'applicant', '自助申请', 'admin', 'internal', true, NULL, '2026-06-12 04:39:25.566262+00', '2026-06-12 04:39:25.566262+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (6, 'default', 'brandmind', 'brandmind', 'admin', 'internal', true, NULL, '2026-06-13 05:41:55.960755+00', '2026-06-13 05:41:55.960755+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (7, 'default', 'user', 'user', 'admin', 'internal', true, NULL, '2026-06-14 18:39:49.881815+00', '2026-06-14 18:39:49.881815+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (8, 'default', 'e2e-app', 'e2e-app', 'admin', 'internal', true, NULL, '2026-06-15 11:39:03.410375+00', '2026-06-15 11:39:03.410375+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (9, 'default', 'default', 'default', 'kevinuser1', 'internal', true, NULL, '2026-06-15 16:03:59.471571+00', '2026-06-15 16:03:59.471571+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (10, 'default', 'wbing', 'wbing', 'admin', 'internal', true, NULL, '2026-06-18 07:11:36.682434+00', '2026-06-18 07:11:36.682434+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (11, 'default', 'caiyc', 'caiyc', 'admin', 'internal', true, NULL, '2026-06-18 07:20:00.819343+00', '2026-06-18 07:20:00.819343+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.applications VALUES (12, 'default', 'default-app', 'default-app', 'applicant', 'internal', true, NULL, '2026-06-19 16:17:21.976265+00', '2026-06-19 16:17:21.976265+00', NULL, NULL) ON CONFLICT DO NOTHING;
-- Test key_application entries. Production seed data is provided out-of-band.
-- The actual key hash and applicant IP are intentionally NOT seeded here; create your own
-- record via the admin UI or by supplying the values in your private seed file.
-- See deploy/sql/03-private-seed.sql.example for the template (gitignored).
INSERT INTO public.key_applications VALUES ('6c795908-9d7b-418e-8b2b-6103feada5d3', '__REDACTED_IP__', '__REDACTED_KEY_HASH__', 'redacted@example.com', '', 'pending', NULL, NULL, NULL, NULL, NULL, NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.key_applications VALUES ('58cef7c8-db04-4625-b02b-abcecb28a331', '60.176.167.110', '1f21ade5ee98c0415f2174f30321dfe0eb0c4a39117df64c2ec2d5258b7b5808', 'admin@test.com', 'e2e-test', 'pending', NULL, NULL, NULL, NULL, '2026-06-24 09:29:03.689041+00', '2026-06-23 09:29:03.689041+00', '2026-06-23 09:29:03.689041+00') ON CONFLICT DO NOTHING;
INSERT INTO public.maas_settings VALUES (1, 0.0100, 600, 'CNY', '2026-06-19 09:13:05.694408+00', '', '', '', '', 1500, 50, 80, 0.8200) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (1, 1, 'gpt-4o', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (2, 1, 'gpt-4o-2024-08-06', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (3, 1, 'gpt-4o-2024-11-20', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (4, 1, 'openai/gpt-4o', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (5, 1, 'chatgpt-4o-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (6, 2, 'gpt-4o-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (7, 2, 'gpt-4o-mini-2024-07-18', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (8, 2, 'openai/gpt-4o-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (9, 3, 'gpt-4-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (10, 3, 'gpt-4-turbo-2024-04-09', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (11, 3, 'gpt-4-turbo-preview', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (12, 4, 'gpt-3.5-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (13, 4, 'gpt-3.5-turbo-0125', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (14, 4, 'openai/gpt-3.5-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (15, 5, 'o1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (16, 5, 'o1-2024-12-17', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (17, 5, 'o1-preview', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (18, 6, 'o1-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (19, 6, 'o1-mini-2024-09-12', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (20, 7, 'o3', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (21, 7, 'o3-2025-04-16', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (22, 8, 'o3-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (23, 8, 'o3-mini-2025-01-31', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (24, 9, 'o4-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (25, 9, 'o4-mini-2025-04-16', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (26, 10, 'gpt-4o-audio-preview', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (27, 10, 'gpt-4o-audio-2024-10-01', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (28, 11, 'claude-sonnet-4.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (29, 11, 'claude-sonnet-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (30, 11, 'claude-sonnet-4-5-20250929', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (31, 11, 'anthropic/claude-sonnet-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (32, 12, 'claude-opus-4', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (33, 12, 'claude-opus-4-20250514', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (34, 12, 'anthropic/claude-opus-4', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (35, 13, 'claude-haiku-4.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (36, 13, 'claude-haiku-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (37, 13, 'claude-haiku-4-5-20251001', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (38, 13, 'anthropic/claude-haiku-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (39, 14, 'claude-opus-4.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (40, 14, 'claude-opus-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (41, 14, 'claude-opus-4-5-20251001', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (42, 14, 'anthropic/claude-opus-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (43, 15, 'claude-haiku-4.6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (44, 15, 'claude-haiku-4-6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (45, 15, 'claude-haiku-4-6-20251101', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (46, 15, 'anthropic/claude-haiku-4-6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (47, 16, 'claude-sonnet-4.6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (48, 16, 'claude-sonnet-4-6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (49, 16, 'claude-sonnet-4-6-20251101', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (50, 16, 'anthropic/claude-sonnet-4-6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (51, 17, 'claude-opus-4.6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (52, 17, 'claude-opus-4-6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (53, 17, 'claude-opus-4-6-20251101', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (54, 17, 'anthropic/claude-opus-4-6', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (55, 18, 'claude-opus-4.7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (56, 18, 'Claude-opus-4-7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (57, 18, 'claude-opus-4-7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (58, 18, 'claude-opus-4-7-20251201', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (59, 18, 'anthropic/claude-opus-4-7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (60, 19, 'claude-3-7-sonnet', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (61, 19, 'claude-3-7-sonnet-20250219', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (62, 19, 'anthropic/claude-3-7-sonnet', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (63, 20, 'claude-3-5-sonnet', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (64, 20, 'claude-3-5-sonnet-20241022', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (65, 20, 'claude-3-5-sonnet-20240620', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (66, 20, 'anthropic/claude-3-5-sonnet', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (67, 21, 'claude-3-5-haiku', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (68, 21, 'claude-haiku-3-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (69, 21, 'claude-3-5-haiku-20241022', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (70, 21, 'anthropic/claude-3-5-haiku', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (71, 22, 'gemini-2.5-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (72, 22, 'gemini-2.5-pro-preview-03-25', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (73, 22, 'google/gemini-2.5-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (74, 23, 'gemini-2.5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (75, 23, 'gemini-2.5-flash-preview-04-17', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (76, 23, 'google/gemini-2.5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (77, 24, 'gemini-2.0-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (78, 24, 'gemini-2.0-flash-exp', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (79, 24, 'google/gemini-2.0-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (80, 25, 'gemini-2.0-flash-lite', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (81, 25, 'google/gemini-2.0-flash-lite', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (82, 26, 'gemini-1.5-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (83, 26, 'gemini-1.5-pro-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (84, 26, 'google/gemini-1.5-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (85, 27, 'gemini-1.5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (86, 27, 'gemini-1.5-flash-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (87, 27, 'google/gemini-1.5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (88, 28, 'deepseek-chat', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (89, 28, 'deepseek-v3', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (90, 28, 'deepseek-ai/DeepSeek-V3', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (91, 28, 'deepseek/deepseek-v3', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (92, 29, 'deepseek-v3.1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (93, 29, 'deepseek-v3-1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (94, 29, 'deepseek-ai/DeepSeek-V3.1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (95, 30, 'deepseek-reasoner', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (96, 30, 'deepseek-r1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (97, 30, 'deepseek-ai/DeepSeek-R1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (98, 30, 'deepseek/deepseek-r1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (99, 31, 'qwen-max', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (100, 31, 'qwen-max-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (101, 31, 'Qwen/Qwen-Max', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (102, 32, 'qwen-plus', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (103, 32, 'qwen-plus-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (104, 33, 'qwen-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (105, 33, 'qwen-turbo-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (106, 34, 'qwen2.5-72b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (107, 34, 'Qwen/Qwen2.5-72B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (108, 35, 'qwen2.5-7b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (109, 35, 'Qwen/Qwen2.5-7B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (110, 36, 'qwq-32b', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (111, 36, 'Qwen/QwQ-32B', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (112, 37, 'doubao-1-5-pro-256k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (113, 38, 'doubao-1-5-pro-32k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (114, 39, 'doubao-1-5-lite-32k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (115, 40, 'doubao-pro-128k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (116, 41, 'doubao-pro-32k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (117, 42, 'doubao-pro-4k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (118, 43, 'doubao-lite-4k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (119, 44, 'doubao-seed-2.0-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (120, 44, 'doubao-seed-2-0-pro-260215', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (121, 45, 'doubao-seed-2.0-lite', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (122, 45, 'doubao-seed-2-0-lite-260215', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (123, 46, 'doubao-seed-2.0-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (124, 46, 'doubao-seed-2-0-mini-260215', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (125, 47, 'doubao-embedding-vision', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (126, 47, 'doubao-embedding-vision-241215', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (127, 48, 'doubao-embedding-large-text', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (128, 49, 'glm-5.1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (129, 49, 'glm-5.1-chat', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (130, 49, 'THUDM/glm-5.1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (131, 49, 'z-ai/glm-5.1', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (132, 50, 'glm-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (133, 50, 'THUDM/glm-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (134, 51, 'glm-4.7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (135, 51, 'glm-4-7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (136, 51, 'glm-4p7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (137, 51, 'glm-4-7-251222', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (138, 51, 'THUDM/glm-4.7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (139, 52, 'glm-4.7-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (140, 52, 'glm-4-7-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (141, 52, 'glm-4p7-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (142, 53, 'glm-4.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (143, 53, 'glm-4-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (144, 53, 'glm-4p5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (145, 53, 'THUDM/glm-4.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (146, 54, 'glm-4.5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (147, 54, 'glm-4-5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (148, 54, 'glm-4p5-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (149, 55, 'glm-4.5-air', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (150, 55, 'glm-4-5-air', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (151, 55, 'glm-4-5-air-20250728', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (152, 56, 'glm-4', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (153, 56, 'THUDM/glm-4', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (154, 57, 'glm-4-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (155, 58, 'glm-4-air', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (156, 59, 'glm-4-9b-chat', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (157, 59, 'THUDM/glm-4-9b-chat', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (158, 60, 'glm-z1-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (159, 61, 'glm-4v-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (160, 61, 'THUDM/glm-4v-flash', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (161, 62, 'llama-3.3-70b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (162, 62, 'llama-3.3-70b-versatile', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (163, 62, 'meta/llama-3.3-70b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (164, 62, 'meta-llama/Llama-3.3-70B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (165, 62, 'meta-llama/Llama-3.3-70B-Instruct-Turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (166, 63, 'llama-3.1-70b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (167, 63, 'meta/llama-3.1-70b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (168, 63, 'meta-llama/Meta-Llama-3.1-70B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (169, 63, 'meta-llama/Llama-3.1-70B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (170, 64, 'llama-3.1-8b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (171, 64, 'llama-3.1-8b-instant', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (172, 64, 'meta/llama-3.1-8b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (173, 64, 'meta-llama/Meta-Llama-3.1-8B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (174, 64, 'meta-llama/Llama-3.1-8B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (175, 65, 'llama-3.2-3b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (176, 65, 'meta/llama-3.2-3b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (177, 65, 'meta-llama/Llama-3.2-3B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (178, 66, 'llama-3.2-90b-vision-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (179, 66, 'meta-llama/Llama-3.2-90B-Vision-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (180, 67, 'llama-3.1-405b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (181, 67, 'meta/llama-3.1-405b-instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (182, 67, 'meta-llama/Meta-Llama-3.1-405B-Instruct', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (183, 68, 'MiniMax-Text-01', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (184, 68, 'minimax-text-01', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (185, 69, 'minimax-m2.7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (186, 69, 'minimax-m2-7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (187, 69, 'MiniMax-M2.7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (188, 69, 'minimaxai/minimax-m2.7', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (189, 70, 'minimax-m2.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (190, 70, 'MiniMax-M2.5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (191, 71, 'abab6.5s-chat', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (192, 72, 'abab5.5-chat', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (193, 73, 'moonshot-v1-128k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (194, 74, 'moonshot-v1-32k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (195, 75, 'moonshot-v1-8k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (196, 76, 'Baichuan4', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (197, 76, 'baichuan-4', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (198, 77, 'Baichuan3-Turbo-128k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (199, 77, 'baichuan3-turbo-128k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (200, 78, 'Baichuan3-Turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (201, 78, 'baichuan3-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (202, 79, 'step-2-16k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (203, 80, 'step-1-256k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (204, 81, 'step-1v-32k', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (205, 82, 'yi-large', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (206, 83, 'yi-medium', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (207, 84, 'yi-large-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (208, 85, 'SenseChat-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (209, 85, 'sensechat-5', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (210, 86, 'SenseChat-5-Thinking', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (211, 86, 'sensechat-5-thinking', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (212, 87, 'SenseChat-Turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (213, 87, 'sensechat-turbo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (214, 88, 'mistral-large-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (215, 88, 'mistral-large-2411', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (216, 89, 'mistral-small-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (217, 89, 'mistral-small-2503', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (218, 90, 'open-mistral-nemo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (219, 90, 'mistral-nemo', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (220, 91, 'codestral-latest', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (221, 91, 'codestral-2505', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (222, 92, 'command-r-plus', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (223, 92, 'command-r-plus-08-2024', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (224, 93, 'command-r', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (225, 93, 'command-r-08-2024', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (226, 94, 'grok-3', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (227, 94, 'grok-3-02-2025', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (228, 95, 'grok-3-mini', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (229, 95, 'grok-3-mini-02-2025', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (230, 96, 'grok-2-1212', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (231, 97, 'sonar-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (232, 98, 'sonar', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (233, 98, 'sonar-small', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (234, 99, 'sonar-reasoning', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (235, 100, 'MiMo-V2.5-Pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (236, 100, 'mimo-v2.5-pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (237, 100, 'mimo_v2_5_pro', NULL, NULL, 'active', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (238, 1, 'gpt-4o-2024-05-13', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.712473+00', '2026-06-11 16:22:46.712473+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (239, 103, 'claude-sonnet-4', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.714249+00', '2026-06-11 16:22:46.714249+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (240, 103, 'claude-sonnet-4-20250514', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.714249+00', '2026-06-11 16:22:46.714249+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (241, 103, 'claude-4-sonnet', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.714249+00', '2026-06-11 16:22:46.714249+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (242, 20, 'claude-3-5-sonnet-latest', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.715397+00', '2026-06-11 16:22:46.715397+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (243, 105, 'composer', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.71629+00', '2026-06-11 16:22:46.71629+00', '{cursor,roocode}') ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (244, 105, 'cursor-composer', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.71629+00', '2026-06-11 16:22:46.71629+00', '{cursor,roocode}') ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (245, 106, 'deepseek/deepseek-chat', NULL, NULL, 'active', 'agent-terminal seed', '2026-06-11 16:22:46.717263+00', '2026-06-11 16:22:46.717263+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (246, 354, 'claude-opus-4.8', NULL, NULL, 'active', NULL, '2026-06-20 06:35:04.63467+00', '2026-06-20 06:35:04.63467+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (248, 5704, 'MiniMax-M3', NULL, NULL, 'active', NULL, '2026-06-23 09:17:39.926948+00', '2026-06-23 09:17:39.926948+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_aliases VALUES (249, 5704, 'minimax-m3', NULL, NULL, 'active', NULL, '2026-06-23 09:17:52.50636+00', '2026-06-23 09:17:52.50636+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('xiaomi-mimo', 'Xiaomi Mimo', NULL, 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('xai', 'Xai', NULL, 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('openai-gpt', 'Openai Gpt', 'OpenAI', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('anthropic-claude', 'Anthropic Claude', 'Anthropic', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('google-gemini', 'Google Gemini', 'Google', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('deepseek', 'Deepseek', 'DeepSeek', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('qwen', 'Qwen', 'Alibaba', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('doubao', 'Doubao', 'ByteDance', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('zhipu-glm', 'Zhipu Glm', 'Zhipu AI', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('meta-llama', 'Meta Llama', 'Meta', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('minimax', 'Minimax', 'MiniMax', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-11 16:22:43.670498+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('gpt', 'GPT', 'OpenAI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('o3', 'OpenAI o-series', 'OpenAI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('o4', 'OpenAI o-series', 'OpenAI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('gemini', 'Gemini', 'Google', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('gemma', 'Gemma', 'Google', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('glm', 'GLM', 'Zhipu AI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('qwen2', 'Qwen', 'Alibaba', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('qwen3', 'Qwen 3', 'Alibaba', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('qwen3.5', 'Qwen 3.5', 'Alibaba', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('qwen3.6', 'Qwen 3.6', 'Alibaba', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('kimi', 'Kimi', 'Moonshot AI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('grok', 'Grok', 'xAI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('llama', 'Llama', 'Meta', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('llama2', 'Llama 2', 'Meta', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('llama3', 'Llama 3', 'Meta', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('mistral', 'Mistral', 'Mistral AI', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('ministral', 'Ministral', 'Mistral AI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('mixtral', 'Mixtral', 'Mistral AI', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('step', 'Step', 'StepFun', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('stepfun', 'Stepfun', 'StepFun', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('mimo', 'MiMo', '小米', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('phi', 'Phi', 'Microsoft', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('nemotron', 'Nemotron', 'NVIDIA', 'active', 'migration-016', NULL, '2026-06-18 20:09:53.754228+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('moonshot', 'Moonshot', 'Moonshot AI', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('baichuan', 'Baichuan', 'Baichuan', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('yi', 'Yi', '01.AI', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('perplexity', 'Perplexity', 'Perplexity', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('sensenova', 'Sensenova', '商汤', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.model_families VALUES ('cohere', 'Cohere', 'Cohere', 'active', 'derived', NULL, '2026-06-11 16:22:43.670498+00', '2026-06-18 20:09:53.754228+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (204, 'credential', 1, 9, NULL, 3, 'token_plan', 'USD', '{"tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate", "input_per_1m": "10.0", "output_per_1m": "30.0"}', '2026-06-12 10:31:32.797764+00', NULL, 'scraped', 0.950, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan', DEFAULT, '2026-06-12 10:31:32.797764+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (205, 'credential', 1, 9, NULL, 5, 'token_plan', 'USD', '{"tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate", "input_per_1m": "15.0", "output_per_1m": "60.0"}', '2026-06-12 10:31:32.797764+00', NULL, 'scraped', 0.950, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan', DEFAULT, '2026-06-12 10:31:32.797764+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (206, 'credential', 1, 9, NULL, 7, 'token_plan', 'USD', '{"tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate", "input_per_1m": "2.0", "output_per_1m": "8.0"}', '2026-06-12 10:31:32.797764+00', NULL, 'scraped', 0.950, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan', DEFAULT, '2026-06-12 10:31:32.797764+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (207, 'credential', 1, 9, NULL, 8, 'token_plan', 'USD', '{"tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate", "input_per_1m": "1.1", "output_per_1m": "4.4"}', '2026-06-12 10:31:32.797764+00', NULL, 'scraped', 0.950, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan', DEFAULT, '2026-06-12 10:31:32.797764+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (208, 'credential', 1, 9, NULL, 9, 'token_plan', 'USD', '{"tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate", "input_per_1m": "1.1", "output_per_1m": "4.4"}', '2026-06-12 10:31:32.797764+00', NULL, 'scraped', 0.950, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan', DEFAULT, '2026-06-12 10:31:32.797764+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (143, 'credential', 33, 10, NULL, 364, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (144, 'credential', 33, 10, NULL, 120, 'token', 'CNY', '{"currency": "USD", "input_per_1m": "0.14", "output_per_1m": "0.28", "cache_hit_per_1m": "0.0028"}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (145, 'credential', 33, 10, NULL, 121, 'token', 'CNY', '{"currency": "USD", "input_per_1m": "0.435", "output_per_1m": "0.87", "cache_hit_per_1m": "0.003625"}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (158, 'credential', 33, 10, NULL, 69, 'token', 'CNY', '{"input_per_1m": "0.30", "output_per_1m": "1.20", "cache_read_per_1m": "0.06", "cache_write_per_1m": "0.375"}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (138, 'credential', 33, 10, NULL, 352, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (139, 'credential', 33, 10, NULL, 350, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (140, 'credential', 33, 10, NULL, 353, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (141, 'credential', 33, 10, NULL, 354, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (142, 'credential', 33, 10, NULL, 351, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (146, 'credential', 33, 10, NULL, 358, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (147, 'credential', 33, 10, NULL, 370, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (148, 'credential', 33, 10, NULL, 359, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (149, 'credential', 33, 10, NULL, 51, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (150, 'credential', 33, 10, NULL, 50, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (151, 'credential', 33, 10, NULL, 49, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (152, 'credential', 33, 10, NULL, 356, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (153, 'credential', 33, 10, NULL, 355, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (154, 'credential', 33, 10, NULL, 357, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (155, 'credential', 33, 10, NULL, 369, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (156, 'credential', 33, 10, NULL, 363, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (157, 'credential', 33, 10, NULL, 166, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (159, 'credential', 33, 10, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (160, 'credential', 33, 10, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (161, 'credential', 33, 10, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (162, 'credential', 33, 10, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (163, 'credential', 14, 6, NULL, NULL, 'token', 'USD', '{"input_per_1m": "0.30", "output_per_1m": "1.20", "cache_read_per_1m": "0.06", "permanent_50_percent_off": true}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (164, 'credential', 14, 6, NULL, NULL, 'token', 'USD', '{"input_per_1m": "0.30", "output_per_1m": "1.20"}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (165, 'credential', 14, 6, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (166, 'credential', 14, 6, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (167, 'credential', 14, 6, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (168, 'credential', 1, 9, NULL, 100, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (169, 'credential', 1, 9, NULL, 1001, 'token_plan', 'CNY', '{"tier": "Standard", "monthly_cny": "99", "input_per_1m": "0.14", "output_per_1m": "0.28", "validity_days": "30"}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (170, 'credential', 1, 9, NULL, 999, 'token_plan', 'CNY', '{"tier": "Standard", "monthly_cny": "99", "input_per_1m": "0.14", "output_per_1m": "0.28", "validity_days": "30"}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (171, 'credential', 1, 9, NULL, 998, 'token_plan', 'CNY', '{"tier": "Standard", "modality": "multimodal", "monthly_cny": "99", "input_per_1m": "0.14", "output_per_1m": "0.28", "validity_days": "30"}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (172, 'credential', 1, 9, NULL, 1000, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (173, 'credential', 1, 9, NULL, 1002, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (174, 'credential', 1, 9, NULL, 1004, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (175, 'credential', 1, 9, NULL, 1005, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (176, 'credential', 1, 9, NULL, 1006, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (177, 'credential', 32, 7, NULL, 49, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (178, 'credential', 32, 7, NULL, 51, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (179, 'credential', 32, 7, NULL, 50, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (180, 'credential', 32, 7, NULL, 1153, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (181, 'credential', 32, 7, NULL, 55, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (182, 'credential', 32, 7, NULL, 1150, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (183, 'credential', 32, 7, NULL, 53, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.950, 'https://openrouter.ai/models', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (184, 'credential', 32, 7, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (185, 'credential', 32, 7, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (186, 'credential', 32, 7, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (187, 'credential', 35, 12, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (188, 'credential', 35, 12, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (189, 'credential', 35, 12, NULL, NULL, 'token', 'CNY', '{"note": "NEEDS_REVIEW", "unit": "per_image", "modality": "embedding"}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (190, 'credential', 35, 12, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (191, 'credential', 35, 12, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (192, 'credential', 35, 12, NULL, NULL, 'token', 'CNY', '{"note": "NEEDS_REVIEW", "custom_endpoint": true}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (193, 'credential', 34, 11, NULL, NULL, 'token_plan', 'CNY', '{"note": "NEEDS_REVIEW", "unit": "per_image", "modality": "embedding"}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (194, 'credential', 34, 11, NULL, NULL, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (195, 'credential', 34, 11, NULL, NULL, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (196, 'credential', 34, 11, NULL, NULL, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (197, 'credential', 34, 11, NULL, NULL, 'token_plan', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (198, 'credential', 36, 13, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (199, 'credential', 36, 13, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (200, 'credential', 36, 13, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (201, 'credential', 36, 13, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (202, 'credential', 37, 14, NULL, NULL, 'token', 'USD', '{}', '2026-06-12 10:15:25.592298+00', '2026-06-12 10:15:25.592298+00', 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.pricing_plans VALUES (203, 'credential', 37, 14, NULL, NULL, 'token', 'CNY', '{}', '2026-06-12 10:15:25.592298+00', NULL, 'scraped', 0.900, 'https://platform.MiniMax.io/docs/pricing-paygo', DEFAULT, '2026-06-12 10:15:25.592298+00') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('vapeur', 'tier1', 'Vapeur AI', NULL, 'official', 'cloud', 'openai-completions', 'https://api.vapeur.ai/v1', NULL, 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'registered 2026-06-11 by llm-gateway-go; base_url ends in /v1, append /models', 1, '2026-06-11 19:02:44.216748+00', '2026-06-11 19:02:44.216748+00', NULL, '{}', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('scnet', 'tier1', 'SCNet', NULL, 'official', 'cloud', 'openai-completions', 'https://api.scnet.cn/api/llm/v1', NULL, 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'registered 2026-06-11 by llm-gateway-go; base_url ends in /v1, append /models', 1, '2026-06-11 19:02:44.216748+00', '2026-06-11 19:02:44.216748+00', NULL, '{}', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('volcano-normal', 'tier1', 'Volcano Ark (OpenAI)', NULL, 'official', 'cloud', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/v3', NULL, 'direct', true, 1.0000, '[]', 'manifest', '/models', '[]', '{}', false, 'registered 2026-06-11 by llm-gateway-go; OpenAI-compat path uses native /v3/models', 1, '2026-06-11 19:02:44.216748+00', '2026-06-11 19:02:44.216748+00', NULL, '{}', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('volcano-tokenplan', 'tier1', 'Volcano Ark (TokenPlan)', NULL, 'official', 'cloud', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', NULL, 'direct', true, 1.0000, '[]', 'manifest', '/models', '[]', '{}', false, 'registered 2026-06-11 by llm-gateway-go; TokenPlan coding path /v3/models', 1, '2026-06-11 19:02:44.216748+00', '2026-06-11 19:02:44.216748+00', NULL, '{}', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('deepseek', 'tier1', 'DeepSeek', 'DeepSeek', 'official', 'cloud', 'openai-completions', 'https://api.deepseek.com/v1', 'https://platform.deepseek.com/docs', 'direct', true, 1.0000, '[{"id": "deepseek-chat", "ctx_k": 64, "display_name": "DeepSeek Chat (V3)"}, {"id": "deepseek-reasoner", "ctx_k": 64, "display_name": "DeepSeek R1"}]', 'manifest', '/models', '[]', '{}', false, '国内知名推理服务商，deepseek-chat/deepseek-reasoner 旗舰模型', 1, '2026-06-11 16:22:38.58003+00', '2026-06-11 16:22:38.58003+00', 'openai_official', '{"max_tokens_cap": 8192}', 'deepseek') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('minimax', 'tier1', 'MiniMax', 'MiniMax', 'official', 'cloud', 'openai-completions', 'https://api.minimax.chat/v1', 'https://platform.minimaxi.com/document/ChatCompletion', 'direct', true, 1.0000, '[{"id": "MiniMax-Text-01", "ctx_k": 1000, "display_name": "MiniMax Text-01"}, {"id": "abab6.5s-chat", "ctx_k": 245, "display_name": "MiniMax abab6.5s"}, {"id": "abab5.5-chat", "ctx_k": 16, "display_name": "MiniMax abab5.5"}]', 'manifest', '/models', '[]', '{}', false, 'MiniMax 国内多模态大模型，支持超长上下文', 1, '2026-06-11 16:22:38.585897+00', '2026-06-11 16:22:38.585897+00', 'openai_official', '{"tool_use": true, "simplify_tools": true, "strip_request_fields": ["parallel_tool_calls", "reasoning_effort"], "strip_stream_options": ["include_usage"]}', 'minimax') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('azure-openai', 'tier2', 'Azure OpenAI', 'Azure OpenAI', 'official', 'cloud', 'openai-completions', 'https://{resource}.openai.azure.com/openai/deployments/{deployment}', 'https://learn.microsoft.com/azure/ai-services/openai', 'proxy', false, 1.0000, '[]', 'auto', '', '[]', '{}', false, 'Azure OpenAI，需要在 providers.base_url 中填写资源名和部署名', 1, '2026-06-11 16:22:38.592077+00', '2026-06-11 16:22:38.592077+00', 'openai_official', '{}', 'azure-openai') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('cohere', 'tier2', 'Cohere', 'Cohere', 'official', 'cloud', 'openai-completions', 'https://api.cohere.com/compatibility/v1', 'https://docs.cohere.com', 'proxy', false, 1.0000, '[{"id": "command-r-plus", "ctx_k": 128, "display_name": "Command R+"}, {"id": "command-r", "ctx_k": 128, "display_name": "Command R"}, {"id": "command-a-03-2025", "ctx_k": 256, "display_name": "Command A (2025-03)"}]', 'manifest', '/models', '[]', '{}', false, 'Cohere Command 系列，支持 OpenAI 兼容端点', 1, '2026-06-11 16:22:38.592654+00', '2026-06-11 16:22:38.592654+00', 'openai_official', '{}', 'cohere') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('llamacpp', 'local', 'llama.cpp Server', 'llama.cpp Server', 'self_host', 'local', 'openai-completions', 'http://{host}:{port}/v1', 'https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md', 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'llama.cpp 内置 HTTP server，支持 OpenAI 兼容接口，GGUF 格式', 1, '2026-06-11 16:22:38.598424+00', '2026-06-11 16:22:38.598424+00', NULL, '{}', 'llamacpp') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('sensenova', 'tier1', '商汤 SenseNova', 'SenseNova', 'official', 'cloud', 'openai-completions', 'https://api.sensenova.cn/compatible-mode/v1', 'https://platform.sensenova.cn/doc', 'direct', true, 1.0000, '[{"id": "SenseChat-5", "ctx_k": 32, "display_name": "SenseChat 5"}, {"id": "SenseChat-5-Thinking", "ctx_k": 32, "display_name": "SenseChat 5 Thinking"}, {"id": "SenseChat-Turbo", "ctx_k": 32, "display_name": "SenseChat Turbo"}]', 'manifest', '/models', '[]', '{}', false, '商汤科技 SenseNova 大模型，国内主流推理平台', 1, '2026-06-11 16:22:38.590799+00', '2026-06-11 16:22:38.590799+00', 'openai_official', '{}', 'sensenova') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('anthropic', 'tier2', 'Anthropic', 'Anthropic', 'official', 'cloud', 'anthropic-messages', 'https://api.anthropic.com', 'https://docs.anthropic.com', 'proxy', false, 1.0000, '[{"id": "claude-opus-4-5", "ctx_k": 200, "display_name": "Claude Opus 4.5"}, {"id": "claude-sonnet-4-5", "ctx_k": 200, "display_name": "Claude Sonnet 4.5"}, {"id": "claude-haiku-3-5", "ctx_k": 200, "display_name": "Claude Haiku 3.5"}, {"id": "claude-3-7-sonnet-20250219", "ctx_k": 200, "display_name": "Claude 3.7 Sonnet"}]', 'manifest', '/v1/models', '[]', '{}', false, 'Claude 系列，使用 Anthropic 原生协议（messages API）', 1, '2026-06-11 16:22:38.591489+00', '2026-06-11 16:22:38.591489+00', 'anthropic_official', '{"tool_use": true}', 'anthropic') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('siliconflow', 'tier1', '硅基流动', 'SiliconFlow', 'aggregator', 'cloud', 'openai-completions', 'https://api.siliconflow.cn/v1', 'https://docs.siliconflow.cn/introduction', 'direct', true, 1.0000, '[{"id": "Qwen/Qwen2.5-72B-Instruct", "ctx_k": 32, "display_name": "Qwen2.5-72B (SiliconFlow)"}, {"id": "Qwen/QwQ-32B", "ctx_k": 32, "display_name": "QwQ-32B (SiliconFlow)"}, {"id": "deepseek-ai/DeepSeek-V3", "ctx_k": 64, "display_name": "DeepSeek-V3 (SiliconFlow)"}, {"id": "deepseek-ai/DeepSeek-R1", "ctx_k": 64, "display_name": "DeepSeek-R1 (SiliconFlow)"}, {"id": "meta-llama/Meta-Llama-3.1-8B-Instruct", "ctx_k": 128, "display_name": "Llama 3.1 8B (SiliconFlow)"}, {"id": "THUDM/glm-4-9b-chat", "ctx_k": 128, "display_name": "GLM-4-9B (SiliconFlow)"}]', 'manifest', '/models', '[]', '{}', false, '国内 LLM 推理聚合平台，托管多家开源模型，按 token 计费', 1, '2026-06-11 16:22:38.587145+00', '2026-06-11 16:22:38.587145+00', 'openai_relay', '{}', 'siliconflow') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('lmstudio', 'local', 'LM Studio', 'LM Studio', 'self_host', 'local', 'openai-completions', 'http://{host}:{port}/v1', 'https://lmstudio.ai/docs/api/openai-api', 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'LM Studio 本地服务，OpenAI 兼容，默认端口 1234，用于桌面端模型测试', 1, '2026-06-11 16:22:38.599017+00', '2026-06-11 16:22:38.599017+00', NULL, '{}', 'lmstudio') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('mlx', 'local', 'Apple MLX Server', 'Apple MLX Server', 'self_host', 'local', 'openai-completions', 'http://{host}:{port}/v1', 'https://github.com/ml-explore/mlx-lm?tab=readme-ov-file#generate-text', 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'mlx_lm.server，Apple Silicon 专用，Metal GPU 加速，仅限 macOS', 1, '2026-06-11 16:22:38.599615+00', '2026-06-11 16:22:38.599615+00', NULL, '{}', 'mlx') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('ollama', 'local', 'Ollama', 'Ollama', 'self_host', 'local', 'openai-completions', 'http://{host}:{port}/v1', 'https://github.com/ollama/ollama/blob/main/docs/openai.md', 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, '本地 LLM 运行框架，支持全生命周期管理（pull/run/stop），默认端口 11434', 1, '2026-06-11 16:22:38.600178+00', '2026-06-11 16:22:38.600178+00', NULL, '{}', 'ollama') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('vllm', 'local', 'vLLM Server', 'vLLM Server', 'self_host', 'local', 'openai-completions', 'http://{host}:{port}/v1', 'https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html', 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'vLLM 高效推理引擎，PagedAttention，适合 GPU 服务器，OpenAI 兼容', 1, '2026-06-11 16:22:38.600824+00', '2026-06-11 16:22:38.600824+00', NULL, '{}', 'vllm') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('moonshot', 'tier1', 'Moonshot / Kimi', 'Moonshot', 'official', 'cloud', 'openai-completions', 'https://api.moonshot.cn/v1', 'https://platform.moonshot.cn/docs', 'direct', true, 1.0000, '[{"id": "moonshot-v1-8k", "ctx_k": 8, "display_name": "Moonshot v1 8k"}, {"id": "moonshot-v1-32k", "ctx_k": 32, "display_name": "Moonshot v1 32k"}, {"id": "moonshot-v1-128k", "ctx_k": 128, "display_name": "Moonshot v1 128k"}]', 'manifest', '/models', '[]', '{}', false, '月之暗面 Kimi 大模型，国内主流推理平台', 1, '2026-06-11 16:22:38.588341+00', '2026-06-11 16:22:38.588341+00', 'openai_official', '{}', 'moonshot') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('nvidia', 'tier2', 'NVIDIA NIM', 'NVIDIA NIM', 'aggregator', 'cloud', 'openai-completions', 'https://integrate.api.nvidia.com/v1', 'https://docs.api.nvidia.com/nim/reference/llm-apis', 'proxy', false, 1.0000, '[{"id": "z-ai/glm-5.1", "ctx_k": 128, "display_name": "GLM-5.1 (NVIDIA)"}, {"id": "minimaxai/minimax-m2.7", "ctx_k": 245, "display_name": "MiniMax M2.7 (NVIDIA)"}, {"id": "meta/llama-3.3-70b-instruct", "ctx_k": 131, "display_name": "Llama 3.3 70B (NVIDIA)"}, {"id": "nvidia/llama-3.3-nemotron-super-49b-v1.5", "ctx_k": 131, "display_name": "Nemotron Super 49B v1.5"}, {"id": "qwen/qwen3-5-122b-a10b", "ctx_k": 131, "display_name": "Qwen3.5 122B (NVIDIA)"}, {"id": "deepseek-ai/deepseek-v4-flash", "ctx_k": 64, "display_name": "DeepSeek V4 Flash (NVIDIA)"}]', 'auto', '/models', '[]', '{}', false, 'NVIDIA NIM 云端推理，托管多家开源模型，OpenAI 兼容，需海外网络访问', 1, '2026-06-11 16:22:38.602581+00', '2026-06-11 16:22:38.602581+00', 'openai_relay', '{}', 'nvidia') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('yi', 'tier1', '零一万物', '01.AI / Yi', 'official', 'cloud', 'openai-completions', 'https://api.lingyiwanwu.com/v1', 'https://platform.lingyiwanwu.com/docs', 'direct', true, 1.0000, '[{"id": "yi-large", "ctx_k": 32, "display_name": "Yi Large"}, {"id": "yi-medium", "ctx_k": 16, "display_name": "Yi Medium"}, {"id": "yi-large-turbo", "ctx_k": 16, "display_name": "Yi Large Turbo"}]', 'manifest', '/models', '[]', '{}', false, '零一万物 Yi 大模型，国内主流推理平台', 1, '2026-06-11 16:22:38.590206+00', '2026-06-11 16:22:38.590206+00', 'openai_official', '{}', 'yi') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('fireworks', 'tier2', 'Fireworks AI', 'Fireworks AI', 'aggregator', 'cloud', 'openai-completions', 'https://api.fireworks.ai/inference/v1', 'https://docs.fireworks.ai', 'proxy', false, 1.0000, '[{"id": "accounts/fireworks/models/llama-v3p1-70b-instruct", "ctx_k": 131, "display_name": "Llama 3.1 70B (Fireworks)"}, {"id": "accounts/fireworks/models/deepseek-r1", "ctx_k": 64, "display_name": "DeepSeek R1 (Fireworks)"}, {"id": "accounts/fireworks/models/mixtral-8x7b-instruct", "ctx_k": 32, "display_name": "Mixtral 8x7B (Fireworks)"}]', 'manifest', '/models', '[]', '{}', false, '开源模型托管/推理平台，低延迟，支持 LoRA 等微调模型', 1, '2026-06-11 16:22:38.59322+00', '2026-06-11 16:22:38.59322+00', 'openai_relay', '{}', 'fireworks') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('openrouter', 'tier2', 'OpenRouter', 'OpenRouter', 'third_party_relay', 'cloud', 'openai-completions', 'https://openrouter.ai/api/v1', 'https://openrouter.ai/docs', 'proxy', false, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, '模型路由聚合商，统一接入数百个模型，model 字段格式 provider/model-name', 1, '2026-06-11 16:22:38.596103+00', '2026-06-11 16:22:38.596103+00', 'openai_relay', '{}', 'openrouter') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('together', 'tier2', 'Together AI', 'Together AI', 'aggregator', 'cloud', 'openai-completions', 'https://api.together.xyz/v1', 'https://docs.together.ai', 'proxy', false, 1.0000, '[{"id": "meta-llama/Llama-3.3-70B-Instruct-Turbo", "ctx_k": 131, "display_name": "Llama 3.3 70B (Together)"}, {"id": "deepseek-ai/DeepSeek-R1", "ctx_k": 64, "display_name": "DeepSeek R1 (Together)"}, {"id": "Qwen/Qwen2.5-72B-Instruct-Turbo", "ctx_k": 32, "display_name": "Qwen2.5-72B (Together)"}, {"id": "mistralai/Mixtral-8x7B-Instruct-v0.1", "ctx_k": 32, "display_name": "Mixtral 8x7B (Together)"}]', 'manifest', '/models', '[]', '{}', false, '开源模型云端推理，价格较低，支持 200+ 模型', 1, '2026-06-11 16:22:38.597349+00', '2026-06-11 16:22:38.597349+00', 'openai_relay', '{}', 'together') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('zhipu', 'tier1', '智谱AI', 'Zhipu AI', 'official', 'cloud', 'openai-completions', 'https://open.bigmodel.cn/api/paas/v4', 'https://open.bigmodel.cn/dev/howuse/introduction', 'direct', true, 1.0000, '[{"id": "glm-4", "ctx_k": 128, "display_name": "GLM-4"}, {"id": "glm-4-flash", "ctx_k": 128, "display_name": "GLM-4 Flash"}, {"id": "glm-4-air", "ctx_k": 128, "display_name": "GLM-4 Air"}, {"id": "glm-4-9b-chat", "ctx_k": 128, "display_name": "GLM-4 9B Chat"}, {"id": "glm-4.7", "ctx_k": 128, "display_name": "GLM-4.7"}, {"id": "glm-4.7-flash", "ctx_k": 128, "display_name": "GLM-4.7 Flash"}, {"id": "glm-4.5", "ctx_k": 128, "display_name": "GLM-4.5"}, {"id": "glm-4.5-flash", "ctx_k": 128, "display_name": "GLM-4.5 Flash"}, {"id": "glm-4.5-air", "ctx_k": 128, "display_name": "GLM-4.5 Air"}, {"id": "glm-z1-flash", "ctx_k": 64, "display_name": "GLM-Z1 Flash（推理）"}, {"id": "glm-5.1", "ctx_k": 128, "display_name": "GLM-5.1"}, {"id": "glm-5.2", "ctx_k": 128, "display_name": "GLM-5.2"}]', 'manifest', '/models', '[]', '{}', false, '清华系 GLM 大模型，国内知名推理平台', 1, '2026-06-11 16:22:38.587737+00', '2026-06-11 16:22:38.587737+00', 'openai_official', '{"tool_use": true}', 'zhipu') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('doubao', 'tier1', '豆包（字节跳动）', 'Doubao (ByteDance)', 'official', 'cloud', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'https://www.volcengine.com/docs/82379', 'direct', true, 1.0000, '[{"id": "doubao-seed-2.0-pro", "ctx_k": 128, "display_name": "豆包 Seed 2.0 Pro"}, {"id": "doubao-seed-2.0-lite", "ctx_k": 128, "display_name": "豆包 Seed 2.0 Lite"}, {"id": "doubao-seed-2.0-mini", "ctx_k": 128, "display_name": "豆包 Seed 2.0 Mini"}, {"id": "doubao-1-5-pro-32k", "ctx_k": 32, "display_name": "豆包 1.5 Pro 32K"}, {"id": "doubao-1-5-pro-256k", "ctx_k": 256, "display_name": "豆包 1.5 Pro 256K"}, {"id": "doubao-1-5-lite-32k", "ctx_k": 32, "display_name": "豆包 1.5 Lite 32K"}, {"id": "doubao-pro-128k", "ctx_k": 128, "display_name": "豆包 Pro 128K"}, {"id": "doubao-pro-32k", "ctx_k": 32, "display_name": "豆包 Pro 32K"}, {"id": "doubao-pro-4k", "ctx_k": 4, "display_name": "豆包 Pro 4K"}, {"id": "doubao-lite-4k", "ctx_k": 4, "display_name": "豆包 Lite 4K"}, {"id": "doubao-embedding-vision", "ctx_k": 32, "display_name": "豆包嵌入（视觉）"}, {"id": "doubao-embedding-large-text", "ctx_k": 32, "display_name": "豆包嵌入（大文本）"}, {"id": "glm-4.7", "ctx_k": 128, "display_name": "GLM-4.7（火山方舟）"}, {"id": "glm-4-5-air-20250728", "ctx_k": 128, "display_name": "GLM-4.5 Air（火山方舟）"}]', 'manifest', '/models', '[]', '{}', false, '字节跳动豆包（火山方舟），OpenAI 兼容协议', 1, '2026-06-11 16:22:38.585191+00', '2026-06-11 16:22:38.585191+00', 'openai_official', '{}', 'doubao') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('qwen', 'tier1', '阿里百炼（通义千问）', 'Alibaba Qwen (Bailian)', 'official', 'cloud', 'openai-completions', 'https://dashscope.aliyuncs.com/compatible-mode/v1', 'https://help.aliyun.com/zh/model-studio/developer-reference/use-qwen-by-calling-api', 'direct', true, 1.0000, '[{"id": "qwen-max", "ctx_k": 32, "display_name": "通义千问 Max"}, {"id": "qwen-plus", "ctx_k": 131, "display_name": "通义千问 Plus"}, {"id": "qwen-turbo", "ctx_k": 131, "display_name": "通义千问 Turbo"}, {"id": "qwen2.5-72b-instruct", "ctx_k": 131, "display_name": "Qwen2.5-72B Instruct"}, {"id": "qwq-32b", "ctx_k": 131, "display_name": "QwQ-32B（推理）"}]', 'manifest', '/models', '[]', '{}', false, '阿里云百炼平台，通义千问系列，支持 OpenAI 兼容接口', 1, '2026-06-11 16:22:38.586489+00', '2026-06-11 16:22:38.586489+00', 'openai_official', '{}', 'qwen') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('baichuan', 'tier1', '百川 AI', 'Baichuan', 'official', 'cloud', 'openai-completions', 'https://api.baichuan-ai.com/v1', 'https://platform.baichuan-ai.com/docs', 'direct', true, 1.0000, '[{"id": "Baichuan4", "ctx_k": 32, "display_name": "Baichuan 4"}, {"id": "Baichuan3-Turbo", "ctx_k": 32, "display_name": "Baichuan 3 Turbo"}, {"id": "Baichuan3-Turbo-128k", "ctx_k": 128, "display_name": "Baichuan 3 Turbo 128k"}]', 'manifest', '/models', '[]', '{}', false, '百川智能大模型，国内主流推理平台', 1, '2026-06-11 16:22:38.588972+00', '2026-06-11 16:22:38.588972+00', 'openai_official', '{}', 'baichuan') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('stepfun', 'tier1', '阶跃星辰', 'StepFun', 'official', 'cloud', 'openai-completions', 'https://api.stepfun.com/v1', 'https://platform.stepfun.com/docs', 'direct', true, 1.0000, '[{"id": "step-1v-32k", "ctx_k": 32, "display_name": "Step-1V 32k"}, {"id": "step-2-16k", "ctx_k": 16, "display_name": "Step-2 16k"}, {"id": "step-1-256k", "ctx_k": 256, "display_name": "Step-1 256k"}]', 'manifest', '/models', '[]', '{}', false, '阶跃星辰大模型，国内主流推理平台', 1, '2026-06-11 16:22:38.58954+00', '2026-06-11 16:22:38.58954+00', 'openai_official', '{}', 'stepfun') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('google-gemini', 'tier2', 'Google Gemini', 'Google Gemini', 'official', 'cloud', 'openai-completions', 'https://generativelanguage.googleapis.com/v1beta/openai', 'https://ai.google.dev/gemini-api/docs', 'proxy', false, 1.0000, '[{"id": "gemini-2.5-pro", "ctx_k": 1000, "display_name": "Gemini 2.5 Pro"}, {"id": "gemini-2.5-flash", "ctx_k": 1000, "display_name": "Gemini 2.5 Flash"}, {"id": "gemini-1.5-pro", "ctx_k": 2000, "display_name": "Gemini 1.5 Pro"}, {"id": "gemini-1.5-flash", "ctx_k": 1000, "display_name": "Gemini 1.5 Flash"}]', 'manifest', '/models', '[]', '{}', false, 'Google Gemini，通过 OpenAI 兼容端点接入', 1, '2026-06-11 16:22:38.59377+00', '2026-06-11 16:22:38.59377+00', 'openai_official', '{}', 'google-gemini') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('groq', 'tier2', 'Groq', 'Groq', 'official', 'cloud', 'openai-completions', 'https://api.groq.com/openai/v1', 'https://console.groq.com/docs', 'proxy', false, 1.0000, '[{"id": "llama-3.3-70b-versatile", "ctx_k": 128, "display_name": "Llama 3.3 70B (Groq)"}, {"id": "llama-3.1-8b-instant", "ctx_k": 128, "display_name": "Llama 3.1 8B Instant (Groq)"}, {"id": "mixtral-8x7b-32768", "ctx_k": 32, "display_name": "Mixtral 8x7B (Groq)"}, {"id": "gemma2-9b-it", "ctx_k": 8, "display_name": "Gemma 2 9B (Groq)"}]', 'manifest', '/models', '[]', '{}', false, 'Groq LPU 高速推理，极低延迟', 1, '2026-06-11 16:22:38.594324+00', '2026-06-11 16:22:38.594324+00', 'openai_official', '{}', 'groq') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('mistral', 'tier2', 'Mistral AI', 'Mistral AI', 'official', 'cloud', 'openai-completions', 'https://api.mistral.ai/v1', 'https://docs.mistral.ai', 'proxy', false, 1.0000, '[{"id": "mistral-large-latest", "ctx_k": 128, "display_name": "Mistral Large"}, {"id": "mistral-small-latest", "ctx_k": 32, "display_name": "Mistral Small"}, {"id": "mistral-nemo", "ctx_k": 128, "display_name": "Mistral Nemo"}, {"id": "codestral-latest", "ctx_k": 256, "display_name": "Codestral"}]', 'manifest', '/models', '[]', '{}', false, '欧洲 LLM 新贵，Mistral/Mixtral/Codestral 系列', 1, '2026-06-11 16:22:38.594886+00', '2026-06-11 16:22:38.594886+00', 'openai_official', '{}', 'mistral') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('openai', 'tier2', 'OpenAI', 'OpenAI', 'official', 'cloud', 'openai-completions', 'https://api.openai.com/v1', 'https://platform.openai.com/docs', 'proxy', false, 1.0000, '[{"id": "gpt-4o", "ctx_k": 128, "display_name": "GPT-4o"}, {"id": "gpt-4o-mini", "ctx_k": 128, "display_name": "GPT-4o Mini"}, {"id": "o3", "ctx_k": 200, "display_name": "o3"}, {"id": "o4-mini", "ctx_k": 200, "display_name": "o4-mini"}, {"id": "o3-mini", "ctx_k": 200, "display_name": "o3-mini"}]', 'manifest', '/models', '[]', '{}', false, 'OpenAI 官方 API，GPT-4o 和 o 系列', 1, '2026-06-11 16:22:38.595541+00', '2026-06-11 16:22:38.595541+00', 'openai_official', '{"tool_use": true}', 'openai') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('perplexity', 'tier2', 'Perplexity AI', 'Perplexity AI', 'official', 'cloud', 'openai-completions', 'https://api.perplexity.ai', 'https://docs.perplexity.ai', 'proxy', false, 1.0000, '[{"id": "sonar-pro", "ctx_k": 200, "display_name": "Sonar Pro（联网）"}, {"id": "sonar", "ctx_k": 128, "display_name": "Sonar（联网）"}, {"id": "sonar-reasoning", "ctx_k": 128, "display_name": "Sonar Reasoning（联网+推理）"}]', 'manifest', '/models', '[]', '{}', false, '带联网检索能力的推理服务，Sonar 系列', 1, '2026-06-11 16:22:38.596752+00', '2026-06-11 16:22:38.596752+00', 'openai_official', '{}', 'perplexity') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('xai', 'tier2', 'xAI (Grok)', 'xAI (Grok)', 'official', 'cloud', 'openai-completions', 'https://api.x.ai/v1', 'https://docs.x.ai/api', 'proxy', false, 1.0000, '[{"id": "grok-3", "ctx_k": 131, "display_name": "Grok 3"}, {"id": "grok-3-mini", "ctx_k": 131, "display_name": "Grok 3 Mini"}, {"id": "grok-2-1212", "ctx_k": 131, "display_name": "Grok 2"}]', 'manifest', '/models', '[]', '{}', false, 'Elon Musk 旗下 xAI，Grok 系列模型，OpenAI 兼容协议', 1, '2026-06-11 16:22:38.597897+00', '2026-06-11 16:22:38.597897+00', 'openai_official', '{}', 'xai') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('github-copilot', 'restricted', 'GitHub Copilot', 'GitHub Copilot', 'official_proxy', 'cloud', 'openai-completions', 'https://api.githubcopilot.com', 'https://github.com/features/copilot', 'proxy', false, 1.0000, '[{"id": "gpt-4o", "ctx_k": 128, "display_name": "GPT-4o (Copilot)"}, {"id": "claude-3-7-sonnet-20250219", "ctx_k": 200, "display_name": "Claude 3.7 Sonnet (Copilot)"}, {"id": "o3-mini", "ctx_k": 200, "display_name": "o3-mini (Copilot)"}]', 'manifest', '', '[]', '{}', true, 'GitHub Copilot API，token 通过 device flow 授权，受 GitHub ToS 限制，谨慎使用', 1, '2026-06-11 16:22:38.601395+00', '2026-06-11 16:22:38.601395+00', 'openai_relay', '{}', 'github-copilot') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('xiaomi', 'tier2', '小米大模型', 'Xiaomi MiMo', 'official', 'cloud', 'openai-completions', 'https://token-plan-cn.xiaomimimo.com/v1', 'https://token-plan-cn.xiaomimimo.com', 'direct', true, 1.0000, '[{"id": "MiMo-V2.5-Pro", "ctx_k": 128, "display_name": "MiMo V2.5 Pro"}]', 'auto', '/models', '[]', '{}', false, '小米大模型（MiMo），OpenAI 兼容协议，支持模型自动发现', 1, '2026-06-11 16:22:38.601959+00', '2026-06-11 16:22:38.601959+00', 'openai_official', '{}', 'xiaomi') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('evol', 'tier2', 'EvolAI 聚合代理', 'EvolAI Aggregator', 'aggregator', 'cloud', 'openai-completions', 'https://mg-new.evolai.cn/openclaw-proxy/v1', 'https://mg-new.evolai.cn', 'direct', true, 1.0000, '[]', 'auto', '/models', '[]', '{}', false, 'EvolAI 多模型聚合代理，统一接入 Anthropic/OpenAI/Google/DeepSeek/Zhipu 等，OpenAI 兼容协议', 1, '2026-06-11 16:41:03.347756+00', '2026-06-11 16:41:03.347756+00', NULL, '{}', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.provider_catalog VALUES ('volcengine-coding', 'tier1', '火山方舟 Coding', 'Volcengine Coding', 'aggregator', 'cloud', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'https://www.volcengine.com/docs/82379', 'direct', true, 1.0000, '[{"id": "glm-4-7-251222", "ctx_k": 128, "display_name": "GLM-4.7 (Volcengine)"}, {"id": "glm-4-5-air-20250728", "ctx_k": 128, "display_name": "GLM-4.5 Air (Volcengine)"}, {"id": "doubao-seed-2-0-pro-260215", "ctx_k": 128, "display_name": "豆包 Seed 2.0 Pro"}, {"id": "doubao-seed-2-0-lite-260215", "ctx_k": 128, "display_name": "豆包 Seed 2.0 Lite"}, {"id": "doubao-seed-2-0-mini-260215", "ctx_k": 128, "display_name": "豆包 Seed 2.0 Mini"}, {"id": "deepseek-v3-2-251201", "ctx_k": 64, "display_name": "DeepSeek V3 (Volcengine)"}, {"id": "doubao-embedding-vision-241215", "ctx_k": 32, "display_name": "豆包嵌入视觉"}, {"id": "doubao-seed-code", "ctx_k": 128, "display_name": "Doubao Seed Code"}, {"id": "doubao-seed-2.0-code", "ctx_k": 128, "display_name": "Doubao Seed 2.0 Code"}, {"id": "doubao-seed-2.0-pro", "ctx_k": 128, "display_name": "Doubao Seed 2.0 Pro"}, {"id": "doubao-seed-2.0-lite", "ctx_k": 128, "display_name": "Doubao Seed 2.0 Lite"}, {"id": "minimax-m2.7", "ctx_k": 128, "display_name": "MiniMax M2.7"}, {"id": "glm-5.1", "ctx_k": 128, "display_name": "GLM-5.1"}, {"id": "kimi-k2.6", "ctx_k": 128, "display_name": "Kimi K2.6"}, {"id": "deepseek-v4-pro", "ctx_k": 64, "display_name": "DeepSeek V4 Pro"}, {"id": "deepseek-v4-flash", "ctx_k": 64, "display_name": "DeepSeek V4 Flash"}, {"id": "minimax-m3", "ctx_k": 128, "display_name": "MiniMax M3"}]', 'manifest', '/models', '[]', '{}', false, '火山方舟 Coding Plan，聚合 GLM/Doubao/DeepSeek 等模型，含嵌入模型', 1, '2026-06-11 16:22:38.603179+00', '2026-06-11 16:22:38.603179+00', 'openai_relay', '{}', 'volcengine-coding') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_settings VALUES (1, 581, 'compression.mode', '"off"', true, 'admin', '2026-06-20 21:48:58.049531+00', '2026-06-20 21:48:58.049531+00') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_settings VALUES (2, 581, 'format_conversion.enabled', 'true', true, 'admin', '2026-06-20 21:49:30.403988+00', '2026-06-20 21:49:30.403988+00') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_settings VALUES (3, 581, 'cache.enabled', 'false', true, 'admin', '2026-06-20 21:49:33.655731+00', '2026-06-20 21:49:35.596238+00') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_settings VALUES (5, 587, 'compression.mode', '"off"', true, 'admin', '2026-06-20 21:49:59.473334+00', '2026-06-20 21:49:59.473334+00') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_settings VALUES (6, 587, 'cache.enabled', 'false', true, 'admin', '2026-06-20 21:50:04.463605+00', '2026-06-20 21:50:04.463605+00') ON CONFLICT DO NOTHING;
INSERT INTO public.provider_settings VALUES (7, 14, 'compression.mode', '"off"', true, 'admin', '2026-06-20 21:50:17.866341+00', '2026-06-20 21:50:17.866341+00') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (1, 'default', 'xiaomi', '小米大模型', 'xiaomi', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://token-plan-cn.xiaomimimo.com/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:22:58.041562+00', '2026-06-11 16:22:58.041562+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (2, 'default', 'anthropic', 'Anthropic', 'anthropic', false, NULL, '[]', 'cloud', 'official', 'anthropic-messages', 'https://api.anthropic.com', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (3, 'default', 'azure-openai', 'Azure OpenAI', 'azure-openai', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://{resource}.openai.azure.com/openai/deployments/{deployment}', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (4, 'default', 'baichuan', '百川 AI', 'baichuan', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.baichuan-ai.com/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (5, 'default', 'cohere', 'Cohere', 'cohere', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.cohere.com/compatibility/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (6, 'default', 'deepseek', 'DeepSeek', 'deepseek', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.deepseek.com/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (7, 'default', 'doubao', '豆包（字节跳动）', 'doubao', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (8, 'default', 'fireworks', 'Fireworks AI', 'fireworks', false, NULL, '[]', 'cloud', 'aggregator', 'openai-completions', 'https://api.fireworks.ai/inference/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (9, 'default', 'github-copilot', 'GitHub Copilot', 'github-copilot', false, NULL, '[]', 'cloud', 'official_proxy', 'openai-completions', 'https://api.githubcopilot.com', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (10, 'default', 'google-gemini', 'Google Gemini', 'google-gemini', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://generativelanguage.googleapis.com/v1beta/openai', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (11, 'default', 'groq', 'Groq', 'groq', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.groq.com/openai/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (12, 'default', 'llamacpp', 'llama.cpp Server', 'llamacpp', false, NULL, '[]', 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (13, 'default', 'lmstudio', 'LM Studio', 'lmstudio', false, NULL, '[]', 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (15, 'default', 'mistral', 'Mistral AI', 'mistral', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.mistral.ai/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (16, 'default', 'mlx', 'Apple MLX Server', 'mlx', false, NULL, '[]', 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (17, 'default', 'moonshot', 'Moonshot / Kimi', 'moonshot', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.moonshot.cn/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (18, 'default', 'nvidia', 'NVIDIA NIM', 'nvidia', false, NULL, '[]', 'cloud', 'aggregator', 'openai-completions', 'https://integrate.api.nvidia.com/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (19, 'default', 'ollama', 'Ollama', 'ollama', false, NULL, '[]', 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (20, 'default', 'openai', 'OpenAI', 'openai', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.openai.com/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (21, 'default', 'openrouter', 'OpenRouter', 'openrouter', false, NULL, '[]', 'cloud', 'third_party_relay', 'openai-completions', 'https://openrouter.ai/api/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (22, 'default', 'perplexity', 'Perplexity AI', 'perplexity', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.perplexity.ai', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (23, 'default', 'qwen', '阿里百炼（通义千问）', 'qwen', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://dashscope.aliyuncs.com/compatible-mode/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (24, 'default', 'sensenova', '商汤 SenseNova', 'sensenova', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.sensenova.cn/compatible-mode/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (25, 'default', 'siliconflow', '硅基流动', 'siliconflow', false, NULL, '[]', 'cloud', 'aggregator', 'openai-completions', 'https://api.siliconflow.cn/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (26, 'default', 'stepfun', '阶跃星辰', 'stepfun', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.stepfun.com/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (27, 'default', 'together', 'Together AI', 'together', false, NULL, '[]', 'cloud', 'aggregator', 'openai-completions', 'https://api.together.xyz/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (28, 'default', 'vllm', 'vLLM Server', 'vllm', false, NULL, '[]', 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (29, 'default', 'volcengine-coding', '火山方舟 Coding', 'volcengine-coding', false, NULL, '[]', 'cloud', 'aggregator', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (30, 'default', 'xai', 'xAI (Grok)', 'xai', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.x.ai/v1', 'proxy', false, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (31, 'default', 'yi', '零一万物', 'yi', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.lingyiwanwu.com/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (32, 'default', 'zhipu', '智谱AI', 'zhipu', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://open.bigmodel.cn/api/coding/paas/v4', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (37, 'default', 'scnet', '国家超算中心 (scnet)', NULL, true, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.scnet.cn/api/llm/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 17:32:51.87869+00', '2026-06-11 17:32:51.87869+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (33, 'default', 'evol', 'EvolAI 聚合代理', 'evol', false, NULL, '[]', 'cloud', 'aggregator', 'openai-completions', 'https://mg-new.evolai.cn/openclaw-proxy/v1', 'direct', true, 1.0000, false, 1.000, NULL, NULL, '2026-06-11 16:41:03.347756+00', '2026-06-20 06:52:41.199199+00', true, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (34, 'default', 'volcano-tokenplan', '火山方舟 TokenPlan', 'volcengine-coding', true, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 17:32:51.450125+00', '2026-06-11 17:32:51.450125+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (35, 'default', 'volcano-normal', '火山方舟 普通版', 'volcengine-coding', true, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/v3', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 17:32:51.587743+00', '2026-06-11 17:32:51.587743+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (36, 'default', 'vapeur', 'Vapeur AI (OpenAI兼容)', NULL, true, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.vapeur.ai/v1', 'direct', true, 1.0000, false, 1.000, NULL, NULL, '2026-06-11 17:32:51.740006+00', '2026-06-13 02:49:18.382372+00', true, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (587, 'default', 'apiclaude', 'apiclaude', NULL, true, NULL, '[]', 'cloud', 'third_party_relay', 'anthropic-messages', 'https://apiclaude.cc', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-20 06:13:12.800436+00', '2026-06-20 21:22:12.066955+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (2433, 'default', 'mock-openai', 'Mock OpenAI (8901)', NULL, true, NULL, '[]', 'local', 'self_host', 'openai', 'http://127.0.0.1:8901', 'direct', true, 0.0100, true, 1.000, NULL, NULL, '2026-06-24 09:53:59.912737+00', '2026-06-24 09:53:59.912737+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (2434, 'default', 'mock-anthropic', 'Mock Anthropic (8902)', NULL, true, NULL, '[]', 'local', 'self_host', 'anthropic', 'http://127.0.0.1:8902', 'direct', true, 0.0100, true, 1.000, NULL, NULL, '2026-06-24 09:53:59.914573+00', '2026-06-24 09:53:59.914573+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (14, 'default', 'minimax', 'MiniMax', 'minimax', false, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.minimaxi.com/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-11 16:23:17.508435+00', '2026-06-11 16:23:17.508435+00', true, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (847, 'default', 'glm-5.2-oneday', 'glm-5.2一天用', NULL, true, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.supxh.xin/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-21 07:50:11.21947+00', '2026-06-21 14:54:37.665197+00', true, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (581, 'default', 'glm-xianyu', 'glm-xianyu', NULL, true, NULL, '[]', 'cloud', 'official', 'openai-completions', 'https://api.tokenhub.market/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-19 18:57:15.425102+00', '2026-06-19 18:57:15.425102+00', true, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (67, 'default', 'minimax-anthropic', 'MiniMax (Anthropic)', 'minimax', false, NULL, '[]', 'cloud', 'official', 'anthropic-messages', 'https://api.minimaxi.com/anthropic', 'direct', true, 1.0000, false, 1.000, NULL, 'auto-created for anthropic passthrough (2026-06-12)', '2026-06-12 14:34:13.538285+00', '2026-06-22 18:05:47.4362+00', true, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.providers VALUES (2451, 'default', 'apigpt', 'apigpt', NULL, true, NULL, '[]', 'cloud', 'third_party_relay', 'openai-completions', 'https://apiclaude.cc/v1', 'direct', true, 1.0000, true, 1.000, NULL, NULL, '2026-06-24 10:56:08.632177+00', '2026-06-24 11:00:04.429059+00', false, 'off') ON CONFLICT DO NOTHING;
INSERT INTO public.routing_policy VALUES (1, 'default', '{"price": 0.20, "speed": 0.15, "credibility": 0.25, "discount_bonus": 0.05, "domestic_bonus": 0.10, "remaining_quota": 0.15, "concurrency_used": 0.10}', 1800, 0.000, 'v14 default; tune via admin UI', '2026-06-11 16:22:39.910792+00', 2, 1, 4, 1.00, 1.50, 200, 300, 5, 1800, '{claude-opus-4-8,claude-sonnet-4-6,deepseek-v4-pro,doubao-1-5-pro-32k,gemini-3-flash-preview,gemini-3.5-flash,glm-4-7,glm-5.1,glm-5.2,gpt-5.4,gpt-5.4-pro,gpt-5.5,mimo-v2.5,mimo-v2.5-pro,minimax-2.7,minimax-m2.5,minimax-m2.7,minimax-m2.7-highspeed,minimax-m3}', 2, 10, 60, '{"price": 10, "session_load": 5, "failure_penalty": 20, "default_price_cny": 5.0, "default_price_usd": 5.0}') ON CONFLICT DO NOTHING;
INSERT INTO public.schema_migrations VALUES ('100', 'credential_state_machine', '2026-06-11 16:22:52.998711+00') ON CONFLICT DO NOTHING;
INSERT INTO public.schema_migrations VALUES ('230', 'transient_fail_threshold', '2026-06-11 16:41:02.120338+00') ON CONFLICT DO NOTHING;
INSERT INTO public.schema_migrations VALUES ('231', 'model_offer_unavailable_reason', '2026-06-11 16:41:03.999395+00') ON CONFLICT DO NOTHING;
INSERT INTO public.schema_migrations VALUES ('290', 'api_key_remark', '2026-06-11 16:41:10.192269+00') ON CONFLICT DO NOTHING;
INSERT INTO public.schema_migrations VALUES ('025', 'Tool registry enhancements: version mgmt, usage stats, permissions', '2026-06-20 19:29:04.263399+00') ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('handoff.enabled', 'true', 'bool', 'platform', 'compression', '2026-06-21 14:27:13.696276+00', NULL, 'false', '2026-06-21 14:13:03.875672+00') ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('compression.enabled', 'true', 'bool', 'platform', 'compression', '2026-06-21 14:27:20.54267+00', NULL, 'false', '2026-06-21 14:13:12.433025+00') ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('llmgw_fp_slot_enabled', 'true', 'boolean', 'platform', 'fingerprint_slots', '2026-06-23 10:07:51.651258+00', NULL, NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('llmgw_fp_slot_max_per_credential', '100', 'integer', 'platform', 'fingerprint_slots', '2026-06-23 10:07:51.651258+00', NULL, NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('llmgw_fp_slot_default_ratio', '0.25', 'number', 'platform', 'fingerprint_slots', '2026-06-23 10:07:51.651258+00', NULL, NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('llmgw_client_fingerprint_ttl_days', '30', 'integer', 'platform', 'fingerprint_slots', '2026-06-23 10:07:51.651258+00', NULL, NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.settings_kv VALUES ('llmgw_fp_slot_max_total_clients', '10000', 'integer', 'platform', 'fingerprint_slots', '2026-06-23 10:07:51.651258+00', NULL, NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.sticky_sessions VALUES ('default:4:4:default', 17, '2026-06-24 12:27:14.032598+00', '2026-06-24 12:57:14.03255+00', NULL, NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.subscription_plans VALUES (1, 'basic-monthly', 'basic', '基础版', 2900, 100000, true, 1, '2026-06-15 16:39:09.586244+00', '2026-06-15 16:39:09.586244+00') ON CONFLICT DO NOTHING;
INSERT INTO public.subscription_plans VALUES (2, 'pro-monthly', 'pro', '高级版', 9900, 500000, true, 2, '2026-06-15 16:39:09.586244+00', '2026-06-15 16:39:09.586244+00') ON CONFLICT DO NOTHING;
INSERT INTO public.subscription_plans VALUES (3, 'max-monthly', 'max', '最大版', 29900, 2000000, true, 3, '2026-06-15 16:39:09.586244+00', '2026-06-15 16:39:09.586244+00') ON CONFLICT DO NOTHING;
INSERT INTO public.system_identity_pool VALUES (1, 10000, '2026-06-23 09:30:10.290885+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('filesystem', 'File System Operations', 'Read, write, search files and directories', true, 1, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('web_search', 'Web Search & Scraping', 'Search the web, fetch URLs, extract content', true, 2, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('database', 'Database Operations', 'Query and manipulate databases (PostgreSQL, MySQL, Redis)', true, 3, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('code_execution', 'Code Execution', 'Execute code in various languages', true, 4, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('network', 'Network Operations', 'HTTP requests, websockets, SSH', true, 5, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('data_processing', 'Data Processing', 'Transform, analyze, and visualize data', true, 6, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_categories VALUES ('ai_ml', 'AI & Machine Learning', 'Run ML models, embeddings, classification', true, 7, '2026-06-20 15:34:55.371825+00', '2026-06-20 15:34:55.371825+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (1, 'filesystem', 'read_file', '{"type": "function", "function": {"name": "read_file", "parameters": {"type": "object", "required": ["path"], "properties": {"path": {"type": "string", "description": "File path to read"}}}, "description": "Read contents of a file"}}', true, 100, '2026-06-20 15:34:55.372539+00', '2026-06-20 15:34:55.372539+00', 'filesystem.read_file', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (2, 'filesystem', 'write_file', '{"type": "function", "function": {"name": "write_file", "parameters": {"type": "object", "required": ["path", "content"], "properties": {"path": {"type": "string", "description": "File path to write"}, "content": {"type": "string", "description": "Content to write"}}}, "description": "Write content to a file"}}', true, 90, '2026-06-20 15:34:55.372539+00', '2026-06-20 15:34:55.372539+00', 'filesystem.write_file', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (3, 'filesystem', 'list_directory', '{"type": "function", "function": {"name": "list_directory", "parameters": {"type": "object", "required": ["path"], "properties": {"path": {"type": "string", "description": "Directory path"}}}, "description": "List files and directories"}}', true, 80, '2026-06-20 15:34:55.372539+00', '2026-06-20 15:34:55.372539+00', 'filesystem.list_directory', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (4, 'network', 'http_get', '{"type": "function", "function": {"name": "http_get", "parameters": {"type": "object", "required": ["url"], "properties": {"url": {"type": "string"}}}, "description": "Send HTTP GET request"}}', true, 50, '2026-06-20 18:19:54.431681+00', '2026-06-20 18:19:54.431681+00', 'network.http_get', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (5, 'network', 'http_post', '{"type": "function", "function": {"name": "http_post", "parameters": {"type": "object", "required": ["url", "body"], "properties": {"url": {"type": "string"}, "body": {"type": "string"}}}, "description": "Send HTTP POST request"}}', true, 50, '2026-06-20 18:19:54.431681+00', '2026-06-20 18:19:54.431681+00', 'network.http_post', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (6, 'database', 'query', '{"type": "function", "function": {"name": "database_query", "parameters": {"type": "object", "required": ["sql"], "properties": {"sql": {"type": "string"}}}, "description": "Execute a database query"}}', true, 50, '2026-06-20 18:19:54.431681+00', '2026-06-20 18:19:54.431681+00', 'database.query', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.tool_registry VALUES (7, 'database', 'insert', '{"type": "function", "function": {"name": "database_insert", "parameters": {"type": "object", "required": ["table", "data"], "properties": {"data": {"type": "object"}, "table": {"type": "string"}}}, "description": "Insert data into database"}}', true, 50, '2026-06-20 18:19:54.431681+00', '2026-06-20 18:19:54.431681+00', 'database.insert', 'default', 1, NULL, NULL, '[]', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.topup_packages VALUES (1, 'topup-small', 'small', '加油包 · 小', 1000, 10000, true, 1, '2026-06-15 16:39:09.586244+00', '2026-06-15 16:39:09.586244+00') ON CONFLICT DO NOTHING;
INSERT INTO public.topup_packages VALUES (2, 'topup-medium', 'medium', '加油包 · 中', 5000, 55000, true, 2, '2026-06-15 16:39:09.586244+00', '2026-06-15 16:39:09.586244+00') ON CONFLICT DO NOTHING;
INSERT INTO public.topup_packages VALUES (3, 'topup-large', 'large', '加油包 · 大', 10000, 120000, true, 3, '2026-06-15 16:39:09.586244+00', '2026-06-15 16:39:09.586244+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('keywords.reasoning', '["solve", "prove", "derive", "calculate", "compute", "reason", "reasoning", "logic", "theorem", "proof", "step by step", "explain why", "analyze", "证明", "推导", "求解", "计算", "推理", "逻辑", "分析", "证明题", "推导过程", "步骤"]', 'keywords', 'default', 1.000, true, 'Reasoning keyword list', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('keywords.code', '["function", "class", "method", "algorithm", "implement", "code", "program", "script", "debug", "refactor", "compile", "syntax", "variable", "代码", "函数", "方法", "实现", "编写", "写代码", "算法", "重构", "调试", "bug", "编程"]', 'keywords', 'default', 1.000, true, 'Code keyword list', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('keywords.creative', '["write a", "write an", "compose", "draft", "story", "blog post", "essay", "poem", "creative", "translate", "summarize", "summary", "写一篇", "撰写", "创作", "故事", "小说", "诗歌", "翻译", "总结", "摘要", "文案"]', 'keywords', 'default', 1.000, true, 'Creative keyword list', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('thresholds.llm_confidence', '0.7', 'thresholds', 'default', 1.000, true, 'Heuristic confidence below this triggers LLM fallback', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('thresholds.long_context_tokens', '50000', 'thresholds', 'default', 1.000, true, 'Token count above this triggers TaskLongContext', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('weights.smart', '{"match": 25, "price": 25, "speed": 25, "pressure": 10, "stability": 20, "context_fit": 15}', 'weights', 'default', 1.000, true, 'Smart profile weights', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('weights.speed_first', '{"match": 15, "price": 10, "speed": 50, "pressure": 5, "stability": 20, "context_fit": 10}', 'weights', 'default', 1.000, true, 'Speed-first profile weights', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.tuning_params VALUES ('weights.cost_first', '{"match": 20, "price": 50, "speed": 10, "pressure": 5, "stability": 15, "context_fit": 10}', 'weights', 'default', 1.000, true, 'Cost-first profile weights', NULL, '2026-06-15 07:49:04.601572+00', '2026-06-15 07:49:04.601572+00') ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('general_chat', '通用对话', '通用', 'chat', 'smart', '{chat,general}', '{对话,聊天,问答}', NULL, true, 1, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('reasoning', '逻辑推理', '通用', 'reasoning', 'smart', '{reasoning,logic}', '{推理,逻辑,数学,证明}', NULL, true, 2, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('long_doc', '长文档处理', '通用', 'long_context', 'smart', '{long_context,document}', '{长文档,全文,摘要,PDF}', NULL, true, 3, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('code_gen', '代码生成', '研发', 'code', 'speed_first', '{code,programming}', '{代码,编程,实现,函数}', NULL, true, 4, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('code_review', '代码审查', '研发', 'code', 'smart', '{code,review}', '{审查,review,重构,bug}', NULL, true, 5, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('agent_workflow', '多步Agent', '研发', 'agent', 'smart', '{agent,workflow}', '{agent,多步,工作流,工具}', NULL, true, 6, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('fn_call', '函数调用', '研发', 'function_call', 'speed_first', '{function_call,tools}', '{function,tool,调用,API}', NULL, true, 7, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('copywriting', '文案创作', '营销', 'creative', 'smart', '{creative,copy}', '{文案,标题,广告语,营销}', NULL, true, 8, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('social_post', '社媒发帖', '营销', 'creative', 'speed_first', '{social,post}', '{发帖,微博,小红书,朋友圈}', NULL, true, 9, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('video_script', '短视频脚本', '营销', 'creative', 'smart', '{video,script}', '{脚本,短视频,分镜,口播}', NULL, true, 10, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('brand_strategy', '品牌策略', '营销', 'reasoning', 'smart', '{brand,strategy}', '{品牌,策略,定位,竞品}', NULL, true, 11, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('web_scrape', '网页采集', '采集', 'agent', 'cost_first', '{scrape,crawl}', '{采集,爬虫,抓取,网页}', NULL, true, 12, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('social_monitor', '自媒体监测', '采集', 'agent', 'cost_first', '{monitor,social}', '{监测,舆情,评论,热搜}', NULL, true, 13, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('short_video_collect', '短视频采集', '采集', 'agent', 'cost_first', '{video,collect}', '{短视频,下载,采集,抖音}', NULL, true, 14, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('news_digest', '资讯摘要', '采集', 'creative', 'speed_first', '{news,digest}', '{资讯,新闻,摘要,日报}', NULL, true, 15, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('competitor_intel', '竞品情报', '采集', 'reasoning', 'smart', '{competitor,intel}', '{竞品,情报,对比,市场}', NULL, true, 16, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('image_understand', '图像理解', '多媒体', 'vision', 'smart', '{vision,image}', '{图像,识图,OCR,视觉}', NULL, true, 17, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('image_gen_prompt', '生图Prompt', '多媒体', 'creative', 'smart', '{image,prompt}', '{生图,prompt,Stable,Midjourney}', NULL, true, 18, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('crm_followup', 'CRM跟进', '企业', 'chat', 'smart', '{crm,followup}', '{CRM,跟进,客户,销售}', NULL, true, 19, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('doc_translate', '文档翻译', '企业', 'creative', 'cost_first', '{translate,document}', '{翻译,文档,双语,本地化}', NULL, true, 20, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('meeting_summary', '会议纪要', '企业', 'creative', 'speed_first', '{meeting,summary}', '{会议,纪要,总结,行动项}', NULL, true, 21, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('compliance_audit', '合规审计', '企业', 'reasoning', 'smart', '{compliance,audit}', '{合规,审计,风控,政策}', NULL, true, 22, NULL, '2026-06-14 19:11:16.200703+00', NULL) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('session_title', '会话标题生成', '企业', 'creative', 'cost_first', '{session,title,admin,gateway}', '{标题,会话,总结,主题}', NULL, true, 23, NULL, '2026-06-18 18:23:51.006312+00', '你是会话标题生成助手。根据下方完整多轮会话日志，用中文生成一个简短准确的标题（不超过18字），概括用户目标与会话结果。只输出标题纯文本：不要引号、编号、解释、XML/HTML 标签、thinking/redacted 标记或英文占位符。') ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_config VALUES ('session_summary', '会话日志总结', '企业', 'creative', 'cost_first', '{session,summary,admin,gateway}', '{总结,摘要,会话,日志}', NULL, true, 24, NULL, '2026-06-18 18:23:51.006312+00', '你是会话日志分析助手。请严格输出 JSON，格式如下：
{"summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"]}
要求：
- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何
- key_points 提取 3-5 个关键事实或决策点，每条 15-40 字
- 不要输出 JSON 以外的任何文本
- 如果语料中包含错误信息，务必在总结中提及') ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (1, 'session_title', 'minimax-m2.7', 1.00, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (2, 'session_title', 'glm-5.1', 0.95, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (3, 'session_title', 'minimax-m3', 0.90, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (4, 'session_title', 'deepseek-chat', 0.85, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (5, 'session_summary', 'minimax-m2.7', 1.00, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (6, 'session_summary', 'glm-5.1', 0.95, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (7, 'session_summary', 'minimax-m3', 0.90, 0.0000, true) ON CONFLICT DO NOTHING;
INSERT INTO public.work_type_model_route VALUES (8, 'session_summary', 'deepseek-chat', 0.85, 0.0000, true) ON CONFLICT DO NOTHING;
