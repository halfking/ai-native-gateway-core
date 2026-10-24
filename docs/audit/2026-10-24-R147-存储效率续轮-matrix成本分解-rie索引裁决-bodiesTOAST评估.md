# R147 · 存储效率续轮 —— ㉙ matrix REFRESH 成本分解 / ㉘ rie 三索引裁决（迁移 908）/ ③ bodies TOAST 拆分评估

> 承接 R146（§143.102/§143.103 + 审计档 §8）。**对生产的写入 = 0**：
> 252 全程只读（全部 SELECT/EXPLAIN，含两次 EXPLAIN ANALYZE——只读 SELECT，
> 会话级抬 statement_timeout，与一次生产 REFRESH 同量级）；唯一落地 = 迁移 908
> **只落仓**，生产应用需属主授权（902 同款）。
> 时间：2026-10-24 04:48–06:00（252 时区）。

## §1 一句话结论

- **㉙**：matrix REFRESH 的成本分解做完了——**R146 的「~2.2h/天」是最差单日读数，
  5.2 天 pgss 均值是 ~0.50h/天**（三兄弟全家 ~1.44h/天）；结构成本 = 每次执行
  ~32.3 万 buffer 触碰（91.9% 是 ss 侧 35.2 万次逐行 heap fetch）+ **131.6 MB
  temp 读写**（work_mem=8MB 的外排 + hash 4 分批）+ session_dim 206MB 全堆扫；
  属主三选一之外新发现一个**零语义杠杆（d）**：刷新器会话抬 work_mem 可消掉
  每次 131.6MB temp（计划 est −34%），热缓存下墙钟平手（如实）。
- **㉘**：三索引裁决闭环——**type_created（110MB）零消费方可删，已落仓为迁移 908**
  （8 点注册 + 门 9 测 + 变异 M1~M6 全红 + 本地一次性库五段生命周期）；
  incident_created（93MB）与 created_at（41MB）各有不可替代消费方，**保留**。
- **③**：bodies_2026_10 的 6.08GB TOAST = request_delta ~63% + outbound_body ~38%；
  **字段级拆分本身不省空间**（压缩已在 lz4 上限 1.81×、zstd 不可用、columnar 被
  ON CONFLICT 挡住——§143.65 已证），量化去重杠杆只有 ~135MB（2.2%）；
  拆分的真实价值只剩**留存灵活性**（语义，属主）。

## §2 ㉘ route_incident_events 三索引：消费方核对与裁决

### §2.1 252 读数（2026-10-24 只读，stats reset 2026-10-18 22:03 起 5.2 天）

| 索引 | 定义 | 体积 | idx_scan | 判定 |
|---|---|---|---|---|
| `idx_route_incident_events_type_created` | (event_type, created_at DESC) | **110 MB** | **1** | **删（908）** |
| `idx_route_incident_events_incident_created` | (incident_id, created_at DESC) | 93 MB | 20 | 保留 |
| `idx_route_incident_events_created_at` | (created_at) | 41 MB | 8 | 保留 |
| `uq_route_incident_events_idem`（对照） | (incident_id, request_id, terminal_status) partial UNIQUE | 250 MB | **422,297** | 写路径本体 |
| 表 | 1,897,740 行 / heap 665 MB | — | — | — |

### §2.2 消费方核对（代码面，逐个过）

- **type_created：零消费方**。全仓 Go 无任何以 `event_type` 为谓词查询
  `route_incident_events` 的语句（requestjourney 的 event_type 是
  `request_state_transitions`，另一张表）；pgss 里本表的非 INSERT 语句只有
  手工 count。它历史上的唯一使用者是 30 天留存 DELETE——**871 头注自己实测判了
  它死刑**：「条件建在非首列 ⇒ 扫完整个 93MB 索引再逐条过滤」的坏计划，并为
  此新建了 created_at 首列索引取代。**不背书任何约束**（pg_constraint 实查 0 行）。
  写路径热度佐证：同表 uq_idem 5.2 天 42.2 万次扫描（≈7.8 万次/天的
  ON CONFLICT 冲突检查）⇒ 每次写入都白维护一个零读者的 110MB btree。
- **incident_created：两个真实消费方**（都 incident_id 首列）：
  `domains/routeincident/store.go:651`（ListEvents：`WHERE e.incident_id=$1
  ORDER BY e.created_at`，admin 事件列表）与 `admin/route_incidents.go:778`
  （sampleRequestsFor：`WHERE incident_id=$1 AND request_id IS NOT NULL
  ORDER BY created_at DESC LIMIT 8`）。后者理论上可被 uq_idem（incident_id
  首列 + partial 谓词吻合）承接，前者不能（无 request_id 谓词，partial 不适用）
  ⇒ **删了 = admin 事件页每次全表扫 1.9M 行** ⇒ 保留。
- **created_at：retention worker 的硬前置**。`domains/routeincident/retention.go`
  的 `hasTimeIndex` 探针只认「首列 = created_at」的索引，没有它
  `CleanupOnce` 每轮 `SkippedNoIndex` 跳过 ⇒ 这个 190 万行、1.2GB 的表
  **唯一的行级留存机制永久失效** ⇒ 保留（41MB 换留存可用性）。

### §2.3 迁移 908 落仓清单

- 正本 `sql/migrations/startup/908_route_incident_events_type_created_drop.sql`
  （`dbinit:no-transaction` + `DROP INDEX CONCURRENTLY IF EXISTS` + 会话级
  lock_timeout=60s / statement_timeout=10min + RESET）+ down（逐字同形重建）。
- **配套**：`db/db.go ensureRouteIncidentSchema` 同批删除同名 `CREATE INDEX`
  ——否则 ensure 链每次启动把刚删的索引复活（718 三项核实的「无 Go ensure
  创建者」在本表不成立，必须先拆）。baseline（两份快照）**刻意保留**：
  718 先例，fresh-install 链次序 =「baseline 建出 → 908 删掉」。
- 8 点注册：正本 / embeddata 副本×2（sha 逐字一致）/ main.go embed+map /
  runner.go StartupFiles / TSV 镜像（行 305）/ apply-db-revision-sequence.sh /
  db-changelog.md（存在性口径，LESSONS §302）/ startup_concurrency_gate_test.go
  豁免表（计数 1）。
- 门 `migration_908_route_incident_type_created_drop_test.go`（9 测）：up 形态
  + **3 份坏样本阳性对照**（去 CONCURRENTLY / 去 IF EXISTS / 抽 RESET）+ down
  同形 + 副本逐字节 + sha 钉 + 8 点登记 + **ensure 不复活腿** +
  **baseline 保留腿**。
- 变异 `scripts/audit/mutate_147_908_gate.py`：M1~M6 全红并按原路径 md5
  逐字还原（M6 首两轮被脚本自检拦下——drop 型替换串含 needle ⇒ 拆
  drop/insert 两型后通过；坏样本③首版「注释化 RESET」被门漏过——检测器收到的
  已是剥注释文本，改为整体删除后判红，两处自踩均已按纪律留痕）。
- 本地一次性库（`itgate_147`，退出即 DROP）五段生命周期：建 → up（删）→
  幂等重跑 → down（同形重建）→ 再 up（再删），全绿。

## §3 ㉙ session_client_task_matrix REFRESH 成本分解（R75 §6 三选一的拍板材料）

### §3.1 先订正 R146 的量级口径

pgss 5.2 天窗口（10-18 22:03 reset）：

| 视图 | calls | mean | max | 总 DB 时间 | 折每天 |
|---|---|---|---|---|---|
| session_client_task_matrix | 155（≈29.8/天） | 60.9s | 592.6s | 2.62h/5.2d | **≈0.50h** |
| session_client_stats | 150 | 59.5s | 176.0s | 2.48h | ≈0.48h |
| session_task_stats | 158 | 56.1s | 260.9s | 2.46h | ≈0.47h |
| （对照）routing_analytics_7d | 293 | 61.0s | 179.5s | 4.97h | ≈0.96h |

⇒ **R146 的「26 次/天、中位 ~393s ⇒ ~2.2-2.3h/天」是 10-23 单日（当日 VACUUM
823s 的高负载日）读数**；均值口径下 matrix ≈ 0.50h/天，**routing_analytics_7d
才是 REFRESH 族最大头**。紧迫性排序因此变化：㉙ 从「第一」降为「与 routing_7d
同族之一」。max 592.6s 与 10-23 日志的 592.7s 顶格吻合（同一条）。

### §3.2 结构成本分解（EXPLAIN ANALYZE BUFFERS TIMING OFF，现行形态）

每次执行（JOIN 输出 352,038 行 → 输出 **1,348 组**；输入 = 窗口内 ss 行
352,016 / 全表 857,362 = 41.1%）：

| 组件 | 读数 | 说明 |
|---|---|---|
| ss 侧 Parallel Index Scan（tenant_time 全索引扫+过滤） | **hit 274K + read 22.7K ≈ 296,890 touches（91.9%）** | 本质是 **35.2 万次逐行 heap fetch**（窗口行散布全堆）；不是索引选择问题 |
| sd 侧 Parallel Seq Scan（session_dim 全堆） | **26,422 touches（206MB），其中 read=24,581** | **每次都从盘读**（898,919 行的堆冷得很彻底） |
| Sort（GROUP BY） | external merge，3 worker 各 9.4~12.2MB disk | work_mem=8MB ⇒ 必外排 |
| Parallel Hash | **Batches: 4**（hash 侧也溢出） | 同上 |
| temp 合计 | **16,850 blocks ≈ 131.6MB 写 + 131.3MB 读 / 次** | 外排 + hash 分批 |
| 墙钟 | 冷/温 30.3s → 热态 2.16s（同机同缓存差 14×） | 与 §143.28「单点墙钟无判别力」一致 |

### §3.3 三选一 + 新发现的对照（全部实测/EXPLAIN，§300 先 A/B 两计划）

| 方案 | est cost（现行 161,563 基准） | 结构效果 | 语义 |
|---|---|---|---|
| 现行（30d, work_mem 8MB） | 161,563 | — | — |
| **(d) 新：抬 work_mem（30d 不变）** | **106,741（−34%）** | HashAggregate 替换 Sort+GroupAggregate；**temp 131.6MB→0**（Batches:1）；热态墙钟 2.70s vs 2.16s **平手**（temp 落 OS 页缓存时不显性，I/O 争用日才兑现） | **零**（会话级 SET，R75 分族 statement_timeout 同款接线点） |
| (a) 收窄窗口 30d→7d | 81,275（−50%） | 窗口行 352,016→103,978（−70.4%）；**join 换 session_dim_pkey Nested Loop，sd 206MB 全堆扫从计划里消失** | 报表窗口语义变更（属主） |
| (a)+(d) 组合 | 77,637（−52%） | 两者叠加 | 同 (a) |
| (b) 去 array_agg(DISTINCT) | **对 matrix 不适用——它没有 array_agg** | 适用于两个兄弟（client_stats/task_stats）：DISTINCT 强制 GroupAggregate+Sort（实测 Sort Key tenant,task,primary_model），抬 work_mem 也救不了它们 | models_used/clients_used 列语义变更（属主） |
| (c) 物化中间表（按天预聚合） | 未建表未测 | 结构上消灭「每小时全量重读 35.2 万行 + 全堆 sd」，是唯一治本的形态 | 结构变更（属主），收益最大、工作量最大 |

### §3.4 给属主的建议排序（本轮只给材料，不替拍板）

1. **(d) work_mem 杠杆先行**：零语义、纯代码（`bg/materialized_view_refresher.go`
   的分族接线点现成），消掉 matrix 每次 131.6MB + 兄弟们的外排 temp；
   不承诺墙钟收益（§143.28 纪律），只承诺 temp I/O 归零与计划 est −34%。
2. (a) 与 (c) 二选一取决于属主对「报表窗口 30 天」的语义取舍——(a) 一行
   INTERVAL 字面量（855 头注已给配方），(c) 是新表 + 双写窗口。
3. (b) 单独立项不值得（只救两个兄弟的 Sort，且改列语义）。
4. routing_analytics_7d（0.96h/天）的降本未被本轮覆盖，**量级比 matrix 大**，
   建议下一轮单独分解。

## §4 ③ session_bodies_2026_10 TOAST 拆分评估（252 只读）

### §4.1 ★ 自我纠错（先记，防再犯）

本轮一度从 `pg_attribute.attstorage='x'` 推出「五列 EXTERNAL ⇒ lz4 从未生效
⇒ 6GB 未压缩」——**错在字母映射读反**：PG 里 `'x' = EXTENDED`（压缩+行外，
默认），`'e'` 才是 EXTERNAL（行外不压缩）。按天分桶实测
`pg_column_size//octet_length(::text) ≈ 0.31~0.67`（10-01~10-23 每一天都在压）
+ §143.65 的整表 1.81× ⇒ 压缩一直在正常工作。教训入 LESSONS（§397 · 原取号 §355，
  2026-10-25 与上游 R91 轮 §355 双胞胎撞号 ⇒ 零引用侧让号，见 LESSONS §397 抬头留痕）。

### §4.2 字段级体积分布（TABLESAMPLE SYSTEM 1%，n=3,719，外推 ÷0.0119）

2026_10 分区：heap 127MB / **TOAST 6,081MB** / 索引 171MB，~31.2 万行；
hot 表另有 TOAST 1,006MB（8h 保留，稳态）。

| 列 | 非空率 | 均值（已压缩存储字节） | 外推总量 | 占比 |
|---|---|---|---|---|
| request_delta | 96.3% | 12,900 B | ≈3.88 GB | **63%** |
| outbound_body | 100% | 7,644 B | ≈2.39 GB | **38%** |
| response_delta | 96.3% | 251 B | ≈75 MB | 1.2% |
| 两 attachments | ~100%（多为 `[]`） | 11/5 B | <1 MB | ~0 |

外推合计 ≈5.9GiB，与 TOAST 实测 6,081MB 吻合（闭环）。增长节奏 ≈253MB/天
（10 月均值口径）⇒ ~7.8GB/月。

### §4.3 拆分评估结论（属主拍板材料）

- **拆分本身不省空间**：压缩已在 lz4 上限（整表 1.81×；zstd 本构建不可用；
  columnar 被 promote 的 ON CONFLICT 挡住——三者均 §143.65 已证）。把
  request_delta 挪去单独的表族，同一列还是同一个压缩比。
- **量化去重杠杆很小**：25.1% 的行 request_delta ≡ outbound_body（逐字相同，
  §143.48 双写同源的延续），但这些相同行多是小行，只占 request_delta 字节的
  3.6% ⇒ 全部去重 ≈ **135MB（2.2%）**——不值得为它改写入语义。
- **拆分的真实价值只剩留存灵活性**：例如「request_delta 保 30 天、
  outbound_body 保 90 天」这类**按列差分留存**——这是纯语义决策，属主拍板；
  若不做差分留存，§143.65 已给的 841 `retain_months`（当时量到 4,957MB）
  仍是这一族最大的单杠杆。

## §5 诚实边界

1. 两次 EXPLAIN ANALYZE 是真执行（只读 SELECT）：第一次 47,248 物理读、
   30.3s；第二次全 hit、2.7s——**冷热差 14×，墙钟只作时点记录**，结构读数
   （buffers/temp）不受影响（§143.28/§143.29 口径）。
2. work_mem 杠杆的热态墙钟平手（2.16s vs 2.70s，8MB 臂反而略快，在噪声带内）
   ——它的可辩护收益只有 temp I/O 归零与 est cost，**不承诺秒数**。
3. (c) 方案的收益未实测（没有建中间表）；est 表格里只有 (a)/(d) 是实测计划。
4. ㉙ 的 pgss 窗口（5.2 天）覆盖 10-19~10-24，其中 10-23 是高负载日 ⇒
   「折每天」是混合口径；单日波动 2.6×+（§143.28 已证）。
5. ③ 的外推基于 1% 系统采样（页级，行内可能聚簇）；列占比与 TOAST 总量的
   闭环吻合（5.9 vs 6.08 GiB，差 ~3%）支撑其可信度。
6. 908 只落仓未应用；252 上 idx_route_incident_events_type_created 仍在位。
