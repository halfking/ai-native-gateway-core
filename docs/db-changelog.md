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
| 542 | `542_request_logs_token_band.sql` | `2ed9d3e8c5b555774a6bb97757c8be73fa32e45a420c8118af036e387b2d079b` | applied+verified |

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
## 2026-08-23T18:01:08Z — deploy 154 build_seq 1693 (7b51da98)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 570 | `570_model_offers_insert_priority_passthrough.sql` | `bf1b1b335600158cca88be0a9bcf1c3391b43832fd64d4e0c67116cc579478ee` | applied+verified |

## 2026-08-23T20:05:11Z — deploy 245 build_seq 1696 (47add732)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 572 | `572_session_summary_large_token_ratio.sql` | `e60df0b0adc24665da72273a8787d2cb5bec98c3d504da0a6abf0049db4fe56d` | applied+verified |
## 2026-08-24T19:27:11Z — deploy 154 build_seq 1736 (d644a267)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 573 | `573_drop_request_logs_body_columns.sql` | `f2bfbe03bf3e838b799a4d5e55a9ee0f8a759a44278c3491b09c7897cf335e9c` | applied+verified |
## 2026-08-25T06:09:32Z — deploy 154 build_seq 1747 (aa42f889)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 601 | `601_request_logs_bodies_drop_metadata.sql` | `17adda684d907c32a8a54842d6256eb29f769ce9c4a338da6987b2cab491120f` | applied+verified |## 2026-08-25T16:05:05Z — deploy 245 build_seq 1755 (08566ce9)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 605 | `605_fix_tool_calls_index_predicate.sql` | `a4dc10884a165173fe5ea254724eb86deb01699efebb9256e43376733f049aff` | applied+verified |

## 2026-08-25T19:48:03Z — deploy 154 build_seq 1758 (fe1e870e)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 606 | `606_session_summaries_agent_expert_tags.sql` | `e49a553121f23af735ccfdb56e549425c94428e1e8f105563bb2a8007d14de17` | applied+verified |

## 2026-08-25T21:16:30Z — deploy 154 build_seq 1761 (d24dab5e)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 580 | `580_session_module_executions_hot_promote.sql` | `1a517b3426c451cec08b4c0ab30a086f308f360ba9066fc78290e1c2112ef7c4` | applied+verified |

