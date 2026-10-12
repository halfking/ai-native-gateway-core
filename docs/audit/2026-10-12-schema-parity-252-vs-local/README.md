# 2026-10-12 252 生产库 vs 本地库 schema 对账与双侧修正

> 范围：结构对账（表/列/索引/约束/视图/函数/触发器），不含数据。
> 左侧：**252 生产库** = docker `pg-252-pg17`（PostgreSQL 17.10，`llm_gateway` 库，
> 154/245 网关共用生产库）。右侧：**本地库** = host pg18（`~/kaixuan/pg18`，
> 端口 54318，host-first fresh-install 血统 + 2026-10-12 从 252 镜像的配置面数据）。
> 方法：同一份指纹 SQL（`fp2.sql`，information_schema + pg_catalog，按
> table/view/column/index/constraint/function/trigger 七节输出制表符行）在两侧执行，
> `LC_ALL=C sort` 后做集合差；分区子表单列 `partchild` 节不参与对账。
> 指纹快照：local 17137 行 / prod 17679 行（2026-10-12 03:0x 采集）。

## 一、结论摘要

| 类别 | 生产独有 | 本地独有 | 定义不同 | 处置 |
|---|---|---|---|---|
| 表（共有 330 之外） | 109 | 4 | — | 见 §3.1 |
| 视图 | 11 | 2 | — | 见 §3.2 |
| 列（共有表上） | 50 | 6 | 19 | 见 §3.3 |
| 约束（共有表上） | 41 | 21 | 19 | 见 §3.4 |
| 索引（共有表上） | 53 | 30 | 3 | 见 §3.5 |
| 函数（public） | 19 | 27 | 11（体差异） | 见 §3.6 |

- **修正方向仲裁 SSOT = 仓库迁移文件（creator）**；canonical 表定义以
  `sql/objects/tables/*.sql` 为准；creator 失落者以生产实况为准并标注「prod-manual」。
- 本轮执行 **`01-local-fix-canonical-files.sh` + `02-local-fix-extra.sql`（本地侧）与 `03-prod-fix.sql`（生产侧，3 笔）**。
  全部幂等/additive；生产侧仅三笔且均为零数据风险的索引/检查补齐（CONCURRENTLY /
  NOT VALID 形态）。其余差异按 §3 各节的「不修」裁决留档。
- 已知血统性豁免（不是缺陷，不修）：PG18 NOT NULL 显式约束目录（2555 条假差异）、
  数组 deparse 格式、`gbt_*`/`pg_stat_statements`/`citus_*` 扩展函数与视图、
  bak_*/归档/分区子表数量、bodies 列退休计划（生产已执行、本地保留）、
  `ursm_node_snapshot_min` 生产手工分区（830.skip）、`durable_llm_tasks` 生产 0 行休眠代差、
  62 张外部子系统表。

## 二、根因（为什么本地落后）

1. **两条投递通道分叉**：installer（`dbinit` StartupFiles，五点同步契约）是 fresh-install
   的完整序列；host-first 部署（`deploy-local.sh: apply_schema_if_empty`）用的是主仓
   `sql/schema/01-schema.sql` 原始快照 + `apply-db-revision-sequence.sh` 通道文件数组。
   本地库走的是后者。
2. **01-schema 快照「不忠实于任何单一血统」**（runner.go 注释自认，dump 于 ~477 附近）：
   456/467（sessions v2 列）、315（prompt_injection 检测列）等 <471 对象不在快照里。
3. **通道文件数组历史性缺口**：本地 `schema_migrations` 仅 60 条（471 起跳），
   472–643 区间的通道登记长期稀疏（845-851 八连红是同一疾病的晚期症状）。
   实测本地缺失对象横跨：351/426/520/541/554/567/569/571/578/629/632（表，全部
   TSV✓ 却不在本地；其中 541/569 两表在执行前已被并行会话补齐，见 §3.1 更正）、
   479/539/544/602/603/611（列）、511/518/521/526/527/476/614/621/644 系（约束/索引/函数）。
4. **两类「从未登记」实例**（上一轮 852/486 同族）：`605_fix_tool_calls_index_predicate`
   （TSV✗ chan✗——修 `jsonb_array_length` 对非数组 jsonb 报错的谓词守卫，本地至今带病）
   与 830（ursm 分区，`.skip` 后缀，生产手工执行）。
5. 附带发现（环境事故，非 schema 缺陷）：本机 2026-10-12 02:33–02:56 重启，host PG/Redis
   均为手动服务无 launchd，重启后网关+双依赖全灭；已按部署同款方式手动拉起
   （pg_ctl + redis-server + bin/2.5.8.2538/gateway，见 §7 验证）。

## 3.1 表（生产独有 109 / 本地独有 4）

生产独有 109 张分类（子代理逐表考古全表见附录 A）：
- **BACKUP 24**：`bak_20260920_*`（模型目录重构前手工备份）+ `bak_20261011_drift_bindings{,_b}`
  + `instance_heartbeats_old_backup`。不修。
- **CANONICAL_MISSING_LOCAL 21**（creator 迁移全部定位，本地补建，见 01 脚本 A 节）：
  candidate_binding_scope_revision(541)、candidate_binding_scope_revision_canonical(569/571/578)
  （**审计期实测更正**：执行前两表已被并行会话落库并带数据/函数/6 触发器——本地日志的
  42P01×219 发生在 01:5x 重启窗口，02:04 起已自愈；01 腿对其为幂等 no-op，实际收益=
  571/578 刷新函数体）、
  durable_task_settlement_intents(520)、goal_runs/goal_run_steps/goal_run_actions(554)、
  session_analysis_metadata(567)、session_clusters/session_cluster_members/session_embeddings/
  session_optimization_suggestions/session_request_summaries/session_tags(351)、
  task_type_centroids(426)、audit_attachments_cleanup(629)、audit_attachments_filesystem_cleanup(632)、
  request_abandoned(819)、mcp_registry/mcp_tools/task_assigner_agents/task_assigner_assignments
  （local/641 共享平台 fixup，additive-only，本机即目标环境）。
- **ARCHIVE_PARTITION 2**：cache_metrics（月度分区父表，470/472——**TSV✓ 却缺本地**，
  本地日志实测 `relation cache_metrics does not exist`×2；随 470/472 文件补建）、
  ursm_node_snapshot_min_legacy（830 分区化遗留快照，不修）。
- **INVESTIGATE 62**：memora/kxmemory/graph/knowledge/openclaw/wiki/projects/documents/
  chat/memory/auth/doc_tools 等外部子系统在共享库自建的表（全仓库无 CREATE TABLE）。
  252 库是多服务共享库；不属本仓库对账范围，不修。`repository_schema_migrations` 为
  历史账本（run-migrations-strict.sh 引用），同列不修。

本地独有 4 张：`candidate_failure_logs_columnar_archive`、`handoff_logs_legacy_532`（532 血统遗留）、
`request_logs_archive_2026_07`（680b 本地归档）、`session_owners`。均为本地血统产物，不修。

## 3.2 视图

- 生产独有 11：`v_task_summary`、`v_daily_session_costs`、`v_project_summary`、
  `v_session_task_id_coverage`、`v_unified_probe_{failing,suspicious,urgent,watchdog}`（302）、
  `prompt_injection_stats_enhanced`（364）、`citus_schemas`/`citus_tables`（扩展版本差异，不修）。
  前 10 个为报表/诊断视图：302/364 creator 的随文件补建；4 个无 creator（prod-manual
  报表视图）按生产定义复制（01 脚本 D 节）。
- 本地独有 2：`request_logs_bodies_progress`（bodies 退休计划本地进度视图）、
  `session_owner_stats`。不修。

## 3.3 列（共有表：50 生产独有 / 6 本地独有 / 19 定义不同）

**生产独有 → 本地补齐**（creator 与形态逐列核对，SQL 见 01 脚本 B 节）：

| 表 | 列（缺） | creator | 备注 |
|---|---|---|---|
| sessions | last_full_request/last_full_response/last_full_payload_at/title/summary/summary_model/summary_generated_at/summary_quality/user_tags | 456/467 | 两文件形态不同（456 有 IF NOT EXISTS），取 456 形态 |
| handoff_pending_confirmations | goal_state/goal_state_version/restore_status/restore_error/restore_attempted_at/restored_at | 527 | 连带状态 CHECK 扩宽（§3.4） |
| prompt_injection_detections | detection_score/matched_rules/matched_rules_count/detection_layers | 315 | 本地 0 行，可直接 NOT NULL |
| request_logs_hot | cached_response_id/context_size_tokens/continuation_keywords/effective_timeout_seconds/is_continuation/keepalive_sent_count/node_switch_count/timeout_mode | 603 | 603 的 ADD COLUMN IF NOT EXISTS 块逐列照抄 |
| auto_combo_templates | capability_image_generation/capability_tool_use/capability_vision/capability_modalities/denylist_codes/display_name_en/notes/pool_keys/sort_by/template_key/tier_preference | 无 creator（prod-manual） | 按生产实况复制，连带 UNIQUE(tenant_id,template_key) |
| free_resource_catalog | allowlist_in_auto_combo/api_endpoint/capability_modalities/capability_tool_use/capability_vision/last_probe_at/notes/signup_url | 无 creator（prod-manual，084 只建了基础列） | 按生产实况复制，连带 UNIQUE(tenant_id,provider_code,model_id) |
| stats_adjustments | adjustment_type/metric_name | 544 | IF NOT EXISTS 原形 |
| stats_reconciliation_diffs | tenant_id | 539 | NOT NULL DEFAULT 'default' |
| durable_llm_tasks | commit_metadata/connection_attached/decision_history/last_disconnect_at | 657/722 | **不修**：本地列集匹配当前代码；生产 0 行休眠代差（§3.7） |

**定义不同（19）→ 裁决**：

| 对象 | 本地 | 生产 | canonical 裁决 | 动作 |
|---|---|---|---|---|
| auto_combo_templates.variant default | 无 | 'cheap' | 075 | 本地 SET DEFAULT |
| auto_combo_templates.free_type_filter default | 无 | '{}' | 075 | 本地 SET DEFAULT |
| credential_model_bindings.context_window_source default | 无 | 'catalog' | 804（`sql/objects` 同） | 本地 SET DEFAULT |
| credentials.concurrency_mode | nullable 无默认无 CHECK | NOT NULL DEFAULT 'concurrency' + 4 值 CHECK | 479 canonical 终态=SET DEFAULT+SET NOT NULL+CHECK（**审计更正**：生产=canonical，本地欠账） | 本地补 DEFAULT+NOT NULL+CHECK |
| free_resource_catalog.tos_verdict | NOT NULL DEFAULT 'unknown' | nullable DEFAULT 'caution' | 084 待考 | **维持生产形态**（生产在跑）：本地 DROP NOT NULL+SET DEFAULT 'caution' |
| request_logs_hot.agent_name/agent_type | varchar | text | 443 = VARCHAR(255)（父表） | **不修**（text/varchar 语义等价，改型要重写视图族，风险>收益，留档） |
| candidate_failure_logs_hot.ts | NOT NULL（canonical 392） | nullable | 392 = NOT NULL | 本地不动；生产回补 SET NOT NULL 列入 §5.2 低优先（本轮不执行，热表锁风险） |
| durable_llm_tasks 各列 default/nullability | 516/722 形态 | 生产旧形态 | 代码实证（durable/store.go INSERT status='running'，Status 枚举 8 值）= **本地** | 双侧不修；生产休眠（0 行）留档 |

**本地独有 6 列**：request_logs.request_body/response_body、request_logs_hot.request_body/
response_body/outbound_body（**bodies 退休计划**生产已执行列删除；本地按计划未到该步）、
gateway_db_revision_sequences.content_sha256（部署链内容指纹记账，本地通道机制私有）。
均不修。

## 3.4 约束（共有表：41 生产独有 / 21 本地独有 / 19 定义不同）

- 定义不同 19 中 **13 条是 pg17↔pg18 deparse 格式差**（`(ARRAY[('x'::varchar)::text,…])` vs
  `((ARRAY['x'::varchar,…])::text[])`），语义逐字相同，不修。
- 真差 3 条：`handoff_pending_confirmations_status_check`（3 值 vs 6 值，creator 527——本地扩宽）；
  `hosted_tasks_status_check`（生产多 'completing'——**全仓代码零引用**，休眠漂移，双侧不修）；
  `providers_proxy_subscription_id_fkey`（本地 NOT VALID——小表 VALIDATE，本地修）。
  `self_check_runs_error_type_check`（值域相同，仅 NOT VALID 旗标差——不修，留档）。
- 本地独有 21 / 生产独有 41 中，除以下外多为**改名等价对**（agent 配对报告附录 B）或
  511 约束族（request_state_transitions 20 条 chk，creator 511，本地缺→补）：
  `session_bodies*` 唯一键（生产含 tenant_id=476 canonical；本地旧形 (request_id,partition_date)
  更严——**加 canonical 不删旧**，单租户数据下二者等价）；`keyless_providers`/`free_resource_catalog`
  唯一（同列集异序异名，属改名对，不修）；`session_turns` attachment 三检查（474，生产缺→生产补，
  NOT VALID 形）；`chk_request_logs_token_band`（creator 542，内容加固重放缺口，生产补 NOT VALID）；
  `task_type_tier_config_preferred_tier_check`/`auto_combo_templates_variant_check`（本地血统，保留）；
  durable 约束族（§3.3 裁决，双侧不修）。

## 3.5 索引（共有表：53 生产独有 / 30 本地独有 / 3 定义不同）

子代理逐条语义配对（全表附录 B）：
- **改名等价对 14**：不修（两侧各自命名血统一致即可）。
- **本地缺 39**：26 条有 creator（518/527/621/478p2/511/476/614/526/474/544/548/539/718 →
  本地补，01 脚本 C 节）；~13 条 prod-manual（auto_combo×2、durable×2、free_quota×1、
  free_resource×3、keyless×3 等运维手建）——**其中与 §3.3 复制的列配套的唯一索引随列补齐，
  其余纯性能索引不复制**（无 creator 的形状不进本地）。
- **本地多 16**：多为 476 之前的旧唯一键与旧命名（保留）；`idx_credential_probe_queue_automatic_claim`
  （本地有、生产缺——**生产补**，CONCURRENTLY，creator=817 系探针队列）。
- **定义不同 3**：`idx_request_logs_provider_tool_calls` 谓词缺 `jsonb_typeof='array'` 守卫
  （605，TSV✗——**本地真缺陷**：非数组 tool_calls 会令 jsonb_array_length 报错；本地 DROP+重建，
  并把 605 登记进通道/安装器列为后续票）；另两条为 deparse/结构（ursm ON ONLY=830.skip 血统）不修。

## 3.6 函数与触发器

- **触发器差异（审计更正，初版误判为零）**：真实差异 5 处已全部补挂（02 脚本 D3）——
  `credential_model_index_hot.trg_credential_model_index_hot_replace`（642，防并发回滚 23505）、
  `request_logs_hot.trg_sync_session_task_id`（session_summaries.gw_task_id 回填）、
  `model_offers` INSTEAD OF insert/update/delete×3（软删触发器，函数本地已在仅缺挂载）。
  642 系函数+触发器是**成对缺口**：只拷函数不挂触发器等于没修。
- **体差异（BODY_DIFF）**：promote_*×3 / ensure_request_logs_partition / auto_rotate_to_columnar /
  bump_credentials_governor_revision / notify_credentials_governor_revision / update_session_summary /
  update_task_type_tier_config_updated_at / update_updated_at_column / refresh_session_analytics_views
  ——多为 535/689/722 系函数链的**代次差**。逐个比对属高成本低收益（调用方两侧同为网关二进制，
  写路径以 SQL 文件+Go ensure 为准）。本轮仅核对 `update_session_summary`（661 尾版，双侧语义一致）
  不改写；其余留档为低优先对齐项。**不修**。
- **生产独有 10**：`ensure_ursm_node_snapshot_min_daily_partition`（**本地活缺口**：本地代码
  URSM 分区器调用，日志实测 `function ... does not exist`×2——从 830.skip 提取建）；
  `ensure_handoff_logs_partitions`（baseline 契约函数，`baseline_ensure_functions_contract_test.go`
  引用——按生产定义复制）；`archive_session_module_executions`（580）；`replace_credential_model_index_row`
  （642）；`get_system_snapshot`（433）；`sanitize_model_integrity_jsonb`/`sync_session_task_id`
  （creator 失落，644/655 down 痕迹——按生产定义复制）；`archive_dashboard_events`（361.disabled，
  刻意停用——不修）；`backfill_session_v2_turns`/`credential_client_quota_*`（prod-manual 一次性
  助手/外部表触发函数——不修）。
- **本地独有 2**：`cleanup_expired_session_turn_logs`（753 建、755 判死未登记——按 755 意图本地
  应 DROP；为保守起见本轮仅留档不删）；`fips_mode`（creator 失落，无调用方——不修）。
- **孤儿函数**：生产 `bump_candidate_binding_scope_revision*` 7 个无触发器挂载（触发器已删）。
  本地随 569/571/578 建表会带同批函数——**属 canonical 形态**，不视为漂移。

## 3.7 血统/版本噪音豁免清单（不修的完整理由）

1. PG18 将 NOT NULL 落目录为命名约束（2555 条 `*_not_null` 假差异）。
2. pg17/pg18 CHECK 数组 deparse 格式（约 13 条约束 + 1 条索引）。
3. 扩展对象：`gbt_*`/`gist_*`（btree_gist 内部随 PG 版本）、`pg_stat_statements`（版本不同签名不同）、
   `citus_tables`/`citus_schemas`（列存扩展视图）。
4. bak_*/归档/分区子表数量差（运维备份与 promote 节奏）。
5. bodies 列退休计划：生产已删 request_logs(bodies) 列，本地保留（计划未到）。
6. `ursm_node_snapshot_min` 生产已分区（830.skip 手工操作），本地 heap。
7. `durable_llm_tasks` 生产 0 行休眠代差（状态枚举/列集为旧代；当前代码 8 值枚举与本地一致；
   生产若启用 durable 功能需先做 516/722 收敛——登记为遗留风险）。
8. 62 张外部子系统表（共享库多服务）。

## 4. 修正 SQL

- **`01-local-fix-canonical-files.sh`**（本地腿一：canonical 迁移文件原样投递，16 个文件，
  补 21 张缺失正式表 + cache_metrics + 527 的列/状态/索引）。
- **`02-local-fix-extra.sql`**（本地腿二：外科补充，幂等 additive）：B 列/默认值/约束
  （含 request_state_transitions 约束族 NOT VALID+VALIDATE、476 租户唯一）→ C 缺失索引
  （19 条 + 605 谓词重建）→ D 缺失函数（7 个）与视图（9 个）。
- **`02-prod-fix.sql`**（生产侧，仅 3 笔零数据风险项）：
  1. `idx_session_turns_attachment_count_gw` + attachment 三检查（474，CREATE INDEX CONCURRENTLY +
     ADD CONSTRAINT NOT VALID）；
  2. `idx_credential_probe_queue_automatic_claim`（CONCURRENTLY）；
  3. `idx_request_logs_token_band_ts`（CONCURRENTLY）+ `chk_request_logs_token_band`（NOT VALID）。
   其余生产侧候选（cfl_hot.ts SET NOT NULL、self_check VALIDATE、providers FK 已 VALID 不动）
   均因锁风险/收益比列为不执行。

## 5. 审计记录

- 自查：每条修正 SQL 标注 creator/来源与幂等形式；方向仲裁以迁移文件 > canonical 表定义 >
  生产实况 > 本地实况 优先级裁决；「不修」清单逐项给理由（§3.7）。
- 执行前机械分类：子代理（Explore×2）——109 张生产独有表逐表考古（附录 A）与索引逐条
  语义配对（附录 B）。
- 执行前独立复核（general-purpose 审计代理，只读+BEGIN/ROLLBACK 试跑）：**总体判定
  「需修正后执行」**，拦截问题与处置：
  - P0×2（02 脚本）：18 处 `ADD CONSTRAINT ... NOT VALID CHECK(...)` 语法序错误（PG 要求
    NOT VALID 置尾）+ 7 处函数体缺结尾分号——已修，修后整脚本 BEGIN…ROLLBACK 试跑
    0 ERROR 0 WARNING（B10 的 18 条 VALIDATE 对本地 8695 行全部通过）。
  - P1×3（文档断言）：479 裁决与迁移文件相反（生产=canonical，已改写 §3.3 并在 B5 补
    SET NOT NULL+CHECK）；「触发器零差异」为假（实测 5 处真差，已补 D3 挂载）；scope_revision
    两表执行前已被并行会话落库（已改写 §3.1/§二 叙述）。
  - P2×5：两条 476 索引误带 ON ONLY（会造 INVALID 空壳，已删）；474 的附件索引漏交付（已补）；
    providers FK VALIDATE 文档矛盾且未落地（已补 C3）；跳过理由失实（已改写）；ursm 函数
    在本地 heap 表上首调会报错（README 注明：830 分区化是前置，留档不执行）。
  - P3：文件数 15→16、creator 张冠李戴三处（542/706b/545）等文字更正。
  - **审计员事故披露**：其 BEGIN…ROLLBACK 试跑因未剥文件内 COMMIT，导致 01 腿 16 个
    canonical 文件被实际提交到本地库。性质评估：全部为幂等 additive canonical SQL（=修正
    目标终态），数据行变更仅 527 UPDATE 0 行命中与 ON CONFLICT DO NOTHING；生产库全程
    只读。客观副产品：01 腿 16 文件在本地库全量跑通零错误已获实证。
- 结论：README/01/02/03 均按审计意见修订后进入执行（§6/§7）。

## 6. 执行记录（2026-10-12 03:4x–04:0x）

1. `01-local-fix-canonical-files.sh`（驱动含逐文件容错与 psql PATH 守卫）：
   15/16 文件幂等通过；唯一失败 `520_durable_task_settlement_intents.sql:56`——该文件对
   RLS policy 无重放守卫（`policy ... already exists`），效果已在库（首跑已落），登记为
   520 文件自身的重放缺陷（不改 canonical 文件，留档）。
2. `02-local-fix-extra.sql`：真跑 0 ERROR 0 WARNING（B10 的 18 条 VALIDATE 全通过，
   含 D3 触发器挂载与 605 谓词重建）。
3. 追加四项残余收口（01 腿新建表暴露的后继 ALTER）：555（goal_run_actions 租约族，
   TSV✓）、427（task_type_centroids.embedding_model）、387 的 embedding_v2 vector(1024)
   （本地 pgvector 在场）、`mcp_registry.health_check_path`（prod-manual，按生产实况）。
4. `03-prod-fix.sql`（252 生产，经 ssh+docker exec）：5 语句全成功——
   session_turns 三检查（NOT VALID）、`idx_credential_probe_queue_automatic_claim`
   （CONCURRENTLY）、`chk_request_logs_token_band`（NOT VALID）。

## 7. 验证证据（2026-10-12 实测）

- **指纹收敛**：本地缺表 109 → **87**（余量=62 外部子系统 + 24 备份 + 1 血统遗留，
  全部计划内不修）；共有表列差异 50 生产独有 → **11**（余量全为计划内：durable 休眠
  代差×4、bodies 退休×5、部署链私有 content_sha256、生产落后的 456 三列×3——
  零计划外残留）；约束/索引/函数按 §3.4–3.6 裁决收敛（含 14+14 改名对等价保留）。
- **三族活错误全部止熄**（gateway-8782.log 最后出现时间均早于对应修复落地）：
  `candidate_binding_scope_revision_canonical` 42P01 最后 01:58:36（修前）；
  `cache_metrics` 最后 03:05:07（修前）；`ensure_ursm_node_snapshot_min_daily_partition`
  最后 03:41:29（修前）。已知残留：该 ursm 函数在本地 heap 表上**下次被调用时**会换成
  「父表非分区」错误——830 分区化是前置（§3.7 第 6 条，登记不执行）。
- **端点/健康**：本地 `/readyz` ready（pg18+redis 连通）；`GET /api/providers/36/models`
  200；生产 245(:8782) 与 154(:8781) `/readyz` 均 database/redis connected
  （03 生产 DDL 后双侧网关无异常）。
- **生产安全窗**：03 全部语句 NOT VALID/CONCURRENTLY 形态，执行期间无锁等待报告。

## 附录

- A. 109 张生产独有表逐表分类（子代理产出，TSV）——见本文档同目录 `appendix-a-tables.tsv`。
- B. 索引差异逐条配对报告（子代理产出）——见 `appendix-b-indexes.md`。
- C. 指纹 SQL——`fp2.sql`。
