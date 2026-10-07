# 252 PG SQL 日志审计第二十八轮（2026-10-08）—— R27 三项修复闭环验证轮：compliance 42703 归零 + 357 视图接线待部署量化 + modality 台账约束缝隙（部署顺序）定案 + 股票客户端全天候化（实例墙钟翻倍全归外部）

以 round27 §五.5 为起点。**起点重算**：开局本地 main=a1465fa9a（=origin/main，仅 `web/public/menu-config.json` 一处既有本地改动，与本轮无关）；R27=2026-10-07（85ad5f5da）；其间并行轨产出 1498c576c（357 刷新器接线+陈旧度门，10-08 00:23）+ 0d40a05b5 等，且并行轨于 10-07 12:48→21:23 向 252 台账补应用迁移 834-844（台账 375→388 条）。**本轮无新增仓内根修**——三项真缺陷的修复全部已在库（R27/并行线），本轮性质为**闭环验证 + 部署顺序缝隙定案 + 外部负载升级申报**。**取证窗 = 2026-10-07 06:03:20.667 → 10-08 06:07:27.637（服务器钟，24.07h）**，取自轮转正本 `ctr.log-20261008`（199.9MB，10-07 03:45:12 起）全量 + 活 `ctr.log`（15.2MB）至取证时刻，708,087 物理行 / 30,814 记录。本轮由用户 /goal 指令触发。

---

## §零、传输层巡检：快照层连续产出 ✓ + 今日 copytruncate 洞 13 秒

1. **首查⑤ PASS**：`/var/log/pg17-ctrlog-archive/` 当日链完整（`ctrlog-20261008.log` 33.5MB mtime 05:47 + `ctrlog-20261007.log` 236MB mtime 23:47），R26 动态解析脚本每小时产出持续。
2. **轮转洞申报（纪律 70）**：今日 logrotate copytruncate 发生在 **10-08 03:07:14 → 03:07:27（≈13 秒）**——`ctr.log-20261008` 尾行 03:07:14.103、活 `ctr.log` 首行 03:07:27.275，两侧时间戳交叉证实。窗口内 13s 不可恢复，与 R27 的 03:45 洞（10s/3 行）同族：copytruncate 语义固有，非快照层事故。轮转时刻在 03:07 与 03:45 间漂移，凡窗口含凌晨的审计须两侧取界后申报。
3. **三轴对账**：全文件头锚定 ERROR 1,851 + FATAL 703 / duration 22,767；提取器窗口内 ERROR 1,746 + FATAL 673 = **2,419 锚定错误** / duration **21,092**（其中 ≥1s statement 模式 19,979 + execute/预备语句等其余模式 1,113，见 §二.3 盲区补挖）。Δ 全部落在窗口外段（两文件头部 03:45→06:03 与 03:07→06:07）与 13s 洞，账目闭合。

---

## §一、错误族全景（2,419 条 / 24.07h；头锚定二段计数）

| # | 家族 | n | 归因与处置 |
|---|---|---|---|
| 1 | `canceling statement due to user request` | 1,001（41.6/h，R27 26/h 抬升） | top 形状：request_logs 列表分页 ×202 + turns MAX(turn_no) ×150——R8 已知族。抬升主因 = **10-08 01:30-06:00 夜间单会话翻页风暴**（§二.7）；夜间 01-05h 达 54-103/h |
| 2 | FATAL `connection to client lost` | 644（26.8/h，R27 15.6/h） | 已知自噪音，与 #1 夜间风暴同步抬升；不立项 |
| 3 | E6 pocket `inconsistent types deduced for parameter $4` | 289（12.0/h 节拍器精确维持） | scheduled_tasks 在 pocket 库，ACC 侧 SQL 缺陷，本仓不可修，催办维持 |
| 4 | `canceling statement due to statement timeout` | 259（10.8/h，R27 11.4/h 持平） | top 形状**换血**：session_turns JOIN request_logs ×122（12-14h 白天繁忙窗集中，§二.5 已知豁免设计自愈）+ session 列表 ×25 |
| 5 | `system_probe_runs_task_type_check` 违反 | **103**（12:40→12:47 七分钟，之后零） | **部署顺序缝隙定案（§四.1）**：modality 台账写臂随 10-07 晨构建上线，835 迁移 13:13 才到 252——33 分钟窗口 ×103 全部自愈，13:13:36 起 1,529 行零复发 |
| 6 | FATAL `terminating background worker "parallel worker"` | 28 | 实测会话 kill（session_turns JOIN ×16、v_probe_system_health ×6 等），runbook 会话自噪音 |
| 7 | `column "total_tokens" does not exist`（system_probe_runs） | 24（14:24-15:41） | 外部客户端漂移，R27 家族延续，催办维持 |
| 8 | `function ensure_ursm_node_snapshot_min_daily_partition(date) does not exist` | 6（12:40-12:46，×3/日两日） | **首查③复现**：830=.sql.skip（ursm 轨二选一未决）+ 并行批 831-844 越过 830 应用 ⇒ 函数持续缺席；归 ursm 轨（选项不变 + 新增「删调用方」选项） |
| 9 | 单发杂项 ~44 | ~44 | runbook 实测自噪音（猜列/猜表/语法试错：pg_stat_statements 列、schema_migrations 混名 max(version::int) V359、analysis 函数签名枚举、`cho === matviews ===` heredoc 手误 ×2 等）+ limit_up_temp（TEMP 表）integer out of range ×2 + 外部 `record` 表 NUL 字节 ×1 + `role "root" does not exist` ×1 |

**每小时直方图**：白天 90-176/h，夜间 01-05h 因翻页风暴抬至 90-130/h。**compliance `total_detections` 与 `session_client_stats` 42P01 双归零全窗成立**（首查①②，§三）。json_hex worker 路径零复发第 6 天成立。

## §二、慢 SQL 全景（≥1s 共 21,092 条 / 54,747.9s / 24.07h = **2,275 s/h，R27 1,081 s/h 的 2.1 倍**）

**判定前置（纪律 72 候选）：实例日志混库，回归判定必须先按库拆分。** 股票三表（stock_basic/daily_kline/fundamentals）实证在同实例 **smm 库**（R13「smm 股票库外部铁证」一致），不在 llm_gateway 库。拆分后：

| 库/归属 | n | sum(s) | 判定 |
|---|---|---|---|
| **smm 股票客户端合计** | **14,554** | **30,232（55.2%）** | **首查④升级实锤：全天候化**。R27「00-05h 夜间 6s 轮询、白天 4-18/h」→ 本窗口 24h 全时段 489-622/h（三连 statement 各 4,447 次 ≈ 每 19.5s 一轮 = 27,613s）+ execute 模式 daily_kline MAX(date) 族 ×1,110/2,131s + **`ALTER TABLE daily_kline ADD COLUMN IF NOT EXISTS adjust` ×87/247s（每轮重复跑 DDL，ACCESS EXCLUSIVE 锁）** + `WITH recent` ×16/240s。外部催办再升级：从「夜间循环」改为「24/7 全天候 + 轮内重复 DDL」 |
| **llm_gateway 本库** | ~6,538 | ~24,516（**1,019 s/h，R27 1,081 持平略降**） | 已知族全部维持，无新回归 |

llm_gateway 本库 top 形状（与 R27 对照）：

| 形状 | n | sum(s) | max(s) | 判定 |
|---|---|---|---|---|
| analyze_llm_gateway_table_stats('2') | 46 | 1,821.6 | 104.2 | **首查⑤ PASS（拆时段）**：白天 max 104.2s @09:12 与实测窗吻合；**17h 后稳态 max 50.1s < 90s 判据**，且 17h 起每小时恰 1 条（背景节拍）。R26 顺延判据关闭 |
| 路由候选族（cmb nps ×650/1,808s + matched CTE ×564/1,313s + mps ×184/1,411s） | 1,398 | 4,532 | 27.9 | R8 已知族，11-02 复核判据维持 |
| **project_backfill（WITH targets）** | **123** | **3,521.9** | 117.1 | **回升申报**：R26 205→R27 34→本轮 123（13h 起恒 6/h）。R8 已知族随积压量波动，登记观察不立项 |
| credential_selfcheck v1 LEFT JOIN | 221 | 694.0 | 5.0 | R26 已知，不立项 |
| **session_turns JOIN request_logs（request-status 回填候选臂）** | **360** | **1,388.0** | 27.9 | 新面孔归因：`domains/session/v2/session_request_status_backfill.go:519`（30s tick/batch 100/idle backoff 30min）——**命名豁免的故意设计**（父表 keyset 扫描，admin/session_family_two_surface_test.go 登记，v1 退役轨资产，122 次超时击杀幂等自愈）。登记负载观察：360 慢查/24h + 122 timeout + parallel worker kill ×16 |
| REFRESH routing_analytics_7d | 152 | 661.7 | 27.8 | pss 节奏正常，11-02 判据维持 |
| request_logs 列表读臂 | 479 | 672.6 | 8.0 | **全部集中在 10-08 01-06h**——与错误族 #1 翻页风暴同源，单会话自噪音窗 |
| v_probe_system_health（legacy） | 16 | 255.9 | 28.0 | R27 P3 延续 |
| WITH win（hour/bucket 统计族） | 126+395 | 1,974 | 19.8 | 已知 |
| memora 读臂（client_model 子查询） | 96 | 318.8 | 18.7 | 新形状归因：admin/memora_handlers.go:631，13 s/h 量级，P3 登记 |
| dashboard 普查（api_keys census） | 30 | 208.0 | 26.0 | 归因 admin/dashboard_board_queries.go:264，已知面 |

### 3. 提取盲区补挖（自审计 A2）

execute 模式（预备语句）duration 1,113 条不在 statement 形状分析内：sum 2,135.2s，其中 **1,110/2,131.4s 为 daily_kline（股票客户端）**——已并入 §二股票归属；其余 3 条为散点。此盲区即 R22 纪律 56（多行语句提取盲区）的模式版，已固化为提取器对账项。

## §三、R27 移交项逐项复核（§五.5 首查①-⑤）

| R27 登记 | 本轮复核结果 |
|---|---|
| ① compliance 42703 是否归零 | **闭环 PASS**：全窗零条（R27 ×98/日）。154 现构建 c982c224-2492（20261007）经 git ancestry 实证**包含** 85ad5f5da；245 构建 352cd041-2499 的 SHA 未上 origin 无法 ancestry 验证（披露），但 245 流量在窗内（含完整夜间轮询时段）同样零 42703。判据「零复发」成立 |
| ② session_client_stats 42P01 + 老化幅度 | **42P01 归零 PASS**；老化量化：10-07 06:31 应用后**冻结 18h01m**（至 10-08 00:32 首次手动刷新），窗末仍冻结 **4h26m**（mv refreshed_at=02:00:08 vs 源表 max last_request_at=06:26:09）。10-08 00:32→02:02 四轮手动 REFRESH（3 视图 ×4，单轮最长 67.5s）与 1498c576c 作者实测吻合。**修复代码已入库未部署**，移交下轮部署后验收 |
| ③ ursm ensure ×4 是否复现 | **复现 ×6**（12:40-12:46 两日各 3 条）；830 维持 .sql.skip，并行批 834-844 越过 830 应用。归 ursm 轨，登记 |
| ④ 股票族夜间循环是否仍在 | **升级**：24/7 全天候化（§二），实例慢查墙钟 2.1× 全部归此。催办二次升级 |
| ⑤ analyze max 回落 | **PASS（拆时段定案）**：稳态（17h 后）max 50.1s < 90s；>90s 峰仅在实测窗重叠时段。R26 顺延判据关闭 |

## §四、本轮处置与拍板

### 1. [定案·零代码] modality 台账约束缝隙 = 部署顺序问题，835 正典闭合，fresh-install 链已正确

- **时间线**：台账写臂代码 ≤c6c78773e（10-06 13:53）进入主干 → 10-07 晨构建（154-2492 代）上线 → **12:40-12:47 首轮扫描 ×103 INSERT 全部被 6 值 CHECK 拒绝** → 并行轨 13:13:15-19 应用 834-836（**835_modality_verify_probe_ledger** 于 13:13:17 落台账）→ **13:13:36 首行成功**，至窗末 1,529 行零复发。
- **仓内核验**：835 立项书完备（2026-10-06 决策「只接台账」四证据链），CHECK 只放宽词表、结构上不可能因存量失败；752→835 迁移链对 fresh-install 正确（installer embeddata 双通道在位）。**产品无缺陷，缝隙仅在「写臂先于迁移抵达存量环境」的 33 分钟**——835 台账 applied_at 与 103 条失败逐秒互证。
- **不代修的理由**：修复本体（835+代码）已在 main 且已在 252 应用；残余动作只有一个流程教训（§六.71）。

### 2. [拍板] 其余

- 股票客户端 / E6 / total_tokens = 外部，催办分级维持（股票升级）。
- request-status 回填、project_backfill、memora 读臂、dashboard 普查、vpsh = 登记观察，不立项。
- **运维轨登记新增：llmgo-252-dev 自 10-06 01:01:40 failed（Result: timeout, SIGKILL），已停摆 2 天+且 unit=disabled**——252 实例侧三网关通道只剩 154/245。按 R27 纪律不代重启（过期构建无人管），登记待运维轨拍板（重启/下线二选一）。
- 运维轨顺延不变：disk-watch CROND、podman-pg-252 死信文件、log_size_max/journald 提案。

## §五、登记与移交清单

1. **[部署 pending·下一窗口首项]** 1498c576c（357 刷新器接线+陈旧度门）随下次 154/245 部署生效；验收 = 部署后 matview refreshed_at 按其周期推进、无手动干预。
2. **[ursm 轨]** 830 二选一（现新增第三选项：删 ensure 调用方）；ensure ×6/日持续中。
3. **[催办]** ①股票客户端：24/7 轮询 + 每轮重复 ALTER TABLE DDL（实例慢查墙钟 55.2%）；②E6 12.0/h；③total_tokens 外部漂移。
4. **[运维轨]** ①llmgo-252-dev failed 2 天+（新）；②357 刷新器部署项见 #1；③disk-watch CROND/死信文件/journald（R24/R26/R27 顺延）。
5. **[终验节点]** 10-11 03:15 vacuum-bloat 首跑（前置不变）；11-02 路由 7d 复核。
6. **[下轮首查]** ① 1498c576c 部署后 session 三视图 refreshed_at 是否自动推进（陈旧度门生效）；② system_probe_runs modality_verify 零复发延续 + 1,529 行后的日增曲线；③ request-status 回填 worker 是否随 v1 退役推进收敛（360/日 + timeout 122 基线）；④ 股票客户端 24/7 是否持续（若持续升级至限流/防火墙议题）；⑤ 夜间翻页风暴会话是否再现。

## §六、新纪律候选

- **71**：**写臂与迁移必须同窗口抵达每一存量环境**——代码（新词表/新表/新列的写方）先于其正典迁移到达存量库时，产生「产品无缺陷但线上报错」的缝隙窗（本轮 103 条/33 分钟，自愈侥幸依赖并行轨恰好同日应用 834-844）。凡写臂上线，核对目标环境台账含对应迁移号才算部署完成。
- **72**：**实例级慢查墙钟回归判定必须先按数据库拆分**——同实例混库日志（llm_gateway/smm/acc_db/pocket/memora/casdoor）的墙钟翻倍（1,081→2,275 s/h）可以完全来自单一外部库（本轮 55.2% = smm 股票客户端），不拆分即误判「网关负载翻倍」。R14「实例级分析须按 dbid 过滤」的慢查版。

## §七、自审计

- **A1**：提取器首版重踩 R27 A1 点名警告过的同款坑（`LOG:` 双空格、`\s?` 只吃一个空格 → 形状分析零命中），由零命中当场揭穿后以 `\s*` 修正重跑；三轴以头锚定正则独立复算闭合。
- **A2**：slow.py 的 EXEC 正则漏 execute 模式（预备语句名含数字/下划线），由「提取器 duration 21,092 vs 形状分析 19,979」Δ=1,113 对账揭穿，补挖后确认 99.7% 为股票客户端并并入归属（§二.3）。
- **A3**：information_schema 对物化视图不返回列（PG 已知怪癖）造成一次「视图被删」误报，pg_class relkind='m' 复查纠正；物化视图列名两次猜错（last_active_at/last_request_at→实为 last_seen_at/refreshed_at）均由 42703 当场纠正后改用 pg_attribute。
- **A4**：本轮自噪音 3 条已登记：06:10-06:20 的 42703 ×3（session_client_stats/session_summaries 列名试错，A3 过程产物）；对 252 全程只读（SELECT/系统巡检），零写入、零 DDL、零部署。
- **A5**：并行轨零触碰——834-844 批（含 835）、1498c576c、v1 退役门族、runbook 实测会话均只读取证；252 台账 375→388 的增量全部为并行轨应用记录，本轮未动台账。
