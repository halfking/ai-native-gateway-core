# ★ SQL 变更登记册 · 权威状态（机器可判）

> ★★ 本文件是 `docs/db-changelog.md`（★ **唯一登记册**）的**机器可判镜像**：
> ★★ 一行一个迁移号 × 三格（★ `local` / `252` / `211`）⇒ 每格 `applied@<账本 applied_at>` 或 `—`。
> ★★ **为什么独立成文件**：★ ★★ 登记册正本里有「同一迁移号**必须恰好出现一次**」的连续性门
> （★ `TestSection14335RegistrationPoints` / `TestMigration895RegisteredInEveryDeliverySurface`）
> ⇒ ★★ ⇒ **把第二张表插进正本 ⇒ 直接破坏那些门**（★ ★★ 实测：★ 892 那行立刻变成「出现 2 次」）
> ⇒ ★★ ⇒ 权威表**必须**住在正本之外。
> ★★ **真值**：★ 各环境自己的 `gateway_db_revision_sequences`（★ 本文件是**采集快照的投影** ⇒ ★ 会过期）。
> ★★ 生成器：`scripts/audit/db-changelog-ledger-reconcile.py render --write`（★ 只读）。
> ★★ 判读：`applied@…` = 该环境账本里已记账；★ `—` = 该环境账本里**没有**（★ 注意：★ **账本里没有 ≠ 没应用过** ⇒ ★ 旁路应用不写这个账本）。

<!-- R160-AUTHORITATIVE:BEGIN -->

### ★ 权威状态（机器可判 · R160 轮生成）

★ 采集时刻 `2026-10-25T23:46:09+08:00` · ★ 环境 `211,252,local` · ★ 行数 `590`
★ 账本集合指纹 `44f99c1d035f3a6c1d30f103899909a451d5c3b7d67219807d746b6635d5e689`

★ ★ **判读法**：★ 每格 = `applied@<账本 applied_at>` ⇒ 该环境**已应用**；★ `—` ⇒ **该环境账本里没有**（★ 注意：★ **账本里没有 ≠ 没应用过** ⇒ ★ 旁路应用（★ 手工 psql / 另一条序列）不会进账本）。

| 迁移 | 文件 | 211 | 252 | local |
|---|---|---|---|---|
| 032 | `032_session_tenant_binding.sql` | `applied@2026-10-17 00:34:18.146866+08` | — | `applied@2026-10-20 21:58:52.594521+08` |
| 044 | `044_health_source_probe_now.sql` | `applied@2026-10-17 00:34:32.798022+08` | — | `applied@2026-10-20 22:00:52.285805+08` |
| 351 | `351_session_analytics_tables.sql` | `applied@2026-10-17 00:34:14.911897+08` | — | `applied@2026-10-20 21:58:43.647775+08` |
| 357 | `357_session_analytics_aggregation_views.sql` | `applied@2026-10-12 01:27:02.501685+08` | — | `applied@2026-10-20 21:58:49.277718+08` |
| 358 | `358_session_ownership.sql` | `applied@2026-10-12 01:27:02.57128+08` | — | `applied@2026-10-20 21:58:49.765295+08` |
| 361 | `361b_provider_models_source_prelude.sql` | `applied@2026-10-11 01:42:42.011018+08` | — | `applied@2026-10-20 22:00:25.165972+08` |
| 375 | `375_offline_requests_status.sql` | `applied@2026-10-21 14:30:11.138561+08` | — | `applied@2026-10-20 22:00:25.655205+08` |
| 376 | `376_offline_activation_code.sql` | `applied@2026-10-21 14:30:11.474617+08` | — | `applied@2026-10-20 22:00:26.056243+08` |
| 387 | `387_session_analysis_inference.sql` | `applied@2026-10-21 14:30:11.833175+08` | — | `applied@2026-10-20 22:00:27.255589+08` |
| 392 | `392_candidate_failure_logs_monthly_partition.sql` | `applied@2026-10-11 01:39:58.31966+08` | — | `applied@2026-10-20 21:58:51.724529+08` |
| 394 | `394_request_stats_minute.sql` | `applied@2026-10-12 01:27:02.598479+08` | — | `applied@2026-10-20 21:58:54.669046+08` |
| 407 | `407_request_context_attrs.sql` | `applied@2026-10-11 01:33:57.778609+08` | — | `applied@2026-10-20 22:00:00.02738+08` |
| 427 | `427_task_type_centroids_model.sql` | `applied@2026-10-21 14:30:12.171656+08` | — | `applied@2026-10-20 22:00:28.659724+08` |
| 456 | `456_session_v2_display_columns.sql` | `applied@2026-10-17 00:34:14.54723+08` | — | `applied@2026-10-20 21:58:43.245106+08` |
| 470 | `470_cache_metrics.sql` | `applied@2026-10-17 00:34:16.326777+08` | — | `applied@2026-10-20 21:58:45.165989+08` |
| 472 | `472_cache_metrics_partitions.sql` | `applied@2026-10-17 00:34:16.674751+08` | — | `applied@2026-10-20 21:58:45.552811+08` |
| 478 | `478_auto_route_affinity.sql` | `applied@2026-10-11 00:08:32.222285+08` | — | `applied@2026-10-20 21:59:00.392347+08` |
| 484 | `484_request_logs_hot_add_status_code.sql` | `applied@2026-10-12 01:29:38.865024+08` | — | `applied@2026-10-20 21:59:21.27395+08` |
| 485 | `485_request_logs_add_raw_model_name.sql` | `applied@2026-10-11 00:12:59.754031+08` | — | `applied@2026-10-20 21:59:25.305232+08` |
| 486 | `486_credential_model_bindings_add_probe_revert_at.sql` | `applied@2026-10-12 01:29:38.885494+08` | — | `applied@2026-10-20 22:00:39.409438+08` |
| 487 | `487_request_logs_add_system_fingerprint.sql` | `applied@2026-10-11 00:12:03.106617+08` | — | `applied@2026-10-20 21:59:25.787257+08` |
| 487 | `487b_request_logs_hot_system_fingerprint.sql` | `applied@2026-10-11 01:16:46.603021+08` | — | `applied@2026-10-20 21:59:26.207642+08` |
| 487 | `487c_request_logs_hot_raw_model_name.sql` | `applied@2026-10-11 01:16:46.627572+08` | — | `applied@2026-10-20 21:59:26.611531+08` |
| 489 | `489_credential_probe_queue_startup_backfill.sql` | `applied@2026-10-12 01:29:38.92232+08` | — | `applied@2026-10-20 21:59:21.748201+08` |
| 490 | `490_credential_probe_queue_runtime_columns.sql` | `applied@2026-10-12 01:29:38.945931+08` | — | `applied@2026-10-20 21:59:22.774625+08` |
| 491 | `491_request_logs_queue_timestamps.sql` | `applied@2026-10-11 00:17:40.686609+08` | — | `applied@2026-10-20 21:59:35.173778+08` |
| 510 | `510_request_type.sql` | `applied@2026-10-11 01:12:33.415859+08` | — | `applied@2026-10-20 21:59:46.268835+08` |
| 513 | `513_schema_unification_and_session_turns_dual_write.sql` | `applied@2026-10-11 01:13:04.039431+08` | — | `applied@2026-10-20 21:59:47.13696+08` |
| 516 | `516_durable_llm_tasks.sql` | `applied@2026-10-11 01:11:31.875626+08` | — | `applied@2026-10-20 21:59:40.911076+08` |
| 517 | `517_handoff_pending_confirmations.sql` | `applied@2026-10-11 14:36:21.161198+08` | — | `applied@2026-10-20 21:59:41.335612+08` |
| 532 | `532_request_logs_final_success.sql` | `applied@2026-10-11 01:21:24.415793+08` | — | `applied@2026-10-20 21:59:19.997753+08` |
| 534 | `534_handoff_logs_hot_columnar.sql` | `applied@2026-10-11 00:17:40.767543+08` | — | `applied@2026-10-20 21:59:36.018729+08` |
| 536 | `536_stats_analytics_foundation.sql` | `applied@2026-10-11 01:33:06.838391+08` | — | `applied@2026-10-20 21:59:59.138415+08` |
| 537 | `537_usage_facts.sql` | `applied@2026-10-11 01:31:21.329718+08` | — | `applied@2026-10-20 21:59:53.535476+08` |
| 538 | `538_node_probe_runs_trigger_kind_unified_queue.sql` | `applied@2026-10-12 01:29:38.970503+08` | — | `applied@2026-10-20 21:59:24.293299+08` |
| 540 | `540_stats_event_inbox_consumer.sql` | `applied@2026-10-11 01:33:27.305888+08` | — | `applied@2026-10-20 21:59:59.569703+08` |
| 541 | `541_candidate_binding_scope_revision.sql` | `applied@2026-10-12 02:04:16.307095+08` | — | `applied@2026-10-20 21:58:55.09428+08` |
| 542 | `542_request_logs_token_band.sql` | `applied@2026-10-11 01:37:07.202558+08` | — | `applied@2026-10-20 22:00:12.855708+08` |
| 543 | `543_request_logs_discard_events.sql` | `applied@2026-10-12 01:46:30.624732+08` | — | `applied@2026-10-20 21:58:56.804826+08` |
| 547 | `547_session_project_attribution.sql` | `applied@2026-10-11 14:36:21.222094+08` | — | `applied@2026-10-20 21:59:41.902984+08` |
| 560 | `560_session_summaries_tenant_uniqueness.sql` | `applied@2026-10-11 00:08:32.045019+08` | `applied@2026-09-06 10:57:03.147523+08` | `applied@2026-10-20 21:58:46.691489+08` |
| 563 | `563_session_summary_trigger_on_hot.sql` | `applied@2026-10-11 00:08:32.111354+08` | `applied@2026-09-06 10:57:05.871662+08` | `applied@2026-10-20 21:58:47.790022+08` |
| 564 | `564_session_summary_backfill_safe.sql` | `applied@2026-10-11 00:08:32.133013+08` | `applied@2026-09-06 10:57:05.99851+08` | `applied@2026-10-20 21:58:48.284839+08` |
| 567 | `567_session_analysis_metadata.sql` | `applied@2026-10-17 00:34:15.982899+08` | — | `applied@2026-10-20 21:58:44.785314+08` |
| 569 | `569_candidate_binding_scope_revision_canonical.sql` | `applied@2026-10-12 02:04:16.344032+08` | — | `applied@2026-10-20 21:58:55.51633+08` |
| 571 | `571_candidate_binding_scope_revision_canonical_priority_hash.sql` | `applied@2026-10-12 02:04:16.367726+08` | — | `applied@2026-10-20 21:58:55.944356+08` |
| 572 | `572_session_summary_large_token_ratio.sql` | `applied@2026-10-11 00:08:32.065373+08` | `applied@2026-09-06 10:57:03.255888+08` | `applied@2026-10-20 21:58:47.050174+08` |
| 578 | `578_candidate_binding_scope_filter_disabled.sql` | `applied@2026-10-12 02:04:16.404745+08` | — | `applied@2026-10-20 21:58:56.354997+08` |
| 606 | `606_session_summaries_agent_expert_tags.sql` | `applied@2026-10-11 00:08:32.086923+08` | `applied@2026-09-06 10:57:03.335754+08` | `applied@2026-10-20 21:58:47.408579+08` |
| 610 | `610_request_class_due_at.sql` | `applied@2026-10-11 01:16:46.446268+08` | — | `applied@2026-10-20 21:59:13.995753+08` |
| 612 | `612_native_responses_capability.sql` | `applied@2026-10-11 01:33:58.231122+08` | — | `applied@2026-10-20 22:00:07.394128+08` |
| 617 | `617_candidate_failure_logs_hot_contract.sql` | `applied@2026-10-12 01:29:38.355855+08` | — | `applied@2026-10-20 21:58:57.305126+08` |
| 620 | `620_provider_error_details_tenant_scope.sql` | `applied@2026-10-11 00:08:46.059154+08` | — | `applied@2026-10-20 21:59:05.141598+08` |
| 622 | `622_provider_error_aggregator_state.sql` | `applied@2026-10-12 01:27:02.626468+08` | — | `applied@2026-10-20 21:58:57.90967+08` |
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `applied@2026-10-12 01:29:38.401063+08` | — | `applied@2026-10-20 21:58:58.724519+08` |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `applied@2026-10-12 01:29:38.422082+08` | — | `applied@2026-10-20 21:58:59.113247+08` |
| 629 | `629_audit_attachments_cleanup.sql` | `applied@2026-10-17 00:34:15.262759+08` | — | `applied@2026-10-20 21:58:44.015962+08` |
| 630 | `630_session_aggregate_outbox.sql` | `applied@2026-10-11 00:17:58.942135+08` | — | `applied@2026-10-20 21:59:38.691136+08` |
| 631 | `631_provider_credential_soft_delete.sql` | `applied@2026-10-11 01:32:07.899459+08` | — | `applied@2026-10-20 21:59:55.734286+08` |
| 632 | `632_audit_attachments_filesystem_cleanup.sql` | `applied@2026-10-17 00:34:15.623342+08` | — | `applied@2026-10-20 21:58:44.396385+08` |
| 639 | `639_provider_error_details_credential.sql` | `applied@2026-10-11 00:08:46.265741+08` | — | `applied@2026-10-20 21:59:05.553363+08` |
| 640 | `640_session_turns_protocol_fields.sql` | `applied@2026-10-11 00:17:21.186615+08` | — | `applied@2026-10-20 21:59:33.761645+08` |
| 644 | `644_tuning_views_selfcheck_and_candidate_failure_cache.sql` | `applied@2026-10-11 00:08:32.168118+08` | `applied@2026-09-06 10:57:09.267409+08` | `applied@2026-10-20 21:58:59.545374+08` |
| 645 | `645_session_bodies_hot_request_unique_repair.sql` | `applied@2026-10-11 00:08:32.189057+08` | `applied@2026-09-06 10:57:09.371415+08` | `applied@2026-10-20 21:58:59.936101+08` |
| 650 | `650_auto_route_selection_treatment_attribution.sql` | `applied@2026-10-11 00:08:32.245126+08` | `applied@2026-09-06 10:57:09.458403+08` | `applied@2026-10-20 21:59:00.803731+08` |
| 651 | `651_provider_quality_hot_rollup.sql` | `applied@2026-10-11 00:08:32.359167+08` | `applied@2026-09-06 10:58:17.251409+08` | `applied@2026-10-20 21:59:02.577078+08` |
| 652 | `652_system_monitor_fallback_queue.sql` | `applied@2026-10-11 00:08:32.380152+08` | `applied@2026-09-06 10:58:17.331483+08` | `applied@2026-10-20 21:59:03.019142+08` |
| 653 | `653_archive_credential_model_index_canonical_return.sql` | `applied@2026-10-11 00:08:32.400819+08` | `applied@2026-09-06 10:58:17.420323+08` | `applied@2026-10-20 21:59:03.445645+08` |
| 654 | `654_archive_credential_model_index_detach_drop.sql` | `applied@2026-10-11 00:08:32.422034+08` | `applied@2026-09-06 10:58:17.507526+08` | `applied@2026-10-20 21:59:03.864944+08` |
| 655 | `655_session_summaries_schema_reconcile.sql` | `applied@2026-10-11 00:08:32.022839+08` | `applied@2026-09-06 10:56:50.278586+08` | `applied@2026-10-20 21:58:46.328173+08` |
| 656 | `656_auto_route_selections_hot.sql` | `applied@2026-10-11 00:08:32.27172+08` | `applied@2026-09-06 10:58:17.055887+08` | `applied@2026-10-20 21:59:01.244142+08` |
| 658 | `658_auto_route_structured_features.sql` | `applied@2026-10-11 00:08:32.314211+08` | `applied@2026-09-12 05:32:49.674121+08` | `applied@2026-10-20 21:59:01.718329+08` |
| 659 | `659_legacy_promote_atomic_cte.sql` | `applied@2026-10-11 00:08:32.338421+08` | `applied@2026-09-06 10:58:17.161428+08` | `applied@2026-10-20 21:59:02.147235+08` |
| 660 | `660_credential_model_weekly_peak_unique.sql` | `applied@2026-10-11 00:08:32.443076+08` | `applied@2026-09-06 10:58:17.589534+08` | `applied@2026-10-20 21:59:04.311576+08` |
| 661 | `661_session_summary_token_ratio_reassert.sql` | `applied@2026-10-11 00:08:32.464398+08` | `applied@2026-09-06 10:58:17.678948+08` | `applied@2026-10-20 21:59:04.728576+08` |
| 662 | `662_feature_distribution_stats.sql` | `applied@2026-10-11 00:08:46.504816+08` | `applied@2026-09-06 10:58:17.816911+08` | `applied@2026-10-20 21:59:05.991373+08` |
| 663 | `663_training_export.sql` | `applied@2026-10-11 00:08:46.539842+08` | `applied@2026-09-12 05:32:49.900691+08` | `applied@2026-10-20 21:59:06.42108+08` |
| 664 | `664_provider_error_details_agg_key_dedup.sql` | `applied@2026-10-11 00:08:46.603801+08` | `applied@2026-09-12 05:32:50.962108+08` | `applied@2026-10-20 21:59:06.84155+08` |
| 666 | `666_orchestration_and_stats_tables.sql` | `applied@2026-10-11 00:08:46.792988+08` | `applied@2026-09-12 05:32:51.054962+08` | `applied@2026-10-20 21:59:07.293829+08` |
| 667 | `667_llm_hourly_stats_timestamp_fix.sql` | `applied@2026-10-11 00:08:47.077054+08` | `applied@2026-09-12 05:32:51.13927+08` | `applied@2026-10-20 21:59:07.716579+08` |
| 668 | `668_llm_hourly_stats_final_fix.sql` | `applied@2026-10-11 00:08:47.421713+08` | `applied@2026-09-12 05:32:51.218471+08` | `applied@2026-10-20 21:59:08.132683+08` |
| 669 | `669_training_human_annotations.sql` | `applied@2026-10-11 00:08:48.130591+08` | `applied@2026-09-12 05:32:51.654718+08` | `applied@2026-10-20 21:59:09.436491+08` |
| 670 | `670_routing_optimization.sql` | `applied@2026-10-11 00:08:48.308757+08` | `applied@2026-09-12 05:32:51.762207+08` | `applied@2026-10-20 21:59:10.031433+08` |
| 671 | `671_local_provider_catalog.sql` | `applied@2026-10-11 00:08:48.385798+08` | `applied@2026-09-12 05:32:51.833635+08` | `applied@2026-10-20 21:59:10.542627+08` |
| 672 | `672_local_first_title_summary_routing.sql` | `applied@2026-10-11 00:08:48.513658+08` | `applied@2026-09-12 05:32:51.896047+08` | `applied@2026-10-20 21:59:10.962205+08` |
| 673 | `673_annotation_stats_empty_table_fix.sql` | `applied@2026-10-11 00:08:48.624322+08` | `applied@2026-09-12 05:32:51.951468+08` | `applied@2026-10-20 21:59:11.381548+08` |
| 674 | `674_annotation_request_id_unique.sql` | `applied@2026-10-11 00:08:48.793011+08` | `applied@2026-09-12 05:32:52.013391+08` | `applied@2026-10-20 21:59:11.816843+08` |
| 675 | `675_qwen38_family_vendor.sql` | `applied@2026-10-11 00:08:48.91136+08` | `applied@2026-09-12 05:32:52.064347+08` | `applied@2026-10-20 21:59:12.260478+08` |
| 676 | `676_routing_opt_active_fix.sql` | `applied@2026-10-11 00:08:48.972708+08` | `applied@2026-09-12 05:32:52.126191+08` | `applied@2026-10-20 21:59:12.685054+08` |
| 677 | `677_session_summaries_canonical_bootstrap.sql` | `applied@2026-10-11 00:08:31.985824+08` | `applied@2026-09-12 05:31:24.180301+08` | `applied@2026-10-20 21:58:45.931533+08` |
| 678 | `678_request_logs_bodies_hot_unique_repair_and_model_offers_columns.sql` | `applied@2026-10-11 00:08:48.081423+08` | `applied@2026-09-12 05:32:51.531471+08` | `applied@2026-10-20 21:59:08.998711+08` |
| 679 | `679_local_credential_unique.sql` | `applied@2026-10-11 00:08:49.024435+08` | `applied@2026-09-12 05:32:52.24581+08` | `applied@2026-10-20 21:59:13.559492+08` |
| 680 | `680_request_logs_current_month_view_bootstrap.sql` | `applied@2026-10-11 01:16:46.472626+08` | `applied@2026-09-12 05:32:52.299396+08` | `applied@2026-10-20 21:59:14.518283+08` |
| 680 | `680b_request_logs_helper_view_column_resync.sql` | `applied@2026-10-11 01:23:57.894498+08` | — | `applied@2026-10-20 21:59:47.57221+08` |
| 681 | `681_provider_error_details_fingerprint_restore_8part.sql` | `applied@2026-10-11 00:08:49.001056+08` | `applied@2026-09-12 05:32:52.181208+08` | `applied@2026-10-20 21:59:13.127675+08` |
| 682 | `682_model_offers_context_window_columns.sql` | `applied@2026-10-11 00:09:56.7993+08` | `applied@2026-09-12 05:32:52.387886+08` | `applied@2026-10-20 21:59:16.143934+08` |
| 682 | `682b_model_offers_context_window_columns_prelude.sql` | `applied@2026-10-11 00:09:56.775217+08` | — | `applied@2026-10-20 21:59:15.437426+08` |
| 683 | `683_session_dim_ownership_columns.sql` | `applied@2026-10-11 00:10:46.209566+08` | `applied@2026-09-12 05:32:52.454718+08` | `applied@2026-10-20 21:59:16.677623+08` |
| 683 | `683b_session_dim_table_prelude.sql` | `applied@2026-10-11 00:10:46.18397+08` | — | `applied@2026-10-20 21:58:48.729202+08` |
| 684 | `684_drop_stale_provider_error_tenant_fingerprint.sql` | `applied@2026-10-11 00:10:46.231061+08` | `applied@2026-09-12 05:32:52.53005+08` | `applied@2026-10-20 21:59:17.408612+08` |
| 685 | `685_task_default_routing_tenant_text.sql` | `applied@2026-10-11 00:10:46.338748+08` | `applied@2026-09-12 05:32:52.62676+08` | `applied@2026-10-20 21:59:18.03447+08` |
| 686 | `686_fix_session_module_executions_2026_10_bounds.sql` | `applied@2026-10-11 00:10:46.382625+08` | `applied@2026-09-12 05:32:52.681518+08` | `applied@2026-10-20 21:59:18.999916+08` |
| 693 | `693_provider_models_canonical_cleared_at.sql` | `applied@2026-10-11 00:10:46.405179+08` | `applied@2026-09-12 05:32:52.740067+08` | `applied@2026-10-20 21:59:19.522569+08` |
| 694 | `694_partition_ensure_timezone.sql` | `applied@2026-10-11 00:10:46.429927+08` | `applied@2026-09-12 05:32:52.814402+08` | `applied@2026-10-20 21:59:20.427621+08` |
| 695 | `695_request_logs_promote_final_success_self_heal.sql` | `applied@2026-10-11 00:10:46.450359+08` | `applied@2026-09-12 05:32:52.875753+08` | `applied@2026-10-20 21:59:20.857334+08` |
| 696 | `696_request_logs_view_system_fingerprint.sql` | `applied@2026-10-11 01:16:46.650523+08` | `applied@2026-09-12 05:32:53.167062+08` | `applied@2026-10-20 21:59:27.119651+08` |
| 697 | `697_request_logs_promote_system_fingerprint.sql` | `applied@2026-10-11 00:12:23.250882+08` | `applied@2026-09-12 05:58:37.986831+08` | `applied@2026-10-20 21:59:27.537957+08` |
| 698 | `698_promote_hot_partition_timezone_pin.sql` | `applied@2026-10-11 00:12:23.278885+08` | — | `applied@2026-10-20 21:59:28.002692+08` |
| 699 | `699_supplier_errors_ensure_timezone_pin.sql` | `applied@2026-10-11 00:12:23.300067+08` | — | `applied@2026-10-20 21:59:28.586269+08` |
| 700 | `700_request_logs_view_raw_model_name.sql` | `applied@2026-10-11 01:16:46.7027+08` | — | `applied@2026-10-20 21:59:29.022283+08` |
| 701 | `701_credential_balance_floor.sql` | `applied@2026-10-11 00:12:59.861599+08` | — | `applied@2026-10-20 21:59:29.470179+08` |
| 703 | `703_supplier_errors_promote_timezone_pin.sql` | `applied@2026-10-11 00:12:59.882206+08` | — | `applied@2026-10-20 21:59:29.913386+08` |
| 705 | `705_request_logs_reattach_detached_partitions.sql` | `applied@2026-10-11 00:12:59.906889+08` | — | `applied@2026-10-20 21:59:30.355679+08` |
| 706 | `706_session_family_s1a.sql` | `applied@2026-10-11 00:12:59.959292+08` | — | `applied@2026-10-20 21:59:30.856065+08` |
| 706 | `706b_session_turns_hot_prelude.sql` | `applied@2026-10-11 00:16:19.671302+08` | — | `applied@2026-10-20 21:59:31.32625+08` |
| 707 | `707_session_turns_s1a.sql` | `applied@2026-10-11 00:16:19.706742+08` | — | `applied@2026-10-20 21:59:31.859777+08` |
| 707 | `707b_session_bodies_hot_prelude.sql` | `applied@2026-10-11 00:16:56.701851+08` | — | `applied@2026-10-20 21:59:32.300876+08` |
| 708 | `708_session_bodies_s1a.sql` | `applied@2026-10-11 00:16:56.742375+08` | — | `applied@2026-10-20 21:59:32.841136+08` |
| 710 | `710_request_logs_view_session_family_v2.sql` | `applied@2026-10-11 00:17:40.717256+08` | — | `applied@2026-10-20 21:59:35.58206+08` |
| 711 | `711_hosted_tasks.sql` | `applied@2026-10-11 00:17:21.260715+08` | — | `applied@2026-10-20 21:59:34.680177+08` |
| 712 | `712_session_mirror_outbox.sql` | `applied@2026-10-11 00:16:56.77414+08` | — | `applied@2026-10-20 21:59:33.317856+08` |
| 713 | `713_session_turns_cost_precision.sql` | `applied@2026-10-11 00:17:21.219028+08` | — | `applied@2026-10-20 21:59:34.258921+08` |
| 714 | `714_partition_timezone_pin_remaining.sql` | `applied@2026-10-11 00:17:40.8031+08` | — | `applied@2026-10-20 21:59:36.426873+08` |
| 715 | `715_route_incidents_pending_state.sql` | `applied@2026-10-11 00:17:40.834618+08` | — | `applied@2026-10-20 21:59:37.232591+08` |
| 716 | `716_unify_probe_health_views.sql` | `applied@2026-10-11 00:17:40.875151+08` | — | `applied@2026-10-20 21:59:37.674689+08` |
| 717 | `717_request_logs_hot_column_alignment.sql` | `applied@2026-10-11 00:17:40.919505+08` | — | `applied@2026-10-20 21:59:38.205683+08` |
| 718 | `718_drop_redundant_indexes_and_add_ttl_indexes.sql` | `applied@2026-10-11 00:17:58.970595+08` | — | `applied@2026-10-20 21:59:39.198271+08` |
| 719 | `719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql` | `applied@2026-10-11 00:17:59.008463+08` | — | `applied@2026-10-20 21:59:39.639917+08` |
| 720 | `720_rls_policy_vocabulary_unification.sql` | `applied@2026-10-11 00:17:59.04825+08` | — | `applied@2026-10-20 21:59:40.063562+08` |
| 721 | `721_credential_balance_source_and_error.sql` | `applied@2026-10-11 00:17:59.075743+08` | — | `applied@2026-10-20 21:59:40.487252+08` |
| 722 | `722_durable_family_schema_convergence.sql` | `applied@2026-10-11 01:11:32.065781+08` | — | `applied@2026-10-20 21:59:42.495439+08` |
| 723 | `723_rls_enable_attachments_and_cfl_old.sql` | `applied@2026-10-11 01:11:32.125802+08` | — | `applied@2026-10-20 21:59:42.896104+08` |
| 724 | `724_task_type_corrections.sql` | `applied@2026-10-11 01:11:32.167462+08` | — | `applied@2026-10-20 21:59:43.305103+08` |
| 725 | `725_r41_request_logs_and_tmp_super_admin_bypass.sql` | `applied@2026-10-11 01:11:32.192675+08` | — | `applied@2026-10-20 21:59:43.706685+08` |
| 726 | `726_restore_credential_model_index_hot_unique.sql` | `applied@2026-10-11 01:11:32.220176+08` | — | `applied@2026-10-20 21:59:44.123019+08` |
| 727 | `727_sql_audit_slow_query_indexes.sql` | `applied@2026-10-11 01:11:32.25992+08` | — | `applied@2026-10-20 21:59:44.545803+08` |
| 728 | `728_sql_audit_request_logs_credential_model_index.sql` | `applied@2026-10-11 01:11:32.289973+08` | — | `applied@2026-10-20 21:59:44.961744+08` |
| 729 | `729_sql_audit_session_turns_credential_ts_index.sql` | `applied@2026-10-11 01:23:57.966189+08` | — | `applied@2026-10-20 21:59:48.466204+08` |
| 730 | `730_session_role_hierarchy.sql` | `applied@2026-10-11 01:11:32.329898+08` | — | `applied@2026-10-20 21:59:45.403698+08` |
| 731 | `731_auto_route_selection_role_attribution.sql` | `applied@2026-10-11 01:11:32.35481+08` | — | `applied@2026-10-20 21:59:45.817157+08` |
| 733 | `733_session_turn_details.sql` | `applied@2026-10-11 01:12:33.815933+08` | — | `applied@2026-10-20 21:59:46.738198+08` |
| 734 | `734_request_logs_view_details_join.sql` | `applied@2026-10-11 01:23:57.92951+08` | — | `applied@2026-10-20 21:59:48.015489+08` |
| 735 | `735_models_canonical_active_folded_unique.sql` | `applied@2026-10-11 01:23:57.991918+08` | — | `applied@2026-10-20 21:59:48.870445+08` |
| 736 | `736_maas_rate_multiplier.sql` | `applied@2026-10-11 01:23:58.02634+08` | — | `applied@2026-10-20 21:59:49.281705+08` |
| 737 | `737_maas_reconciliation_findings.sql` | `applied@2026-10-11 01:23:58.054708+08` | — | `applied@2026-10-20 21:59:49.712857+08` |
| 738 | `738_view_chain_credits_rate_multiplier.sql` | `applied@2026-10-11 01:29:02.41462+08` | — | `applied@2026-10-20 21:59:50.194502+08` |
| 739 | `739_promote_functions_rate_multiplier.sql` | `applied@2026-10-11 01:29:02.439227+08` | — | `applied@2026-10-20 21:59:50.595391+08` |
| 740 | `740_view_chain_client_ip.sql` | `applied@2026-10-11 01:30:45.812848+08` | — | `applied@2026-10-20 21:59:51.053186+08` |
| 742 | `742_hosted_task_recalled_event.sql` | `applied@2026-10-11 01:30:45.8364+08` | — | `applied@2026-10-20 21:59:51.459999+08` |
| 743 | `743_normalize_provider_protocol.sql` | `applied@2026-10-11 01:30:45.858569+08` | — | `applied@2026-10-20 21:59:51.862958+08` |
| 744 | `744_sql_audit_partial_indexes.sql` | `applied@2026-10-11 01:30:45.893385+08` | — | `applied@2026-10-20 21:59:52.281764+08` |
| 745 | `745_report_snapshots.sql` | `applied@2026-10-11 01:30:45.918127+08` | — | `applied@2026-10-20 21:59:52.68548+08` |
| 746 | `746_report_snapshots_internal_dims.sql` | `applied@2026-10-11 01:30:45.958842+08` | — | `applied@2026-10-20 21:59:53.103583+08` |
| 749 | `749_usage_facts_occurred_at_index.sql` | `applied@2026-10-11 01:31:21.359192+08` | — | `applied@2026-10-20 21:59:53.949776+08` |
| 750 | `750_usage_facts_daily_partition.sql` | `applied@2026-10-11 01:31:21.400958+08` | — | `applied@2026-10-20 21:59:54.395115+08` |
| 751 | `751_usage_facts_partition_tz_pin.sql` | `applied@2026-10-11 01:31:21.421865+08` | — | `applied@2026-10-20 21:59:54.80452+08` |
| 752 | `752_mock_probe_history.sql` | `applied@2026-10-11 01:31:21.456706+08` | — | `applied@2026-10-20 21:59:55.247278+08` |
| 753 | `753_session_turn_logs_ttl.sql` | `applied@2026-10-11 01:32:07.952821+08` | — | `applied@2026-10-20 21:59:56.880494+08` |
| 754 | `754_archive_request_logs_default.sql` | `applied@2026-10-11 01:32:07.975245+08` | — | `applied@2026-10-20 21:59:57.288761+08` |
| 756 | `756_request_logs_id_index.sql` | `applied@2026-10-11 01:32:07.999003+08` | — | `applied@2026-10-20 21:59:57.703295+08` |
| 757 | `757_session_turns_origin_actor_projection.sql` | `applied@2026-10-11 01:32:08.031111+08` | — | `applied@2026-10-20 21:59:58.249386+08` |
| 758 | `758_routeincident_missing_columns.sql` | `applied@2026-10-11 01:32:08.061688+08` | — | `applied@2026-10-20 21:59:58.682703+08` |
| 760 | `760_analysis_events_inbox_ttl_indexes.sql` | `applied@2026-10-11 01:33:27.327759+08` | — | `applied@2026-10-20 22:00:00.482418+08` |
| 761 | `761_stats_inbox_sync_status_backfill.sql` | `applied@2026-10-11 01:33:27.350863+08` | — | `applied@2026-10-20 22:00:00.937378+08` |
| 762 | `762_session_project_backfill_chain.sql` | `applied@2026-10-11 01:33:57.820353+08` | — | `applied@2026-10-20 22:00:01.355538+08` |
| 763 | `763_provider_events_contract.sql` | `applied@2026-10-11 01:33:57.850981+08` | — | `applied@2026-10-20 22:00:01.785031+08` |
| 764 | `764_request_logs_tenant_ts_index.sql` | `applied@2026-10-11 01:33:57.872122+08` | — | `applied@2026-10-20 22:00:02.216501+08` |
| 765 | `765_bodies_columnar_storage.sql` | `applied@2026-10-11 01:33:57.968668+08` | — | `applied@2026-10-20 22:00:02.731237+08` |
| 800 | `800_provider_endpoint_protocols.sql` | `applied@2026-10-11 01:32:07.923715+08` | — | `applied@2026-10-20 21:59:56.344564+08` |
| 801 | `801_session_turn_details_duplicate_drain.sql` | `applied@2026-10-11 01:33:58.032168+08` | — | `applied@2026-10-20 22:00:03.944965+08` |
| 802 | `802_session_turn_details_gw_task_id_index.sql` | `applied@2026-10-11 01:33:58.077385+08` | — | `applied@2026-10-20 22:00:04.502667+08` |
| 803 | `803_candidate_failure_logs_hot_column_reconcile.sql` | `applied@2026-10-11 01:33:58.098056+08` | — | `applied@2026-10-20 21:58:58.302054+08` |
| 804 | `804_credential_model_context_window_columns.sql` | `applied@2026-10-11 01:33:58.11895+08` | — | `applied@2026-10-20 22:00:05.169041+08` |
| 805 | `805_session_dim_reconcile.sql` | `applied@2026-10-11 01:33:58.142322+08` | — | `applied@2026-10-20 22:00:06.004952+08` |
| 806 | `806_session_bodies_partitions_heap.sql` | `applied@2026-10-11 01:33:58.184028+08` | — | `applied@2026-10-20 22:00:06.509522+08` |
| 807 | `807_request_logs_bodies_hot_drop_duplicate_request_id_index.sql` | `applied@2026-10-11 01:33:58.206888+08` | — | `applied@2026-10-20 22:00:06.939566+08` |
| 808 | `808_request_logs_default_partition.sql` | `applied@2026-10-11 01:33:58.264052+08` | — | `applied@2026-10-20 22:00:07.870031+08` |
| 809 | `809_instance_release_status_nullable_release_id.sql` | `applied@2026-10-11 01:33:58.297195+08` | — | `applied@2026-10-20 22:00:08.31106+08` |
| 810 | `810_heap_partitions_toastless_heal.sql` | `applied@2026-10-11 01:33:58.319449+08` | — | `applied@2026-10-20 22:00:08.756912+08` |
| 811 | `811_partition_bounds_shanghai_midnight_repair.sql` | `applied@2026-10-11 01:33:58.398459+08` | — | `applied@2026-10-20 22:00:10.918948+08` |
| 812 | `812_model_probe_runs_partitions_heap.sql` | `applied@2026-10-11 01:33:58.427947+08` | — | `applied@2026-10-20 22:00:11.358885+08` |
| 813 | `813_supplier_errors_partitions_heap.sql` | `applied@2026-10-11 01:33:58.462302+08` | — | `applied@2026-10-20 22:00:11.848888+08` |
| 814 | `814_adaptive_probe_targets_hot_subquery.sql` | `applied@2026-10-11 01:33:58.484688+08` | — | `applied@2026-10-20 22:00:12.355099+08` |
| 815 | `815_request_logs_view_stage_band_cff.sql` | `applied@2026-10-11 01:37:44.787337+08` | — | `applied@2026-10-20 22:00:13.337913+08` |
| 816 | `816_request_logs_view_client_ip_projection.sql` | `applied@2026-10-11 01:38:19.144136+08` | — | `applied@2026-10-20 22:00:13.80506+08` |
| 817 | `817_request_logs_view_client_ip_semantic_guard.sql` | `applied@2026-10-11 01:38:55.293236+08` | — | `applied@2026-10-20 22:00:14.302567+08` |
| 818 | `818_ursm_snapshot_typed_columns.sql` | `applied@2026-10-11 01:38:55.347722+08` | — | `applied@2026-10-20 22:00:14.732352+08` |
| 820 | `820_audio_modality_backfill.sql` | `applied@2026-10-11 01:38:55.385929+08` | — | `applied@2026-10-20 22:00:15.394929+08` |
| 821 | `821_session_turns_abandoned_marker.sql` | `applied@2026-10-11 01:38:55.417554+08` | — | `applied@2026-10-20 22:00:15.842811+08` |
| 822 | `822_session_summaries_health_pending_index.sql` | `applied@2026-10-11 01:38:55.440077+08` | — | `applied@2026-10-20 22:00:16.261077+08` |
| 823 | `823_session_turns_request_status.sql` | `applied@2026-10-11 01:38:55.463291+08` | — | `applied@2026-10-20 22:00:16.683036+08` |
| 824 | `824_request_status_rate_limited_projection.sql` | `applied@2026-10-11 01:38:55.501628+08` | — | `applied@2026-10-20 22:00:17.186267+08` |
| 825 | `825_modality_graded_verification.sql` | `applied@2026-10-11 01:38:55.540649+08` | — | `applied@2026-10-20 22:00:17.610863+08` |
| 826 | `826_model_baseline_price.sql` | `applied@2026-10-11 01:38:55.569054+08` | — | `applied@2026-10-20 22:00:18.034517+08` |
| 827 | `827_modality_verification_progress_view.sql` | `applied@2026-10-11 01:38:55.593242+08` | — | `applied@2026-10-20 22:00:18.469082+08` |
| 828 | `828_supplier_errors_unified_tracked.sql` | `applied@2026-10-11 01:38:55.642546+08` | — | `applied@2026-10-20 22:00:19.292842+08` |
| 829 | `829_bodies_columnar_rollback.sql` | `applied@2026-10-11 01:38:55.702209+08` | — | `applied@2026-10-20 22:00:20.116326+08` |
| 831 | `831_work_type_route_source.sql` | `applied@2026-10-11 01:38:55.666005+08` | — | `applied@2026-10-20 22:00:19.694257+08` |
| 832 | `832_model_baseline_observation_health.sql` | `applied@2026-10-11 01:38:55.722066+08` | — | `applied@2026-10-20 22:00:20.520772+08` |
| 833 | `833_supplier_price_nonneg_check.sql` | `applied@2026-10-11 01:38:55.744298+08` | — | `applied@2026-10-20 22:00:20.921701+08` |
| 834 | `834_supplier_errors_base_tables.sql` | `applied@2026-10-11 01:38:55.620296+08` | — | `applied@2026-10-20 22:00:18.884899+08` |
| 835 | `835_modality_verify_probe_ledger.sql` | `applied@2026-10-11 01:38:55.769516+08` | — | `applied@2026-10-20 22:00:21.33462+08` |
| 836 | `836_supplier_errors_heap_reassert.sql` | `applied@2026-10-11 01:38:55.800292+08` | — | `applied@2026-10-20 22:00:21.743842+08` |
| 837 | `837_routing_mv_refresh_state.sql` | `applied@2026-10-11 01:38:55.846653+08` | — | `applied@2026-10-20 22:00:22.220464+08` |
| 838 | `838_analyze_skip_frozen_month.sql` | `applied@2026-10-11 01:38:55.86753+08` | — | `applied@2026-10-20 22:00:22.616481+08` |
| 839 | `839_autovac_current_month_heap_handoff.sql` | `applied@2026-10-11 01:38:55.888707+08` | — | `applied@2026-10-20 22:00:23.024341+08` |
| 840 | `840_analyze_stats_throttle_slot.sql` | `applied@2026-10-11 01:38:55.916638+08` | — | `applied@2026-10-20 22:00:23.450068+08` |
| 841 | `841_monthly_partition_retention.sql` | `applied@2026-10-11 01:38:55.940652+08` | — | `applied@2026-10-20 22:00:23.872151+08` |
| 842 | `842_credential_model_index_latest_bucket_idx.sql` | `applied@2026-10-11 01:38:55.961618+08` | — | `applied@2026-10-20 22:00:24.282625+08` |
| 843 | `843_candidate_failure_logs_ts_desc_idx.sql` | `applied@2026-10-11 01:39:59.43282+08` | — | `applied@2026-10-20 22:00:24.759798+08` |
| 844 | `844_supplier_view_cache_baseline_columns.sql` | `applied@2026-10-11 01:39:59.461162+08` | — | `applied@2026-10-20 22:00:35.012689+08` |
| 845 | `845_stats_event_dedup_join_index.sql` | `applied@2026-10-11 01:52:20.775357+08` | — | `applied@2026-10-20 22:00:35.659774+08` |
| 846 | `846_session_turns_req_status_backfill_idx.sql` | `applied@2026-10-11 01:52:20.804106+08` | — | `applied@2026-10-20 22:00:36.158189+08` |
| 847 | `847_credential_routing_scope.sql` | `applied@2026-10-11 01:52:20.826492+08` | — | `applied@2026-10-20 22:00:36.653438+08` |
| 848 | `848_billing_unit_and_audio_seconds.sql` | `applied@2026-10-11 01:52:20.847946+08` | — | `applied@2026-10-20 22:00:37.12628+08` |
| 849 | `849_session_turns_req_status_backfill_cover_idx.sql` | `applied@2026-10-11 01:52:20.871273+08` | — | `applied@2026-10-20 22:00:37.620196+08` |
| 850 | `850_session_summaries_project_backfill_recent_idx.sql` | `applied@2026-10-11 01:52:20.894712+08` | — | `applied@2026-10-20 22:00:38.053831+08` |
| 851 | `851_disable_overbroad_pii_ip_address_rule.sql` | `applied@2026-10-11 01:52:20.913797+08` | — | `applied@2026-10-20 22:00:38.52063+08` |
| 852 | `852_models_canonical_column_set_reconcile.sql` | `applied@2026-10-12 01:29:40.181789+08` | — | `applied@2026-10-20 22:00:39.007272+08` |
| 853 | `853_credential_probe_queue_automatic.sql` | `applied@2026-10-12 01:42:55.560577+08` | — | `applied@2026-10-20 21:59:24.748586+08` |
| 854 | `854_session_turns_advisory_lock_key.sql` | `applied@2026-10-12 01:52:08.353533+08` | — | `applied@2026-10-20 21:59:23.807714+08` |
| 855 | `855_session_analytics_time_window.sql` | `applied@2026-10-13 05:08:17.155402+08` | — | `applied@2026-10-20 21:58:50.200805+08` |
| 856 | `856_usage_ledger_ts_cover_agg_idx.sql` | `applied@2026-10-13 08:18:50.106143+08` | — | `applied@2026-10-20 21:58:50.561762+08` |
| 857 | `857_analyze_columnar_staleness_gate.sql` | `applied@2026-10-13 08:50:33.386921+08` | — | `applied@2026-10-20 22:00:39.815168+08` |
| 857 | `857_tighten_pii_phone_intl_regex.sql` | `applied@2026-10-13 08:01:13.700804+08` | — | — |
| 858 | `858_request_logs_final_success_gsid_idx.sql` | `applied@2026-10-13 08:50:33.488909+08` | — | `applied@2026-10-20 22:00:40.238401+08` |
| 859 | `859_tighten_pii_phone_intl_regex.sql` | `applied@2026-10-13 08:50:31.648902+08` | — | `applied@2026-10-20 21:58:50.922089+08` |
| 860 | `860_rename_request_logs_family_pending_delete.sql` | `applied@2026-10-14 13:47:36.709864+08` | — | `applied@2026-10-20 21:58:51.303855+08` |
| 861 | `861_candidate_failure_view_absorb_session_id.sql` | `applied@2026-10-14 13:47:36.991264+08` | — | `applied@2026-10-20 22:00:41.077644+08` |
| 862 | `862_partition_family_leading_ts_idx.sql` | `applied@2026-10-14 13:47:37.337096+08` | — | `applied@2026-10-20 21:58:52.16898+08` |
| 863 | `863_handoff_partition_am_aware.sql` | `applied@2026-10-14 22:48:55.761459+08` | — | `applied@2026-10-20 21:59:36.830066+08` |
| 864 | `864_model_aliases_bare_raw_name_active_idx.sql` | `applied@2026-10-15 12:50:22.133289+08` | — | `applied@2026-10-20 21:58:52.999265+08` |
| 865 | `865_session_bodies_kind_reconcile.sql` | `applied@2026-10-16 02:45:15.682675+08` | — | `applied@2026-10-20 22:00:40.654908+08` |
| 866 | `866_sanitize_rule_management.sql` | `applied@2026-10-16 04:04:02.832414+08` | — | `applied@2026-10-20 21:58:53.43037+08` |
| 867 | `867_session_summaries_last_request_at_idx.sql` | `applied@2026-10-16 04:04:03.129719+08` | — | `applied@2026-10-20 21:58:53.825841+08` |
| 868 | `868_credential_model_stream_mode.sql` | `applied@2026-10-16 06:43:45.582092+08` | — | `applied@2026-10-20 21:58:54.26027+08` |
| 869 | `869_promote_session_turns_trace_events.sql` | `applied@2026-10-17 00:34:32.449588+08` | — | `applied@2026-10-20 22:00:41.591385+08` |
| 870 | `870_usage_ledger_recon_cover_idx.sql` | `applied@2026-10-18 00:57:01.666107+08` | — | `applied@2026-10-20 22:00:42.140354+08` |
| 871 | `871_route_incident_events_created_at_idx.sql` | `applied@2026-10-20 02:32:33.738809+08` | — | `applied@2026-10-20 22:00:42.556714+08` |
| 872 | `872_credential_model_index_partitions_heap.sql` | `applied@2026-10-20 02:32:34.085704+08` | — | `applied@2026-10-20 22:00:43.122951+08` |
| 873 | `873_usage_ledger_ts_cover_cache_agg_idx.sql` | `applied@2026-10-20 02:32:34.557949+08` | — | `applied@2026-10-20 22:00:43.760676+08` |
| 874 | `874_session_turns_client_request_id_idx.sql` | `applied@2026-10-20 02:32:34.972468+08` | — | `applied@2026-10-20 22:00:44.283471+08` |
| 875 | `875_ursm_snapshot_empty_response_rate_5m.sql` | `applied@2026-10-20 02:32:35.324824+08` | — | `applied@2026-10-20 22:00:44.759774+08` |
| 876 | `876_drop_prefix_covered_parent_indexes.sql` | `applied@2026-10-20 02:32:35.686352+08` | — | `applied@2026-10-20 22:00:45.258651+08` |
| 877 | `877_session_turns_search_text_trgm.sql` | `applied@2026-10-20 02:32:37.218625+08` | — | `applied@2026-10-20 22:00:45.73444+08` |
| 878 | `878_supplier_errors_partition_indexes.sql` | `applied@2026-10-20 02:32:37.574203+08` | — | `applied@2026-10-20 22:00:46.172504+08` |
| 879 | `879_drop_session_summaries_redundant_session_key_uidx.sql` | `applied@2026-10-20 05:59:48.493899+08` | — | `applied@2026-10-20 22:00:46.653632+08` |
| 880 | `880_session_write_timeout_budget.sql` | `applied@2026-10-20 13:29:29.118603+08` | — | `applied@2026-10-20 22:00:52.665818+08` |
| 881 | `881_drop_attack_vectors_embedding_index.sql` | `applied@2026-10-20 06:59:21.56257+08` | — | `applied@2026-10-20 22:00:47.178468+08` |
| 882 | `882_request_logs_cost_negative_partial_idx.sql` | `applied@2026-10-20 06:59:21.960935+08` | — | `applied@2026-10-20 22:00:47.59154+08` |
| 883 | `883_session_family_cost_view_metrics.sql` | `applied@2026-10-20 13:29:28.326854+08` | — | `applied@2026-10-20 22:00:48.016113+08` |
| 884 | `884_session_family_v_analytics_views.sql` | `applied@2026-10-20 13:29:28.69882+08` | — | `applied@2026-10-20 22:00:48.581251+08` |
| 885 | `885_session_turns_host_tags.sql` | `applied@2026-10-21 14:30:15.435341+08` | — | `applied@2026-10-20 22:00:49.026412+08` |
| 886 | `886_session_bodies_storage_comment.sql` | `applied@2026-10-21 14:30:15.7649+08` | — | `applied@2026-10-20 22:00:49.466555+08` |
| 887 | `887_session_turns_search_text_fts.sql` | `applied@2026-10-21 14:30:17.269793+08` | — | `applied@2026-10-20 22:00:49.888711+08` |
| 888 | `888_session_family_join_index_gaps.sql` | `applied@2026-10-21 14:30:17.933281+08` | — | `applied@2026-10-20 22:00:50.337925+08` |
| 889 | `889_session_turns_tools_population.sql` | `applied@2026-10-21 14:30:18.261537+08` | — | `applied@2026-10-20 22:00:50.767328+08` |
| 890 | `890_drop_request_logs_v1_family.sql` | `applied@2026-10-21 14:19:33.580944+08` | `applied@2026-10-21 17:41:22.446652+08` | `applied@2026-10-20 22:00:51.481746+08` |
| 891 | `891_session_turns_cost_negative_partial_idx.sql` | `applied@2026-10-21 14:30:18.673345+08` | — | `applied@2026-10-20 22:00:51.877172+08` |
| 892 | `892_ursm_snapshot_slot_daily.sql` | `applied@2026-10-21 14:30:29.878793+08` | — | `applied@2026-10-20 22:42:44.548981+08` |
| 893 | `893_fresh_install_partition_prereq.sql` | `applied@2026-10-21 14:30:12.589419+08` | — | `applied@2026-10-20 22:00:34.441014+08` |
| 894 | `894_rebind_candidate_failure_logs_diagnosis_view.sql` | `applied@2026-10-21 14:30:12.931652+08` | — | `applied@2026-10-21 08:01:26.966465+08` |
| 895 | `895_domain_chain_startup_images.sql` | `applied@2026-10-22 23:34:42.983003+08` | — | `applied@2026-10-24 13:36:54.516885+08` |
| 896 | `896_offline_activation_requests_columns_reassert.sql` | — | — | `applied@2026-10-24 13:36:48.364175+08` |
| 897 | `897_session_turns_promote_final_success_self_heal.sql` | — | — | `applied@2026-10-24 13:36:55.229026+08` |
| 898 | `898_session_bodies_hot_ts_index.sql` | — | — | `applied@2026-10-24 13:36:55.757399+08` |
| 899 | `899_session_bodies_promote_body_sink.sql` | — | — | `applied@2026-10-24 13:36:56.208998+08` |
| 900 | `900_freediscovery_schema_reassert.sql` | — | — | `applied@2026-10-24 13:36:56.946042+08` |
| 901 | `901_usage_ledger_api_key_id_ts_idx.sql` | — | — | `applied@2026-10-24 13:36:57.393393+08` |
| 902 | `902_stats_event_dedup_pkey_include.sql` | — | — | `applied@2026-10-24 13:36:58.052587+08` |
| 903 | `903_sessions_updated_at_session_id_index.sql` | — | — | `applied@2026-10-24 13:36:58.466404+08` |
| 904 | `904_inspector_stats_partial_indexes.sql` | — | — | `applied@2026-10-25 02:50:50.088579+08` |
| 905 | `905_turns_session_model_index.sql` | — | — | `applied@2026-10-25 02:50:50.556306+08` |
| 906 | `906_turns_api_key_numeric_partial_index.sql` | — | — | `applied@2026-10-25 02:50:51.035234+08` |
| 907 | `907_routing_analytics_source_lateral.sql` | — | — | `applied@2026-10-25 02:50:51.65447+08` |
| 908 | `908_route_incident_events_type_created_drop.sql` | — | — | `applied@2026-10-25 08:24:28.495721+08` |
| 909 | `909_session_summaries_search_trgm_indexes.sql` | — | — | `applied@2026-10-25 14:44:52.548181+08` |
|  | `V371__supplier_errors_hot_and_stats.sql` | `applied@2026-10-11 00:08:48.036924+08` | `applied@2026-09-12 05:32:51.384148+08` | `applied@2026-10-20 21:59:08.587417+08` |

★ ★ **散文状态与账本的矛盾数（★ 棘轮基线，★ 只许降）**：`106`（★ 判据：「声称未应用」但该环境账本已 `applied` ⇒ ★ ★★ **重新生成本段落即等于收紧基线** ⇒ ★ 门要求现值 ≤ 本值）

<!-- R160-AUTHORITATIVE:END -->
