# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

---

## 2026-08-18T18:59:33Z — deploy 154 build_seq 1622 (884ee9e1)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 540 | `540_stats_event_inbox_consumer.sql` | `7241ab1a5e458e883d04031d97890049115f2e42fa5232fff94d48af8d7cfe0a` | applied+verified |

## 2026-08-19T07:12:55Z — deploy 245 build_seq 1626 (cfe53228)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 542 | `542_request_logs_token_band.sql` | `318c7f342afed326cf6f44fbe092b11c06d7a24acbc5cf902bd6ab53fb032ab3` | applied+verified |

## 2026-08-19T10:11:23Z — deploy 154 build_seq 1629 (b310b700)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 543 | `543_request_logs_discard_events.sql` | `6fe37eb4f0dd5cc75f400376473471b5f2b6f5f8edcdf2d92a1f71d291b18fa8` | applied+verified |

## 2026-08-19T10:11:23Z — deploy 154 build_seq 1629 (b310b700)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 544 | `544_stats_adjustments_alignment.sql` | `9c06b5ac0ee9e8ad04c9517963399518ff9118b14f3619a67475641e8058ea1e` | applied+verified |

## 2026-08-20T02:37:33Z — deploy 154 build_seq 1631 (manual fix)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 545 | `545_stats_reconciliation_phantom_resolution.sql` | `6f2cbdefb62fefa17ea5d9d5238b5f2062720fd636a97d60ebda9a862579a993` | applied+verified |
| 546 | `546_stats_reconciliation_diffs_unique.sql` | `57a05c88937b` | applied+verified |
## 2026-08-19T18:39:56Z — deploy 154 build_seq 1632 (0eec1b62)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 547 | `547_session_project_attribution.sql` | `359353c4155d1c4331ef56356d1cae8e408b7ece01c9e1ef95b8adb9dbda16a2` | applied+verified |

## 2026-08-20T07:56:44Z — deploy 245 build_seq 1639 (0185b5d8)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 548 | `548_stats_reconciliation_diffs_identity.sql` | `163003022c8b81c61a4f9da270d6a479e641a90ea683aa16fb9225e7be350b9a` | applied+verified |

## 2026-08-21T14:51:52Z — deploy 245 build_seq 1648 (2321deb9)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 550 | `550_session_title_states_expand.sql` | `c9cfbf7af53c646d79320cab6c1d03f2378b364723c1106ca1373a1b47a2ff29` | applied+verified |
| 551 | `551_session_title_states_indexes.sql` | `d51776ad17568c3704b68b00482ece5f3fb975b7669a4122090b0d197815db28` | applied+verified |

## 2026-08-21T15:35:01Z — deploy 245 build_seq 1650 (d351cf0a)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 554 | `554_goal_runs.sql` | `fdf2a5a4900fbae3559fbb86cfe21d8e1fbbfa18f56d205c73fcd1b31073ca21` | applied+verified |

## 2026-08-21T17:41:16Z — deploy 154 build_seq 1654 (cbca3593)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 555 | `555_goal_run_actions_lease_fencing.sql` | `1e2af70f5b0ee66c58ab54b7515e359e140b6425cd76488086982f289b10d6fe` | applied+verified |

## 2026-08-22T13:39:45Z — deploy 154 build_seq 1671 (c9ceea30)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 561 | `561_request_logs_view_origin_actor.sql` | `ef139bf1a895c53e93c1c495d1891d2bbb64b4d4e953ee3c93f920750babd36a` | applied+verified |


## 2026-08-23T23:30:00Z — deploy 154 build_seq 1693 (de6c5994)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 569 | `569_candidate_binding_scope_revision_canonical.sql` | `6642ef23b19faeb2c5be376093728cbb4282a938bc92314df1e5297a89565c40` | applied+verified |
## 2026-08-26T23:56:40Z — deploy 154 build_seq 1764 (4c4b1441)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 607 | `607_repair_dashboard_access_events_promote_columns.sql` | `f75487f3c16cbec71c87a642d0d00932afc419496640e413e1b64076108f557b` | applied+verified |
| 608 | `608_tenant_model_policies_add_pkey.sql` | `e0e25bed2972bb3555d229739f2250820a06ecfc6796b7ba5e0328cff48425e5` | applied+verified |
| 609 | `609_tenant_model_policies_audit_rekey_pkey.sql` | `1d9eda29db77ab80e04db1a7325c9ffb8665002415220190f1a60f0156bd31f8` | applied+verified |
## 2026-08-27T06:10:35Z — deploy 154 build_seq 1765 (1aff393a)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 610 | `610_request_class_due_at.sql` | `379ae7f22dcf656de3672ae20b6df33d73b1a6ee42aba4cc56d2a28d78c06ba0` | applied+verified |
## 2026-08-28T23:54:59Z — deploy 154 build_seq 1801 (2ec1b325)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 612 | `612_native_responses_capability.sql` | `1bb7769b9de61c3432cc95bd6a86c18a9e58556c6af1d8a5b40b5953a5317dcc` | applied+verified |
| 613 | `613_native_responses_stream_capability.sql` | `fbf7c9f55108fde7659e31730b2d9f9c4d5fb0ba3b044336a5fc479c8d0195d1` | applied+verified |

## 2026-08-29T08:48:10Z — deploy 245 build_seq 1805 (64b9b19c)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 614 | `614_session_bodies_hot.sql` | `c79502c50906b221db76d7b545074901c56a086277bc2927c66383ca72580d25` | applied+verified |
| 615 | `615_session_bodies_hot_promote_function.sql` | `716520628861fda58b31af00db20a6acd115cd954ddafca61812b6da1456aa5d` | applied+verified |
| 616 | `616_provider_error_details_unique_constraint.sql` | `aafd7ecfcf3e916d18b575d1a8c390d3be922720658be9597daecf33aad306d5` | applied+verified |
| 617 | `617_candidate_failure_logs_hot_contract.sql` | `b0bd2d72250614d9c1f17d6926c3ee1bbb0ec649c42d8b35d45993d981759d53` | applied+verified |
| 618 | `618_request_journey_snapshot_receipts.sql` | `6d4d9f44cd931641363e61c2c72a50c90b606830645e5ea375dae62dc26f21fb` | applied+verified |
| 619 | `619_session_turns_unified_view.sql` | `3da375593e6d1abddb70205a69fdd674127f90d86ee42f6b5cb3cde5adf9c835` | applied+verified |

## 2026-08-29T14:14:39Z — deploy 154 build_seq 1806 (5e94de42)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 620 | `620_provider_error_details_tenant_scope.sql` | `0d8da54b0af09d656be88b962d9a9f9ba3ade1dde17f9655c46471dae570826d` | applied+verified |
| 621 | `621_provider_error_details_cleanup_index.sql` | `df5873c19a7b4baefc06f28ef04c8e24b354cec3bbd7af5b153fa7c2b41daefe` | applied+verified |
| 622 | `622_provider_error_aggregator_state.sql` | `4fe5e0fa0cb73ccf69cc292e499be28a493a8b9fe447acb2d5e8e1501af1db3d` | applied+verified |

## 2026-08-29T17:14:05Z — deploy 245 build_seq 1811 (c50f0376)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 623 | `623_journal_snapshot_receipts_projection_base.sql` | `f37fd429f1d0c5ebba03821e2df743f863e06e08809fefa7fcc077bfd5f4ec27` | applied+verified |

## 2026-08-29T19:07:20Z — deploy 245 build_seq 1818 (e10c8425)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 624 | `624_candidate_failure_logs_promote_atomic_v2.sql` | `701601cbb4597af85f7b43ea3398beac0fe9e215be6be21c4385b5843ce27738` | applied+verified |
| 625 | `625_session_bodies_unified_explicit.sql` | `5058cf367b9742d5cf5ddb9757e3879382743f0149f380ebfca8692900634a48` | applied+verified |

## 2026-08-31T00:00:00Z — deploy 154 build_seq 1819 (manual fix)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 626 | `626_session_bodies_hot_promote_reconcile.sql` | `9139b773b2f18cc7d1113f6c363b0afeba1db3f3229de9c3d8447a32712689a3` | applied+verified |

