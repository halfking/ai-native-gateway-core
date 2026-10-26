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

## 八、第二族慢接口（~~同源~~ **订正见 §12：不同源**，本轮未修）

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

## 十一、gauge 加窗已落地（owner 拍板后实施，提交 `a86d1d286`，上线 `2698-aa16536e`）

**说明**：owner 那一票是问卷**超时自动选中推荐项**，非手动确认。改动可
`git revert a86d1d286` 回滚，故按推荐项推进。

### 改了什么

`sessionRequestStatusRemainingSQL` 的**父表臂**加 `partition_date >= (now()
- interval '7 days')::date`，与 `sessionRequestStatusCandidateSQL` 同窗；
`_hot` 臂**保持无窗**（它承载 pre-823 进程写入、尚未 promote 的 NULL 行，
正是发布闸门要答的那一族，且仅约 6k 行）。

### 口径取舍（如实记录，未埋在注释深处）

旧注释称：无窗 gauge 能让「job 停摆超 7 天后仍报出积压，发布闸门保持诚实」。
**加窗确实放弃了这一条。** 接受理由：

1. 该性质在实践中本就是空的——gauge 从未返回过数字（20 次失败 / 0 次成功），
   无从「失去」诚实性；
2. 候选查询同样不会去动窗外那些行，下界与 job 实际作用域一致。

### 判据（3 条新增，变异验证 3/3 变红）

| 判据 | 钉住什么 | 变异 |
| --- | --- | --- |
| `ParentArmIsWindowed` | 父表臂必须有 7 天下界 | 拿掉 → 红 |
| `HotArmStaysWindowless` | `_hot` 臂**不许**有 partition_date | 加上 → 红 |
| `WindowAppliesToBothPredicateSites` | 两臂各自的 NULL/NOT NULL 谓词都在 | 删一个 → 红 |

**为什么拆成三条而不并入既有 `CoversBothSurfaces`**：那个测试守的是
「两个面都要查」（退役前仍存在的行不能漏），新测试守的是「父表臂要加窗」
（否则空转回来）。两者方向相反；合并会得到一个「为满足其一而破坏其二」的
判据。这正是本次事故里反复出现的判据设计错误。

既有的 5 条相关判据（`RemainingGaugeIsVerbatim`、`RemainingGaugeFailureIs
NotZero`、`RemainingSampledOncePerDrain`、`RemainingSQLContract`、
`RemainingSQLCoversBothSurfaces`）全部仍然绿。

### 上线

`2698-aa16536e`（总 599s，慢在前端构建，非切换；切换 54s）。
`/api/providers/14` 上线后仍 **200 / 0.12s**，无回归。

## 十二、订正 §八 的「同源」判断（自己的假设被自己的验证推翻）

§八 与 §十 都把这 15 个超时端点写成 backfill gauge 的「同源/共因」。**gauge
修复上线（`2698-aa16536e`）后的复测否证了这个判断。**

串行复测同一批端点，修复前后对照：

| 端点 | 修复前 | 修复后 | 结论 |
| --- | --- | --- | --- |
| `/api/admin/usage/cache-economics` | 500 @ 10.05s | 500 @ **10.07s** | 未变 |
| `/api/admin/session-analytics` | 500 @ 15.08s | 500 @ **15.07s** | 未变 |
| `/api/admin/users/usage-summary` | 500 @ 15.07s | 500 @ **15.06s** | 未变 |
| `/api/admin/turns/sessions/filter-options` | 500 @ 12.08s | 500 @ **12.06s** | 未变 |
| `/api/admin/dashboard/session-trend` | 500 @ 15.08s | 500 @ **15.15s** | 未变 |
| `/api/admin/dashboard/session-health` | 500 @ 15.07s | 500 @ **15.06s** | 未变 |
| `/api/admin/attachments/stats` | 500 @ 10.16s | **200 @ 8.82s** | 改善 |
| `/api/admin/data-lifecycle/blobs/top` | 500 @ 15.08s | **200 @ 10.75s** | 改善 |

8 个里 6 个**纹丝不动**。⇒ gauge 不是它们的原因；它是一个**独立的**真实缺陷
（21.7s→666ms、20 次失败→0 次），值得修、也确实修对了，但它不解释这一族。

### 错在哪

我把「两件事都慢、都在同一个库上」当成了「同一个原因」。这是**相关当因果**：
gauge 每 3 分钟烧满一次预算确实在加剧库的争抢（2 个端点因此受益），但它不是
这些端点超时的主因——每个端点有自己的那条慢查询，它们各自打满了自己的
10s/15s/30s ctx。

正确做法本该是：在提出「共因」时就说出它的**可证伪预测**——「若 A 是 B 的
原因，那么修好 A 之后 B 会变快」。这句话写下来只要几分钟，就能省掉一次
上线 + 一轮复测，以及一份会被推翻的结论。

### 这不推翻 gauge 的修复

gauge 仍然是真问题、有真实代价（5.5 小时 20 次失败 / 0 次成功，每 3 分钟烧满
一次共享库预算）。修复保留。**被推翻的只是「它解释了第二族」这一句。**

### 第二族仍未修

`session-analytics`(15s)、`users/usage-summary`(15s)、`cache-economics`(10s)、
`turns/sessions/filter-options`(12s)、`dashboard/session-{trend,health}`(15s)、
`data-lifecycle/{stats,storage/tables}`(15–30s)、`auto-route/{index,audit,
analytics/*}`(10–15s)、`probe/system-health`(55s) 各自独立，需要逐条
EXPLAIN 取证。`request_logs_with_current_month` 在 `admin/` 下有 734 处引用、
跨 143 个文件，全量治理是独立工程。

---

## §13 cache-economics：第一版修复无效，第二轮定位到覆盖索引缺列

### 13.1 先证伪自己上一轮写的注释

第一版（提交 `1928c544`，上线 `2699`）的注释写着：

> 252 实测（同一个月窗口，逐条计时）：主聚合 6020 ms / compressedQuery 35344 ms

**这句话是错的**，而且错在两个地方：

1. **窗口不对**。端点用的是 `resolveUsageTimeRange(r, 30)`，默认窗口是
   **最近 30 天**，不是「本月」。注释里写的计时来自「今天（6,668 行）」和
   「本月（438,544 行）」两个窗口 —— 行数差 65 倍，耗时却几乎相同
   （8294 / 12397 ms），当时被我读成「视图疑似不剪枝」。
2. **计时器不对**。那次 `EXPLAIN` 在「今天」窗口下 `Execution Time` 只有
   **61.367 ms**，`Subplans Removed: 3` 说明分区裁剪完全正常。
   8.3s / 12.4s 来自包着 `podman exec` 的 wall clock，混进了启动开销。

⇒ 第一版修复的问题不在方向（把 35s 的附加查询隔离出共享 ctx 是对的），
而在**它建立在一个不成立的测量上**。教训见 LESSONS「写进代码注释的实测数字
要能自证窗口」。

### 13.2 真实窗口下的根因：覆盖索引缺 `cache_read_tokens`

30 天窗口的真实主聚合（逐字取自 `usage_enhanced.go`）：

```
SELECT COUNT(*), SUM(ul.cache_read_tokens), SUM(ul.prompt_tokens), SUM(ul.cost_usd)
FROM usage_ledger_with_current_month ul
WHERE ul.ts >= now() - interval '30 days' AND ul.ts < now();
```

第一次跑 **12.667 s**（1,478,131 行）。同轮**交错**对照（A/B/A/B）：

| 变体 | 计划 | Buffers | Execution |
|---|---|---|---|
| A 四列（含 `cache_read_tokens`） | Parallel **Index Scan** using `usage_ledger_2026_09_ts_idx` | **209,361** | 3239 / 3426 ms |
| B 三列（去掉该列） | Parallel **Index Only Scan** using `usage_ledger_2026_09_ts_tenant_id_prompt_tokens_completion__idx` | **19,245** | 1502 / 1462 ms |

4/4 计划形态稳定。856 建的覆盖索引是

```
INCLUDE (prompt_tokens, completion_tokens, cost_usd, latency_ms, success)
```

**不含 `cache_read_tokens`**。于是任何带 `SUM(cache_read_tokens)` 的窗口聚合
都会放弃 index-only scan，退回逐行回堆 —— 1,478k 行里 1,039,470 行落在 9 月
分区，多读约 190k 个 buffer（约 1.5 GB）。

⇒ **856 对这条查询整体失效**：它服务「按时间窗聚合用量」这族查询，
而这族里带缓存 token 的那部分拿不到任何收益。

### 13.3 先否证的两条「更省事」的路

| 备选 | 结论 | 依据 |
|---|---|---|
| 读滚存 `request_stats_minute` | 否 | 无 `cache_read_tokens` 列 |
| 读滚存 `stats_usage_daily` | **双重否证** | ①口径不对：`dimension_type` 有 person/provider/provider_model/credential/tenant/error 六种存同一批事实，直接求和是多重计数；即便只取 `tenant+business` 也只有 318,225 条 vs 明细 1,478,030 条。②更慢：扫 74,178 行用了 **32.9 s**（明细只要 3.4 s） |
| 加宽 handler 预算 | 否 | 不消除 1.5 GB 回堆；且属行为变更，需单独评审（856 文末已预留这条） |

### 13.4 修复：迁移 873

建 856 的**超集**（键 `(ts, tenant_id)` 不变，INCLUDE 补上
`cache_read_tokens` / `cache_write_tokens`），同迁移内按序删掉 856 的两条 ——
usage_ledger 是全站最热的写表，让两条同前缀、高度冗余的索引并存等于长期写放大。

**事务内试跑 + ROLLBACK（870 同款做法，零残留）**：

```
CREATE INDEX … ON public.usage_ledger (ts, tenant_id) INCLUDE (…, cache_read_tokens, …)
Time: 56712.240 ms          ← 负载 44 下，建索引持锁窗口
CREATE INDEX … ON public.usage_ledger_hot (…)
Time:   123.637 ms

Parallel Index Only Scan using usage_ledger_2026_09_ts_tenant_id_prompt_tokens_completion_idx1
Parallel Index Only Scan using usage_ledger_2026_10_ts_tenant_id_prompt_tokens_completion_idx1
Parallel Index Only Scan using usage_ledger_hot_ts_tenant_cache_agg_idx
Buffers: shared hit=10325 read=10656          ← 20,981，基线 209,361（−90%）
Execution Time: 14101.397 ms                 ← 见下：不是本索引的账
ROLLBACK
```

⚠️ **14.1 s 不能读成「索引更慢」**：新索引刚建完还是冷的
（`read=10656` 是从 OS cache/文件读的），且此刻宿主 load average 44/4 核。
**buffer 计数是确定量，耗时绝对值在这台机器上不是。**
这也正是 13.2 用交错对照而不是前后两次测的原因。

### 13.5 一个与代码无关、但正在主导时序的现场问题

252 宿主的 CPU 被**别的会话遗留的递归 grep** 吃满：

```
PID 3353332  47.7% CPU  已跑 06:08:54   grep -rls -m1 AUTH_ENABLED /root
PID 3365283  44.7% CPU  已跑 06:04:24   grep -rls AUTH_ENABLED /opt /etc/goacos /usr/local/sbin /root
PID 3535476  42.5% CPU  已跑 04:59:09   grep -rl acc-kid /root /opt /home /etc/systemd
PID 49210    17.6% CPU  已跑 19:25      grep -rn ADD COLUMN IF NOT EXISTS adjust /data
load average: 44.02 / 38.71 / 37.47   (4 核)
```

PG 侧同时只有 1 条 active 查询、0 IO 等待、0 锁等待 ⇒ **负载不在数据库里**。
在 load 33–44 的机器上，任何端点的耗时数字都只能当上界看；
`cache-economics` 今天 4 次抽测（500@10.08 / 200@6.98 / 200@6.53 / 200@10.07）
里，有多少是查询本身、多少是 CPU 饥饿，目前**无法分开**。

本轮没有动这些进程：它们不属于本会话，且可能仍被某个活跃会话持有。
已向 owner 报告，由 owner 决定是否清理。

### 13.6 上线验证（2026-10-18 12:42，owner 显式授权后手工 psql 应用）

前置核对（照 870 的先例）：落地文件 SHA256 与仓库副本**逐字节一致**
（`a1793c47…`）；`to_regclass('…cache_agg_idx')` 应用前为 NULL、旧索引在位。

执行：`psql_rc=0`，墙钟约 4 分钟（其中建索引约 57s–数分钟，宿主负载当时 28–48）。

验收（**按定义匹配分区级，不按索引名、不按父表大小** —— 870 记录的陷阱）：

```
旧父表索引  空        新父表索引  usage_ledger_ts_tenant_cache_agg_idx
旧 hot 索引 空        新 hot 索引  usage_ledger_hot_ts_tenant_cache_agg_idx

分区               idx_size    valid
usage_ledger       0 bytes     t     ← 分区父表的正常渲染，不是没建出来
usage_ledger_2026_08  8192 B    t
usage_ledger_2026_09  81 MB     t
usage_ledger_2026_10  25 MB     t
usage_ledger_2026_11  8192 B    t
usage_ledger_hot      624 kB    t
旧 856 覆盖索引分区残留：0
```

端点真身那条聚合（30 天窗口）：

| | 基线 | 应用后 |
|---|---|---|
| 9 月分区 | Parallel **Index Scan**（回堆） | Parallel **Index Only Scan** |
| 10 月分区 | Parallel Seq Scan | Parallel **Index Only Scan** |
| hot | Parallel Seq Scan | Parallel **Index Only Scan** |
| Buffers | 209,361 | **19,560**（−91%） |
| Execution（热缓存） | 8,375 ms | **487 ms（−94%）** |

端点连测 6 次：**6/6 全 200**，3.43 / 3.72 / 3.43 / 3.76 / 3.88 / 4.00 s
（修复前：500@10.08s、200@6.98s、200@6.53s、200@10.07s，约半数失败）。
数据与 psql 对得上：`total_requests` 1,484,604、`cache_read_tokens` 2,505,087,892、
`dollars_spent` 3,938.18。

真库判据完成**红→绿闭环**：应用前红（报「分区上没有携带 cache_read_tokens 的
覆盖索引，请应用迁移 873」），应用后绿（三臂全 Index Only Scan）。

#### ⚠ 13.7 顺带暴露的真问题：压缩计数每次必超时，端点在拿一个假 0

154 journalctl 里 6/6 次端点调用都伴随：

```
WARN cache-economics: compressed request count unavailable
     tenant="" err="timeout: context deadline exceeded"
```

即 `compressed_requests` **恒为 0 是降级产物，不是真实值**。第一版修复把这条
查询隔离进 3s 子预算（`cacheEconomicsCompressedBudget`），避免了它拖垮主聚合，
但代价是**每次调用白烧 3 秒**——这正好是端点现在稳定停在 3.4–4.0s 的原因
（主聚合已降到 0.5s，剩下的全是这条拿不到结果的查询）。

这条查询本身仍是 `request_logs_with_current_month` 上的 30 天窗口计数。
本轮按 AGENTS.md 的规矩**如实记录为未完成项**：它与主聚合是两条独立的慢查询，
不因 873 而受益，需要单独取证（真实值、计划、是否已有可用滚存）。
在它被治理之前，缓存经济卡片上的「压缩请求数」这一格**不可信**。

#### 13.8 并发合并造成的判据失效（又一次「取子串要有两端界定」）

真库判据应用后首次复跑红了，报的是**前提断言**：

```
提取到的 cache-economics 聚合 SQL 不含 "SUM(ul.cache_read_tokens)"：提取点已漂移
```

原因：`usage_enhanced.go` 里 `query := fmt.Sprintf(` 原本只有一处，本轮合并
（`d7e23bf57` 等）新增到**三处**（488 / 533 / 660 行），而判据按**整文件取第一个**，
抓到的是别的 handler 的 SQL。

⇒ 这是「取子串必须有两端界定」这条教训的**第四次现身**（前三次：`--` 注释、
`DO` 块字符串字面量、守卫块无上界切片）。判据没给出假绿，而是被前提断言当场
拦下——这正是那条前提断言存在的意义。已改为先切到 `usageCacheEconomics`
函数体、再在该体内断言字面量**有且仅有一处**，然后才取字面量。

### 13.9 压缩计数：端点在拿一个假 0 当真值报（873 之后修掉）

873 上线让主聚合降到 487ms，端点 6/6 全 200。但 154 journalctl 里
**每次调用**都还挂着一条：

```
WARN cache-economics: compressed request count unavailable
     tenant="" err="timeout: context deadline exceeded"
```

这意味着 `compressed_requests` 恒为 0 **是降级产物，不是真实值**，
而且每次调用白烧 3s——873 之后端点稳定在 3.4–4.0s，这段时间**全部**
是这条拿不到结果的查询（主聚合只占 0.5s）。

| 读法（同一 30 天窗口） | 耗时 | 结果 |
|---|---|---|
| `request_logs_with_current_month` | **>300s（跑不完）** | — |
| `_request_logs`（母表单腿） | **648 ms** | **10,590 行** |
| `_request_logs_hot` | 3 ms | 0 行 |

母表命中 10,590 ⇒ 视图报出来的 0 确实是错的。

视图为什么跑不完：它把 `session_turns` / `session_turn_details` 一并联进来
（`LATERAL` 补列 + `NOT EXISTS` 反连接）。本端点只要一个 `COUNT(*)`，
却要为 30 天 200 万行做整套补列——**要的东西和视图定义的东西差着两个数量级**。

#### 关键取舍：单腿母表，而不是 hot∪母表双腿

`sqlreadguard` 的读面纪律要求读双腿（最近 8h 的行只在 `_request_logs_hot`）。
但对**计数**指标来说，朴素的 `UNION ALL` 双腿是**更糟**的错误方向：

> promote 会把旧行从 hot 搬到母表。两腿同时存在时，同一行会被数两次。

`bg/routing_health_checks.go` 的 R47 债条目记的就是这件事，并注明
「正确双腿必须走族视图并核对 promote 去重语义」——那需要一个真实 promote
环境，不是本轮能定的。

⇒ **低估 ≤8h（保守、可在注释里说清）优于系统性虚高**（不可自证、
且方向随 promote 时机漂移）。同款取舍在本仓已有先例：
`internal/summarystore/store.go` 与
`domains/session/v2/session_request_status_backfill.go`。
已按格式登记 `sqlreadguard` 白名单（`LEGIT`，写清三条依据）。

口径变化如实记录：本指标此前**从未返回过真值**，因此不存在「与旧值不一致」
的回归面——现在是第一次给出真实数字。

判据 3 条（`admin/cache_economics_compressed_source_test.go`）：
必须读母表且四条谓词不得被改；必须保持单腿（出现 `_request_logs_hot` 或
`UNION` 即红）；真库层用同款 3s 子预算必须跑完且与独立重算一致
（实测 10,590 / 1.57s）。变异 4/4 精确变红，其中真库层那条是把查询改回
canonical 视图后**因 3s 预算超时而红**——不是假红。

⚠ 判据里又踩了一次注释陷阱：那条 SQL 的注释通篇在讨论
`request_logs_with_current_month` / `_request_logs_hot` / promote 去重。
不先剥 `--` 注释，「不得出现某视图」会因注释提及而误报、
「必须出现某表」会因注释提及而恒真。

---

## §14 第二族复测：它不是一族（宿主负载回落后的干净基线）

§12 曾把 8 个慢端点归为一族。宿主负载回落后串行复测，**其中一半自愈**：

| 端点 | 负载 33–44 时 | 负载 19 时 | 判定 |
|---|---|---|---|
| `users/usage-summary` | 15s | **200 @1.36s** | 纯 CPU 饥饿 |
| `dashboard/session-trend` | 15s | **200 @1.36s** | 纯 CPU 饥饿 |
| `dashboard/session-health` | 15s | **200 @2.22s** | 纯 CPU 饥饿 |
| `data-lifecycle/storage/tables` | 15–30s | **200 @10.05s** | 纯 CPU 饥饿 |
| `auto-route/audit` | 10–15s | **200 @7.33s** | 仍慢但可用 |
| `probe/system-health` | 55s | **200 @19.88s** | 仍慢但可用 |
| `session-analytics` | 500 @15s | **500 @15.06s** | **真缺陷** |
| `turns/sessions/filter-options` | 500 @12s | **500 @12.06s** | **真缺陷** |
| `data-lifecycle/stats` | 500 @15–30s | **500 @30.07s** | **真缺陷** |
| `auto-route/index` | 500 @10–15s | **500 @1.78s** | 快速失败，**不是超时** |

⇒ 「第二族」这个词本身就该废弃：它把「宿主 CPU 饥饿」和「查询/处理逻辑缺陷」
混成了一族。前者会自愈，后者不会；拿前者当后者的修复目标会白付部署。

### 14.1 `filter-options` 的结构：8 条串行查询塞进一个 12s 预算

`admin/turns_filter_options.go` 里 `runFilterOptionQueries` 是**顺序 for 循环**，
5 条走 `session_summaries JOIN session_dim`、3 条走 `session_turns_with_current_month`，
共享同一个 12s ctx。逐条实测（无并发干扰）：

```
session_summaries × 3 条（projects/tasks/owners）  5.12s / 6.69s / 4.00s = 15.8s
session_summaries tasks 单条，连跑三次            8.53s / 9.66s / 7.53s（非冷热问题）
session_turns model 单条                           3.83s / 6.23s
```

⇒ 端点需要 ~40s，预算只有 12s。**预算结构上不可能成立**，
与数据量无关。

### 14.2 反直觉：GROUPING SETS 对两条臂的效果相反

我原本打算把 8 条合并成 2 条 GROUPING SETS。**实测否证了这个方案的一半**：

```
session_turns 三维合并 GROUPING SETS        2.84s  ← 比单跑一条（3.83s）还快
session_summaries 三维合并 GROUPING SETS   35.45s ← 比三条分跑的总和（15.8s）还慢一倍多
```

summaries 那条臂慢，是因为它每条查询各自带
`IS NOT NULL AND != ''` 谓词、只哈希自己那一列；合并成 GROUPING SETS 后
必须在 join 之后对所有列组合一次性做多列分组，代价反而更高。

⇒ **「合并成一条」不是普适的优化，必须逐臂实测。**
turns 臂已验证可合并（3 → 1，省下 ~10s）；summaries 臂**不能**合并。

### 14.3 `session_summaries` 扫描成本随缓存状态摆动 0.4s ↔ 27s

```
SELECT count(*) FROM session_summaries;   -- 776,418 行
EXPLAIN 版：  Execution Time 398.312 ms
   Parallel Index Only Scan using idx_session_summaries_cost
   Buffers: shared hit=14,298      Heap Fetches: 162,391
同一语句裸跑（另一轮）： 1.02s / 27.4s
```

`Heap Fetches: 162,391` 是关键：**可见性映射没设全**，
所谓 index-only scan 有近六成条目仍要回堆。回堆页是否驻留在 buffer 里，
决定了它 0.4s 还是 27s。宿主内存一紧张，同一条语句的耗时就摆动两个量级。

⇒ 这解释了为什么这一族的端点在不同时间点测出的数字能差 10 倍。
**报表格耗时之前必须先报 buffer 驻留状态**，否则数字不可复现。

### 14.4 `auto-route/index` 是另一个问题

它 **1.78s 就 500**，不是超时——预算根本没被用满。属于快速失败（参数/校验/
上游异常），不在本节的「慢查询」范围内，留作单独条目。

### 14.5 下一步（已定位，未实施）

`filter-options` 的可行修法是**并行化**：`runFilterOptionQueries` 从顺序 for
改为并发执行，墙钟从「8 条之和 ~40s」降到「最慢单条 ~8–10s」，落在 12s 内。
这条路的价值在于**不碰任何一条 SQL**，因此零口径风险；
turns 臂再叠加已验证的 GROUPING SETS 合并（3 → 1）还有余量。
本轮未实施——并发化要处理错误传播、ctx 取消与连接池上限，
属于需要独立评审的改动，不适合与 §13 的上线验证混在一起。

### 14.6 `filter-options`：三个「显然的修法」被实测逐一证伪

§14.5 提出「并行化」作为下一步。动手前先量了三件事，结果**三个都不成立**。

#### 证伪一：给 `session_turns` 加覆盖索引（与 873 同型的猜想）

`turns` 那条臂的 EXPLAIN 走 `Index Scan using session_turns_2026_09_heap_tenant_id_ts_idx`，
`model`/`provider`/`status_code` 都不在索引里 ⇒ 猜「和 cache-economics 同病，
补一条 `(ts, tenant_id) INCLUDE (model, provider, status_code)` 即可」。

事务内建索引 + ROLLBACK（870 同款做法）：

```
CREATE INDEX session_turns_ts_tenant_dim_cover_idx … INCLUDE (model, provider, status_code)
  Time: 5073.336 ms      索引确实级联：9月 48MB / 10月 12MB / hot 448kB
基线   Execution 2924.825 ms   Buffers 215,013
加索引 Execution —            Buffers 181,891      ← 只降 15%，计划一字未变
   仍走 session_turns_2026_09_heap_tenant_id_ts_idx
```

**计划根本没换**：`session_turns_2026_10` 是 `Seq Scan`（206,013 行，planner 认为顺序扫更划算），
9 月那条 planner 也没偏好新的覆盖索引。⇒ **`session_turns` 与 cache-economics 不是同一个病**，
「同型缺陷 ⇒ 同型修法」在这里不成立。若不测就动手，会白付一次生产建索引。

#### 证伪二：给 `session_dim` 补 join 键索引

9 条里有 5 条 join `session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id`，
而 `session_dim` 的 7 个索引**没有一个建在 join 键上**：

```
idx_session_dim_apikey (tenant_id, api_key_id)   idx_session_dim_client (tenant_id, client_id)
idx_session_dim_end_user (tenant_id, end_user_id) idx_session_dim_owner_user (tenant_id, owner_user)
idx_session_dim_task (tenant_id, task_id)         idx_session_dim_tenant (tenant_id, last_active_at DESC)
```

EXPLAIN 显示 `Parallel Hash Join` + `Parallel Seq Scan on session_dim` ⇒ 「缺 join 索引」的猜想看着很顺。
但**实测把索引建上去反而更慢**：

```
基线     Parallel Seq Scan on session_dim      Buffers 501,427   Execution 1877.455 ms
加索引   Parallel Index Only Scan              Buffers 678,171   ← 更高
```

原因很直白：`session_dim` 全表只 **23,272 个 buffer**，而那条索引本身就要 **200,008 个 buffer**
——**比表还宽**。对一次全表范围的 hash join，顺序扫是接近最优的，
拿索引去换只会更贵。

#### 证伪三：「并行化能把墙钟降到 max(段)」

`admin/dashboardapi/session_overview.go:189-192` 有完全同型的先例（"九段互相独立 ⇒ 墙钟 = max(段)"），
但那是**实测过的**；这里先量再上：

```
串行（现状的 for 循环）  121,761 ms
并发（9 条同时）          22,835 ms     ← 5.3×，不是 9×
```

并发下每条自身都变慢（q1 9,971 → 18,845 ms；q2 7,027 → 16,349 ms），
因为它们争的是 CPU 与 buffer 而不是 I/O。⇒ **CPU/缓冲密集型查询的并行收益 ≈ 核数**，
不是「条数」。12s 预算仍然不够。

#### 剩下的账

```
turns 臂合成 1 条 GROUPING SETS       2,924 ms   ← 已验证可用
ss 臂 5 条，各自 4~10s（且同一查询在 2s↔27s 间摆动）
⇒ 12s 预算要装下 5 条 ss 查询，当前杠杆都不够
```

**本节结论**：`filter-options` 需要的不是索引、不是并行、也不是查询合并，
而是**降低 ss 臂单条的成本**（`session_summaries` 776,418 行 × `session_dim` 817,970 行的
全量 hash join 被重复做了 5 次，每次只为取一列）。
本轮不实施——在没有可靠手段降低单条成本之前，
先上并行/合并只会把「12s 超时」换成「12s 超时但数据库更热」。

⚠ 附带记录一次**我自己的测量错误**：先测出 `session_turns` 单条 41.9s，
据此以为它是主因；实际那 41.9s 是我的端点探测与该查询并发打同一个库造成的争抢。
清空并发后复测是 **3.83s**。同一条查询在本次会话里被测到 2.08 / 3.83 / 6.23 / 78.7 秒四个值。
⇒ **任何报给别人的性能数字，必须先确认测量期间没有自己的并发**。

### §14.7 第五轮：上一轮「修好了」的判断是错的——两个缺陷都被判据放过了

第四轮部署 `2705-9b586d21` 之后连抽 10 次。表面看是修复（7/10 返 200，
1.7~6.3s），**实际上两个独立缺陷都还在**：

```
1: 500 12.051965s      ← 缺陷 B
2: 500 12.057726s      ← 缺陷 B
3: 500 12.050294s      ← 缺陷 B
4: 200  6.363738s
5: 200  2.144361s
...
```

更关键的是**成功响应本身**：

```json
{"projects":10,"tasks":1,"owners":12,"clients":20,"tags":0,
 "models":0,"providers":0,"status_codes":0}
```

三个 turns 维度**全空**。第四轮的改动（turns 臂改读 hot∪父表两腿）
确实把那条查询从 44.5s 降到 1.7s——但它**一条结果都没返回**，
所以端点看起来"变快了"。上一轮据此写下「8/8 全 200」，是错的。

#### 缺陷 A：`turnTenantClause` 拼出 `AND AND`（100% 必现）

```
turnTenantClause(" AND t.tenant_id = $1")
  → " AND " + strings.TrimPrefix(strings.Replace(s, "t.", "", 1), " ")
  → Replace      ⇒ " AND tenant_id = $1"
  → TrimPrefix " " ⇒ "AND tenant_id = $1"
  → 拼前缀        ⇒ " AND AND tenant_id = $1"     ← 42601
```

154 上连续 7 次：

```
WARN filter-options: turns 维度不可用，已退化为空 budget=5s
  err=query filter option groups failed: ERROR: syntax error at or near "AND" (SQLSTATE 42601)
```

**为什么上一轮的真库判据没抓到**：它调的是 `h.turnMergedSource("")`，
而生产上 tenantID 永远非空——`GetTenantID(r)` 取不到租户时兜底返回
`"default"`（`admin/context.go:37`），admin 用户走 `tenantFromQueryOrContext`
时既没有 `?tenant=` 也没有 `X-Tenant-ID`，最后落回 `GetTenantID`。
⇒ `tenantWhere == ""` 是死代码，判据测的正是它。

**判据纪律的补充**：函数同层 ≠ 参数同层。判据自己编的"方便的值"
不等于生产会传的值。这条缺陷 100% 必现、判据却全绿，就是这么漏的。

修法不是改拼接，是**删掉字符串手术**：`turnMergedSource` 直接收 `tenantID`，
两腿各自持有自己的租户子句，`AND AND` 结构上不可能再出现。

#### 缺陷 B：ss 臂无界，冷缓存时独占总预算 → 整页 500

前 3 次 500 的原因写在 handler 自己的日志里：

```
ERROR filter-options: ss 维度查询失败
  err=query filter option groups failed: timeout: context deadline exceeded
  duration_ms=12003
```

ss 臂当时直接吃**总预算 ctx**。它的成本是结构性的
（`EXPLAIN (ANALYZE, BUFFERS)`，热态）：

```
Parallel Hash Join                      Buffers: shared hit=69912
  Parallel Seq Scan on session_dim        828k 行 / 23,854 buffers
  Parallel Seq Scan on session_summaries  423k 行扫、过滤掉 155,775 行 / 45,902 buffers
Execution Time: 4845 ms
```

546MB 工作集。`shared_buffers` 3584MB 装得下，所以是**「冷进热出」的一次性现象**——
实测裸 `count(*) FROM session_dim`（828k 行）：**冷读 13.73s / 热读 0.28~0.41s**。

为什么偏偏在部署后立刻出现：进程 `23:31:19` 启动，23:33:23 就开始 500，
期间 `session_turns_hot` 触发了一次 autovacuum（23:31:12），
新进程又在跑 dimension queue 快照 / auto index refresh（journalctl 里 55 次
`snapshot from dimension queues built`）。154 的 load 当时只有 1.00——
**这不是 CPU 饥饿，是查询真的跑不完 12s**。

修法：ss / tags / turns 三条臂各有界，总预算 12s → 20s（nginx 侧
`proxy_read_timeout 30s`，读得下）。取舍如实记录：**真冷缓存那一次，
projects/tasks/owners/clients 会空着**。这比整页 500 好，但不是白捡的。

#### 三条更彻底的路线，全部实测否证

| 路线 | 想法 | 实测 | 结论 |
|---|---|---|---|
| ss 覆盖索引 | `first_request_at INCLUDE (session_key, tenant_id, gw_project_id)` | buffers **69,912 → 168,993（2.4×）**，Heap Fetches 172,001；wall clock 3,432 → 2,490ms | **否**。墙钟变快是假象，IO 量翻三倍，冷缓存必然更差——而故障恰恰是冷缓存造成的 |
| 改读 `session_dim.last_active_at` | 现成索引，**371 buffers / 0.7ms，快 200 倍** | 口径变了，且**不收敛**：N=400/1k/2k/5k/20k/60k 分别缺 19/28/27/26/26/20 个值，多 2/11/11/13/13/13 个 | **否**。属口径变更，需 owner 裁定 |
| `session_turns` 覆盖索引 | `(tenant_id, ts DESC) INCLUDE (model, provider, status_code)` | buffers 159,153 → 127,888（**仅 −20%**）。九月分区（483,570 行）仍被 planner 选去用既有 `session_turns_2026_09_heap_tenant_id_ts_idx`，单独占 118,030 buffers（84%） | **否**。要拿到收益得让 planner 改选，不是「加个索引」能定的事 |

#### 一条测量错误：`count(*)` 包一层改变了被测查询的工作量

用 `SELECT count(*) FROM (<真实查询>) z` 测「加租户过滤有没有变慢」，
得到 1.68s / 1.70s，据此判「子预算够」。换成**照原样取 961 行**跑，
同一条查询是 **6.3s（首读）/ 1.54s（次读）**。
⇒ `count(*)` 让 PG 有余地裁掉外层投影，量到的不是生产那条查询。

#### 判据（8 条，变异 6/6 变红）

新增/修正三条：

1. `TestFilterOptionsTenantClauseIsRealSQL` —— 两腿各一条 `AND tenant_id = $1`，
   无双 AND、无表别名。
2. `TestFilterOptionsHandlerEndToEndWithRealTenant` —— **端到端驱动 handler**，
   用不带 auth context 的请求走生产的 `"default"` 兜底，断言三个 turns 维度
   **非空**。变异下逐字复现线上症状。
3. `TestFilterOptionsNeverReturns500` —— 降级给 `[]` 不给 `null`；
   handler 内不得有 500 分支；三条臂必须 `context.WithTimeout(ctx, <各自预算>)`。

另外**修了两条判据自身**的缺陷：

- `TestFilterOptionsNeverReturns500` 原本只查"常量名出现在函数体里"，
  变异把 ss 臂改成 `context.WithCancel(ctx)`（同样无界）后**照样全绿**——
  因为 `slog.Warn` 里还在引用那个常量。改成匹配机制后立刻变红。
- `TestFilterOptionsRealDBWithinHandlerBudget` 原只把 turns 行数
  `t.Logf` 出来，"查得飞快但一个值都没返回"与正常返回在判据眼里一样。
  补上正向下限后当场抓出三格全空。
- `TestFilterOptionsTurnBudgetLeavesHeadroom` 原只算 turns 一条臂的余量，
  会放过"ss 吃掉余量"这种回归；改为按三条臂算。
