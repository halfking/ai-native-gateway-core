# 252 PG SQL 日志审计第三十一轮（2026-10-10）—— 夜间断连风暴全景 + gauge 可完成性根修（849 + SET LOCAL）+ project_backfill 空扫根修（850 + 48h 窗）+ R30 双迁移部署验证闭环

以 round30 §五.6 为起点。**起点重算**：开局本地 main=ac6f22767（=origin/main），窗内 origin 快进 3 个 mobile 提交（1f1433d62 等，web-mobile 专用），本轮 ff-only 收编后落地、无冲突。R30=2026-10-09（5630938f4+93dd0541f）；其间并行轨 multimedia 落地 847/848 并于 **10-09 10:00:49 在 252 生产库应用**（154=ac6f2276/seq 2514、245=786de4a1/seq 2512，双双携带 R30 全部修复）——R30「部署 pending」状态在本轮取证窗内解除。本轮性质 = **R30 部署验证闭环 + 夜间风暴全景归因 + 两项新 P1 仓内根修（849/850 + 候选 SQL/超时钉）**。**取证窗 = 2026-10-09 06:06:28.648 → 10-10 06:11:38.203（服务器钟，24.09h）**。数据源三段快照（/tmp/r31-audit-20261010/）：ctrlog-20261009-snap（638,333 物理行，10-08 23:47→10-09 23:46）、ctrlog-20261010-snap（634,484 行，10-09 23:47→10-10 05:47）、ctrlog-live-snap（活 ctr.log，03:41 copytruncate 点→06:11，与 10-10 归档重叠段按 ≤05:47:05.806 截弃）。本轮由用户 /goal 指令触发。

---

## §零、传输层巡检

1. **快照层连续产出 ✓**：/var/log/pg17-ctrlog-archive/ 链完整（ctrlog-20261009.log 152.8MB 全天封口 + ctrlog-20261010.log 174MB@05:47——**体量异动本身即信号**：今天前 5.8h 已超昨天全天，对应 §一 夜间风暴）。
2. **轮转洞**：今日 copytruncate 发生在 **03:41**（非惯例 03:07 点，边界如实登记）；活日志段 03:41→06:11 与归档 10-10 文件重叠 03:41→05:47，提取器按头时间戳 ≤05:47:05.806 整体截弃重叠区，零双计。
3. **三轴对账**：头锚定 ERROR+FATAL **5,829** / duration **23,271**（全部 ≥1s，log_min_duration_statement=1s 语义自洽）= 81,284.2s。**81,284s / 86,710s 窗 = 93.7% 跨连接求和占比**（R30 61.7%↑）——多客户端并行求和可超墙钟，纪律 72 口径按库拆分见 §二。提取器沿用 R30 v2 状态机（`\s*` 前缀 + execute/多行语句体归属），新增三段拼接与重叠截弃逻辑。
4. **k8s-file 部分行（纪律 41）实锤**：夜间巨 payload INSERT 的 STATEMENT 文本被 podman 按缓冲区切成大量 `stderr P` 分片，5,829 条错误的 errors.jsonl 达 97MB（均 16.6KB/条）；「cancel_user 无语句捕获 3,242 条」的真相=语句体被 P 分片与头锚定正则分离——现场取证回原始快照完成（§一.1）。

## §一、错误族全景（5,829 条 / 24.09h；头锚定二段计数）

| # | 家族 | n | 对比 R30 | 归因与处置 |
|---|---|---|---|---|
| 1 | `canceling statement due to user request` | **3,325（138/h）** | R30 564（23.5/h）**5.9×** | **夜间风暴主体**。小时直方图：06-21h 恒 15-43/h（R30 基线形态），**22:00 起 265→394→463→536（01:00 峰）→06h 回落**。现场取证（原始快照 3 抽样）：被杀语句 = 网关大 payload INSERT（session_bodies_hot / session_turn_details_hot，payload 含 `[smm_v1:...]` 网关压缩会话摘要 + goal 模式指令）+ `UPDATE api_keys SET last_used_at`（CONTEXT: while updating tuple）——**客户端断连 → 网关 ctx 取消 → PG cancel + broken pipe 三连**。源头 = smm nightly-backtest（21:30 工作日 cron）经网关的 LLM 批量任务，客户端超时/断连放大；DB 被动承伤，本仓 PG 面无可修，登记 §五.2 |
| 2 | FATAL `connection to client lost` | 1,722（71.5/h） | R30 325 **5.3×** | 与 #1 逐小时同步（相关系数≈1），同源登记 |
| 3 | E6 pocket `inconsistent types $4` | 279（11.6/h） | R30 287 节拍器精确维持 | ACC 侧缺陷，本仓不可修，催办维持 |
| 4 | `canceling statement due to statement timeout` | 261（10.8/h） | R30 176 | 分解：**回填候选（无窗旧代码）×~40 全部集中在 10:00 部署前**（4/h × 4h，部署后归零=846 生效实锤）+ **gauge ×~60（部署后 3/h，→§四.1 根修）** + smm COUNT ×40 + session REFRESH ×6（180s 钉内被杀，风暴争用）+ 杂项 |
| 5 | FATAL `terminating background worker "parallel worker"` | **152** | R30 4 **38×** | 全部为 gauge 查询的 parallel worker 陪葬（max_parallel_workers_per_gather=2 × 被杀 leader ≈ 152 与 #4 gauge 击杀数吻合）；起点 10:02:04 = 部署重启后 75 秒，非部署本身引起（gauge 击杀常态延续），→§四.1 |
| 6 | **`could not attach to dynamic shared area` ×32 + `could not map dynamic shared memory segment` ×25** | **57** | **新家族** | 集中 01:19→05:32（风暴窗）；样本被杀前语句 = request_logs_with_current_month ANY(array) 探测的并行计划。/dev/shm 1G 采样时仅用 12MB（非 shm 满溢）；归因 = 夜间并行突发 + 10g cgroup 内存压力下 mmap 失败（max_worker_processes=8 全局）；白天零发生。登记 §五.3，暂不动生产参数 |
| 7 | `syntax error at or near ","` ×13 + `column reference "relname" is ambiguous` ×1 | 14 | 新 | 06:13→10:03（10-09 晨）交互式会话试错（SELECT string_agg(relname,, 等），审计/运维会话自噪音，一轮即止 |
| 8 | 外部股票客户端 `could not determine data type of parameter $2` | 7 | R30 6 维持 | smm fundamentals 族外部缺陷，催办并入股票项 |
| 9 | `canceling autovacuum task` | 6 | R30 6 维持 | 对象 request_logs_hot，风暴窗内争用让位，正常行为 |
| 10 | 外部 04:50 探测（relation does not exist ×4 + cl.task_type ×1） | 5 | R30 #6 复发 | **升级为每日节拍**（同一秒簇、同形状），外部工具试库，登记 |
| 11 | 其余杂项 | ~4 | — | integer out of range ×1（limit_up 外部）、set_config 双写 ×1 等 |

**compliance 42703 / session_client_stats 42P01 双归零延续全窗**；`system_probe_runs_task_type_check` 违反零复发第 4 天。

## §二、慢 SQL 全景（23,271 条 / 81,284.2s；按库拆分——纪律 72）

| 库/归属 | n | sum(s) | 判定 |
|---|---|---|---|
| **smm 股票客户端** | ~9,834+ | **27,047（33.1%）** | 24/7 维持：三连 COUNT ×3,278×3（恒 ~410/h）+ MAX(date) 族 ×1,237 + **ALTER TABLE daily_kline ADD COLUMN IF NOT EXISTS adjust ×123/日（R30 99，+24% 继续涨）**。催办三级维持 |
| **llm_gateway 本库** | ~13,437 | **54,237（2,251 s/h）** | R30 952 s/h → **137%↑**，增量为夜间风暴被动面 + gauge/WITH targets 两个本轮根修族（§四） |

llm_gateway 本库 top 形状（部署 10:00 界线分箱）：

| 形状 | n | sum(s) | max(s) | 判定 |
|---|---|---|---|---|
| **project_backfill（WITH targets）** | 120（pre 24/post 96） | 4,747.6 | 117.3 | **R26→R31 三连攀升后本轮根修**（§四.2）：R30 145→本轮 120 但 post-deploy 速率 ~165/day 折算，sum 4,139→4,747；120s SET LOCAL 预算内不被杀故全以慢查形式消耗，**本库第一大慢查族** |
| **gauge（count JOIN request_status IS NOT NULL）** | 43（pre 1/post 42） | 803.6 | 29.6 | **R30 预言的残余成本兑现**：post-deploy 3/h 击杀 + term_worker ×152，→§四.1 根修 |
| **WITH win（date_trunc/make_interval，candidate_failure_monitor）** | 48 | 2,703.3 | 75.4 | R28 已知族；**21:18 后归零**——与 154 部署窗（~22:00）吻合，疑新二进制已改该形状，登记观察（§五.5，下轮确认根治 or 波动） |
| **REFRESH 四 matview** | 205 | 3,362.6 | 167.3 | session 族 69 次/小时整点节拍 ✓（R30 判据④ PASS）但 max 45-50s→167.3s（风暴争用，180s 钉内）；routing 族 136 次中 **112 次间隔 9-11min、相位钉 :10/:20/:30/:40/:50/:00**——R12-F1 对齐健康，首判「频率翻倍 17/h」系两视图混计假象（§七 A2 订正） |
| **analyze_llm_gateway_table_stats** | 22 | 2,018.2 | **301.2** | **破 <90s 判据**（R30 max 40.8）：6 次 >90s 全部 22:33→05:13 风暴窗，06:00 后恢复常态——风暴症状，随 §五.2 治理预期回落，11-02 前复核 |
| 路由候选族（matched CTE ×1,077 + latest_bucket ×469 + cmb ×449 等） | ~2,000 | ~4,300 | 24.9 | R8 已知族维持 |
| stats minute rollup 族（INSERT dim_minute ×796 + DELETE ×502 + INSERT minute ×321） | 1,619 | 3,443.9 | 12.2 | 已知面量化登记（R30 混于「其余」），随流量线性，暂不立项 |
| UPDATE request_logs_hot is_final_success | 822 | 1,710.1 | 5.5 | R20 已知族维持 |
| apihub assets upsert | 247 | 840.7 | 10.0 | R44 已知面（~10/h）维持 |
| **dailyInsert（WITH latest，usage_facts JOIN dedup）** | **1（pre）/0（post）** | 2.3 | 2.3 | **845 生效实锤**：部署后 14h 零慢查（连 1s 阈值都不进），R30 判据③「sum<30s/日」以 0s 达成 |
| **回填候选（t.id > $）** | post 4（全部带 7 天窗谓词，7.5s 内完成） | 7.5 | 3.6 | **846+代码窗口生效实锤**：候选击杀 10:00 后归零（R30 判据② PASS） |
| 尺寸探针/月度视图探测/索引 hot 写入等已知族 | ~1,900 | ~2,900 | — | R20-R28 已知面维持 |

## §三、R30 移交项逐项复核（§五.6 首查①-⑤）

| R30 登记 | 本轮复核结果 |
|---|---|
| ① 845/846 部署后四判据 | **闭环 PASS（全部四项）**：迁移 845-848 于 10-09 10:00:49 应用（multimedia 轨一并部署）；154=ac6f2276/2514、245=786de4a1/2512 ready:true。判据① stats 聚合击杀 0（窗内本就 1 次慢跑 2.3s，post 零）；② 回填候选击杀 post 归零 + 新候选 SQL 实带 7 天窗谓词；③ dailyInsertSQL sum=0s ✓；④ session 族 matview 整点节拍 24/24h 维持 |
| ② stats_event_dedup 增长曲线 | 914,685 → **933,868**（+19,183/24h ≈ 800/h，无 TTL 线性增长维持，容量登记不变） |
| ③ project_backfill / WITH win 攀升 | project_backfill 判「继续攀升」→ **本轮根修**（§四.2）；WITH win 21:18 后归零（疑 154 部署根治，观察） |
| ④ 股票 24/7 与 ALTER ×99 | 维持 24/7；ALTER ×123/日（+24%），升级条件不变，催办维持 |
| ⑤ 外部新客户端探测复发 | **复发且成每日节拍**（04:50 同秒簇同形状 ×5），登记 |

## §四、本轮根修（两项 P1，252 只读取证 → 本机真库实跑验证）

### 1. [P1·迁移 849 + 代码超时钉] remaining gauge 永远 -1（D9 release gate 事实死亡）→ 可完成化

- **证据链**：gauge（sessionRequestStatusRemainingSQL，D9 clause-4 退役 release gate 的可执行形式，**故意 window-less、双存储面**）在 846 部署后仍是主要击杀源：post-deploy 30s 击杀 ~3/h（60+/24h）+ parallel worker 陪葬 152，gauge 永远拿不到值（-1 =「无法测量」）——R30 预言的「window-less gauge 保持诚实」在成本上不可达，gate 死了。252 EXPLAIN 实证：846 的 (id) 部分索引不覆盖 JOIN 所需 request_id → 规划器走 per-partition (id) btree + 逐行 heap 回表 + 每行 ~5 个 request_logs 月分区索引探测（79.3 万 NULL 行 × 5 ≈ 4M 探测）。本机实测 4.6s@167k NULL → 252 线性外推 20-40s，骑在 30s 角色上限上（19.9s 成功样本与 30s 击杀并存的原因）。
- **修复（双管，语义零变更）**：
  1. **迁移 849**：`CREATE INDEX idx_session_turns_req_status_backfill_cov ON session_turns (request_id) WHERE request_status IS NULL`（分区父表普通 CREATE INDEX，846 同款 ShareLock 代价披露于头注）→ t 侧翻 index-only。与 846 分工：846 服务候选 keyset（id 序），849 服务 gauge（request_id 投影），同谓词不同列集不互替。
  2. **代码**：sampleRemaining 事务内增第三个 `set_config('statement_timeout', $1, true)`（值 = 自身 60s ctx 预算，与批路径 120s 钉、REFRESH 家族 180s 钉同一先例系）；window-less/双面/-1 语义一字未动（admin/session_family_two_surface_test 绿）。
- **本机验证**（171 万 turns / 16.7 万 NULL）：849 正跑 + 幂等二跑 PASS；nestloop 强制形态下计划翻转 `Parallel Index Scan (id)+heap` → **Index Only Scan（849 子索引）** 实证。

### 2. [P1·迁移 850 + 代码窗口] project_backfill 空扫 12.9 万永久不可解析水库 → 6,700s/天 纯浪费归零

- **证据链**：252 实测 session_summaries 653,406 行中 128,699 行 gw_project_id IS NULL，**可解析积压 = 0**（逐行验证 12.3s），水库全部为 project_id/application_code 双空的结构性不可解析行。关键洞察：gw_resolve_project_ref 是**纯字符串推导**（头注函数体实证，无查表）——今天解不出 = 永远解不出；且全部 session_dim UPDATE 站点（admin/session_health_api.go 会话关闭、bg/session_lifecycle_worker.go 回收）只写 status/closed_at/stop_reason/updated_at，project 字段创建时冻结。worker 每 10min × 2 实例顺序走完 12.8 万 NULL 索引 + 63 万 sd pkey merge 证明「无事可做」，单次 40-117s（120s SET LOCAL 预算内不被杀故无击杀计数），~6,700s/天 = 本库第一大慢查族——**纪律 74 第三例**（request_status 候选、gauge 之后）。
- **修复（双管）**：
  1. **代码**：候选 SQL 增 `AND ss.created_at >= (now() - interval '48 hours')` 滚动下界。**窗口宽度论证（区别于 R30 的 7d）**：本查询的候选存在窗口 = 762 触发器失败到 worker 重试 ≈ 分钟级（新会话由触发器即时同步，worker 只是失败重试安全网），48h ≈ 300 倍边际；R30 式 7d 在此被实测否决——252 NULL∧7d = 45,400 行（水库仍以 ~6.5k/天 增长，出生即不可解析），7d 窗常驻扫描面太大；48h ≈ 1.3 万行且随出生速率稳态。头注与测试均钉死「若未来出现修改历史 sd.project 字段的机制则窗口须重估」。
  2. **迁移 850**：`(created_at, session_key) WHERE gw_project_id IS NULL` 部分索引 → 窗口化扫描走 created_at 范围扫描。与 762 的 (session_key) 同谓词索引并存（回滚两面不空），退役留待后续轮评估。
- **本机验证**：850 正跑 + 幂等二跑 PASS；EXPLAIN 实证 `Index Scan using idx_session_summaries_project_null_recent, Index Cond: created_at >= ...` 范围扫描 + 2000 行 productive 批 1.08s；252 post-drain 空扫成本外推 ≈ 亚秒级/tick（对照 40s）。
- **设计弯路披露（自审计 A3）**：①「sd 可解析面翻转驱动」被 252 实测否决（可解析面 662,893 = 水库 5 倍，翻转更贵）；②初版 7d 窗被 NULL∧7d=45,400 实测收窄到 48h。全部证伪发生在拍板前（252 只读 SELECT），生产零接触。

## §五、登记与移交清单

1. **[部署 pending·下一窗口首项]** 849+850 与两处代码修复随下次 154/245 部署生效。验收判据：① gauge ≥1s 慢查与 30s 击杀归零（基线 43 慢查 + 60 击杀/日）；② term_worker 归零（基线 152/日）；③ WITH targets 慢查 sum 从 4,747.6s/日 → <500s/日（空扫面 12.8 万→1.3 万行）；④ `/metrics` requestStatusBackfillRemaining 从恒 -1 变为实值。
2. **[风暴登记·移交网关轨]** 夜间风暴全景：22:00-05:30 cancel_user ~2,800 + conn_lost ~1,400 + DSM ×57 + analyze 301s + session REFRESH max 167s。归因 = smm nightly-backtest（21:30 工作日 cron）LLM 批量经网关 + 客户端断连放大；DB 被动承伤，PG 面无可修。治理面在网关/客户端（payload 上限、请求超时、断连退避），非本仓；若风暴持续，PG 侧唯一候选杠杆 = llm_gateway 角色级连接数/并行度，本轮不动。
3. **[DSM 新族登记]** could not attach/map dynamic shared memory ×57（01:19-05:32）：/dev/shm 1G 采样时 12MB 无压，max_worker_processes=8 全局，归因夜间并行突发 + 10g cgroup mmap 压力。若下轮复发或扩大 → 评估 max_worker_processes 与风暴治理联动。
4. **[催办维持]** ①股票 24/7（33.1% 墙钟）+ ALTER ×123/日（↑24%，若继续 → 升级限流/防火墙议题触发条件不变）+ $2 类型 ×7；②E6 11.6/h 节拍器；③04:50 外部探测已成每日节拍——三项均外部。
5. **[观察]** WITH win date_trunc 族 21:18 归零疑与 154 部署窗吻合（下轮确认）；analyze <90s 判据破（风暴症状，随 §五.2 评估回落，11-02 前复核）；stats minute rollup 族 3,443.9s/day 量化登记；llmgo-252-dev failed 第 4 天（顺延，R28 登记不变）；[运维轨顺延] disk-watch CROND、log_size_max 提案（R24-R30 顺延链不变）。
6. **[终验节点]** 10-11（周日）03:15 vacuum-bloat 首跑（下轮首查项）；11-02 路由 7d 复核。
7. **[下轮首查]** ① 849/850 部署后 §五.1 四判据；② WITH win 是否真归零（vs 154 部署）；③ analyze 是否回落 <90s；④ 股票 ALTER 曲线（×87→99→123）；⑤ DSM 是否复发；⑥ stats_event_dedup 曲线（933,868→?）。

## §六、新纪律候选

- **76**：**「频率回归」判定必须先做间隔分布 + 相位直方图，不能拿 n/h 拍脑袋**——本轮 routing_7d 首判 17/h（较 R30 翻倍，疑似合并引入回归）被间隔分布证伪：136 事件中 112 次间隔恰 9-11min、相位钉 :10-:00 整点（R12-F1 对齐健康），17/h 系两个视图（routing_7d + routing_audit_summary_7d）混计的算术假象。同族多视图/多实例共享一个家族名时，n/h 无意义。
- **77**：**窗口类修复的宽度论证必须区分「候选存在窗口」的两种语义**——「写入存在窗口」（R30：可回填行只能来自近期写入，小时级）与「重试可见窗口」（本轮：触发器失败行需要留在扫描面内等 worker 重试，分钟级但失败行持续存在）。前者窗口从写入路径推，后者从重试节奏推；照抄上一轮的 7d 会把 45,400 行出生即不可解析的新行常驻扫描面。且窗口必须有「可解析性/可回填性冻结」的结构证明支撑（纯函数 + UPDATE 面核查），否则窗口就是静默丢行的盲区制造器。

## §七、自审计

- **A1**：252 python3 = 3.6 连坑两次（fromisoformat 不存在、import 就地遮蔽）——R30 时代脚本全部兼容 3.6，本轮新脚本上机前未核版本，浪费两个来回。**上机脚本先 `python3 -V`** 升格为固定动作。
- **A2**：routing_7d「频率翻倍」假回归——若直接进文档即成假警报，被间隔分布+相位直方图当场证伪（§六 76 的案例本体）；教训同向：分类器把两个视图混进一个形状键（`routing_analytics_Nd` 规范化吃掉了 7d/audit_summary_7d 后缀），形状键设计要保语义词。
- **A3**：project_backfill 修复两轮证伪（翻转驱动、7d 窗）全部发生在拍板前、生产零接触（§四.2 披露）；取证期三次 252 真查询（backlog 12.3s / reservoir 7.4s / 可解析面秒级）均为只读秒级，与 worker 自身每 10min 的 40s 扫描同量级或更小。
- **A4**：252 全程只读（SELECT/目录/EXPLAIN 无 ANALYZE）；本机真库承担全部实跑（迁移双跑幂等、EXPLAIN 对照、计时）。披露一处本机操作：测量期手建过 849 同名索引，验证前 DROP 后按迁移文件重放，保证「实跑对象=迁移文件本体」。
- **A5**：并行轨零触碰——origin 领先 3 提交（mobile 轨）ff-only 收编无冲突；849/850 编号经 252 台账（max=848）与本仓目录双核无撞号；multimedia 847/848 落地形态未动。
