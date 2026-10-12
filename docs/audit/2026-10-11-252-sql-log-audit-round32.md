# 252 PG SQL 日志审计第三十二轮（2026-10-11）—— 宿主 OOM 七连环崩溃全景 + R31 修复部署验证 + 抽屉回退查询 hot 快路径根修

以 round31 §五.7 为起点。**起点重算**：开局本地 main=4c8cce53d，`git fetch` 后 origin 领先 25 个提交（并行轨：web-mobile 运维闭环、sanitize r1013-r1016、部署脚本 252 巡检单实例闸门、**851 已被并行 sanitize 轨占用**——本会话原计划的 session_dim 覆盖索引让号改登记不落 DDL，见 §四.2），ff-only 收编至 cb153c55e 无冲突。**取证窗 = 2026-10-10 06:11:38.203 → 10-11 06:02:23.034（服务器钟，23.85h）**。数据源三段：归档 ctrlog-20261010.log（**840MB 表观 / du 802MB——真实内容非稀疏洞**，10-10 全天日志体量为 10-09 的 5.5×）、归档 ctrlog-20261011.log（63MB）、活 ctr.log 快照（剥 NUL 洞后 7.1MB）。本轮由用户 /goal 指令触发。**主发现：10-10 下午宿主全局 OOM 引发 PG 七连环崩溃恢复循环，31,547 次连接拒绝构成 840MB 日志日的主体。**

---

## §零、传输层巡检

1. **快照层连续产出 ✓**：归档链完整（10-10 840MB / 10-11 63MB@05:47），cron `47 * * * *` 每小时快照在位。
2. **时钟偏斜消失**：252 与本机钟差从 ~10min（R14 fact 83）收敛到秒级（双端 date 对时实测）——旧偏斜观察项销案。
3. **三轴对账**：物理行 4,413,321 / 头锚定记录 161,582（窗内 141,779）/ 头锚定 ERROR+FATAL **47,580**（R31 5,829 的 8.2×）/ duration ≥1s **51,608 条**，sum **182,947.3s（50.8h，墙钟 2.1×）**（R31 81,284s 的 2.25×）。log_min_duration_statement=1s 语义自洽（n_dur_all == n_dur_1s）。
4. **提取器升级**：R31 版只剥 `stderr F` 帧前缀，巨 payload 语句被 conmon 切成 `stderr P` 分片时 PG 头嵌在 P 帧内会漏。R32 版加 P 帧分割慢路径（行首 ISO+`stderr` 即按帧边界 split 递归处理）。本轮实测 **n_p_lines=0**——840MB 由海量小条目构成（非 R15 式巨 payload 爆发），P 路径未被触发但已就位。

## §一、主发现：宿主 OOM 七连环崩溃循环（10-10 14:33 → 16:12）

### 时间线（全部锚定取证）

| # | signal 9 击杀 | reinitializing | ready to accept |
|---|---|---|---|
| 1 | 14:33:54.906 | 14:34:18.854 | 14:36:48.372 |
| 2 | 15:02:00.161 | 15:02:13.047 | 15:03:46.055 |
| 3 | 15:13:36.180 | 15:13:47.408 | 15:15:34.688 |
| 4 | 15:23:15.806 | 15:23:29.229 | 15:25:34.238 |
| 5 | 15:38:17.558 | 15:38:36.083 | 15:40:19.715 |
| 6 | 15:58:50.978 | 15:59:08.302 | 16:00:26.959 |
| 7 | 16:11:24.120 | 16:11:44.568 | 16:12:35.808 |

每轮崩溃恢复 1.5-2.5 分钟；期间新连接收到 FATAL `the database system is in recovery mode`（22,788 条）与 `not yet accepting connections`（8,759 条），客户端连接池疯狂重试构成拒绝风暴（5 分钟桶峰值 6,125）。

### 归因链（证据闭环）

1. **内核 OOM 实锤**：`journalctl -k` 14:33:40 `systemd invoked oom-killer` → 14:33:54 `Out of memory: Killed process 1052193 (postgres) ... shmem-rss:3323932kB`；16:07:17 第二批 OOM 同时杀两个 postgres（shmem 1.4GB/772MB）。dmesg 与 PG 日志 signal 9 时间戳精确吻合。
2. **constraint=CONSTRAINT_NONE global_oom**——**宿主 15GB 全局耗尽，不是容器 10g 限制**。OOM 快照：anon 13.2GB + **Free swap = 0kB（4GB swap 全满）** + Normal 区 free 171MB < min 174MB。容器 RestartCount=0 / StartedAt 10-05 / OOMKilled=false——七轮全是容器内 postmaster 自恢复，容器未重启。
3. **内存棘轮形态**：巡检时点 9 个 llm_gateway 后端各持 RSS 1.4-1.9GB（含共享页），llm_gateway 角色连接池 154=25 / 245=19 连接。work_mem=8MB 默认，大排序（matview REFRESH max 168.9s、analyze max 583.2s、smm COUNT 族）撑高后端 RSS 后 glibc arena 不还 OS；池侧 MaxConnLifetime=30min（db/db.go:64）已设，单后端膨胀有界但 44 连接 × 平均膨胀在宿主层面叠加。kswapd0 自 7 月常驻 = 慢性超募背景。
4. **放大器（本轮根修对象）**：admin credential monitor 抽屉 batch 端点（8 worker × ≤100 pairs）对空 pair 的 Redis miss 全部落到 canonical 视图回退查询（med 1.27s/次）。管理员 15:00 起（首次恢复后 24 分钟）打开抽屉排查事故并整晚保持——**15:00-01:00 每小时 770-3,283 次**，峰值 55/min。观测负载在事故期反向放大 DB 压力。

### blast radius 与伴生症状

- 31,547 连接拒绝 + 全部在飞查询被 signal 9 连坐（36.2s 的 orchestration_active_tenants、23.6s 的 routing_overrides 读等"空表名桶"受害者多落在此窗）。
- 00:18 锁超时 ×29：sessionarchive archiver 的运行时 `SET LOCAL lock_timeout='2s'; ALTER TABLE session_summaries ADD COLUMN last_accessed_at/archived_at` 与正典 DDL 争锁——**两列现已落地（information_schema 实证），×29 自愈销案**；archiver 的懒建列机制按设计工作。
- kxuser 密码失败 ×6 全部集中在 17:11:07-12（5 秒内 6 连错后停止）= 外部（smm/kaixuan 侧）客户端瞬时配置错误，自愈，登记。
- DSM（could not attach/map dynamic shared memory）×144 中 140 条在 14:33-16:12 崩溃窗、窗外仅 4 条（最后 16:38）——R31 §五.3 的 DSM 登记项按「崩溃窗症状」关闭，非独立缺陷。
- `terminating background worker "parallel worker" due to administrator command` ×539（R31 152 的 3.5×）——**语句归属修正**：抽样 STATEMENT 全文 = health-grade COUNT FILTER 聚合（admin/dashboard 面板族），不是 R31 归因的 gauge。leader 被 30s rolconfig 取消后 worker 陪葬；部署后仍有 440 条（见 §三），是独立于 gauge 的面板族成本，登记为面板族慢查治理项。

## §二、错误族全景（47,580 条 / 23.85h；头锚定二段计数）

| # | 家族 | n | 对比 R31 | 归因与处置 |
|---|---|---|---|---|
| 1 | `in recovery mode` + `not yet accepting connections` | 22,788+8,759 | **新** | §一 OOM 七连环的主体，host 内存治理移交 ops |
| 2 | `canceling statement due to user request` | 9,461 | R31 3,325 **2.8×** | 全天分布（非仅夜间），含崩溃窗连坐 + smm 白天负载断连；外部+事故放大，随 §一 治理预期回落 |
| 3 | FATAL `connection to client lost` | 4,635 | R31 1,722 2.7× | 与 #2 同源同步 |
| 4 | `canceling statement due to statement timeout` | 877 | R31 261 3.4× | 饱和膨胀；gauge 击杀部署后归零（§三.1），余为面板族（health-grade COUNT / auto-cool / ursm health）|
| 5 | `terminating background worker` | 539 | R31 152 3.5× | §一 伴生症状：health-grade COUNT 面板族 leader 陪葬 |
| 6 | E6 pocket `inconsistent types $4` | 271 | R31 279 节拍器维持 | ACC 侧，催办维持 |
| 7 | DSM attach/map | 144 | R31 57 2.5× | §一：崩溃窗症状，窗外归零，销案 |
| 8 | `canceling statement due to lock timeout` | 29 | 新 | 00:18 archiver 懒建列 DDL 争锁，列已落地，自愈销案 |
| 9 | `password authentication failed for user "kxuser"` | 6 | 新 | 17:11 单簇 5 秒内外部客户端配置错误，自愈，登记 |
| 10 | deadlock | 3 | — | 19:00-01:00 零星，无模式 |
| 11 | 杂项 42703/42P01/22P02 一次性 | ~30 | — | AI 生成 SQL 试库散点（llm_usage_stats/raw_data/q/V359…）；`invalid input syntax for integer: "V359"` ×3 = 外部工具 `version::int` 撞台账 V 前缀行（fact 154 混名陷阱的外部版），登记 |

**compliance 42703 / current_role 双归零延续全窗**；`system_probe_runs_task_type_check` 违反零复发第 5 天。

## §三、R31 移交项逐项复核（§五.7 首查①-⑥ + vacuum-bloat）

| R31 登记 | 本轮复核结果 |
|---|---|
| ① 849/850 四判据 | **849/850 已于 10-10 11:51:27/54 应用**（schema_migrations 实证，154/245 部署窗）。分项见下 |
| ①-a gauge 击杀归零 | **PASS（改良达成）**：30s 击杀归零（60s 代码钉生效），部署后 24 次完成态慢查 max 57.1s < 60s 钉；/metrics 实值未直接验（Bearer），以完成态查询存在为代理证据 |
| ①-b term_worker 归零 | **FAIL（归属修正）**：部署后 440 条，抽样=health-grade COUNT 面板族非 gauge——R31 把 152 全归 gauge 是错归因，面板族是独立成本，登记 §五.2 |
| ①-c WITH targets sum < 500s/日 | **部分达成**：48h 谓词部署后 99/99 全带（代码生效实锤）；窗外安静时段（01-06h）max 从 116s 降到 7.6-21.6s，但慢查计 n≈6/h（半数 tick 已 <1s 不进慢日志）。EXPLAIN 实证 252 planner 正确走 850（21,715 行窗扫描 + sd pkey 探测，暖缓存 442ms）；残差=冷缓存下 2.2 万候选 × session_dim 随机堆探测（81k buffers）。**未落新 DDL 的理由**：候选行=48h 内新写行，visibility map 未置位，覆盖索引（INCLUDE 列）的 index-only 会退化堆回表，预估收益 20-40% 不值一次迁移（纪律⑳）。登记 §五.3 |
| ② stats_event_dedup 曲线 | 933,868 → **956,049**（+22,181/24h ≈ 924/h，趋势不变，容量登记不变） |
| ③ WITH win 是否真归零 | **形状键混计修正（纪律㊼ 复用）**：精确头 `WITH win AS` 全天 1,308 条 / 5,813s，实为两个子形状——auto-cool failure-ratio（含 2026-09-25 注释指纹）与 ursm_node_snapshot_min 时序查询（max 161s）。R31 观察的"21:18 归零"对象在 252 统计下无法与二者的混合计数区分；两子形状均为面板/健康检查族，30s 边界击杀候选，登记 §五.2 |
| ④ 股票 ALTER 曲线 | ×87→99→123→**190/日**（+54%，升级条件第三次触发，见 §五.4） |
| ⑤ DSM 复发 | §一：崩溃窗症状销案 |
| ⑥ analyze 回落 | **未回落**：max 583.2s（R30 判据 <90s 破，R31 已宽限至风暴评估）——10-10 全天饱和日，与 §一 同源；11-02 复核点维持 |
| 附 vacuum-bloat 首跑 | **PASS（历史上线以来首个成功周日）**：03:15:02-03:16:30，columnar_internal.chunk 493MB→133MB、stripe/chunk_group→0、session_summaries 993MB（dead 52.3%）VACUUM FULL 完成。索引 bloat 报告 04:41 measured=48 flagged=8 headroom≈377MB（notify failed = 通知通道小故障，登记） |

## §四、本轮根修（一项代码，252 只读取证 → 本机真库实跑验证）

### 1. [P2·代码] credential monitor 抽屉回退查询 hot 快路径（观测负载不得放大事故）

- **证据链**：§一.4——`SELECT ... FROM request_logs_with_current_month WHERE credential_id=$1 AND lower(COALESCE(outbound_model, client_model))=lower($2) AND ts > NOW()-($3||' minutes')::interval ORDER BY ts DESC LIMIT $4` ×14,990/24h（本库第一大慢查族，sum 31.7Ks = 全窗慢查 17%），med 1,568ms、安静时段 med 1,266ms。视图三臂（turns_hot+turns 分区+request_logs LATERAL 富化臂）对 **session_turns 全分区父表双重反连接**，LIMIT 下推被阻断（R8 fact 43 结构性）——计划上无便宜化空间，必须绕开视图。指纹澄清：R30 fact 158 曾判"会话型负载不立项"，本轮实锤为抽屉 batch 端点的系统性回退（8 worker × ≤100 pairs × 空 pair Redis miss）+ 事故期人工 kept-open。
- **修复**：`admin/credential_monitor.go` 新增 `slidingWindowHotQuery()`（`session_turns_hot LEFT JOIN session_turn_details_hot` ∪ `request_logs_hot` 反连接 `session_turns_hot`），`slidingWindowFromRequestLogs` 按窗口闸门分流：≤ `slidingWindowHotMaxMinutes(120)` 走 hot 快路径，>120min 保留视图慢路径（batch 端点上限 1440min，长窗语义全域正确）。**窗内语义等价论证**：与 request 行同生的 turn 必在 hot（写路径同步 + hot 保留 8h 名义/12h 滞后上界 ≫ 120min 窗），故分区臂与全分区反连接可安全省略。
- **252 真库验证**：① EXPLAIN ANALYZE **0.644ms**（视图 med 1,266ms，**~2,000×**），全臂索引条件下推（idx_session_turns_hot_credential_ts / idx_request_logs_hot_credential_model_ts / 反连接 Index Only）；② 语义对拍 pair=(35, claude-opus-5) 90min 窗 EXCEPT 双向差集 **0/0**。
- **本机真库回归**：`TestCredentialDrawerHotWindow_RealDB`（执行+计划断言：触碰视图即红）+ `TestCredentialDrawerWindowPathsAgree_RealDB`（同窗双路径一致性冒烟）+ 原 `TestCredentialDrawerWindow_RealDB`（视图长窗 729 断言保留）三测全绿（TEST_DATABASE_URL=本地 llm_gateway）。
- **登记门同步**（改动引入新 v1 直读者，两道静态门按机制登记）：`requestLogsReadInventory` admin/credential_monitor.go 3→4（本轮完成复审）；`unjudgedBaseline` 47→48 + 基线集合增补 + 书面理由（③ 类按设计读 v1，退役轮改指）。

### 2. [评估后不立项] session_dim 覆盖索引（852 号让号）

WITH targets 残差（冷缓存 8-20s/tick）的候选杠杆 `CREATE INDEX ON session_dim (gw_session_id) INCLUDE (project_id, application_code, tenant_id)` 经评估**放弃**：候选行是 48h 内新写行，页面上 visibility map 位未置位，index-only 必退化堆回表，预估收益 20-40%（纪律⑳：EXPLAIN 与机制分析先于改写）。结构化解法（已证不可解析 key 的负缓存表）涉及 worker 双语句改造，登记 §五.3 待后续轮评估。另：**851 已被并行 sanitize 轨占用**（851_disable_overbroad_pii_ip_address_rule.sql），本会话立项前 fetch 重查迁移号（纪律㉒）再次生效。

## §五、登记与移交清单

1. **[P1·ops 移交] 宿主内存治理**（§一）：15GB 宿主 OOM ×7 连环，swap 4GB 全满、kswapd0 常驻。候选杠杆（属主拍板）：①宿主内存扩容或 swap 扩容；②PG 容器内存上限与宿主预算对齐（当前 10g 上限 vs 15GB 宿主，redis+smm backfill+系统在其上叠放）；③llm_gateway 角色 `statement_timeout` 之外增 `work_mem` 上限或 REFRESH/analyze 会话级内存帽；④降低面板族高频聚合的并行度。**本轮零生产参数变更。**
2. **[面板族登记]** health-grade COUNT（term_worker ×539 陪葬主体）、auto-cool failure-ratio（WITH win 子形状）、ursm_node_snapshot_min 时序（max 161s）三族均在 30s rolconfig 边界运行，饱和日集体进入击杀/慢查名单。治理面=窗口化/降频/内存帽，与 §五.1 联动，登记待产品拍板。
3. **[project_backfill 残差登记]** 部署后安静时段 max 7.6-21.6s（对照修复前 40-117s）、半数 tick <1s；§四.2 已证覆盖索引非杠杆。负缓存表方案（worker 证伪后记 key，候选查询 NOT EXISTS）为候选结构解，涉及新表+双语句改造，下一轮评估。R31 判据 <500s/日 视宿主恢复情况在 11-02 复核点重测。
4. **[股票客户端升级·第三次触发]** ALTER daily_kline ×87→99→123→**190/日**；COUNT 族 3,512 条 / 36,253s（全窗慢查时长 19.8%）；R28 升级条件（持续上涨→限流/防火墙议题）已满足。**新事实：客户端实为 252 本机 /opt/kaixuan/smm/backfill_loop.sh，cron `*/5` 保活**（本轮巡检时 fundamentals backfill --max 150 在跑，68% CPU）——"外部"实为同宿主租户，内存/IO 与 §五.1 同锅。移交属主。
5. **[催办维持]** E6 pocket 11.3/h 节拍器；04:50 外部探测 + AI 生成 SQL 试库散点扩大（V359 版本探测新形状）。
6. **[终验节点]** 11-02 路由 7d 复核 + analyze <90s 复测 + project_backfill 判据重测。
7. **[下轮首查]** ① Fix B 部署后 mega-family 慢查归零验证（hot 路径亚毫秒不进慢日志；视图路径仅 >120min 窗残留）；② 宿主内存治理动作落地情况（OOM 复发即升级）；③ 面板族击杀曲线（§五.2 三族）；④ 股票客户端是否被限流（×190 曲线走向）；⑤ stats_event_dedup（956,049→?）；⑥ 11-02 临近项预检。

## §六、新纪律候选

- **78**：**「部署验证判据的语句归属必须抽样 STATEMENT 全文」**——R31 把 term_worker ×152 全归 gauge（同期唯一在修的族），本轮抽样实锤主体是 health-grade COUNT 面板族，判据②"term_worker 归零"从"修复无效"改判"归属错误"。同族教训㊷（同关键字≠同查询形状）的部署验证版：验证窗口内的伴生错误族要在修复语义之外独立归因，不能默认全归当前修复对象。
- **79**：**「排序/窗口键与修复索引键的一致性要按目标库 EXPLAIN 复核」**——R31 的 48h 窗在本机 EXPLAIN 走 850，252 上因 `ORDER BY ss.session_key` 与 762 索引 `(session_key) WHERE gw_project_id IS NULL` 完美匹配而存在翻面风险（本轮实测 252 当前选择 850，未翻面，但翻面条件真实存在）。修复迁移与查询形状跨库验证时，ORDER BY 键必须显式对照每个同谓词索引。

## §七、自审计

- **A1**：252 /tmp 撞车检查前置：R31 快照在 /tmp/r31-audit-20261010/ 完好复用（extract.py 模板），本轮新目录 /tmp/r32-audit/ 双标识命名。
- **A2**：分类器两处形状键混计当场纠正（§三③ WITH win 双子形状；§一 term_worker STATEMENT 抽样归属）——均为「锚定头提取+语句全文抽样」二段计数纪律（㊷/78）的执行案例，未进报告前证伪。
- **A3**：252 全程只读（SELECT/EXPLAIN 无 ANALYZE 执行面、目录、日志）；两处 EXPLAIN ANALYZE 均用只读 CTE 变体（count(*) 外层，sync_session_project_attr 写函数未执行）。生产零 DDL、零参数变更。
- **A4**：ssh heredoc 剥引号老坑复踩一次（fact 108），SQL 一律 Write+scp 文件通道后收敛；podman exec -f 路径在宿主不在容器内（stdin 管道通道纠正）。
- **A5**：并行轨零触碰：ff-only 收编 25 提交无冲突；851 撞号在立项前发现并让号（§四.2）；并行会话同日产出的 24h 审计报告（docs/audit/2026-10-11-24h-audit-pass.md）主题（sanitize/installer/部署脚本）与本轮零交集，其 P1-1 五点同步修复为本轮迁移登记提供了完整先例。
- **A6**：admin 全包测试首轮 FAIL 经 stash 对照甄别：2/3 为本轮新读方触发的登记门（按门机制登记后转绿），1/3 为 AGENTS.md 载明故意红的负向闸门（TestV1BodiesReadersAreAssessed，不修）。无既有测试被我改动破坏。
