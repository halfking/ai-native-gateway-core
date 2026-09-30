# 51 号报告 · R80：dual_read_validator 真库门 + 一个被数据依赖断言漏掉的变异

日期：2026-10-01
轮次：R80（主代理）
来源：R79 登记的「R80 首选」

---

## 0. 结论摘要

- 新建 `cmd/gateway/dual_read_validator_pg_test.go`：5 道真库门，覆盖此前**从未被任何测试执行过**的三段 SQL（`CompareDetail` / `Summarize` / `driftBuckets`）。
- 顺带查实一处**同文件内的口径分叉**：`Compare` 与 `CompareDetail` 对同一会话读的不是同一批行（141 vs 152），差值全部来自 hot 窗口。已钉成显式测试，未改实现。
- **我自己写的第一版分类断言是无效的**：把分类值改名后变异仍绿，因为当前窗口里那一类根本不存在。补了数据无关版本后转红。详见 §4。

---

## 1. 为什么需要这道门

本包既有的 4 个 `dual_read_validator` 测试——`TestDualReadValidator_Constructor` / `NilPoolCompare` / `SummarizeSignature` / `NilPoolSummarize`——**全部 `NewDualReadValidator(nil)`**。它们证明的是「nil pool 不得 panic」。

而真正复杂的部分：`CompareDetail` / `Summarize` / `driftBuckets`，含 `mirrorDriftScopeSQL` 这段跨 `request_logs_hot` + `request_logs` 两表、两个 `NOT EXISTS` 反连接、再套一层 `db.MirrorDriftClassSQL` 分类 CASE 的表达式——**数百行真实 SQL 零测试覆盖**。

形态与 R76 挖出的那个 P0 完全同型：假件只验签名与空指针，真实 SQL 靠静态阅读。R79 用真库 `PREPARE` 证了 `Compare` 一段，本轮补齐其余三段。

---

## 2. 门的设计

**变量名**按仓内契约取 `TEST_PG_DSN`（见 47 号报告）：注入清单由 `sql/schema/integration_gate_test.go` 的 `TestGateInjectsEveryDBCredentialName` 从仓内按后缀推导，自造 `_PG_DSN` 名字会被结构性排除在 CI 之外却仍显示 `ok`。

**只读**：不播种、不删数据、无残留，因此不需要事务回滚或前缀清理（与 R76 的 handoff 门不同——那门调用的方法不接受 `tx`，只能播种后按前缀删）。

**RLS 旁路用 `AfterConnect` 而非一次性 Exec**：pgxpool 的连接会被复用，一次 Exec 只落在当时那条连接上。`request_logs` 是 `FORCE RLS`（真库确认 `relforcerowsecurity=t`），会在别的连接上**静默返回 0 行**——而 0 行恰好能通过下面若干断言，是最危险的失败形态。本机 DSN 用户是 superuser 所以看不出差别，CI 的受限 DSN 用户会。

### 2.1 判据是跨查询不变量，不是「不报错」

| # | 不变量 | 抓什么 |
|---|---|---|
| I1 | `sum(ByWorkType[].Rows) == V1RowsWithoutTurns` | 桶查询被静默截断 |
| I2 | `sum(ByRequestStatus[].Rows) == V1RowsWithoutTurns` | 同上（另一维度） |
| I3 | 三类之和 `== V1RowsWithoutTurns` | 分类 CASE 漏分支 |
| I4 | `GenuineLossRows <= V1Rows` | 分母查询被截断 |
| I5 | `S4Ready == (GenuineLossRows == 0)` | 停写门算错 |
| I6 | 桶内 class ∈ 三个已知值 | 分类改名（**第一版无效，见 §4**） |
| I7 | `OnlyInV1Count`/`OnlyInV2Count` 与列表长度自洽 | FULL JOIN 退化成 INNER JOIN |
| I8 | `len(DriftSamples) <= sampleLimit` | 采样上限没传下去 |

I1/I2 是关键：`driftBuckets` 与 `V1RowsWithoutTurns` 来自**同一段 `mirrorDriftScopeSQL` 的三次独立执行**。任何一次被静默截断（行被 guard 吞掉、超时后返回部分结果、分区漏扫）都会让两条聚合不相等——而「不报错」对此一无所知。

### 2.2 两处「Skip 必须说清原因」

两轮开发中我各踩了一次**门在报告里和 PASS 长得一样**的坑：

1. **第一版 `pickRealSession` 30s 超时**。没有 `ts` 边界，对 215 万行 `request_logs` 做 `GROUP BY` + 相关 `EXISTS`。同一段 `EXISTS` 在 `Summarize` 里对 24h 窗口只需 0.43s，证明 `request_id` 索引没问题，**问题纯在分母**。
2. **第二版取「最新一条」的会话，拿到只有 1 行的会话**，`Compared==1` 让断言被平凡满足——门证明的是「查询能返回」，不是「查询算得对」。改为在有界样本（20000 行）内按行数降序取最大会话，并加 `requireSessionWithRows(…, 5)` 门槛：会话太小就显式 Skip 并说明「断言会被平凡满足」，而不是让一个没有判别力的 PASS 混进报告。

---

## 3. 顺带查实的口径分叉（本轮唯一的行为发现）

真库实跑时，`Compare` 与 `CompareDetail` 对**同一个会话**给出不同的 V1 行数：

```
Compare(default/gw_b1be9881-…):     compared=141
CompareDetail(default/gw_b1be9881-…): v1=152, v2=150
```

真库逐表核实：

| 口径 | 行数 |
|---|---|
| `request_logs`（父表） | 141 |
| `request_logs_hot` | 11 |
| hot ∪ 父表 | **152** |

**我先假设是重复计数并去查了**——`pg_inherits` 查 `request_logs_hot` 是不是 `request_logs` 的分区，答案是**否**（命中 0）。两者是独立表，`UNION ALL` 不重复计数。差异是真实的口径差。

- `Compare` 只读 `public.request_logs`（+ `session_turns_with_current_month` 视图）；
- `CompareDetail` 按 `dual_read_validator.go:189-191` 的注释读 **hot ∪ 父表**——而那句注释写的是「Reads BASE tables (hot ∪ parent) on both sides for the same reason as **CompareDetail**」，措辞暗示 `Compare` 也读基表，**但它并不**。

后果：`Compare.TokenDiff` / `CostDiff` 会**系统性低估漂移**（漏掉整个 hot 窗口的行）。

**本轮不改实现**，改口径会改变对外端点的既有读数（客户端观感），与 R79 已登记的「TokenDiff 不可解释」是同一个待裁决项。改为**钉成显式测试** `TestDualReadValidator_CompareVsDetailScope_RealDB`，断言「差异必须恰好等于 hot 侧行数」——这是把「口径差异」与「算错」区分开的那条线。任何人统一口径（或把差异扩大）都会红，迫使那次改动被显式决定。

> **不一致本身不是缺陷，不一致且无人知情才是。**

---

## 4. 我自己写的第一版分类断言是无效的（第 4 次同类失败）

变异验证时，`db/request_logs_view_schema.go:839` 的 `ELSE 'genuine_loss'` 改成 `'genuine'`——**门全绿**。

第一解释（按纪律先查）：变异是否生效？查了 M2/M3 都正常转红 ⇒ 变异机制没问题。第二解释成立：**断言量的是代理量，不是语义量**。本机 24h 窗口 `GenuineLossRows=0`，桶里只剩 `internal_loopback` / `non_terminal`，`I6` 那个 `switch` **一次都没被求值到 `genuine_loss` 分支**。

> 判别形态：一条只在「数据恰好包含该类」时才生效的断言，在数据不含该类时**看起来在守着，实际一行都没守**。这是本会话第 4 次「变异后仍绿」，也是最隐蔽的一次——前三次的断言至少在别的数据下会求值。

修法：补**数据无关**的 `TestDriftClassExpression_RealDB`——直接对 `db.MirrorDriftClassSQL` 求值，用 5 行合成输入（`VALUES`）各命中一个 `WHEN`/`ELSE`，恒定覆盖三个分支：

```
internal_loopback_by_request_type   → internal_loopback
internal_loopback_by_empty_task_type→ internal_loopback
non_terminal                        → non_terminal
genuine_loss_terminal_failure       → genuine_loss
genuine_loss_success                → genuine_loss
```

I6 保留但降级为「对实际出现的值做白名单」，数据无关版本承担主要约束。两者并存，并在 I6 处注明它为什么可能不被求值。

（实现注记：`MirrorDriftClassSQL` 内部用 `rl.` 前缀引用列，所以合成表的别名必须叫 `rl`——第一次写成 `t` 报 `42P01 missing FROM-clause entry for table "rl"`，已写进注释。）

---

## 5. 变异验证汇总

| 变异 | 结果 |
|---|---|
| M1 `ELSE 'genuine_loss'` → `'genuine'` | 第一版**仍绿**（见 §4）→ 补数据无关门后**转红** ✓ |
| M2 桶聚合 `COUNT(*) AS n` → `n - 1` | 红（I1/I2）✓ |
| M3 `d.Compared = count` → `count + 1` | 红（独立复算 + 口径差断言）✓ |
| M4 `non_terminal` 分支条件改永假 | 红（数据无关分类门）✓ |

全部还原后 `cmp` 逐字节一致。

**最终态**：`TEST_PG_DSN` 存在时 5 道门全绿（1.4s），不存在时 5 道干净 `SKIP`。

真库实跑输出：

```
被测会话 gw_b1be9881-…：V1 141 行
Compare:        compared=141 token_diff=-10212831 cost_diff=0.000000
CompareDetail:  v1=152 v2=150 onlyV1=2 onlyV2=0 samples=0
Summarize(24h): v1=3021 noTurns=17 genuine=0 internal=14 nonTerminal=3 S4Ready=true
```

**`Summarize` 独立印证了 R79 的结论**：`genuine=0`、`S4Ready=true`——24 小时内 17 条无 turn 的行**全部**归入 `internal_loopback`(14) 与 `non_terminal`(3)，**没有一条是真实丢失**。

---

## 6. 待裁决（新增 1 条，累计 16 条）

1. **`Compare` 与 `CompareDetail` 的口径统一**：统一到 hot ∪ 父表（`Compare` 的漂移读数会变小、更接近真实），还是保留现状并在端点文档里写明两者不可直接比较？前者改变对外读数，属客户端观感变更。

沿用 R78/R79 的 15 条，其中与本轮直接相关的三条：
- 失败请求的 token 口径（存储切换会改变 `prompt_too_large`/`invalid_key`/`no_candidates`/`transient` 的 token 值）；
- `TokenDiff` 的可解释性；
- 有会话头请求 0.6%–0.8% 的残余缺口（`CompareDetail` 本轮给出 `onlyV1=2`，量级吻合，仍未逐条归因）。

---

## 7. 验证方式

- `go build ./...` exit 0
- `go test ./cmd/gateway/` 全包绿（既有 4 个 nil-pool 测试无回归）
- `make guards` 9 包绿（新增文件不影响既有守卫）
- 真库：PG 17.10 + citus 13.3，全程只读（`SELECT` / `VALUES` / `pg_inherits` 元数据查询），未建对象、零残留
