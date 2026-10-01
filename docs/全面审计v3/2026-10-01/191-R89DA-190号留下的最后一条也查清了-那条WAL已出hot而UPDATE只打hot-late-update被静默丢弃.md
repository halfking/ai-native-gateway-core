# 191 号 · R89-DA —— **190 号留下的最后一条也查清了**：那 1 条 WAL 已出 hot，而 UPDATE **只打 hot** ⇒ late update 被静默丢弃

> **日期**：2026-10-01
> **轮次**：R89-DA（第 91 轮，审计第 191 号）
> **类型**：**把「无法定性」彻底关闭** + 撞上一个**已在注释里写明的设计取舍**（零新增待裁决）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：190 号（那 1 行走了 abandon 路径）

---

## 〇、起手

190 号把 189 号的「不猜」查清了一半：

> 「**查清的是「网关侧对它的处理是对的」**，
> **未查清的是「**它是怎么坏的**」**（需网关侧日志，§44③）。」

**本轮走了一条新路：不查日志，查它到底经过了哪些表。**
**结果：机制完全清楚，而且它撞上的是一个注释里白纸黑字写着的取舍。**

---

## 一、🔴 F1：它没消失，只是**卡在了 WAL 里**

先前的查法只看了 `request_logs`（0 行）与 `session_turns`（0 行）。
**本轮改问「这条请求经过了哪些表」** —— 既然是 `auto_route` 决策过、后来日志没了，
那它**一定在某张表里留下了痕迹**。

```sql
-- 全库哪些表带 request_id 列 → 逐张查这条
request_wal  WHERE request_id='ef7036046caf20c8974c6896a530bed7'  → 1 行 ✅
session_turns                                                       → 0 行
```

**⇒ 找到了。查那条 WAL 的状态：**

| 字段 | 值 |
|---|---|
| `status` | **`pending`** |
| `stage` | **`0`** |
| `completed_at` | **空** |
| `client_model` | **空** |
| `error` | 空 |
| `tenant_id` | `default` |
| `gw_session_id` | `gw_d8a1e29f-…`（与 `auto_route_selections` 里那条**完全一致**） |
| `created_at` | `2026-09-15 03:06:54.327446+08` |

**⇒ 决定性：这条请求在 WAL 的第一阶段（`stage=0`）就中断了，
从来没有走到「写 `request_logs`」那一步。**
**⇒ 「日志没落库」不是「落库失败」，是「压根没走到那一步」。**

**⚠️ 顺带解释了 189/190 号追了三条轮的那个 `tenant_id` 为空：
`auto_route_selections` 那行 tenant 空、WAL 这行 tenant 却是 `default`
⇒ 两者是**不同写入方在不同时刻写下的**，`auto_route` 那一刻还没解析出租户。**

---

## 二、🔴 F2：为什么第二阶段永远没来 —— **UPDATE 只打 `hot`**

`domains/hooks/observability/telemetry/request_logger.go:684-716` 的注释
**把机制完整写了出来**：

> ```
> // UPDATE directly targets request_wal_hot — the canonical write target per the
> // 2026-07 data-lifecycle architecture. The inner SELECT for "the latest row"
> // also reads from request_wal_hot since rows that have already been migrated
> // to a monthly partition are no longer editable (columnar storage / archived
> // partitions do not support UPDATE).
> // Late updates after migration are silently dropped, which is the intended behavior.
> ```

**⇒ UPDATE 语句本身也是 `UPDATE request_wal_hot … WHERE request_id = $1`。**

**验证这条到底在哪个分区：**

```
  2026_06 | 0        2026_08 | 0        hot    | 0
  2026_07 | 0        2026_09 | 1   ← 就在这里
```

**⇒ 它在 `request_wal_2026_09`（已 promote 出 hot）。
⇒ 后续的 UPDATE 打 hot 表，匹配不到它 ⇒ **静默丢弃。**
⇒ `status` 永远停在 `pending`、`stage` 永远停在 `0`。**

**⚠️ 注释里写的是「which is the intended behavior」—— 这是一个明确的设计取舍，不是缺陷。**
**本轮按 §41 桶②「明确声明 = 不是缺陷」处理。**

---

## 三、⚠️ F3：一个必须纠正的中间数字 —— **666,046 是我算错的**

我先跑了 `SELECT count(*) FROM request_wal WHERE status='pending'` 得到 **666,046**，
差点当成「大积压」写进报告。

**逐分区重新数：**

| 口径 | 值 |
|---|---|
| **Citus 父表聚合 `request_wal WHERE status='pending'`** | **666,046（重复计数）** |
| `request_wal_hot WHERE status='pending'` | **515** |
| 各月分区（2026_06/07/08/09/10）| **各 1** |
| **`request_wal_hot` 总行数** | **722**（时间范围 **仅 2026-10-01 06:04 → 14:20**） |

**⇒ 666,046 与真实值差了三个数量级。**
**⇒ 原因是 Citus 分区父表的 `count(*)` 会把同一行在其所有后代上累加
（或按分布节点重复统计），而 hot 表只有 722 行、时间跨度仅 8 小时。**

**⚠️ 若不逐分区数，这条「666,046 条 pending 积压」会作为一条 P1 级「数据积压」写进报告。**
**⇒ 它是一个纯粹由查询口径造成的假象。**

**（并排记录本轮同期的另外两次口径错误：`request_status` 那次忘了 `request_logs` 是分区父表、
分区归属判据用 `regexp_match` 提时间失败导致 4 个分区全部误命中 —— 最后改成逐分区直查才拿到真值。）**

---

## 四、结论

| 项 | 结论 |
|---|---|
| **「为什么这条请求日志没落库」** | ✅ **已定性**：WAL 停在 `pending`/`stage=0`；该行已被 promote 到 `request_wal_2026_09`；而 **UPDATE 只打 hot** ⇒ **第二次写入被静默丢弃** |
| **这是缺陷吗** | ❌ **不是**。注释明写「Late updates after migration are **silently dropped, which is the intended behavior**」⇒ **设计取舍**（§41 桶②） |
| **代价是什么** | ⚠️ **该请求永久停在 `pending`**，`request_logs` 永无此行；**`auto_route_selections` 侧因等不到 `request_logs`，4 小时后走 `abandon`**（190 号已证）⇒ **两侧的异常是同一个上游原因的下游表现** |
| **「666,046 条 pending 积压」** | 🔴 **是我算错的**。Citus 父表聚合重复计数，真实 hot 只有 **515 条、全是今天** |
| **零新增待裁决** | 设计取舍 + 报告口径纠错 |

**⇒ 189 号追了三条轮的那 1 行，机制至此完全清楚：
**请求在第一阶段中断 → 唯一能救它的 UPDATE 打不到它 → 下游两处（`request_logs` 无行、auto_route 走 abandon）都是这个原因的下游表现。**
**⚠️ 唯一仍未定的还是「它为什么在第一阶段中断」**（需网关侧日志，§44③）——
**但那已经是「为什么这一次请求失败」，不是「为什么数据对不上」了。**

---

## 五、playbook §83 新增

> **§83 在 Citus / 分区父表上跑 `count(*)` 之前，先逐分区数一遍 —— 父表聚合会把同一个数放大三个数量级**

**由来**（R89-DA / 191 号）：`SELECT count(*) FROM request_wal WHERE status='pending'` = **666,046**，
逐分区数只有 **515 + 5 = 520**。**差 1280 倍。**
**若直接写进报告，就是一条凭空出现的 P1「数据积压」。**

**⇒ 落地三条：**
1. **分区父表上的 `count(*)` / `sum()` 必须先与逐分区统计对账** ——
   本仓是 **Citus 13.3 + citus_columnar**，父表聚合会重复计数；
   ⚠️ **对照命令**：
   ```sql
   SELECT count(*) FROM <parent> WHERE …;                    -- 666,046
   SELECT count(*) FROM <parent>_hot WHERE …;                -- 515
   SELECT c.relname, count(*) FROM pg_inherits i
     JOIN pg_class c ON c.oid=i.inhrelid … GROUP BY 1;        -- 逐分区
   ```
   ⚠️ **本会话已因此算错三次**（`request_logs` 时间列、`regexp_match` 提分区边界、Citus 父表聚合）——
   **这已是第 4 次「口径」类失误**，说明它是本仓的高频陷阱（§59 的高频形态）；
2. **⚠️ 「数据没落库」要先问「它卡在哪张表」再问「为什么没往下走」** ——
   本轮先查 `request_logs`（0 行）就以为「没落库」，
   **改问「经过了哪些表」才发现它在 WAL 里躺着，且状态是 `pending`**；
   ⇒ **「查不到」常常是「查错了表」，而那张表正在告诉你它停在哪一步**（§70 的表亲形态）；
3. **⚠️ 读到「intended behavior」时，把它拆成「设计」与「代价」两栏** ——
   本轮这条设计是**故意的**，但代价是**该请求永久停在 `pending`**，
   **且下游两处（`request_logs` 缺行、auto_route 走 abandon）都是它的表现**
   ⇒ **只写「符合设计」会漏掉代价，只写「有代价」会误报缺陷**（§37 的四维在单条记录上的应用）。

**⇒ 与 §82 的关系**：
§82 说「先问字段是写入时填的还是事后回填的」；
**本条说「先问这条记录停在哪张表、哪个状态」——
两者都是在问「它在流程的哪一步」，只是一个在字段层、一个在记录层。**

**同族**：§33 / §41（注释三桶）/ §45 / §59（尺子错的两种形态）/ §61 / §63 / §70 /
§71 / §75 / §79 / §80 / §81 / §82。
