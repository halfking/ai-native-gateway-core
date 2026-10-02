# 252 PG SQL 日志审计第二十轮（2026-10-02）—— 三 P1 根修轮：toastless-heap 治愈（810）/ 分区边界网格重建（811）/ model_probe_runs 列存毒源清除（812）

以 round19 §八 + round18 §九为起点（起点重算：origin/main = 77837b013）。本轮 = 每日 06:00 自动化（automation-f0a3bf63）第 16 次运行。
取证窗口 = 2026-10-01 11:32（R19 快照终点）→ 10-02 06:04+（ctr.log 611MB + 归档 ctr.log-20261002 606MB 过滤切片，合计 647MB/18.5h）。所有时间戳为 252 服务器 CST。
修复经本机 llm_gateway 真库实跑先行（途中抓出并修复 3 个迁移缺陷），再 252 生产预应用 + 台账登记（810/811/812 双账本 + checksum）。

## §一、三大 P1（全部真库定位 + 修复 + 生产功能验证）

### P1-1 session_bodies promote 死亡 14h（row-too-big ×59）→ 迁移 810
- 形态：每小时 promote tick 全灭 `ERROR: row is too big: size 36112, maximum size 8160`，×59/窗口（16:06 起每小时 ×3 实例）；hot 积压 4,227 行（最老 10-01 07:38）无出口。数据零丢失（全在 hot，WAL 持久）。
- 根因：`session_bodies_2026_10` 与 `session_bodies_default` 是 **heap 但无 TOAST 表**（reltoastrelid=0），大列 attstorage 仍 'x' —— 超 8160B 行 INSERT 即炸。时间线夹逼：promote 最后成功 ≈15:38（hot 最老=15:38−8h 保留=07:38）、首错 16:06:56，toast 丢失窗口 = 10-01 15:40-16:06，与并行存储轨道 ops_probe 实验起始时刻吻合（其可行性文档自述只读生产+沙箱，但对空分区 _10 的列存↔heap 往返未记录——列存表天然无 TOAST、`SET ACCESS METHOD heap` 直改不重建 TOAST = 已知机制（fact 100）。归因：高度置信、未归档）。
- 全库扫描：21 个 heap-without-toast 分区。**810 治愈全部 8 个空分区**（session_bodies_{2026_10,default}、sessions_{2026_07,08,11,default}、session_turns_default；session_bodies_2026_10 重建后 reltoastrelid=96868643 ✓ + lz4 五列补齐）；13 个非空分区 NOTICE 跳过登记（§五）。
- 验证：手动 promote 一批 500 行成功（3.8s）、2026_10 收首批 371 行；07:06 网关 tick 后 row-too-big 归零，hot 4,391→1,653、2026_10=2,749（+2,000/30min 排水中）。

### P1-2 分区边界 473 型污染复活：ensure 2026_11 永远建不出来（overlap ×8）→ 迁移 811
- 形态：`SELECT ensure_routing_decision_log_partition('2026-11-01 …')` / `ensure_request_logs_partition(…)` 自 10-01 15:05 起每小时失败 `partition "…_2026_11" would overlap partition "…_2026_10"`（rdl ×5 + request_logs ×3）。**2026-11-01 08:00 CST 起两表全部 inserts 将硬失败（两表在 252 均无 DEFAULT 分区）——30 天 P0 时间炸弹。**
- 根因：252 的 request_logs/routing_decision_log 2026_09/10 分区边界是 **UTC 零点**（pg_get_expr 渲染 `'…T08:00:00+08'`）——473 型污染（687 的姊妹病史；687 当年本机执行时 request_logs 仅 default 分区被守卫跳过、252 台账缺登记从未修）。而正典 ensure 函数（694 时区钉扎）`SET LOCAL TIME ZONE 'Asia/Shanghai'` 后裸日期 `%L` 字面量 = **+08 零点** < 存量上界 → 42P17。
- 修复（687 两遍通道改造，存储引擎跟随原分区）：4 个污染分区 DETACH→bak→按 +08 零点重建（rdl `USING columnar` 列存单族保留；request_logs heap，父表分区索引自动建叶）→ bak 经**父表**回灌（污染边界吞进的次月头 8h 行自动路由：request_logs 6,949 行→2026_10、rdl 3,914 行→2026_10）→ 计数守恒校验 → DROP bak。守恒实测：rl 11,256+9,837 / rdl 444,052+2,967 全部落位（rl09=4,307/rl10=16,786/rdl09=440,138/rdl10=6,881 逐分区吻合）。
- 炸弹解除实证：迁移末尾调用正典 ensure 预建下月分区——`request_logs_2026_11`（heap）、`routing_decision_log_2026_11`（enforce_columnar 触发器自动转列存=正典单族机制在位工作）双双建成；网格 08/09/10/11 连续 +08 零点（顺带治愈 08→09 的历史 8h 缝隙走向）；分区索引叶 indisvalid 0 无效。历史遗留：09-01 00:00-08:00 CST 缝隙行当年已拒收（8h 数据缺口，超出窗口无法量化，登记）。

### P1-3 探针状态更新器 CTID ×114——真根因补全（R19 归因修正）→ 迁移 812
- 形态：`UPDATE model_probe_state … NOT EXISTS(… model_probe_runs …)` ~9-10min 节奏失败 ×114/18.5h（精确 6/h）。R19 结论"model_probe_state 11:26 闭合"被证伪：11:36 起复发贯穿全窗；06:00 前后并行会话把 model_probe_state 转回 heap 后仍间歇失败（07:01 成功、07:06 失败、表 heap）。
- 真根因：**model_probe_runs 的 2026_09/10/default 分区是 columnar**（10-01 事故漂移残留，不在 R18 正典单族内）。citus columnar 拒绝任何含 ColumnarScan 节点的 UPDATE 计划——与 UPDATE 目标 AM 无关、与列存分区是否为空无关（分区剪枝按边界，created_at>now()-2h 恒命中当月分区）。间歇性 = 计划剪枝抖动。probe 状态机死亡 ~20h（无 recovering 流转）。
- 修复：三列存分区全为 0 行空壳（活数据在独立双写表 model_probe_runs_hot=heap 20,946 行）→ 812（806 同款）重建 heap，零数据风险。
- 验证：812 后（07:14+）CTID 零复发、07:20:51 更新器成功写、**state 机复活**（recovering=436 行=健康流转重启）。

## §二、错误 SQL 全景（头锚定 + 二段计数，纪律㊾）

| 锚定家族 | 数 | 处置 |
|---|---|---|
| inconsistent types $4 @char135（E6 pocket 库 scheduled_tasks） | 223 | 外部移交维持（精确 12/h 恒定节拍） |
| canceling statement due to user request | 153 | 应用 ctx 取消基线形态，不修 |
| **UPDATE and CTID scans（model_probe_runs 毒源）** | **114** | **812 根修 ✓** |
| FATAL connection to client lost | 83 | cancel 伴随弃连，不修 |
| **row is too big（session_bodies promote）** | **59** | **810 根修 ✓** |
| canceling statement due to statement timeout | 27 | probe-target/canonical 视图家族贴 30s rolconfig 线（§三），登记 |
| FATAL terminating parallel worker | 20 | 并行 worker 随 leader 终止，不修 |
| **partition would overlap（ensure）** | **8** | **811 根修 ✓** |
| ops_probe/idxt_*/column 探错/length(jsonb)/GREATEST/OOM ×3/263.8s COPY | ~40 | **并行轨道测量自噪音全剔除**（其可行性文档自证：OOM=alter_table_set_access_method MaxAllocSize 机制实测、COPY 50000=263.78s=本轮 max 慢查、沙箱已 DROP CASCADE） |
| smm 认证失败 ×2 / integer out of range ×1 | 3 | 外部轨道（E7/F2，F2 基本清零） |

## §三、慢 SQL 普查（≥1s：n=4,803 / sum 16,483s ≈ 24.7% 墙钟 / med ~2s / max 263.8s）

无新增产缺陷签名。质量分布（多行语句续行锚定，LC_ALL=C）：`WITH latest_bucket AS` ×1,789/2,915s（R12-F2 登记家族，改写已被 EXPLAIN 否决）、`SELECT analyze_llm_gateway_table_stats` ×57/2,037s（D7' 登记家族）、probe-target 家族（`WITH targets AS`+`SELECT cmb.credential_id…`）×425/2,910s 贴 30s 线（与 N2 探针视图死列同族，待拍板项）、credential_model_index_hot INSERT ×262、promote 家族 ×~30、canonical 视图聚合散布（D15 等）。主机 load 6.1/4 核（中度饱和：ursm 21GB 快照负载+测量时代余波），全形状一致膨胀成分与 R10 纪律㉚ 归因一致。**本窗慢查大头 = 已登记家族 + 中度饱和，无独立新签名**；max 263.8s = 并行轨道 COPY 测量（自噪音）。

## §四、对账与身位

- 部署身位（10-01 并行轨道已换装）：**154=e469c023-2383 ready:true、245=bfa3530e-2380 ready:true、252-dev=cc0e977f-2373 ready:false**（无 Redis 结构性 not_ready 常态）。810/811/812 已入 252 台账，下次部署 boot 链幂等跳过。
- R19 §八 优先级逐项：①17:30 自动化产出已采纳（bodies-columnar-followup.md §九：2026_10 首批 6,591 行/月轨迹 0.8-1.7GB 达标、drawer 125.4ms、lz4 附件 ratio<1 结案）✓；②sessions_2026_10 斜率 461@11:32→3,630@06:52（+155/h 健康）✓；③legacy outbox dead 147=**0**（已清）✓；④G2/S4 事故豁免日注记——本报告 §一 P1-3 即 model_probe_state 事故终账（20h 死亡窗内 probe 状态流转缺失，恢复后 436 recovering 补偿中）；⑤E6 12/h 维持、F2 清零 ✓；⑥本报告即 06:00 轮产出。
- 迁移尾段对账：810/811/812 预应用后，启动链（max=809 现装构建）下次部署对 810-812 幂等 no-op；252 schema_migrations 数字链水位 → 812。

## §五、登记不修（latent 面，待存储轨道对签/拍板）

1. **13 个非空 toastless-heap 分区**（810 NOTICE 跳过）：usage_facts_default 274k+6 日分区、stats_event_inbox_default 78k、sessions_2026_09/10、session_censors_2026_09、session_memora_2026_09、auto_route_selections_2026_10——行均小行未触发；处方=月分区轮换到空时由 810 幂等重放兜底，或运维窗口按 806/562 通道重建。
2. **列存残留全景 ~59 关系**（AM 普查）：正典设计内=request_logs_bodies 三分区（765）+ routing_decision_log 三分区+archive_default；**漂移残留=约 20 族**（credential_model_index 09/10/11、supplier_errors 08-11、handoff_logs 09/10/11/default、tool_usage_stats 08-10、session_module_executions 07-09/default、auto_route_selections 08/09、cache_metrics、credit_ledger、dashboard_access_events、model_probe_runs（本轮已清）、provider_events、model_offer_events、price_change_events、tool_call_events、system_probe_runs_default、test_columnar_new 残留）。**P2 时间炸弹登记：supplier_errors 90d TTL 是 Row-level DELETE（opslog_trimmer 路径，694 头注同族处方），2026_08 分区 ~11-06 出 90d 窗即触发列存 DELETE 必炸**——须在 11-06 前处置（转 heap 或 TTL 路径改分区 DROP）。
3. N2（recent_passive_failures 恒 0 探针视图死列）维持待拍板；本窗 probe-target 慢查 ×425/2,910s 是其代价实证。
4. 252 DB 的 `ensure_request_logs_partition` 函数体含 10-01 并行轨道手工修改（columnar 感知 GIN 跳过注释），与仓内正典版本漂移——归属 R44 存储轨道收编（本轮不代改，功能在位无害）。

## §六、新纪律候选

- ㊿ **UPDATE/DELETE 路径的列存毒源按"查询引用面"排查而非"目标表 AM"**：citus columnar 拒绝含 ColumnarScan 的 UPDATE 计划，NOT EXISTS/FROM 里任何一个列存分区都足以击杀目标表为 heap 的更新器（本轮 P1-3 的 R19 归因修正）。
- 51 **分区边界网格必须以 pg_get_expr 渲染文本审计**（'08:00:00' 字样=473 型污染指纹）：ensure 裸日期字面量的语义随会话时区漂移，任何非正典会话（UTC pgx 会话/手工 psql）执行 DDL 都可能引入网格分裂，月尾 ensure tick 是唯一报警器。

## §七、自审计

- A1：811 初版两处缺陷均被**本机真库实跑先行**拦截（`to_date()+1`=+1 天不是 +1 月，两处；回灌目标误写分区名而非父表）——"新迁移必须存量真库实跑"纪律直接兑现，252 零损伤。另 RAISE 占位符多写一个 `%`（5 占位 4 参数）psql 编译期报错拦截。
- A2：P1-1 初判"promote 自 R17 晨即死"被 hot 最老行时间戳证伪修正（15:38 最后成功 + 16:06 首错夹逼）；P1-3 初判"model_probe_state 转回 heap 即愈"被 07:06 复发证伪，顺藤查出 model_probe_runs 毒源——两层归因修正均以生产日志+真库复核为准。
- A3：OOM×3 与 max 263.8s 慢查先按自噪音候选拿到并行轨道文档自证（ops_probe 语句指纹+时间窗吻合）再剔除，未计入生产发现。
- A4：修复全程未触碰并行轨道对象（其 ops_probe 沙箱已自清；feasibility 文档保持 untracked 不动）；三迁移均先 810→811→812 顺序真库验证后应用，全部幂等可重放。

## §八、下一轮提示词（建议）

> 以本文 + round19 §八遗留为起点。优先级：① **supplier_errors 90d TTL 时间炸弹**（§五.2，11-06 前处置：列存分区转 heap 或 TTL 改整分区 DROP，须与存储轨道对签）；② N2 探针视图死列拍板（probe-target 家族 425×/2,910s 代价已实证）；③ 13 个非空 toastless 分区的轮换兜底复核（810 幂等重放观察）；④ 列存残留 ~20 族的 AM 归一决策（与 R44 存储轨道对签，正典单族基线维持）；⑤ E6（223/18.5h=12/h）外部移交催办；⑥ 更新器/ensure/promote 三 tick 的 24h 零错误对账。纪律沿用 ⑪-㊼ + ㊽-51。

## §九、产物与物证

- 迁移：sql/migrations/startup/810_heap_partitions_toastless_heal.sql（+.down）、811_partition_bounds_shanghai_midnight_repair.sql（+.down）、812_model_probe_runs_partitions_heap.sql（+.down）；登记=scripts/apply-db-revision-sequence.sh + docs/db-changelog.md。
- 252 台账：schema_migrations 810/811/812 + llm_gateway_migration_checksums（sha256 双登记，advisory lock 契约）。
- 日志物证：252:/tmp/pg252-r20/（win_all.log 647MB 切片、error_families/durations/slow 形状提取）+ /tmp/pg252-r20-audit-current-1002.log 快照；分析脚本 /tmp/pg252-r20-{analyze,context,context2,slowfam2}.sh。
- 本报告：docs/audit/2026-10-02-252-sql-log-audit-round20.md。
