# Round 44 §9-4：把「N 个红」逐条定性

日期：2026-10-02
基线：`06948fac9`（origin/main）
对应 §9 第 4 项：*逐条定性剩下 11 个红包，别让「14 个红」长期作为一个无人认领的总数。*

---

## 0. 结论先行

§9-4 要的是**归属**，不是又一个总数。上一轮完整 sweep（27 包）共 **44 个红测试**（子测试已折进父测试），
按「错误类别 × DSN 来源」两个可判定维度分成 **5 个族**：

| 族 | 数量 | 性质 | 是否有人认领 |
|---|---|---|---|
| 1 自建夹具撞共享库 | 16 | 测试夹具问题，机制已在本轮验证 | 是（隔离机制，见 §2） |
| 2 夹具与生产 schema 漂移 | 6 | **真缺陷**，夹具没跟上 schema | 是（可直接修，见 §3） |
| 3 测试要的是「数据」不是 schema | 6 | 契约问题：门禁只给 schema，不给数据 | 否（**需要裁决**，见 §4） |
| 4 测试自起容器/硬编码 DSN，绕过门禁 | 5 | 契约违规，压根没用门禁的库 | 否（**需要裁决**，见 §5） |
| 5 715 FreshChain 缺安装器迁移 | 1 | 已定位，本轮修了两层 | 是（见 §6） |

「无人认领」的其实是**族 3 和族 4，共 11 个**——这个数字对上 §9-4 立项时的「11 个红包」纯属巧合，
但两者的性质描述一致：**它们不是测试写错了，是门禁的契约没有覆盖到它们需要的东西。**

---

## 1. 分类方法（以及我自己把量具弄坏了一次）

用两个维度，**都不靠测试名字猜**：

- **轴 1 — 错误本身**：服务端报了 SQLSTATE 就用 SQLSTATE，否则用第一条可归因的错误行。
- **轴 2 — DSN 来源**：读测试自己的源码判定，分四类
  - `GATE_DSN` 读 `TEST_PG_URL` / `TEST_DATABASE_URL` / `TEST_DB_URL` / `LLM_GATEWAY_PG_URL` …
  - `OWN_CONTAINER` 自己 `postgres.Run(...)` 起 testcontainer
  - `HARDCODE_DSN` 源码里嵌了 `postgres://` 字面量
  - `NO_DSN` 以上都不是

**量具坏过一次，值得记。** 第一版探测器从 `func TestX(` 往后取窗口，于是 `fault`、`licensing`、
`vibecoding` 这些**在同文件更早位置的 helper 里读 DSN** 的测试被判成 `NO_DSN`——26 个。
一个恒为 0 的量和一个正确的量看起来一样。改成扫全文件后 `NO_DSN` 降到 19，且剩下的
`NO_DSN` 全部落在 42P07 那一族上，另有解释（见 §2）。

---

## 2. 族 1：自建夹具撞共享库（16 个）

`42P07 relation … already exists` ×14 + `2BP01 cannot drop table … because other objects depend on it` ×2。

| 包 | 数 | 取连接的方式 |
|---|---|---|
| `sql/migrations/startup` | 6 | 包级 helper `partitionBehaviorContainer` |
| `bg` | 8 | 包级 helper `DispatchPostgresContainer`（`bg/dispatch_postgres_helper.go`，**非测试文件**） |
| `taskprofile` | 2 | 包级 helper |

这 19 个里的 `NO_DSN` 不是「不连库」，而是**从同包另一个文件里的包级 helper 拿连接**——
bg 的证据：`auto_route_realtime_listener_integration_test.go:127` 调
`DispatchPostgresContainer(t, ctx, autoRouteListenerSchema)`。

`2BP01` 那两个（`TestMigration541ScopeRevision*`）性质更重：它们的 cleanup 会
`DROP TABLE public.credential_model_bindings`，在满库上被 PostgreSQL 拦下，
**指向真实部署就是数据丢失**。

**归属：已有人认领，且机制已验证。** `sql/migrations/startup` 的 6 个在本轮用 `internal/testdb`
修掉 5 个（见 `2026-10-02-round44-closure-migration-fixtures.md`）。bg 的 8 个和 taskprofile 的 2 个
是**同一族、同一手法**；`bg/dispatch_postgres_helper.go` 上轮已经留了 per-test schema 的缝
（仅在显式传 schema 时生效），这批测试没走那条缝。

---

## 3. 族 2：夹具与生产 schema 漂移（6 个）—— 真缺陷，可直接修

| 包 | 测试 | 错误 |
|---|---|---|
| `admin` | `TestTaskSummaryAggregatesModelsTagsAndWallClockDuration` | `23502` null value in column `status` of `session_dim` |
| `admin` | `TestProjectTasksSkipsNullTaskID` | `23503` `session_summaries` violates FK `fk_session_tenant` |
| `bg` | `TestMigration762ProjectBackfillChain_RealDB` | `23503` 同上 |
| `domains-hooks-handoff` | `TestMigration527_DurableGoalStateLifecycleIntegration` | `23502` null value in column `session_id` of `handoff_logs` |
| `domains-dispatch` | `TestPolicyPublisherPublishCatchUpIntegrationAppliesMaterializedPolicy` | `42703` column `max_queue_depth` does not exist |
| `domains-dispatch` | `TestPolicyPublisherPublishCatchUpIntegrationFailureLeavesRevisionsPinned` | 同上 |

形态统一：**测试往生产表里塞数据，而生产 schema 已经变了**（加了 NOT NULL、加了外键、删/改了列），
夹具没跟上。

`42703 max_queue_depth` 那两个要单独看一眼：它报的是**代码**引用的列在库里不存在，
可能是夹具建表语句缺列，也可能是产品代码引用了一个从未创建的列——**这一条需要单独确认归属**，
本轮不下结论。

**归属：夹具维护，跟着生产 schema 走。** 这 6 个不需要新机制，改夹具即可。

---

## 4. 族 3：测试要的是「数据」不是 schema（6 个）—— 需要裁决

| 包 | 测试 | 证据 |
|---|---|---|
| `admin` | `TestReportRollup_HTTPContract` | 报错自述：「report_snapshots 为空：DSN 已指定却无数据，多半指错了库。本测试要求有日聚合结果的库」 |
| `admin` | `TestResolveCandidatesInvariant_Live` | 报错自述：「empty models_canonical — the gate would be vacuous」 |
| `admin` | `TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows` | 窗口内没有 body 与 v1 turn ts 不同的 session |
| `admin` | `TestFetchRequestBodies_HotMiss_FallsBackToBodiesView` | 需要 request body 行 |
| `admin` | `TestRoutingCandidateBindingReorder_IntegrationHappy` | 期望有 audit 行，实际没有 |
| `bg` | `TestLedgerReconciler_RunOnce_RealDB` | 「RunOnce found 1 differences, want >=2」 |

门禁给的是**一次性空库 + 完整 schema，零业务数据**。这些测试要的是**装了数据的库**。

值得肯定的是：其中两个**自己检测到了空洞并在报错里说明**（「the gate would be vacuous」、
「多半指错了库」）。这是好的设计——它们没有假装通过。

**归属：无人认领，需要裁决。** 三条路，代价递增：
1. 给这些测试自建带种子的库（`testdb` + 种子脚本）——最干净，但要为每个测试写种子；
2. 门禁增加一个「带样本数据」的形态（第三个 `GATE_DB_SHAPE`）——一次性成本，但形态会随生产漂移；
3. 承认它们需要外部数据源，从门禁列表里移出并显式 SKIP——最诚实，也最省事，但降低了覆盖。

**这是需要人拍板的取舍，本轮不替用户选。**

---

## 5. 族 4：自起容器 / 硬编码 DSN，绕过门禁（5 个）

| 包 | 测试 | 证据 |
|---|---|---|
| `bg` | `TestNonfeaturedWatchdogIntegration` | `postgres.Run(...)`，硬编码 `testdb`/`testuser`；报 `failed to connect to user=testuser database=testdb` |
| `bg` | `TestStrictCanaryProbeQueueIntegration` | `postgres.Run(...)`，硬编码 `canary`/`canary` |
| `domains-dispatch` | `TestPolicyPublisherPublishCatchUpIntegration*` ×2 | 同文件含 `postgres.Run(` |
| `tests-integration` | `TestFaultInjection` | 源码内嵌 `postgres://` 字面量 |

这些测试**从头到尾没有用门禁提供的那个库**。它们要么自己起容器（本机起不来，报连接失败），
要么硬编码 DSN（门禁的 DSN 注入对它们完全无效）。

`domains-dispatch` 那两个同时落在族 2（`42703 max_queue_depth`）——它们自起容器后建了一套
**和生产不一致的表**，所以列对不上。这两个是**跨族的**，只归一类会误导。

**归属：无人认领，需要裁决。** 和族 3 是同一类契约缺口：门禁的 DSN 约定只覆盖了
「读 `TEST_*_URL`」的写法，而**读取方式本身没有约束**。

一个可以立刻做、且不损失覆盖的收口：让 harness 在**运行前**检查每个待跑测试是否真的读到了
门禁注入的 DSN，读不到就明确报「该测试不消费门禁的库」，而不是让它以
「连接失败」这种完全指错方向的样子变红。这跟本轮修的 `go list` stderr 是同一类修复。

---

## 6. 族 5：715 FreshChain（1 个）

本轮已修两层（executor 语义 + 隔离缺口），剩「漏了安装器注册的 198 条启动迁移」一层，
`db.Open` 报 `42P01 public.session_aggregate_outbox does not exist`。
详见 `2026-10-02-round44-closure-migration-fixtures.md` §4A。

---

## 7. 数据来源与其可信度

- **基线数据**：上一轮完整 sweep，`/tmp/r44-closure-sweep-after/`（27 包，44 红）。
  这份数据产生于本轮 5 次修复**之前**，其中 `sql/migrations/startup` 的 6 个已过时。
- **本轮实测抽查**：`admin` 2430 PASS / 30 SKIP / **11 FAIL**（与基线一致，无回归）、
  `autoupdate` 112 PASS / **0 FAIL**、`bg` 1044 / **10 FAIL**、`taskprofile` 37 / **0 FAIL**、
  `sql/migrations/startup` 172 / **1 FAIL**。
- **全量复测**：28 包 sweep 正在后台跑（`/tmp/r44-sweep-94/`），完成后会得到与当前 HEAD 对齐的
  完整数字。**本报告的合计仍是修复前的口径，不是当前 HEAD 的口径。**
- **一个量具缺口**：CI 里的 `integration-gate` job 在 round-43 已被移除（三个可证伪的失效原因），
  所以并不存在「CI 包列表」。上一轮那份 27 包列表是**本地测量集**，它漏了 `scripts/audit`
  （该包有 integration 专用测试文件但不在列表里）。本轮改用门禁自己的判据
  （仅由 integration tag 引入的测试文件 > 0）重算，得到 **28 个包**。
