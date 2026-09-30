# 基线逐对象对账报告（round 43）

生成方式：`scripts/audit/baseline-reconcile.py`，按 pg_dump 的
`-- Name: X; Type: Y; Schema:` 横幅逐对象比对。

| 项 | 值 |
|---|---|
| 已提交基线 | `sql/schema/01-schema.sql` |
| 新生成基线 | `/tmp/r43/baseline/01-schema.sql` |
| 已提交对象数 | 2612 |
| 新生成对象数 | 6354 |
| **ADDED**（仅新生成有） | **3759** |
| **MISSING**（仅已提交有） | **17** |
| REORDERED（两边都有但位置不同） | 2595 |
| SHARED（两边都有且行号相同） | 0 |

## MISSING — 已提交基线有、新生成没有（17）

**这是唯一有语义风险的一类**：对象在新基线里消失，说明源库上它已被重命名或删除。若确认是源库真实状态，替换基线前需确认无代码/迁移仍依赖它。

| Type | Name | 行号 |
|---|---|---|
| TABLE | `request_logs_bodies_2026_07` | 13379 |
| TABLE | `request_logs_bodies_2026_08` | 13393 |
| VIEW | `request_logs_bodies_progress` | 13437 |
| TABLE ATTACH | `request_logs_bodies_2026_07` | 19539 |
| TABLE ATTACH | `request_logs_bodies_2026_08` | 19546 |
| CONSTRAINT | `handoff_logs handoff_logs_pkey` | 21041 |
| CONSTRAINT | `instance_heartbeats instance_heartbeats_pkey` | 21057 |
| CONSTRAINT | `request_logs_bodies_2026_07 request_logs_bodies_2026_07_pkey` | 21809 |
| CONSTRAINT | `request_logs_bodies_2026_08 request_logs_bodies_2026_08_pkey` | 21817 |
| INDEX | `idx_stage_events_request_id` | 25889 |
| INDEX | `idx_stage_events_stage_status` | 25896 |
| INDEX | `idx_stage_events_tenant_ts` | 25903 |
| INDEX ATTACH | `session_turns_2026_07_tenant_request_partition_key` | 28455 |
| INDEX ATTACH | `session_turns_2026_07_tenant_session_turn_partition_key` | 28469 |
| INDEX ATTACH | `session_turns_2026_08_tenant_request_partition_key` | 28497 |
| INDEX ATTACH | `session_turns_2026_08_tenant_session_turn_partition_key` | 28511 |
| FK CONSTRAINT | `route_incident_events route_incident_events_incident_id_fkey` | 29339 |

## ADDED — 新生成基线有、已提交没有（3759）

对应本轮实测的缺口：新基线含 `candidate_failure_logs_hot`、`session_turns_hot`、`session_dim`、`model_offers`、`proxy_subscriptions` 等已提交基线缺失的对象。

| Type | Name | 行号 |
|---|---|---|
| FUNCTION | `archive_dashboard_events(integer)` | 385 |
| FUNCTION | `archive_request_logs_default(integer)` | 483 |
| COMMENT | `FUNCTION archive_request_logs_default(p_retention_days integer)` | 651 |
| FUNCTION | `archive_session_module_executions(integer)` | 821 |
| COMMENT | `PROCEDURE backfill_request_logs_bodies(IN p_batch integer)` | 990 |
| FUNCTION | `backfill_session_v2_turns(text, text, integer)` | 999 |
| COMMENT | `FUNCTION backfill_session_v2_turns(p_tenant_id text, p_session_id text, p_batch_size integer)` | 1066 |
| FUNCTION | `bump_candidate_binding_scope_revision_canonical_delete()` | 1075 |
| COMMENT | `FUNCTION bump_candidate_binding_scope_revision_canonical_delete()` | 1131 |
| FUNCTION | `bump_candidate_binding_scope_revision_canonical_insert()` | 1137 |
| COMMENT | `FUNCTION bump_candidate_binding_scope_revision_canonical_insert()` | 1198 |
| FUNCTION | `bump_candidate_binding_scope_revision_canonical_pm_update()` | 1204 |
| FUNCTION | `bump_candidate_binding_scope_revision_canonical_update()` | 1256 |
| COMMENT | `FUNCTION bump_candidate_binding_scope_revision_canonical_update()` | 1349 |
| FUNCTION | `bump_candidate_binding_scope_revision_delete()` | 1355 |
| COMMENT | `FUNCTION bump_candidate_binding_scope_revision_delete()` | 1410 |
| FUNCTION | `bump_candidate_binding_scope_revision_insert()` | 1416 |
| COMMENT | `FUNCTION bump_candidate_binding_scope_revision_insert()` | 1471 |
| FUNCTION | `bump_candidate_binding_scope_revision_update()` | 1477 |
| COMMENT | `FUNCTION bump_candidate_binding_scope_revision_update()` | 1560 |
| FUNCTION | `calculate_annotation_agreement()` | 1592 |
| COMMENT | `FUNCTION calculate_annotation_agreement()` | 1627 |
| FUNCTION | `cast_text_to_hour_timestamp(text)` | 1676 |
| COMMENT | `FUNCTION cast_text_to_hour_timestamp(input_text text)` | 1693 |
| FUNCTION | `cleanup_expired_session_turn_logs()` | 1743 |

… 另有 3734 个未列出

## REORDERED — 位置不同（2595）

位置变化本身不必然是缺陷；关键是重排后 `LANGUAGE sql` 函数体与 `COMMENT ON` 仍满足校验顺序（新生成器已把自包含单行 `COMMENT ON` 后置）。

| Type | Name | 行号 |
|---|---|---|
| COMMENT | `SCHEMA public` | 29 |
| TYPE | `injection_action` | 35 |
| COMMENT | `TYPE injection_action` | 54 |
| TYPE | `injection_category` | 60 |
| COMMENT | `TYPE injection_category` | 83 |
| PROCEDURE | `_064_convert_partition_to_heap(text)` | 89 |
| FUNCTION | `analyze_llm_gateway_table_stats(integer)` | 172 |
| COMMENT | `FUNCTION analyze_llm_gateway_table_stats(p_recent_months integer)` | 208 |
| FUNCTION | `apply_llm_gateway_autovacuum_settings()` | 216 |
| COMMENT | `FUNCTION apply_llm_gateway_autovacuum_settings()` | 271 |
| FUNCTION | `archive_credential_model_index(date)` | 279 |
| COMMENT | `FUNCTION archive_credential_model_index(archive_month date)` | 374 |
| FUNCTION | `archive_request_logs(date)` | 409 |
| COMMENT | `FUNCTION archive_request_logs(archive_month date)` | 467 |
| FUNCTION | `archive_request_wal(date)` | 657 |
| COMMENT | `FUNCTION archive_request_wal(archive_month date)` | 740 |
| FUNCTION | `archive_routing_decision_log(date)` | 752 |
| COMMENT | `FUNCTION archive_routing_decision_log(archive_month date)` | 810 |
| FUNCTION | `array_unique_append(text[], text)` | 846 |
| FUNCTION | `auto_rotate_to_columnar(boolean, integer)` | 867 |
| FUNCTION | `auto_set_fp_slot_limit()` | 935 |
| PROCEDURE | `backfill_request_logs_bodies(integer)` | 956 |
| FUNCTION | `calculate_request_cost(character varying, integer, integer, integer, integer)` | 1633 |
| FUNCTION | `check_credential_dates()` | 1699 |
| FUNCTION | `cleanup_expired_session_requests()` | 1717 |

… 另有 2570 个未列出

---

## 对 17 个 MISSING 的逐条取证（round 43 实测）

不能把「17 个 MISSING」整体当作「源库已删除，可以安全丢弃」。逐条查证后分成两类：

### A. 月度分区与其附属对象（11 个）— 预期消失，不是缺陷

`request_logs_bodies_2026_07/_2026_08`（TABLE ×2 / TABLE ATTACH ×2 / CONSTRAINT ×2）
与 `session_turns_2026_07/_2026_08` 的 INDEX ATTACH ×4。

源库上这些月份分区已被保留策略清掉（实测源库仍有 119 个 `session_turns_2026_*`
分区，只是没有 07/08）。全新安装**不应**重建过期的月份分区——由分区管理器按需创建。
⇒ 这 11 个的缺失是**正确**的。

### B. 既不在源库、也无任何迁移能重建（6 个）— 真正的存量疑点

| 对象 | 源库 | 仓库内创建者 |
|---|---|---|
| `idx_stage_events_request_id` | 无 | **无**（`grep -rl` 无命中） |
| `idx_stage_events_stage_status` | 无 | **无** |
| `idx_stage_events_tenant_ts` | 无 | **无** |
| `handoff_logs_pkey` | 无 | 无命中 |
| `route_incident_events_incident_id_fkey` | 无 | 无命中 |
| `instance_heartbeats_pkey` | 有同名约束 2 个 | — |

这 6 个只存在于**已提交的 pg_dump 基线**里：既不在当前源库，也无法由本仓任何迁移重建。
它们属于「来源不可复现」的存量——可能是更早的手工变更被 dump 固化后，源库侧已被
回退/重命名。

**对「用生成基线替换已提交基线」的直接约束**：
在替换前必须逐个确认这 6 个对象是「已被有意废弃」还是「源库侧发生漂移」，
否则替换会静默丢失线上仍在使用的索引与外键。当前源库是 local 开发库，
**不能假定它与生产等价**；因此本轮只出对账报告，不做替换。

## 结论

- 生成器已可用且可复现：新基线 apply 到空库 **exit=0 / 0 错误 / 660 relations /
  2491 indexes / 211 policies**，与源库对象数一致。
- 新基线覆盖 3759 个已提交基线缺失的对象（含本轮实测的
  `candidate_failure_logs_hot`、`session_turns_hot`、`session_dim`、`model_offers`、
  `proxy_subscriptions`），方向上是对的。
- 但**不能直接替换**：先清掉上面 B 类 6 个对象的来源疑点，并确认 dump 源库
  （当前为 local 开发库）与生产等价。
- 2595 个 REORDERED 属预期（新增文件头 + 774 条 `COMMENT ON` 后置导致整体行号位移），
  且新基线已通过真库 apply 证明顺序合法。
