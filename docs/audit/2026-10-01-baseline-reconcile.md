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

## 对 17 个 MISSING 的逐条取证（round 43，含自我更正）

不能把「17 个 MISSING」整体当作「源库已删除，可以安全丢弃」。逐条查证后分三类。

### ⚠️ 自我更正：上一版把 B 类写成「来源不可复现」，是错的

上一版报告称这 6 个对象「既不在源库，也无法由本仓任何迁移重建」。**该结论错误**——
当时只 grep 了 `sql/migrations/`，漏掉了 `sql/objects/`。本仓存在一个 **1910 个文件**的
对象登记目录：

```
sql/objects/{tables,indexes,constraints,functions,views,policies,triggers,sequences,other}/
```

这 6 个对象**全部有仓内出处**，见下表。修正后的分类如下。

### A 类：月度分区及其附属对象（11 个）— 预期消失，不是缺陷

`request_logs_bodies_2026_07/_2026_08`（TABLE ×2 / TABLE ATTACH ×2 / CONSTRAINT ×2）
与 `session_turns_2026_07/_2026_08` 的 INDEX ATTACH ×4。

源库上这些月份分区已被保留策略清掉（实测源库仍有 119 个 `session_turns_2026_*`
分区，只是没有 07/08）。全新安装**不应**重建过期的月份分区——由分区管理器按需创建。

### B 类：真实存在、出处已查明（5 个）— 上一版「来源不可复现」不成立

| 对象 | 出处 | 创建迁移是否在 installer 启动集 |
|---|---|---|
| `idx_stage_events_stage_status` | 迁移 `434_request_stage_events_table.sql:63` | **否**（低于 511 下限） |
| `idx_stage_events_tenant_ts` | 迁移 `450_request_stage_events_tenant.sql:13` | **否**（低于 511 下限） |
| `idx_stage_events_request_id` | 仅 `sql/objects/indexes/idx_stage_events_request_id.sql` | 无迁移 |
| `handoff_logs_pkey` | 仅 `sql/objects/constraints/handoff_logs_handoff_logs_pkey.sql` | 无迁移 |
| `route_incident_events_incident_id_fkey` | 仅 `sql/objects/other/route_incident_events_…_fkey.sql` | 无迁移 |

（第 6 个 `instance_heartbeats_pkey` 经查在源库**仍然存在**（同名约束 2 个），
是比对键的构造差异，不是真丢失，已移出本类。）

### C 类：真正的新发现 —— **local 开发库是一个「部分迁移」的库，不能当 dump 源**

上一版把风险归到「对象可能是有意废弃」，方向判反了。实测：

- 迁移 434 一次性创建 4 个索引（`idx_stage_events_ts` / `_stage_status` /
  `_upstream_failure` / `_redis_miss`）。源库上**有 3 个、没有 `_stage_status`**。
- 源库另有 `idx_stage_events_created_at`（非 434 所建）。
- 源库 `handoff_logs` **完全没有主键**（`contype='p'` 为空），而已提交基线有。

⇒ 源库既不是「更新版」也不是「干净版」，而是一个**迁移到一半的中间态**。
用它重新生成基线，会把上面 5 个合法对象**静默删掉**；其中 3 个在
`sql/migrations/` 里根本无创建者，全仓只有 `sql/objects/` 那份定义。

**修订后的硬约束：本地开发库不可作为基线 dump 源。** 正确顺序是

1. 先把源库补齐到与已提交基线对等：应用迁移 `434` / `450`，并 apply
   `sql/objects/` 里那 3 份缺失定义（`handoff_logs` 加主键前需先查
   NULL/重复值，实测源库该表当前无主键，加约束前必须确认数据干净）；
2. 再重新生成，MISSING 应降到只剩 A 类 11 个分区；
3. 之后才谈替换基线，并重跑 `TestBaselineDefinitionPrecedesEagerReference`
   —— 上一轮那 6 处前向引用的人工排序修复是钉在**旧基线**上的。

## 结论

- 生成器已可用且可复现：新基线 apply 到空库 **exit=0 / 0 错误 / 660 relations /
  2491 indexes / 211 policies**，与源库对象数一致。
- 新基线覆盖 3759 个已提交基线缺失的对象（含本轮实测的
  `candidate_failure_logs_hot`、`session_turns_hot`、`session_dim`、`model_offers`、
  `proxy_subscriptions`），方向上是对的。
- 但**不能直接替换**：local 开发库经查是「迁移到一半」的中间态（见 C 类），
  不可作为 dump 源。须先按上述步骤把源库补齐，再重新生成、确认 MISSING 只剩
  A 类 11 个分区，然后才谈替换。
- 2595 个 REORDERED 属预期（新增文件头 + 774 条 `COMMENT ON` 后置导致整体行号位移），
  且新基线已通过真库 apply 证明顺序合法。

---

## 附：StartupFiles 两处「编号倒挂」专项复核（round 43 结案）

上一轮遗留「`560` 在 `655` 后、`704` 在 `713` 后，待专项复核」。**结论：两处都不是缺陷。**

判据不是编号，而是依赖 + 真库行为。注意 `apply` 顺序跟随依赖而非编号
（`801` 被刻意置末），因此**不得**用「StartupFiles 按编号升序」当门
—— 上一轮已因自造这条不成立的不变式报红并删除。

### `655` → `560`：是**创建在前、校验在后**，顺序正确

| 迁移 | 实际动作（剔除注释后） |
|---|---|
| `655_session_summaries_schema_reconcile` | **创建** `session_summaries_session_key_uidx`（:152）与 `UNIQUE (tenant_id, session_key)`（:169） |
| `560_session_summaries_tenant_uniqueness` | **只校验**：查约束是否存在、查跨租户 `session_key` 冲突并 `RAISE WARNING`，**不创建任何对象** |

且 `560` 自己的正文写着
`RAISE NOTICE 'migration 560: session_summaries is absent; 655 or the base schema must run first'`
—— 即它**自述要求 655 先跑**。故 `655` 在 `560` 之前是**刻意的**。

若顺序反过来，560 不会失败（它用 `to_regclass(...) IS NULL` 优雅降级），
只会发出误导性的 NOTICE，校验语义落空。因此这是**软依赖**，不值得设硬门。

### `704` → `713`：两者操作**不同的表**，无任何依赖

- `704_plan_quota_probe_backoff`：`ALTER TABLE public.credentials`
- `713_session_turns_cost_precision`：`session_turns` / `session_turns_hot` 的列与视图

真库佐证：fresh-install 逐文件 apply 的失败日志里，**`560`、`655`、`704` 均未失败**；
`713` 确实失败，但原因是 `relation "public.session_turns_hot" does not exist`
（`526` 低于 511 下限那族），**与 `704` 的位置无关**。

⇒ 两处编号倒挂均无需改动，也**不新增门**。把它们写进本文件，
是为了让下一轮不必重复排查。

---

## 附：`scripts/audit/fresh-schema-from-migrations.sh` 重写（round 43，目标项 4）

原脚本有三处使它**作为审计工具不可信**的缺陷，全部是**跑出来的**、不是读出来的：

| # | 缺陷 | 后果 |
|---|---|---|
| 1 | 容器/用户硬编码 `kx-citus` / `kxuser` | 本机是 `llm-gateway-pg` / `llm_gateway` ⇒ 脚本在本机**根本无法运行** |
| 2 | **无「库确实被填充」断言** | `CREATE DATABASE` 静默失败 ⇒ `applied=0 failed=0`，仍打印 `complete` |
| 3 | 文件选择正则要求「数字前缀后紧跟下划线」 | 只匹配到 **464/777**，静默漏掉 `2026-07-13-multimodal-token-fields-hot.sql` 等全部日期前缀迁移 |

另有 2 处是我重写时**自己引入又当场抓到**的：

- `10#$n` 在 `328a` 上是**硬算术错误**，会中途终止循环，而脚本仍报告 `complete`
  → 改为正则提取纯数字前缀；并加「循环计数必须等于文件总数，否则判为中途夭折」的自检。
- `apply_file` 写 `2>"$label.err"`（**相对路径**）而调用点读 `/tmp/$label.err`
  ⇒ 两边永不相等，**每条失败原因都是空的**，且 `mig.err` / `prereqs.err`
  被生成在**仓库根目录**（已被本轮删除）。→ 统一到单一绝对路径 `ERRFILE`。

重写后全量（465 条 up 迁移）实测，与本轮早先的独立测量**完全一致**：

```
applied=229 skipped=0 failed=236
[populated] relations=233 functions=455
tables=186  partitioned=14  views=33  functions=455  policies=89
```

> 口径更正：本目录共 **777** 个 `.sql`，其中 **312** 个是 `.down.sql`，
> 实际 apply 的是 **465** 条 up 迁移。上一版文档写的「全部 777 条迁移」不准确，
> 结论（236 处失败）不变。

新增守卫 `sql/schema/audit_script_test.go`（4 项，均经变异验证）：无硬编码容器/用户、
`db_populated` 必须可失败且 `CREATE DATABASE` 失败必须致命、
选择器不得要求「数字后紧跟下划线」、`.err` 不得落在仓库根。
其中「`.err` 落仓库根」那条守卫**首次运行是假阳性**——脚本正文注释里记录了这个 bug 本身，
裸文本搜索命中了注释。已按既有纪律改为**剥注释后匹配活跃行**。
