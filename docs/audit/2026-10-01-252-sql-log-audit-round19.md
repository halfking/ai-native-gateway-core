# 252 PG SQL 日志审计·第十九轮（2026-10-01，SQL 日志轨道——R17/R18 根修独立验证轮）

以 round17 §九 + round18（同日收口轮，并行会话）为起点（起点重算：origin/main = 2408f12ce）。**本轮我方全程只读（零 DB 写、零运维动作）**，定位=对 R17 根修与 R18 收口做**独立证据链验证**，并闭环 R16 遗留的 mv_data 慢 era 悬案与 17:30 自动化处置——四项均为 R18 未覆盖的盲区。与 R18 的重叠计数全部做了交叉对账（§五，两会话独立提取、数字一致）。纪律沿用 ⑪-㊼，新增候选 ㊽㊾（§七）。取证窗口 = ctr.log 03:18:37 → 11:32（568MB，03:18 轮转后未再轮转），尾段增速 230B/s ≈ 基线。所有时间戳为 252 服务器 CST。

## §一、P1 终验（R16 §五 / R17 §九①② 的独立证据链）—— 全项 PASS

AM 全景普查（24 族正则 + 目录直查，11:0x）：

| 项 | 实测 | 判定 |
|---|---|---|
| sessions 全族（07-11+default） | **全 heap**；sessions_2026_09=163,357（较 R17 收口 +238 续增）；**sessions_2026_10=349@11:05 → 461@11:32**（+112/27min，partition_date 全=2026-10-01） | ✅ upsert 恢复，UTC 翻日后正常收行（R17 §九①、R18 §三 307→357 斜率外推吻合） |
| speculative 家族（sessions/stats_inbox/usage_facts upsert） | 全窗 8,579；逐时 04h=4855/05h=2713/06h=1011，**07h 起零**（终验 11:32 复核仍零） | ✅ 根修生效 |
| **bodies promote（R16 §五.8 待验证项）** | **request_logs_bodies_2026_09=4,408 + 2026_10=3,898@11:05 → 4,032@11:32**（持续入列存分区）；hot 5,276 行、oldest=02:57（≈8h 名义水位，R16 的 10-12h 滞后=事故积压已排空） | ✅ **§五.8 解除**：promote 未炸（裸 INSERT 对列存合法），首批无需等 10:00-14:00 窗口，积压已清 |
| 写链（冻结 05:31-07:05） | turns_hot/sbodies_hot newest=11:04:34、232 turns/h | ✅ 复活 |
| session_aggregate_outbox | done=61,070、last_done=11:05:03、**dead=0**（R18 §三 N1 补清 335 后无新增死行） | ✅ |
| usage_facts / stats_event_inbox | usage_facts_20261001=5,256 行（heap）、20261002（ensure 预建）=heap（无 boot 复发源）；stats_event_inbox_default=47MB heap | ✅ |
| 触发器名单 | `columnar_insert_only_parents()` = **{routing_decision_log}**、enforce_columnar_trigger enabled='O' | ✅ 与 R18 §二 三方对账一致，维持正典 |
| `_columnar_old` ×3 | 目录已无（R18 §一 重挂视图后 DROP，无 CASCADE 阻塞=硬依赖清零） | ✅ 采信 R18 证据链 |

**结论：R16 §五 P1、R17 §九①、R18 §一/§三 关闭获独立验证。** 2026_09 bodies 的 4,408 行为 promote 从 hot 回填值，1,404,892 行本体维持"换壳销毁、不可恢复"定性（R16 §五.5）不变。

## §二、残余 CTID 插曲（R18 未覆盖）—— model_probe_state 更新路径，11:26 后闭合

- CTID 家族（`UPDATE and CTID scans not supported for ColumnarScan`）全窗 3,003：05h=2,937 为 R17 已修的 session_turns claim 死循环；**修后残余 6-8/h**（06h=9/07h=8/08h=6/09h=6/10h=7）。
- **目标定位**（k8s 续行块 -A 通道，㊱ 强化款）：`WITH static AS (…routing_policy featured_models…) UPDATE model_probe_state mps FROM credential_model_bindings…` = **自适应探针状态更新器**，~9min 节奏与残余速率吻合。
- **时间线**：…→11:06:54→11:11:00→**11:20:51（最后一条）**→ 11:26:58 model_probe_state heap 主文件出现成功更新写入（mtime）→ 11:32 终验零复发。**回退完成窗口收敛至 11:20:51-11:26:58**；model_probe_state 终态=heap、999 行数据完好。
- **归属开放（如实登记）**：我方零写动作；并行收尾会话全部**归档脚本中零 ALTER TABLE**（glob 全量 grep）；其 A4 自噪账亦无此条 → 最佳解释=该会话 11:21-11:26 间未归档的临时 ALTER，归属留其复核，不冒认（A4）。
- **微疑点**：①model_probe_state 不在 R17 §〇 21 族名单内，若其确曾列存则实际转换面 >21 族；②`pg_stat_user_tables` 形态 n_tup_ins=1 / n_tup_upd=4,237 与 999 行存量矛盾（若为 SET ACCESS METHOD 重写，计数器应保留历史 insert 量）——两种形态一并留存储轨道复核。
- **41 个残留列存分区清单**（24 族普查口径；R18 §二"53 个 columnar 关系"为全库口径，含 routing_decision_log 家族与 test_columnar_new 残留，两者一致不冲突）：request_logs_bodies_{09,10,11}（**765 设计内**，本轮 3,898→4,032 行活体验证裸 INSERT promote 安全）、supplier_errors_2026_09（21MB，promote 在列存上已跑通）、credential_model_index_{08,09,10,11}、auto_route_selections_{08,09}（旧月只读）、其余为 16kB 空壳/dormant 月壳。**当前安全证据 = 07h 起 speculative 全族零复发 + promote 裸 INSERT 活体证据**；**潜伏面** = 任何未来 ON CONFLICT/UPDATE 路径落到这批分区即重演 sessions 事故——归入 R18 §五 P1-P3 防漂移处方一并处置。

## §三、mv_data 慢 era 零条目——闭环（R16 §二遗留终审，两轮均未关闭）

1. **/metrics 活性**（252-dev :8780）：`routing_analytics_mv_consistency_last_unix` 两视图 = 1,790,823,600 vs 服务器 epoch 1,790,823,608——**核查完成于 8 秒前**，10min 节奏推进 ✓。252-dev 11:00:00 journal：REFRESH elapsed=254.6ms/85.0ms（快 era 双确认）。
2. **R13 提取形状复核（实锤证伪）**：原始快照（/tmp/pg252-r13-audit-0929-snapshot.log，44MB，09-29 06:03）按正典锚定重提取——真慢条目头含 mv_data 者 **0 条**；80 处 mv_data 提及全为巨型污染 INSERT 的续行块（样例=裸 `WITH mv_data AS (` 块体——R13 当轮转录污染把审计 SQL 文本经 body 记录再入日志）。**R13 的"40×120.9s"从未是真实语句。**
3. 签名有效性排除：`WITH mv_data AS` 在 R13 窗口构建（774ad643/e1b73e88）中已存在（git 验证，09-01 1b257be72 引入）——零条目非签名失配。
4. 真核查慢条目三窗全零（R13 窗/R16 23h/今日）；核查器随 REFRESH 每 tick 常驻运行（materialized_view_refresher.go:388-411）。**残余矛盾**（源窗大时按成本推理应 ≥1s 却零条目）不强行归因，以"健康判据 = metric 推进而非日志条目"了结（A5）。
5. **R13 墙钟修正**：其 REFRESH 家族 10.4% 含污染项 40×120.9s=4,836s → 修正后 (440.6+49.0)/5880 = **8.3%**（过渡期口径）。

## §四、17:30 自动化（automation-5c6e92e1）处置—— R16/R17 均未处置

- 状态：一次性今日 17:30 触发，11:00 时 runCount=0 **尚未运行**，产出采纳移下一轮。
- 前提对照（其 prompt vs 本轮实测）：步骤 5"2026_09 count 对照 1,404,892"将实测 **4,408**（promote 回填值；本体销毁定性不变）；df 基线 93%/15G → 实际 **61%/76G**；"53GB 全扫禁令"对象已消失（现 7.9MB 列存壳+回填）。步骤 1-4（2026_10 对账/月轨迹外推/抽屉冷读/lz4 附件列异常取证）**全部有效且更有意义**（现已有 4,000+ 行可抽）。
- **处置：保留运行**（R17 §五.2 取消建议暂不执行——剩余步骤价值完整，按"如实报告"纪律自证过期基线；跑完由用户决定是否删）。

## §五、与 R18 的交叉验证 + 常规对账补充

两会话独立提取，计数一致（互为取证质量佐证）：E6 03-10h 合计 **92**（R18 记 91，差 1=窗界取法）≈ 精确 **12/h=5min 节拍全天化**；F2 daily_kline **46** 条慢（R18 记 47）；cancel 曲线形态一致（本轮逐时 03h=2819/04h=2975/05h=1823/06h=1656/07h=474/08h=2/09h=8/10h=7，与 R18 30min 桶同形）——**R17"事故驱动"归因定案，修复后基线 ~7/h**（低于 R16 208/h，巨 payload 客户端负载亦回落）。E7=0 安静一致。

本轮独有对账补充：

- **慢查真普查**（㊱ 条目起始锚定 + ㊵ 首匹配 + 单位修正后）：**n=5,164 ≥1s，med 1.74s，p90 10.4s，max 535.1s**（max 与 R17 档案精确吻合）。家族：prepared-stmt bind/parse 变体 ~4,586（task_assigner/orchestration 形状，饱和期膨胀）、credential_model_index_hot INSERT ×175、daily_kline ×46、ping/commit ×72（池探活饱和膨胀）、analyze ×26、promote 族各 8-12、REFRESH analytics ≥1s ×11。全部事故/饱和归因，无独立新签名。
- **R16 五项（二段计数口径）**：current_role 真阳性 0（唯一命中=`set_config('app.current_role',…)` 合法 GUC）；jsonb hex 0；vector .so 0；folded 23505 0；42703 原始命中 86 → 真锚定列错误 17 条、全在 05-07h 事故探错窗（created_at ×10/session_id ×3 等探索形状）——与 R18 A4 自噪账（P3 执行 ×4 + 并行探测）互洽，非生产签名。原始 substring 计数被负载内嵌文本膨胀 5×（㊾ 案例）。
- statement_timeout 759/deadlock 36/aborted 31/lock timeout 8/row-too-big 7/overlap 8：均为 R17 已档事故噪声，无新增。
- **provider_events**：max(id) 236→269（+33=单波巡检批，R16 §四 形态复现）；契约健康。
- **G2/S4 台账**：今日=事故归因日（写冻结 + R18 N1 的 335 死行群），日对账与 S4 gate 评估须注记豁免；S4 停写仍待用户拍板（R16 §七 不变）。
- 巨 payload 自噪为常态：本轮普查首过的 4,547 处下锚不足命中即其活体（审计会话自身流量 body 记录再入日志）。

## §六、纪律

- 沿用 ⑪-㊼；㊵㊶ 本轮实战转正（续行块取证走 grep -A 邻行通道、duration 一律首匹配）。
- **新增候选㊽**：慢查汇总脚本必须以 **ms 为单位显式换算**（打印 `d/1000` 或标注单位）——本轮 med 1,735.9ms 被裸当秒差点产出"中位 29 分钟"假信号，靠 max 535,056ms ≡ R17 档案 535.1s 单位守恒交叉验证才纠正。
- **新增候选㊾**：错误家族结论禁止只做 substring 计数（42703：86 原始 vs 17 真锚定；current_role：1 命中 vs 0 真阳性）——负载内嵌文本时代必须"锚定头提取 + 形状分层"二段计数（㉘ 的容器日志时代强化）。

## §七、自审计（A1-A6）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | 首轮慢查普查 med 1735.9"s" → 疑慢查爆炸 | **单位 bug**（值本为 ms），修正后 med 1.74s/max 535.1s 与 R17 档案吻合；教训=㊽；不虚构"慢查风暴" |
| A2 | 空头慢查家族 4,547 条疑转录污染复发 | 抽样定性=扩展协议 `bind/parse stmtcache_` 变体（提取正则漏 bind/parse 前缀），饱和膨胀良性；现行窗口无污染 |
| A3 | 42703 ×86 → 疑新列错误风暴 | 二段计数=17 真锚定、全为事故探错窗自噪、与 R18 A4 互洽；教训=㊾ |
| A4 | model_probe_state heap 但 11:11/11:20 仍报 CTID → 归因矛盾 | 如实收敛时间线（最后错误 11:20:51、首次成功写 11:26:58），归因开放（最佳解释=并行会话未归档临时 ALTER），不冒认不虚构；两形态疑点留档 |
| A5 | 慢 era 真核查零条目与源窗成本推理矛盾 | 不强行归因，以 metric 推进为健康判据了结，残余矛盾显式留档 |
| A6 | 我方只读声明核验 | 本轮 SQL 全为 SELECT/目录查询（SET statement_timeout 会话局部），bash 全为 grep/awk/sleep/curl/stat——零写通道 ✓ |

## §八、下一轮提示词（建议）

> 以本文 + round18 §九为起点。优先级：① 17:30 自动化产出采纳（对照本文 §四：步骤 5 将报 2026_09=4,408/df=61%，属预期自证非异常；跑完由用户定去留）；② R18 §九 ①-⑤ 照单（N2 探针视图死列拍板、防漂移 P1-P3 实施、legacy dead 147 盘点、基线对签、test_columnar_new 清理）；③ model_probe_state 归因复核（本文 §二 两形态疑点：转换面 >21 族？n_tup_ins=1 计数形态？）+ 41 残留列存分区 AM 归一决策（并入 R18 §五 处方）；④ G2/S4 台账注记今日事故豁免日；⑤ E6 12/h 精确节拍与 F2 续期；⑥ 恢复后首个整点批次与 sessions_2026_10 增长斜率（R18 §九⑦，本轮 461@11:32 为基线点）。纪律沿用 ⑪-㊼ + ㊽㊾。

## §九、产物与物证

- 本文档（零代码、零 DB 写变更）。
- 服务器侧（文件通道）：/tmp/pg252-20261001-postfix-verify.sql（只读普查）、-postfix-track.sh/.txt（窗界/列存错误逐时/五项/metrics/R13 复核）、-postfix-track2.sh/.txt（CTID 定位/真慢查普查/E6 逐时/R13 样本）、-postfix-slow2.txt（5,164 条 duration 原值，ms）。
- 并行会话物证（引用）：/tmp/pg252-20261001-r18closeout-*.sql（10:16-11:05）及其文档 docs/audit/2026-10-01-252-sql-log-audit-round18.md。
- R13 复核原件：/tmp/pg252-r13-audit-0929-snapshot.log（44MB，09-29 06:03）；签名史：git 1b257be72（09-01 引入）/774ad643/e1b73e88。
- 所有计数可由 ctr.log（03:18 轮转点起）复算；终验点 11:32:36（CTID 零复发/sessions_2026_10=461/bodies_2026_10=4,032/speculative=0）。
