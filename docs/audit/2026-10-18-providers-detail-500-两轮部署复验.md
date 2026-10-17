# 2026-10-18 /providers/{id} 详情页 500 —— 慢查询根因、修复与两轮部署复验

> 目标（用户原话）：拉取最新代码合并本地，用 Maestro 方式对整个项目 web 端做测试验证，
> 主要检查 llm.kxpms.cn（154）；每页数据都要检查；过慢的查询要查 154 的 log 与 252 上数据库的
> log，分析原因并优化。**重点：https://llm.kxpms.cn/providers/14 查询数据出错**。
> 修正 → 部署 → 再次验证，反复通过后提交并推送。
>
> 会话 checkout：`llm-gateway-go`，基线 `e2cbd11cf`（快进合并远端 `61a3e3abe`）。
> 提交：`db89dccb2`（修复）、`f7de9944`（修复 + 判据盲区）。

## 一、复现与定位

登录 `/api/auth/token` 成功（admin/super_admin），直接在 API 层复现，比开浏览器快得多：

```
GET /api/providers/14   → 500  5.101728s
GET /api/providers/13   → 200  5.058216s
GET /api/providers/1    → 200  4.378896s
GET /api/providers      → 200  0.157457s   ← 列表不算这个字段
```

**列表 0.16s、详情 5s** 本身就是定位线索：`error_rate_24h` 只存在于详情查询
（`admin/providers.go`，`ErrorRate24h` 全仓仅此一处使用）。

154 日志（`gateway-canary-8782.log`）：

```json
{"level":"ERROR","msg":"getProvider query failed","id":14,"error":"timeout: context deadline exceeded"}
{"msg":"http_request","path":"/api/providers/14","status":500,"duration_ms":5037}
```

5.0 秒整 = handler 的 ctx 预算（`admin/providers.go:740`
`context.WithTimeout(r.Context(), 5*time.Second)`）。**不是 14 号独有**：13/1 同样贴着
这条线，只是没压过去。所以真实缺陷范围是「全部供应商详情页」，不是单个数据问题。

### 数据库侧（172.16.2.210，即 252 的第二个 IP）

`EXPLAIN (ANALYZE, BUFFERS)` 旧形态：

```
Nested Loop Left Join (actual rows=1)
   Buffers: shared hit=57682          ← 约 450MB 缓冲
   ->  GroupAggregate
         ->  Append (actual rows=3533)
               Nested Loop: Seq Scan on session_turn_details_hot (820 rows, 5444 filtered)
               Gather → Parallel Append on session_turns_2026_10 (4328 rows)
```

本机实测耗时：

| 形态 | 24h 窗口耗时 |
| --- | --- |
| 原形态（LEFT JOIN 派生表，无下推） | **12997.736 ms** |
| 仅下推 `provider_id = 14` | **7584.171 ms** |
| 改读 `request_stats_minute` 滚存 + 相关子查询 | **45.2 ms** |

**下推不够**——视图本身太重。`request_logs_with_current_month` 是四路 UNION ALL
（`session_turns_hot` ∪ `session_turns` ∪ 明细基表）+ details `LATERAL` 补列 +
两个 `NOT EXISTS` 反连接，扫 24h 窗口天然贵。

`pg_stat_statements` 侧证：该查询 mean 697ms / max 4165ms / 92 calls，是真实生产流量。

## 二、修复

改读 `request_stats_minute` 分钟级滚存，并从 LEFT JOIN 派生表改成**相关子查询**。

**不是新发明**：`usageTrendSource`（`admin/usage_trend_series.go:153`）早就把
「带 provider 过滤」的读面分档到同一张滚存表，注释写的理由就是「避免明细分区慢扫」：
> `// usageTrendSource 挑数据档：api_key 过滤只有明细读面有该列；provider 过滤走`
> `// minute 汇总（自带 provider_id×canonical_id），避免明细分区慢扫。`

`error_rate_24h` 同属 provider 过滤的聚合，按同一条分档走。

### 口径变化（已知且接受，写进代码注释）

视图臂排除了有 session turn 的行，滚存臂不排 ⇒ 分子分母总体不同。
同窗实测 provider 14：视图 3533 行 / 0.0232，滚存 2487 行 / 0.0261，同量级。
滚存是仓库自己的成对 `success_count`/`failure_count`，比视图从
`request_status IN ('failure','') AND NOT success` 反推更直接。写侧已按
`ProbeTrafficExclusionPredicateView` 剔除探针流量（`bg/stats_minute_rollup.go:252`）。

修复后整条查询 **1315 buffers**（原 57682，44×），Execution 17.4ms / Planning 122.9ms。

## 三、两轮部署（这是本轮最重要的部分）

### 第 1 轮 `db89dccb2` → 2695-db89dccb：仍然 500，但换了原因

超时消失（500 从 5037ms 降到 66ms），却还是 500：

```
ERROR: syntax error at or near "同属" (SQLSTATE 42601)
```

**根因**：getProvider 的 SQL 在 Go raw string 里，注释必须用 SQL 的 `--`。
那两行被误写成 Go 的 `//`，在 raw string 中就成了 SQL 正文。
Go 编译器不报错（字符串内容合法），`go vet` 不报错，单测全绿——**只有数据库会报错**。

### 判据盲区（真正的收获）

第一轮我写了三层判据，且都**没抓到**这次事故：

1. **真库门把 SQL 抄在测试文件里**。判的是抄件，不是被测物。抄件与 providers.go 的
   真实 SQL 从那一刻起已经不同，而线上跑的是后者。⇒ 改成 `getProviderSQLLiteral()`
   逐字取 providers.go 里的字面量，真库门直接执行它，判据从此不可能与被测物漂移。
2. 三条源码不变量只看视图名与关联条件，`//` 行不含这些 ⇒ 全部放行。
   ⇒ 新增 `TestGetProviderSQLLiteralHasNoGoComments`：SQL 字面量里不得出现以 `//` 开头的行。
3. 第一轮判据还会**对注释报错**：要断言的视图名恰恰被写进了那段「为什么不再读它」的注释里。
   ⇒ 「不得出现某视图名」这类断言必须先剥 SQL 注释。

### 第 2 轮 `f7de9944` → 2696-f7de9944：复验通过

```
GET /api/providers/14 → 200  error_rate_24h=0.0255  active_cred_count=4
                             available_model_count=58  display_name=MiniMax
```

154 日志中 `/api/providers/{1,13,14,18}` 连续全部 200。

## 四、判据与变异验证

`admin/providers_get_error_rate_test.go`，三层：

1. **SQL 字面量无 Go 注释**（默认跑）——本次事故的直接形态。
2. **源码不变量**（默认跑）——`getProvider` 的 SQL 不得出现
   `request_logs_with_current_month`、必须出现 `request_stats_minute`、
   error_rate 子查询必须关联 `p.id` 且保留 24h 窗口；5s ctx 预算本身被钉住。
3. **真库门**（`TEST_DATABASE_URL` 门控）——用 handler **同款 5s deadline** 跑
   **providers.go 里的真实 SQL**，并断言算出的 rate 等于独立重算值（防止
   「子查询什么都没查到、返回 0」假绿）。连接失败是硬失败、只有窗口真无数据才 skip，
   否则这条判据会在网络不通的机器上静默恒绿。

变异验证 **5/5 变红**：

| 变异 | 变红 |
| --- | --- |
| 换回旧 LEFT JOIN + canonical 视图 | 判据 1、2 |
| 只删 24h bucket 窗口 | 「必须保留窗口」 |
| 只删 provider_id 关联 | 「必须关联 p.id」 |
| 真库门改跑旧 SQL | 4.89s deadline exceeded（修复后 0.57s 通过） |
| **把 `//` 改回泄漏形态** | `HasNoGoComments` **与真库门同时**变红（真库门此前对这次事故是绿的） |

## 五、回归与遗留

- `go build ./...` 通过；admin 套件除 3 条 `V1Bodies*` 门外全绿。
  那 3 条用 **pristine worktree**（`git worktree add` 到 HEAD）复跑为 `ok`，证明是本地
  gitignored 的 `.audit-validation/`（677MB 脚手架目录）被遍历型判据扫进来，
  **与本改动无关**。属环境遗留，未在本轮改动（会改判据语义，超出本次范围）。
- 全站 310 个只读端点扫描：**并发跑出的 102 个非 2xx 里有大量是我自己造成的**——
  8 并发压在共享 PG 上。串行复测后，真正的超时族是**耗时卡在 10s/15s/30s 整数**的那批
  （session-analytics、data-lifecycle、auto-route、probe、users/usage-summary 等），
  形状是各自 handler 的 ctx 预算被打满，**属另一组缺陷，未在本轮修复**，如实登记。
- **AGENTS.md 里「252 上的数据库」需要更正**：PG 在 252 的 `172.16.2.210`，
  跑在 podman 容器 `pg-252-pg17`（`kx-citus-pg17:amd64`，已 Up 12 天）里，
  `systemctl list-units | grep postgres` 查不到。`log_min_duration_statement=1s`，
  `logging_collector=off`（日志走容器 stderr）。该 PG **与其他应用共用**
  （同实例上跑着 `daily_kline` 等非 llm_gateway 负载，慢查询日志里最长 110s 的
  语句都来自它们），网关侧查询耗时会被这类争抢放大。

## 六、顺带更正的一条本地事实

本机 checkout **有并发会话在写同一个工作区**（会话期间 `admin/request_logs_bodies_retirement_gate_test.go`、
`domains/streaming/*.go`、`go.mod` 等在不同时刻被我之外的进程改动）。因此：
- `go.mod` 的 puddle 依赖位移是我自己 `GOFLAGS=-mod=mod` 的副产物，已 `git checkout` 还原
  （回退后构建与测试照常通过，说明并非必需）；
- 提交只 `git add` 我自己的两个文件，没碰别人的半成品；
- `scripts/deploy-lib` 是指向 `../../deploy-lib` 的**符号链接、独立仓库**，不在本仓 git 内。
  我曾按错误诊断（bash 5.2 空数组）改过 `ssh-retry.sh`，已在其仓库内 `git checkout` 还原。
  **真实原因是**：`deploy-154.sh:81` 用 `exec bash` 重新执行，会取 PATH 上的 `bash`，
  系统只有 3.2（3.2 对 `set -u` 下的空数组 `${arr[@]}` 报 unbound variable，5.2 不会）。
  ⇒ 部署必须把 `~/sdk/bash5/bin` 放在 PATH **最前**，而不是只改调用方式。

## 七、第三轮：并发部署把修复顶掉了（回归 → 再修复）

部署成功复验之后，**并发会话用 `9c43da60` 又部署了一次 154**，该提交基于
`61a3e3abe`（早于本修复），于是 `/api/providers/14` **当场回归**：

```
version=2.5.8-9c43da60-20261017-2715
GET /api/providers/14 → 500  5.073974s
```

更早一步：并发会话还执行过 `git reset: moving to 61a3e3abe`
（`git reflog` HEAD@{7}），把 `db89dccb2`/`f7de9944` 从分支上抹掉了——
当时 `git merge-base --is-ancestor <我的提交> HEAD` 已经是 NO，
而提交仍在 reflog 里。`git merge origin/main` 会「干净地」把我的修复合没了
（因为它们的 providers.go 与 merge-base 同形，三方合并取了对侧版本）。

处理：
1. `git merge --abort` → `git cherry-pick db89dccb2 f7de9944` 把修复取回；
2. 验证修复在新基线上仍然成立（`WHERE s.provider_id = p.id` 与 24h 窗口都在）
   ——**注意**：合并后源码里 `request_logs_with_current_month` 仍出现 1 次，
   但那是在「为什么不再读它」的注释里，判据剥注释后才断言，不受影响；
3. 提交 `db84e1e7b`（真库门自身的 arity 缺陷，见下）；
4. `git push origin main` → `d82a9fb06..0d7de6e7b`；
5. 第三次部署 `2697-0d7de6e7` → 复验 **200 / error_rate_24h=0.0300**。

⇒ **教训**：共享分支上「本地绿 + 已部署」不等于「主线有落点」。修复必须
`git merge-base --is-ancestor <commit> origin/main` 验过才算数；别人一次
reset 或一次基于旧基线的部署就能把它弄没，而生产不会告诉你。

### 判据自身的第三个缺陷（真库门 arity）

判据改成执行真实 SQL 后立刻暴露：那条 SQL 返回 **25 列、类型不一**，
而测试只 `Scan(&got)` 一个目标 ⇒ `number of field descriptions must equal
number of destinations, got 25 and 1`。**那是判据自己的问题，不是被测行为。**

改法：`rows.Values()` + **断言列数**（`providerDetailColumnCount=25`）。
error_rate_24h 按下标 23 定位，SELECT 列表一变就可能读到别的列而断言仍
「看着通过」——与判据纪律第 3 条同源。变异：把常量改 24 → 真库门立即报
`returned 25 columns, expected 24`。

## 八、第二族慢接口（同源，未在本轮修复）

串行复测（排除并发压测干扰）后确认的**真实**超时族——耗时卡在 10s/15s/30s
整数上，那是各自 handler 的 ctx 预算被打满，不是随机抖动：

| 端点 | ctx | 错误（154 日志） |
| --- | --- | --- |
| `/api/admin/usage/cache-economics` | 10s | `cache-economics query failed` @ `usage_enhanced.go:712` |
| `/api/admin/session-analytics` | 15s | `session analytics list failed` @ `session_analytics_handler.go:358` |
| `/api/admin/users/usage-summary` | 15s | `query failed` |
| `/api/admin/attachments/stats` | 10s | `attachments: stats query failed` |
| `/api/admin/dashboard/session-health` | 15s | `failed to query health distribution` |
| `/api/admin/dashboard/session-overview` | 30s | 同上 |
| `/api/admin/dashboard/session-trend` | 15s | `failed to query session trend` |
| `/api/admin/data-lifecycle/{stats,storage/tables,blobs/top}` | 15–30s | `query failed` / `查询表大小失败` |
| `/api/admin/auto-route/{index,audit,analytics/flow,analytics/matrix}` | 10–15s | `internal error` |
| `/api/admin/turns/sessions/filter-options` | 12s | `query filter options failed` |
| `/api/admin/probe/system-health` | 55s | （能返回但远超预算） |

**不与 providers/14 同源**：这些不是那个 canonical 视图。
`admin/user_usage_stats.go:23` 已注明「放弃 request_logs_with_current_month」，
改读 `usage_facts`；慢的是 `usage_facts` 自身与 session_* 表族。
`request_logs_with_current_month` 在 `admin/` 下有 734 处引用、跨 143 个文件，
全量治理是独立工程，**本轮不做，如实登记**。

另：`/api/credentials/monitor-summary` 的 `mode=core` 正常（200 / 2.8s），
但 `mode=full|light|all` 均 500 @ 15s——同族。

## 九、浏览器 GUI 取证：未完成（如实登记）

用户已在内置浏览器完成登录（标签跳到 `/providers/14`，外壳渲染出
「系统管理员」与版本徽章 **#2697**）。但页面主体持续停在
「正在检测登录状态…」/「加载中…」，且：

- 同一时刻 `/api/providers/14` 在**网关访问日志里是 200 / 29ms**（浏览器 UA）；
- Browser 的 `network` 作用域为空（`lastSequence: 0`），`console` 无条目；
- 该标签连续操作数次后劣化到 275s `BROWSER_OPERATION_TIMEOUT`。

⇒ 判为**观测通道劣化**，不是页面缺陷：同一批端点在服务端与 curl 侧全绿。
按 LESSONS「渲染侧定不下来时退回既定口径，以网关访问日志 + 直测 API 定案，
并如实标注渲染未取证」，本轮的页面数据结论**以网关日志 + API 直测**为准，
逐页截图证据**未取得**。

期间发现的真实信号（来自网关日志，非浏览器）：该页同批请求里
`/api/credentials/monitor-summary?mode=core` 出现 **500 / 104ms**（快速失败，
非超时），curl 复测却 200 —— 未能稳定复现，记为待查。

## 十、第二族的共因：backfill gauge「证明无事空扫」空转到超时（未修，待 owner 决策）

上一节那 15 个端点不是各自独立的问题。追到数据库侧找到一条**每 tick 必烧满
预算、却从未产出过任何指标**的后台查询。

### 现场

`domains/session/v2/session_request_status_backfill.go:498` 的
`sessionRequestStatusRemainingSQL`（remaining gauge）：

```sql
SELECT (SELECT count(*) FROM public.session_turns t
        JOIN public._request_logs l ON l.request_id = t.request_id
       WHERE t.request_status IS NULL AND l.request_status IS NOT NULL)
     + (SELECT count(*) FROM public.session_turns_hot t
        JOIN public._request_logs l ON l.request_id = t.request_id
       WHERE t.request_status IS NULL AND l.request_status IS NOT NULL)
```

它**刻意没有时间窗**（注释原文：*the semantics (window-less, both surfaces,
-1 on failure) are untouched*）。

154 当前实例（`gateway-canary-8782.log`，00:56 启动）该 worker 的全部日志形态：

| 形态 | 次数 |
| --- | --- |
| `starting` | 2 |
| `stopped` | 1 |
| `remaining count failed` | **20** |
| 任何成功行（`remaining=`） | **0** |

末次失败 06:30，**5.5 小时内零成功**。这不是"偶发超时"，是**从未成功过**——
而它每次都要把预算烧完。

### 根因（三层，逐层实测）

1. **统计从未采集**。`session_turns_2026_09`（**798,278 行 / 2.7GB**）
   的 `last_autoanalyze` 为 NULL、`n_live_tup=0`。规划器只能估 2348 行并
   改用 pkey → Nested Loop，每个外层行还要探测 `_request_logs` 的 5 个分区。
   我手工 `ANALYZE` 该分区，**耗时 13 分 37 秒**（本身即采样代价的证据）。

2. **但 ANALYZE 并不解决问题**。重新 EXPLAIN 得到诚实计划
   （`session_turns_2026_09` 上 `rows=330544`，改为 Parallel Hash Join，
   cost 202322），实测该查询**仍然 60s 超时**。真值是：
   **该分区有 330,544 行 `request_status IS NULL`**，必须与 `_request_logs`
   做哈希连接。数据量本身就是不可承受的。

3. **而这 330k 行按代码自己的注释是死重**。`session_request_status_backfill.go:46-55`
   已经为**候选查询**做过同一件事的修复并写明理由：
   > 可回填候选的存在窗口 = 「823 之后写入 + promote 保留期（8h）」——小时级；
   > 7 天是 ~21 倍安全系数的滚动窗口。**窗外行 = 823 之前的 v1 历史行**
   > （252 实测 793,519 行，全部 partition_date ≤ 2026-09-30，
   > 且 `_request_logs` 源行已被 TTL 清除——「结构性无法回填」族），
   > **排除它们不改变可回填集合**

   候选查询拿到了 7 天窗，**gauge 没有**。2026_09 正是那一族。

### 实测对照（252 生产库）

| 形态 | 结果 |
| --- | --- |
| A 现行（无窗） | **60s 超时，从未完成**（另一次 120s 同样被杀） |
| B 加 7 天窗（与候选同窗） | **7 秒完成，返回 0** |

`0` 是正确答案：回填在实际作用域内已无剩余。

### 为什么本轮没有直接改

这是一条**发布闸门指标**的语义。R31（2026-10-10）明确保留过
"window-less, both surfaces, -1 on failure"，并以「849 覆盖部分索引 +
`SET LOCAL` 提预算」作为可完成性的依据——**本轮的实测否证了这个依据**：
优化计划不等于查询能完成，那次"修复"从未验证过端到端效果。

加窗会把指标从"全量 NULL 行里还剩多少可回填"改成"回填实际排空的窗口内
还剩多少可回填"。按代码自己的论证两者在**可回填集合**上等价（窗外结构性
不可回填），但这是 release gate 的口径变更，应由 owner 拍板，不在本轮单方面改。

**建议 patch**（待拍板）：给 `sessionRequestStatusRemainingSQL` 的父表臂
加 `AND t.partition_date >= (now() - interval '7 days')::date`，与
`sessionRequestStatusCandidateSQL` 保持同窗；`_hot` 臂不加（hot 表仅
6,193 行 / 93MB，且其 NULL 行是 pre-823 进程写入的"退役前仍存在"的那些，
保持覆盖正是 R31 反对过的那个盲区）。

**风险与收益**：收益 = 每 3 分钟省下一次满预算空转、并让一个从未有值的指标
开始有值；风险 = 指标口径变化（窗外那 33 万结构性死行不再计入）。
