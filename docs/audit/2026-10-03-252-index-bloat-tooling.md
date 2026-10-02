# 索引膨胀治理工具：判据推导、实证与一次自我推翻

- 日期：2026-10-03
- 环境：252 (`115.29.212.252:25022`)，PG 容器 `pg-252-pg17`（PostgreSQL 17.10 + Citus）
- 交付物：`scripts/252-monitor/pg17-index-bloat.sh`、`scripts/252-monitor/pg17-index-bloat-selftest.sql`
- 修的既有缺陷：`scripts/252-monitor/pg17-vacuum-bloat.sh`
- 授权状态：**只读巡检已实跑；服务器 cron 未改；`--fix` 未启用**

## 0. 缘起

2026-10-02 手工做了两批 `REINDEX INDEX CONCURRENTLY`，25 个索引从 1,245 MB 压到 101 MB，
回收约 1,141 MB。当时用的判据是 `pgstatindex.deleted_pages`。结论写进了台账：
「索引膨胀是周期性问题，写入模式不变会重新长回来，建议纳入定期维护」。

本轮目标：把这件事固化成工具。开工时默认判据是对的，直接写脚本即可。
**结果不是这样 —— 判据本身错了两次，两次都是被自己造的对照样本推翻的。**

## 1. 附带发现：既有的周级 bloat 脚本从未成功执行过一次

`/opt/scripts/pg17-vacuum-bloat.sh`（2026-07-15 上线，周日 03:15 cron）每次都这么写：

```bash
docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SET statement_timeout='30min'; SET lock_timeout='5min'; VACUUM FULL ${tname}"
```

`psql -c` 传入多条语句时会包成一个隐式事务块，而 `VACUUM` 不能在事务块内运行。
`/var/log/pg17-vacuum-bloat.log` 里 2026-09-20 与 2026-09-27 两次运行逐字重复：

```
SET
SET
ERROR:  VACUUM cannot run inside a transaction block
[2026-09-27T03:15:02+08:00]   VACUUM FULL columnar_internal.chunk FAILED
[2026-09-27T03:15:02+08:00]   columnar_internal.chunk size=471MB -> 471MB
...
[2026-09-27T03:15:02+08:00] done
```

**自上线起每个周日 100% 失败，贡献为零。** 之所以长期无人发现：

- 退出码被 `|| echo FAILED` 吞掉，脚本整体仍以 0 退出；
- 日志结尾照打 `done`，读起来像一次正常运行；
- 没有告警通道（该脚本不调 `notify.sh`）。

无副作用证伪（`VACUUM` 而非 `VACUUM FULL`，同一形态）：

| 形态 | 结果 |
|---|---|
| A：`SET ...; SET ...; VACUUM pg_class` 同 `-c` | `ERROR: VACUUM cannot run inside a transaction block` |
| B：`PGOPTIONS` 设超时 + `VACUUM pg_class` 单语句 | `VACUUM` 成功 |

同仓库的应用层代码早已踩过并解决了这个坑 ——
`admin/data_lifecycle_storage.go:750` 明写「VACUUM/REINDEX cannot run inside a tx block,
so use a session-level SET on this dedicated connection」。**同一个陷阱在 Go 侧被记住了，
在 shell 侧漏了两个月。**

## 2. 判据的第一次自我推翻：`deleted_pages` 有真实盲区

写完脚本后，我造了一个 30 万行删 90% 的样本库做阳性对照。结果：

```
--- 删除 90% 后 ---
 deleted_pages | leaf_fragmentation | size_mb
---------------+--------------------+---------
             0 |                  0 |     2.8
--- 堆侧对照 ---
 n_live_tup | n_dead_tup | dead_pct
------------+------------+----------
      30000 |     270000 |     90.0
```

堆侧明明白白 90% 死元组，索引侧 `deleted_pages` 却是 0。

第一反应是 autovacuum 已经把索引清干净了，于是做了隔离变量实验
（两份完全相同的数据，只改 `autovacuum_enabled`）：

| 变体 | deleted_pages | leaf_fragmentation | size_mb |
|---|---|---|---|
| autovacuum 关 | 0 | 0 | 2.8 |
| autovacuum 开 | 0 | 0 | 2.8 |

**假设被证伪。** 变量不是 autovacuum。决定性实验是直接 REINDEX 看回收量：

```
=== 回收前 ===  av_off 2.79MB | av_on 2.79MB
REINDEX
=== 回收后 ===  av_off 0.30MB | av_on 2.79MB（未动）
```

**REINDEX 回收 89%（9.4×），而 `deleted_pages` 全程为 0。**
换句话说：只用 `deleted_pages` 判据，这类膨胀完全看不见。

逐列对比找出了真正的区分项：

| 索引 | deleted_pages | leaf_fragmentation | **avg_leaf_density** |
|---|---|---|---|
| av_off（已重建） | 0 | 0 | **92.52** |
| av_on（未重建） | 0 | 0 | **33.94** |

`deleted_pages` 数的是**完全空页**。零散 churn（随机更新/删除）之后每页都还剩几条存活，
没有一页会全空，于是这个指标恒为 0。而 `avg_leaf_density` 才是密度。
`leaf_fragmentation` 在两种状态下都是 0，不是密度，不能当判据用 ——
本项目 2026-10-02 的分析里曾把它当密度列读过。

## 3. 判据的第二次自我推翻：修正后的公式反而漏掉了最大的那个

加上密度信号后跑生产，`request_state_transitions_pkey` 判成了 `ok`：

```
ok  request_state_transitions_pkey size=94MB dead_pages=9572 density=90.08%
```

**这正是 2026-10-02 亲手量出「74.8 MB 死页」的那个索引。** 我的新判据把它判成健康。

原因在 headroom 公式。原先写的是 `size × (1 - density/100)`，即只按密度估。
这个索引的形态是「空页占大头、活页排得很紧」：密度 90% 说明**剩下的**页很紧凑，
但被密度项完全忽略了。实测 `leaf_pages = 2469` 而 `deleted_pages = 9572`：

| 公式 | 结果 |
|---|---|
| `size × (1 - density/100)`（旧） | 9.3 MB → 低于门槛，误判 ok |
| `deleted_pages + leaf_pages×(1-density/100)`（新） | **76.7 MB** |

差 8 倍。两种浪费形态必须分开量再相加。

公式在地面真值上的校准（保守下界，方向安全）：

| 样本 | 新公式估计 | 实测回收 |
|---|---|---|
| av_on_pad（未重建） | 1.76 MB | **2.49 MB** |
| av_off_pad（已重建） | 0.02 MB | — |

**两次自我推翻的共同点**：单看生产数据永远发现不了。`deleted_pages` 在生产的
retention 型索引上工作得很好（所以 10-02 那天它是够用的）；盲区和误报都出现在
合成样本暴露的形态上。判据在没有对照样本时看起来总是合理的。

## 4. 工具设计中被判据本身逼出来的两条约束

### 4.1 报告型判据必须能区分「没有」与「没测到」

`pgstatindex` 逐页读，>1 GB 索引实测数分钟。若某个索引量不出来，
它不能被静默并入「不膨胀」——那会让报告在一个残缺的取样面上宣称健康。

实现：逐索引 `statement_timeout`（`PROBE_TIMEOUT`，默认 180s），
超时/报错计入 `unmeasured` 并在汇总行显式出现；`measured == 0` 时 `exit 2` 而非输出结论。

### 4.2 对照断言：先证明量具可用，再谈结论

`pgstatindex` 依赖 `pgstattuple` 扩展。扩展缺失时，若不做任何检查，
「0 个膨胀索引」在「真的没有」与「量具根本跑不通」之间不可区分。

实现：取最小的非空 btree 索引当探针，要求 `pgstatindex` 返回结构完整的记录，否则 `exit 2`。

**这个断言在首次实跑时立刻红了一次**，抓到的还是真东西：
最小索引是空 TOAST 索引，`avg_leaf_density` 返回 `NaN`（无叶页时密度无定义），
被正则判成量具故障。`NaN` 是合法返回值，探针要证明的是「能跑通且列结构完整」，
不是「密度必须是个数」。断言本身过严，已放宽为接受 `NaN`。

另一个在阴性对照里抓到的真缺陷：`de()` 让 psql 的退出码冒泡，
`x=$(de ...)` 在 `set -e` 下会直接掐死脚本 —— 日志停在 `start`、退出码 1、零诊断。
错误文本本来已经并进 stdout 了，却因为退出码先被掐死，运维只会看到「脚本挂了」
而看不到「量具不可用」。修法是 `de()` 末尾 `|| true`，让错误作为取值流到显式检查。

## 5. 验证矩阵

| # | 场景 | 期望 | 实测 |
|---|---|---|---|
| V1 | 地面真值：已重建样本（密度 92.52） | 判 `ok` | ✅ `ok headroom≈0MB` |
| V2 | 地面真值：churn 型样本（密度 33.94 / 9.27） | 判 `CANDIDATE` | ✅ 全部命中 |
| V3 | 目标库无 `pgstattuple` 扩展 | `exit 2` + 指名原因 | ✅ `exit 2`，诊断含 `function pgstatindex(unknown) does not exist` |
| V4 | 候选集查询失败 | `exit 2`，不得当成 0 个索引 | ✅ 显式 ERROR 分支 |
| V5 | 生产只读巡检 | 出报告，不改数据 | ✅ 44 索引全测成 `unmeasured=0` |
| V6 | 首批 `--fix` 后复测 4 个索引 | 已修的回落、未修的仍命中 | ✅ 3/4 回落；`ursm_pkey` 仍高 → 触发 §6c 的第三次推翻 |

V1/V2 用的是同一份数据造出的两个索引，只让「是否 REINDEX 过」这一个变量不同。
夹具固化为 `scripts/252-monitor/pg17-index-bloat-selftest.sql`，可复现
（须在 `DELETE` 后显式 `VACUUM`，理由见 §6c）。

## 6. 生产巡检结果

`llm_gateway` 库，public schema，≥16 MB 的有效 btree 索引共 44 个，全部测成
（`unmeasured=0`）。库内 btree 索引合计 6,819 MB。

**判据修正的量化价值**（同一批 44 个索引，两套判据各跑一次）：

| 判据 | 命中 | 潜在回收 |
|---|---|---|
| 仅 `deleted_pages`（2026-10-02 口径） | 8 | 363 MB |
| 三信号 + 修正 headroom（本轮） | **13** | **728 MB** |

漏掉的 5 个里有 `request_state_transitions_pkey`（76 MB）——即 10-02 亲手量出
「74.8 MB 死页」的那个索引，被密度法判成了健康。**实验室里的自我推翻，
在生产上正好等于 2 倍的回收量。**

### 候选清单（按潜在回收排序）

| 索引 | 当前 | headroom | 命中信号 |
|---|---|---|---|
| `ursm_node_snapshot_min_pkey` | 1,230 MB | 132 MB | 累计（leaf 155,932 页 × 10.9% 空隙） |
| `uq_route_incident_events_idem` | 222 MB | 91 MB | density 58.2% |
| `request_state_transitions_pkey` | 94 MB | 76 MB | 空页 9,571（74 MB） |
| `idx_state_transitions_created` | 93 MB | 75 MB | 空页 9,463（73 MB） |
| `idx_route_incident_events_type_created` | 111 MB | 54 MB | density 50.8% |
| `idx_session_summaries_archival` | 65 MB | 45 MB | 空页 3,507 + density 51.5% |
| `idx_rca_session_turn` | 49 MB | 44 MB | 空页 2,192 + density 14.4% |
| `idx_session_summaries_status_time` | 61 MB | 40 MB | density 33.8% |
| `idx_route_incident_events_incident_created` | 85 MB | 38 MB | density 54.4% |
| `request_stats_error_drill_minute_pkey` | 92 MB | 34 MB | 累计 |
| `request_context_attrs_pkey` | 39 MB | 34 MB | density 12.2% |
| `idx_session_summaries_cost` | 49 MB | 33 MB | density 46.6% |
| `provider_metrics_minute_..._buc_key` | 60 MB | 32 MB | density 45.5% |
| **合计** | | **≈ 728 MB** | |

### 对既有待决事项的影响：`request_state_transitions_pkey` 有了第三个选项

该索引 94 MB、其中 9,571 个页完全为空。此前摆在台账上的选择是
「删掉它（`ALTER TABLE ... DROP CONSTRAINT`，94 MB）」或「不动」。

本轮数据提供了第三条路：**REINDEX 把它压到约 18 MB，回收 76 MB，
且不损失主键/唯一约束。** 三者对比：

| 方案 | 回收 | 代价 |
|---|---|---|
| DROP CONSTRAINT | 94 MB | 结构性变更，失去约束；需确认无代码依赖 |
| REINDEX CONCURRENTLY | 76 MB | 无语义变化，索引重建期间不阻塞读写 |
| 不动 | 0 | 每周约 76 MB 反复长回来 |

**倾向 REINDEX 而非 DROP** —— 回收 81% 的空间且不承担结构性变更风险。
但仍需用户拍板，因为它会改变该表的约束形态这一长期性质（若最终选 DROP），
且属生产变更。

### 跑批过程的一处自我纠错

第一轮跑批被我 `task_stop` 中断，但被杀的是本地 ssh 包装进程，**远端脚本仍在跑**；
它在我归档旧日志之后继续写入，于是新旧两轮结果混进了同一个文件。
两轮的时间戳在各自内部固定（`ts` 在脚本启动时取一次），因此可按时间戳分离：
`00:34:01` 为旧判据，`00:38:52` 为新判据。上表已按此分离后统计。
**本节数字若不分離直接汇总，会得到一个不存在的 21 个命中 / 1,091 MB。**

## 6b. 首批 `--fix` 实执行（4 个索引，用户选定批次）

`MAX_FIX_PER_RUN=4 ... pg17-index-bloat.sh --fix`，逐索引 before/after 记录在日志中。

| 索引 | 重建前 | 重建后 | 实收 | 预估 headroom | 预估/实收 |
|---|---|---|---|---|---|
| `ursm_node_snapshot_min_pkey` | 1,230 MB | 1,184 MB | 46 MB | 132 MB | **2.87×** |
| `uq_route_incident_events_idem` | 222 MB | 142 MB | 80 MB | 91 MB | 1.14× |
| `request_state_transitions_pkey` | 94 MB | 17 MB | 77 MB | 76 MB | 0.99× |
| `idx_state_transitions_created` | 93 MB | 17 MB | 76 MB | 75 MB | 0.99× |
| **合计** | | | **277 MB** | 374 MB | |

库 22.75 GB → 22.49 GB；磁盘 83G/197G = 44%，可用 107 GB。

### headroom 预估的适用边界（本次新增的校准）

`deleted_pages + leaf_pages×(1-density/100)` **在 retention 批量删除型索引上极准**（0.99×），
因为预估模型与实际机制同构：全空页 REINDEX 直接丢弃。

但在**零散 churn 型大表上严重高估**：`ursm_node_snapshot_min_pkey` 估 132 MB、实收 46 MB（2.87×）。
该表 1,987 万行、89.14% 密度，未用空间分散在 15.6 万个叶页里；
REINDEX 只能合并相邻键范围的页，**不能重排行序**，所以回收远小于「密度×体积」的直算。

⇒ **结论：headroom 是上界，不是期望值。它可用于排序（相对大小有意义），不能当收益承诺。**
脚本注释与日志措辞（`≈`）已按此收紧。

### 验证

- 4/4 `indisvalid=t`、`indisready=t`
- 锁等待 0
- 重建后复测 `request_state_transitions_pkey`：`deleted_pages` 9,571 → **0**，密度 90.08
- 写入面全程持续推进（REINDEX 期间及之后）：
  `request_logs_hot` / `request_state_transitions` 最新写入 01:44:55，
  `ursm_node_snapshot_min` 01:42:58

### 两处过程中的误判（如实记录）

1. **`request_logs` 近 10 分钟 0 条，一度以为写入面停了。** 实为查错了：该表时间列是 `ts`
   不是 `created_at`，且 `request_logs`（父表/历史分区）与 `request_logs_hot` 是两套。
   真正活跃的是 `_hot`，其最新写入就在查询当时。
   *判「写入面是否活着」必须逐表确认时间列名，不能凭表名猜。*
2. **252 本机 `llmgo-252-dev.service`(:8780) readyz 返回 503 `{"redis":null}`。**
   与本轮无关：该服务 10-01 05:19 启动至今 `NRestarts=0`，`.env.dev` 中 redis 配置项数为 0
   （dev 实例本就未接 redis），503 是其常态。

### 网关健康复核（订正上文「未验证项」）

初稿写「`.209`/`.241` 两台 canary 的 readyz 未能复核」——**该结论本身是错的**。
`115.29.212.209/.241` 并非 canary 地址，与 252 不同网段，本就不该从那里连。
真实拓扑是：网关生产在 **154**（`47.97.111.154`），canary 经 netbird 内网
`172.16.2.209 / .241 / .210` 访问 252 的 PG —— `docs/audit/2026-09-21-252-deploy-verification.md:11`
早有记载，当时未查而凭记忆填写。

直接复核 154（非推断）：

| 项 | 值 |
|---|---|
| `llm-gateway-go-canary@8782.service` | `active running` |
| readyz | `200` `{"database":{"connected":true,"latency":"21.25ms"},"redis":{"connected":true,"latency":"23.60ms"},"status":"ready"}` |
| PID / 启动时间 | 24690 / 2026-10-02 17:30:48，`NRestarts=0` |

该进程自 10-02 17:30 起未重启，**跨本轮 REINDEX（10-03 01:15–01:40）无重启**。

252 侧三台客户端均在线且有活跃查询：

| client_addr | 连接数 | idle / active |
|---|---|---|
| `172.16.2.241` | 31 | 29 / 2 |
| `172.16.2.209` | 22 | 20 / 2 |
| `172.16.2.210` | 17 | 12 / 4 |

最老后端 `backend_start = 2026-09-29 09:37`，即连接池跨本轮 REINDEX 存续、未重建；
近 15 分钟 `request_logs_hot` 写入 139 条。

⇒ REINDEX 无副作用。**教训**：这台机器上「哪台 host 提供什么」的地址我记错过一次，
凡涉及跨机结论必须先查仓库里的既有文档，不凭记忆。

## 6c. 判据的第三次推翻：`avg_leaf_density` 的语义被我读错了

复测「刚 REINDEX 完的 4 个」时发现异常：

| 索引 | 重建前 headroom | 重建后 headroom | 密度变化 |
|---|---|---|---|
| `rst_pkey` | 76 MB | 1.7 MB | 90.08 → 90.06 |
| `rst_created` | 75 MB | 1.7 MB | 90.08 → 90.06 |
| `route_idem` | 91 MB | 13.2 MB | 58.20 → 90.54 |
| `ursm_pkey` | 132 MB | **115 MB** | 89.14 → 90.20 |

前三个正确回落（判据无假阳性）。但 `ursm_pkey` 重建后仍报 115 MB——按当前逻辑它会
**永远留在候选名单里**，每个周日被重复选中、每次只回收一点。

我据此加了一个「密度 ≥ 88 视为已达重建上限」的豁免分支（理由：REINDEX 只能合并相邻
键范围的页、不能重排行序，1987 万行索引重建后 ~90% 就是可达密度）。**这个分支已撤除。**

### 撤除的理由：密度不是「存活元组占比」

为构造地面真值而重跑夹具时，密度两次给出不同结果（33.94 vs 92.26），同样的表结构、
同样的操作。第一次归因于「autovacuum 替 REINDEX 干了活」——**这个解释也是错的**：

| 变体 | leaf_pages | avg_leaf_density |
|---|---|---|
| `av_on`（autovacuum 默认开） | 341 | **33.94** |
| `floor_sparse`（`autovacuum_enabled=false`） | 341 | **92.26** |

关掉 autovacuum 反而让密度变高，与「autovacuum 回收死页」正好相反。

决定性实验——对同一索引跑一次 `VACUUM`，其他什么都不动：

```
before VACUUM   deleted_pages=0  leaf_pages=341  avg_leaf_density=92.26
     (VACUUM)
after  VACUUM   deleted_pages=0  leaf_pages=341  avg_leaf_density=33.94
```

⇒ **`pgstatindex.avg_leaf_density` 度量的是页内已用字节占比。死元组的行指针在被
vacuum 之前一直占位、计为「已用」。** 我把「页内占位」误读成了「页排得满」。

这个误读直接污染了两个结论：

1. 「重建后密度 90.20% ⇒ 已达可达密度」——不成立，那个 90.20% 可能只是没被 vacuum 过。
2. 由此设的 `REBUILD_FLOOR_DENSITY=88` 阈值没有依据，连带 `at_rebuild_floor` 计数
   与 `floor-selftest.sql` 夹具一并撤除/删除。

**正面用途不变**：`avg_leaf_density` 确实能区分 churn 型膨胀（33.94 vs 重建后 92.52），
前提是样本/索引已被 vacuum 过。生产库由 autovacuum 常态维护，该前提成立。

### 夹具的连带修正

`pg17-index-bloat-selftest.sql` 原本在 `DELETE` 之前 VACUUM、之后不 VACUUM，
因此密度依赖运气、两次跑可能不同。已在 `DELETE` 之后补 `VACUUM (ANALYZE)`，
并把上述决定性实验写进文件头。修正后连跑稳定复现 33.94 / 92.52。

原本为豁免分支写的 `pg17-index-bloat-floor-selftest.sql` 中，第三个样本
（想造出「密度高但有全空页」的边界形态）反复三次没造对：
`WHERE id > 30000` 删高位 90% → `deleted_pages` 仍为 0；`TRUNCATE` 后重插再删 →
删了个寂寞（`n_live_tup/n_dead_tup = 300000/300000`）。**留一个造不出来的夹具
比没有更糟**，该文件已删除。

### 这一段的教训

三次推翻里有两次（`deleted_pages` 盲区、密度语义）**都不是读代码看出来的，是被
「让这个判据证明自己有效」逼出来的**。而在追查过程中我连续给出两个错误解释
（autovacuum 回收了、autovacuum 开着才会假高），都是**看到结果对不上就立刻找一个
说得通的机制**，而不是先做隔离变量实验。第三次才做出决定性实验（一行 `VACUUM`），
一次就把范围锁死。

⇒ 判据类工作里，「结果不稳定」本身是最高价值的信号：它说明量具有隐藏前提，
而隐藏前提正是最容易写进代码、也最难被发现的东西。

## 7. 部署状态与待决事项

- 服务器已放 `/opt/scripts/pg17-index-bloat.sh`（755），**只跑过 `--report`**。
- `/etc/cron.d/pg17` 的仓库模板已加周日 04:15 条目，**服务器上的 cron 文件未改**。
- cron 默认 `--report`（只读 + 超阈值告警），`--fix` 需显式启用。
- `pg17-vacuum-bloat.sh` 的修复**只在仓库，未部署**（用户已定：先不部署）。

## 8. 818 部署：被共享工作区的迁移注册表阻塞（未执行）

用户已授权部署 818。**执行前核查发现不能安全部署，故停下。**

### 事实

迁移的实际执行顺序由 `sql/schema/installed_startup_migrations.tsv` 决定
（索引 = 执行序号，值 = 文件名）。该表当前状态：

| 来源 | 注册情况 |
|---|---|
| `HEAD`（a3468d85e） | 末尾为 `202/813`、`203/814`、`204/815` —— **无 818、无 819** |
| 工作区（未提交） | `200/818`、`201/819` —— **但 813~817 全部丢失** |

`git log -S "818_ursm_snapshot_typed_columns.sql" -- ...tsv` 无任何结果：
**818 在 tsv 里的注册从未进入任何提交**，只存在于脏工作区。

818 在 `installer/cmd/llm-gw-installer/main.go`（go:embed）与
`installer/internal/dbinit/runner.go`（StartupFiles）中的注册**是已提交的**，
缺的是 tsv 这一环。

### 为什么不能直接构建部署

| 构建来源 | 后果 |
|---|---|
| `HEAD` | 818 **不会执行** —— 部署了等于没部署，字段拆分没生效 |
| 当前工作区 | 818 与 819 会执行，但 **813~817 全部被跳过** |

两条路都不对。要把 818 的 tsv 注册提交上去，就必须同时提交
并行会话正在做的那次**未完成的 tsv 重编号**（它的 20~39 行都被改动，且丢了 813-817）。
这不是我该单方面吞掉的东西。

### 为什么会撞上

回看提交历史可见成因：

- `597f65032`（我的 818 五点同步）**删除了** `517`、`527`、`813`、`814`
  四个迁移文件 —— 共享工作区里撞掉了并行会话正在写的文件；
- 并行会话随后用 `d5932d26c`（注册 813/814）、`f7f1c97f4`（接上 815）把它们补回；
- 816/817 目前既无注册、也不在 main.go/runner.go 中，对话另一侧仍在推进。

⇒ **教训**：五点同步里的 tsv 那一点当时被我跳过了（`597f65032` 未碰 tsv），
而 tsv 恰恰是「漏了则永不执行」的那一环。我当时甚至把这条写进了 commit message，
却没在同一个提交里满足它。

### 需要什么才能解

1. 并行会话完成 813~817 的 tsv 注册，让 tsv 与文件集一致；
2. 在那个干净的基线上，把 818 的注册作为**独立小提交**补上；
3. 重跑 `TestInstallerMigrationsRegistered` 转绿后再部署。

在 1 完成前，818 保持「代码在仓库、未上生产」的状态。

---

# 补记（2026-10-03 06:40）：第二批 REINDEX 的有效性核验是一条**恒假判据**

## 1. 事件

第二批 9 个 `REINDEX INDEX CONCURRENTLY` 执行完（9 成功 / 0 失败 / 回收 341 MB），
脚本末尾有一道有效性核验：

```bash
v=$(q "SELECT indisvalid::text||'/'||indisready::text FROM pg_index WHERE indexrelid='$i'::regclass")
[ "$v" = "t/t" ] || printf "  !! %s -> %s\n" "$i" "$v"
```

判据里把**两个不同的渲染形态**混为一谈了。地面真值：

```sql
SELECT (true)::text, (true), (true)::text||'/'||(true)::text;
--    true    |  t  |        true/true
```

- `::text` 渲染：`true` / `false`
- psql 默认展示：`t` / `f`

脚本里 `::text` 拿到的是 `true/true`，与字面量 `t/t` **永不相等**
⇒ 这条检查**恒为假**，且是「健康与失效都触发」的那种恒假。

| 索引实际状态 | `$v` | `[ "$v" = "t/t" ]` | 脚本输出 |
|---|---|---|---|
| 健康 | `true/true` | 假 | `!!` |
| **失效** | `false/true` | 假 | `!!` |

⇒ **它无法区分健康与失效**。真出现一个 INVALID 索引时，它的输出与这 9 行噪声完全同形。

## 2. 为什么结论还是对的

上一轮记的是「索引实际值显示全部 `true/true`（健康）」—— 那是**逐行读了值**得出的，
不是脚本判出来的。本轮用地面真值补做了独立复核：

```sql
SELECT count(*) FROM pg_index WHERE NOT indisvalid OR NOT indisready;  -- → 0
```

全库 0 个无效/未就绪索引，逐个点名确认 9 个全为 `true/true`。
**结论成立，但成立的原因是补了测量，不是原判据。**

## 3. 修法

```bash
bad=0
for i in "${IDXES[@]}"; do
  v=$(q "SELECT indisvalid::text||'/'||indisready::text ...")
  if [ "$v" != "true/true" ]; then printf "  !! %s -> %s\n" "$i" "$v"; bad=$((bad+1)); fi
done
echo "  异常 ${bad} 个（0 = 全部 valid/ready）"
[ "$bad" -eq 0 ] || exit 1
```

两处改动：**比对字面量改成 `true/true`**；**只打日志改为计数 + 非零即 exit 1**
（人不去读日志的检查等于没有）。

## 4. 判据能不能区分合格与不合格 —— 必须自己构造反面输入

引用任何「核验通过」之前，先把 expected 改成一个反面值看它是否变红。
本次：把 `!= "true/true"` 改成 `!= "false/true"`，9 行 `!!` 全部出现 ⇒ 判据有鉴别力。

**教训形态**：这类缺陷不是「漏报」，是「**假装在报**」——
脚本确实吐了 9 行 `!!`，与真故障时的输出同形，于是永远不会被当真故障发现。
凡是「断言条件恒定」的检查，都要先问它能否区分两个方向。

## 5. 顺带发现：仓库脚本把无效索引**静默排除**在候选之外

`scripts/252-monitor/pg17-index-bloat.sh:141` 的候选查询带 `AND i.indisvalid`，
这是正确的 SQL 布尔谓词（不是上面那种字符串比对），但副作用是：

> INVALID 索引不会出现在报告的候选里，且**没有「因无效而跳过」的计数**。

与本文件 §「报告型判据必须区分『没有』与『没测到』」同族：
当前全库 INVALID 数为 0，所以这是**潜在缺口而非活跃故障**。
待办：给候选查询加一个 `skipped_invalid` 计数并纳入 `unmeasured` 口径。
