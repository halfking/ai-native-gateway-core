# 252 SQL 日志审计轮（2026-10-19）

> 会话：ZCode（本仓 llm-gateway-go-4 checkout）。
> 目标（用户指令）：对 252 docker 中 pg-252-pg17 的全部 SQL 日志审计，找出慢 SQL 与
> 错误 SQL，分析并修正。
> 上轮：2026-10-18（872 CMI 转堆 + smm 守卫首装）。本轮窗口从上轮覆盖边界之后起算。

## 一、窗口与方法（起点重算）

- **日志源**：podman `ctr.log`（容器完整 ID `dc5e54272d7a3561…`，上轮记录的短前缀
  路径无法直接定位——`podman ps -q` 给 12 位短 ID 而**目录用 64 位完整 ID**，经
  `podman inspect …HostConfig.LogConfig.Path` 取得）。
- **窗口**：`ctr.log-20261019`（10-18 03:20 → 10-19 03:22，684MB）+
  `ctr.log`（10-19 03:22 → 06:04，692MB）；按时间戳过滤 ≥ 10-18 06:02（上轮覆盖边界）。
  计 **24.0 小时**，压缩后合计 ~62MB 拉回本地解析。
- **方法**：沿上轮 perl 逐行状态机（剥 podman `TS stderr P/F` 包装；`%m [%p] LEVEL:`
  行开记录；语句体并入）。本轮修了三个解析缺陷：
  1. **慢查行内联格式**：`log_min_duration_statement` 的输出是
     `LOG:  duration: X ms  statement: <全文>`（单条消息、小写 `statement:` 内联），
     不是独立的 `STATEMENT:` 行——独立大写 `STATEMENT:` 行只用于错误记录。
  2. **带前缀的空 `STATEMENT:` 行**（本窗口 6,781 条）：PG 对长语句会把
     `TS [pid] STATEMENT:` 作为带前缀行发出、语句体在后续无前缀行。此前解析会把
     它开成新记录、语句体全部丢弃；修正为「带前缀 STATEMENT 并入当前记录」后
     孤儿数 6,781 → 0，错误记录的语句携带率 4,758/6,941。
  3. 归一化引号正则换成线性匹配（`'[^']*'(?:''[^']*')*`）——原 `(?:[^']|'')*`
     在超长语句上回溯爆炸（会碎家族计数）。
- **产物**：错误 6,941 条 / 97 族；慢查 16,755 条 / ≥1s 累计 67,729s；
  **`<EMPTY>` 伪影 0 条**（上轮 350 条，解析缺陷修复后消失）。
- **量具坑（新增）**：本机 `cut`/`awk` 默认 locale 会在含中文的语句行上
  `Illegal byte sequence`/`towc failure` 静默截断统计——家族聚合全程 `LC_ALL=C`；
  `grep` 对含坏字节行需 `-a`。

## 二、主发现：10-18 早晨 06-12 时全面变慢事件（11,030+ 条慢查、2,865 次取消）

### 2.1 事件形状

- 慢查逐小时：06 时 2,192 → 09 时 **3,089（峰）** → 12 时 1,002 → 13 时起回落到
  基线 200-500/h。06-13 时合计 **12,030 条 / 41,663s**，占全天 72%。
- 同窗口错误侧：`canceling statement due to user request` 2,865 次（06 时单小时
  1,023）、`connection to client lost` 1,294 次——被取消语句 TOP 全是网关生产核心
  （路由策略 `WITH policy` ×780、凭据清单 ×353、session_turns 写读 ×200+）：
  形态为「网关查询被拖慢 → 客户端放弃 → 取消」。
- 09 时连 `commit` 都有 82 次 ≥1s——主机级饱和特征（WAL flush 等待）。

### 2.2 事件构成（逐项实证）

| 成分 | 证据 |
|---|---|
| **网关迁移重放批（06:00-06:35）** | 06:18 `CREATE INDEX idx_session_turns_digest_null ON ONLY public.session_turns` **163s**（744，非 CONCURRENT，SHARE 锁堵写 163s）；06:19 `CREATE TABLE proxy_subscriptions`（646）；`apply_llm_gateway_autovacuum_settings()` ×2；06:19 `DROP VIEW v_model_health_dashboard CASCADE` 等 idempotent DDL 连发 |
| **人工 ANALYZE 战役** | 06:15 `ANALYZE session_turns_2026_09` **130s**；06:26 `ANALYZE public.session_turns` **670.6s**（单条最大 IO 消耗） |
| **CMI 父表锁队列** | 06:01-06:09 共 **654 次** `still waiting for AccessShareLock on relation 217766`（=credential_model_index 父表）；refreshIndexSQL 20 次 5-25s；被堵语句=网关凭据/路由读。AccessShare 只与 ACCESS EXCLUSIVE 冲突 ⇒ 队列头是排他锁请求；持有者列表全是普通读语句（PG17 队列渲染）⇒ 高置信归因为重放批里 337/347/354/654 的 DETACH 守卫短排他锁被队列放大（单条 <1s 未入慢查日志，未逐条定案） |
| **外部杀查询** | `terminating background worker "parallel worker" due to administrator command` **552 次**（本仓无任何 `pg_terminate_backend` 调用点，vendor 除外）⇒ 有外部动作在杀长查询（杀 leader 时其并行工人以此消息退出），与 DDL 批「清障」行为吻合 |
| **smm ALTER 洪水（守卫失守，见 §2.3）** | 12 次 pre-guard（06:02-07:07，最长 47.5s） |
| **acc 侧 DDL** | `route_incidents DROP CONSTRAINT` ×4、`recommendations ADD COLUMN`、`work_type_model_route ADD COLUMN`、`CREATE INDEX CONCURRENTLY idx_route_incident_events_created_at` **414s** |
| **standard_iq DDL** | 06:03 `ALTER TABLE models_canonical ADD COLUMN standard_iq …`（`cmd/fetch-standard-iq` 工具批量首装，另见 §三 `//` 草稿同源） |
| **REFRESH 风暴** | tuning_signals_5m 每 2-5min、session_task_stats 145.5s、session_client_stats 116s、session_client_task_matrix 45s |

**结论**：事件是「迁移重放批 + ANALYZE 战役 + 跨系统 DDL」在周六早晨叠加引爆，
锁队列（CMI 654 次、proxy_nodes RowExclusive 323 次、ShareTx 167 次、
stats_event_dedup 索引插入等待 267 次）与外部杀查询互相放大，全库读写下探。
13 时起无新 DDL、负载回落后自行恢复。**无单一根修动作**；运维教训是：
迁移重放/ANALYZE 战役应避开业务早高峰，且 `CREATE INDEX`（非 CONCURRENTLY）
163s 级别的语句不该在活跃库白天跑（744 的索引在 fresh-install 语境设计的）。

### 2.3 smm ALTER 守卫失守——根因与修复（本轮唯一修复动作）

- 上轮守卫（10-18 07:14 部署）后本窗口仍有 **6 条 ALTER**（07:17/07:23/09:30/
  12:51/14:31/14:37，最长 45s）——「恒零」预期未兑现。
- **根因（实测）**：上轮只补了宿主副本 + `smm` 主容器两处；舰队里
  **`smm-collector` / `smm-mcp` / `smm-web` / `smm-watchdog` 四容器的
  `/app/src/data/pg_store.py` 仍是 10-08 无守卫版**（md5 `8f86a70a…` 四容器一致，
  与守卫前备份一致，即与守卫版 diff 只差守卫补丁）。
- **修复（本轮）**：守卫版（md5 `7a7c0b25…`）docker cp 进四容器（容器内各自留
  `.bak_20261019`），`smm-collector` 内用其解释器实跑 `PgStore()` ×2 通过。
- **验证现状**：活日志（10-19 03:22 起）`ALTER TABLE daily_kline` **0 条**；
  严格「恒零」判定留下一轮（需覆盖一个白天 cron 周期）。
- **教训**：「容器内补丁」必须**枚举整条容器舰队**而不是只补主容器——
  同一镜像派生的 N 个容器各自持有独立可写层副本。

## 三、错误 SQL 审计（窗口 6,941 条 ERROR/FATAL，97 族）

### 3.1 pocket `scheduled_tasks` $4 双型错——仍在烧（第 N+2 次复登）

`inconsistent types deduced for parameter $4 at character 135` **289 条**（≈12 条/小时，
与上轮 ~294/天持平）；10-19 03:23、03:28 仍在发射。修复 `fa20e648` 依旧未部署。
每次失败都是一次 claim 续租失败，pocket 任务调度持续受损。

### 3.2 `AND AND` ×8——本仓 admin filter-options 缺陷，已被并行会话修复

10-18 23:33-23:40 连发 8 条
`SELECT s.c1, s.c2, s.c3 … GROUPING SETS ((1),(2),(3))`，两臂都有
`AND AND tenant_id = 'default'`。**归因更正**：首轮排查只在本 checkout 搜（落后
origin/main 12 提交）判为「外部工具」——快进后确认这是 admin filter-options 端点的
真缺陷，origin/main `f05058f7c`（fix: filter-options 的 AND AND 语法错 + ss 臂无界
500）已修。8 连错是带缺陷二进制的生产排放 + 端点自动重试。
**教训入档：下「不在仓里」结论前必须先同步远端再搜。**

### 3.3 `//` 草稿语法错 ×2——并行会话 data-lifecycle 工作流的现场排放

10-19 00:39/00:40：SQL 第 2 行起是 `//` 注释（`-- ★ 2026-10-19：…改读
dataLifecycleCanonicalSourceSQL() …` 的续行写成 `//`），e8f186916 同型。
origin/main `3be79b5df`/`023861173` 即该工作流的成品修复。草稿直连生产库试跑
排放，**提醒并行会话：草稿上真库前先过 sqlguard**。

### 3.4 手工/诊断噪音（登记不修）

- `\timing on` 粘进 SQL 窗（截成 `iming on`）×2（06:33，比较「现行/加 7 天窗」的
  判据实验——并行审计会话手滑）。
- **中文 markdown 报告文本被当 SQL 发送** ×1（11:57 FATAL connection lost，
  语句是「…fs 余额镜像整体清空…`DELETE FROM acc_opening_balances WHERE
  source='fs_import'`…」的叙述文本）：某个 AI 会话把文档段落灌进了数据库连接。
  修不在本仓；建议 acc/编排侧排查哪个 agent 的 DB 写入通道会注入文档文本。
- `syntax error at or near "AND"`（非 AND AND 形）×0——已并入 3.2。
- `column cl.task_type` ×3、`llm_usage_stats` ×4、`llm_optimization_*` ×6：
  「LLM 优化器」探错库工具族（上轮已登记，仍在探测，无收敛迹象）。
- `round(double precision, integer)` ×4、`could not determine data type of
  parameter $2` ×4、`integer out of range` ×2：smm 自身缺陷（已知）。
- DSM 8+4、`unsupported frontend protocol` ×2、`invalid length of startup packet`
  **642 条**（LOG 级，端口探测噪音）：全部已知。
- **体检脚本引用已删视图** ×1（19:24）：`SELECT '_request_logs_hot' … (SELECT
  count(*) FROM public.request_logs …)` 报 `relation "public.request_logs" does
  not exist`——owner 10-16 删了视图层，某个存储普查脚本没跟着改（脚本不在本仓，
  按 `src_rows_1d` 特征未定位到宿主）。**谁维护这个普查脚本就改谁**：读法切到
  `_request_logs` 物理族。

### 3.5 外部客户端探错库（维持上轮登记）

`relation "llm_optimization_reports/records"` 等仍按 ~10 条/天探测。owner 排查
哪个 agent 配置指向 llm_gateway DSN（挂账第 2 轮）。

## 四、慢 SQL 审计（窗口 ≥1s 共 16,755 条 / 67,729s）

### 4.1 主复核：872 真实收益——结构性根修成立，表面 pss 均值具有欺骗性

- **三个时点的同语句 EXPLAIN**：872 前 3.17s → 872 预应用当日（load 47 噪声下）
  1.08s → **本轮安静时点 0.19-0.20s**（两跑一致，TIMING OFF+BUFFERS：
  428,941 行 Append 全扫 + HashAggregate，13.5k buffers，无 stripe 病态命中）。
  **16× 结构性改善成立。**
- **但日志面 ≥1s 慢查没消失**：`WITH latest_bucket` 家族 585 次 / 3,524s
  （上轮 488 次 / 2,050s），**全天每小时 13-41 次**，凌晨安静时段均值也有 2-3s。
- **判定：残余是并发受害，不是内在成本**。证据链：
  (a) 内在 0.2s vs 日志均值 6s 差 30 倍，且慢发时刻与 REFRESH/promote/批处理
  撞点吻合；(b) `pg_stat_statements` 均值 2,310→2,117ms 的「表面微降」不可比——
  **pss 在 10-18 22:03:37 被外部 RESET 过**（本轮实测 `stats_reset`），且窗口
  含早晨队列污染（20 次 5-25s 阻塞样本即可把均值拉到 2s 量级）；
  (c) 语句文本与上轮相同（走 `credential_model_index_with_current_month` 视图，
  本就是 main 的长期形态，非新变更）。
- **下轮口径**：不再用 pss 均值做 872 验收（污染源太多），改用
  「安静时点 EXPLAIN 三点对比」+ `columnar_healthcheck()`（本轮仍三行 compliant）。

### 4.2 870 复核：`WITH deduped`（usage_ledger 月度对账）修复确认

上轮 8 次 / 1,136s（mean 142s）→ 本轮 **2 次 / 82s**（mean 41s）。870 覆盖索引
生效，销案（「外层 Sort external merge 34MB temp」次级问题随量级消失，一并销）。

### 4.3 家族表要点（总量 Top，全部对过已知账）

| 族 | 次数 / 总秒 | 定性 |
|---|---|---|
| `WITH matched`（候选查询） | 1,792 / 6,021s，max 27s | 事件受害放大（上轮 1,105/2,945s）；夜间基线健康（r0926 判定维持） |
| REFRESH `routing_analytics_7d` | 51 / 4,489s，mean 88s max 149s | 已知族，7d 窗裁决挂 11-02 |
| `WITH latest_bucket`（CMI 刷新） | 585 / 3,524s | 见 4.1 |
| `WITH targets`（session_summaries 48h 回填） | 184 / 2,675s，mean 14.5s | 已知族，与上轮持平微增（167/2,504s），继续观察 |
| candidate_failure 双面读（`-- both surfaces must read the current-month view`） | 688 / 2,550s | 事件受害 |
| `INSERT INTO provider_metrics_minute` | 653 / 2,465s | 事件受害（锁队列） |
| REFRESH `session_client_stats` / `session_task_stats` | 38+40 / 1,624+974s | 已知 matview 族，持平 |
| `WITH neg`（负值成本扫描 30d） | 154 / 1,529s，mean 9.9s | **较上轮 +30%**（117/1,148s），抬头未达修线，下轮若再增立项 |
| `WITH win` | 12 / 1,357s，mean 113s | 低频，维持登记 |
| `INSERT INTO public.assets`（apihub 端点注册） | 273 / 946s | **新定位**：apihub/pg_store.go 的 llm_endpoint 资产 upsert；336/376 集中在事件窗口=受害；凌晨仍有 1.3-2.8s 单插=轻度尾巴。登记观察 |
| 近期请求读（`EXTRACT(EPOCH FROM ts)`，session_turns_hot⋈details_hot） | 675 / 1,109s | **新定位**：管理面 recent-requests 轮询，事件受害；登记 |
| provider_error_aggregator 5min 窗读（date_bin supplier_errors_hot） | 192 / 322s | 已知族，事件受害 |
| promote 族（turns/bodies/usage_ledger/wal/memora） | 合计 ~263 次 | 事件窗口放大，max 53s；基线持平 |
| `ANALYZE session_turns` 670s / `_2026_09` 130s | 各 1 | §2.2 人工战役成分 |
| 872 DO 块 224s（07:01:58） | 1 | 上轮预应用实录本体；12:59 台账行的 runner 重放为幂等 no-op（无第二次 224s），自洽 |
| 尺寸普查（`pg_total_relation_size` 族） | 95 / 364s | bloat 巡检脚本（§97-104 线程），正常 |
| smm `daily_kline` SELECT/GROUP | 105 / 620s | 仓外，事件受害 |

### 4.4 台账新事实

`schema_migrations` 本窗口新增 **871（07:03）/ 872（12:59，runner 幂等重放）/
873（12:59）/ 874（13:06）/ 875（18:42）/ 876（23:13）**。873-876 为并行会话
交付（usage_ledger 覆盖索引 / session_turns client_request_id 索引 / ursm 空响应率
5m / drop prefix 覆盖父索引），通道门实测 rc=0（六点同步齐）。
**注意**：252 库已含 873-876 而 154/245 二进制未部署对应代码——维持「树有 WIP
不代部署」纪律，由 owner 安排部署窗口。

## 五、改动清单

| # | 位置 | 性质 |
|---|---|---|
| 1 | 252: smm-collector/smm-mcp/smm-web/smm-watchdog 四容器 `/app/src/data/pg_store.py` | ALTER 守卫补齐（宿主守卫版 docker cp，容器内留 `.bak_20261019`；md5 `7a7c0b25…` 四容器一致） |
| 2 | 252 llm_gateway 库 | 无改动（872 验收为只读复核；pss 被外部重置未复原） |
| 3 | `docs/audit/2026-10-19-252-sql-log-audit-round.md` | 本文档 |

本仓无代码改动——错误侧两大新签名（AND AND、data-lifecycle `//`）已被并行会话
在 origin/main 修复；慢查侧无新缺陷签名，残余为已知族与事件受害。

## 六、门与验证记录

- 解析器：三处缺陷修复有实证（慢查 0→16,755；孤儿 6,781→0；归一化回溯爆炸消除）。
- smm 守卫：四容器 md5 与宿主守卫版一致；`smm-collector` 解释器实跑 `PgStore()` ×2
  通过；活日志（03:22 起）ALTER 0 条（白天 cron 周期复核留下一轮）。
- `bash scripts/apply-db-revision-sequence_test.sh`：快进后 rc=0（873-876 通道齐）。
- 872 验收：安静时点 EXPLAIN ×2 = 194/204ms；`columnar_healthcheck()` 三行
  compliant（本轮复核）。
- 变异口径：本轮红→绿由「smm 守卫舰队补齐」承担（补前 6 条 ALTER 实测、补后
  实跑通过 + 活日志零排放）；AND AND 归因更正由 origin/main 修复提交本身承担。

## 七、遗留与下一轮

1. **smm 守卫恒零终验**（下轮首查）：需覆盖一个完整白天 cron 周期的
   `ALTER TABLE daily_kline` 零排放。
2. **pocket $4 双型错**（289/天）：owner 部署 fa20e648——挂账第 N+2 次。
3. **873-876 对应二进制部署**：252 库已领先 154/245 二进制四个迁移，owner 安排
   部署窗口（含 f05058f7c 的 AND AND 修复上线）。
4. **维护窗口纪律**（本轮新登记）：迁移重放/非 CONCURRENTLY 建索引（744 重放
   163s）/ANALYZE 战役（670s）/外部杀查询，四者叠加制造了 06-12 时全面劣化；
   建议此类操作走低峰窗口并预先公告。
5. **CMI 队列头未逐条定案**（654 次 AccessShare 等待，06:01-06:09）：重放批
   DETACH 守卫为高置信假设；如 owner 能提供 06:00-06:35 的操作记录可闭合。
6. **普查脚本改读 `_request_logs`**：引用已删视图 `public.request_logs` 的存储
   普查脚本（`src_rows_1d` 特征）需更新，脚本宿主未定位，owner/并行会话认领。
7. **外部探错库工具**（llm_optimization_* + 中文 markdown 当 SQL）与 **smm 自身
   缺陷**：维持登记。
8. `WITH neg` +30% 抬头、`WITH targets` 微增：若下轮再增，立项专属分片。
9. **量具新坑入册**：podman 短 ID ≠ 目录长 ID（inspect 取路径）；本机 GNU/BSD
   文本工具对坏字节行需 `LC_ALL=C`+`-a`；pss 会被外部 RESET，验收判据勿依赖
   长窗口均值。
