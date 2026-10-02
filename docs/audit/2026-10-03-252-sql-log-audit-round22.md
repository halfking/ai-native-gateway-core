# 252 PG SQL 日志审计第二十二轮（2026-10-03）—— 传输层稀疏洞法证 + R21「日志爆发」证伪修正 + capability evidence_json hex 根修（FIX-C 第四断点）+ acc 外部错误风暴登记

以 round21 §九 + round20 §八为起点（起点重算：origin/main 由 R21 时的 20fbdba74 前进到 **f5a13adc4**（本轮窗口内两度前进：5096f902b → f5a13adc4，含 R34 轮对 capability_backfill 区域的重构——本修复已在 f5a13adc4 基线上重新接线，persistRow 裸 []byte 在新基线仍在））。取证窗 = **2026-10-02 11:48 → 10-03 06:03**（ctr.log-20261003 归档 [→03:07] + 现行 ctr.log 快照 [03:08→06:03]，与 R21 窗口 [→11:48] 首尾相接）。所有时间戳为 252 服务器 CST；快照 06:03，本机/252 时差 <1min。

---

## §零、传输层法证：logrotate copytruncate 稀疏洞 —— R21「622MB 日志爆发」证伪修正

本轮最大方法论发现。快照时现行 ctr.log 表观 748MB，但**前 718,328,668 字节是一条纯 NUL 行**（首条真实日志 `2026-10-03T03:08:03 … duration: 1132.860 ms` 恰在偏移 718,328,545 之后）。机制：conmon 写日志**不带 O_APPEND**，`/etc/logrotate.d/podman-pg-252` 的 copytruncate 截断文件后 conmon 写偏移不复位，新内容从洞后追加——文件表观尺寸 = 旧偏移 + 新内容，洞读出为 NUL。三文件互证（洞偏移完全自洽）：

| 文件 | 表观尺寸 | NUL 洞 | 真实内容 | 覆盖窗 | 真实速率 |
|---|---|---|---|---|---|
| ctr-snapshot-r21.log（R21 快照） | 622.4MB | 606.6MB | **15.8MB** | 10-02 03:51→10:49（7h） | 0.63KB/s ≈ 2.7× 基线 |
| ctr.log-20261003（归档） | 718.3MB | 606.6MB | 111.7MB（F=101.4+P=10.3） | 10-02 →10-03 03:07 | 1.33KB/s |
| ctr-snapshot-r22.log（本轮快照） | 748.4MB | 718.3MB | 30.1MB（F=28.9+P=1.2） | 10-03 03:08→06:03（2.9h） | 2.9KB/s ≈ 12× 基线 |

**修正 R21 §五两处叙事**（依据上文，非重读原文臆测）：
1. 「622MB/4.7h 日志爆发（2.2MB/s，50× 基线）」→ 真实内容仅 15.8MB，速率为基线 ~2.7× 而非 50×；「爆发」的主体是稀疏洞伪影。巨 payload body 记录机制本身存在（P 帧 1.2MB 实测、8KB 级源码文本载荷），但量级是 MB/h 而非百 MB/h。
2. 「1,996 空头 = 巨 payload INSERT 续行形态」→ 空头实为**全部多行语句**的提取盲区（本仓库 SQL 风格带缩进换行，`duration: … statement: ` 行内为空、语句体从下一行开始）；其中最大子群是路由候选 SELECT ×1020（见 §二），并非 body INSERT。R21 慢查「2,287 条/8,177s」的家族构成需按此重读。

连带发现（磁盘侧）：logrotate 的 copy 会把洞**物化**为真实磁盘块——10-03 归档 du=686MB 而真实内容 111.7MB，**单归档 ~574MB 纯浪费**；`rotate 3 + delaycompress` 下最多 ~2 个未压缩物化归档并存。`.gz` 归档（22-167MB）无此问题（NUL 串被压缩坍缩）。处方（移交运维轨，本轮不动生产配置）：podman 原生 `--log-opt max-size` 旋转（带 O_APPEND）或去掉 delaycompress 让归档尽早 gzip；任何字节量取证必须排除 NUL 洞（`ls` 表观 ≠ `du` 实占）。

---

## §一、错误家族全景（头锚定 `CST [pid] ERROR:` 二段计数）

| # | 家族 | 归档窗 [11:48→03:07] | 现行窗 [03:08→06:03] | 归因 | 处置 |
|---|---|---|---|---|---|
| 1 | `operator does not exist: timestamp with time zone < interval at character 140` | **0** | **×1,741**（首条 05:05:22，~30/min 热循环） | `UPDATE acc_candidate_saga_steps … WHERE claimed_at < $1 - $2::interval`——**acc_db / acc_swarm_db 域服务**（两库同名表，非本仓代码，全仓 grep 零命中），F1（$n+INTERVAL SimpleProtocol 炸弹）同形外部实例 | 外部催办登记（§五.3） |
| 2 | `invalid input syntax for type json at character 221`（DETAIL Token "\\" is invalid） | ×245 | ×516（合计 **×761**） | **本仓 bug**：`credential_model_capabilities.evidence_json`（jsonb）收到 `'\x7b22…'` hex 字面量——`bg/capability_backfill.go` persistRow 把 `json.Marshal` 的 `[]byte` 直传 SimpleProtocol（R11 FIX-C 同根第四断点） | **本轮根修**（§四） |
| 3 | `inconsistent types deduced for parameter $4 at character 135` | ×185 | ×35（合计 220 / 18.25h ≈ **12.5/h**） | E6 opencode-pocket scheduled_tasks，自 R14 起节奏纹丝不动 | 催办维持（§五.4） |
| 4 | `canceling statement due to user request` | ×1,304 | ×632 | 应用侧 context 取消：probe cycle 目标查询（`COALESCE(cmb.available…)` ×421）+ turns/bodies promote 批（`MAX(turn_no)` ×166、hot INSERT ×106）+ `WITH used` ×51 等——R5 整点尖峰级联已知容忍族 | 计数登记，无动作 |
| 5 | `canceling statement due to statement timeout` | ×294 | ×119 | probe cycle 目标查询 **×36**（R21 记 ×7，**恶化 5×**）、project_backfill `WITH targets` ×36、`SELECT session_key, tenant_id` ×10（新形状待归因）、`WITH latest` ×9、`maas_credit_consumption_buckets` INSERT ×2（MAAS 新代码已上 30s 榜） | 登记＋R8 老病治理提案顺位上升 |
| 6 | `deadlock detected` | ×3（17:31:45 / 00:51:18 / 01:09:39） | ×5（05:25:41→05:27:09 连环） | **MAAS 特性 DDL 手动应用会话**（`ALTER TABLE request_logs ADD COLUMN credits_charged` / `CREATE TABLE maas_settings` / `CREATE INDEX idx_request_logs_credits_charged`，access-exclusive/share 锁环）vs ①`analyze_llm_gateway_table_stats('2')`（ShareUpdateExclusive，17:31 与 05:25 两例）②REINDEX CONCURRENTLY 读臂（00:51/01:09，AccessExclusive vs AccessShare，OID 217273=request_logs 父/217364=maas_settings/96868952=request_logs_2026_10） | 最终 05:25 批成功落库（credits_charged/credits_rate_multiplier/maas_settings 实测在位）；部署纪律建议见 §五.2 |
| 7 | 索引膨胀工具轮自噪音（2026-10-03-252-index-bloat-tooling 会话） | pg_class missing FROM ×44、trailing junk "8s" ×13、pgstatindex 不存在 ×5、VACUUM-in-tx ×2、BOM "﻿select" ×2、手工 count 「总行数」×2（03:12/03:19，138s/条） | 残余 | 只读巡检/自测样本（其报告自述） | 对账剔除 |
| 8 | 单发外部噪音 | projects/memory/conversation_history/orchestration_dispatches/agent_inventory/`invalid message format` ×7/`integer out of range` ×3 等 | 残余 | 共享实例其他库服务（dbid 过滤后与本库无关） | 不计入本库对账 |

二段计数：本库（llm_gateway, dbid 16384 实证）真阳性 = 家族 2/3/6 + 家族 4/5 的本库子集；家族 1 在 acc_db/acc_swarm_db。

---

## §二、慢 SQL 全景（≥1s 共 3,437 条 / 12,949s 语句时 / 2.9h 窗）

形状提取修正：R21 的「同线提取」漏掉全部多行语句，本轮改「duration 行 + 下一非空行」配对法（python 流式，awk 大行 OOM 教训）。

| 家族 | n | sum | max/avg | 判定 |
|---|---|---|---|---|
| **路由候选 SELECT（autoroute `refreshIndexSQL`，`WITH latest_bucket` 形状）** | **×1,020**（~10s 节拍） | — | 15.1s / **avg 2.67s** | **P2 性能回归**（R8 D10 基线 61ms）——见下 |
| acc saga UPDATE（外部） | ×43 | 1,532s | 382s | acc_db 域服务锁等待/无索引面，随 §一.1 一并催办 |
| probe_cycle 目标查询 | ×186 | 459s | — | avg 2.5s；R8 v_routable_credential_models 双视图 join 老病（R21 §九④），timeout ×36 恶化同源 |
| analyze tick（`analyze_llm_gateway_table_stats`） | 2 大条+ | 206s+153s | 287s | D7' 已知（巨 payload 行宽采样贵）；未破 10min SET LOCAL 线 |
| project_backfill `WITH targets` | ×2 大条 | 153s+ | — | 已知 |
| ursm 快照聚合 `date_trunc('hour', snapshot_ts)` | ×19 | 244s | 144s | 03:24/03:25 集中=膨胀轮工作窗，自噪音嫌疑为主 |
| smm daily_kline `MAX(date)`（外部） | ×2 ≥19s | — | 19.6s | 外部已知 |
| 其余（stats rollup DELETE/INSERT dim_minute 等） | ~500 | ~2,500s | 1-3s | R12-F1/F2 收敛后常态节拍 |

### P2 登记：路由候选查询随月分区增长退化（含 R8 61ms 基线对照）

证据链（252 真库实测，全部只读）：
- `refreshIndexSQL` 对 `credential_model_index_with_current_month` 视图**扫描两遍**（latest_bucket 聚合一遍 + JOIN 臂一遍）；核心聚合单独执行 581ms（321,016 行：hot 2,818 + 09 列存 301,255 + 10 列存 16,943 + 11 空）。
- 09 分区（2026-09-04→09-30，301k 行，列存）全是陈旧 bucket，每次执行陪跑两遍；列存无 btree、分区剪枝对 `now()`（稳定函数）与字面量**均不生效**（Citus ColumnarScan 自定义扫描路径），唯一抓手是 **Chunk Group Filters**：加 `bucket >= now()-interval '168 hours'` 后 09 臂 cost 109.37→11.91（9×）、HashAggregate 总 cost 4626→916（5×）。
- 完整查询 A/B（252 活数据）：原版 **268 行** vs 加 7d 回看 **267 行**——差 1 对（最新 bucket >7d 的活凭据候选）。
- **裁决：不改语义、不落补丁。** 7d 回看存在「7 天无流量 → 退出候选 → 无流量即无新索引行 → 永久无法复活」的自锁语义，且已有 1 对活候选真实命中——这是产品语义变更，不是审计轮性能修复。完整提案（SQL 已写好、A/B 数据在手）：`WITH latest_bucket` 与 JOIN 臂各加 `AND bucket >= now() - interval '168 hours'`，需产品对「闲置 7 天通道自动退出 autoroute」拍板后随版生效；同时 `admin/auto_route.go:278` 的同形副本（admin 低频路径）一并改。**自然缓解节点：2026-11-02 day-2 tick DROP credential_model_index_2026_09（R21 §四矩阵），届时视图瘦身 301k 行**——但 10 分区将继续按 ~43k/天增长，11-02 后若不复现 2.67s 需复核写入速率。

---

## §三、R21 三 tick 零复发对账——预检 PASS

- `row is too big`：全窗 **0**（810 后延续）。
- `would overlap partition`：全窗 **0**（811 后延续）。
- `UPDATE and CTID scans`：全窗唯一命中 = 10-02 11:10:38 R21 recon3-J4 自噪音探针（已在其清单内），**生产 0 复发**。
- promote/ensure 节拍：supplier_errors/session_bodies promote 每小时 tick 无慢无错形态（错误家族表零相关条目）。
- 24h 满窗收口由 automation-09e1ae20（今日 07:30）照常补满，其 §十二 判据不受本轮影响；本轮预检与其自噪音清单一致。

---

## §四、本轮修复：capability evidence_json hex 根修（FIX-C 第四断点）

- **缺陷**：`bg/capability_backfill.go` `persistRow` 将 `buildCapabilityEvidence`（`json.Marshal`）的 `[]byte` 直传 pgx SimpleProtocol → 内联为 bytea hex 字面量 `'\x7b22…'`，PG 拒绝转 jsonb。**连带伤害：整个 upsert 失败 → 能力位丢失 + `writeResponsesCapability` Redis 镜像被跳过**（persist 错误提前 return）。表现状佐证：252 `credential_model_capabilities` 仅 2 行（612 种子），0 条带证据。
- **修复**（R71 projectattr 同款）：新增 `capabilityEvidenceParam([]byte) interface{}`（非空转 string、空保 nil——空串同样炸 jsonb），persistRow `$4` 经它传。
- **守卫三层**：①单测钉 helper 三态；②AST 源码扫描守卫（Exec 参数出现裸 `evidence` Ident 即红，沿 `TestBackfillNeverWritesStreamCapability` 惯例，注释不喂饱子串门）；③真库正反对照 `bg/capability_evidence_realdb_test.go`（SimpleProtocol 池：string 插 jsonb 成功 + 裸 `[]byte` 必炸 invalid input syntax——负例同时证明池确为 SimpleProtocol，模式失效时守卫以「负例未引爆」红灯而非静默放过）。
- **验证**：`go build`/`go vet` 干净；`TEST_DATABASE_URL` 指向本机 llm_gateway 真库，三用例全 PASS；bg 全量套件唯一失败 `TestLedgerReconciler_RunOnce_RealDB` 经 origin/main 干净 worktree 复跑**同样失败**＝既有真库数据态依赖（种子期望 2 处差异只现 1），与本轮无关（登记 §五.6）。
- **生效路径**：Go 侧变更，随下次 252 部署生效；部署后 R23 复核该家族应归零、能力位开始落行。

---

## §五、登记与移交清单

1. **[P2·提案待拍板]** 路由候选 7d 回看（§二，提案 SQL+A/B 数据齐备）；R8 老病 probe_cycle 目标查询治理顺位上升（timeout ×36/3h）。
2. **[部署纪律]** 手动 DDL 应用与后台 tick 的死锁环（8 例全闭环无实害）：建议手动应用前 `SET lock_timeout` + 失败显式重试，避免 analyze tick 侧反复被选为牺牲品；MAAS 相关对象（credits_charged/credits_rate_multiplier/maas_settings）已核验落库完整。
3. **[外部催办·升级]** acc_db/acc_swarm_db 域服务：05:05 起 `timestamptz < interval` ~30/min 热循环（F1 同形，修复处方即 `::timestamptz` 显式转型）+ saga UPDATE 慢至 382s——共享实例上错误负载与 IO 双污染，建议按 E6 先例升级排期；**注意 11:48→05:05 窗口内零命中，故障起点可精确交给对方定位**。
4. **[外部·维持]** E6 pocket $4：228 条/18.25h ≈ 12.5/h，节奏不变，催办维持。
5. **[运维轨]** logrotate copytruncate 稀疏洞（§零处方）；`/opt/scripts/pg17-vacuum-bloat.sh` 服务器侧仍为坏版本（repo 已修、未部署），**明日（周日）03:15 将第 N 次失败**——部署缺口移交索引膨胀工具轨。
6. **[测试债]** `bg/ledger_reconciliation_test.go` 真库用例对本机数据态敏感（期望 ≥2 差异实得 1），origin/main 干净检出同样失败；本地真库种子需修复或断言改容。
7. **[新形状]** statement timeout `SELECT session_key, tenant_id` ×10 待归因（R23 首查）；`maas_credit_consumption_buckets` ×2 属 MAAS 新代码预热，观察即可。

---

## §六、新纪律候选

- **55**：容器日志字节量结论必须排除 copytruncate 稀疏洞——`ls` 表观尺寸 ≠ 内容量 ≠ 磁盘实占（du）；跨轮转点的快照（cp 产物）自带洞，「日志爆发」类登记必须以帧字节（`stderr F/P` 行字节和）复核。R21「622MB 爆发」为本纪律的直接教训。
- **56**：慢查形状提取必须「duration 行 + 下一非空行」配对——本仓 SQL 多行风格下 statement 同线恒空，同线直方图会把全体多行语句归入「空头」并错误归因（R21 1,996 空头误标 body INSERT 的机制）。

## §七、自审计

- A1：家族 1（acc）先按 1741/3h 报了 ~10/min，后经归档窗反查（0 命中）+ 首条时间戳（05:05:22）修正为 ~30/min——外部故障强度以「首条时间戳锚定窗长」计算，不以快照窗想当然。
- A2：R21 爆发证伪用了三文件洞偏移互证（606MB→718MB 单调递增与轮转时刻吻合），不是单点测量；且公开修正对象是自家前轮结论。
- A3：7d 回看提案做了 A/B 等价性检验（268→267）才发现语义陷阱；那 1 对差异候选没有讳言，直接促成「不落补丁、待拍板」裁决——等价性检验先于补丁落地。
- A4：capability 修复的负例对照设计里，若池模式不是 SimpleProtocol，负例会「意外成功」——把它写成红灯而非跳过，防止守卫在错误模式下静默失效。
- A5：ledger 测试失败未据「与我无关」草率放过——worktree 干净检出复跑归因后，仍登记为测试债（§五.6）而非忽略。
- A6：252 全程只读（EXPLAIN/EXPLAIN ANALYZE/count/temp 表随会话消亡）；未触碰并行会话在制对象（MAAS 应用会话、膨胀轮 REINDEX、V1-freeze 工作区改动）；admin/auto_route.go 有并行 +1 行在制，本轮回避未编辑。

## §八、下一轮提示词（建议）

> 以本文 + R21 §九为起点。优先级：① 采纳 §十二 收口（automation-09e1ae20 产出，窗口至 10-03 07:14+；本报告 §三 已预检 PASS）；② capability 修复部署后复核（json 家族应归零、credential_model_capabilities 开始落行）；③ routing 候选 7d 提案产品拍板跟踪（拍板后随版落地 + admin 副本）；④ `session_key` timeout 新形状归因 + maas_credit_consumption_buckets 观察；⑤ 周日 03:15 vacuum 脚本失败实录（验证 §五.5 预言，补服务器侧部署）；⑥ acc 催办回执跟进；⑦ 11-02 day-2 live-fire 前最后准备。纪律沿用 ⑪-54 + 55-56。

## §九、产物与物证

- 代码：bg/capability_backfill.go（capabilityEvidenceParam + persistRow 接线）、bg/capability_backfill_test.go（helper 三态 + AST 守卫）、bg/capability_evidence_realdb_test.go（真库正反对照，新建）。
- 日志物证：252:/tmp/pg252-r22/（ctr-snapshot-r22.log、slow_pairs.tsv 3,317 对、analysis1-4/evidence2-4/nulcheck/slowscan/hist 脚本、recon1-7.sql）。
- 关键实测值：capability 表 2 行/761 次失败写入；acc 首错 05:05:22、×1,741；deadlock 8 例全时间戳；路由查询 EXPLAIN 前后 cost 4626→916、A/B 268/267；洞偏移 606,605,840（R21 快照=归档）→718,328,545（本轮快照）。
- 本报告：docs/audit/2026-10-03-252-sql-log-audit-round22.md。
