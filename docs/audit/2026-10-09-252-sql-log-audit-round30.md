# 252 PG SQL 日志审计第三十轮（2026-10-09）—— 双 30s 击杀根修轮：stats 日聚合 dedup JOIN 回表（845）+ 回填 worker 空扫永久 NULL 集合（7 天窗 + 846）+ R28 移交四项闭环

以 round28 §五.6 为起点。**起点重算**：开局本地 main=98b8626ee（=origin/main 起点处，既有版本身份本地改动 VERSION/version.json/web 公共两件与本轮无关）；R28=2026-10-08（f8f7decc9）；R29=同日 154 事故轮（9c3548691→部署 2509-70c0a245，非本系列）；其间并行轨产出 22e5e3f8f（deploy-lib symlink SSOT）、7cbf65478/1a368a301（sanitize 容错），本轮 rebase 收编。本轮性质 = **闭环验证 + 两项新 P1 仓内根修（迁移 845/846 + 候选 SQL 窗口）**。**取证窗 = 2026-10-08 06:07:27.637 → 10-09 06:06:28.648（服务器钟，23.98h）**，数据源为当前活 `ctr.log`（dc5e5427… 容器，114.2MB，自 10-08 03:07:27 copytruncate 点起——今日 logrotate 尚未轮转，故单文件覆盖全窗），快照 `/tmp/r30-audit-20261009-ctrlog-snap1` 608,816 物理行 / 28,759 头锚定行（窗内 24,847）。本轮由用户 /goal 指令触发。

---

## §零、传输层巡检

1. **快照层连续产出 ✓**：`/var/log/pg17-ctrlog-archive/` 链完整（ctrlog-20261008.log 107.9MB + ctrlog-20261009.log 24.3MB mtime 05:47），R26 动态解析脚本持续。
2. **轮转洞**：取证窗内无 copytruncate 事件（上次 10-08 03:07:27 在窗外，今日轮转未发生）——零洞窗，无需双侧取界。
3. **三轴对账**：头锚定 ERROR+FATAL 1,381 / duration 19,748（全部 ≥1s，`log_min_duration_statement=1s` 语义，n_dur_all == n_dur_1s 自洽）= 53,323.4s / 23.98h = **2,223 s/h**（R28 2,275 持平）。提取器 v2 为干净状态机（R28 A1/A2 两处盲区均预防：`\s*` 前缀 + execute/多行语句体归属）。

## §一、错误族全景（1,381 条 / 23.98h；头锚定二段计数）

| # | 家族 | n | 对比 R28 | 归因与处置 |
|---|---|---|---|---|
| 1 | `canceling statement due to user request` | 564（23.5/h） | R28 1,001（41.6/h）**回落 44%** | top 形状换血：credential_monitor epoch 读臂 ×107 **全部集中在 06:09-11:15（R28 窗末尾巴，11:14 后归零）**；其余为 status 轮询/写臂散点。**首查⑤ PASS：夜间翻页风暴未再现** |
| 2 | FATAL `connection to client lost` | 325（13.5/h） | R28 644（26.8/h）减半 | 已知自噪音，与 #1 同步回落；不立项 |
| 3 | E6 pocket `inconsistent types deduced for parameter $4` | 287（12.0/h） | R28 289（12.0/h）**节拍器精确维持** | scheduled_tasks 在 pocket 库，ACC 侧缺陷，本仓不可修，催办维持 |
| 4 | `canceling statement due to statement timeout` | 176（7.3/h） | R28 259（10.8/h）回落 | top = **session_turns JOIN request_logs 回填候选 ×103（→§四.2 根修）** + usage_facts rollup ×36（→§四.1 根修）+ cmb ×28 + maas_credit ×3 |
| 5 | 外部股票客户端 `could not determine data type of parameter $2` | 6 | 新形状 | fundamentals `WITH latest DISTINCT ON` 族，extended protocol $2 类型无法推断——smm 库外部客户端缺陷（与 R28 #7 total_tokens 漂移同库），催办并入股票项 |
| 6 | 外部新客户端探测 04:50:49 | 4 | 新 | `cl.task_type does not exist`（prompt 表）+ `llm_usage_stats`/`llm_optimization_reports/records` 不存在 ×3——同秒 4 pid，外部客户端拿 AI 生成 SQL 试库（schema 全不存在）；一轮即止，登记外部 |
| 7 | 外部 `session_id does not exist`（HINT gw_session_id）×2 | 2 | 新 | 03:17/03:58 request_logs 分区猜列，HINT 指向 gw_session_id——外部工具会话，登记 |
| 8 | R29 取证会话自噪音 | ~7 | — | 06:59-08:04 gateway_survival_fail_closed 调查查询列名试错 ×3（model/created_at/stream_chunks）等，审计会话产物 |
| 9 | 其余杂项 | ~10 | — | schema_migrations 混名 ×1、运维双引号字面量手误 ×2、limit_up integer ×2（R28 同族外部）、startup packet ×1、 terminating worker ×4（R28 28→4 回落） |

**compliance 42703 / session_client_stats 42P01 双归零延续全窗**；`system_probe_runs_task_type_check` 违反零复发第 2 天（R28 ×103 → 0）。

## §二、慢 SQL 全景（19,748 条 / 53,323.4s；按库拆分——纪律 72）

| 库/归属 | n | sum(s) | 判定 |
|---|---|---|---|
| **smm 股票客户端** | **14,800** | **30,487（57.1%）** | **24/7 全天候持续**（首查④）：三连 COUNT 轮询 13,495 次（每小时 ~600 条恒定）+ MAX(date) 族 ×487 + GROUP BY source ×276 + **`ALTER TABLE daily_kline ADD COLUMN IF NOT EXISTS adjust` ×99/日**（R28 ×87，重复 DDL 继续）。催办三级维持 |
| **llm_gateway 本库** | 4,948 | 22,836（952 s/h） | R28 1,019 持平略降；两处 30s 击杀根因在本轮根修（§四） |

llm_gateway 本库 top 形状：

| 形状 | n | sum(s) | max(s) | 判定 |
|---|---|---|---|---|
| **project_backfill（WITH targets）** | 145 | 4,139.7 | 109.1 | R8 已知族四连：R26 205→R27 34→R28 123→**145（max 首破百）**。随积压量波动，登记观察不立项（11-02 复核判据外新增观察点） |
| WITH win（series/date_trunc 统计族） | 102 | 3,070.8 | 52.9 | R28 1,974s 回升 56%，bg/candidate_failure_monitor.go:317 已知形状，登记 |
| 路由候选族（cmb ×164/1,338.6 + matched CTE ×329/717.5 + latest_bucket ×427/974.5） | 920 | 3,030.6 | 29.9 | R8 已知族维持，11-02 复核判据 |
| **REFRESH 四 matview**（client_task_matrix 24/913.0 max74.4 + client_stats 24/662.4 max45.0 + routing_7d 141/661.5 + task_stats 24/508.4） | 213 | 2,745.3 | 74.4 | **首查①核心证据**：client 族三视图 07:19 起每小时整点恰 1 轮 × 24/24h，无手动干预（R28 冻结 18h/仅手动 4 轮）——1498c576c 刷新器部署生效实锤 |
| credential_monitor epoch 读臂（admin/credential_monitor.go:798） | 435 | 831.8 | 11.5 | **10-08 11:14 后归零**（监控会话结束），非持续负载，不立项 |
| **stats 日聚合 dailyInsertSQL（usage_facts JOIN stats_event_dedup）** | 21 | 307.7 | 29.5 | **+30s 击杀 36 次**——新 P1，§四.1 根修 |
| apihub assets upsert | 140 | 349.7 | 9.3 | R44 心跳优化后已知面（~6/h），不立项 |
| analyze_llm_gateway_table_stats | 23 | 659.7 | 40.8 | <90s 判据维持（R28 顺延关闭项延续 PASS） |
| 回填候选（session_turns JOIN） | 7 | 153.7 | 29.6 | **+30s 击杀 103 次**——新 P1，§四.2 根修 |
| UPDATE is_final_success / INSERT 索引 hot / 其余已知族 | ~2,900 | ~9,100 | — | R20-R28 已知面全部维持，无新回归 |

## §三、R28 移交项逐项复核（§五.6 首查①-⑤）

| R28 登记 | 本轮复核结果 |
|---|---|
| ① 1498c576c 部署后三视图 refreshed_at 自动推进 | **闭环 PASS**：session_client_stats/session_task_stats/session_client_task_matrix refreshed_at 全部 = 10-09 06:00（取证时 12 分钟前），源滞后 12min；窗内 07:19 起每小时整点恰 1 轮 ×24，零手动。R28 §三.2「冻结 18h01m」形态终结 |
| ② modality_verify 零复发 + 日增曲线 | **PASS**：task_type 实名 `modality_verify`（R28 文档转述为 modality_verify_probe，订正），3,419 行、首行 10-07 13:13:37（与 R28 逐秒互证吻合）、最新 10-09 05:59、日增 ~100 行（15min 节拍），CHECK 违反零复发 |
| ③ 回填 worker 收敛（360/日 + timeout 122 基线） | **数据面收敛、扫描面空转**：可回填水位 0（48h 窗 36,912/36,912 已回填、JOIN 口径 pending=0），但 idle 空扫 30s 击杀 103/日持续——根因 793,519 行永久 NULL（§四.2），**本轮根修** |
| ④ 股票 24/7 是否持续 | **持续**（57.1% 墙钟 + ALTER ×99），催办维持（若持续至下轮，升级限流/防火墙议题的触发条件不变） |
| ⑤ 夜间翻页风暴是否再现 | **未再现**：user cancel top1 ×107 全部为 R28 风暴窗末尾巴（06:09-11:15），夜间 01-05h cancel 密度回落至常态 |

## §四、本轮根修（两项 P1，均 252 实测归因 → 本机真库实跑验证）

### 1. [P1·迁移 845] stats 日聚合 dedup JOIN 逐行 heap 回表 → 30s 击杀 36/48 tick

- **证据链**：`dailyInsertSQL`（domains/stats/daily_monthly_rollup.go:195，每实例每小时 tick 重建 48h 窗）的 `JOIN stats_event_dedup d ON d.event_id = f.event_id AND d.occurred_at = f.occurred_at`——dedup 表（914,685 行）只有 `pkey(event_id)`，occurred_at 只能做 Filter（回 heap 比较）。252 实测 EXPLAIN ANALYZE（2h 窗 2,208 行）：**总 2,285ms，dedup 探测 loops=2,208 × 1.677ms/loop = 99.7%**（冷页随机 IO）。48h 窗 ~28,429 行外推 30-48s，与角色级 30s rolconfig 击杀边缘吻合（max 29.5s=热缓存下界）；**窗内 36 次击杀 / 48 tick ≈ 75% tick 报废重试**。
- **修复**：迁移 845 `CREATE INDEX CONCURRENTLY idx_stats_event_dedup_event_id_occurred_at ON stats_event_dedup (event_id, occurred_at)`（普通表，822 的 dbinit:no-transaction 惯例）→ Index Only Scan。
- **本机验证**（127.0.0.1 真库，146 万行）：正跑 3.2s + 幂等二跑 PASS；计划翻转 `Index Scan pkey + Filter` → `Index Only Scan`，2h 窗 34.9ms。

### 2. [P1·迁移 846 + 代码窗口] 回填 worker 空扫 79.3 万永久 NULL 集合 → 30s 击杀 103/日

- **证据链**：候选 SQL（session_request_status_backfill.go，keyset `t.id > $1 ORDER BY t.id LIMIT 100`）在积压清零后，idle drain 第一批必须从 id=0 走完 pkey 全程逐行 JOIN 探测才能证明「无候选」。**252 生产 session_turns 有 793,519 行 request_status IS NULL**——全部 823 之前的 v1 历史行（partition_date ≤ 2026-09-30、`with_log_row=0`：request_logs 源行已被 TTL 清除，结构性不可回填，823 头注明文「No backfill——回填只会是猜测」）。24h 103 次击杀 ≈ 2 实例 × MaxIdle 30min 精确吻合。~3,090s/天 DB 墙钟全部花在「证明无事可做」。
- **修复（双管）**：
  1. **代码**：候选 SELECT 加 `AND t.partition_date >= (now() - interval '7 days')::date` 滚动下界。论证：可回填候选的存在窗口 = 823 写路径 + promote 保留期（8h）= 小时级，7 天 ~21 倍系数；窗外 = 永久 NULL 历史行，排除无损；分区裁剪把扫描面缩到当月分区零星 NULL 行。**窗口自愈**（今日积压 7 天后自动出窗），且 gauge（`sessionRequestStatusRemainingSQL`）不带窗口——若真出现窗外可回填行，gauge>0 而 worker 不处理，release gate 诚实卡住，保守方向正确。
  2. **迁移 846**：父表级部分索引 `(id) WHERE request_status IS NULL`（分区父表普通 CREATE INDEX 级联 6 分区，843 先例 ShareLock 挡写不挡读，随维护窗口应用）——与窗口谓词叠加后当月分区候选定位/无候选证明都走 index-only。
- **本机验证**（171 万行、167,109 NULL 积压更重）：846 正跑 1.5s 级联 + 幂等二跑 PASS；EXPLAIN 对照：7 天窗空扫 **窗内 2 行/毫秒级/裁剪 3 分区**（对照 30 天窗 55,868 行 1,388ms、无窗 pkey 全扫 >30s 击杀）。
- **设计弯路披露（自审计 A2）**：初版「纯 partial index」被本机 EXPLAIN 证伪（79 万 NULL 时 partial 也要全条目扫+probe，252 同样击杀）；二版「30 天窗」被 252 NULL 分布证伪（≤09-30 的行在 30 天窗内仍剩 73 万，要等三个月才自愈）；三版定稿 7 天窗。全部证伪发生在本机验证环节，生产零接触。
- **附带发现（新纪律 73）**：本机库 6 个 per-partition 形态的 `(id) WHERE request_status IS NULL` 部分索引**早已存在**（此前无父表索引、文档注释「没有部分索引」指的是父表级）；本机建 846 时 PG 把既有等价子索引 **ATTACH** 进父索引（pg_inherits 索引继承可证）而非新建——252 与本机 schema 漂移实锤（252 无任何同谓词索引）。

## §五、登记与移交清单

1. **[部署 pending·下一窗口首项]** 845+846 随下次 154/245 部署生效（与 R28 移交的刷新器验收同窗口）。验收判据：① `/metrics` 或日志侧 stats 日聚合 30s 击杀归零（基线 36/日）；② 回填候选 30s 击杀归零（基线 103/日）；③ dailyInsertSQL 慢查 sum 从 307.7s/日回落至 <30s/日；④ matview 刷新维持整点节拍。
2. **[催办]** ①股票客户端 24/7 + 轮内重复 ALTER ×99/日（实例墙钟 57.1%）+ 新错误形状 $2 类型 ×6；②E6 12.0/h 节拍器；③外部新客户端探测（llm_usage_stats 族 ×4 + session_id 猜列 ×2）——三项均外部，维持登记。
3. **[观察]** project_backfill ×145/max 109.1s 四连攀升；WITH win series 回升 3,070s；两者若继续攀升立项。llmgo-252-dev failed 顺延（本轮未复核，R28 登记维持）。
4. **[运维轨顺延]** disk-watch CROND、podman-pg-252 死信文件、log_size_max/journald 提案（R24-R28 顺延链不变）。
5. **[终验节点]** 10-11 03:15 vacuum-bloat 首跑；11-02 路由 7d 复核。
6. **[下轮首查]** ① 845/846 部署后 §五.1 四判据；② stats_event_dedup 无 TTL 增长曲线（914,685 → ?，event_writer 只增不删，登记容量观察）；③ project_backfill/WITH win 是否继续攀升；④ 股票 24/7 与 ALTER ×99；⑤ 外部新客户端探测是否复发。

## §六、新纪律候选

- **73**：**分区父表索引的真实形态要查叶子分区的 pg_index 实体行**——`CREATE INDEX ON 分区父表` 对已有等价子分区索引走 **ATTACH 收编**（不新建，pg_inherits 索引继承可验证），pg_indexes 恒渲染 `ON ONLY` 既不代表缺子索引也不代表有实体；252 与本机库同名表的索引形态可以漂移（本轮：本机 6 个 per-partition partial 早已在位而 252 一个都没有）。凡「加了索引没变快」，先看叶子分区有没有实体索引、计划里用的是哪个名字。
- **74**：**「可回填水位 0」不等于「worker 空转无害」**——回填/巡检类 worker 的健康判据必须含「证明无事所需成本」。结构性不可达集合（本例 79.3 万永久 NULL）若不显式移出扫描面（时间窗/终态标签），每次空扫都为「证明它们不存在」全额付费（103 击杀/日 ≈ 0.86h DB 墙钟）。治理顺序：先问「哪些行永远不可能成为候选」，把它们从查询面上裁掉，再谈索引。
- **75**：**窗口/下界类修复必须用目标环境的分布反推宽度**——30 天窗在本机（NULL 分布到 10-04）看着安全，在 252（NULL 分布 ≤09-30 但 30 天窗下界=09-09）窗内仍剩 73 万行，要等三个月才自愈。下界宽度 = 真实存在窗口（小时级）× 安全系数，用**生产分布的 max(窗外键值)** 校验「部署日即生效」，而非本机分布。

## §七、自审计

- **A1**：提取器首版重踩 R28 A1 点名警告的同款坑（`LOG:` 双空格，`\s` 只吃一个空格 → duration 全零命中），由「n_dur_all=0 vs 首行肉眼可见 duration」当场揭穿后 `\s*` 修正重跑。同一坑连续两轮踩，已随纪律候选升格警惕项。
- **A2**：846 初版（纯 partial）与二版（30 天窗）两次被本机 EXPLAIN/252 分布证伪，均在本机验证环节抓住回炉（§四.2 弯路披露），生产零接触。
- **A3**：首查③初判「积压清零、worker 空转」基于 48h JOIN 口径（=0），漏了全表口径（793,519 永久 NULL）——两口径混用被 §四.2 证据链深挖纠正，「清零」改判「可回填清零、永久 NULL 滞留」。
- **A4**：252 全程只读（SELECT/目录查询 + EXPLAIN ANALYZE 仅 2h 小窗一次 2.3s），零写入零 DDL 零部署；845/846 只在本机真库（127.0.0.1）实跑。快照与脚本以 `r30-audit-20261009-*` 命名留在 252 /tmp（下轮可复核），无覆盖风险。
- **A5**：并行轨零触碰——22e5e3f8f 等 sanitize 轨产出以 rebase 方式收编（无冲突）；834-844 台账、v1 退役门族均只读取证。845/846 编号经 252 台账核对无撞号（台账正典链 max=844，999 为 2026-08-13 legacy marker 非迁移链成员）。
