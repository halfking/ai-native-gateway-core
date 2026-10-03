# 2026-10-04 §10 补充：落库倍率与容量口径的实测定论

> 本篇**不改** [`2026-10-04-ursm-snapshot-partitioning-design.md`](./2026-10-04-ursm-snapshot-partitioning-design.md)（评审稿，commit `0a2d94d1b`），
> 只补两件用只读手段就能定论的事：评审稿 **§10 拍板问题 #2（行数是否因双写翻倍）**
> 与 **#3（对外承诺 3.6 GB 还是 4~7 GB）**。
>
> 另起一篇而不追加进评审稿，是因为共享工作区里已有多方在同一份审计链上追加，
> 同一文件尾部并发追加会变成三方竞争。
>
> **测量时刻：2026-10-03 21:56 ~ 22:05 (+08)。全部数据为该窗口实测。**

---

## 1. ★ 结论先行

| 问题 | 评审稿的疑问 | 实测定论 |
|---|---|---|
| 落库是否因双写翻倍 | Finding D：「1.83× 写放大」与「行数翻倍」**不能同时成立** | **两者同时成立，且倍数就是 2.00×**，不是 1.83×。`ON CONFLICT DO NOTHING` **从未生效过**。 |
| 稳态容量承诺 | #3：3.6 GB 还是 4~7 GB | 两个都是错的。真值 **5.1 ~ 6.9 GB**（单写 + 7 天），**3.6 GB 低估了 40%~90%**。 |

---

## 2. 为什么 `ON CONFLICT` 不生效

主键定义（`deploy/sql/schemas/baseline/01-schema.sql:22635`）：

```sql
ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey
    PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);
```

`snapshot_ts` 是**主键第一列**，而它由每个写入方**各自用本地时钟**生成
（`domains/ursm/v2/persist/writer.go` 的 INSERT 参数 `$1`）。
两台机器的批次相位差 30 秒（见 §3），于是同一节点的同一状态在两台写入的
主键组合**不同** ⇒ 不冲突 ⇒ `ON CONFLICT ... DO NOTHING`（`writer.go:402`）
**一次都没命中过**。

> 这也说明 §3.1「writer 逐字不用改」在分区化后依然成立，但要清楚：
> 不改 writer 的代价是**双写期间锁死了 2× 存储**。这不是分区化引入的，
> 是分区化之前就一直在付的成本，只是此前没人把倍率算清楚。

---

## 3. 证据链（三段独立证据，缺一不可）

### 3.1 两台日志：批相位差 30 秒，且写的是同一批节点

154（`llm-gateway-go-canary@8782`）：

```
22:03:35  persist committed  rows=1242  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:03:17
22:04:30  persist committed  rows=1242  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:04:17
```

245（`llmgo-245-canary@8781`，`URSM_V2_MODE=shadow`）：

```
22:01:59  persist committed  rows=1242  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:01:47
22:02:57  persist committed  rows=1243  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:02:47
22:03:57  persist committed  rows=1244  first_cid=2  first_model=gpt-5.3-codex  snapshot_ts=22:03:47
```

- 两台都是 **60s 周期**，但 154 落在 `:17`、245 落在 `:47` ⇒ **相位差 30 秒**
- `first_cid` / `first_model` **完全相同** ⇒ 两台写的是同一批节点
- 合计 **1242 + 1242 = 2484 行/分**

### 3.2 表内批指纹：每 30 秒一批

```sql
SELECT date_trunc('second', snapshot_ts), count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '8 minutes'
GROUP BY 1 ORDER BY 1;
```

```
21:56:17 | 1245      21:58:17 | 1243      22:00:17 | 1242
21:56:47 | 1245      21:58:47 | 1242      22:00:47 | 1242
21:57:17 | 1244      21:59:17 | 1242      22:01:17 | 1242
21:57:47 | 1242      21:59:47 | 1242      22:01:47 | 1242
...                            22:02:17 | 1243      22:02:47 | 1243
                             22:03:17 | 1242
```

### 3.3 落库速率：1 小时窗口 2430 行/分 ≈ 2484

```sql
SELECT round(count(*)::numeric/60.0, 1) FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '1 hour';     -- 2430.4 行/分（145825 行）
```

### ⚠️ 一处必须写明的判据局限

**§3.2 的批指纹单独看无法区分两种情形**：

- (A) 双台各 60s 周期、相位差 30 秒 ⇒ 每 30s 一个时间戳
- (B) 单台 30s 周期 ⇒ 同样每 30s 一个时间戳

**两者在表内数据上完全同形。** 结论靠的是 §3.1（日志给出两台各自的
批时刻与行数）＋ §3.3（总速率 2430 只能由两个写入方凑出），**不是靠表内指纹**。
若只做 §3.2 就会得出「单台高频写入」的错误结论。

---

## 4. 单行成本（实测，不是估值）

`ursm_node_snapshot_min` 存活 **18,719,448** 行，堆 9,129 MB，索引 1,362 MB：

| 项 | 字节/行 |
|---|---|
| 堆 | **511.4** |
| 索引 | **76.3** |
| **合计** | **587.7** |

> 提案 §10 的 3.6 GB 用的是 **220 B/行**（活行口径）。按**整表堆/行**实测是
> **511.4 B/行**，差 2.3 倍 —— 这正是评审稿 Finding E 指出的口径分歧，
> 本篇把它定死为 587.7 B/行（含索引）。

---

## 5. 容量折算：为什么给区间而不是单值

| 口径 | 算式 | 结果 |
|---|---|---|
| 现状（双写） | 18,719,448 行 × 587.7 B | **10.25 GB** |
| 单写·折半法 | 18,719,448 ÷ 2 × 587.7 B | **5.12 GB** |
| 单写·速率外推法 | 1,242 行/分 × 1440 × 7 × 587.7 B | **6.85 GB** |

**两个口径差 34%，原因是它们对不上：**

- 速率外推：单写 7 天应有 `1242 × 1440 × 7 = 12,519,360` 行
- 实测存活折半：只有 `18,719,448 ÷ 2 = 9,359,724` 行
- **缺口 3,159,636 行（25%）**

### 5.1 缺口定位：昼夜节律，不是保留期也不是节点数（2026-10-03 22:15 补测）

保留期实测 `min=2026-09-26 21:37:28` → `max=2026-10-03 22:06:47`，
**跨度 7.020 天 / 10,109 分钟**，与契约一致 ⇒ 「保留期不足 7 天」被排除。

最近 24 小时逐小时落库量（走 `_ts_idx`，有界，耗时 6.7s）：

```
10-02 22:00 |  33,221 |   554 行/分
10-03 00:00 |  14,354 |   239
10-03 06:00 |   2,518 |    42   ← 谷
10-03 08:00 |   3,654 |    61
10-03 11:00 |  (整行缺失，见 §5.2)
10-03 13:00 |  40,397 |   673
10-03 16:00 |  71,665 |  1194
10-03 19:00 | 139,861 |  2331
10-03 21:00 | 145,872 |  2431   ← 峰
```

**谷 42 行/分 vs 峰 2431 行/分，差 58 倍。** 7 天平均
`18,719,448 ÷ 10,109 = 1851 行/分` 之所以远低于当前峰值，正是被凌晨时段
拉低的。**缺口主因是昼夜节律**，不是节点数增长（活跃节点 1,276，
距摘要记录的 1,292 上限只剩 4%）。

**⇒ 容量不是稳态量，是随时段波动的量。** 这恰好是分区化最该解决的场景：
每天 DROP 旧分区，空间随写入速率波动自动伸缩，而不是让 7 天窗口里的
低速率时段永久占着位置。

### 5.2 ★ 附带发现：10-03 10:20~12:00 有约 100 分钟写入空洞

`date_bin('10 minutes')` 粒度下：

```
10:00 | 2406      10:20 | 2398
12:00 | 2400      12:30 | 2385
```

**10:20 到 12:00 之间整段无数据**，前后速率正常（240 行/分）。
同窗口 154 日志该时段 400 行消息，全部是
`snapshot from dimension queues built`（263）与 `auto index refreshed`（137），
**没有一条 `persist committed`**。

两点必须分清：

- **日志里没有 `persist committed`，不能当作空洞的原因** —— 154 的
  `releases/2427-b7f371e5` 是 16:29 才部署的，空洞发生在上午，
  那时跑的旧版本根本不打印这条日志。
- 快照在采集（`snapshot ... built` 跑了 263 次）却零提交，方向上指向
  **persist collect 失败**（已知症状：`redis scan failed: context deadline
  exceeded`，SCAN 全库实测 12.95~30.40s 顶着 writer 的 30s 预算）。
  **这只是方向，不是结论**。

这个空洞只占 25% 缺口的约 4%（约 2.4 万行），**不是缺口主因**，
但它是一个独立的生产异常，**建议单独立项排查**。

### 5.3 ★ 根因无法定位：证据已被轮转删除（2026-10-03 22:12 追查结果）

追查了两个可能的日志源，**都已不可得**：

| 源 | 覆盖范围 | 能否拿到 10:20~12:00 |
|---|---|---|
| 154 `gateway-canary-8782.log` | 04:06 → 22:09 | ✗ 无 persist committed（旧版本不打） |
| 154 `gateway-canary-8781.log` | **16:13:55 → 16:29:57** | ✗ 只覆盖部署窗口本身 |
| 154 journald | **最早 18:58:44** | ✗ |
| 245 journald | **最早 19:52:26** | ✗ |
| 245 文件日志 | 无 gateway/canary 日志（走 journald） | ✗ |

**⇒ 根因无法用现有证据定位，本篇不下结论。** 若要定位，必须先解决
「日志留存短于排查周期」这个问题本身（见 §5.4）。

#### ★★ 一条必须订正的无效推论

追查中途我写过一句「154/245 在 09:30–12:30 都没有重启记录 ⇒ 排除服务重启」。

**这条推论无效，予以作废。** 那次 `journalctl --since 09:30 --until 12:30`
返回空，**原因是 journal 根本不覆盖那个时段**（最早只到 18:58/19:52），
而不是「那个时段没有发生重启」。

**「查询无记录」与「事件未发生」是两件事** —— 前者只说明日志留存窗口不够。
这与 §5.2 第一条是**同一个误判的两次踩中**：第一次差点把「日志没记录」
当成空洞原因，第二次差点把「日志没记录」当成排除依据。
**教训：凡是用日志缺失做正面推论，必须先确认 journal 的实际覆盖范围。**

### 5.4 附带建议：journal 留存窗口短于排查周期

两台 journal 最早只到当天 18:58 / 19:52，意味着**约 6 小时前的日志已不可查**。
对一次需要跨小时对齐的线上排查，这个窗口太短了。建议纳入运维改进项
（调大 `SystemMaxUse`/`MaxRetentionSec`，或把 persist 相关的 WARN 单独落文件）。

**⇒ 对外承诺建议写「约 5~7 GB」，不要写 3.6 GB，也不要只写 7.2 GB。**
若评审要求单值，取 **6 GB**（区间上偏保守，且含索引）。

---

## 6. 这对 §10 改造的实际影响

1. **容量收益要按「单写」承诺，不能按「双写」。** 245 退出 shadow 是
   分区化收益的前置条件 —— 不退出，稳定态就是现在的 10 GB，
   分区化只会让这 10 GB 每天归还一部分，而不是降到 5~7 GB。
2. **退出 245 shadow 的收益现在可以量化了**：写放大从 2.00× 降到 1.00×，
   即存储与索引写入量**立刻减半**（每分钟少 1,242 次 INSERT 及对应索引维护）。
3. **这不改变分区化的必要性**：即便单写，DELETE 型留存仍然不归还磁盘
   （§1.1 撤回的那条），分区化仍是唯一让空间每日回到操作系统的手段。

---

## 7. 本篇用到的查询（均可安全重跑）

全部有界：`pg_stat_*` 走系统目录；`ursm_node_snapshot_min` 的查询都带
`snapshot_ts > now() - interval ...` 上界，走 `_ts_idx` 范围扫描。

```sql
-- 落库速率（1h / 6h 窗口）
SELECT round(count(*)::numeric/60.0,1) AS rows_per_min
FROM ursm_node_snapshot_min WHERE snapshot_ts > now() - interval '1 hour';

-- 批指纹（写放方判别用，须配合两台日志，单独看会歧义）
SELECT to_char(date_trunc('second',snapshot_ts),'HH24:MI:SS') AS sec, count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '8 minutes' GROUP BY 1 ORDER BY 1;

-- 累计计数与索引数（O(1)）
SELECT relname, n_tup_ins, n_tup_upd, n_tup_del,
       (SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
        WHERE c.relname LIKE 'ursm\_node\_snapshot\_min%') AS idx_cnt
FROM pg_stat_user_tables WHERE relname LIKE 'ursm\_node\_snapshot\_min%';

-- 保留期实际跨度（min/max 走索引，O(1)）
SELECT min(snapshot_ts), max(snapshot_ts),
       round(EXTRACT(epoch FROM (max(snapshot_ts)-min(snapshot_ts)))/86400.0,3)
FROM ursm_node_snapshot_min;

-- 昼夜节律曲线（24h 有界，实测 6.7s）
SELECT date_trunc('hour', snapshot_ts) AS hr, count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts > now() - interval '24 hours' GROUP BY 1 ORDER BY 1;

-- 定位写入空洞（10 分钟粒度；注意 date_trunc 不认 'ten_min'，要用 date_bin）
SELECT date_bin('10 minutes', snapshot_ts, '2000-01-01 00:00+08'), count(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts >= '2026-10-03 10:00+08' AND snapshot_ts < '2026-10-03 12:40+08'
GROUP BY 1 ORDER BY 1;

-- 活跃节点数（1h 有界）
SELECT count(*) FROM (SELECT DISTINCT tenant_id, credential_id, raw_model_name
  FROM ursm_node_snapshot_min WHERE snapshot_ts > now() - interval '1 hour') t;
```

> ⚠️ 两个我在本篇踩到的换算坑（都写在这里免得下次照抄）：
>
> 1. **速率**：用 `n_tup_ins / (now() - stats_reset)` 时，时间单位极易搞错。
>    我第一次多除了一个 60，得到「27.2 行/分」。正确是 **1,639 行/分**
>    （stats_reset = `2026-09-23 06:56:38`，即 255.11 小时 / 10.63 天）。
> 2. **批大小**：`count(*)` 是「窗口内总行数」，要除以**批数**而不是 1440。
>    批数 = 窗口秒数 ÷ 批间隔（当前每 30s 一批）。除以 1440 会得到
>    「101.2 行/批」这种明显不可能的数。

> ⚠️ 累计计数要配 `pg_stat_database.stats_reset` 解读
> （本窗口实测 `2026-09-23 06:56:38`，即 255.11 小时 / 10.63 天）。
> **不要**用 `n_tup_ins / now() - stats_reset` 直接当速率，中间的
> 时间单位换算极易出错 —— 我第一次就多除了一个 60，得到「27.2 行/分」
> 这种明显不可能的数字。（订正后：10.63 天平均 **1,639 行/分**。）

---

## 8. ★★ 追加：DEFAULT 分区拍板项的仓库级定论（2026-10-03 22:20）

评审稿 §10 拍板 #4 建议「**不建** DEFAULT 分区，理由是黑洞 + ATTACH 变慢 +
静默丢数优于响亮失败」。**这条建议与本仓库既定模式相悖，应予推翻。**

### 8.1 仓库既有模式就是「预建 + DEFAULT 兜底」

全仓已有 6 张表采用 DEFAULT 分区（`sql/migrations/` 实测）：

| 表 | 出处 |
|---|---|
| `platform_outbox` | `local/641_...` |
| `request_wal` | `startup/332_...` |
| `routing_decision_log` | `startup/333_...` |
| `stats_event_inbox` | `startup/536_...` |
| `usage_facts` | `startup/537_...` |
| `auto_route_selections` | `startup/478_...` |

且 `startup/473_partition_precreate_2026_09_10.sql` 的预建循环第三步注释写的是：

```sql
-- 3. Add default partition if missing (rule 33 §2 兜底)
IF NOT has_default THEN
    EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I DEFAULT', ...);
```

**⇒ DEFAULT 分区是仓库规范（rule 33 §2）要求的兜底，不是可选项。**

### 8.2 而且按日分区已有现成机制，不必新写

`bg/partition_manager.go` 的 `archiveSpec.partitionUnit` 字段：

```go
// partitionUnit controls how ensureNextMonthPartitions derives the ...;
// "day" → AddDate(0,0,offset) (daily partitions, R68 迁移 750 usage_facts 按日分区接入)
partitionUnit string
```

`usage_facts` 已用 `partitionUnit="day"` 接入按日分区并跑了几个月。
**⇒ `ursm_node_snapshot_min` 只需在 spec 里加一条 + `partitionUnit="day"`，
不需要另建 ensure 函数**（评审稿改动清单 #1 的「新增迁移建 ensure 函数」
与 #4 的「加 spec 条目」可以合并成后者）。

### 8.3 ★ 切换顺序有一条硬约束（踩坑史就在仓库里）

`750_usage_facts_daily_partition.sql` 的注释记录：

> **PG 对「父表挂 DEFAULT 分区时新建具体分区」会先校验 DEFAULT 内无落入
> 区间内的行**。存量数据环境（252 共享 PG 的 telemetry 持续写入 DEFAULT）
> **首启必踩**。

修法是六步：移走 DEFAULT 内的界内行 → 建空表 → `ALTER TABLE ... ATTACH`。

**⇒ 对本改造的直接影响（且是个好消息）**：
`ursm_node_snapshot_min` 现在是**普通表**（`relkind='r'`，0 分区），
**没有 DEFAULT 分区**，切换时 DEFAULT 天然为空 ⇒ **不存在这个校验陷阱**，
比 `usage_facts` 的切换简单一整级。但**顺序仍必须遵守**：
挂 DEFAULT 与建首个具体分区不能反着来。

### 8.4 订正后的拍板 #4

| | 评审稿建议 | 订正 |
|---|---|---|
| DEFAULT 分区 | 不建 | **建**（仓库既定模式 + rule 33 §2 兜底） |
| 越界/未来数据 | 响亮失败 | 落 DEFAULT catch-all（与 `usage_facts` 一致） |
| 监控 | 需另设「有行即报警」 | 复用 `usage_facts` 的既有约定即可 |

代价是评审稿原先列的三条确实存在（黑洞、ATTACH 校验、需监控），
但它们是**本仓库已经付过的代价**，不是本次新增的风险。
真正的新增风险只有一条：**§8.3 的挂载顺序**。

**⇒ 拍板 #4 不再是开放问题，按仓库惯例即可定。**

---

## 9. 仍未定论的（不要当结论用）

- **§5.2 那 100 分钟空洞（10-03 10:20~12:00）的成因无法定位** ——
  两个日志源都已被轮转删除（§5.3 附表）。**不下结论。**
  方向指向 persist collect 失败，但那是推断不是证据。
  若要继续追，需先解决日志留存窗口问题（§5.4）。
- 空洞仅占 25% 缺口的约 4%，**缺口主因已定位为昼夜节律**（§5.1），
  这一条不再是开放问题。
- Finding D 里「1.83×」这个旧数字的来源已无从追溯；本篇只确立**当前实测是 2.00×**。
- 245 退出 shadow 后的**实际**稳态体积仍需实测确认，本篇是折算值不是实测值。
