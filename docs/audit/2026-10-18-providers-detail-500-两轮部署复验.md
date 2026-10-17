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
