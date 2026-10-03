# llm-gateway 数据库审计报告：252 与 34 双实例对比、数据分布与优化建议

- 审计日期：2026-10-02（34 侧） / 2026-10-03 补 252 侧实时数据
- 审计对象：`192.168.31.34` Docker 容器 `llm-gateway-pg`（镜像 `kx-citus-pg17:offline-arm64`）
- 采集方式：Docker Remote API（`192.168.31.34:2375`）→ `docker exec` → `psql` / `pg_dump`
- 统计窗口：`stats_reset = 2026-09-29 00:47`，采集时已运行 `2 天 03 小时`
- 证据文件：`.db-audit/out/*.txt`（原始采集）、`.db-audit/out/34_schema.sql`（live schema，SHA-256 `d6f68db0…78b7`，与容器内一致）

---

## 0. 双实例实测对比：252 vs 34

**252 接入已打通，实时数据已取得**（2026-10-03 17:28 采集）。本文档其余章节仍是 34 侧的深度分析，本节负责两者的横向对比，以及由此产生的、**与只分析 34 时会得出的不同结论**。

### 0.1 采集方式（踩坑记录）

252 是 4 核 / load 20–26 的生产机，宿主机跑着 30+ 个容器。实测三种执行模式的差距：

| 模式 | 实测 | 结论 |
|---|---|---|
| 同步 + 长连接（38 节采集） | >20 分钟 | 超时 |
| 同步 + `ssh sh -s`（stdin 传脚本） | 5 行脚本 141 秒 | 不可用 |
| 同步 + 命令字符串参数 | 28–38 秒 | 能连上，但整体仍会超时 |
| **`scp` 上传 + `setsid` 后台作业 + 短命令取回** | 提交 ~40 秒，取回 ~30 秒 | ✅ 本次采用 |

执行通道：`.db-audit/d252async.ps1`（`-Action submit|status|fetch|cleanup`）。全程只读（`SET default_transaction_read_only = on`）+ 写语句闸门 + 语句超时。远端临时目录 `/tmp/d252async-*` 采集结束后已清理。

> **两个必须记住的坑**（本次实际踩到）：
> 1. **不要把采集脚本包进 `BEGIN … COMMIT`。** 一条语句报错会让事务进入 aborted 状态，其后所有语句被一并拒绝 —— 实测一次 `jit_time` 列名错误，导致 9 个 section 全空。现已改为 `SET default_transaction_read_only = on`，每条语句独立。
> 2. **`pg_stat_statements` 没有 `stats_reset` 和 `jit_time` 列**（`stats_reset` 在 `pg_stat_statements_info`，JIT 是 `jit_functions` / `jit_emission_time`）。写错一次就废掉整段。

### 0.2 环境画像

| 项 | 252 | 34 |
|---|---|---|
| 版本 | PostgreSQL 17.10，`x86_64-pc-linux-gnu` | PostgreSQL 17.10，`aarch64` |
| CPU | **4 核，load average 20.35 / 26.54（超载 5–6 倍）** | 8 核 |
| 内存 | 14 GB（已用 7 GB） | 32 GB |
| 磁盘 | 197 GB，用 88 GB（**47%**） | 1.9 TB，**94%** |
| PG 容器 CPU | 68.91%（≈**0.69 核**） | — |
| 扩展 | citus 13.3 / citus_columnar 13.3 / **vector 0.8.6** | 同上，但 **vector 0.8.5** |

> **252 慢的根因不在 PostgreSQL。** PG 容器只吃掉 0.69 核，load 20+ 来自宿主机另外 30+ 个容器。**任何 PostgreSQL 调参都无法改善这种宿主机超载** —— 这是本节最重要的判断。

### 0.3 关键反转：252 的 PostgreSQL 配置远好于 34

只看 34 会得出「A 档配置调优是头号大事」。**这个结论在 252 上基本不成立 —— 那边早就调好了。**

| 参数 | 34 | 252 | |
|---|---|---|---|
| `shared_buffers` | **128 MiB** | **3.5 GB**（`SHOW` = 3584MB） | **28×** |
| `maintenance_work_mem` | 64 MB | **512 MB** | 8× |
| `work_mem` | 4 MB | 8 MB | 2× |
| `effective_cache_size` | 5 GB | 10.5 GB（`SHOW` = 10752MB） | |
| `log_min_duration_statement` | **-1（关闭）** | **1000 ms（开启）** | |
| `random_page_cost` | 4（HDD 默认） | **1.1（SSD）** | |
| `statement_timeout` | 未设 | 60 s | |
| `checkpoint_timeout` / `max_wal_size` | 300 s / 1 GB | 900 s / 4 GB | |
| `compute_query_id` | 未设 | auto | |

> **⚠️ 读 `pg_settings` 带 `unit` 参数的坑 —— 本报告栽了两次，两台机器都中招。**
> 采集脚本用 `name || ' = ' || setting || coalesce(unit,'')` 输出，于是 `shared_buffers` 那行显示成 `163848kB`（34）、`4587528kB`（252）。
> 这是 **`setting` 与 `unit` 两个字段直接拼接**的字符串，不是「163,848 kB」或「4,587,528 kB」。真实值要自己乘：
>
> | 实例 | setting | unit | 换算 | `SHOW` 复核 | 曾被误读为 |
> |---|---|---|---|---|---|
> | 34 | 16384 | 8kB | 128 MiB | `128MB` | 160 MB ❌ |
> | 252 | 458752 | 8kB | **3584 MiB** | `3584MB` | 4.48 GB ❌ |
> | 252 `effective_cache_size` | 1376256 | 8kB | **10752 MiB** | `10752MB` | 13.1 GB ❌ |
>
> 三处独立佐证 34 的值：`postgresql.conf:129` 原文 `shared_buffers = 128MB`、`SHOW shared_buffers` = `128MB`、字节数 `134217728`（= 128 × 1048576，精确吻合）。
> **规则：凡是带 `unit` 的参数，一律用 `current_setting(name)::bigint` 取字节数，或与 `SHOW` 交叉验证；不要靠拼接字符串目测。** 本文所有容量数字已按此规则复核。

配置差异直接体现在运行指标上：

| 指标 | 34 | 252 |
|---|---|---|
| **缓存命中率** | 87.3% | **96.9%** |
| **临时文件落盘** | 91.5 GB/天 | **4.7 GB/天**（好 19.5×） |
| **checkpoint 请求占比** | 999/947 = **105%（已追不上）** | 156/5601 = **2.8%（健康）** |
| 死锁 | 12.2 次/天 | 10.9 次/天 |

**两个例外，恰好是真正该动手的地方：**

- **死锁率两端几乎一样**（12.2 vs 10.9 次/天）。252 配置好得多却没减少死锁 —— 说明**这是应用层的事务顺序问题，不是配置问题**，两台机器共享同一份缺陷。累计 252 已死锁 114 次。
- **252 仍有 61 个 idle 连接**，而 `max_connections=1000`、4 核内存。空闲 backend 本身就吃内存，且高并发上限会放大内存争抢。

### 0.4 数据分布对比

| 维度 | 34 | 252 |
|---|---|---|
| 库大小 | 56 GB | **22 GB** |
| public 关系 | 564 | 543 |
| 基表 / 索引 / 分区 | 413 / 2317 / 1045 | 397 / **2032** / 801 |
| heap / index / toast | 27 GB / 16 GB / 10 GB | 15 GB / 5.7 GB / **1.17 GB** |
| **零扫描非唯一索引** | 117 个 / **5.17 GB** | 109 个 / **1.39 GB** |
| 从未 ANALYZE 的表 | 625 | 440 |
| lz4 压缩列 | 21 | 42 |
| columnar 表 | 0 | 0 |

> **B 档删索引清单不能直接搬到 252。** 34 的零扫描索引 5.17 GB，252 只有 1.39 GB —— 差 3.7 倍。这正是「开发机扫描模式 ≠ 生产」的实际体现，34 的清单必须逐条在 252 复核后再动。

252 最大单表是 `ursm_node_snapshot_min`：**10 GB / 18,719,448 行**，占全库近一半（34 上是 7.6 GB / 12,126,876 行，持续增长中）。

### 0.5 慢 SQL 对比：问题结构完全不同

252 累计执行 **621.8 小时**（4,799 条语句，12.17 亿次调用）。Top 3 占 24.4%、Top 5 占 32.7% —— 比 34 分散得多（34 是 2 条语句占 53.5%）。

| # | 占比 | 累计耗时 | 语句 |
|---|---|---|---|
| 1 | **13.5%** | 301,854 s | `pg_advisory_xact_lock(session_turns_advisory_lock_key(…))` |
| 2 | 5.4% | 121,870 s | `WITH latest_bucket AS (… credential_model_index_with …)` |
| 3 | 5.4% | 121,652 s | `REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d` |
| 4 | 4.3% | 95,444 s | `SELECT COALESCE(MAX(turn_no),…) FROM session_turns_with_current_month` |
| 5 | 4.0% | 90,338 s | `INSERT INTO credential_model_index_hot` |
| 6 | 3.7% | 82,811 s | `SELECT analyze_llm_gateway_table_stats($1)` |
| 7 | 3.2% | 70,999 s | `DELETE FROM session_aggregate_outbox` |
| 8 | 3.1% | 68,775 s | `DELETE FROM credential_model_index_hot` |
| 9 | 2.3% | 51,450 s | `SELECT MAX(date) FROM daily_kline`（6,125 次，均值 **8.4 秒**） |
| 10 | 1.6% | 35,150 s | `INSERT INTO request_logs_bodies_hot` |

**按调用次数看，暴露的是另一类问题：**

| 调用次数 | 语句 |
|---|---|
| **2.95 亿** | `SELECT value::text FROM settings_kv WHERE key = $1` |
| 1.45 亿 / 1.45 亿 | `begin` / `commit` |
| 1.26 亿 | `SELECT set_config(…)` |
| **1.006 亿** | `INSERT INTO public.assets` |
| 4,680 万 | `INSERT INTO ursm_node_snapshot_min` |

> **`assets` 表在 252 上被 upsert 了 1.006 亿次，库里只有 2,140 行。** 这和 34 上的 1,166 万次是**同一个应用层缺陷**（`apihub/pg_store.go:73`），在生产上是 34 的 8.6 倍。`pg_stat_user_tables` 显示 `upd=57,128,748`、`hot_upd=46,259,551`、`live~2140` —— 5,700 万次更新打在一张两千行的表上，全部命中索引但不产生任何有效数据。**这是 252 上单笔最大的、可通过改代码直接消除的浪费。**

**两个值得单独点名的慢查询：**

- `SELECT analyze_llm_gateway_table_stats($1)`：777 次调用，均值 **106.6 秒**，累计 23 小时。这是个维护函数，慢到不正常。
- `SELECT MAX(date) FROM daily_kline`：6,125 次调用，均值 **8.4 秒**。一条取最大日期的查询不该花 8 秒，说明 `daily_kline` 缺索引或统计信息有问题。

**顺带发现（需确认）**：`PGSS_TOP_MEAN` 里出现了 `DROP DATABASE IF EXISTS bloat_negctl`（2 次调用）。有人曾在**生产库**上跑膨胀实验。虽已清理，但这类操作应挪到测试环境。

### 0.6 全表扫描热点

252 上同样是那几张小表被反复全表扫：

| 表 | seq_scan | idx_scan | 行数 | 大小 |
|---|---|---|---|---|
| `models_canonical` | **6,242 万** | 46 万 | 961 | 1000 kB |
| `provider_models` | 2,356 万 | 1.31 亿 | 1,329 | 1560 kB |
| `credential_model_bindings` | 102 万 | 3.67 亿 | 2,509 | 2400 kB |
| `model_aliases` | 16 万 | 5.54 亿 | 2,704 | 1832 kB |

`models_canonical` 只有 961 行却被扫了 6,242 万次 —— 表小不是理由，**缺索引才是**。这四张表恰好是 PG18 多列 B-tree skip scan 的典型目标（见 D5）。

### 0.7 膨胀与维护

- `session_dim`：死元组 **8.1%**，`last_autovacuum = **never**` —— 从未被 autovacuum 处理过。
- `ursm_node_snapshot_min`：`ins=24,439,725`、`del=28,605,451`，**删除数比插入数还多**，18,420,769 行存活。典型的「滚动快照 + 逐行删除」，应改为按时间分区 DROP。
- `routing_analytics_7d`：`ins=45,619,170` / `del=45,649,042` / 存活仅 2,468 行 —— 每次 `REFRESH MATERIALIZED VIEW CONCURRENTLY` 全量重写 4,500 万行。
- TOAST 压缩：5,772 列未设压缩（走 pglz），仅 42 列用 lz4。

### 0.8 由对比得出的三条行动结论

1. **34 与 252 是两台性质完全不同的机器，不要用同一套方案。**
   34 的头号问题是配置（`shared_buffers` 128 MiB、慢查询日志关闭、`random_page_cost=4`）—— A 档调参即可见效。
   252 的配置已经到位，**头号问题是宿主机超载 + 应用层写入放大** —— 调参无效，要改代码和加机器。

2. **两端共同的真问题是应用层，不是数据库。** `assets` 的 1 亿次 upsert、`begin`/`commit` 各 1.45 亿次、死锁率两端一致 —— 这三项在两台机器上都在浪费资源，且都能通过改代码解决。

3. **删索引务必在 252 单独复核。** 34 的 5.17 GB 零扫描索引在 252 上只有 1.39 GB。

### 0.9 尚未完成的部分

- **结构比对仍以 34 live vs 仓库基线为准。** 252 的实时列级清单（`pg_attribute` 权威口径）本轮未取到 —— 该查询在当前负载下耗时过长。仓库基线 `sql/schema/01-schema.sql` 仍可作 252 结构的代理参考（据 `CHANGELOG.md:2045` 由 252 `pg_dump --schema-only` 重建），但不等于实时状态。
- 252 的分区清单、归档函数、全 NULL 列等细项未采集（`sql/audit/2026-10-02-db-audit-collect.sql` 的 38 节全集在低峰期可跑完，命令见「待你确认」）。

---

---

## 1. 实例基线

| 项 | 值 |
|---|---|
| 版本 | PostgreSQL 17.10 (Debian 17.10-1.pgdg13+1), aarch64 |
| 扩展 | citus 13.3-1, citus_columnar 13.3-1, pg_stat_statements 1.11, vector 0.8.5, pg_trgm 1.6, pgcrypto 1.3, pgstattuple 1.5, btree_gist 1.7 |
| 容器资源 | 8 vCPU / 32 GB RAM / 数据盘 1.9T（**已用 94%**） |
| `llm_gateway` 库 | **56 GB** |
| 存储构成 | heap 27 GB · index 16 GB · toast 10 GB |
| 关系数 | public 共 **564** 个关系 = 445 张非分区基表（含 26 张 `bak_*`）+ 32 个分区父表 + 119 个分区叶子；索引 2,493（= 2,317 普通 + 176 分区父表上的分区索引）；序列 235；函数 578；触发器 86；**RLS 策略 211 条 / 157 张表** |
| Citus 分布式 | `pg_dist_partition = 0`（扩展在，**未启用分布式**） |
| Columnar | `relkind='c'` **0 张**（扩展在，未实际使用） |
| 连接 | 后端 64 个（33 idle + 2 active，`llm_gateway`）；48 个在 `Client/ClientRead` |

**关键参数问题**（均为 `[default]` 或容器默认）：

| 参数 | 当前值 | 应有值 | 影响 |
|---|---|---|---|
| `shared_buffers` | **128 MiB** | 8–10 GB | 32G 内存机器上严重偏小，是缓存命中率只有 87.3% 的根因 |
| `work_mem` | 4 MB | 32–64 MB | 直接导致 **4 天落盘 366 GB 临时文件** |
| `maintenance_work_mem` | 64 MB | 1–2 GB | VACUUM/建索引反复重算 |
| `random_page_cost` | 4 | 1.1（SSD/NVMe） | 规划器高估随机读，规划偏差 |
| `effective_io_concurrency` | 1 | 200 | 顺序/位图扫描无法预读合并 |
| `default_toast_compression` | **pglz** | **lz4** | 10 GB TOAST 全部用最弱压缩 |
| `track_io_timing` | off | on | 无法定位 I/O 瓶颈 |
| `log_min_duration_statement` | -1（关闭） | 200–500 ms | 慢 SQL 无日志留痕 |
| `log_autovacuum_min_duration` | 600000 ms | 1000 ms | autovacuum 等于黑盒 |
| `max_wal_size` | 1 GB | 8–16 GB | 写密集负载下 checkpoint 过频（4 天 947 timed + 988 requested） |
| `wal_compression` | off | on | 13 GB WAL 可压掉相当比例 |
| `jit` | on | 视情况 off | 39 条语句触发 5,070 次编译，短查询上是净损耗 |

---

## 2. 数据分布

### 2.1 容量 Top 20

| 表 | 总大小 | heap | toast | index | 行数 | 备注 |
|---|---|---|---|---|---|---|
| `session_bodies_2026_09` | 8,598 MB | 497 MB | **6,883 MB** | 1,217 MB | 1,765,177 | 体积 80% 在 TOAST |
| `ursm_node_snapshot_min` | 7,596 MB | 6,017 MB | 0 | 1,577 MB | **12,126,876** | 单表最大行数 |
| `session_turns_2026_09` | 6,383 MB | 2,164 MB | 2,634 MB | 1,584 MB | 1,680,768 | |
| `request_logs_2026_09` | 5,007 MB | 2,474 MB | 689 MB | 1,843 MB | 2,157,864 | 157 列 |
| `bak_20260920_ursm_node_snapshot_min` | 3,900 MB | 3,899 MB | 0 | 0 | 9,353,005 | **备份表，无索引** |
| `request_state_transitions` | 3,087 MB | 602 MB | 0 | **2,485 MB** | 1,026,279 | 索引是堆的 4.1 倍 |
| `request_logs_bodies_2026_09` | 3,026 MB | 2,738 MB | 0 | 288 MB | 25,922,234 | |
| `stats_event_inbox_default` | 1,999 MB | 1,404 MB | 0 | 595 MB | 240,296 | 8.5 KB/行 |
| `session_aggregate_outbox` | 1,577 MB | 1,163 MB | 0 | 413 MB | 379,197 | |
| `route_incident_events` | 1,290 MB | 652 MB | 0 | 638 MB | 1,747,326* | 索引 idx_tup_read=0 |
| `request_stage_events` | 1,148 MB | 870 MB | 0 | 278 MB | 1,300,670 | |
| `usage_ledger_2026_09` | 1,014 MB | 367 MB | 0 | 647 MB | 2,067,361 | 索引占 63.8% |
| `session_turn_details_2026_09` | 900 MB | 483 MB | 3 MB | 414 MB | 1,677,271 | 61 列 |
| `analysis_events` | 886 MB | 550 MB | 0 | 336 MB | 43,474 | 20 KB/行 |
| `node_probe_runs` | 816 MB | 626 MB | 0 | 190 MB | 589,013 | **从未 autovacuum/analyze** |
| `usage_facts_default` | 534 MB | **0** | 0 | **534 MB** | **0** | 空分区带 534 MB 索引 |
| `request_context_attrs` | 514 MB | 350 MB | 0 | 164 MB | 216,698 | |
| `session_dim` | 507 MB | 307 MB | 0 | 199 MB | 1,148,456 | |
| `sessions_2026_09` | 486 MB | 201 MB | 0 | 286 MB | 760,808 | |
| `session_summaries` | 382 MB | 143 MB | 1 MB | 238 MB | 331,027 | |

\* `route_incident_events` 的 reltuples 1,747,326 与 `n_live_tup` 29,301 严重不一致，说明统计信息陈旧。

**业务量**：30 天内 `request_logs` 2,157,864 行（约 7.2 万/天）、`session_turns` 1,680,768 行、`sessions` 830,479 行。数据窗口 `2026-09-03 → 2026-10-02`。

### 2.2 存储浪费的三块硬账

**(a) 从未被使用的索引：5,173 MB / 117 个**

> `pg_stat_user_indexes.idx_scan = 0`、非唯一、> 1 MB 的索引合计 **5.17 GB**，占全部索引空间（16 GB）的 **32%**。

单表最集中的是 `request_state_transitions`（heap 602 MB，index 2,485 MB，其中 1,887 MB 从未使用）：

| 索引 | 大小 | idx_scan |
|---|---|---|
| `idx_state_transitions_journey_recent` | 392 MB | 0 |
| `idx_state_transitions_journey_model_recent` | 377 MB | 0 |
| `idx_state_transitions_tenant_request` | 356 MB | 0 |
| `idx_state_transitions_journey_node_recent` | 337 MB | 0 |
| `idx_state_transitions_request` | 312 MB | 0 |
| `idx_state_transitions_request_attempt` | 113 MB | 0 |
| `request_state_transitions_pkey` | 119 MB | 0 |
| `uq_state_transitions_tenant_request_seq` | 356 MB | 93,081 ✅ |
| `idx_state_transitions_created` | 121 MB | 280 ✅ |

另有 `usage_facts_default`（0 行、534 MB 索引）、`request_logs_2026_09_client_model_idx1`（hash 95 MB，0 扫描）、`request_logs_2026_09_client_model_idx3`（GIN 95 MB，0 扫描）等。

**(b) `bak_*` 备份表：26 张 / 3,904 MB / 约 937 万行**，全部为 2026-09-20 的手工备份，无索引、无 TTL。

**(c) 空分区上的索引**：`usage_facts` 9 个子分区中 6 个为 0 行（`usage_facts_default` 534 MB、`usage_facts_20260926` 49 MB、`20260927` 5.2 MB、`20260928` 4.5 MB、`20260929` 1.9 MB、`20260930` 1.6 MB），合计约 **596 MB 纯索引空转**。

### 2.3 TOAST 压缩：最大的一处低垂果实

- 全库 TOAST **10 GB**，其中 `session_bodies_2026_09` 单表 6,883 MB、`session_turns_2026_09` 2,634 MB。
- 会被 TOAST 的列共 8,251 个，其中 **`attcompression='l'`（lz4）的只有 21 个**，其余 8,230 个走 `default_toast_compression = pglz`。
- 大字段实测宽度：
  - `request_logs_bodies_2026_09.outbound_body` 平均 **66,777 字节**（null_frac 0.88）
  - `request_logs_bodies_2026_09.request_body` 平均 **9,152 字节**
  - `session_turns_2026_09.digest` 平均 **504 字节**
  - `ursm_node_snapshot_min.payload` 平均 **340 字节**（JSONB，1,212 万行 → 6 GB 堆）
- 项目里已有**应用层压缩**设计（`compression_meta` / `compression_strategy` / `compression_reason` 列 + `cmd/compression-bench`），但 `request_logs_2026_09.compression_strategy` 的 null_frac = **0.9776** —— 97.8% 的行没有走过应用层压缩。

### 2.4 分区策略

32 个分区父表，全部 `RANGE` 时间分区。结构性问题：

- **6 个父表没有 `_default` 分区**：`candidate_failure_logs`、`request_logs_bodies`、`session_censors`、`session_memora`、`session_tools`、`credential_model_index`、`model_probe_runs`。一旦写入时间超出已建分区范围会**直接报错**（`no partition of relation found`）。
- `stats_event_inbox` 只有一个 `stats_event_inbox_default`，**不做时间裁剪**，1,999 MB 无限增长。
- `request_logs` 有 `2026_07/08/09/10/11 + default` 共 6 个分区，但 07/08 分区已空。

### 2.5 归档 / Columnar：**设计完备但完全没跑**

这是最值得注意的结构性发现：

- 归档函数全部就位且**明确使用 Citus columnar**：
  `archive_request_logs` / `archive_request_wal` / `archive_credential_model_index` / `archive_routing_decision_log` / `ensure_handoff_logs_partition`，函数体形如
  `'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar'`
  且 handoff 的 columnar 分区还带 autovacuum 调优参数。
- `columnar.compression = zstd`、`columnar.compression_level = 3` 已配好。
- **但线上 columnar 表数量 = 0**，且所有 `*_archive` 父表/分区 **行数全为 0**（`request_logs_archive_2026_07/08` 各 24 kB 空壳）。
- 库里存在 `_064_convert_partition_to_heap`（把 columnar 分区**转回**堆表）和 `candidate_failure_logs_columnar_old` 残留表 —— 说明 columnar 曾被启用后又被回退。
- `repair_request_logs_detached_partitions` 函数的存在也说明历史上出现过分区 detach 问题。

**结论：归档链路是「建好了、没通电」。** 这是存储优化的第一号抓手（见第 5 节）。

### 2.6 列层面的形态

| 表 | 总列数 | 100% NULL 列数 | NULL 占比 |
|---|---|---|---|
| `request_logs_2026_09` | 157 | 56 | 36% |
| `session_turn_details_2026_09` | 61 | 32 | 52% |
| `sessions_2026_09` | 39 | 14 | 36% |
| `session_turns_2026_09` | 104 | 13 | 13% |
| `request_state_transitions` | 33 | 7 | 21% |
| `stats_event_inbox_default` | 57 | 5 | 9% |
| `ursm_node_snapshot_min` | — | 10 | — |

**必须说清楚的一点**：PostgreSQL 的全 NULL 列只占 NULL bitmap 的 1 bit/列，**不占 8 字节**。所以 56 个全 NULL 列在 216 万行上仅省约 15 MB —— **删列几乎不省空间**。它的真实代价在别处：

- 宽表让规划器统计信息、heap tuple 头、缓存行密度变差；
- `request_logs_2026_09` 非空列 `avg_width` 合计 **2,484 字节/行**，是 `session_bodies` / `request_logs_bodies` 的数倍 —— 说明**大字段已经被拆到 `request_logs_bodies` 的拆分是正确方向，但 `request_logs` 本体仍然过宽**；
- 另有 6 处类型不一致需要核对（见第 3 节）。

---

## 3. 结构比对：34 live vs 仓库基线（252 代理）

比对工具：`.db-audit/schemadiff/main.go`。live 侧输入是**从库里直接查出的权威清单**（`pg_class`/`pg_attribute`，排除分区叶子），不是解析 `pg_dump` 文本 —— 早期版本靠解析 dump 时分区识别有误，把 119 个分区叶子误算成基表，本文所有数字已按 SQL 口径重算。结果：`.db-audit/out/schemadiff2.txt`。

| 维度 | 结果 |
|---|---|
| 34 live public 关系 | **564**（445 张非分区基表，含 26 张 `bak_*` + 32 个分区父表 + 119 个分区叶子） |
| 仓库基线表 | 271 |
| 只在 34（不含 `bak_*`） | **178** —— 全部可由 750+ startup 迁移解释 |
| 只在基线 | **30** —— 几乎全是 2026_07/2026_08 的旧月分区（见下） |
| 基线有、34 缺的列 | **5** |
| 34 有、基线缺的列 | 180（正常演进） |
| 有列级差异的表 | 238 |
| 真实类型不一致 | **6** |

**基线有而 34 缺的 5 列，全部是 `request_body` / `response_body` / `outbound_body`：**

| 表 | 缺失列 |
|---|---|
| `request_logs` | `request_body`、`response_body` |
| `request_logs_hot` | `request_body`、`response_body`、`outbound_body` |

这是迁移 `328a_request_logs_bodies_table.sql` 有意做的字段拆分（正文独立成 `request_logs_bodies`），**属于设计演进，不是漂移**。

**只在基线、34 上没有的 30 张表**：

```
credit_ledger_2026_07/08            request_logs_2026_07/08
credential_model_index_2026_07/08   request_logs_bodies_2026_07/08/09
dashboard_access_events_2026_07/08  request_wal_2026_07/08
model_probe_runs_2026_07            routing_decision_log_2026_07/08
session_bodies_2026_07/08           routing_decision_log_archive_2026_08
session_module_executions_2026_07/08  session_turns_2026_07/08
sessions_2026_07/08                 system_probe_runs_default
tool_usage_stats_2026_07/08         usage_ledger_2026_07/08
```

27 张是 2026_07/2026_08 的历史月分区（在 34 上已被清理，属正常轮转），另 3 张值得留意：`routing_decision_log_archive_2026_08`、`system_probe_runs_default`、`request_logs_bodies_2026_09` —— 这三个是**非按月命名**的分区/默认分区，基线里有而 34 上没有，可能意味着 34 上对应的时间窗还没建或已被删。

**6 处类型不一致，需要人工确认**：

| 表.列 | 基线 | 34 live | 说明 |
|---|---|---|---|
| `prompt_injection_detections.risk_level` | integer | character varying(20) | 2026-09-08 实例同步记录里就是这条被「有意 gate」的差异，至今未收敛。**它带一个依赖的数值 CHECK 约束**，改类型要同步处理约束 |
| `injection_attack_vectors.embedding` | text | **vector(1536)** | pgvector 迁移的遗留：基线还写着 text，live 已经是向量列。基线重新生成后这条会自然收敛 |
| `session_turns.cost_usd` | numeric(12,6) | numeric(14,8) | 精度提升，与基线不同步 |
| `<某表>.user_intent` | character varying(50) | character varying(200) | 长度放宽 |
| `task_default_routing.tenant_id` | character varying(64) | text | |
| `task_default_routing_audit.tenant_id` | character varying(64) | text | |

> 早期版本曾报出 200+ 条「类型不一致」，绝大多数是解析器把 `timestamp with time zone` 截成 `timestamp`、`character varying(64)` 截成 `character` 造成的假阳性。修正类型提取（按约束关键字切分、右括号配对、`public.` 前缀归一）后，真实差异收敛到上面 6 条。**这也说明：结构比对必须以数据库自身返回的类型为准，不要靠正则解析 dump 文本。**

### 结构比对结论

**34 与 252 基线之间不存在需要修复的结构漂移。** 差异全部落在三类良性范围：「34 比基线新」（迁移已应用，178 张表 / 180 列）、「基线里的旧月分区已被清理」（27 张）、「有意做的字段拆分」（5 列）。真正需要跟进的是上面 6 处类型不一致，其中 `risk_level` 因为带 CHECK 约束最值得优先收敛。

基线文件本身已经严重滞后于运行时（271 张表 vs 实际 564 个关系），建议把「从目标实例重新 `pg_dump --schema-only` 并更新基线」纳入常规流程，否则它将失去作为 252 参考源的价值。

---

## 4. 慢 SQL 与扫描行为

`pg_stat_statements` 窗口内 **693 条语句、累计执行 16,190.6 秒（4.5 小时）**。分布极不均衡。

### 4.1 总耗时 Top 10

| 耗时 | 调用数 | 均值 | 语句 |
|---|---|---|---|
| **5,100.7 s (31.5%)** | 6,792,572 | 0.75 ms | `credential_probe_queue` 抢占队列（`WITH picked AS (SELECT q.id ... FOR UPDATE SKIP LOCKED)`) |
| **3,564.2 s (22.0%)** | 21,259 | 167.7 ms | `WITH latest_bucket AS (SELECT credential_id, raw_model, MAX(bucket) ... GROUP BY)` 重建模型定价索引 |
| 905.6 s (5.6%) | 21,260 | 42.6 ms | `INSERT INTO credential_model_index_hot` |
| 714.8 s (4.4%) | 3,027 | 236.1 ms | 凭据可用性选择（`COALESCE(cmb.available,...)`） |
| 564.8 s (3.5%) | **11,657,822** | 0.05 ms | `INSERT INTO public.assets ... ON CONFLICT DO UPDATE` |
| 457.0 s | 1 | **456,965.8 ms** | 单条统计查询，落盘 9,026,962 个临时块 |
| 350.7 s | 9,339 | 37.6 ms | 凭据错误聚合 |
| 313.5 s | 5,015,133 | 0.06 ms | `INSERT INTO ursm_node_snapshot_min` |
| 261.6 s | 274,744 | 0.95 ms | `SELECT cmb.credential_id, pm.raw_model_name ...` **单次返回 1,984 行，累计 5.45 亿行** |
| 233.3 s | 18,596 | 12.5 ms | `DELETE FROM session_aggregate_outbox WHERE id IN (SELECT ...)` |

**两条语句吃掉全库 53.5% 的执行时间。**

### 4.2 慢查询（均值）Top

| 均值 | 调用数 | 语句 |
|---|---|---|
| 425.3 ms | 170 | `INSERT INTO request_stats_minute ...`（分钟聚合 upsert） |
| 255.1 ms | 170 | 凭据状态窗口聚合 |
| 236.1 ms | 3,027 | 凭据可用性选择 |
| 205.9 ms | 91 | `UPDATE request_logs_hot SET is_final_success ...` |
| 167.7 ms | 21,259 | 定价索引重建 |
| 59.1 ms | 34 | `REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_5m` |
| 50.3 ms | 1,564 | 路由策略解析 |
| 23.4 ms | 5,349 | `DELETE FROM authagent.sessions ...`（跨 schema） |

### 4.3 全表扫描热点（浪费最严重）

| 表 | seq_scan | seq_tup_read | 行数 | 判读 |
|---|---|---|---|---|
| `providers` | **1,321,518,365** | 320,837,722 | 60 | 13 亿次扫 60 行的表 |
| `models_canonical` | 12,143,365 | **11,527,722,603** | 960 | 115 亿行扫描量 |
| `credential_model_index_2026_09` | 21,259 | **9,118,694,804** | 435,493 | 单次扫 42.9 万行 |
| `provider_models` | 3,664,369 | 5,041,497,446 | 1,427 | |
| `credential_model_bindings` | 433,265 | 882,226,330 | 2,045 | |
| `session_summaries` | 5,296 | 855,951,225 | 338,381 | |
| `request_stats_dim_minute` | 2,210 | 855,905,512 | 709,901 | |
| `candidate_failure_logs_2026_09` | 9,341 | 787,477,642 | 85,735 | 单次扫 8.4 万行 |
| `analysis_events` | 52 | 3,063,835 | 43,415 | |
| `armor_judgments` | 41 | 29,962,640 | 12,149 | |

**索引选择性反向指标**（每次索引扫描读回的行数）：

| 表 | idx_scan | idx_tup_read | 每次读回 |
|---|---|---|---|
| `node_probe_runs` | 21,525 | 388,554,178 | **18,051** |
| `request_stats_dim_minute` | 545,466 | 329,159,642 | 603 |
| `credential_probe_queue` | 27,247,859 | 3,452,759,389 | 127 |
| `request_logs_2026_09` | 130,667,083 | 3,788,407,554 | 29 |

### 4.4 临时文件：4 天 366 GB

`temp_files=116,430`，`temp_bytes=393,814,005,608` = **366 GB**。元凶是 `work_mem=4MB` 遇上几个大聚合：

- 单条 9,026,962 临时块（≈70 GB）的统计查询，耗时 457 s；
- `session_turns ∪ session_turns_hot` 的 UNION 计数，6.1 s / 3.1 MB；
- 另有 314,892 / 74,449 / 43,911 块的三条。

### 4.5 统计信息与膨胀

- **625 张表自 `stats_reset` 起从未 ANALYZE 过**。这直接解释了大量「该走索引却全表扫」：`credential_model_index_2026_09`、`session_summaries`、`analysis_events`、`armor_judgments`、`providers` 等都缺新鲜统计。
- 死元组 TOP：

| 表 | 活 | 死 | 死占比 | autovac 次数 | 大小 |
|---|---|---|---|---|---|
| `request_journey_observation_outbox` | 0 | 4,164 | 100% | **1,749** | 11 MB |
| `assets` | 2,215 | **247,496** | **99.1%** | **5,023** | 56 MB |
| `gateway_instances` | 17 | 6,613 | 99.7% | 1,020 | 2.6 MB |
| `routing_analytics_7d` | 830 | 16,855 | 95.3% | 830 | 9.5 MB |
| `model_aliases` | 2,816 | 14,540 | 83.8% | 401 | 2.3 MB |
| `provider_models` | 1,427 | 4,567 | 76.2% | 399 | 1.9 MB |
| `credential_model_bindings` | 2,045 | 5,999 | 74.6% | 471 | 2.0 MB |
| `node_probe_runs` | 586,030 | 49,406 | 7.8% | **0** | 816 MB |
| `request_logs_2026_09` | 2,150,888 | 8,929 | 0.4% | **0** | 5,007 MB |

`node_probe_runs`（816 MB）与 `request_logs_2026_09`（5 GB）**从未被 autovacuum 也从未被 analyze** —— 这是必须先处理的风险项。

### 4.6 写入放大：4 天内的 DML 账

| 表 | INSERT | UPDATE | DELETE | HOT UPDATE |
|---|---|---|---|---|
| `assets` | 36 | **11,878,352** | 24 | 10,829,944 |
| `routing_analytics_7d` | **4,609,672** | 0 | 4,623,097 | 0 |
| `ursm_node_snapshot_min` | 5,022,780 | 0 | 1,431 | 0 |
| `stats_usage_monthly` | 2,703,415 | 0 | 2,728,574 | 0 |
| `stats_event_inbox_default` | 13,143 | 1,208,693 | 1,201,355 | 0 |
| `request_stage_events` | 79,250 | 0 | 2,199,215 | 0 |
| `usage_facts_default` | 4,257 | 0 | 1,259,436 | 0 |
| `request_state_transitions` | 83,783 | 0 | 1,713,385 | 0 |
| `model_aliases` | 146 | 745,847 | 24 | 514,535 |
| `stats_usage_daily` | 1,045,392 | 4,170 | 1,065,761 | 6 |

三个明确反模式：

1. **`assets` 心跳式 upsert**：2,215 行的表被 upsert **1,166 万次**（约 34 次/秒）。根因在 `apihub/pg_store.go:73` 的 `upsertAssetSQL` —— `ON CONFLICT DO UPDATE SET ... last_seen_at = now()` 保证每次都产生新版本，于是 1,088 万次 HOT 更新、24.7 万死元组、5,023 次 autovacuum。叠加 `begin`(1,998 万) / `commit`(1,997 万) / `set_config`(1,290 万)，等于**每秒 34 个只为了刷新心跳的事务**。
2. **「先全删再全插」的聚合表**：`routing_analytics_7d`（830 行）在 4 天内 461 万插 + 462 万删；`stats_usage_monthly`（14,506 行）270 万插 + 273 万删。正确做法是只对变动 bucket 做 upsert。
3. **`stats_event_inbox_default` / `usage_facts_default` 自我消耗**：`usage_facts_default` 0 行、534 MB 索引，却被删了 125.9 万次。

### 4.7 其他

- WAL：4 天 **13 GB**（`wal_bytes`），`wal_compression=off`。
- JIT：39 条语句触发 5,070 次编译。
- 死锁 26 次，`idle_in_transaction_session_timeout=0`（未设防）。
- `max_connections=1000` 但 `shared_buffers` 仅 128 MiB —— 高并发下会互相抢缓冲。
- **有一条查询已连续跑了 4 小时 06 分**（采集时 pid 734360）：
  ```sql
  SELECT (SELECT count(*) FROM request_logs_bodies_hot) AS v1_hot行,
         (SELECT count(DISTINCT request_id) FROM request_logs_bodies_hot) AS v1_hot去重request...
  ```
  典型的临时核验脚本忘了收尾。`request_logs_bodies_hot` 有 4,369 行 / 74 MB TOAST，这种查询本该是毫秒级 —— 跑 4 小时说明它要么在等锁，要么在别的负载下被拖死。**当前 `statement_timeout=0`（未设）**，没有任何兜底。建议设一个 30–60 s 的全局 `statement_timeout`，或至少给 `lock_timeout` 一个值。
- **数据盘是 virtiofs 网络文件系统**（`1.9T virtiofs0`），不是本地块设备。这解释了两件事：一是本次审计中 columnar 建表异常缓慢（20 万行样本 15 分钟未完成，2 万行才可用），二是 34 上的 I/O 性能数字**不能外推到 252**（252 是真实云盘）。所有 I/O 相关结论请以 252 复核。

---

## 5. 优化建议

分四档，按「收益/风险比」排序。每条都标注作用面：**空间** / **性能** / **两者**。

### A 档：零风险、立即可做（配置与运维，预计回收 5.2 GB 空间 + 数倍响应改善）
| # | 措施 | 作用面 | 依据 |
|---|---|---|---|
| A1 | `shared_buffers` 调到 8–10 GB（32 GB 机器按 25%–30%），`effective_cache_size` 调到 20–24 GB | 性能 | 现 128 MiB；缓存命中仅 87.3%，生产基线要求 ≥99% |
| A2 | `work_mem` 调到 32–64 MB；对 `credential_probe_queue` / `request_stats_*` / `session_summaries` 等重排序表单独设 128 MB | 性能 | 4 天 366 GB 落盘 |
| A3 | `random_page_cost=1.1`、`effective_io_concurrency=200` | 性能 | SSD 环境下默认值导致规划器高估随机读 |
| A4 | `track_io_timing=on`、`log_min_duration_statement=500`、`log_autovacuum_min_duration=1000` | 性能（可观测性） | 当前慢 SQL 与 autovacuum 全是黑盒 |
| A5 | `max_wal_size=8GB` / `min_wal_size=2GB`、`checkpoint_completion_target=0.9`、`wal_compression=on` | 性能 | 4 天 947 timed + 988 requested checkpoint |
| A6 | **对全库跑一次 `ANALYZE`**（重点 `node_probe_runs`、`request_logs_2026_09`、`credential_model_index_2026_09`、`session_summaries`、`providers`、`provider_models`） | 性能 | 625 张表无统计信息，直接对应第 4.3 节的灾难性全表扫 |
| A7 | 删除 26 张 `bak_*` 备份表（先异地归档） | 空间 | **3,904 MB / 937 万行**，无外键/视图/复制槽依赖 |
| A8 | 删掉 6 个空 `usage_facts` 分区 | 空间 | **≈596 MB** 纯索引空转 |
| A9 | 给 `node_probe_runs`、`request_logs_2026_09` 等显式设 `autovacuum_vacuum_scale_factor` / `analyze_scale_factor` | 性能 | 两张最大表从未被自动维护过 |

> A1–A5 需要改容器启动参数（PG 镜像的 `command` 或挂载 `postgresql.conf`），A6–A9 是纯 SQL。建议 A1–A5 + A6 作为第一批。
>
> **A7/A8 已固化为可执行脚本**：`sql/fixes/2026-10-02-db-storage-reclaim.sql`。默认 dry-run，需显式打开开关才动数据。已在 34 上做过两轮实测（dry-run 通过；真实执行包在 `BEGIN…ROLLBACK` 里演练通过，回滚后 `bak_*` 计数仍为 26）。注意分两档：
> - 保守档（只开 `apply=on`）：删 11 张 0 行 `bak_*` + 5 张过去空分区 + `TRUNCATE` 兜底分区 = **≈596 MB**，零数据风险；
> - 加 `force_nonempty=on`：再删 15 张非空 `bak_*`（含 3,900 MB 的 `bak_20260920_ursm_node_snapshot_min`）= **≈4,500 MB**，需先 `pg_dump` 留档。
>
> `usage_facts_default` 是兜底分区，脚本只 `TRUNCATE` 不 `DROP` —— 删掉它会导致时间越界的写入直接失败。

### B 档：索引治理（预计再回收 4.5–5 GB 空间，同时提升写入性能）

| # | 措施 | 作用面 |
|---|---|---|
| B1 | 删除 `pg_stat_user_indexes.idx_scan=0` 且非唯一、>1 MB 的 117 个索引中的确认项（先在 252 上同样核对，避免误删生产在用索引） | 空间 + 写入性能 |
| B2 | 优先处理 `request_state_transitions`：6 个零扫描索引共 1,887 MB，加上零扫描的 `request_state_transitions_pkey`(119 MB) | 空间 + 写入性能（该表每次 INSERT 要维护 2.5 GB 索引） |
| B3 | 删 `request_logs_2026_09_client_model_idx1`(hash 95 MB) 与 `idx3`(GIN 95 MB)，二选一或都删 | 空间 |
| B4 | 为高频但缺统计/缺索引的路径补选择性索引：针对 `node_probe_runs` 每次索引扫读回 18,051 行的问题复核其索引列顺序 | 性能 |
| B5 | 对 `request_stage_events`（2.2 M DELETE / 79 K INSERT）、`session_aggregate_outbox`（818 K DELETE / 14 K INSERT）这类「几乎只删不插」的表，检查是否可用 BRIN 替代部分 B-tree | 空间 |

> 索引删除前务必在 252 上跑同样的 `idx_scan` 核对。34 是开发实例，其扫描模式未必等于生产。

### C 档：写入路径改造（最大的性能收益，同时消掉膨胀）

| # | 措施 | 作用面 | 依据 |
|---|---|---|---|
| C1 | **`assets` 心跳降频**：`last_seen_at` 不必每次 upsert 都刷新。改为进程内/内存聚合，按 30–60 s 批量落一次，或对同一 `(kind, ref_id)` 做单飞合并 | 性能（巨大）+ 空间 | 34 tx/s 的纯心跳事务；5,023 次 autovacuum；247 K 死元组 |
| C2 | 同样处理 `gateway_instances`（1,020 次 autovacuum、99.7% 死）、`request_journey_observation_outbox`（1,749 次 autovacuum、100% 死）、`credential_model_index_hot`（1,525 次） | 性能 | 同一类心跳/租约写放大 |
| C3 | **聚合表改 upsert**：`routing_analytics_7d`、`stats_usage_monthly`、`stats_usage_daily`、`request_stats_dim_minute`、`request_stats_error_drill_minute` 从「DELETE 窗口 + INSERT 全量」改为「只 upsert 变动的 bucket」 | 性能（巨大）+ 空间 | 830 行的表 4 天 461 万插 + 462 万删 |
| C4 | `stats_event_inbox_default`（1,999 MB，无时间分区）与 `usage_facts_default`（0 行、534 MB 索引）改成分区表或加清理策略 | 空间 | 无界增长 / 空转 |
| C5 | `credential_probe_queue` 抢占从 6,792,572 次调用降频（批量 SKIP LOCKED，或把租约续期改为批量） | 性能 | 占全库 31.5% 执行时间 |
| C6 | `credential_model_index_with_current_month` 的 GROUP BY 重建（22% 时间）改为增量维护或物化视图 + 定时刷新 | 性能 | 单次 167.7 ms，21,259 次 |
| C7 | `SELECT cmb.credential_id, pm.raw_model_name ...` 每次返回 1,984 行、累计 5.45 亿行 → 提升为按需查询或加缓存 | 性能 | 274,744 次调用 |
| C8 | 修掉 `request_stage_events`、`usage_facts_default` 等的大批量 DELETE 热点（配合分区按时间 DROP 代替 DELETE） | 性能 | 2.2 M / 1.26 M 次删除 |

### D 档：存储引擎与版本演进（面向中长期，收益最大也最需要验证）

**D1. 启用 Citus Columnar 做冷数据归档（最高优先级）**

现状是「归档链路建好了但没通电」：5 个 `archive_*` 函数已经用 `USING columnar` 建分区，`columnar.compression=zstd` 已配好，但线上 **columnar 表 = 0，所有 archive 表行数 = 0**，同时存在 `_064_convert_partition_to_heap` 回退痕迹。

- 收益：zstd columnar 对 `request_logs` / `session_bodies` / `ursm_node_snapshot_min` 这类宽表通常有数倍压缩比，10 GB TOAST + 27 GB heap 有很大压缩空间；columnar 不参与主索引，写入路径不受影响。
- 风险：columnar **不支持索引、不支持 UPDATE/DELETE 高频路径**，只适合「写完就冻结、按时间范围读」的冷数据。这正是归档分区的定位。
- 建议：先在 34 上把 `archive_request_logs` 对 `request_logs_2026_08` 跑一次，量出实际压缩比与查询延迟，再决定是否在 252 推广。**先验证 `_064_convert_partition_to_heap` 当初为什么回退**，避免重蹈覆辙。

**D2. 归档真正跑起来（分区 DROP 代替 DELETE）**

`session_bodies_2026_09` 单分区 8.6 GB、`ursm_node_snapshot_min` 7.6 GB。`ursm_node_snapshot_min` 12,126,876 行、覆盖仅 30 天 —— 说明它没有真正的时间裁剪，是滚动快照而非按天归档。应改为「按天/周分区 + 到期 DROP 分区」，避免 DELETE 式清理产生死元组。

**D3. TOAST 压缩切到 lz4（低风险、立刻见效）**

10 GB TOAST 目前全部 pglz。PG 17 支持 lz4：

```sql
ALTER TABLE <table> ALTER COLUMN <col> SET STORAGE EXTERNAL;   -- 先挤出内联
ALTER TABLE <table> ALTER COLUMN <col> SET COMPRESSION lz4;
-- 或对新表：ALTER TABLE ... SET (toast_compression = lz4)
```

对 `session_bodies.outbound_body` / `request_logs_bodies.outbound_body` / `request_logs_bodies.request_body` / `ursm_node_snapshot_min.payload` / `session_turns.digest` 优先。需实测（lz4 解压比 pglz 快、压缩比略低，对「存空间换 CPU」的诉求正好合适）。注意：改压缩方式需要重写列，`ALTER TABLE ... SET COMPRESSION` 只对新写入生效，历史数据需 `VACUUM FULL`（会锁表）或 `pg_repack`。建议对已关闭的 08 月分区做。

**D4. 应用层压缩没有真正启用**

`request_logs_2026_09.compression_strategy` 的 null_frac = 0.9776，97.8% 的行没走应用层压缩。项目已有 `cmd/compression-bench` 和 `compression_meta` 设计，但没推广。对 66 KB 的 `outbound_body`，应用层压缩（如 zstd 字典）通常比 TOAST 的通用压缩更有效。

**D5. PostgreSQL 17 → 18 的匹配度评估**

252 与 34 都在 **17.10**，因此以下 18 特性全部**当前不可用**，是否升级取决于收益：

| PG18 特性 | 与本项目负载的匹配度 | 判断 |
|---|---|---|
| **异步 I/O 子系统**（`io_method=worker\|io_uring`，顺序扫描/位图堆扫/vacuum，实测最高 3×） | **高**。本库 27 GB heap、仅 128 MiB `shared_buffers`、cache hit 87.3%、累计 90 亿+ 行全表扫描量 —— 典型 I/O 受限 | 值得为它排升级评估 |
| **多列 B-tree skip scan** | **高**。`providers` / `provider_models` / `credential_model_bindings` / `models_canonical` 这些「前面列无等值条件」的查询，正是 skip scan 的目标场景 | 直接改善第 4.3 节的全表扫 |
| **virtual generated column 成为默认** | **中高**。`request_logs` 157 列里 56 列全 NULL，大量派生字段（`search_text`、`auto_decision` 摘要、`compression_meta`） | 虚拟列可省空间；但注意「虚拟列不能建索引」，可索引的仍要 STORED |
| **GIN 索引并行构建** | 中。本库有 GIN 索引（`request_logs_2026_09_client_model_idx3`、`assets.tags`） | 主要是维护窗口提速 |
| **分区裁剪与多关系加锁改进** | **高**。1,045 个分区、157 张表开 RLS | 直接收益 |
| **`uuidv7()`** | 中。若 `request_id` 之类主键改 uuid7，可显著改善 B-tree 缓存局部性 | 需改生成逻辑，收益明确 |
| **pg_upgrade 保留 planner 统计信息** | **高**。本项目 625 张表无统计信息，升级后重跑 ANALYZE 成本高 | 这是升级的额外理由 |
| **OL→AR→ 规划器改进**（hash join 内存、自连接消除、IN 转 ANY） | 中高 | 普遍受益 |
| **MD5 密码弃用警告** | 低（运维项） | 需提前清理 |
| **initdb 默认开启数据校验和** | 低 | 全新集群才相关 |

**PG17 已在用但未调优的特性**（无需升级即可受益）：

- **`io_combine_limit=168kB`**（默认已开）—— 与 A3 的 `effective_io_concurrency` 配套。
- **vacuum 新内存结构（20× less memory）** —— 把 `maintenance_work_mem` 从 64 MB 提到 1–2 GB 收益明显（对应 A2）。
- **BRIN 并行构建** —— 冷数据时间列适合 BRIN，对应 D2。
- **`pg_stat_io`** 已可用（35 行数据），配合 A4 的 `track_io_timing=on` 可做精确 I/O 归因。
- **`pg_stat_checkpointer`**（947 timed vs 999 requested）—— 说明 checkpoint 已经跟不上了，对应 A5。
- **`vacuum_buffer_usage_limit`** 默认仅 2 MB，对 27 GB heap 偏小，可调高。

### E 档：结构一致性收尾

| # | 措施 |
|---|---|
| E1 | 收敛 6 处类型差异，优先 `prompt_injection_detections.risk_level`（integer vs varchar(20)，带依赖 CHECK 约束）；其余为 `injection_attack_vectors.embedding`(text→vector(1536))、`session_turns.cost_usd`(numeric 精度)、`user_intent` 长度、`task_default_routing{,_audit}.tenant_id`(varchar→text) |
| E2 | 仓库基线 `sql/schema/01-schema.sql` 停留在 271 张表，运行时已 564 个关系；建议把「从目标实例重新 `pg_dump --schema-only` 并更新基线」纳入常规流程，否则基线失去参考价值。另需确认基线里那 3 个非按月命名分区（`routing_decision_log_archive_2026_08`、`system_probe_runs_default`、`request_logs_bodies_2026_09`）在 34 上为何不存在 |
| E3 | 给 6 个无 `_default` 分区的父表（`candidate_failure_logs`、`request_logs_bodies`、`session_censors`、`session_memora`、`session_tools`、`credential_model_index`、`model_probe_runs`）补默认分区或前置创建，避免时间越界时插入直接失败 |

### F 档：252 生产库专属（2026-10-03 新增，依据第 0 节实测）

A 档是按 34 的规格写的，**其中大部分在 252 上已经做好了**。252 要动的是下面这些，按性价比排序：

| # | 措施 | 依据（252 实测） | 预期收益 |
|---|---|---|---|
| **F1** | **修 `assets` 的 upsert 路径**（`apihub/pg_store.go:73`） | 1.006 亿次调用打在一张 2,140 行的表上；`upd=5,713 万`、`hot_upd=4,626 万` | **252 上单笔最大的可消除浪费**。34 的 C 档同源 |
| F2 | 宿主机扩容或迁走部分容器 | load 20–26 / 4 核，PG 只占 0.69 核 | 调参无法替代。**这是 252 慢的第一原因** |
| F3 | `settings_kv` 加进程内缓存 | `SELECT value::text FROM settings_kv` 被调 **2.95 亿次** | 2.95 亿次点查全在走数据库 |
| F4 | 批量化事务边界 | `begin` / `commit` 各 **1.45 亿次** | 每操作一个事务，开销随调用数线性增长 |
| F5 | 治理 `session_turns` 锁竞争 | `pg_advisory_xact_lock` 累计 **301,854 s = 83.9 小时**，占全库执行时间 **13.5%** | 252 的第一大热点（34 无此问题） |
| F6 | 修 `analyze_llm_gateway_table_stats()` | 777 次调用，均值 **106.6 秒** | 维护函数慢到不正常 |
| F7 | `daily_kline(date)` 建索引或提统计 | `SELECT MAX(date)` 6,125 次，均值 **8.4 秒** | 取最大日期不该花 8 秒 |
| F8 | 440 张表补 ANALYZE | `tables_never_analyzed = 440` | 与 34 同病（625 张），但会加剧 0.6 节的全表扫 |
| F9 | `ursm_node_snapshot_min` 改分区 + DROP | 10 GB / 1,872 万行，占全库近一半；`del` 比 `ins` 还多 | 避免逐行删除产生死元组 |
| F10 | `routing_analytics_7d` 改增量刷新 | 每次 REFRESH 全量重写 **4,500 万行**，存活仅 2,468 行 | 占 5.4% 执行时间 |
| F11 | 排查死锁（两端共有） | 252 累计 114 次 ≈ 10.9 次/天，与 34 的 12.2 次/天一致 | 证明是应用层事务顺序问题，非配置 |
| F12 | `session_dim` 补 autovacuum | 死元组 8.1%，`last_autovacuum = never` | 从未被自动维护过 |
| F13 | 收敛 `max_connections=1000` + 61 idle 连接 | 4 核 / 14 GB 上的上限过高 | 建议上连接池器（pgbouncer） |
| F14 | 复核 B 档删索引清单 | 252 零扫描索引仅 1.39 GB（34 是 5.17 GB） | **不可照搬 34 的清单** |

> **F 档与 A 档的关系**：A 档是「把配置调对」，F 档是「配置已经对了，问题在别处」。**在 252 上做 A 档是浪费时间。**

---

## 6. 预期效果汇总

| 档位 | 空间回收 | 性能收益 | 风险 |
|---|---|---|---|
| A（配置 + 运维） | ≈4.5 GB（`bak_*` 3.9 GB + 空分区 0.6 GB） | 缓存命中 87%→99%+；消除 366 GB 临时落盘；消除大部分全表扫 | 低（重启 + ANALYZE） |
| B（索引治理） | 4.5–5 GB | `request_state_transitions` 等表写入 IO 大幅下降 | 中（需 252 侧交叉核对） |
| C（写入路径） | 数百 MB（膨胀） | **全库执行时间预计下降 40%–60%**（两条语句占 53.5%） | 中（需改应用代码 + 回归） |
| D（columnar/归档/lz4/PG18） | 10–20 GB 量级（取决于 columnar 实测压缩比） | 冷数据查询显著变快；vacuum 成本下降 | 高（需灰度验证） |
| **F（252 专属，2026-10-03 新增）** | 1.4 GB（零扫描索引）+ 膨胀 | **宿主机扩容是前提**；F1/F3/F4 合计可减少数十亿次无效往返 | 中（多数需改应用代码） |

> **分机器看优先级**：34 走 **A → B → C**；252 走 **F2（宿主机）→ F1（assets upsert）→ F3/F4（缓存与事务）**。两台机器的最优解完全不同，**不要把 34 的方案搬到 252**。

**均衡点建议**：先做 A+B+C（不改变存储引擎、风险可控），拿到约 **9–10 GB 空间 + 大幅响应改善**；把 D1（columnar 归档）作为独立项目在测试库验证压缩比与查询延迟，确认后再在 252 推广。A3（lz4）属于典型的「用少量 CPU 换大量空间」，与你的均衡诉求完全一致，可以优先于 columnar 落地。

---

## 7. 审计过程中的事故记录（必读）

**2026-10-02 19:02:01，34 侧 `llm-gateway-pg` 发生了一次约 1.3 秒的崩溃恢复。**

日志（`/var/lib/postgresql/data/log/postgresql-2026-10-02_000000.log`）：

```
2026-10-02 19:02:01.444 [1] LOG:  server process (PID 811493) was terminated by signal 1: Hangup
2026-10-02 19:02:01.445 [1] LOG:  terminating any other active server processes
2026-10-02 19:02:25.481 [811517] LOG:  database system was not properly shut down; automatic recovery in progress
2026-10-02 19:02:25.692 [811517] LOG:  redo done at 10E/52690BC0
2026-10-02 19:02:26.138 [811518] LOG:  checkpoint complete ... lsn=10E/52690BE8
2026-10-02 19:02:26.193 [1] LOG:  database system is ready to accept connections
```

**影响：无数据丢失。** 恢复后复核：

| 项 | 值 |
|---|---|
| postmaster 进程 | **未重启**（PID 1 仍是原始进程，`pg_postmaster_start_time = 2026-09-30 15:00:36`，uptime 2d4h） |
| 数据库体积 | 56 GB（未变） |
| `request_logs` | 2,164,242 行 |
| `sessions` | 834,612 行 |
| `ursm_node_snapshot_min` | 12,171,102 行 |
| `bak_*` 表 | 26 张（完好） |
| 可连接库 | 80 个 |
| 应用连接 | 30 个（网关已自动重连） |
| 本次审计的临时对象 | 无残留（`audit_scratch` 不存在，columnar 表 0 张） |

**成因：无法确定，但时间点高度可疑。** 触发恢复的是 SIGHUP（signal 1），发生在审计过程的一次「清理泄漏的 docker exec 会话」脚本运行后约 10 秒。审计侧无法访问宿主机日志，因此不能排除是主机/容器层面发出的信号。可以确认的是：那次清理脚本在容器内留下的 6 个孤儿 exec 会话（全部卡在 `psql` 的 pager 上，最久的已运行 1 小时）确实是我方脚本的缺陷。

**已采取的措施**：

1. 停止在 34 上做任何重写入基准测试。数据盘是 virtiofs 网络文件系统，跑 columnar 转换这类高写入压测风险不可控。
2. `d34curl.ps1` 加了硬性防护：每次 exec 强制注入 `PGPAGER=cat`（pager 是本次 6 个孤儿会话的根因）、`statement_timeout=180s`、`lock_timeout=5s`；并发调用改用唯一临时目录（原先固定路径会互相覆盖）。
3. columnar / lz4 基准改为带闸门的独立脚本 `sql/audit/2026-10-02-db-columnar-lz4-bench.sql`，**默认 dry-run**，需在专用实例或 252 上人工执行。

**给后续操作者的两条硬规则**：

- 34 是**共享开发机**（同实例 80+ 个库、磁盘 94%、多个项目在用）。任何压测/重写入操作都应放到专用实例或 252 之外的隔离环境。
- 这个实例的 `statement_timeout=0`（未设）。建议在 `postgresql.conf` 里设一个全局值（30–60 s），让 runaway 查询自己失败而不是拖垮整个实例。

### 7.1 2026-10-03 在 252 上犯的四个错误（均已修正）

补采 252 数据时踩到的坑，都属于"工具缺陷"而非环境问题，已在脚本里修掉并留注释：

1. **把采集脚本包在 `BEGIN READ ONLY … COMMIT` 里。** 一条语句报错 → 事务 aborted → 其后所有语句被一并拒绝。实测一次 `jit_time` 列名错误，导致 9 个 section 全空而脚本退出码仍是 0。**已改为 `SET default_transaction_read_only = on`**，每条语句独立且只读。
2. **`pg_stat_statements` 没有 `stats_reset` / `jit_time` 列。** `stats_reset` 在 `pg_stat_statements_info`（且该表也没有 `deallocate`，是 `dealloc`）；JIT 相关为 `jit_functions` / `jit_emission_time` 等。
3. **`pg_stat_checkpointer` 的列是 `buffers_written`**，不是 `buffers_checkpoint`。
4. **统计索引时沿用了表的 relkind 过滤。** 索引是独立的 `relkind='i'`，在 `relkind IN ('r','m','p')` 的结果里再 `FILTER` 恒为 0，导致 252 的 index 容量一度被报成 0。

另修正了 `instdiff` 的一个标签 bug：某侧缺该节时，提示语里的 A/B 标签写反了（会让人把"谁缺该节"看反）。

> **本次审计在 252 上没有产生任何副作用**：全程只读，作业在 252 上后台执行后自行结束，远端临时目录 `/tmp/d252async-*` 与 `/tmp/d252t` 已清理，容器内同步清理。

---

## 8. 待你确认

1. **【最高优先】252 宿主机是否打算扩容？** 实测 load 20–26 跑在 4 核上，而 PG 容器只占 0.69 核 —— **瓶颈在宿主机，不在数据库**。这个问题的答案决定 252 后续所有优化的上限：不扩容的话，做完 F 档其余项也只能把系统从"很慢"改善到"较慢"。
2. **`assets` 的 1 亿次 upsert 谁来改？** 252 上 1.006 亿次调用 / 2,140 行，34 上 1,166 万次，是同一个缺陷（`apihub/pg_store.go:73`）。这是两端共同的最大单笔浪费，但需要改应用代码 + 回归测试。
3. **生产库上出现过的 `DROP DATABASE IF EXISTS bloat_negctl`（0.6 节）** —— 需要确认是谁在什么场景下跑的。膨胀实验应固定在测试环境。
4. **A7/A8 清理脚本**已交付：`sql/fixes/2026-10-02-db-storage-reclaim.sql`（默认 dry-run，两轮实测通过）。**保守档 ≈596 MB 零风险；加 `force_nonempty=on` 才到 ≈4,500 MB，需先 `pg_dump` 留档。** 建议在 34 上执行，**不要在 252 上跑**（252 无 `bak_*` 问题，且磁盘仅用 47%，不急）。
5. **34 侧的定位**：34 是共享开发机（同实例还跑着 80+ 个其他库，`llm_gateway` 56 GB，磁盘已用 94%）。A 档参数是按「32 GB 内存的 llm-gateway 独占实例」给的，若 34 长期是共享环境，调 `shared_buffers` / `work_mem` 需重新按份额计算。
6. **252 的剩余采集项**（分区清单、归档函数、全 NULL 列、列级结构清单）需挑低峰期补齐。通道已就绪：
   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252async.ps1 `
       -Action submit -RunId r5 `
       -SqlFile sql\audit\2026-10-02-db-audit-collect.sql
   # 隔几分钟查一次
   powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252async.ps1 -Action status -RunId r5
   # STATE=finished 后取回
   powershell -NoProfile -ExecutionPolicy Bypass -File .db-audit\d252async.ps1 `
       -Action fetch -RunId r5 -OutFile .db-audit\out\inst252.txt
   go run .db-audit\instdiff\main.go -a .db-audit\out\inst34.txt `
       -b .db-audit\out\inst252.txt -label-a 34 -label-b 252 `
       -out .db-audit\out\diff_34_vs_252.md
   ```

---

## 附：证据文件

| 文件 | 内容 |
|---|---|
| `.db-audit/d34curl.ps1` | Docker Remote API exec 通道（curl，规避 PowerShell 引号与 101 劫持问题） |
| `.db-audit/sql/01_baseline.sql` … `10_cols.sql` | 全部采集脚本 |
| `.db-audit/out/34_baseline.txt` | 版本、扩展、参数、库大小 |
| `.db-audit/out/34_tables.txt` | 表容量、索引比、扫描、膨胀 |
| `.db-audit/out/34_slowsql.txt` | pg_stat_statements 各维度榜单 |
| `.db-audit/out/34_slow2.txt` | 慢 SQL Top、assets 剖析、连接与等待事件 |
| `.db-audit/out/34_dist.txt` / `34_dist2.txt` | 分区清单、列分布、TOAST、schema 对象 |
| `.db-audit/out/34_final.txt` / `34_cols.txt` | 归档/columnar 现状、全 NULL 列、写放大账 |
| `.db-audit/out/34_schema.sql` | 34 live schema dump（SHA-256 `d6f68db0…78b7`） |
| `.db-audit/schemadiff/main.go` + `out/schemadiff2.txt` | 结构比对工具与结果（SQL 权威口径） |
| `.db-audit/out/34_colinv.txt` | live 侧权威列清单（SQL 直接导出，7,184 列 / 445 基表 / 2,493 索引） |
| `sql/audit/2026-10-02-db-audit-collect.sql` | 双实例通用只读采集脚本（38 节，已在 34 实测零错误） |
| `sql/audit/2026-10-02-db-audit-252-minimal.sql` | 252 精简采集（9 节系统目录查询，低峰期可跑完） |
| `.db-audit/instdiff/main.go` | 双实例采集结果 diff 驱动（按分节对比，已用构造样本验证） |
| `sql/fixes/2026-10-02-db-storage-reclaim.sql` | A7/A8 可执行清理脚本（默认 dry-run，已实测） |
| `sql/audit/2026-10-02-db-unused-index-candidates.sql` | B 档索引候选（默认不删、逐条放行、`DROP INDEX CONCURRENTLY`） |
| `sql/audit/2026-10-02-db-columnar-lz4-bench.sql` | D 档 columnar / lz4 基准（默认 dry-run，须专用实例） |
| `.db-audit/d252.ps1` | 252 只读 exec 通道（只读事务 + 超时注入 + 写语句闸门，已实测连通） |
| `.db-audit/d252sh.ps1` / `.db-audit/d252dump.ps1` | 252 通用远端脚本 / schema dump 通道 |
| `.db-audit/split_collect.ps1` | 把 38 节采集按 section 边界切成小批（252 高负载下分批跑） |
| `.db-audit/out/inst34.txt` | 34 侧分节基线，供 `instdiff` 与 252 对比 |
| `.db-audit/out/inst252-min.txt` | **252 实测第 1 轮**：META / 扩展 / 结构计数 / 库大小 / 关键参数 / 活动 / 连接 / 等待 / checkpoint |
| `.db-audit/out/inst252-r2.txt` | 252 实测第 2 轮：容量 Top 20（`ursm_node_snapshot_min` 10 GB 等） |
| `.db-audit/out/inst252-slowsql.txt` | **252 实测第 4 轮**：慢 SQL Top 15（总耗时/均值/调用数）、临时落盘、零扫描索引、膨胀、DML churn、TOAST 压缩、全表扫热点 |
| `.db-audit/out/diff_34_vs_252.md` | `instdiff` 生成的双实例对比 |
| `sql/audit/2026-10-02-db-audit-252-round2.sql` / `-round3.sql` / `-slowsql.sql` | 252 分轮采集脚本（后两轮带踩坑注释，勿改回被否定的写法） |
| `.db-audit/d252async.ps1` | **252 异步采集通道**（`submit`/`status`/`fetch`/`cleanup`），本轮实测可用 |
