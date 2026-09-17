# R41 首位任务 — RLS Phase 2 前置：73 张 FORCE 表逐表路径审计（2026-09-18）

- 性质：设计专项（docs/design/rls-tenant-isolation-architecture.md §五 Phase 2 前置第 1 项），非 48h 滚动窗口轮。本轮只审计不动手修复——输出即 Phase 2 降权门槛的判定依据
- 起点（重算）：HEAD=b75c91900，最新迁移 723，R40 已关（RLS Phase 1 四项收口）；本任务为 R41 重算后首位，A 任务（252 维护窗口）仍待 ops 确认，不在本轮
- 方法：真库枚举（`pg_class.relforcerowsecurity`，实测 73 张与 R38 记载一致）→ policy 全量导出（pg_policies 逐条 qual/with_check）→ 全仓 SQL 上下文命中提取（含 `public.` 前缀，首轮正则漏检 sessionv2mirror 系已返工）→ 5 路只读子代理分组逐表四路径审计 → **全部 P1 承重结论主代理亲读复核**（policy 原文、代码点位、视图 reloptions、函数 prosecdef 均二次实证）→ R40 census 三测活库复跑 PASS
- 判定口径：✅ 覆盖（显式事务内设了 policy 所需 GUC）｜⚠️ 潜伏（依赖 get_current_tenant() 'default' 回退或 NULL 分支，当前数据态下降权无症状，多租户数据一入即触发）｜❌ 不通过（降权后必然断流 / 42501 / 静默失效，与数据无关或数据已存在）｜— 该路径对此表不存在。四路径 = admin 读 / 网关写 / 后台 worker / 回填-维护（设计 §三.3 口径）；ensure 链 DDL 不受 RLS 约束（设计 D2），不计入

## 一、总判定

**Phase 2 降权门槛未达成**。73 张 = 18 张本仓业务表 + 54 张外部产品表（A 类，网关零运行时路径）+ 1 张本仓 legacy 表（B 类）。

- 业务表 18 张：**通过 5、条件通过 8、不通过 5**。P1 修复项 6 个（§三），其中 3 个为降权即触发的无条件失效
- 数据面现状：本机 request_logs 401,624 行、supplier_errors_hot/candidate_failure_logs_hot/supplier_errors 全部仅 'default' 租户——多数 ❌ 的断流形态被单租户数据态掩盖为潜伏；但 provider_error_details（policy 无回退）、tenant_model_policies（已存 hansi/e2e-policy-a 行 + checker fail-open）两族**在当前数据下降权即出症状**
- 55 张外部表：本仓唯一引用是 720 policy 重写（裸子串 grep + ensure 链逐表核验为零），FORCE 对网关降权无影响；**真正风险在共享库 co-tenant 侧**（§四）：非 default 租户存量 55 行分布 10 张表，若属主产品经 llm_gateway 角色连接，降权后这些行对其不可见——须在 252 降权窗口前核实各产品连接身份（本机实测仅 llm_gateway（superuser）与 platform_app（普通角色，只活动于 fencing/integration/workflow/authagent 等**非 public schema**）两个登录角色在连）

## 二、73 表总表（每表一行 × 四路径 GUC 状态）

### 2.1 本仓业务表（18 张）

| # | 表 | admin读 | 网关写 | worker | 回填-维护 | 判定 | 关键证据 |
|---|---|---|---|---|---|---|---|
| 1 | request_logs | ❌ | —¹ | ❌ | ❌ | **不通过** | policy 仅 `tenant_id=get_current_tenant()`，**无 super_admin/bypass 旁路分支**（pg_policies 亲核）→ withAllTenantTx 双 GUC 对本表无效；admin 约 25 处裸池读（usage/analytics/request_trace/logs/data_lifecycle 系，session_list.go:83 与 session_analytics 租户态为 ✅ 范本）；worker 裸读（credential_recovery/discovery/MV refresher/maas 账单等）；promote 主通道（partition_manager.go:1089 只设 bypass_rls，对本表无效）+ 函数非 SECURITY DEFINER |
| 2 | supplier_errors_hot | ✅ | ✅ | ✅ | ⚠️ | 条件通过 | admin errors_trend/vendor handler 走 bypass 只读事务；网关写 execWithRLSBypass（supplier_error_logger.go:120-138）；aggregator bypass tx；⚠️=promote 三处裸 autocommit 调用点（data_lifecycle_cron.go:252、data_lifecycle_hot_partition.go:388/:645），非 default 行滞留 hot |
| 3 | supplier_errors | ✅ | — | ✅ | ⚠️ | 条件通过 | 同上族；promote 函数 INSERT 父表在 bypass tx 内 ✅；裸调用点同 #2 |
| 4 | supplier_error_stats | ✅ | — | ✅ | ✅ | **通过** | policy cmd=ALL USING(true) 恒真（表无 tenant_id 列，刻意设计，亲核）；cleanup 裸 DELETE 亦恒放行 |
| 5 | provider_error_details | ❌ | — | ✅ | ❌ | **不通过** | **policy 无 'default' 回退**（fail-closed：role/bypass 分支 + 裸 current_setting，亲核）；admin/provider_credential.go:1254-1278 裸池读 → 降权后供应商错误面板恒空；bg/partition_manager.go:1314 裸 DELETE → TTL 清理静默死亡；worker 聚合器双 GUC tx ✅；存量已有 'chenb' 非 default 行 1 条 |
| 6 | candidate_failure_logs_hot | ✅ | ❌ | ⚠️ | ⚠️ | **不通过** | 网关写 candidate_failure_logger.go:160-165 **裸 pool INSERT**（同文件族 supplier 投影走 execWithRLSBypass，唯此表裸写——非 default 租户请求 42501 且被 warn 吞掉=失败账本静默丢失）；worker 裸读三链（candidate_failure_monitor/model_probe/daily_probe_audit 经 definer 视图，FORCE 底表不豁免）；promote 裸调用点同 #2 |
| 7 | settings_audit | ⚠️ | ⚠️ | — | ⚠️ | 条件通过 | 真库 policy 实为 `(tenant_id=get_current_tenant()) OR (tenant_id IS NULL)`（亲核，R38 表述修正）；写路径（settings/audit.go:39-45 裸 Exec）全靠 NULL 分支存活——平台 PUT 写 NULL、provider 链写 'default'；非 default 行读不到也清不掉（cleanup 裸 DELETE）；policy 若改纯等式形态即丢审计 |
| 8 | tenant_settings_kv | ⚠️ | ⚠️ | ⚠️ | — | 条件通过 | settings/store_db.go 全部 6 方法裸池（读回退 default/非 default 写 42501 或 0 行静默）；网关热路径 goal_retry_policy/goal_control 每请求读无 GUC；当前真库 0 行 → 潜伏 |
| 9 | tenant_model_policies | ❌ | ✅ | ❌ | — | **不通过（本轮最重）** | 写侧 ✅（setPolicyTxGUCs 显式事务，42501 事故后已修）；读侧 ❌×2：admin/model_policies.go:191 list 裸读（真库已存 hansi×2/e2e-policy-a×1 行 → 降权后管理端对这两租户恒空）；**internal/modelpolicy/checker.go:265 `SET LOCAL row_security=off` 在 FORCE+owner+NOSUPERUSER 下按 PG 语义报错**（reload 每 30s 失败 → IsForbidden fail-open → 非 default 租户模型 denylist 整体静默失效）。`row_security=off` 精确行为（error vs 忽略）待 staging 降权演练矩阵定案 |
| 10 | tenant_model_policies_audit | ❌ | ✅ | — | — | 不通过 | 读 admin/model_policies.go:594 裸读 → hansi/e2e 共 20 行审计降权后不可见；写侧经触发器随父事务（✅，audit policy 的 NULL 分支为死分支） |
| 11 | tenant_tool_policies | ⚠️ | ⚠️ | — | — | 条件通过 | 三端点裸池（create/list/delete，tool_policy_api.go:94/144/221）；附带发现：本表当前"只写不读"摆设（registry cache 恒空，无消费方）；真库 0 行 → 潜伏 |
| 12 | approval_queue | ✅ | ✅ | ⚠️ | — | 条件通过 | 读/写/认领/resume/notifier 全链 GUC 纪律最好（beginTenantTx 系，super 走 role 分支）；❌仅一处：MarkTimeout（approval_manager.go:400 裸 Exec，:375 与 worker :8 注释谎称已在 bypass 上下文）→ 非 default 租户 pending 永不超时（当前 49 行全 default，潜伏）；修法现成（role 分支 policy） |
| 13 | session_audit_records | ⚠️ | —² | — | ⚠️ | 条件通过 | policy 仅 role 分支无 bypass_rls 分支（单分支形态，亲核）；super 跨租户读三态失败（list/export 空参 500、stats 吞错全 0、get 恒 'default' 404）——**经核为既有 Go 缺陷非降权新增**（withTenantTx("") 在 superuser 期同样失败），修法=withAllTenantReadOnlyTx（role 分支即满足）；本仓无生产写入方；真库 0 行 |
| 14 | session_mirror_outbox | — | ✅ | ✅ | ❌³ | **通过** | outbox.go:98-123 显式事务+双 GUC；replay 全部经 execBypass 显式事务（R40 autocommit 教训落实，亲核）；³=mirror_outbox_backfill.sql 无 SET bypass（**D3 反例**），一次性已跑完，重跑会炸——登记为规范反面样本 |
| 15 | session_aggregate_outbox | — | ⚠️ | ⚠️ | — | 条件通过 | claim/trim ✅（双 GUC/role 分支 tx）；markDone/markDead/scheduleRetry 三条裸 autocommit UPDATE（reaper.go:392/:407/:446 亲核）→ 非 default 行滞留 claimed 无限热循环（当前 18 万行全 default，潜伏）；网关写侧 Enqueue 共享 turn 写事务无 GUC（session_writer_v2.go:339，潜伏）——修法=durable/rls.go execWithBypassTx 同包样板 |
| 16 | journal_snapshot_receipts | — | ✅ | ✅ | — | **通过** | fail-closed 租户分支 + 旁路分支双 policy；全部读写路径显式事务走 bypass 分支（journal_snapshot_receipt.go:120-135/217-230、retention.go:126-146 亲核）——fail-closed 形同虚设但无害 |
| 17 | request_journey_observation_outbox | — | ✅ | ✅ | — | **通过** | 全路径显式事务+bypass（observation_outbox.go 全链），R37 记载属实；当前 0 行 |
| 18 | credential_client_quota | — | —（休眠） | — | — | **通过（休眠）** | ensure 已 parked（B1）、真库 0 行、NewPGSource 无生产接线；policy 列为 owner_tenant_id（亲核）；un-park 时必须补 GUC 事务（postgres.go:75 裸读） |

¹ request_logs 网关 DML 全落 request_logs_hot（**无 RLS 无 policy**，pg_class 亲核）→ 本路径实际不受 FORCE 影响，判 —；但请求路径存在父表裸读 4 处（lookupTurnNumber/L3 回退/替代模型/goal history），归入 #1 admin 读同族风险。² 网关写 = 本仓无生产写入方。³ 一次性脚本已执行完毕，非常驻路径。

### 2.2 外部产品表（54 张 A 类 + 1 张 B 类）——网关四路径全部不存在（—）

| 表 | A/B | 非 default 行 | 表 | A/B | 非 default 行 |
|---|---|---|---|---|---|
| auth_api_key_requests | A | 0 | memory_candidate_events | A | 8 (acc-selfcheck) |
| auth_apikey_applications | A | 0 | memory_candidates | A | 4 (acc-selfcheck) |
| auth_sessions | A | 0 | memory_conversation_links | A | 0 |
| auth_users | A | 0 | memory_ingest_receipts | A | 8 (acc/console) |
| chat_messages | A | 0 | openclaw_events | A | 0 |
| chat_sessions | A | 0 | openclaw_feedback | A | 0 |
| context_manifest_entries | A | 4 (acc-selfcheck) | openclaw_handoffs | A | 0 |
| context_manifests | A | 2 (acc-selfcheck) | openclaw_prompt_ledger | A | 0 |
| conversation_history | A | 0 | openclaw_shared_thread | A | 0 |
| doc_tools_tasks | A | 0 | project_dreams | A | 0 |
| doc_tools_uploads | A | 0 | project_identity_audit | A | 0 |
| document_chunks | A | 0（全表 2 行 default） | project_insights | A | 0 |
| document_feedback | A | 0 | project_jobs | A | 0 |
| document_links | A | 0 | project_observations | A | 2 (acc/console) |
| document_retrieval_events | A | 0 | project_states | A | 2 (acc/console) |
| documents | A | 0（全表 5 行 default） | projects | A | 2 (acc/console) |
| graph_entities | A | 0 | session_compressions | A | 0（10 行 default） |
| graph_episodes | A | 0 | session_memory_summaries | A | 0（10 行 default） |
| graph_facts | A | 0 | source_assets | A | 0 |
| knowledge | A | 0 | user_profiles | A | 0 |
| knowledge_annotations | A | 0 | wiki_page_versions | A | 0 |
| knowledge_base_acl | A | 0 | wiki_pages | A | 0 |
| knowledge_bases | A | **5 (redclaw-local)** | wiki_proposals | A | 0 |
| knowledge_entities | A | 0 | wiki_sections | A | 0（policy get_current_tenant 形态亲核） |
| knowledge_lineages | A | 0 | **tool_usage_stats_old** | **B** | 0 |
| knowledge_metadata | A | 0（9 行 tenant_id **全 NULL**，数据质量异常） | knowledge_relations | A | 0 |
| knowledge_versions | A | 0 | memora_session_summaries_orphan | A | 0 |
| memory | A | **18** (acc 6/console 12；default 10032) | 〈A 类 54 张+本仓 legacy 1 张至此列毕〉 | | |

A 类=外部产品表（本仓+ensure 链均零引用，裸子串 grep/DML 正则/前缀拼接三重防漏核验）；B 类=本仓 legacy（tool_usage_stats_old：本仓声明面建表、迁移 335 SELECT 迁数后 DROP，fresh install 先建后删，0 行 0 引用，Phase 4 声明面清理时一并处理）。

## 三、P1 修复清单（降权门槛缺口，本轮只登记不修复）

| # | 发现 | 证据 | 降权后果 | 修复候选 |
|---|---|---|---|---|
| P1-1 | request_logs policy 无 super_admin/bypass 旁路分支，全仓既有双 GUC 通道（admin withAllTenantTx、promote tx、worker bypass）对本表全部失效 | pg_policies 亲核 + bg/partition_manager.go:1089（只设 bypass_rls）+ sql/objects/policies/request_logs_tenant_isolation_request_logs.sql | promote 批次遇非 default 行 → WITH CHECK 42501 整批失败、hot 表只进不出；admin 全租户态/worker 父表读静默缩水（当前 40 万行全 default，promote 靠 'default' 回退侥幸存活） | 按 V367/V371 先例补 `request_logs_super_admin_bypass` policy（迁移，真库实跑）；promote/admin/worker 三族即一并收敛 |
| P1-2 | modelpolicy checker `SET LOCAL row_security=off` 前提（非 FORCE/超管）在降权后不成立 | internal/modelpolicy/checker.go:265（亲核）+ :202 ReloadAll 裸读 + :338 ticker | FORCE+owner+NOSUPERUSER 下按 PG 语义 reload 报错（精确行为待 staging 演练定案），缓存停更 → IsForbidden fail-open，**hansi/e2e-policy-a 的模型 denylist 即刻静默失效**（真库已有数据） | reloadTenant 改显式事务 `SET LOCAL app.current_tenant=$tenant`（去 row_security=off）；ReloadAll 逐租户或 bypass；staging 降权演练矩阵验证 |
| P1-3 | provider_error_details admin 读与 TTL 清理裸池，policy 无回退（fail-closed） | admin/provider_credential.go:1254-1278、bg/partition_manager.go:1314（子代理实证，主代理亲核 policy 形态） | 错误统计面板恒空（静默）；TTL DELETE 恒 0 行 → 表无界增长 | 两处包显式事务 + bypass（对齐 vendor_credential_error_handlers.go 范式） |
| P1-4 | candidate_failure_logger 裸 INSERT（同族唯一漏网） | domains/streaming/executors/candidate_failure_logger.go:160-165（亲核） | 非 default 租户请求 42501 被 warn 吞掉 → 失败账本断流（当前全 default 潜伏） | 复用同族 execWithRLSBypass（supplier_error_logger.go:120） |
| P1-5 | session_aggregate_outbox 三条裸 autocommit 终态转移 | domains/session/v2/session_aggregate_outbox_reaper.go:392/:407/:446（亲核） | 非 default 行滞留 claimed 无限热循环 + dead/retry 状态机失效（当前全 default 潜伏） | durable/rls.go execWithBypassTx 同包样板包三处；网关写事务（session_writer_v2.go:339）一并补 GUC |
| P1-6 | approval MarkTimeout 裸 Exec，注释与实现不符 | domains/sessionaudit/approval_manager.go:400（亲核）、bg/approval_timeout_worker.go:8-9 | 非 default 租户 pending 永不超时 → 审批闸卡死（当前 49 行全 default 潜伏） | 包事务 SET LOCAL app.current_role='super_admin'（policy role 分支即满足） |

P2（条件通过表的加固项，多租户接入前必须清零）：tenant_settings_kv store 6 方法裸池 + goal 热路径读；model_policies list(:191)/audit(:594) 裸读；tool_policy 三端点；promote 三处裸 autocommit 调用点（cron:252/hot_partition:388/:645）；settings_audit 写依赖 NULL 分支；mirror_outbox_backfill.sql 补 SET bypass（D3 反例修正）；session_audit_records super 三态（既有缺陷，修法 withAllTenantReadOnlyTx）。

P3（登记）：candidate_failure_logs 父表 FORCE=f 且 policy 无旁路分支（opslog_trimmer 依赖 owner 豁免，将来 FORCE 即断——需成文决策）；telemetry lookupTurnNumber 只读父表漏 hot（既有正确性缺陷）；knowledge_metadata 9 行 tenant_id NULL（外部属主数据质量）；session_audit_records/tenant_tool_policies 等"摆设面"。

## 四、共享库 co-tenant 风险（Phase 2 移交登记，非本仓修复面）

1. 55 张外部表 FORCE 对网关降权无影响（网关零路径），但它们与网关同库同 schema，FORCE 语义对**任何非 BYPASSRLS 连接角色**生效。本机登录角色实测：llm_gateway（superuser+BYPASSRLS）、platform_app（普通角色，活动面全部在非 public schema：fencing/integration/workflow/authagent）、memora_runtime（BYPASSRLS，可登录未在连）。
2. **252 降权窗口前必须核实**：是否存在其他产品以 llm_gateway DSN 连接。若有，降权后其未设 GUC 的查询按 get_current_tenant() 回退 'default' 过滤——外部表非 default 存量（redclaw-local×5、selfcheck 系×44、其余见 §2.2）将对其不可见。核实成本一条 SQL（pg_stat_activity usename × query 面）。
3. D1 预案适用：个别外部表若查实被 llm_gateway 身份的 co-tenant 使用且无法先补 GUC，`ALTER TABLE ... NO FORCE` 单表回退，不推迟全局降权。

## 五、数据面实测记录（2026-09-18 本机 llm_gateway@127.0.0.1:5432）

- FORCE 面：73 张（`relforcerowsecurity` 实测，与 R38 记载一致）；全部 owner=llm_gateway；candidate_failure_logs 父表 ENABLE 未 FORCE（relforcerowsecurity=f，亲核）
- 数据分布：request_logs 401,624 行全 'default'；supplier_errors_hot 955 / candidate_failure_logs_hot 955 / supplier_errors（父表 30d）39,690 / provider_error_details 22,489+1('chenb')；session_aggregate_outbox 180,497 / journal_snapshot_receipts 47,870 / session_mirror_outbox 4 全 'default'；approval_queue 49 行全 default(timeout)；tenant_model_policies 3 行非 default（hansi×2/e2e-policy-a×1）+ audit 20 行；settings_audit 7 行全 NULL
- 视图：supplier_errors_unified / candidate_failure_logs_unified = security_invoker=true；candidate_failure_logs_with_current_month / tenant_model_policies_active = definer 语义（FORCE 底表下降权后同样被过滤，pg_class.reloptions 亲核）
- 函数：promote_supplier_errors_hot / promote_candidate_failure_logs_hot / promote_request_logs_hot / promote_request_logs_default_batch 均 prosecdef=f（SECURITY INVOKER）
- 验收复验：`go test ./db/ -run TestRLSPolicyVocabulary`（含 TEST_DB_URL 活库 census）三测 **PASS**（R40 §六 口径持续成立）
- 审计期间本机 PG 两次入 recovery（预存不稳定债，[[llm-gateway-local-db]]），全部核查在可达窗口内完成，结论不受影响

## 六、六件套收尾

1. **结论/根因**：Phase 2 降权门槛未达成——18 业务表中 5 张不通过（P1×6），8 张条件通过（P2 加固），5 张通过；根因集中于三类：policy 词汇旁路分支缺失（request_logs）、fail-closed policy 遇裸池调用点（provider_error_details）、R40 autocommit-is_local 教训在 outbox/approval/candidate 族未推广
2. **改动**：无代码改动（纯审计轮）；唯一产物 = 本文档 + 记忆更新
3. **测试**：R40 census 三测活库复跑 PASS；全部 P1 结论经主代理亲核（policy 原文/代码点位/视图 reloptions/prosecdef/数据分布五类实证）；子代理判定与主代理复核冲突处以真库实测为准（settings_audit NULL 分支、provider_error_details 无回退两处纠正了审计假设）
4. **风险**：降权若在本清单清零前执行——必炸点=provider_error_details 面板/清理、modelpolicy denylist fail-open；静默点=tenant_model_policies 管理读；多租户触发点=P1-1/4/5/6 与 P2 全部；co-tenant 侧待 252 核实（§四）
5. **更新 handoff**：记忆 llm-gateway-go-audit-cycle-progress 已更新（本轮判定 + P1 清单 + 修复轮起点）
6. **下一轮提示词**：「R41 修复轮：按 docs/audit/2026-09-18-r41-force-tables-path-audit.md §三 P1-1→P1-6 逐项修复（每项走迁移/代码真库实跑纪律，P1-2 需先做 staging row_security=off 演练矩阵定案行为），P2 项随批或列遗留；完成后重跑本文档 §二 总表刷新判定，目标=18 业务表全 ✅/⚠️ 归零 ❌，再提 Phase 2 降权窗口（A 任务 252 窗口仍待 ops 确认，与修复轮并行推进）」
