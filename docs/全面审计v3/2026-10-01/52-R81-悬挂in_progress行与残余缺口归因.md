# 52 号报告 · R81：`request_logs` 永久悬挂的 `in_progress` 行

日期：2026-10-01
轮次：R81（主代理，真库只读）
来源：R80 登记的待查项「有会话头请求 0.6–0.8% 残余缺口（`onlyV1=2`，量级吻合）」

---

## 0. 结论

- **残余缺口已查清：不是数据丢失。** 24h 内 V1 有 3021 行（带会话头）、17 行在 V2 无对应，其中 `genuine_loss` = **0**——14 条是设计内排除的 `internal_loopback`，3 条是 `non_terminal` 占位行。R80 报出的 `onlyV1=2` 与此一致。
- **但顺带挖出一个真缺陷**：`request_logs` 里有 **1,559 行永久卡在 `request_status='in_progress'`**，最老的已 **465.6 小时（19 天）**，且**全仓没有任何代码会把该状态改回终态**。这是一个无回收器的单调累积泄漏。

---

## 1. 残余缺口的归因（关闭 R80 待查项）

按 `db.MirrorDriftClassSQL` 的同一套判据，对 24h 窗口内「带 `gw_session_id` 但 V2 无对应 `request_id`」的 17 行逐条分类：

| 来源 | request_status | success | error_kind | is_auto_request | drift_class | 条数 |
|---|---|---|---|---|---|---|
| parent | failure | f | `session_unavailable` | t | internal_loopback | 8 |
| parent | success | t | — | t | internal_loopback | 4 |
| parent | in_progress | f | — | f | non_terminal | 3 |
| hot | failure | f | provider_error | t | internal_loopback | 1 |
| hot | failure | f | session_unavailable | t | internal_loopback | 1 |
| | | | | | **genuine_loss** | **0** |

与 R80 跑出的 `Summarize(24h): v1=3021 noTurns=17 genuine=0 internal=14 nonTerminal=3` **逐项吻合**。

**两类都是设计内排除，不是丢失**：

- **`internal_loopback`（14 条）**：`is_auto_request=true` 且 `task_type` 为空。`internal/sessionv2mirror/hook.go:104` 的注释写明「Business auto-route requests also set IsAutoRequest, but carry TaskType and must be mirrored」——即**有 TaskType 的 auto 请求要镜像，空 TaskType 的判定为网关内部 title/summary 回环，不镜像**。14 条全部 `task_type` 为空，与判据一致。
- **`non_terminal`（3 条）**：`request_status='in_progress'`，即从未进入终态的占位行。`hook.go:70-73` 记录了原因：telemetry 在上游执行**前**先 INSERT 占位，而 V2 turn 以 `request_id` 幂等、首插之后不可更新，镜像占位行会永久记成 `success=false/status_code=500` 并吞掉后续的成功富化。**故明确跳过非终态**。

⇒ **objective 的「确保原有的 api 不出错能取到正确的数据」在这条线上成立：真实用户轮次的迁移覆盖率 99%+, 且零真实丢失。**

---

## 2. 新缺陷：`request_logs` 的 `in_progress` 行永久悬挂

### 2.1 事实

```sql
SELECT count(*), max(ts), min(ts) FROM public.request_logs WHERE request_status='in_progress';
→ 1559 | 2026-09-30 14:45 | 2026-09-14 09:07
```

- **1,559 行**卡在 `request_status='in_progress'`（`request_logs_hot` 里为 0）。
- 最新的已 **16.1 小时**，最老的 **465.6 小时（19 天）**。没有任何请求会在途 19 天——这些行不是「在飞」，是**卡死**。
- 逐日分布（与当日总写入量对照）：

| 日期 | 悬挂 | 当日总写入 | 比例 |
|---|---|---|---|
| 09-24 | 430 | 392,873 | 0.109% |
| 09-25 | 295 | 370,767 | 0.080% |
| 09-26 | 315 | 404,799 | 0.078% |
| 09-27 | 3 | 21,965 | 0.014% |
| 09-28 | 47 | 19,034 | 0.247% |
| 09-29 | 59 | 11,512 | 0.513% |
| 09-30 | 7 | 8,803 | 0.080% |

比例与负载正相关（低负载日比例反而偏高，说明是**绝对条数少但一样不回收**），且**逐日累积、无下降**。

### 2.2 为什么没有回收器（这是缺陷的核心）

我先假设存在老化回收、只是没生效，去查了三条路径：

1. `bg/pending_sweeper.go` —— 文件头自述「worker that marks abandoned in_progress pending entries as …」。但它扫的是 **Redis pending 存储**，不是 `request_logs` 表（文件体内无任何 `UPDATE`/`FROM` SQL）。
2. `cmd/gateway/main.go:4005-4040` —— 那里确实有一段关于「evicts a stuck in_progress row via TTL」的注释与 `pendingStaleTimeout` 钳位逻辑，但整段都在 `if pendingStore != nil` 分支内，同样是 **pending 存储**，与 `request_logs` 无关。
3. 全仓 grep 把 `request_status` 与 `UPDATE`/`SET` 放在一起 —— **零命中**。没有任何代码把 `request_logs` 的 `in_progress` 改回终态。

⇒ 占位行只由 telemetry 在上游执行前 INSERT，**没有对应的终态 UPDATE 补偿路径**。进程被杀、终态写失败、或写侧 best-effort 失败（`PersistHook` 的契约明确是「任何 DB 错误只 log 不阻断」）都会留下永久悬挂行。

### 2.3 危害

- **单调累积**：无回收器 ⇒ 行数只增不减。
- **按 `request_status` 聚合的读数被永久污染**：`in_progress` 会被任何「在途/失败/总量」口径算进去，且这些行**永远不会被修正**。
- **占位行的 `ts` 落在 8h hot 窗口之外时**，它仍留在父表分区里、参与 `drop_old_state_partitions` 之外的任何全表扫描（本机 `request_logs` 24h 有 5,660 行、hot 侧 2,744 行，父表 2.15M 行）。

### 2.4 定级与修法

**P2**——不是数据丢失（这些请求本就未完成，V2 不镜像它们是**对的**），是数据质量与容量问题，且**不影响客户端观感**（客户端拿到的是终态或 404，与本表无关）。

**本轮未修**，理由：
1. 修法有两种语义选择，属产品裁决——(a) 老化后把行**标成失败**（`success=false` + `error_kind='gateway_stale'`，与 `dual_read_validator` 的 `non_terminal` 分类对齐）；(b) 直接**删除**（与 `in_progress` 占位的原意「即将补终态」相悖，但能止住累积）。选哪种会影响任何按 `request_status` 做统计的读数。
2. 已有现成范式可抄：`cmd/gateway/main.go:4005-4040` 的 pending sweeper（10m stale / 60s interval，`LLM_GATEWAY_PENDING_STALE_TIMEOUT` 可覆盖），照搬到 `request_logs` 是一个 `bg` 下的新 worker，改动可控但需要独立决定阈值与语义。

---

## 3. 顺带确认的两件事（防止下一个人重复查）

- **`session_turns_with_current_month` 的名字具误导性**：真库 `pg_get_viewdef` 显示它是 `session_turns_hot`（对父表去重）`UNION ALL` **全部父表分区**，不是「仅当月」。R79 依赖它做的对账因此覆盖完整，无需修正。
- **`request_logs_hot` 不是 `request_logs` 的分区**（`pg_inherits` 命中 0），两者是独立表。所以 `mirrorDriftScopeSQL` 的 `UNION ALL` **不重复计数**；R80 报出的 `Compare`(141) 与 `CompareDetail`(152) 之差确实来自 hot 侧行，不是 bug。**这条假设我先查证再下结论，避免了一次误报。**

---

## 4. 待裁决（新增 1 条，累计 17 条）

1. **悬挂 `in_progress` 行的处置语义**：老化后标失败（`error_kind='gateway_stale'`）还是直接删除？阈值取多少（pending sweeper 用的是 10 分钟）？影响所有按 `request_status` 统计的读数。

沿用前 16 条（详见 51 号报告 §6 与 49 号报告 §7）。

---

## 5. 验证方式

真库只读，全程 `SELECT` 与 `pg_inherits` / `pg_get_viewdef` 元数据查询；未建对象、零残留、未修改任何代码文件。
