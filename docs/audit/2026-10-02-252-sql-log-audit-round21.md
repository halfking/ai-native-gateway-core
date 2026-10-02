# 252 PG SQL 日志审计第二十一轮（2026-10-02）—— R20 遗留六项处置轮：supplier_errors 误登记裁决 + heap 归一（813）/ N2 死列拍板落定（814）/ AM 残留全景决策矩阵

以 round20 §八 + round19 §八遗留为起点（起点重算：origin/main = 20fbdba74）。本轮 = 每日 06:00 自动化（automation-f0a3bf63）之后的当日追加轮，六项优先级 ①-⑥ 逐项推进。修复经本机 llm_gateway 真库实跑先行（途中抓出 n_live_tup 陈旧统计陷阱，见 §七 A1），再 252 生产预应用 + 双账本登记（813/814，sha256 双端一致）。所有时间戳为 252 服务器 CST；本机/252 数据分别标注。

## §一、① supplier_errors 90d TTL「时间炸弹」裁决：R20 误登记 + 迁移 813 heap 归一

### 裁决：「Row-level DELETE 11-06 必炸」不成立（三链证据）
1. **opslog_trimmer 无 supplier_errors 路径**：bg/opslog_trimmer.go 只管 candidate_failure_logs（7d）与 credential_probe_model_log（30d）两表；其 row-level DELETE 家族的月分区自 689 起已全 heap，本就安全。
2. **全仓 supplier_errors 的 row-level DELETE 仅存在于 _hot**（promote 批内 `DELETE FROM supplier_errors_hot`，heap 表；252 真库函数体核验 deletes_hot_only=t）。
3. **真 TTL 路径 = 整分区 DROP**：bg/partition_manager.go `stateTableTTLSpecs` → `drop_old_state_partition_table`（689 helper）；252 真库函数体指纹核验 **has_drop_table=t / has_row_delete=f**——`(month_first_day + 1month - 1day) < now()-90d` 命中即 `DROP TABLE`，对列存分区安全。出窗数学边界：supplier_errors_2026_08 → **2026-11-29 后首个 day-2 tick（≈2026-12-02）**，非 11-06。settings_kv 无 `lifecycle.supplier_errors_ttl_days` 显式行，默认 90 生效。

**结论：R20 §五.2 的处置选项二（TTL 改整分区 DROP）已经在位，无需任何改动；登记撤销。**

### 列存残留的实害面（仍须归一的机制原因）
- **UPDATE/DELETE/tableoid 读毒面**（㊿ 查询引用面）：`SELECT tableoid::regclass, count(*) FROM supplier_errors GROUP BY 1` 实测炸 `UPDATE and CTID scans not supported for ColumnarScan`；普通 `count(*)` 129,279 行可读；带 ts 谓词读（生产聚合器形态）分区剪枝安全。生产无 tableoid 消费方 → 零现网故障，纯潜伏面。
- **自愈反噬环**：`ensure_supplier_errors_partition` 的 ELSE 分支每小时 ensure tick `PERFORM enforce_columnar_partition(partition_name,'supplier_errors')` 把既有分区强行转回 columnar——**只转分区不换函数必然复发**（689 两步缺一不可的机制原因）。

### 迁移 813（689 两步同款，单事务 + 10min statement_timeout + 上海钉扎）
- 第一步：`CREATE OR REPLACE FUNCTION ensure_supplier_errors_partition` 为 heap 版（去 `USING columnar`、去 ELSE enforce 分支；上海钉扎第一语句保留——三基线契约测试钉同形）。
- 第二步：四分区转 heap——空壳（2026_08/2026_11）走 812 同款 DETACH+锁内二次 count（TOCTOU 收口）+DROP+重建；带数据（2026_09/2026_10）走 689/811 同款 DETACH→改名 bak→同边界重建 heap 叶→INSERT SELECT 回拷→**行数守恒校验（新 ≥ bak，容忍并发 promote）**→DROP bak。
- 并发窗口取舍：DETACH→重建秒级窗内 promote tick 整批原子失败、行留 hot、下小时重试（656 模板单语句 CTE 原子性）；事件触发器反噬已排除（`columnar_insert_only_parents()` 252 实测 = `ARRAY['routing_decision_log']` 正典单族，不含本族）。

### 实跑数据
| 环境 | 2026_08 | 2026_09 | 2026_10 | 2026_11 | 守恒 |
|---|---|---|---|---|---|
| 本机（转换前实测行数） | 0 | 73,675 | 282 | 0 | 73,957 = 73,675+282 ✓ |
| 252（转换前实测行数） | 0 | 124,846 | 4,433（侦察时点） | 0 | 父表转换后 129,325 = 124,846+4,479 ✓ |

- 本机 09 实测 73,675 行 vs `pg_stat_user_tables.n_live_tup=0`（陈旧统计）——若按单通道"空壳"设计将 DETACH+DROP 丢数据；双通道（exact count 判定分流）兜住（纪律 52 的直接来源）。
- 252 2026_10 转换计数 4,479 = 侦察 4,433 + DETACH 窗口并发 promote 46 行（守恒 ≥ 语义兜住，非数据异常）。
- 转换后四分区 heap + **reltoastrelid>0** ✓；幂等重放 813 二跑零输出 ✓；功能活体：手动 `promote_supplier_errors_hot_to_partition()` tick 32 行走通 heap 新路径、`ensure_supplier_errors_partition(now())` 返回正常 ✓。
- 台账：本机 + 252 双账本（schema_migrations + llm_gateway_migration_checksums，advisory lock 契约；sha256 `72c39970…` 双端一致）+ docs/db-changelog.md。
- 三基线 + contract test 同步：sql/schema/01-schema.sql、deploy/sql/schemas/baseline/01-schema.sql、installer embeddata/01-schema.sql 的 ensure 函数改 heap 版（新环境从源头建 heap）；baseline_ensure_functions_contract_test.go 的 heapMust 增补 `ensure_supplier_errors_partition` 防基线再漂移；apply-db-revision-sequence.sh 登记。历史迁移 699 文件本体不动（revision-sequence 不可变），813 运行期取代。

## §二、② N2 探针视图死列拍板 → 迁移 814

- 死列机制（R18 fact 103 确认并落地）：`v_adaptive_probe_targets.recent_passive_failures` 相关子查询读 candidate_failure_logs 分区父表，而 0-8h 行按 promote 保留期语义全在 candidate_failure_logs_hot → 5min 窗谓词下父表臂恒空 → 该列自 038（2026-07-05）起恒 0。
- **R20 §三"probe-target 家族 ×425/2,910s 是其代价实证"归因修正（日志逐条取证）**：该家族实为两类语句的合并标签——
  1. model_probe cycle 目标查询（`SELECT cmb.credential_id…JOIN provider_models…`）×7 statement timeout 实证（贴 30s rolconfig 线，续行体 `JOIN provider_models pm ON pm.id = cmb.provider_model_id`）；
  2. project_backfill（`WITH targets AS … sync_session_project_attr`）×6 statement timeout 实证；
  且 v_adaptive_probe_targets **全仓零消费方**（bg/model_probe.go 仅 627 行注释残留；applyPassiveBoosts 走 candidate_failure_logs_with_current_month；admin 无引用）——视图不在任何执行路径上，不可能产生该代价。
- 拍板=**修不删**：迁移 814 `CREATE OR REPLACE VIEW`，子查询改读 `candidate_failure_logs_hot`（5min 窗 ⊂ 8h promote 保留窗 ⇒ hot 是精确源不多不少；hot 有 (credential_id,ts DESC)/(raw_model_name,ts DESC) 复合索引，252 实测在位）；列集/顺序不变，对任何未来消费方契约稳定。对象退役（如需）归存储轨道清单。
- 落地：本机 + 252 双应用（幂等，to_regclass 守卫）；sql/objects/views/v_adaptive_probe_targets.sql + 三基线四处同步；双账本 sha256 `73089753…`。
- 收益定性：**契约正确性**（未来消费方不再拿恒 0 的 passive-failure 信号），非现网减负——R20 的"代价实证"句撤销。

## §三、③ 13 个非空 toastless 分区轮换兜底复核

- **机制复核：810 幂等重放无自动触发者**。810 登记 schema_migrations 后启动链永久跳过；content_sha256 重放通道只在文件内容变更时触发；"月分区轮换到空时由 810 重放兜底"（R20 §五.1 处方）缺执行机制 = **机制缺口登记**，处方归存储轨道（启动 pg_get_functiondef 对账自愈 / 择机运维窗口按 806/562 通道重建 / 或接受现状——见下行风险实测）。
- **风险实测：零实害**。全库 heap-without-toast 现存 **30 关系**（R20 只扫分区漏了普通表；810 治愈后余 13 非空分区 + 6 个 usage_facts 日分区 + ~17 张普通表，含 provider_metrics_minute 249MB / provider_metrics_hour 25MB / provider_quality_profiles 4MB）：
  - 7 个可测分区行宽 max：sessions_2026_09=1,389B、sessions_2026_10=1,342B、stats_event_inbox_default=736B、usage_facts_default=616B、session_memora_2026_09=476B、auto_route_selections_2026_10=352B、session_censors_2026_09=148B——全部 ≪ 8,160B TOAST 线；
  - provider_metrics_minute/hour/profiles 行宽 max 208B（行均 180B）；其余小配置表 KB 级；
  - 结论：**无 row-too-big 复发风险**；月分区随 TTL DROP 自然退役；default 分区与普通表无轮换 → 择机重建或接受现状（存储轨道择策）。
- 附加事实：新日/月分区由正典 ensure 建 heap（自带 TOAST），病灶不再新增；810 判据（relam=heap ∧ reltoastrelid=0 ∧ 有 toastable 列）已入迁移头注可随时手工重扫。

## §四、④ 列存残留 AM 归一决策矩阵（与 R44 存储轨道对签）

252 现状盘点（pg_class×pg_am 实测）：**18 族 50 关系**——正典设计内 7（request_logs_bodies ×3[765] + routing_decision_log ×3 + routing_decision_log_archive_default）；漂移 43 关系/15 族（model_probe_runs 已被 812 清除、supplier_errors 本轮由 813 归一）。事件触发器正典名单 252 实测 = `ARRAY['routing_decision_log']` ✓（fact 99 基线维持）。

| 族 | 现状（10-02 实测） | 处置决策 |
|---|---|---|
| supplier_errors ×4 | 09=124,846/10=4,479 行（含并发），余 0 | **813 已归一 heap ✓** |
| credential_model_index 09/10/11 | 09=301,255 / 10=9,034 / 11=0 行；promote 追加写、cleanup 设计性跳过列存（真库函数体核验） | 689 helper TTL 7d 自退役（~10-07/11-07/12-07）→ 无动作 |
| cache_metrics 08/09 + default | 全 0 行 | 689 helper TTL 90d 自退役（~11-29/12-29）；default 滞留=手工 DROP 候选（移交） |
| handoff_logs 09/10/11 + default | 全 0 行 | TTL 30d 自退役（~10-31/11-30/12-30）；default=手工 DROP 候选（移交） |
| tool_usage_stats 08/09/10 | 全 0 行；写方只进 _default(heap) | **冻结残留**：无 stateTableTTLSpecs 条目且 `lifecycle.tool_usage_stats_ttl_days` 有键无消费方 → 永不自动 DROP → 手工 DROP（移交存储轨道） |
| credit_ledger 08/09/10 | 08=260 行余 0 | 同上冻结残留（TTL 键有键无消费方）→ 手工 DROP（移交） |
| dashboard_access_events 07-10 + default | 全 0 行 | 冻结残留 → 手工 DROP（移交） |
| session_module_executions 07-09 + default | 全 0 行 | 冻结残留 → 手工 DROP（移交） |
| auto_route_selections 08/09 | 0/6 行（流量 09-09 起断流） | 冻结残留 → 手工 DROP（移交） |
| 单表列存：provider_events / model_offer_events / price_change_events / tool_call_events / system_probe_runs_default | 0 行 ×4；system_probe_runs_default=3,539 行 INSERT-only | 择机 heap 化（小表低险，移交）；写面已核 INSERT-only 无 UPDATE 毒源 |
| test_columnar_new | 0 行 32kB | 测试残留 → DROP（移交） |

决策原则重申：正典单族基线维持（{routing_decision_log} + 765 的 request_logs_bodies）；其余全部 heap 化；空壳冻结族优先手工 DROP 而非转换（最小 DDL 面）。执行主体=R44 存储轨道；本矩阵即对签载体。

## §五、⑥ 更新器/ensure/promote 三 tick 零错误对账（部分窗 + 收口机制）

取证窗 = 本轮快照起点 **10-02 03:51**（ctr.log copytruncate 轮转语义，与 R20 窗重叠 2h13m，家族计数已按时间戳去重）→ 取证点 10:49。

| 家族（头锚定） | 窗内计数 | 最后一条时间戳 | 判定 |
|---|---|---|---|
| UPDATE and CTID（更新器毒源） | ×20 | 07:06:27（812 应用前） | **812 后零复发 ✓**（R20 实证 07:20:51 更新器复写；本轮复核 model_probe_state=467 healthy/436 recovering/65 broken/31 suspicious 健康四态流转） |
| row is too big（promote 毒源） | ×9（同 R20 毒行 size 36112 的逐 tick 重试尾巴） | 06:51（810 应用前后过渡） | **07:06 后零复发 ✓** |
| would overlap（ensure 毒源） | ×1 | 05:20:37（811 应用前） | **811 后零复发 ✓** |
| promote 节拍 | session_bodies 07:11/08:53/09:54 = 1.2-3.8s/批；supplier_errors 手动 tick 32 行 | — | 每小时 tick 健康 ✓ |
| ensure ticks | 无慢无错（仅 ensure_usage_facts_daily_partition 25.2s 一条贴线未杀） | — | ✓ |

- 满 24h 差 ~20h → **收口自动化 automation-09e1ae20 已建（10-03 07:30 一次性）**：PASS 判据、自噪音清单、物证路径写死在 prompt，产出追加本报告 §十。
- 慢查快照（修正提取：duration 与 statement 同行、空头取续行）：≥1s **2,287 条 / 8,177s / 4.7h**。1,996 空头 = 巨 payload INSERT 续行形态——**622MB/4.7h 日志爆发（2.2MB/s，50× 基线 230B/s）= R15 巨 payload body 记录机制复现**（ZCode 会话经网关调 LLM 的 request_logs_bodies_hot MB 级字面量内联），登记不处置；140 credential_model_index_hot INSERT、20 analyze（D7'）、REFRESH 家族个位数贴 180s pin 内合法。
- **本轮自噪音账**（11:00 后窗）：J4 毒源复现探针 CTID ×1（@11:10:38，故意的）、candidate_failure_logs count 超时 ×1（@11:20:19，30s rolconfig）、column-does-not-exist ×4（evtfunid/updated_at/relkind/stats_reset，侦察笔误）——生产对账全部剔除。

## §六、⑤ E6 外部移交催办（状态更新）

- 窗内 **83 条头锚定（4.7h），恒定精确 12/h 整点节拍**（04-09h 每 h 恰 12 条、10h 截至取证点 9 条）——自 R14 登记以来节奏纹丝不动。
- 证据链完备：pocket 库（opencode_pocket schema，opencode-pocket 所有）scheduled_tasks UPDATE `inconsistent types deduced for parameter $4 at character 135`；非本仓代码（全仓 grep 零写方，R10 实证）；外部轨道移交维持。
- **催办升级**：四轮（R14→R21）无衰减、恒 12/h ≈ 288 次/日无效负载与日志噪音；建议 opencode-pocket 轨道排期修复（$4 参数类型标注 integer→bigint）。

## §七、新纪律候选

- **52**：`n_live_tup`/pg_stat_user_tables 对列存与新写入分区不可信（本机 supplier_errors n_live_tup=0 vs 实际 73,675 行；252 同形态）——转换/裁决前必须 exact `count(*)`；"空分区"判定禁止引用统计视图。
- **53**：「TTL 时间炸弹」登记前必须三链核验：①settings spec 有键 ≠ 有消费方（tool_usage_stats/credit_ledger/request_wal TTL 键有键无消费方实锤）；②SQL 侧 drop_old_state_partitions target_tables 清单 ≠ Go 侧 stateTableTTLSpecs 清单（后者多 supplier_errors/cache_metrics）；③opslog_trimmer 路径 ≠ partition_manager 路径——R20 §五.2 误登记的直接教训。
- **54**：自愈反噬环判别——ensure 函数 ELSE 分支的 enforce/强制 AM 调用 = 漂移复活的常驻机制；换 AM 迁移必须"换函数 + 转存量"两步齐做（689 机制成文化，813 兑现）。

## §八、自审计

- A1：813 本机首跑抓到本机分区带真实数据（09=73,675 行，pg_stat 显示 0）——若按"252 侦察数据"假设单通道设计，本机会走 DETACH+DROP 丢数据；双通道（exact count 判定分流）正确兜住。统计视图不可信 → 纪律 52。
- A2：R20 §五.2「11-06 必炸」先按登记语境推演即与代码矛盾（partition_manager 无 row-level DELETE 路径、opslog_trimmer 不涉 supplier_errors），再取 252 函数体指纹（has_drop_table=t/has_row_delete=f）实证——裁决以代码+真库双证为准，未因"上轮登记"照单全收。
- A3：252 2026_10 转换计数 4,479 vs 侦察 4,433 的 +46 归因为 DETACH 窗口并发 promote 写入（守恒守卫 ≥ 语义），与 810/811 并发取舍一致，非数据异常。
- A4：814 未沿用 R20 的"现网减负"叙事——日志逐条取证（timeout 续行体 ×7/×6）推翻"425× 是视图代价"归因后，按实际收益（契约正确性）定性，修正记录保留在迁移头注与本报告 §二。
- A5：252 应用走 superuser 通道（-U postgres）规避 rolconfig 30s 击杀（纪律⑫）；事务内 10min statement_timeout + 上海钉扎自守卫；生产应用后幂等重放 + 功能活体（手动 promote 32 行）双验证；sha256 双端比对（纪律：二进制/文件身份以 sha256 为准）。
- A6：本轮未触碰并行轨道对象（rebuild-vs-inplace feasibility 文档保持 untracked）；813/814 为 DB 侧变更即时生效，三网关不依赖重部署，下次部署启动链对 813/814 幂等 no-op（双账本已登记）。

## §九、下一轮提示词（建议）

> 以本文 + round20 §八为起点。优先级：① §十 24h 收口判定采纳（automation-09e1ae20 产出；若 CTID/row-too-big/overlap 复发按 ㊿ 引用面排查新毒源）；② 存储轨道对签落地跟进：tool_usage_stats/credit_ledger/dashboard_access_events/session_module_executions/auto_route_selections/test_columnar_new 六个冻结空壳族的手工 DROP 清单 + provider_events 等单表 heap 化（§四矩阵）；③ 巨 payload 日志爆发对账（2.2MB/s 是否持续、logrotate 窗口压缩）；④ probe cycle 目标查询 ×7 贴 30s rolconfig 线的家族治理（v_routable_credential_models 双视图 join 老病，R8 遗留）；⑤ E6 催办复核。纪律沿用 ⑪-㊼ + ㊽-54。

## §十、产物与物证

- 迁移：sql/migrations/startup/813_supplier_errors_partitions_heap.sql（+.down）、814_adaptive_probe_targets_hot_subquery.sql（+.down）；三基线（sql/schema、deploy baseline、installer embeddata）+ sql/objects/views + baseline_ensure_functions_contract_test.go（heapMust 增补）同步；apply-db-revision-sequence.sh + docs/db-changelog.md 登记。
- 252 台账：schema_migrations 813/814 + llm_gateway_migration_checksums（sha256 72c39970…/73089753…，advisory lock 契约双登记）；本机双账本同。
- 日志物证：252:/tmp/pg252-r21/（ctr-snapshot-r21.log 622MB 全量快照 [03:51→10:49]、errors_head_anchored.txt、slow1s_v2.txt、durations_stmt.txt）+ recon.sql/recon2/recon3/recon4.sql + log-extract/detail/detail2.sh 分析脚本。
- 收口自动化：automation-09e1ae20-31ea（10-03 07:30 一次性，PASS 判据内嵌）。
- 本报告：docs/audit/2026-10-02-252-sql-log-audit-round21.md。
