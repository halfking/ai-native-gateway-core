# 252 PG SQL 日志审计·第十六轮（2026-10-01，SQL 日志轨道）

以 round14 §一/§四/§十目标 + round15 §八入口为起点（起点重算：R15/R16 收尾与存储轮闭合项见记忆台账，不重做）。纪律沿用 ⑪-㊱ + 新增 ㊲㊳㊴。**本轮全程只读生产，零代码、零运维动作。**

## §〇、窗口与身位

- **取证窗口**：2026-09-30 04:51 → 10-01 04:15 CST（ctr.log-20261001 418MB + ctr.log 433MB，10-01 03:18 logrotate）。共 851MB/23.4h。
- **部署身位**：154 = `2.5.8-2d750fb4-2356`、245 = `2d750fb4-2355`（**09-30 15:45 已换装**，R16 存储收尾轮 §六 healthz 旁证）、252-dev = `dd7e4527-2338`（未变，NRestarts=0）。
- **k8s-file 双前缀锚定**：容器日志 PG 条目带运行时前缀（`<ISO-ts> stderr F <PG头>`），本轮固化"PG 条目 = `stderr [FP] 20…` 起始 + `CST [pid] LEVEL:` 在行内"的锚定正典（v1 用 `^` 锚定 PG 头全数落空，v2 修正）。
- **㉕ 自取证显式扣除**：本轮窗口尾部（04:15+）的巨 payload 爆发即本会话自身流量（ZCode 经网关），cancel/慢日志计数含自身分量。

## §一、目标① REFRESH 全窗复测 —— D18 定案：抬升=7 天源窗数据体量，已被并行轨道操作移除

**≥90min 全窗（实际 23h）逐时证据（PG 侧 duration，单相位维持：每 10min 窗恰 1 analytics + 1 audit，n=6/h × 24h）**：

| 时段（09-30→10-01） | analytics avg | 定性 |
|---|---|---|
| 09-30 05-06h | 38-39s（max 117.1s） | R15 基线复现 |
| 09-30 07-14h（白天） | 20-31s | 与夜间同量级 → **"日时段负载"假设否定** |
| 09-30 15-21h | 26-45s（16h max 179.9s） | 峰值与巨 payload 活动窗重叠 |
| 10-01 00-02h | 17.9-22.8s | 单调回落 |
| 10-01 03h | 2.9-5.4s（n=3） | **翻转段** |
| 10-01 03:40 起 | **120-210ms**（journal elapsed） | 慢日志绝迹（<1s 阈值） |

- **翻转时间线**：02:43=13.5s → 02:50=10.5s → 03:10=5.4s → 03:40=0.12s（渐降 50min，与并行轨道列存改造窗 02:42-04:13 重叠，见 §五）。
- **EXPLAIN (ANALYZE, BUFFERS) MV 定义查询（04:21 低峰，90s 超时守卫）**：**Execution Time 55.4ms**；计划 = hot Seq Scan（8.1k 行，112MB）+ 2026_09 ColumnarScan（575 行）+ 2026_10（0 行），过滤后 4,422 行；credentials 关联子查询 2,629 次循环 ×0 行（provider_id 全内联，未触发回表）。
- **定谳**：单刷 36-70s 抬升的真因 = **7 天源窗数据体量**（当时百万行级），既非日时段负载（昼夜同量级）也非单纯增长（体量与时长同步塌缩）；源窗体量被 §五 的并行轨道分区改造移除后，墙钟占比 **6.7% → <0.1%**（55ms×2/600s）。D18 关闭。
- **语义注记（移交）**：routing_analytics_7d 的 7 天聚合现在实质只覆盖 ~8-12h（hot+575 行）——admin analytics 端点的 7d 历史深度受损，属 §五 操作的语义后果，需存储轨道确认是否预期。

## §二、目标① mv_data 漂移核查零条目定性

- 23h 全窗 `WITH mv_data AS` 慢条目 = **0**（两个 era 都为 0：慢 era 源百万行级、REFRESH 25-45s 期；快 era）。
- 三个部署构建（e1b73e88/dd7e4527/2d750fb4）`checkConsistency` 符号均在位（×4），代码路径无差异；journal 一致性告警 = 0。
- **快 era 解释成立**：核查查询与 MV 定义同源（EXPLAIN 55ms 同型）< 1s 慢日志阈值，不触发日志属预期。
- **慢 era 零条目仍未完全关闭**：与 R13"40×120.9s ≈ 4 条/窗"矛盾。已核 R13 原文：其窗口为**多相位过渡期**（R12-F1 部署前，3 实例错峰 tick → 每 10min 最多 3 个 winner 各跟跑一次核查 ≈ 观测的 ~4 条/窗节奏）；但单相位后应余 1 条/窗而非 0。两个候选解释登记待复核：R13 家族提取形状误归（120.9s 均值远超 REFRESH 本体 15-27s，可疑）；或离线 cron（verify-mv-consistency.sh）路径在 D6 重建窗消失。**下轮在有 /metrics 通道时核 `routing_analytics_mv_consistency_last_unix` 推进即闭环。**

## §三、目标② 763 部署通路确认 —— **关闭**

- **763 已于 2026-09-30 15:45:41 应用生产**（schema_migrations 数字链，与 759-762/801 同批 = 154/245 2d750fb4 换装启动迁移链窗口）。
- **provider_events 契约终态 PASS**：PK `provider_events_pkey` 在位；id NOT NULL + default nextval；credential_id/event_kind/payload_json/ts 保持 nullable（= F-B 修正后的 parity 形态，㊴"契约对齐禁止顺手收紧"兑现）；序列 last_value=177、is_called=t。
- **双台账**：schema_migrations 763 行 ✅ + `apply-db-revision-sequence.sh` 登记（F-A 已闭合）✅。replay no-op 守卫（conname 守卫 + 本机真库双跑 PASS）在位，下次重启窗口顺带确认即可，不再单列。
- **新事实（消解一个假风险）**：写方 `credential_actor.go:153` 与 `pg_reconciliation_store.go:286` 均**显式 `max(id)+1` 取号**，不使用序列 default → 实测 last_value=177 < max_id=236 属预期形态（15:45 setval(max)=177 后的 178-236 行走显式取号），**无 PK 冲突风险**；763 的序列/default 是契约形态而非生产取号路径。
- 数字链水位核验走 ㊳ 过滤（`version ~ '^[0-9]+$'`）；**999 是 08-13 的哨兵行**（预留位），numeric_max=999 非异常。

## §四、目标⑤ provider_events 活写速率归因 —— 关闭

- **写方**：`domains/providerprofile/alert_engine.go:110/126` → `credential_actor.RecordEvent`；event_kind 仅两族：`profile_auto_disable_advisory`（161）/ `profile_auto_enable_advisory`（39）。
- **写入形态**：可用性巡检**批量**写（每波 ~30 行 = 每 credential 一条）；09-30 共三波（03h=31、15h=30、21h=29），payload = "可用性 xx 低于 50，立即禁用"（dimension=availability，score 32-49）。
- **速率归因**：R14 F-H"1.5h 146→177（+31 行）"= 单波巡检批，非泄漏非风暴。**09-30 21:49 后静默 14h+**（分数恢复或巡检波空）。写路径 PK 全程无冲突 = 763 契约活体验证持续成立。

## §五、P1 登记：并行轨道夜间存储改造 —— sessions 列存化打断核心 upsert + 53GB bodies 提前清空

**本轮只读取证，未介入。事实链（全部实测）**：

1. **sessions 全分区族已列存化**（relam 215879：2026_07/08/09/10/11 + default 六分区；2026_09 保留 44MB 数据，2026_10/11/default 为 80kB 空壳）。
2. **生产核心写路径持续失败**：`INSERT INTO public.sessions (…)` 带 ON CONFLICT 的 upsert 在列存分区上必炸 `columnar_tuple_insert_speculative not implemented`——**当前 ctr.log 内 3,509 次（03:18→04:52，~39/min），最近一条 04:48:06（本轮取证时仍在持续）**。R16 存储轮文档自己写过该陷阱（"session_bodies 因 promote ON CONFLICT 不可列存"），sessions 家族转换未规避。
3. **后果面**：sessions_2026_10 为空壳 → 转换后（~03:0x 起）新会话元数据疑似**零落库**（请求面热表仍在正常写入，~1000 行/h，故数据面请求处理本身存活；sessions 主表静默丢失需应用层确认是否被吞错）。
4. **request_logs 家族三分区全列存**（392kB-1.3MB）：2026_09 元数据仅剩 575 行（ts 09-30 18:54-19:55，= 换壳后 promote 回填）；**archive 0 行 = 未归档即销毁**；19:55→20:46（hot 最老行）约 50min 元数据行缺口 = 随旧分区销毁。
5. **request_logs_bodies_2026_09（53GB/1,404,892 行）提前 7 天清空**：现为 16kB 列存空壳、count=0；无 copy-out（archive 0 行 + 无 `_old` 孤儿 + 慢日志无 >1s 拷贝语句）→ 换壳销毁。与 R16 台账"非空 2026_09 随 TTL 10-08 退役"计划**直接冲突**，且 09-24→09-30 未到期 bodies 一并销毁。bodies_2026_07/08 已不存在（round43 基线对账 MISSING 清单旁证）。
6. **容量影响**：库 119GB→**57GB**，根盘 93%/15G→**62%/74G**——D19 容量赛跑被（未授权性待确认的）操作提前解除，10-08 TTL DROP 前提已失效。
7. **活动窗证据**：02:42-04:13 columnar 族错误 923 条（tuple_lock ×199/speculative ×213/CTID ×17/candidate_failure_logs_columnar_old drop ×81/`request_logs_2027_05` 索引访问方法错误——远期分区预建试验）；deadlock ×18（03:46/03:54，**rel 96395843 = session_bodies_2026_11** AccessShareLock 等待——远期分区上 reader 被 ALTER 阻塞）。
8. **bodies promote 现状**：bodies_hot 最老行 09-30 18:42（10.1h 滞后，较 01:37 的 11.95h 回收）；2026_09=0/2026_10=0 → 09-30 夜间 eligible 行未落分区；speculative 错误是否含 bodies promote 待 10:00-14:00 首批窗口验证（17:30 自动化正好覆盖）。

**移交/建议**：①请存储轨道/用户确认本次改造的授权性与台账补账（53GB 销毁、sessions 破坏性换壳均无归档）；②**P1 修复方向已由 R16 文档自证：sessions 家族（ON CONFLICT upsert 路径）不可列存，应回退 heap 或改写 upsert 语义**；③17:30 自动化（automation-5c6e92e1）前提已被推翻（其步骤 5"2026_09 count 对照 1,404,892"将为 0、53GB 全扫禁令对象已消失）——该自动化按"如实报告"纪律会自证，无需改写；④10-08 TTL DROP 确认轮提示词前提（库 119GB/分区 53GB）同步失效。

## §六、常规慢查/错误对账（23h，头锚定正典）

| 家族 | 计数/速率 | 判定 |
|---|---|---|
| user-request cancel | 4,800（≈208/h，R15 57/h 的 3.6×） | 巨 payload 窗口加宽关联（含本轮自身流量 ㉕），非缺陷 |
| statement_timeout cancel | 297 | 与 REFRESH/analyze 预算内完成相伴 |
| analyze_llm_gateway_table_stats | 63 条 40-131s（~3/h） | 10min pin 预算内，FIX-3 形态正常 |
| **E6 pocket $4** | 282 ≈ **12/h 稳态、全天化**（R15 11/h，R14 F-C"夜间节奏相当"措辞再修正为"全天 5min 节奏"） | 未修复，维持移交 pocket 轨道 |
| **F2-smms（改判）** | **daily_kline `SELECT MAX(date)` ×74 复出**（JDBC `_pg3_3`，1-30s，03:05-03:51 活跃）+ fundamentals/limit_up_temp 错误仍 0 | R15"清零"结论**改判为低频复出**，重开观察 |
| E7 smm 认证失败 | password authentication failed ×4 | 复出，登记 |
| **vector 24h 复核** | 18 条全部 ≤ 09-30 06:31:26（修复前），其后零复发 | **PASS，R15 修复项关闭** |
| FATAL | connection to client lost ×576 / parallel worker admin ×26 / root 不存在 ×2 | cancel 风暴伴生，无新增签名 |
| 42703/current_role/jsonb hex/folded 23505 | 0 | R15 五项对账全部维持 ✅ |

## §七、目标③④ 状态落账

- **③ S4 停写**：本机触发条件未变，仍**待用户拍板**（台账 §三已评估）；252 生产观察期未启动，无需重建 cron。
- **④ 外部轨道**：E6 维持移交（12/h 全天化）；F2 改判重开观察（daily_kline ×74）；E7 ×4 复出。无回执通道，以频次/签名为回执状态。

## §八、纪律

- 沿用 ⑪-㊱；㊳（数字链 `^[0-9]+$` 过滤）本轮实测三处（水位/763/999 哨兵）；㊴（禁止顺手收紧）在 763 契约终态复核中兑现。
- **㊲ 未触发**（本轮非部署轮），登记：下次部署窗口验收必含二进制 sha256 双端比对。
- **新增候选㊵**：巨 payload 时代 PG 日志 duration 提取禁止贪婪 `sub(/.*duration: /)`（v2 取证 1,015 条假阳 >30s 即此污染），必须 `match` 首个 `duration: [0-9.]+`——并入 ㊱ 取证纪律族。
- **新增候选㊶**：容器日志 PG 条目锚定正典 = `stderr [FP] 20…` 起始 + `CST [pid] LEVEL:` 行内（k8s-file 双前缀），`^` 锚定 PG 头在容器日志上全数落空。

## §九、自审计（A1-A6）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | v1 日志扫描全零（vector/E6/smm/fatal=0）→ 疑错误消失 | **锚定错位**：k8s-file 双前缀使 `^` PG 头锚定落空；v2 修正后还原真实计数。教训=㊱ 族补容器前缀形态 |
| A2 | v2 扫出 1,015 条 ext-protocol >30s → 疑新慢查风暴 | **贪婪 duration 解析假阳**（payload 内 "duration:" 文本）；v3 首匹配解析后 ext30=0。教训=候选㊵ |
| A3 | bodies_hot 滞后 10.1h 且 2026_09/10 均 0 行 → 初判 promote 全停 P1 | 结合滞后回收（11.95→10.1h）与 journal migrated 行数，改判"部分失败/部分成功形态待 10:00 窗验证"；把断言降级为 §五.8 的待验证项，不虚构结论 |
| A4 | sessions 失败疑本轮自身流量引发 | 自身流量只经 LLM 请求路径（body INSERT），不产生 DDL/列存错误；错误始于 03:0x（本会话首轮请求前），时间轴排除自归因 |
| A5 | numeric_max=999 疑台账污染 | 999 = 08-13 哨兵行（预留位），非污染；不误报 |
| A6 | "请求面存活"推断 | 以 hot 表持续写入（~1000 行/h 至 04:46）+ promote journal 为证；sessions 主表是否静默丢失**不下断言**（需应用层错误吞没行为确认），如实登记 |

## §十、下一轮提示词（建议）

> 以本文 §一-§七为起点。优先级：
> 1) **P1 跟踪**：sessions 列存 upsert 失败（§五.2）是否被存储轨道回退/修复；sessions_2026_10 是否恢复落库；若 10:00-14:00 bodies 2026_10 首批 promote 落地则 §五.8 解除，反之确认 bodies promote 也炸；
> 2) **17:30 自动化对账结果**采纳（其前提已变，读其产出时对照本文 §五）；
> 3) mv_data 慢 era 零条目闭环：核 `/metrics` `routing_analytics_mv_consistency_last_unix` 推进 + R13 提取形状复核；
> 4) E6（12/h 全天化）移交回执、F2 daily_kline 低频观察续期；
> 5) 常规慢查/错误对账 + cancel 208/h 与巨 payload 关联持续性。
> 纪律沿用 ⑪-㊶（㊵㊶ 为本文候选，待并入正典）。

## §十一、产物与物证

- 本文档（零代码变更）；服务器侧 /tmp/r17_log.sh、/tmp/r17v3_log.sh、/tmp/r17v3_*.txt、/tmp/pg252-20261001-r17-forensics-p{1,3,4,5}.sql、-explain-p2.sql（文件通道，stdin 零污染）。
- EXPLAIN ANALYZE 全文（55.4ms 计划）已摘录 §一；所有计数可由 /tmp/r17v3_*.txt 复算。
