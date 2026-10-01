# 138 号｜R89-AZ：核 objective 的「hot 只保留 8 小时」—— **promote 健康（实测最老行 9.0h = 8h 下界 + 1h 周期）**，但 **14/22 张 hot 表连时间列都没有**

- 日期：2026-10-01
- 轮次：R89-AZ
- 起因：objective 原文「hot **只保留 8 小时**的数据，并且通过任务批量转移到分区表中」。
  本轮逐族实测 hot 里**有没有超过 8 小时还没被搬走的行**——直接验 promote 是否真在跑。
- 结论先行：
  1. **promote 任务是健康的，不是坏了。** 有 `ts` 列的 **8/8 张 hot 表**最老行
     **统一 = 9.0 小时**，且代码/settings 已写明「8h 下界 + 1h promote 周期」。
     **objective 的「只保留 8 小时」是上界的措辞，实现是下界 + 周期 ⇒ 实际驻留 8~9 小时。**
  2. **但 22 张 `_hot` 表里有 14 张根本没有 `ts` 列**（时间列名各不相同）
     ⇒ **「hot 是否守住 8 小时」这条要求，在这 14 张表上无法用统一口径核验。**

---

## 一、实测：8/8 张 hot 表最老行**统一 9.0 小时**

```sql
WITH hot(ts, tbl) AS (
  SELECT ts,'usage_ledger' FROM usage_ledger_hot
  UNION ALL SELECT ts,'request_logs' FROM request_logs_hot
  … 共 8 张有 ts 的 hot 表)
SELECT tbl, count(*), round(EXTRACT(epoch FROM (now()-min(ts)))/3600,1) AS oldest_age_h,
       count(*) FILTER (WHERE ts < now()-interval '8 hours') AS older_than_8h
FROM hot GROUP BY tbl;
```

| hot 表 | hot 行数 | **最老行龄(h)** | 超 8h 行数 | 判定 |
|---|---|---|---|---|
| `request_logs` | 2,631 | **9.0** | 484 | YES-STALE |
| `request_logs_bodies` | 2,631 | **9.0** | 484 | YES-STALE |
| `usage_ledger` | 2,631 | **9.0** | 484 | YES-STALE |
| `session_bodies` | 1,180 | **9.0** | 182 | YES-STALE |
| `session_turns` | 928 | **9.0** | 160 | YES-STALE |
| `session_turn_details` | 928 | **9.0** | 160 | YES-STALE |
| `routing_decision_log` | 908 | **9.0** | 160 | YES-STALE |
| `candidate_failure_logs` | 38 | **8.9** | 10 | YES-STALE |

### 关键：**8 张表的最老行龄完全一致（9.0h），行数却从 38 到 2,631 差 70 倍**

⇒ 这不是「某些表的 promote 坏了」，而是**所有表在同一轮 promote 里被搬过**。
**若某张表的 promote 坏了，它的最老行龄会明显大于 9.0h。**

## 二、9.0h 是**稳态**，不是异常 —— 代码里已经写明了

- `settings/spec_lifecycle.go:7`
  `lifecycle.hot_retention_hours` 默认 **8**，
  描述：*「超过此时长的行会在**下次 promote 周期**被自动迁移到对应的月度分区表」*
- `settings/spec_lifecycle.go:8`
  `lifecycle.promote_interval_hours` 默认 **1**
  （`bg/partition_manager.go:27` `DefaultPromoteInterval = 1 * time.Hour`）
- `domains/hooks/observability/telemetry/client.go:2718` 的注释最直白：
  *「hot 保留窗口 8h (lifecycle.hot_retention_hours) **+ promote 周期 1h**」*

⇒ **8h 是「何时变得可搬」的下界，不是「hot 里最多有多少数据」的上界。**
⇒ **实际驻留 = 8h + 距上次 promote 的时间 ∈ (8h, 9h]**；实测 9.0h **正好落在上界**。
⇒ **promote 机制健康。objective 的措辞需要修正为「约 8~9 小时」。**

## 三、真正的缺口：**14 张 `_hot` 表没有 `ts` 列**

```sql
SELECT table_name, EXISTS(… column_name='ts') AS has_ts
FROM information_schema.tables WHERE table_name LIKE '%\_hot';
```

| has_ts | 个数 | 表 |
|---|---|---|
| **true** | **8** | `usage_ledger`、`request_logs`、`request_logs_bodies`、`session_turns`、`session_bodies`、`session_turn_details`、`routing_decision_log`、`candidate_failure_logs`、`auto_route_selections` |
| **false** | **14** | `model_probe_runs`、`request_wal`、`handoff_logs`、`supplier_errors`、`credit_ledger`、`tool_usage_stats`、`dashboard_access_events`、`credential_model_index`、`session_censors`、`session_memora`、`session_tools`、`session_module_executions`、`auto_route_selections`（另一份）、`bak_20260920_credential_model_index_hot` |

⇒ **「hot 只保留 8 小时」这条 objective 要求，在 14 张表上无法用统一口径核验。**
后果不是「它们一定超期了」，而是：
① **任何「hot 都在 8 小时内吗」的全库巡检脚本都会静默漏掉这 14 张表**；
② **要核验就得逐表知道它的时间列叫什么**（`created_at` / `occurred_at` / `event_time` …），
   而**这个映射目前不在任何地方成文**。

⚠️ 与 137 号同族：**「整齐的需求描述 + 不整齐的实现」**。
137 号是「所有表都 columnar」实际只有 8/22；
本条是「所有 hot 表守 8h」实际只有 8/22 **可被核验**。

## 四、定性与建议（**待裁决，不擅自动手**）

| 项 | 定性 |
|---|---|
| promote 是否在跑 | ✅ **健康**（8/8 表最老行 9.0h = 稳态上界） |
| 「只保留 8 小时」的措辞 | ⚠️ **需修正为「8~9 小时（8h 下界 + 1h 周期）」** |
| 14 张 hot 表无 `ts` | **P2（新，结构性）**：巡检与告警无法覆盖它们 |

**建议**：

1. **改文档措辞**（一词级）：「hot 只保留 8 小时」→「hot 保留窗口 8h + promote 周期 1h，
   实际驻留 8~9 小时」。**现状的写法会让运维以为「超过 8h 就是泄漏」而误报。**
2. **把 14 张表的时间列名整理成一张成文映射表**（表名 → 实际时间列），
   让巡检脚本可以统一覆盖；**在此之前，「hot 全部守住 8h」这个断言是**无法被自动化验证的**。
3. 顺带：`bak_20260920_credential_model_index_hot` 是**一张留在 schema 里的备份表**
   （前缀 `bak_` + 日期），**它也在 `*_hot` 命名空间内** ⇒
   任何按 `LIKE '%\_hot'` 写的巡检/保留作业都会把它算进去。
   **建议确认它是否该长期留在库内**（P3）。

**全部只读查询，未改动任何生产代码或数据库结构。**
