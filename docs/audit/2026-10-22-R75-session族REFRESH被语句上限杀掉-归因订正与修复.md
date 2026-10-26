# R75 · session 族 REFRESH 被语句上限杀掉 —— 归因订正与修复

> 日期：2026-10-22
> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`
> 标签：`P11-R75`
> 对生产写入：本轮 **0**（本节所述修复全部在仓内；生产落地是 D1，属主已授权，四件事串行）
> 范围：承接 R75 的「治后台重活」。D2（session-analytics 端点）在 R75 前半段被
> 属主批为「近 30 天窗口」，本节**推翻了那个归因**。

---

## §1 一句话结论

`/api/admin/session-analytics/clients` 恒 503 的真根因**不是**「缺时间窗」
（855 早已在生产落地），也**不是**「视图排第一所以被饿死」——而是
**`REFRESH MATERIALIZED VIEW CONCURRENTLY session_client_stats` 被一个
**全族共用**的 `statement_timeout = 180s` 杀掉**。

同一族的实测：三视图成本跨 **7.45s ~ ≥180.31s**（差 25 倍），一个共用值
必然切掉慢的那一个。

---

## §2 事实链（252 共享库，两台实例 `journalctl` + `pg_stat_statements` + 端点实测）

### §2.1 端点症状

154，2026-10-22 04:30（active 端口 8781，端口不写死）：

| 端点 | 状态 | 耗时 | 备注 |
|---|---|---|---|
| `/api/admin/session-analytics/clients` | **503** | 23ms | `err=analytics_view_stale` |
| `/api/admin/session-analytics/tasks` | 200 | 13ms | 当时仍新鲜 |
| `/api/admin/session-analytics/users` | 200 | 19ms | 走别的源 |
| `/api/admin/auto-route/analytics/matrix?window=7d` | 200 | 41ms | routing 族新鲜 |
| `/api/admin/auto-route/analytics/flow?window=7d` | 200 | 98ms | 同上 |

⇒ ★ **503 在 23ms 返回 ⇒ 是「查询之前」的门在拒（新鲜度门），不是查询慢。**

### §2.2 新鲜度门与视图实态

`admin/analytics_materialized.go:85` ⇒ `sessionMvFreshnessBudget = 90 * time.Minute`。

| 视图 | `max(refreshed_at)` | 相对 90min 预算 |
|---|---|---|
| `session_client_stats` | 2026-10-22 **02:00:29** | **超期** ⇒ 503 |
| `session_task_stats` | 2026-10-22 03:03:00 | 87min ⇒ **只差 3 分钟**就要 503 |
| `session_client_task_matrix` | 2026-10-22 04:07:33 | 新鲜 |

⇒ ★★ **`session_task_stats` 当时距 503 只差 3 分钟** ⇒ 这不是「一个端点坏了」，
而是**同族三个视图都在往同一堵墙上撞**，只是各自撞的时点不同。

### §2.3 ★ 报错原文（决定性证据）

**245**（`llmgo-245-canary@8781.service`），03:00 轮：

```
03:00:00.007  session analytics view refresh: redis leader token acquired
03:03:00.317  failed to refresh session analytics view | view=session_client_stats
              | elapsed=180310045400
              | error=ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
03:05:31.812  refreshed session analytics view | view=session_task_stats | elapsed=151494301396
03:05:39.262  refreshed session analytics view | view=session_client_task_matrix | elapsed=7450094430
```

**154**，04:00 轮：

```
04:00:00.009  session analytics view refresh: redis leader token acquired
04:00:00.082  contended on the advisory lock, retrying | view=session_client_stats | wait=2s
   ...（退避 2→4→8→15→15→15→15，共 8 次，等待 91s）
04:01:14.376  contended on the advisory lock, retrying | wait=15s
04:04:31.124  failed to refresh session analytics view | view=session_client_stats
              | elapsed=271114975617
              | error=ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
04:07:33.451  failed to refresh session analytics view | view=session_task_stats
              | elapsed=182327234852
              | error=ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
04:08:38.854  refreshed session analytics view | view=session_client_task_matrix | elapsed=65403030971
```

**154**，02:00 轮（成功）：

```
02:00:34  refreshed session analytics view | elapsed=34278946817   (client_stats 34.28s)
02:00:40  refreshed session analytics view | elapsed=6191083497    (task_stats   6.19s)
02:00:44  refreshed session analytics view | elapsed=3746927380    (matrix      3.75s)
```

⇒ ★★★ **三视图成本：3.75s / 6.19s / 34.28s（好的一轮）到 65.4s / 151.5s / ≥180.31s（坏的一轮）。**

### §2.4 三条候选机制，用实测逐一排除

| 候选 | 实测判据 | 结论 |
|---|---|---|
| **缺 30 天时间窗** | `schema_migrations` 有 855 行（2026-10-13 01:33 落）；`session_client_stats.has_30day_window = t` | ✘ **排除**（已是现状） |
| **Redis token 抢不到（整轮跳过）** | 两族用**不同** Redis 键：`materialized_view_refresh` vs `materialized_view_refresh_session`；03:00 是 154 拿 routing 键、245 拿 session 键，**活干了**（在 245 上失败） | ✘ **排除**（是伴随现象，不是死因） |
| **advisory lock 饿死** | 245 03:00 那轮**零锁等待**（上一行就是 leader token，中间无 contended），照样跑满 180.31s 被杀 | ✘ **排除为主因**（只贡献 91s 延迟） |
| ★★ **语句上限杀掉 REFRESH** | 报错原文 `statement timeout (SQLSTATE 57014)`，elapsed 恰为 180.31s / 182.33s | ✔ **确认** |

---

## §3 ★ 自我推翻：R75 前半段写的归因是错的

R75 前半段（本会话自己的判断）写的是：

> 「`refreshSessionViews` 按固定顺序逐个抢同一把 advisory lock，
> `session_client_stats` **排第一** ⇒ 每轮它都**最先撞锁** ⇒ **被系统性饿死**。」

**这条被本轮的生产实测推翻。** 错在三处：

1. ★ **锁等待不是杀手**：245 03:00 那轮**零锁等待**，REFRESH 仍跑满 180.31s 被杀。
   ⇒ 「被锁饿死」与「被自己的语句上限杀掉」是两种完全不同的失效。
2. ★ **排第一其实是排在了最有利的位置**：它**先**拿到锁、先跑；被排最后反而会被
   前面的视图推迟。而它慢 ⇒ 它先跑就把整轮推迟了它自己的耗时 ⇒
   **后面两个视图被推到离新鲜度预算只剩 28 分钟**。
   ⇒ ⇒ **「排第一 ⇒ 被饿死」是错的；「排第一 ⇒ 拖慢别人」是对的。**
   ⇒ ⇒ 两者给出的修法**不同**：前者要改锁，后者要改顺序 + 上限。
3. ★ **Redis token 是两族分开的**（`sessionViewsDistLockLogical`），
   「两个周期抢同一把 token」不成立。

⇒ ★★ **通用式（可复用）：「机制 A 与机制 B 都能解释现象」时，
必须去找一个**只可能由 A 产生**的观测点。**
本轮的判决性观测点就是 **`SQLSTATE 57014` 这条报错原文**：
它只可能由「语句上限」产生，不可能是锁等待（那会走 `skipped_no_lock` 路径，
日志里是 `materialized view refresh skipped, another instance holds the lock`）。

⇒ ★★ **通用式（第二弹）：`journalctl` 的**时序**比 `grep` 的**结果**重要。**
本轮第一遍用 grep 抽日志时，把 `03:00:00.006` 那条
「materialized view refresh: redis leader token acquired」误读成 session 轮的
⇒ 推出「session 轮被 Redis follower 跳过」⇒ 又一次指向 token。
⇒ 取**带毫秒时间戳的原始行**才看清：那是 **routing 轮**拿的 token，
而 session 轮在 139ms 后成为 follower —— 两件事发生在同一毫秒级窗口里，
被 grep 的行序骗了。

---

## §4 ★ 量具盲区：`pg_stat_statements` 系统性低估了尾部

R75 前半段引用的读数是：

```
session_client_stats  REFRESH  103 次  mean 62.4s  max 175.5s
```

**`max 175.5s < 钉子 180s` —— 恰恰证明被杀掉的那批根本没进统计。**
`pg_stat_statements` 只统计**完成**的语句；被 `statement_timeout` 取消的语句
不进它的 `max`。

⇒ ★★★ **⇒ 这是一个量具盲区（instrument blind spot）：**
**「平均 62.4s」是「成功轮」的平均，而成功轮天然偏便宜。**
⇒ ⇒ 真实耗时的尾部**目前不可测**。
⇒ ⇒ **推论：任何「max 175.5s 离 180s 还有 4.5% 余量，所以 180s 差不多够」
的说法都是这个盲区的产物 —— 它用「被量到的那些」去推断「没量到的那些」。**

⇒ ★ **⇒ 处置的诚实表述：抬到 600s 是「够用」，不是「已证够用」。**
真正的根治是**把 REFRESH 的成本降下来**（§6），不是继续抬钉子。

---

## §5 修复（四处，每处都有承重判据）

### §5.1 分族语句上限

`bg/materialized_view_refresher.go` 新增 `sessionViewsStatementTimeout = "600s"`，
`refreshView` 改为**按参数**接收语句上限；routing 族仍传 `mvRefreshStatementTimeout`
（`"180s"`）—— 12h 窗口内 routing 族**零失败**记录，且实测 max 139.6s 仍在界内
⇒ 没有证据支持改它。

**600s 的依据**：**仓内同一类病已有先例** —— §14330 把巡检脚本的
`STMT_TIMEOUT` 从 180s 抬到 600s，理由同为「空闲 105s / 有负载 180s+」。

### §5.2 轮内 ctx 必须大于分族上限

`SessionViewsTimeout`：10min → **15min**。

★ 这是最承重的一条：**语句上限 600s 必须小于轮内 ctx**，否则 ctx 先掐断，
抬钉子等于白抬（且失败形态从「被杀」变成「整轮被砍」，后两个视图根本没刷新
—— 比原来更难发现）。

### §5.3 视图顺序：快的先刷

`SessionAnalyticsViews` 由「慢的排第一」改为
`session_client_task_matrix → session_task_stats → session_client_stats`。

⇒ **语义不变**：三个视图彼此独立；admin 侧的新鲜度门按**端点所需**的视图
列表逐个判定（`admin/session_analytics_*.go`），与本清单顺序无关。
⇒ **作用是限制爆炸半径**：慢视图无论成败，都不再推迟快的两个。

### §5.4 相位错开（而不是另开一把锁）

`sessionViewsPhaseOffset = 5 * time.Minute`：session 周期的墙钟相位从整点
错开到 `:05`。

**为什么**：`RefreshInterval=30min`（:00/:30）与 `SessionViewsInterval=60min`（:00）
都对齐整点 ⇒ 每个整点**必然**同刻抢同一把 `mvRefreshLockKey`。
实测 154 04:00 轮 session 为这把锁等了 **91s**；03:00 轮则是 **routing 族
被 session 族占着锁而两个视图全被跳过** ⇒ **共享锁同时伤害两族**。

**为什么不用「另开一把锁键」**：本文件历史上明确否掉过 ——
「修法不是另开锁键（那会让 routing 与 session 的 REFRESH 并发跑，
而 session 一轮已经占掉集群 62%~75% 的 WAL）」。
⇒ **相位错开是零并发的解法**：:00 只有 routing 轮，:05 只有 session 轮，
两族**不再相撞**，不需要第二把锁，也没有并发 REFRESH，**严格优于现状**。

**陈旧度算术不受影响**：刷新戳落在 :05，预算 90min ⇒ 下一轮 :05 之前仍有
30min 余量（与原先 :00 起算同余）。

★ 附带形态：错开相位会带来「进程 :03 启动 ⇒ 首轮 :05、对齐 tick 也在 :05」
⇒ 两轮只隔 2 分钟。⇒ 加 `sessionViewsMinTickGap = 10 * time.Minute`，
间隔不足时把 tick 推后一整个周期。

---

## §6 成本根因（下一层，本轮未动）

三个视图都按 `session_summaries.last_request_at >= NOW() - INTERVAL '30 days'`
过滤（迁移 855）。

生产只读实测（2026-10-22）：

| 事实 | 读数 |
|---|---|
| `session_summaries` | 829,341 行 / **826 MB**，**未分区**（relkind=`r`） |
| `session_dim` | 878,536 行 / 378 MB |
| 窗口内行数（从 MV 反推，零额外扫描） | **343,875 行 = 全表的 41.5%** |
| `EXPLAIN`（无 ANALYZE） | `Index Scan using idx_session_summaries_tenant_time` + `Incremental Sort`（Sort Key: tenant_id, client_models[1], primary_model） |
| 规划器**没有**选 `idx_session_summaries_last_request_recent` | 后者存在（`btree (last_request_at DESC, session_key DESC)`） |

★ **855 自己的注释已过时**：它写「没有任何以 last_request_at 打头的可用索引
⇒ 30 天窗的 WHERE 只能走全表顺序扫」—— **那个索引现在存在了**（不知何时加的）。

⇒ ★★ **⇒ 但成本根因不在 WHERE 上，在 `array_agg(DISTINCT primary_model)`**：
它强制对 34 万行的 `primary_model` 做排序（EXPLAIN 里的 `Incremental Sort`
就是它）⇒ **索引救不了**。

⇒ ★ **⇒ 真正的根治方向（三选一，都需要属主拍板，因为都动语义/结构）**：
- **(a) 收窄窗口**（30 天 → 7 天）：`models_used` 之类的列覆盖更短的历史。
- **(b) 去掉 `array_agg(DISTINCT ...)`**：成本塌陷，但那一列的语义要变。
- **(c) 物化一层中间表**（按天预聚合）+ 视图只读最近 N 天。

⇒ ⚠ **本轮不动这一层**：§5 的修复是「让刷新能跑完」，§6 是「让刷新变便宜」。
**前者是止血，后者是根治。** 把两者混在一起会得到「抬钉子当根治」的错觉。

---

## §7 门与变异

门：`bg/materialized_view_refresher_session_timeout_test.go`（**11 条**，package `bg`）。

| # | 断言 | 守什么 |
|---|---|---|
| 1 | `…ExceedsTheObservedKillLine` | 分族上限 > 实测杀线 180s，且余量 ≥1.5× |
| 2 | `…DiffersFromRoutingFamily` | 分族**真的**分族（两个值不等） |
| 3 | `…UsesThePassedTimeoutNotTheGlobalConstant` | ★ 防「加了参数却仍写死常量」的**假修复** |
| 4 | `…PassTheRightFamilyTimeout` | 两个族的调用点各传各的 |
| 5 | `…ExceedsTheFamilyStatementTimeout` | ★ 轮内 ctx > 分族上限（承重） |
| 6 | `…FastestFirst` | 慢视图排在最后 |
| 7 | `…PhaseOffset_IsNonZeroAndInsideTheInterval` | 相位量落在 (0, 周期) |
| 8 | `…UsesTheOffsetPhase` | ★ 防「定义了常量却没接线」 |
| 9 | `…LandsOnTheOffset` | `nextAlignedWaitAt` 的三条行为（相位前/后/恰在相位上）+ 越界归一 |
| 10 | `…KeepsAGapAfterTheStartupRound` | 启动轮与首个 tick 的最小间隔 |
| 11 | `NoSecondAdvisoryLockKey` | ★ **反向**判据：守住「没有第二把 advisory 锁」（WAL 约束） |

变异：`scripts/mutate-r75-session-mv-timeout-gate.py`，**M1~M11 全红**，
按原路径 md5 逐字还原，备份不删，零残留。

★ **本轮的门自己被抓住两处错**（都是我自己写的）：
- **M9 判据用了复用的 `want` 变量** ⇒ 「offset 越界未归一」那条期望值被上一段
  的赋值污染 ⇒ 期望 58m 实得 2m。
- **M10 的字面量没对上真实源码**（真实形态是
  ``pg_try_advisory_lock($1)`, mvRefreshLockKey)``，中间是反引号不是逗号）。
⇒ **⇒ 门的第一版永远是「还没跑过」的第一版。**

★ **另有一条既有门被打红**：`bg/materialized_view_refresher_session_test.go`
的 `TestRefreshSessionViews_RefreshesEveryListedView`。
它用源码字面量 `"r.refreshView(ctx, view, true)"` 断言「逐个视图调 refreshView」。
⇒ **判据的意图（逐个视图 + 走 advisory 锁）被我完整保留**，只是参数多了一个。
⇒ **处置：把判据从 `r.refreshView(ctx, view, true)` 放宽为
`r.refreshView(ctx, view, true,`**（**`true` 仍被断言** ⇒ session 族不会偷偷绕开锁），
并在原处写明为什么放宽。
⇒ ★ **通用式：门红了先读门在守什么，再判「是它错还是我对」。**
本例是「门对、我的改动也对、字面量脆」⇒ 改门，但要把意图保留下来并记录原因。

---

## §8 D4 落前全量基线（263 端点，154，REPEAT=3）

- 文件：`/tmp/kx-api-full-before.tsv`（本地同路径副本），264 行（263 端点 + 表头）。
- 状态码分布：`200`×121、`405`×66、`400`×34、`404`×18、`500`×8、`000000`×8
  （curl 30s 超时）、`503`×5、`200000`×2、`401`×1。
- **`000000` = 端点跑满 30s**：`/api/admin/backups`(31.0~37.8s)、
  `/api/admin/data-lifecycle/degradation/status`(30.0s)、
  `/api/admin/data-lifecycle/stats`(30.0s)、`/api/admin/turns/sessions`(30.0s)、
  `/api/admin/live-stream`(30.0s)、`/api/free-pool/stream`(30.0s)、
  `/api/admin/db-status`(30.0s)、`/api/sessions/list`(10.1~30.0s)、
  `/api/admin/dashboard/session-overview`(10.1~30.1s)。
- **>2s 的端点共 21 个**，最慢一档是 `data-lifecycle` / `dashboard` / `sessions` /
  `pending-responses` / `compression` 一族 —— ★ **这些正是「后台重活」族**，
  是 R75 主线的下一个靶子。

⇒ ★ **这份基线的价值不在「哪几个慢」，而在「落 892 之后同一把尺子量出来有没有
多出新的慢端点」** ⇒ ⇒ D1 落地后必须**同口径**再量一次。

---

## §9 诚实边界

1. ★ **本节所有修复尚未在生产生效** —— 需要新的二进制部署（本轮未获授权）。
2. ★ **600s 是「够用」不是「已证够用」**：真实耗时的尾部不可测（§4）。
3. ★ **`sessionMeasuredRefreshSeconds` 被从 64s 改成 520s**，依据是实测最坏一轮
   （518.84s）。改后的算术仍成立：稳态 68m40s < 90min < 漏一轮 128m40s
   ⇒ **90min 的新鲜度预算不用改** ⇒ **不需要降低数据质量门槛**。
   ⚠ 但若上线后实测一轮超过 **1800s**，这个算术会失效 ⇒ 那时才需要属主
   重新审视预算。
4. ★ **routing 族的 180s 上限没动** —— 12h 窗口内零失败记录。
   ⚠ 但 routing 的 max 已达 139.6s（离界 23%）⇒ **同一个病可能在 routing 族
   复现**，本轮没有证据支持动它 ⇒ **登记为待观察项**。
5. ★ **`session_user_stats` 在生产不存在**（`/session-analytics/users` 走别的源，
   200 @ 19ms）⇒ 未补，也未确认是否需要补。
6. ★ **§6 的成本根因未动** —— 本轮是止血不是根治。
7. ★ **`session_task_stats` 当时距 503 只差 3 分钟** ⇒ 「只有 clients 坏了」
   是**那一分钟**的快照，不是稳定形态。

---

## §10 待属主决策

| # | 事项 | 本轮是否需要决策 |
|---|---|---|
| D2-a | **是否授权把本修复发到生产**（新二进制 → 154/245） | ★ **需要** |
| D2-b | 是否放宽 90min 新鲜度预算 | **不需要**（§9.3：算术仍成立） |
| D2-c | 是否把 503 改成「降级为陈旧数据 + 标注」 | **不需要**（§5 的修复让刷新能跑完） |
| D6 | §6 的三条根治方向（收窄窗口 / 去掉 DISTINCT 聚合 / 物化中间层） | 下一轮 |

⇒ ★★ **⇒ 与 R75 前半段的「D2 = 近 30 天窗口」相比，本轮最大的变化是：
属主批的那个改动**在生产上已经是现状**，而真正让端点挂掉的东西，
不需要动任何数据口径就能修好。**
