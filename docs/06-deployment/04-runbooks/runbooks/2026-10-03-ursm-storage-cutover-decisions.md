# 2026-10-03 URSM 存储与切换待决事项

> 配套：`ursm-v2-cutover.md`（分阶段契约）、`scripts/ursm-redis-db-migrate.sh`（迁键脚本，可执行）
> 台账：252 `/root/252-storage-opt-ledger-2026-10-02.md`（1879 行）
>
> 本文只写**已实测**的数字与**已就绪**的命令。所有待决项均未执行。

## 0. 本轮已交付（无需再决策）

| 项 | 结果 | 证据 |
|---|---|---|
| 154 payload 瘦身 | `2427-b7f371e5` verified | 行宽 291.0 → 213.4 B（-26.7%）；单批 1157 行 0 键 + 136 行 8 键 |
| 245 payload 瘦身 | `2434-e1a8816a` verified | 胖行 322/批 → **0**；15 分钟窗内两台 fat_rows 均为 0 |
| 252 cron 周日巡检 | 已加 `40 4 * * 0` | 备份 `/etc/cron.d/pg17.bak.20261003-162457`，原有 5 条任务零改动 |
| cron 模板漂移修复 | `b9365c215` | 模板曾漏一条生产任务且删掉全部 `llmgw-source`，整文件覆盖即出事 |
| 迁键脚本 + 守卫盲区 | `2602b2dc4` | 脚本只读预检可用；守卫枚举面补上未跟踪文件（M1 红 / M2 绿） |

**8 个合法键全程保留、无误剥**——`successes_*` / `empty_responses_*` / `last_request_failed` /
`empty_response_rate_5m` 都不在排除表里，走未知键前向兼容路径保留。

## 1. 存储账 —— ⚠️ 下面这张表的「合计」列已撤回，见 §1.1

当前 `ursm_node_snapshot_min`：18,719,448 行 / 堆 9129 MB / 索引 1362 MB = **10 GB**。
7 天留存精确（最老 `09-26 20:37:28`，最新 `10-03 20:55:17`，跨 7 天 00:17:49）。

| 状态 | 行数 | **活数据**（行宽 × 行数） | 堆文件（实测高水位） |
|---|---|---|---|
| 现在 | 18.7 M | ~5.4 GB（全瘦行） | **9,129 MB** |
| 7 天后（双写、全瘦行） | 25.3 M | ~5.4 GB | **≈9,129 MB（不再增长）** |
| 7 天后（245 退 shadow，单写） | 12.7 M | ~2.7 GB | **≈9,129 MB（不再增长）** |

行数按实测 2,511 行/分（154 1,255.5 + 245 1,255.6，各 60s 一批）推算。

### 1.1 ★ 撤回：「7 天后降到 6 GB / 3.5 GB」是错的

**磁盘空间不会还给文件系统。** 经验证据（台账同日两次测量）：

| | 08:00 | 14:29 | 变化 |
|---|---|---|---|
| 行数 | 19,877,268 | 18,719,448 | **−115.8 万** |
| 堆 | 9,054 MB | 9,129 MB | **+75 MB** |

全天堆轨迹：**8,768 → 9,054 → 9,129 MB，单调增长、从未缩小**。
行在持续删除，堆文件却一分空间都没归还。

PostgreSQL 只有 **VACUUM 截断尾部空页** 才会缩小关系文件；本表由留存 worker
**从最老往最新删**（`retention.go:181`），释放的空间与新行插入位置交错，
尾部不存在成片的可截断空页 ⇒ 文件停在高水位。

> **诚实标注**：上面「释放空间与插入位置交错」是**机制推断，不是实测**。
> 我试图用 ctid 验证「最老在堆头、最新在堆尾」，但那个探针**无效** ——
> `order by snapshot_ts desc limit 1` 走 pkey 索引，而同一 snapshot_ts 有
> ~1,255 行，索引顺序 ≠ 物理顺序，返回的是其中任意一行。
> 要确证需要 `pgstattuple` 全表扫描（9 GB），**我没有跑** —— 那正是我今天
> 已经犯错三次的那类查询。
> 但「文件从未归还空间」这个**经验事实**不依赖机制推断，它由上面两次实测
> 与全天轨迹直接支撑。

**因此，瘦身与减写入方的真实收益是**：
1. 表**不再增长**（之前按 25.3M 行 × 453 B 会继续涨到 11 GB 以上）；
2. 关系内部出现约 4~6 GB 的可复用空间，供未来写入吸收；
3. **磁盘占用维持在 ~9.1 GB 高水位，不下降。**

**若确实要归还磁盘**，只有两条路（都需授权，且都很贵）：
- `VACUUM FULL`：重写 9 GB、需等量临时空间、期间独占锁 —— 在这张 10 GB
  的活跃表上等于一次停机；
- `pg_repack`：在线，但需要扩展 + 等价于表大小的额外空间。

**结论**：在当前留存策略下，**不要承诺任何「表变小」的收益**。本轮瘦身
的真实价值是止住增长 + 减少每行字节，而非释放已占用的磁盘。

---

## 2. 待决项 ①：迁键 + 启用 `URSM_V2_REDIS_DB`

**问题**：URSM v2 节点键混在网关会话键的 db2（约 158 万键）里。persist writer 每分钟
`SCAN <prefix>node:*` 采集快照；Redis 的 MATCH 是服务端过滤、游标遍历躲不掉，必须走完整个
键空间。实测 12.95~30.40s，压在 writer 的 30s 预算上 →
`ursm.v2: persist collect failed: redis scan failed: context deadline exceeded` 成为常态
（154 旧实例 4 小时 91 次）。

**已查实的前提**
- 目标 db 可用：db3~db8、db10~db14 全部 0 键（db15 有 4 个陈旧 URSM 残留，脚本会拒绝）
- `32a94439b` 的 `URSM_V2_REDIS_DB` 功能在当前 main 完好，默认 `-1` = 沿用共享 client，行为不变
- 迁键脚本 `scripts/ursm-redis-db-migrate.sh` 已就绪

**★ 关键约束（我此前说错过，此处为准）**
迁键**不能只停 persist writer**。`ursm:v2:node:*` 由请求路径每请求写一次：

```
domains/streaming/executors/executor_nodehealth.go:313
  → Manager.RecordRequest → store.RecordRequestKeySet → record_request.lua
```

停 writer 只停「读」不停「写」；只要还有请求进来就继续写旧 db，切到新 db 后这段窗口的
更新就丢了。而 URSM 靠 coverage manifest 重建只覆盖 714/1240 节点（缺 563 个旧文法节点），
不能靠重启自愈 ⇒ **必须流量静默窗口**。

**执行步骤**
```bash
# 1) 只读预检（任意时刻可跑，只用 O(1) 的 DBSIZE）
bash scripts/ursm-redis-db-migrate.sh --plan 172.16.2.210:6389 -s 2 -d 14

# 2) 静默窗口内（需停写入方网关，使请求不再落到旧 db）
bash scripts/ursm-redis-db-migrate.sh --copy 172.16.2.210:6389 -s 2 -d 14

# 3) 校验通过后，154 与 245 的 env 都要设（只设一台会造成两台节点集不一致）
URSM_V2_REDIS_DB=14

# 4) 启动网关，确认 persist committed 的 rows 回到 ~1,255/批，且 collect failed 消失
```
**回滚**：`URSM_V2_REDIS_DB` 置回未设置并重启。旧 db 数据原样保留。
**风险**：静默窗口内网关不可用；脚本在两侧前缀键数不一致时非零退出并明确拒绝提示设 env。

**替代路线（已否决）**：改用 coverage manifest 代替 SCAN（实测 0.052s，快 580×），但清单由
`bootstrap.go` 的 `readProbeRows` 从 PostgreSQL `node_probe_state` 生成、不从 Redis 枚举，
补全就得反向遍历 Redis —— 回到同一个死结。

---

## 3. 待决项 ②：245 shadow 的第 7 天结论

**这是 CPU/写放大的大头**（不是磁盘）：245 退出 shadow 后写入方从 2 个变 1 个，
每分钟少 ~1,255 次 INSERT、活数据从 5.4 GB 降到 2.7 GB。
但按 §1.1，**磁盘占用不会因此下降**，只是表的增长压力减半。

真正的理由不是省磁盘，而是门禁证据本身：

**门禁实测**（245 `:8781/metrics`，只读）

```
llm_gateway_ursm_v2_shadow_records_total{result="recorded"}   40
llm_gateway_ursm_v2_shadow_records_total{result="failed"}      0   ← 硬门禁 ✓
ursm_shadow_diff_total{type="identical"}                       0   ← 门禁要求 ≥10000 ✗
ursm_shadow_diff_total{type="sampled_out"}                    53
ursm_shadow_diff_total{type=~"availability|order|top1|not_ready|error"}  全 0  ✓
```

**§8 的 `identical >= 10000` 门禁在当前配置下 7 天内不可达**：
245 未设 `URSM_V2_SHADOW_SAMPLE_RATE`，走默认 0.01（`config.go:138`）；`sampled_out` 在
`router.go:771` 产生。两点测量速率 **2.5 ± 0.3 次 diff 调用/分** ⇒ 7 天 25,200 次 × 1%
= **expected identical ≈ 252**，缺口 **40×**（达标需 99.2 次/分）。
这是判据阈值与「默认采样率 × 当前流量」差 40 倍，**不是采数不够**。

**两种读法都指向"证据不足"**：
- 按 §8 失败语义列（`sampled_out>0` 即 fail-closed 回 `off`）→ 已满足回退条件
- 按 §8 PromQL 模板（`identical>=10000`）→ 不能 GO canary

**但有个前置约束不能绕**：runbook §7 要求「154 仅在 245 记录完整且 local、dev、245 全部
通过后才可开始」。245 这条没过 ⇒ 154 切 canary 的前提不成立。这牵涉路由正确性，不属存储范围，
**该由你权衡，不是我能替你判的**。

**局限声明**：两点测量只跨 1.6 分钟（虽与 21.5 分钟长均值一致）。严格说结论是「当前速率下
不可达」，不是「任何情况下不可达」。真正一锤定音要等 8781 跑满几天。

---

## 4. 待决项 ③：autovacuum 调参

**现状**：`n_dead_tup = 991,731`（5.09%），`last_autovacuum = 13:33`（7.5h 前），
表级 reloptions 未设 ⇒ VACUUM 触发点 `0.1×18.5M+25 = 1,850,344`，余量 858,613。

**死元组是每小时突发，不是连续增长**（`retention.go:143` 是 `time.NewTicker(time.Hour)`）：
两点采样 21:01:57 与 21:07:03 的 `n_dead_tup` 完全相同，而 live 涨 13,732。
每批突发 ≈ 11.1 万（1,855 行/分 × 60 分）⇒ 锯齿在 0 ↔ 10% 之间，约 7.9 小时后下次 vacuum。

```sql
ALTER TABLE ursm_node_snapshot_min SET (
  autovacuum_vacuum_scale_factor = 0.01,
  autovacuum_vacuum_threshold   = 5000
);
```

- **收益**：触发点 1,850,344 → 190,025，匹配每小时 11.1 万的突发节奏；死元组均值
  5% → ~1.8%，**平均多回收约 290 MB、峰值少占约 1 GB**
- **代价**：本表 autovacuum 由约每 16 小时一次升为约每小时一次，每次对 9 GB 表做清理，I/O 上升
- `ALTER TABLE SET` 是元数据操作，不重写表、不长时间持锁
- **回滚**：同样 `ALTER TABLE ... RESET` 即可

**这是真实权衡，不是无脑优化。**

---

## 5. 待决项 ④：部署 `03be2baa9`（停 tx is closed 刷屏）

- **现象**：`/var/log/messages` 已 1.4 GB，`tx is closed` 累计 436.9 万行，约 6 MB/h
- **根因**：`withTenantTx` 的 defer 在 `return tx.Commit(ctx)` **之后**执行，事务已关闭，
  `tx.Exec` 必返 `pgx.ErrTxClosed` ⇒ **每次成功调用都打一条 WARN**
- **关键**：该 RESET 本就多余 —— 实际用 `set_config('app.current_tenant', $1, true)`，
  第三参 true 即事务级作用域，提交/回滚自动撤销
- **风险**：最低。纯删两处冗余 RESET + 改写腐化注释 + 加静态守卫
- **回滚**：切回旧 release

## 6. 待决项 ⑤：部署 `4a60d4337`（health 端到端断链）—— **带行为变更**

- **现状**：`health_status` 列生产 100% 为空（实测 1275 行 0 非空），
  `executor_nodehealth.go` 的 EffectUpdateURSM 分支没设 `HealthStatus`
- **修好后会怎样**：`store/probe_evidence.go:160` 的判定含
  `(health == "" || health == HealthStatusHealthy)`，因 health 恒为 "" **该子句一直是恒真的、
  这道健康检查从未生效**。修好后 degraded/suspect/quarantined 节点在 probe evidence 里会开始
  被判 `Healthy=false`
- **副作用**：该列存真实值后表会大 200~370 MB
- **回滚**：切回旧 release

## 7. 待决项 ⑥：252 跑 gateway 违反分阶段契约

runbook §7：「**252 不在任何阶段部署 gateway**」。但 `llmgo-252-dev.service` 正在跑
`/opt/llm-gateway-go/bin/gateway`（PID 879740，10-01 05:19 启动）。

- 它**没有 Redis 配置**（`/proc/879740/environ` 只有 `LLM_GATEWAY_DATABASE_URL`），
  走内存降级，**不写快照** ⇒ 不影响本轮存储优化
- 但与契约冲突。处理方式（停掉 / 保留并记录例外 / 其它）是运维决定，不影响存储
- **注意**：这正是我今天误判过的地方 —— 我曾据 `.env.dev` 没有 URSM 变量就断定「252 不写」，
  而 252 确实不写，**真正的问题是我整个漏了 245**。grep 空结果不等于「不存在」。

---

## 8. 部署基线的固定前置（今天踩了两次）

从生产基线 `1a213f4c`(154) 与 `bd389df9`(245) 部署**任何东西**之前，必须先带上
`58eedd489` + `0b9a00bfc`（纯 shell，零 Go 改动）。否则 `scripts/lib/node-pm.sh` 的
全角逗号 bug（`$pm` 紧跟 U+FF0C，`set -u` 吞掉变量名）会在 `web/node_modules` 缺失时
中止部署。154 和 245 各踩了一次才补上。

## 9. 本轮我的操作失误（已全部恢复，写此备查）

**在生产上 3 次低估大表查询成本**，3 次都对 `ursm_node_snapshot_min`（10 GB / 1871 万行）
做了全表扫描并阻塞锁：
1. `ORDER BY updated_at_ms DESC LIMIT 200` —— 该列无索引
2. `extract(second from snapshot_ts)` 未加时间边界
3. `where snapshot_ts >= now() - 5 days` 上算 `pg_column_size`（约 1300 万行堆访问）

三次均立即杀掉客户端进程，复查昂贵查询 0、锁等待 0、快照写入正常。
**这不是偶发手滑，是我在这张表上估查询代价的系统性缺陷。**
后续全部改为有界写法：pkey 索引取边界、`pg_relation_size` 取规模、`TABLESAMPLE` 取样本。

---

## 10. 【提案·需授权】把 `ursm_node_snapshot_min` 改成时间分区表

§1.1 说明「DELETE 型留存不会归还磁盘」。这是**架构问题**，不是调参问题。
本节是对目标「对整个数据及更新、查询等进行重构优化」的正面回答。

### 10.1 这是本仓库的既定模式，不是新发明

现网已有多张月度声明式分区表，且清理脚本走 `detach + DROP TABLE`
（`scripts/252-monitor/pg17-drop-old-columnar-partitions.sh:66`）——
**DROP 会让空间即时归还文件系统**，正是当前表做不到的。

```
auto_route_selections_2026_08/09/10/11 + _default
cache_metrics_2026_08/09/10/11 + _default
candidate_failure_logs_2026_09/10/11
credential_model_index_2026_09/10/11（另有 hash 子分区）
```

对照目标表：`relkind='r'`（普通表）、**0 个分区**、堆 9129 MB。

### 10.2 改造面（已核对源码）

| 组件 | 改动 |
|---|---|
| **writer INSERT** | **不用改**。`ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name)` 的冲突目标已含分区键 `snapshot_ts`（PK 首列），满足 PG 对分区表唯一索引的约束 |
| **retention worker** | 重写。`retention.go:220-254` 的游标 + 批量 DELETE + deadline 整套逻辑，替换为「找出上界早于 `now - retention` 的分区 → `DROP TABLE`」 |
| **索引** | `_pkey` 变分区父索引自动下推。**`_ts_idx` 可直接不建** —— 它当前被误选且有 14 倍读放大（详见 §10.5②） |
| **基线 schema** | `deploy/sql/schemas/baseline/01-schema.sql:18198`（CREATE TABLE）与 `:22634`（PK 约束）需同步，否则基线重建会退回非分区形态 |
| **迁移** | 建新父表 + 分区；**拷贝 1871 万行（约 5~9 GB 写入）**，或**从空表开始让留存自然填满**（会丢 7 天审计历史，属产品决策） |

### 10.3 分区粒度：建议**按日**，不是沿用月度

留存窗口只有 7 天，沿用月分区会有 3/4 的分区数据一出生就是死的。

按日分区（实测速率 2,511 行/分，双写）：

| 项 | 双写（当前） | 单写（245 退 shadow 后） |
|---|---|---|
| 行/天 | 3,614,400 | 1,807,200 |
| 堆/天 | ~771 MB | ~386 MB |
| 索引/天 | ~263 MB | ~132 MB |
| 7 天稳态 | **~7.2 GB** | **~3.6 GB** |
| 空间归还 | **每天 DROP 一个分区，即时归还** | 同左 |

对比现状：**9,129 MB 且永不归还**。单写情形下 3.6 GB 且每天自动回收。

> 索引/天按 72.8 B/行（实测 `1362 MB / 18.7M`）折算，**是估算不是实测**；
> 分区后父索引的膨胀特性可能不同。

### 10.4 附带收益

- **死元组趋近 0**：分区短命，不存在 11.1 万/小时的 DELETE 突发，
  §4 的 autovacuum 调参**随之变得不必要**（可从待决项里划掉）
- **按时间的查询自动分区裁剪**，不再全表扫 9 GB
- **留存从「每小时删几百万行」变成「每天 drop 一个分区」**——
  §4 记录的双份留存 worker 冗余也随之消失（不再需要按行删）

### 10.5 ★ 已验证：改造风险比预想的小得多

下面三项是提案时我标为「待确认」的假设，现已用只读手段查实（`pg_stat_*` 目录，
O(1)；`grep` 全仓源码）。统计区间自 2026-09-23 06:56 统计重置起，约 10.6 天。

**① 没有任何读路径 —— 这是只写不读的表**

全仓只有两处碰它（排除测试与注释）：

| 位置 | 操作 |
|---|---|
| `writer.go:383` | `INSERT ... ON CONFLICT DO NOTHING` |
| `retention.go:229/235` | 批 `SELECT` + `DELETE` |

**没有任何 `SELECT` 读它**。路由状态读的是 Redis，不是这张表；它是纯审计/快照落库。
⇒ **分区改造没有查询计划风险**，这消除了我原先列的第 4 条风险。
表级 `seq_scan = 30`（221,478,487 行）也印证读极少，那 30 次基本是运维临时查询。

**② `_ts_idx` 确实在被用，但存在 14 倍读放大**

| 索引 | idx_scan | idx_tup_read | idx_tup_fetch | 大小 |
|---|---|---|---|---|
| `ursm_node_snapshot_min_pkey` | 34,580,370 | 73,644,150 | 14,432,893 | 1215 MB |
| `ursm_node_snapshot_min_ts_idx` | **6,815** | **477,231,984** | 67,433,881 | 144 MB |

`_ts_idx` 每次扫描读 **70,030** 个索引项（477,231,984 ÷ 6,815），
而 `BatchSize = 5,000`（`retention.go:42`）——**14 倍读放大**。

原因：留存查询的 `ORDER BY (snapshot_ts, tenant_id, credential_id, raw_model_name)`
有 3 列不在 `_ts_idx` 里，**索引无法满足排序**，于是读整个范围再排序。
`_pkey` 恰好含全部 4 列、可以边走边停，但优化器选了更小的 `_ts_idx`。

⇒ 我原先写「`_ts_idx` 分区后可能冗余，需先验查询计划」是**过于保守**：
它不是冗余，是**被误选且低效**。分区化后留存改为 `DROP PARTITION`，
这 477M 次索引项读取**整体消失**，`_ts_idx` 可直接不建。

**③ 累计写入/删除量印证 churn**

`n_tup_ins = 24,910,471`、`n_tup_del = 28,929,077`（10.6 天），`n_tup_upd = 0`。
**只增不删改**——表是纯 append + 留存删，正面支持分区方案。

### 10.6 仍然存在的风险

1. **迁移期需约 9 GB 临时空间**（磁盘可用 102 GB，够）
2. **`DROP PARTITION` 与进行中 INSERT 的竞态**：分区上界在 `now - 7d` 之外，
   天然安全；但迁移切换瞬间需人工确认
3. **必须走迁移 + `db-changelog` 门**，不能直接改生产库
4. **基线 schema 文件需同步**：`deploy/sql/schemas/baseline/01-schema.sql:18198`
   有该表的 `CREATE TABLE` 与 `PRIMARY KEY` 约束（:22634），分区化后两处都要改，
   否则下次基线重建会退回非分区形态

**我没有实施这一项。** 它比前面 6 项都大，需要单独授权与设计评审。
