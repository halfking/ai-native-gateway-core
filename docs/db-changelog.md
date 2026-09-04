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
| 649 | `649_routing_analytics_probe_filter.sql` | `a5b9fcb8b49e4c4fdff8e670bdf46152587fe3ce20ad01332f5f8b669cb669de` | applied+verified |
| 650 | `650_auto_route_selection_treatment_attribution.sql` | `1336af5e613ac12e909e6a2ac66de58cdbc4237fc5657786a2e61dbbe21af976` | applied+verified |
| 651 | `651_provider_quality_hot_rollup.sql` | `01450ef91e3d912851921c089d7b87cff6ec7cd689983e563c5d8b33dd7134a0` | applied+verified |
| 652 | `652_system_monitor_fallback_queue.sql` | `aa5a942707a2e22658d3a5bb3dd245de402f9a374f1ee7efe162a85a2434cf30` | applied+verified |
| 653 | `653_archive_credential_model_index_canonical_return.sql` | `48a9a8c1f16a941da4321e24d7189c802f821086e6683b6a419271a59432a479` | applied+verified |
| 654 | `654_archive_credential_model_index_detach_drop.sql` | `ef5914f20ee53600ca7d2da0c56275133f606c73fabebb42183897028cff1e8b` | applied+verified |

