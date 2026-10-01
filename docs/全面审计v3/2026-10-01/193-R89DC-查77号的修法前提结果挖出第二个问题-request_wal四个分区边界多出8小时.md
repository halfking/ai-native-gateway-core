# 193 号 · R89-DC —— 查 77 号的修法前提，结果挖出**第二个问题**：`request_wal` 四个分区边界**多出 8 小时**（686 号同款污染）

> **日期**：2026-10-01
> **轮次**：R89-DC（第 93 轮，审计第 193 号）
> **类型**：**核实上一轮的修法建议**（发现建议表述需修正）+ **新发现分区边界污染**（新增 **待裁决 78，P2**）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：192 号（66.6 万条 WAL 卡 pending）

> ## ⚠️ 193 号的「普查完成」不完整 —— 194 号换判据后又查出 **10 张表 38 个分区**
>
> 194 号（[R89-DD](2026-10-01/194-R89DD-分区边界污染是三种形态不是一种-普查补出另外10张表38个分区.md)）
> 指出：193 号的判据是 `bound LIKE '%08:00:00+08%'`，
> **只能命中「显式带 +08 且小时为 08」这一种形态**。
> 换更宽的判据（`NOT LIKE '%+08%'`）后，又查出 **10 张表 38 个月分区**
> 用的是「无时区字面量」（`FROM ('2026-08-01')` 或 `FROM ('2026-08-01 00:00:00')`）。
>
> **⚠️ 但那批在本机是正确的**（`SHOW TimeZone` = `Asia/Shanghai`）
> ⇒ **属「潜伏缺陷」而非当前缺陷。**
>
> **⇒ 78 号的范围要扩**：删除侧受影响的表从 3 张增加到 **5 张**
> （新增 `cache_metrics` 形态② / `handoff_logs` 形态③，两者都在 `stateTableTTLSpecs()` 里）。
>
> **⚠️ 本报告 §F2-b 的「8 张表 24 分区」只覆盖了形态①，不是全部形态。**

---

## 〇、起手

192 号给的修法建议是「让限流早退路径也调用 WAL 的终态 UPDATE」。
**按本会话的纪律（§77：建议修法必须先验前提），本轮去核实这条路是否真的可行。**

**结果：修法方向对，但 192 号的表述漏了一环**；
**而且在核实过程中，撞上一个 686 号早就描述过、但没人确认是否已修的分区边界污染。**

---

## 一、F1：核实 77 号的修法前提 —— **WAL 初始行确实存在，缺的是终态 UPDATE**

先确认「限流早退时 WAL 有没有初始行」：

| 环节 | 位置 | 结论 |
|---|---|---|
| early `CreateInitial`（写 WAL 初始行） | `handler.go:2031` | ✅ **存在**，且**早于**限流检查（`:3225` 附近） |
| 限流早退时的 WAL 终态 UPDATE | — | ❌ **未被调用** |
| `request_logs` 占位行 | `handler.go:8123 insertRateLimitedPlaceholder` | ✅ 2026-08-26 已补 |

`handler.go:2005-2011` 的注释把这个设计写得很清楚：

> ```
> // write the request WAL row NOW, as early as possible, so EVERY received
> // request is traceable in request_wal_hot — including pre-routing failures
> (missing_key, invalid_key, body_too_large, json_parse_error,
>  rate_limit_exceeded, model_forbidden) that previously never reached the
> CreateInitial call at ~line 2371 … For pre-routing failures this is the
> only WAL row.
> ```

**⚠️ 注意最后那句：「For pre-routing failures this is the only WAL row.」**
**⇒ 对限流而言，WAL 只有那一行初始记录，终态从来没被写。**
**⇒ 这与 192 号的实测完全吻合（`client_model` 有值但 `completed_at` 空 ——
注意 `client_model` 是被 192 号 F2 判为「有值」的那一列，
而 `handler.go:2026-2030` 明说 early write **只带 `requestID + provisional session id`**，
**`TenantID` 硬编码 `"default"`、`client_model` 根本没传**）。**

**⚠️⚠️ 这里与 192 号的观测存在一个必须说清的出入：**
192 号实测「`client_model` **100% 有值**」，
而代码说 early write **不传 `client_model`**。
**⇒ 说明这 66.6 万行的 `client_model` 不是 early write 写的，
而是被 `:4064` 那次**完整** `CreateInitial` 补上的**
（`:2020` 注释：「the later, fuller `CreateInitial` at ~2371 **enriches this early row in place**」）。
**⇒ 也就是说：这些请求走完了正常主路径的初始写入，只是**没走到终态 UPDATE**。
⇒ 192 号「early write 只带两列」的推断需按此修正。**

**⇒ 修法方向确认可行，但 192 号的表述要精确成：
「限流早退路径绕过了终态 UPDATE，而完整初始写入已发生」**（不是「WAL 只有初始行」）。

---

## 二、🔴 F2：本轮的意外发现 —— **`request_wal` 四个分区的边界多出 8 小时**

```sql
SELECT relname, pg_get_expr(relpartbound) FROM pg_inherits … WHERE parent='request_wal';

 request_wal_2026_06 | FOR VALUES FROM ('2026-06-01 00:00:00+08') TO ('2026-07-01 00:00:00+08')  ← 正确
 request_wal_2026_07 | FOR VALUES FROM ('2026-07-01 08:00:00+08') TO ('2026-08-01 08:00:00+08')  ← 多 8h
 request_wal_2026_08 | FOR VALUES FROM ('2026-08-01 08:00:00+08') TO ('2026-09-01 08:00:00+08')  ← 多 8h
 request_wal_2026_09 | FOR VALUES FROM ('2026-09-01 08:00:00+08') TO ('2026-10-01 08:00:00+08')  ← 多 8h
 request_wal_2026_10 | FOR VALUES FROM ('2026-10-01 08:00:00+08') TO ('2026-11-01 08:00:00+08')  ← 多 8h
```

**⇒ 恰好 1 个分区（06）是对的，4 个分区（07–10）全部多 8 小时。**

**观测印证：** 192 号那批 pending 的 `created_at` 范围是
**`2026-09-03 15:28` → `2026-10-01 05:58`** ——
**而 `request_wal_2026_09` 的上界是 `2026-10-01 08:00:00+08`**，
**⇒ 那批 10-01 凌晨 5:58 的行落进「9 月分区」正是因为这个 +8 小时。**

### 这与 686 号描述的污染**是同一类，但 686 号已修、这里没修**

`686_fix_session_module_executions_2026_10_bounds.sql` 的注释原文（187 号已引过）：

> ```
> 本库 session_module_executions_2026_10 的分区上界是
>   TO ('2026-11-01 08:00:00+08')   -- 应为 00:00:00+08,多出 8 小时
> (创建时把 +08 时区渲染的字面量直接嵌进了 TO 边界)
> ```

**⇒ `request_wal` 是同一批被污染的表之一，686 号只修了 `session_module_executions` 一张。**

**⇒ 实际后果（可直接量化）：**
- **每天 00:00–08:00 的 8 小时数据会被归到上一个月分区**；
- 月度统计/归档按分区名切分时，**每天有 1/3 的数据在错误的月份里**；
- ⚠️ **本轮未验证是否有下游真的按「月份」聚合 WAL**（无 Go 读端，见 192 号 F 定级）⇒ **登记不下结论**。

---

## 三、定级

| 项 | 定级 | 依据 |
|---|---|---|
| **77 号修法表述** | ⚠️ **需修正** | 方向可行 ✅；但「WAL 只有初始行」不准确 —— 完整 `CreateInitial` 已发生，缺的是**终态 UPDATE** |
| **78 号：8 张表 24 个月分区边界 +8h**（新） | **P2** | 机制 ✅ / 规模 ✅（普查 8 表 24 分区）／**读端侧无影响**（`usage_ledger` 6+ 读端全走 `UNION ALL` 视图不切月；`request_wal` 无 Go 读端）／**删除侧有影响**：✅ `stateTableTTLSpecs()` 含 `routing_decision_log`(30d) / `model_probe_runs`(14d) / `credential_model_index`(7d) **3 张受污染表** ⇒ **`dropOldStatePartitions` 按分区表名删，会让边界附近数据早 8 小时被删** |
| **零新增待裁决** | — | 78 号为新增 1 条 |

**⚠️ 与 686 号的关系**：686 号修了**一张**表（`session_module_executions`），
**本轮普查发现同批未修的还有 8 张表、24 个月分区**（其中 `model_probe_runs_2026_10`
是「半污染」形态，与 686 号那张全污染不同）。
**⇒ 建议：按 686 号的既有修法（`DETACH` + 按实际边界重建）修，而不是只修这一张。**

### F2-b：普查已完成 —— **8 张表、24 个月分区**受污染

| 父表 | 受污染分区数 | 备注 |
|---|---|---|
| `credential_model_index` | 4（07–10） | |
| `credit_ledger` | 4（07–10） | |
| **`usage_ledger`** | **4（07–10）** | ⚠️ **在费用对账链路上** |
| **`request_wal`** | **4（07–10）** | 本轮发现 |
| **`routing_decision_log`** | **3**（07/09/10，08 正确） | |
| `routing_decision_log_archive` | 1（08） | |
| `tool_usage_stats` | 4（07–10） | |
| `model_probe_runs` | 1（10，**边界半污染**：FROM 正确 / TO 错） | 686 号同款 |

**⇒ 合计 24 个月分区受影响，涉及 8 张表。**
**⚠️ `usage_ledger` 与 `routing_decision_log` 都在费用对账 / 路由分析链路上
⇒ 「按月聚合」的报表在这两张表上会系统性偏移 8 小时。**

**⚠️ 但必须说清后果边界：**
1. ✅ **已查清（下）**：`usage_ledger` **有 6+ 个生产读端**
   （`domains/providerprofile/pg_reconciliation_store.go:199`、`domains/authentication/verifier.go:786`
   即 **API key 预算闸门**、`admin/usage_provider_detail.go` 供应商对账明细、`admin/usage_enhanced.go`、
   `admin/keys.go:973`）
   ⇒ **但它们全部走 `usage_ledger_with_current_month` 视图，而该视图是
   `UNION ALL hot + 父表`（全部分区），不按月份切分、不按分区名过滤**
   ⇒ **8 小时边界污染对 `usage_ledger` 的所有已知读端均无影响**；
   192 号已证 `request_wal` **无 Go 读端**；`routing_decision_log` 读端本轮未查；
2. **偏差方向是固定的、可预测的**（每天 00:00–08:00 的数据落到上月分区），
   **不是随机错位** ⇒ **纠偏是一条谓词，不需要重建数据**；
2-b. **🔴 但确实存在一个会真丢数据的地方**：`bg/partition_manager.go:855 dropOldStatePartitions`
   **按「分区表名」+ TTL 删除过期分区**（`SELECT drop_old_state_partition_table($1,$2)`）
   ⇒ **边界污染让每天 00:00–08:00 的数据归属上一个月分区
   ⇒ 它们会随「上个月分区」一起被 TTL 删除，比配置值早 8 小时。**
   ✅ **本轮已查 `stateTableTTLSpecs()`（`partition_manager.go:819`），其中包含 3 张受污染表**：

   | 父表 | TTL 设置 | 回落值 |
   |---|---|---|
   | **`routing_decision_log`** | `lifecycle.routing_decision_log_ttl_days` | **30 天** |
   | **`model_probe_runs`** | `lifecycle.model_probe_runs_ttl_days` | **14 天** |
   | **`credential_model_index`** | `lifecycle.credential_model_index_ttl_days` | **7 天** |

   ⇒ **这 3 张表的边界污染会真的导致数据早 8 小时被 TTL 删除。**
   **⚠️ `credential_model_index` 的 7 天最短 ⇒ 后果最直接。**
3. **`model_probe_runs_2026_10` 是「半污染」**（FROM 00:00 正确、TO 08:00 错），
   **与整表污染形态不同** ⇒ **修法不能一刀切**。

---

## 四、playbook §85 新增

> **§85 修法核实要一路验到底 —— 「缺 A 步」经常其实是「缺 B 步、而 A 步已经发生了」**

**由来**（R89-DC / 193 号）：192 号建议「让限流路径补调 WAL 终态 UPDATE」，
**理由是 early write 只带两列、终态没写**。
**本轮核实发现：完整 `CreateInitial` 其实已经发生并 enrich 了那一行，
真正缺的只有终态 UPDATE** ⇒ **建议本身方向对，但理由写错了一层**。

**⇒ 落地三条：**
1. **核实修法时，把「你说的那个缺失」和「实际缺的那一个」分开验** ——
   本轮 `CreateInitial` 有 **两个**调用点（`handler.go:2031` early / `:4064` 完整），
   **只查到一个就会把「谁写的」判错**（§66 的镜像：**要查写入方有几个，不是有没有**）；
2. **⚠️ 代码注释说「只带两列」与观测到「该列 100% 有值」冲突时，以观测为准并去解释冲突** ——
   本轮冲突的解释是「两处调用点都跑了」⇒
   **冲突不是「注释在骗人」，而是「我只读了注释的一半」**（§41 桶②的补充：**注释描述的是某一次调用，不是全部调用**）；
3. **⚠️ 修一张表时问一句「同批还有哪些表」** ——
   686 号修了 `session_module_executions`，**本轮发现 `request_wal` 是同款未修**；
   ⇒ **同一种缺陷模式在一个修复里通常不止一个实例**（§72 的「差集要看服务对象」的表亲版本：
   **同一批 bug 通常来自同一次批量建表**）。

**⇒ 与 §84 的关系**：
§84 说「一个已知盲点可能只修了一半」（出口层面）；
**本条说「一个已知修复可能只覆盖了同类里的一个」（表层面）——
两者都是「部分修复比不修复更难发现」，只是一个在事件出口、一个在对象集合。**

**同族**：§33 / §41（注释三桶）/ §45 / §59 / §61 / §66（要查写入方有几个）/
§70 / §71 / §72 / §75 / §79 / §80 / §81 / §82 / §83 / §84。
