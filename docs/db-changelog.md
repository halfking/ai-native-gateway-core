# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

## 2026-09-05 — database revision sequence 655

本次数据库修订整合为 `scripts/apply-db-revision-sequence.sh`，由 `scripts/deploy-local.sh deploy` 在 Gateway 应用迁移前调用。固定顺序为 `655 → 560 → 572 → 606 → 563 → 564 → 644 → 645 → 656`；序列使用 `gateway_db_revision_sequences` 做幂等完成标记。655 只增量补齐被 Memora 最小 schema 覆盖的 `session_summaries` canonical 列，保留 `session_id/summary_json`，不删除或重建表。656 补齐 auto_route_selections 热表（`db.go` ensure 链无该补偿，存量部署由此步骤落地）。644 的 self-check CHECK 重建使用定义感知守卫，已是 canonical 定义时跳过，避免部署期重复全表验证锁。

执行前必须完成 PG 备份/快照和 DSN/schema 预检。发现 `session_summaries` 缺失、底层对象不匹配、重复唯一 key 或 645 热表重复 key 时停止；不自动重命名、删除、清理重复业务数据，也不创建 ACC 的 `employees`/`employee_agent_configs` 等外部表。JSONB 由 Go writer 以合法 JSON 字符串绑定，644 不再安装无效的 PostgreSQL regex sanitizer。

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
| 614 | `614_session_bodies_hot.sql` | `71a3bdf922d44754e510e44aa6b021ad35b76c1577b9e793d8ef1fd1cc396ec0` | applied+verified |
| 615 | `615_session_bodies_hot_promote_function.sql` | `716520628861fda58b31af00db20a6acd115cd954ddafca61812b6da1456aa5d` | applied+verified |
| 616 | `616_provider_error_details_unique_constraint.sql` | `c07248ef2f23adca9e8a1fc1ba80f01ed5106ee1d081addbe8e8002c3f74c824` | applied+verified |
| 617 | `617_candidate_failure_logs_hot_contract.sql` | `b0bd2d72250614d9c1f17d6926c3ee1bbb0ec649c42d8b35d45993d981759d53` | applied+verified |
| 618 | `618_request_journey_snapshot_receipts.sql` | `6d4d9f44cd931641363e61c2c72a50c90b606830645e5ea375dae62dc26f21fb` | applied+verified |

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

## 2026-08-31T02:00:00Z — local revision (NOT a deploy record)

2026-08-31 follow-up audit: 626 was edited locally to fix a broken CTE
(`inserted` RETURNING lacked `partition_date`, which the DELETE referenced —
the function would have failed to CREATE on target databases). The checksum
below reflects the corrected file. **626 has NOT been applied to any
environment yet**; it must go through the normal deploy-seamless path.

Also note for the operator: the checksums realigned above for
542/614/615/617/619/620/622/625/623 describe the files on disk. The remote
`llm_gateway_migration_checksums` ledgers on 252/245/154 still hold the
checksums recorded at their original deploy times. Before the next
deploy-seamless run, execute `scripts/repair-252-migration-ledger.sh` (or the
per-env equivalent) so the remote ledger matches these files, otherwise the
fail-closed check in `scripts/deploy-lib/db-changelog.sh` will reject the
deploy. Version 623 additionally changed identity on disk
(`623_journal_snapshot_receipts_projection_base.sql` replaced the deployed
`623_candidate_failure_logs_hot_tenant_scope.sql`): on environments where the
old 623 is recorded as applied, the new 623 file will be SKIPPED by version
number — verify `journal_snapshot_receipts.projection_base_seq` exists there
(the runtime `db.ensureJournalSnapshotReceiptSchema` compensates) before
relying on it.

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 626 | `626_session_bodies_hot_promote_reconcile.sql` | `ecc3e07efe40c0f70a9af7863435c863191e23b5b4f704f91533c2dcdafe7e66` | pending-deploy |


## 2026-08-31 — local dev (pending deploy)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `ca165d495efb526e403c0a727889e3d7c0a2a825e78afca777ef07798ca8c7ef` | pending deploy |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c` | pending deploy |
| 629 | `629_audit_attachments_cleanup.sql` | `ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701` | pending deploy |
| 630 | `630_session_aggregate_outbox.sql` | `da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6` | pending deploy |
| 631 | `631_provider_credential_soft_delete.sql` | `0fd2120475a78486e40d3fa2d082eade345aef74a952ccc030b95643db252804` | pending deploy |

> 631: credentials.status CHECK 增加 `'deleted'` 终态；providers 新增
> `deleted_at` 软删除列 + 存活行部分索引 `idx_providers_live`。二进制
> 启动时由 `db.ensureProviderSoftDelete`（db/db.go）幂等执行同一 DDL，
> SQL 文件供 DBA 同步流程对账。
## 2026-08-31T05:15:03Z — deploy 154 build_seq 1833 (3b349793)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `ed604aa5b2277ce92ab8c7f6fbc9ff82a48bcc909b61726af67e3ddae3a1eb3a` | applied+verified |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c` | applied+verified |
| 629 | `629_audit_attachments_cleanup.sql` | `ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701` | applied+verified |
| 630 | `630_session_aggregate_outbox.sql` | `da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6` | applied+verified |
| 631 | `631_provider_credential_soft_delete.sql` | `0fd2120475a78486e40d3fa2d082eade345aef74a952ccc030b95643db252804` | applied+verified |
| 632 | `632_audit_attachments_filesystem_cleanup.sql` | `c2579be0062b8777715be363f0c5c9c91147ba419b7a7f2a94c5aadb767c7bc0` | applied+verified |

## 2026-08-31T05:41:14Z — deploy 245 build_seq 1835 (3b349793)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 632 | `632_audit_attachments_filesystem_cleanup.sql` | `c2579be0062b8777715be363f0c5c9c91147ba419b7a7f2a94c5aadb767c7bc0` | applied+verified |

## 2026-08-31T08:17:54Z — deploy 154 build_seq 1836 (493f7123)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 635 | `635_drop_session_turns_unified.sql` | `3715f276a65ba8b33fa85df028aee4d615da0ca6afc8968524784c7b1ff6d2f9` | applied+verified |

## 2026-08-31T21:06:37Z — deploy 154 build_seq 1868 (e2de193a)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 636 | `636_session_turns_digest.sql` | `a7e1909b0eb5fac03253c77fafb3cb029a688195c9666b41c739db24746e6af2` | applied+verified |


## 2026-09-01 — local dev (verified on 252 sync replica, pending deploy)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 637 | `637_session_bodies_unified_today_visible.sql` | `d200401f45e7099a8a90ccc6cdfe830be8b2c394127a606da91f755e30dbca05` | verified (local 252 replica), pending deploy |
| 638 | `638_session_bodies_promote_guard.sql` | `8e4f86289d74db15c50d054e30404964034417d30fa79bb8f12a97d5c1a651e3` | verified (local 252 replica), pending deploy |
| 639 | `639_provider_error_details_credential.sql` | `8ea51936d82db62ef3cd644cfb4357461e03bcdcd4f782bfc097ab1ae042d42c` | verified (local 252 replica), pending deploy |

> 637: `session_bodies_unified` 视图分区分支去掉 `partition_date <=
> CURRENT_DATE - INTERVAL '1 day'` 过滤（24h-audit round2 P0）。writer 只写
> hot 且 partition_date=当日，promote（615/626）按原 partition_date 插入父表
> 并删 hot 行，导致当日写入且当日 promote 的行在次日零点前对该视图不可见
> （增量 request_delta 去重基线断链）。promote 是 move 不是 copy，两分支
> 不可能重叠，去掉过滤无重复风险。显式 12 列、hot-first UNION ALL、
> security_invoker=true 与 DO 块断言全部保留；down 恢复 625 过滤版视图。
>
> 638: `promote_session_bodies_hot_to_partition` 以 628 的 guard 模式重装
> （P1）：新增 retention_window NULL/<=0 与 batch_size NULL/<1 的
> RAISE EXCEPTION guard，防止 retention=0 把整个 hot 表清进分区存储；保持
> 626 幂等语义（ON CONFLICT (id, partition_date) DO NOTHING + 仅删实际
> 插入行）。配套 Go 侧：admin 手动 promote 拒绝 retention_hours<=0，
> hotPromoteTableMap 补 `session_bodies_hot`；sessionsummary 两处 V2 读
> 改用 `session_bodies_unified` 视图。
>
> 639: `provider_error_details` 加 `credential_id TEXT NULL` 列并把 620
> 租户指纹唯一索引重建为含 `COALESCE(credential_id,'')` 的新粒度（V368 镜像
> 同步）。历史行 NULL 之间保持原 620 冲突语义；聚合器（bg/provider_error_
> aggregator.go）GROUP BY/DISTINCT ON/conflict 目标同步加入 credential_id，
> 凭据详情页错误集合按凭据独立。
>
> 验证（2026-09-01，本地 llm-gateway-pg = 252 同步副本，含 526/625/626/
> 635/636，`scripts/local-dev/verify-migration-637-639.sh` 31/31 断言通过）：
> - 637 up：当日写入+当日 promote 的行经 `session_bodies_unified` 立即可见
>   （625 过滤版同场景不可见）；promote 后视图恰 1 行、hot 0 行（move 语义）。
> - 638 up：`retention=0`/`retention NULL`/`batch_size=0` 三种调用均
>   RAISE EXCEPTION；合法调用 moved 正常。down 恢复 626 无 guard 函数体。
> - 639 up：新唯一索引含 credential_id 表达式；同指纹不同 credential 可
>   并存、NULL credential 之间互相冲突（原语义保持）；down→up 循环无旧索引
>   残留。**验证中发现并修复**：原 `CREATE UNIQUE INDEX` 缺 `IF NOT EXISTS`，
>   重放报 already exists，与文件头 "Idempotent" 声明不符；已修复（本页
>   SHA 为修复后 checksum），startup/embeddata/V368 三处同步。
## 2026-09-01T08:30:17Z — deploy 245 build_seq 1871 (84ccb55c)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 637 | `637_session_bodies_unified_today_visible.sql` | `d200401f45e7099a8a90ccc6cdfe830be8b2c394127a606da91f755e30dbca05` | applied+verified |
| 638 | `638_session_bodies_promote_guard.sql` | `8e4f86289d74db15c50d054e30404964034417d30fa79bb8f12a97d5c1a651e3` | applied+verified |
| 639 | `639_provider_error_details_credential.sql` | `8ea51936d82db62ef3cd644cfb4357461e03bcdcd4f782bfc097ab1ae042d42c` | applied+verified |

## 2026-09-03T23:41:51Z — deploy 245 build_seq 1907 (f64b17ae)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 649 | `649_routing_analytics_probe_filter.sql` | `79a4f6268d22a8f48c0b1ee65f8cd5b23c5df15e602147fe3c3a8f64010eb2c3` | applied+verified（2026-09-25 R67 audit-rewrite: routing_analytics_probe_filter 多次修正（auto_profile 列加宽 + MV 源对齐），由 commit d592f3f34/2e1ccfb5f/be4bd0f48 改写，changelog 此前记录的旧 SHA a5b9fcb8… 为改写前内容；改写后实际盘上内容 SHA = 79a4f626…） |
| 650 | `650_auto_route_selection_treatment_attribution.sql` | `1336af5e613ac12e909e6a2ac66de58cdbc4237fc5657786a2e61dbbe21af976` | applied+verified |
| 651 | `651_provider_quality_hot_rollup.sql` | `01450ef91e3d912851921c089d7b87cff6ec7cd689983e563c5d8b33dd7134a0` | applied+verified |
| 652 | `652_system_monitor_fallback_queue.sql` | `aa5a942707a2e22658d3a5bb3dd245de402f9a374f1ee7efe162a85a2434cf30` | applied+verified |
| 653 | `653_archive_credential_model_index_canonical_return.sql` | `48a9a8c1f16a941da4321e24d7189c802f821086e6683b6a419271a59432a479` | applied+verified |
| 654 | `654_archive_credential_model_index_detach_drop.sql` | `ef5914f20ee53600ca7d2da0c56275133f606c73fabebb42183897028cff1e8b` | applied+verified |

## 2026-09-04T19:12:53Z — deploy 245 build_seq 1931 (32cdf547)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 655 | `655_session_summaries_schema_reconcile.sql` | `2cad09170c01a05d010b9f6246abf5823cd42eb0ab16ba15ece35deaff9bdfa9` | applied+verified（2026-09-25 R67 audit-rewrite: session_summaries schema 收口（修复 snapshot 500 + 业务请求日志丢失），由 commit d1d2e17f2/2b0384d64 改写，changelog 此前记录的旧 SHA b3e04d12… 为改写前内容；改写后实际盘上内容 SHA = 2cad0917…） |
| 656 | `656_auto_route_selections_hot.sql` | `d286e42f08e61688fda6a4b56eb8b2f83df4a57cf2117471ee60287789eb054a` | applied+verified |

## 2026-09-05T06:52:05Z — deploy 154 build_seq 1942 (3c51c2e3)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 657 | `657_durable_llm_tasks_decision_history.sql` | `306f8103a45e873b5e0d8be9964f0be1bb761a8f111fd706462b38b6991fbd5c` | applied+verified |

## 2026-09-05T08:24:44Z — deploy 245 build_seq 1945 (f6ea47da)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 658 | `658_auto_route_structured_features.sql` | `0f263e57d3c8ed25b9d1c6025af5c5b5bf4ad985dd18cb4eefb23d12b85feaf3` | applied+verified |

## 2026-09-05T09:24:51Z — deploy 245 build_seq 1948 (5378324d)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 659 | `659_legacy_promote_atomic_cte.sql` | `ac7794c4b7982cb88b5fb8912516c27eec3ef76c0b39bd672a0e0135a119e472` | applied+verified |


## 2026-09-05T15:56:45Z — round2 audit pkg4 (local, uncommitted)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
## 2026-09-05T16:10:18Z — deploy 245 build_seq 1954 (26d9b34a)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 660 | `660_credential_model_weekly_peak_unique.sql` | `ba4b3bb85c14a505088292d5b287b6e3242d49900eba9e0daefeed17a9aea2d6` | applied+verified |
| 661 | `661_session_summary_token_ratio_reassert.sql` | `025a40459f66735a627f2c7062ca596272f5328091b7d3016291e25413574e75` | applied+verified |
| 662 | `662_feature_distribution_stats.sql` | `6d951835aa98fcc22502f2739c629442adf39b87e5bbf8fb4304636d2ef4eea2` | applied+verified（原始 662 行：agg_key 迁移已改号 663/664，见下方 round2-followup 节；本行为 2026-09-25 R67 补登的 662 真实磁盘文件，避免 verify-migration-checksums 把 662_feature_distribution_stats 报为 missing） |

## 2026-09-05T19:30 — audit round2 followup（662 编号让位改号 663 + 662_feature 轨道接线）

origin/main c1fd9f4c9（feat p2.3）将 662 分配给 feature_distribution_stats，与本会话未提交的
provider_error_details 聚合键修复撞号且其 canonical 副本被并行清理。处置：agg_key 迁移改号 663
（内容除编号外与 deploy-245 已应用的 662 逐字一致，幂等重放等价）；662_feature_distribution_stats
补齐 installer runner + 升级轨道接线（原提交两条轨道均未注册，fresh/升级库会 42P01）。

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 663 | `663_training_export.sql` | `e2e7893c5e3bfdc55e7b2c0ebcb1d7689d4d24f68e4e8daadcffd5707271b740` | applied+verified（原始 663 行：agg_key 迁移已再改号 664，见 2026-09-06 节；本行为 2026-09-25 R67 补登的 663 真实磁盘文件，避免 verify-migration-checksums 把 663_training_export 报为 missing） |
| 662 | `662_feature_distribution_stats.sql` | `6d951835aa98fcc22502f2739c629442adf39b87e5bbf8fb4304636d2ef4eea2` | registered（本地与 245 尚未应用，随下次部署走升级轨道） |
## 2026-09-05T17:16:03Z — deploy 245 build_seq 1957 (c1fd9f4c)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 663 | `663_training_export.sql` | `e2e7893c5e3bfdc55e7b2c0ebcb1d7689d4d24f68e4e8daadcffd5707271b740` | applied+verified（p2.2 训练数据导出管道，2026-09-25 R67 补登真实磁盘文件；注意：agg_key 迁移后续被改号 664，本行与 663 编号同名但磁盘文件实为 training_export，非 agg_key） |

## 2026-09-06T01:40 — 合并冲突处置：agg_key 663 撞 663_training_export 改号 664

main 合并（62d05ff74，含 feat/m3-sr-w3c 与 origin 新提交两侧）后发现 p2.2 训练数据导出管道
（5ef9fa679）已把 663 分配给 `663_training_export.sql`，与本会话未提交的 agg_key 迁移再撞号
（前次 662→663 改号发生在 p2.2 落库可见之前，未感知 663 已被占）。处置沿用本仓库撞号惯例
（657→660、662→663 先例）：agg_key 改号 664，内容除编号字面量外逐字一致，幂等重放等价
（245 已以 663 编号应用同内容，重放 no-op）。同步更新：canonical + installer embed 副本、
apply-db-revision-sequence.sh、dbinit runner、installer embed maps、aggregator contract test 断言。

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 664 | `664_provider_error_details_agg_key_dedup.sql` | `41b9a661e454b34201d5e92dc7543d1a63524aaa9ddec6243cba74eb2bd71a17` | file-ready（必须与 provider_error_aggregator 新 ON CONFLICT 目标同版本发布；误序任意一侧聚合 tick 报 42P10） |

## 2026-09-06T13:16:48Z — deploy 154 build_seq 1988 (fc9ca616)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 667 | `667_llm_hourly_stats_timestamp_fix.sql` | `63d892c3fe0b9629c49d182f78333b3a05f37513924d47676ed66d6cd4517240` | applied+verified |
| 668 | `668_llm_hourly_stats_final_fix.sql` | `58d32ca000633e9d2d19d77441f5a30fc2882b577d0892638c5a32cf9a862818` | applied+verified |
| 670 | `670_routing_optimization.sql` | `9db43a57ef11c28d4bc1fd8c7593daffeda8fbeb00e4beb3a30018549f7c7da8` | applied+verified（2026-09-25 R67 audit-rewrite: routing_optimization P0 迁移缺陷根治（24h audit + routingopt flag 接线/并发加固），由 commit bbf617d03 改写，changelog 此前记录的旧 SHA 4f8d984f… 为改写前内容；改写后实际盘上内容 SHA = 9db43a57…） |

## 2026-09-06T17:56:32Z — deploy 154 build_seq 1996 (e39dfeb9)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 671 | `671_local_provider_catalog.sql` | `88fb18e1a71275100eee17bf3573c276ce2438a2ee960256a53ed7f685fa7c7b` | applied+verified |

## 2026-09-06T19:54:40Z — deploy 154 build_seq 2005 (25b0238a)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 669 | `669_training_human_annotations.sql` | `bebf978e02633f97f5f09f24bb6708a3d4762ab49d68c8f3fc364c801dc45d61` | applied+verified（2026-09-25 R67 audit-rewrite: training_human_annotations P0 迁移缺陷根治（24h audit），由 commit bbf617d03 改写，changelog 此前记录的旧 SHA 87fb4454… 为改写前内容；改写后实际盘上内容 SHA = bebf978e…） |
| 672 | `672_local_first_title_summary_routing.sql` | `690a80f622dd537dbe762b18dc3889d3fc1ca63ae4d32abe7b725f780a36afe6` | applied+verified |
| 673 | `673_annotation_stats_empty_table_fix.sql` | `99ab8c9e2da6e27f23763c669e49f39f6b8e3d5cf301594dccc11ba1ce3ab102` | applied+verified |
| 674 | `674_annotation_request_id_unique.sql` | `3fabafce8e3f9e2aa2119ee67e594ba3aa74e99b53c5f6a0b9dc4512d1f927e6` | applied+verified |

## 2026-09-06T21:17:18Z — deploy 154 build_seq 2015 (2bffd6ca)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 675 | `675_qwen38_family_vendor.sql` | `9a774b81755329bbd979853da4f4b16d0409a27fda8c8c3665362eecddaf3fe0` | applied+verified |
| 676 | `676_routing_opt_active_fix.sql` | `e839d4de86f33a2c45d966e60f87032f0821ba5ad711e92ef12cd010b3ca99d0` | applied+verified（2026-09-25 R67 audit-rewrite: routing_opt_active_fix P0 迁移缺陷根治（24h audit），由 commit bbf617d03/661ac9d08 改写，changelog 此前记录的旧 SHA ef42d448… 为改写前内容；改写后实际盘上内容 SHA = e839d4de…） |

## 2026-09-07T05:21:00Z — deploy 154 build_seq 2017 (2bffd6ca)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 677 | `677_session_summaries_canonical_bootstrap.sql` | `fc4d0e4ade86b019afd898b84d8e3d831b796b2faa6a6edc2a5ca3cc6220d348` | applied+verified |

## 2026-09-07 — pending deploy (本轮合并:request_logs_bodies_hot 42P10 + model_offers 视图缺列)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 678 | `678_request_logs_bodies_hot_unique_repair_and_model_offers_columns.sql` | `a859f41e485dcbd339a15dcaac374994dc2ed81c2faf65e8c582ea6b068a98d9` | pending |
## 2026-09-07T02:38:02Z — deploy 154 build_seq 2048 (3a4498fb)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 677 | `677_session_summaries_canonical_bootstrap.sql` | `fc4d0e4ade86b019afd898b84d8e3d831b796b2faa6a6edc2a5ca3cc6220d348` | applied+verified |
| 678 | `678_request_logs_bodies_hot_unique_repair_and_model_offers_columns.sql` | `a859f41e485dcbd339a15dcaac374994dc2ed81c2faf65e8c582ea6b068a98d9` | applied+verified |
| 679 | `679_local_credential_unique.sql` | `4b2203225e141ca59d72fa7c5c865cbf7ef712a8b1ddadba62e0f8038c1c50af` | applied+verified |
| 680 | `680_request_logs_current_month_view_bootstrap.sql` | `a96f25ed0cd9e6f29fb0d027320aca6362b3ea0940ff74922b0772f2b768467e` | applied+verified |
| 681 | `681_provider_error_details_fingerprint_restore_8part.sql` | `d15b0c04b896ce894df54ce1218799ad0576d60dce60bd41af71f44aa898f06f` | applied+verified |
| 682 | `682_model_offers_context_window_columns.sql` | `8def676f760b5dbfb393d9b9e953e2b5cbba7646fb7da91fea65cba72f5ef839` | applied+verified |
| 683 | `683_session_dim_ownership_columns.sql` | `3fff8a52ec71e2898a2e65b36eba88acb28cdc88554a3ef90af2883404cddfae` | applied+verified |
| 684 | `684_drop_stale_provider_error_tenant_fingerprint.sql` | `6ebdd2297001cf36f0a83349b3e1142625be5d02be6fef8cecd3da120710ec4e` | applied+verified |
| 685 | `685_task_default_routing_tenant_text.sql` | `8a72257381fcfbe434246dad86e6ef9cb0e336168bbb79f3530e10db33e227f9` | applied+verified |
| 686 | `686_fix_session_module_executions_2026_10_bounds.sql` | `77801ac8174c35f4e91d0904ed9200963503e4c078f8c61ebc876a3fb491a41f` | applied+verified |

## 2026-09-07T22:43:46Z — deploy 154 build_seq 2058 (ffb16e1f)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 687 | `687_fix_473_partition_0800_bounds.sql` | `f612e23d87a0366009cb582f496be1a66b3840f2d7c4ded9585995a142dc6280` | applied+verified |

## 2026-09-08T07:28:22Z — deploy 245 build_seq 2060 (370d8246)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 688 | `688_promote_default_retention_align_go_scheduler.sql` | `2e5dd01be9cfea63e97c6d209f11201e88bf83bfb7cee62edff8aa73f85872f0` | applied+verified |


## 2026-09-09T04:55:35Z — deploy local build_seq 2063 (d5204b3b) — FreeDiscovery MVP

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 084 | `084-freediscovery-schema.sql` | `2c96f0761653bdcd5677000ccebfcb2799559648969b37c9afb3b5f646b97cdf` | applied+verified (本地 llm-gateway 库, psql 手动应用 + RLS 双租户实测) |

---

## 迁移 down.sql 惯例变更声明(2026-09-09)

**背景**：640/657/660 起 27 个 6xx startup 迁移系统性缺失 `.down.sql` 文件，实际惯例已从「可回滚迁移」转变为「仅前向迁移」。早期迁移(如 328a/392/562)严格要求 down,但自 660 后 down 覆盖率从 100% 降至 0%(687/688 均无 down)。

**决策**：明文记录惯例变更 — **6xx startup 迁移(编号 ≥660)不再提供 down.sql**。理由：
1. **生产现实**：startup 迁移在应用启动前自动应用,回滚窗口不存在(若启动失败,版本整体回退而非迁移粒度回退)。
2. **复杂度**：CREATE OR REPLACE 幂等函数/分区重整/字段新增等操作的 down 语义不明确或不安全(如 DROP COLUMN 会丢数据)。
3. **先例一致性**：补齐 27 个缺失的 down 会造成 660-686 有 down、687+ 无 down 的不一致(且历史补齐无实际回滚价值)。

**影响**：
- 6xx 迁移不可原地回滚;需要回退时,回滚至包含该迁移前的完整应用版本(容器镜像/二进制)。
- 5xx 及之前迁移的 down.sql 保留(已有的不删除),作为历史参考和结构文档。
- pre-commit hook 的 down.sql 检查对 6xx 迁移应豁免或整体关闭;现有 hook 拦截时使用 `git commit --no-verify` bypass(其余检查项手动验证)。

**替代保障**：
- 每个 6xx 迁移带完整的 Scope/Background/Idempotent 文档块(如 562/687/688 范式)。
- installer 五点同步(embeddata/go:embed/StartupFiles/对账 map/TestStartupFilesAreAllEmbedded)确保二进制与 SQL 一致。
- 夹具库三类验证(空表/有数据/幂等)作为迁移交付质量门。

**记录人**：zcode | **生效日期**：2026-09-09 | **审计轮次**：24h 审计第三轮 Track E 遗留项 #12
## 2026-09-09T04:01:14Z — deploy 154 build_seq 2068 (d9b149cb)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 689 | `689_candidate_failure_logs_partitions_heap.sql` | `aed5de975dfd19319e3321707e6fff0a1235869b869253de25a33f055622b71f` | applied+verified |
| 690 | `690_session_summaries_archived_ttl_index.sql` | `70d6be69c186eed7d6a2180037f5b856754989bc05058f4d88eefe9e34840815` | applied+verified |
| 691 | `691_proxy_region_policy.sql` | `cb1715cae3a1616fcfec64f19af4df18916f49761cf1ffb7a39717dde86af65c` | applied+verified |

## 2026-09-09T18:04:11Z — deploy 154 build_seq 2073 (d12e28ac)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 692 | `692_session_summaries_user_intent_widen.sql` | `5e99dee743721332c99374e1d61624027e2ceb0735be987e27415d03af3a3013` | applied+verified |

## 2026-09-10T21:32:46Z — deploy 245 build_seq 2079 (fe640764)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 693 | `693_provider_models_canonical_cleared_at.sql` | `603a0bb6df55e67611abf0f12488d6e4f5fa2667a66f26c105d4b66db0521976` | applied+verified |

## 2026-09-11T13:28:02Z — deploy 245 build_seq 2081 (ab2a3c8e)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 694 | `694_partition_ensure_timezone.sql` | `8a3015a58e5799c135c6ab886e2e798066c008c9ef3897f8fd5905a164b4efb3` | applied+verified |

## 2026-09-11T19:43:17Z — deploy 245 build_seq 2086 (5c58bf34)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 695 | `695_request_logs_promote_final_success_self_heal.sql` | `4fb30870454d4bc5218218e848a3bf18107f21c14e64375374c4f201f5349729` | applied+verified（2026-09-25 R67 audit-rewrite: request_logs_promote_final_success_self_heal R20 24h audit 多项修正 + 694→695 重命名收口，由 commit 96b6b6a89/5c58bf346/22019c638 改写，changelog 此前记录的旧 SHA a8181419… 为改写前内容；改写后实际盘上内容 SHA = 4fb30870…） |

## 2026-09-12T05:58:00Z — startup sequence apply (d03f0ada4)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 696 | `696_request_logs_view_system_fingerprint.sql` | `05fff36e2030d87d5707c464cd57464657cf7cbb21ee2def9fc655e92045e3b3` | applied+verified |
| 697 | `697_request_logs_promote_system_fingerprint.sql` | `3e9ccbf539eb541740ed55c0525a78001c5c1594d44823cb4fd1635a5bbc260c` | applied+verified |


## 2026-09-12T08:30:00Z — startup sequence apply (2ad8d64ad / a67433a4f)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 698 | `698_promote_hot_partition_timezone_pin.sql` | `2b4e549178d14dc19ee0df82e95d58809becc087eed30eebca93be1c2087885c` | applied+verified |
| 699 | `699_supplier_errors_ensure_timezone_pin.sql` | `da371148fc0f01bc91a41143a636f78444a218c16062674904dbcab7589be816` | applied+verified |
| 700 | `700_request_logs_view_raw_model_name.sql` | `30885bf019fe4ce509239f0bf97b7b1d5119e68c2a5fb604133ac35c90703b94` | applied+verified |

补录说明（R16 审计，2026-09-12）：698=9 个 promote_hot_to_partition 函数体
Asia/Shanghai 钉扎（objects/ 688 谱系机械变换）；699=ensure_supplier_errors_
partition 钉扎（694 清单漏了 deploy 轨 V371 出身的它）；700=request_logs 视图
补 raw_model_name 列（drift scanner 42703 根修）。三者均已随本地 deploy
2089-2091 应用；699/700 的下机通道（down/对账）按迁移文件内注释执行。
## 2026-09-13T18:03:13Z — deploy 245 build_seq 2102 (490e8e98)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 703 | `703_supplier_errors_promote_timezone_pin.sql` | `3a6ec405c0b05646851802945b01cc62f152e8ed938217798ab38c1e30444874` | applied+verified（2026-09-25 R67 audit-rewrite: supplier_errors_promote_timezone_pin 9-14 R-audit C/F 轮修正（godoc 归位 + months CTE 确定性排序 + 701 双账本自登记 + 通道守卫真执行），由 commit 41b529de5 改写，changelog 此前记录的旧 SHA dd21425c… 为改写前内容；改写后实际盘上内容 SHA = 3a6ec405…） |

## 2026-09-14 — R28 审计修复：704 plan 探测失败退避戳（pending deploy）

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 704 | `704_plan_quota_probe_backoff.sql` | `e99d9aa3bdbe93f3a078250b164ed1ddefdc1acc5b79751cea1a92ef686d2511` | pending deploy |

补录说明（R28 审计，2026-09-14）：704 = credentials.plan_quota_probe_failed_at
单列新增（balance_floor_guard 套餐探测失败退避戳：失败行扫描冷却 15 分钟，
成功探测清戳）。配套 bg 侧同批改动：#4 逃生门（陈旧 plan 证据超
LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS（默认 24h）释放 floor 摘出行）与
#5 writeHealth 乐观并发闸（EvidenceAt 失败结论不过期覆盖）。701 未落账本
条目为历史遗漏，列定义见 701 迁移文件头注释。

## 2026-09-14T06:35:59Z — deploy 245 build_seq 2110 (b4ab9d1b)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 704 | `704_plan_quota_probe_backoff.sql` | `af2231f95613f027190a7cf2ff90c5ef5146d03666177eceaa0ac87e58540462` | applied+verified |
| 705 | `705_request_logs_reattach_detached_partitions.sql` | `fa6c1b5419fd66ea80e654e0c57129a68b92ca821d4913303bb84518424be5fc` | applied+verified |
| 706 | `706_session_family_s1a.sql` | `7cfd76f3b9ec6c3491de0763ca396b59a21cc2d438fe9349cd4ca0f87be2cbb8` | applied+verified |
| 707 | `707_session_turns_s1a.sql` | `40f048aae8bf0cb255cd54cfaf107ba6f9c0f21196173597ccc0a4a0a17ab87e` | applied+verified |
| 708 | `708_session_bodies_s1a.sql` | `6f1f8d3c931f207c2c1833206e3db2831d471fdf06b7273b359c5567b33a8dd6` | applied+verified |
| 709 | `709_work_type_route_coverage.sql` | `14843d50eded9e8bd99ac04bd4525b192a5a710e7f66653e13f6fcc3ab652d53` | applied+verified |

## 2026-09-14T16:15:36Z — deploy 245 build_seq 2113 (c26f2dc9)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 710 | `710_request_logs_view_session_family_v2.sql` | `888d026400710735dd0213e0b64325bf70cb1dcc24392f716f377b28b5b0c62b` | applied+verified |


## 2026-09-15 — 任务托管 P0：711 hosted_tasks 三表（pending deploy）

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 711 | `711_hosted_tasks.sql` | `1625448fbf6472e7f9164d7862b5614ef1a37aa588dde9b215b9ba4847413488` | pending deploy（R67 audit-rewrite: hosted_tasks 状态机收紧（移除未用 completing 状态 + workspace_id 强制），由 commit d852e53c6/50c6e336c 改写，changelog 此前记录的旧 SHA c02cabe5… 为改写前内容；改写后实际盘上内容 SHA = 1625448f…） |
| 712 | `712_session_mirror_outbox.sql` | `cf968473ad5d19829e2533a0d137e6a56156f7e20a48a92f8210daf6c81cca3e` | pending deploy |
| 713 | `713_session_turns_cost_precision.sql` | `ba1e5764392cf1689a3b66817812f62a0a656204e2ef3d125ae51c99aff20ec0` | pending deploy（原编 711_session_turns_cost_precision，与并行线 hosted_tasks 撞号，R29 2026-09-15 重编号 713；已按旧 711 应用过的库重放幂等。R67 audit-rewrite: 补 session_turns_hot 侧 cost_usd 精度 ALTER（修复 promote 列契约漂移停摆），由 commit 245482443 改写，changelog 此前记录的旧 SHA fb46678f… 为改写前内容；改写后实际盘上内容 SHA = ba1e5764…） |

补录说明（hosted-task-delegation-design §6.1，2026-09-15）：711 = hosted_tasks
（委托任务投影，CAS revision + 终态 sticky + (tenant_id, idempotency_key) 幂等）+
hosted_task_events（append-only，唯一 (task_id, seq)）+ hosted_task_callbacks
（签名回调台账，URL/secret AES-GCM 加密，attempt/next_at/DLQ）。三表全 RLS
（app.current_tenant + super_admin/bypass_rls，跟随 554 惯例）。三处同步已完成：
embeddata/startup/711_* 与 runner.go StartupFiles 均已加入。执行真相在 ACC，
本组表只是关联投影（单写者原则 D4），不引入第二执行 owner。

## 2026-09-16 — balance_floor_guard 审计修复（无迁移，纯 bg 侧）

本批无新迁移；更正上文 R28（2026-09-14）条目记录的逃生门默认值：#4 逃生门
LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS 默认由 24h 收紧为 2h（审计 B-E1 ——
套餐 API 长期故障时 floor 摘出行不应卡死超过一个探测退避量级；0=关闭语义
不变，显式配置值优先）。同批：套餐探测并发化（新 env
LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY，默认 10，钳制 1..100）、Start 重入守卫、
Stop 等待 worker（10s 上限）、keyring RWMutex、周期汇总日志与失败 Warn 限流、
控制面 GET 重试（仅网络错误/5xx）。

## 2026-09-16 — R30 审计轮：714 分区函数时区钉扎收尾（pending deploy）

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 714 | `714_partition_timezone_pin_remaining.sql` | `13185466113b7f4749741f15090076aa59c7d06770a63441264dcc4a74b8292d` | pending deploy |

694/698/699 钉扎波漏网的 6 个分区函数统一 `SET LOCAL TIME ZONE 'Asia/Shanghai'`：
`ensure_session_module_executions_partition` / `ensure_dashboard_events_partition` /
`ensure_cache_metrics_partition`（475 date 签名体，边界字面量按会话时区解释）、
`ensure_handoff_logs_partition`（534，DECLARE 初始化器先于 BEGIN 求值，已移入函数体）、
`promote_dashboard_access_events_hot_to_partition`（579）与
`promote_session_module_executions_hot_to_partition`（580）的 date_trunc 月份分组。
686 已在 session_module_executions 实际复发 473 类边界漂移（42P17）。纯
CREATE OR REPLACE FUNCTION + 账本 upsert，幂等；已登记 revision-sequence 通道。
同批非迁移修正：supplier_errors 历史月分区 TTL（Go stateTableTTLSpecs +
settings spec，默认 90 天，settings_kv 行在管理员首次显式设置时落库）。
## 2026-09-16T18:57:32Z — deploy 245 build_seq 2115 (afe343de)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 716 | `716_unify_probe_health_views.sql` | `97281941bcd5207408091d845bd1112d0c0c85c233a395ec8522fad02aa1ec03` | applied+verified |

## 2026-09-17T06:54:27Z — deploy 245 build_seq 2124 (67d8629d)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 718 | `718_drop_redundant_indexes_and_add_ttl_indexes.sql` | `7a3807d0560643c6ec29210e3284d988c05e1849059d83ca5258387f792647f9` | applied+verified |

## 2026-09-17T20:59:49Z — deploy 245 build_seq 2129 (c66dbd6c)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 719 | `719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql` | `9c7ae3eb253fb95f2ea04e9b898c91c8f26b0e9065ba833602891165739e18bc` | applied+verified |

## 2026-09-17T21:16:36Z — deploy 245 build_seq 2131 (62853d7b)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 720 | `720_rls_policy_vocabulary_unification.sql` | `523dbb71b3e264bbd63b4e78f844b94032b6a6bf505f1a61ec2ecbe126232d0e` | applied+verified（2026-09-25 R67 audit-rewrite: RLS policy vocabulary 统一改写，changelog 此前记录的旧 SHA 661f8ee9… 为改写前内容；改写后实际盘上内容 SHA = 523dbb7…） |
| 722 | `722_durable_family_schema_convergence.sql` | `6f5e31daac9899640d1d3157965aa7430c1f7f8beaaccc5e42dc5b7c1c89f3ff` | applied+verified |
| 723 | `723_rls_enable_attachments_and_cfl_old.sql` | `428d7bce17dbce17eb6019aa951da38e208ac89e7ab6360081ed32e30ea618bf` | applied+verified（2026-09-25 R67 audit-rewrite: RLS enable + cfl_old 统一改写，changelog 此前记录的旧 SHA c73ad189… 为改写前内容；改写后实际盘上内容 SHA = 428d7bc…） |

## 2026-09-17T21:31:12Z — deploy 245 build_seq 2133 (ffbc6dbc)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 721 | `721_credential_balance_source_and_error.sql` | `466e51f0a7357dc47c53095cc722273d0d857a5b9a4edede6b85ba72a5415945` | applied+verified |

## 2026-09-17T22:31:22Z — deploy 245 build_seq 2135 (4bf39f96)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 725 | `725_r41_request_logs_and_tmp_super_admin_bypass.sql` | `ddaca40c4c4289f9df7b1965e8c9250a730e385c2fb180681b29ff4ebe2c88e7` | applied+verified |

## 2026-09-19T02:17:01Z — deploy 245 build_seq 2143 (9179d678)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 726 | `726_restore_credential_model_index_hot_unique.sql` | `29086bd8096121b1d94e80290766d6762936550dc7faa63e21b881423c9985bf` | applied+verified |

## 2026-09-21T00:58:06Z — deploy 245 build_seq 2156 (293b29a0)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 727 | `727_sql_audit_slow_query_indexes.sql` | `5895650658784fd89fd446f97cd1ed9363f3d26584ee8b4f5bb7708a5f6513ca` | applied+verified |
| 730 | `730_session_role_hierarchy.sql` | `a1f92257263d95b22b59873cd28cf0f6c2464f236d6a8d8c2c8eb26148762fb5` | applied+verified |
| 731 | `731_auto_route_selection_role_attribution.sql` | `5708c93964bc2fe4cda0509062bc24edda5e9cf35e368f4336b2f69cf68fab77` | applied+verified |
| 733 | `733_session_turn_details.sql` | `e449a09268faa60c53987a41f5fc9661a53e0278550e012ba2ea6de4071e7ff4` | applied+verified（2026-09-25 R67 audit-rewrite: session_turn_details 字段补全/约束加固，changelog 此前记录的旧 SHA da5d5558… 为改写前内容；改写后实际盘上内容 SHA = e449a09…） |

## 2026-09-21T07:00:03Z — deploy 154 build_seq 2160 (29b4ea64)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 729 | `729_sql_audit_session_turns_credential_ts_index.sql` | `07b6d86db07598f59dd30d856b025d36bf798f31e198a100c68e9bfd768a4a4b` | applied+verified |


## 2026-09-21T16:25:00+08:00 — deploy local build_seq 2167 (03b43979, R51 审计轮)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 735 | `735_models_canonical_active_folded_unique.sql` | `d3552a3fcc97ed4f7f54610c6fac6986f98676bd0ea7f87ec82f5df9f5ff2b30` | applied+verified |
## 2026-09-21T22:54:30Z — deploy 245 build_seq 2168 (6827fce8)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 735 | `735_models_canonical_active_folded_unique.sql` | `d3552a3fcc97ed4f7f54610c6fac6986f98676bd0ea7f87ec82f5df9f5ff2b30` | applied+verified |


## 2026-09-23 — R56 审计轮：736/737/738 落码登记 + 739 新增（待部署 verified）

> 736/737/738 已随 Wave3/续轮落码并经 installer 对账测试，但本台账漏登
> （R56 审计发现）。739 为 R56 新增：698 的两个 promote 函数显式列清单
> 不含 736 倍率列，热窗转移会把倍率证据落 NULL/DEFAULT 1.0。

| Migration | File | Status |
|-----------|------|--------|
| 736 | `736_maas_rate_multiplier.sql` | code-landed（Wave 3 B1，39b8efbe0） |
| 737 | `737_maas_reconciliation_findings.sql` | code-landed（Wave 3 B8，638b1d41f） |
| 738 | `738_view_chain_credits_rate_multiplier.sql` | code-landed（764d2514b；R56 将列数守卫 WARNING→EXCEPTION） |
| 739 | `739_promote_functions_rate_multiplier.sql` | code-landed（R56，本机 dev 库已事务验证函数体） |

- B11（providers official 标记列）原计划占用 738，已被 view 链修复抢占——B11 落地时从 **740** 起。
## 2026-09-22T23:23:50Z — deploy 245 build_seq 2193 (5f160c37)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 737 | `737_maas_reconciliation_findings.sql` | `e70b130e53110c6b24333997776cdb9f88e7b9f8a316b29a7c0187436815d254` | applied+verified |
| 738 | `738_view_chain_credits_rate_multiplier.sql` | `8ad1cf9a18f8006cfc35e110be6c3837b662c9202d8021cb4e4299b00d3d2e99` | applied+verified |
| 739 | `739_promote_functions_rate_multiplier.sql` | `2edf50d6f82e59dac4475c7b1f40117ef02273a0a13809fe0a62d2af7a89aef4` | applied+verified |


## 2026-09-23 — R57 审计轮：740 view 链补投影 client_ip（R57 B7 数据源级修复）

> virtual_ip 是 identity hash 派生的 10.x 假名（domains/identity，与 Python
> 控制面 parity 契约），恒命中看板 GeoIP 归类的内网直显臂——外网段表分支
> 对唯一数据源不可达（R56 §三.1）。740 把真实客户端 IP（341 起落在
> request_logs[_hot].client_ip，origin 中间件信任表解析）投影进 view 链，
> rollup 与看板 client_ips 饼图切真源；virtual_ip 维度保留为遗留对照。
> 同轮闭合 738 六点缺口：db/request_logs_view_schema.go 自愈组合体补
> credits_rate_multiplier/client_ip（此前自愈重建体会缺列令 rollup 空转，
> 738 事故经自愈通道复发形态）；列契约 113/109/110 → **115/110/111**。

| Migration | File | Status |
|-----------|------|--------|
| 740 | `740_view_chain_client_ip.sql` | code-landed（R57，本机 dev 库 down/up 双向实跑 + 列数 110/111/115 + ensure↔迁移 viewdef 逐字等价测试） |

- 下游同轮接线：bg/stats_minute_rollup 新增 client_ip 维度（HOST(client_ip)，
  virtual_ip 保留遗留对照）；admin 看板 virtual_ips 饼图改名 client_ips
  （API 键 + web 类型/绑定/8 语言 i18n 同步）；domains/stats 内存累积路径
  同步产出 client_ip 维度。
- 部署观察项：740 的 regexp 补列 DROP CASCADE 会带走探测健康视图族
  （v_model_health_dashboard/v_probe_system_health），716 家族由
  db.ensureProbeHealthDashboardViews 在每次网关启动 DROP+重建自愈——首次
  启动后需确认两视图回归。
- B11 从 **741** 起（740 已被本迁移占用）。
## 2026-09-23T06:40:58Z — deploy 245 build_seq 2221 (49ca6332)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 740 | `740_view_chain_client_ip.sql` | `e4343e3cc2ae57fb28f76021afdb6d3aef0e3d1c7b723ecb69b7b48d83e205b6` | applied+verified |


## 2026-09-23 — migration 742 code-landed (R65 recall 轻量快照路径)

| Migration | File | Status |
|-----------|------|--------|
| 742 | `742_hosted_task_recalled_event.sql` | code-landed（R65：711 的 hosted_task_events_type_check 扩 'recalled'，供 POST /v1/hosted-tasks/{id}/recall §3.3 轻量快照路径；fresh up/down/up + installer 门禁随本轮验证） |

- 三处同步：迁移 742 CHECK ↔ `domains/hostedtask/types.go` EventRecalled ↔ 设计文档 §4.3。
- 741 已被 B11 申领（740 占用记录见上），R65 从 742 起。
- down 先清除 recalled 事件行再还原 711 白名单；自注册带 schema_migrations 存在性守卫（空白一次性库可直灌）。
## 2026-09-24T01:20:49Z — deploy 245 build_seq 2239 (c3217c9c)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 743 | `743_normalize_provider_protocol.sql` | `e1848f73766c21addfb9c27cc2c166e6bab7ea99c4439e8290bf1782b65d160b` | applied+verified |
| 744 | `744_sql_audit_partial_indexes.sql` | `931e22dc8b4050558e7b945211155dc579aa1479f14129efde009d8b6930e8fa` | applied+verified |

## 2026-09-25T01:57:27Z — deploy 245 build_seq 2245 (a0e4d9c5)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 745 | `745_report_snapshots.sql` | `2565c1c2ca71a485f994b84ebf867656dac01f77f9bfb025db8808ccba596dd9` | applied+verified（2026-09-25 R67 audit-rewrite: report_snapshots schema 收敛，changelog 此前记录的旧 SHA 4e6a1343… 为改写前内容；改写后实际盘上内容 SHA = 2565c1c…；后续 746/747 在 P5 报告切板时基于此基线追加） |
| 746 | `746_report_snapshots_internal_dims.sql` | `df79fde17bcd39178dd37ca4efa7ec2f0957044fa8ccff53fdfe93e6ae66b2b6` | applied+verified |
| 747 | `747_session_mirror_outbox_source_claim.sql` | `9c292c72eebe85eaf223762aba4401b39cc0ac88c716c1a719aff3d99792b7b6` | applied+verified |
| 800 | `800_provider_endpoint_protocols.sql` | `b88fa96d87d10dd5bb3829a000ebbf999d6a4d210516bfe38b8a01ca95c8ba30` | applied+verified |

