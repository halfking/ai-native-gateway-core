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

缺口本身尚未定位（候选：节点数在增长导致历史批更小、留存按整点删除导致边界
不精确、保留期实际不足 7 天）。**但无论缺口来自哪里，区间 [5.1, 6.9] GB
都是上界**，因为它由「当前实际占用的体积」和「当前实际写入速率」两个
独立实测量各自推出。

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
```

> ⚠️ 累计计数要配 `pg_stat_database.stats_reset` 解读
> （本窗口实测 `2026-09-23 06:56:38`，即 255.11 小时 / 10.63 天）。
> **不要**用 `n_tup_ins / now() - stats_reset` 直接当速率，中间的
> 时间单位换算极易出错 —— 我第一次就多除了一个 60，得到「27.2 行/分」
> 这种明显不可能的数字。（订正后：10.63 天平均 **1,639 行/分**。）

---

## 8. 仍未定论的（不要当结论用）

- §7 那个 **25% 缺口**（3,159,636 行）去了哪里 —— 三个候选假设都未验证。
- Finding D 里「1.83×」这个旧数字的来源已无从追溯；本篇只确立**当前实测是 2.00×**。
- 245 退出 shadow 后的**实际**稳态体积仍需实测确认，本篇是折算值不是实测值。
