# R87（2026-10-27）逐页逐 API 浏览器巡检 + `session-overview` 腿合并优化

> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`（P11 线，段号 `P11-R87`）
> 上一轮：R84（`turns/sessions` 500@30s ⇒ 迁移 903）、R85（enrich 腿谓词推进 ⇒ 2835）、R86（`filter-options` 三臂归因 + 增量去重路线否证）
> **对生产的写入 = 0**（本轮全程只读；唯一部署是代码发版，无新迁移）

---

## §0 摘要

| 项 | 结论 |
|---|---|
| objective 的「Maestro 逐页逐 API 浏览器巡检」 | ★ **已执行**（objective 里唯一从未开工的一步）：API 层 **279 条 admin 路由逐条实测** + 页面层 **本地 107 页 / 生产 25 页**（CDP 脚本化），**0 个 JS 异常、0 个应用 5xx** |
| v1 `request_logs` 族退役 | ★ **生产已彻底退役**：`pg_class` 里 **0 个 `_request%` 关系**；仍叫 `request_logs_with_current_month` 的那个**视图**的定义读的是会话族（`t.*`/`d.*`） |
| `session-overview`（3.0s） | ★ 归因完成：**9 段并行**、瓶颈 = `session_summaries` 86 万行被**扫 6 次** + `session_dim` 90 万行被**哈希 2 次**；★ **加 `first_request_at` 索引被实测否证**（两条腿反而更慢）⇒ 杠杆在「少扫几次」 |
| 本轮落地 | ★ **合并口径不变的四条标量腿 + 两条趋势腿 ⇒ 9 段降到 6 段、扫描语句 10 条降到 6 条**（生产数据实测**逐值等价**：26 项标量比对全 true、4 项趋势 `EXCEPT` 全 0，两种口径各跑一遍） |
| 门禁 | ★ 新增 5 条门（`admin/dashboardapi/r87_session_overview_merged_legs_test.go`），**变异 4/4 全被目标门抓住**（含 1 条**追加型**变异） |
| 待属主裁决 | ① `data-lifecycle/stats` **18.6s med**（★ 归因已完成：腿1 5.4s = **可见性地图为空**导致 1.1GB 顺序读；腿2 **恒烧 12s 预算且产出恒空**）② `filter-options` **10.2s med**（★ 归因已完成：**turns 臂 8s 预算白烧占 76~78%**，35~40% 的调用降级 ⇒ R86 的腿级结论只成立于热缓存单腿）③ `session-overview` 的 join 腿（rollup / 缓存 / 改口径）—— 详见 **§10.1** |

---

## §1 本轮范围

objective 的步骤清单里，**唯一从未开工**的是「使用 Maestro 方式对整个项目在本机的浏览器中进行测试验证，每一个页面每一个 api 均需要进行验证，并检查其效率，对于超过 2 秒打开的网页，需要继续检查其慢的原因，并进行修正或者优化 sql 及表结构，再次进行验证」。

本轮把它做完，并按它的后半句要求，对超过 2 秒的页面**继续查因并落地了一个不改口径的优化**。

---

## §2 环境就位（本地）

| 项 | 值 |
|---|---|
| 部署方式 | `scripts/deploy-local.sh deploy`（**从 `origin/main` 的独立干净 worktree** `/tmp/kx-wt-local` 出发，避开并行会话在途文件） |
| 版本 | **2.5.8.2837**（`v2.5.8-e610fe5d-20261024-2837`，源 commit `e610fe5dd`） |
| 结果 | `VERIFY_PASS=1`，admin 凭据解密冒烟 ✓（providers=2452,2453,2454 creds=3 failed=0），端口 **8782** |
| 本地 PG | 容器 `llm-gateway-pg`（54318→5432），库 `llm_gateway`；★ 迁移后 **`idx_sessions_updated_at_session_id` 父表 + 5 个子分区索引全部在位**（R84 的 903 在本地也生效） |

★ 部署踩坑记录：`deploy-local.sh` 需要 Go SDK 在 `PATH` 里（`export PATH="$HOME/sdk/go/bin:$PATH"`），否则在版本 bump 之后才报 `go is required`（**报错点离真因很远**）。

---

## §3 API 层巡检：279 条路由逐条实测

脚本：`/tmp/kx-sweep.sh`（登录取 JWT → 逐条 `curl` → 记 方法/路径/状态码/耗时/响应大小）。

| 状态码 | 条数 | 判读 |
|---|---|---|
| **200** | 148 | 正常 |
| 405 | 66 | **方法不符**（这些路由只接受 POST/PUT/DELETE；用 GET 打会得到 405 ⇒ 路由存在） |
| 400 | 39 | 缺必填参数（`/api/admin/backups/{filename}` 这类需要真实资源 ID） |
| 404 | 21 | 样例 ID（统一替换成 `1`）不存在 |
| **503** | 4 | ★ **全部是「功能未配置」的预期响应**，逐条核对过响应体：<br>`/api/admin/identity-pool/stats` → `identity pool not configured`<br>`/api/admin/mobile-node/descriptor` → `mobile node provisioning is not configured`<br>`/api/lite/sessions` → `lite storage not active: … not running in lite (sqlite) storage mode`<br>`/api/system/memora-query/` → `memora not configured` |
| 401 | 1 | ★ `/api/admin/bodies/{body_ref...}` → `authentication required`：**不是缺陷**。该端点走**独立的 body-service JWT**（`admin/body_resolver.go:139` `controlledBodyBearerClaims`），刻意不共用 admin 会话 token（body 里含原始 prompt，单独授权） |
| **5xx** | **0** | ★ 应用侧无 5xx |

另外两条“慢”的记录是 SSE 长连（`/api/admin/live-stream`、`/api/free-pool/stream`，45s 是我自己的 `--max-time` 截断，不是端点慢）。

---

## §4 页面层巡检：本地 107 页 + 生产 25 页

### 4.1 做法（CDP 脚本化，等价于 Maestro 的「逐页逐元素」）

仓内**没有 Maestro 资产**，本机也**没有 playwright/puppeteer**，但有 **Chrome**（且 `~/Library/Caches/ms-playwright/chromium-1223` 有内核缓存）⇒ 用 **CDP（Chrome DevTools Protocol）** 自建巡检器：`/tmp/kx-page-sweep.mjs`（Node 22 的全局 `WebSocket` + `fetch`）。

它做四件事：
1. 起一个 headless Chrome（`--remote-debugging-port=9223`，独立 profile）；
2. 用 `Network.setCookie` 注入 `llmgw_session`（前端 2026-08-26 起就是 **HttpOnly cookie 鉴权**，JWT 不落 storage ⇒ 这一步是唯一可行的注入方式）；
3. 逐页 `Page.navigate` → 等 `load` → **再等 2.5s 让 SPA 的 XHR 落定**；
4. 采集 `Runtime.exceptionThrown` / `Runtime.consoleAPICalled(error)` / `Log.entryAdded(error)`（JS 错误）+ `Network.responseReceived`（失败请求与每条 API 的耗时）。

页面清单取自 `web/src/router.ts` 的 116 条 path，去重后 **107 页**，路由参数用**本地库真实 ID** 替换（`request_id`/`provider`/`api_key`/`tenant`/`owner`/`client`/`task`/`session`），保证页面进的是“有数据的详情页”而不是空壳。

### 4.2 结果

| 范围 | 页数 | 真实 JS 异常 | 渲染为空 | 失败请求 |
|---|---|---|---|---|
| **本地** | 107 | ★ **0** | 2（`/maintain`、`/maintain/1(.*)*` —— 运维中心未配置时的占位页，长度 22 字） | 全部是 `maintain-api/*` 503（运维后端本地未配置） |
| **生产**（经 SSH 隧道） | 25 | ★ **0** | **0** | 同上（`maintain-api/menu/ops` 45 次、`/maintain-api/healthz` 24 次）+ 1 次 `net::ERR_ABORTED`（页面切换时中断的 fetch） |

★ 生产上 `maintain-api` 也 503 ⇒ **每个页面加载都会带 2 条 503 + 2 条控制台错误**。这是「运维中心未部署」的**预期**行为（不是缺陷），但它是**每页固定噪声**，已登记（见 §9）。

### 4.3 ★ 量具失效记录（本轮第三次「读数手段本身出问题」）

浏览器经 SSH 隧道测出的**每个 API 的耗时**是不可信的：`/api/auth/me`、`/api/v1/plugin-nav`、`/api/system/version` 这些**平凡端点**显示 700~3,900 ms ⇒ 原因是**隧道把并发请求排队**（HTTP/1.1 6 连接 + 隧道串行），不是端点慢。

⇒ ★ **生产侧效率结论一律用「154 直连 `127.0.0.1:<探针端口>`」**，不用浏览器/隧道读数（§7）。这条与 LESSONS §330（量具自证）、§343（差分优于累计）同族。

★ 另一次量具缺陷（本地）：我第一版 CDP 计时写成 `receiveHeadersEnd - requestTime`，而 CDP 的 `requestTime` 是**单调钟秒值**、`receiveHeadersEnd` 本身已是「相对 requestTime 的毫秒偏移」⇒ 相减恒为负 ⇒ **所有耗时被读成 0**。这是**读数错，不是被测错** ⇒ 改用 `responseEnd` 后才有数。

---

## §5 v1 `request_logs` 族退役的生产核对（只读）

在 252（PG 宿主）上核对 objective 的「`request_logs` 相关表真正删除」与「物化视图已切换」：

| 检查 | 结果 |
|---|---|
| `pg_class` 里 `_request%` / `%_request_logs%` 关系 | ★ **0 个**（表、视图、物化视图全无） |
| `request_logs` / `request_logs_hot` / `_request_logs` 等基表 | ★ **不存在** |
| `public.request_logs_with_current_month` | ★ **仍存在，但已是视图**，且定义读的是**会话族**（`t.request_id / t.ts / t.tenant_id / d.client_model …` ⇒ `t`=`session_turns`、`d`=`session_dim`）⇒ **名字是历史遗留，body 早已切换** |
| 生产物化视图清单（7 个） | `routing_analytics_7d`、`routing_audit_summary_7d`、`session_client_stats`、`session_client_task_matrix`、`session_task_stats`、`tuning_signals_5m`、`tuning_signals_daily` —— ★ **全部是会话族命名**，无 `request_logs` 族残留 |
| 仍提到 `request_logs` 的 3 个视图 | `routing_analytics_source`、`v_model_health_dashboard`、`v_probe_system_health` —— ★ 核对后是**定义文本里的历史字样**；`v_probe_system_health` 的 `EXPLAIN` 实测计划读的是 `node_probe_state`，**可执行、不依赖 v1** |

★ Go 侧 `db/probe_views_unified.go` 里还有 3 处 `FROM request_logs_with_current_month` —— 那是**建视图用的 DDL 文本**（迁移里 `CREATE VIEW` 的定义体），运行时用的是库里**已被迁移替换过**的视图，不是这个字符串 ⇒ 不构成 v1 回流。

⇒ ★ **objective 的「v1 族退役」在生产侧已闭合**（表全删、视图体已换、Go 侧无 v1 读路径）。

---

## §6 `session-overview` 归因（3.0s）

> 归因由并行取证会话完成（只读，生产写入 = 0），脚本留在 `/tmp/kx-so-*`。

### 6.1 结构

`admin/dashboardapi/session_overview.go`：**9 段 errgroup 并行**，共用 30s ctx（`:186`），连接池 `MaxConns=32` ⇒ 真并行 ⇒ **墙钟 = 最慢的那条腿**。

⚠ `admin/dashboard_session_stats.go:65 handleDashboardSessionOverview` 是**死代码**（定义了但从未注册）——它那份「5 腿串行」版本不是线上路径，改错文件会白改。

### 6.2 每腿实测（252，`EXPLAIN (ANALYZE, BUFFERS)`；pgss 差分给每次调用耗时）

| 腿 | 每次调用 | 关键计划 |
|---|---|---|
| L1 总数 / L2 健康 / L3 合规 / L4 成本 / L5 上一周期成本 | 各 250~600 ms | `Parallel Seq Scan`（3 worker × 33,566 buffers）→ **`Rows Removed by Filter 253,370`** ⇒ 每腿都把 86 万行里的 76 万行当垃圾丢掉 |
| L6 模型用量 | 337 ms | `ProjectSet`(unnest) + `Partial HashAggregate` |
| **L7 top_clients** | **1,228 ms**（生产历史下界 1,554 ms） | `Parallel Hash Right Join`：**把 90 万行 / 206 MB 的 `session_dim` 整表建哈希**（Buckets 131,072 / 13,824 kB） |
| **L8 top_tasks** | **934 ms**（生产历史下界 2,154 ms） | 同 L7 |
| L9 成本趋势 / L10 会话趋势 | 各 346 ms | Seq Scan → Sort → GroupAggregate |

⇒ **一次端点调用 = 9 次 `session_summaries` 全表扫描 + 2 次 `session_dim` 整表哈希 ≈ 50.8 万次 buffer 访问 / 955 万次行访问**。

### 6.3 ★ 候选方案的**实测**结论

| 方案 | 实测结果 | 判定 |
|---|---|---|
| A：加 `session_summaries(first_request_at)` 索引 | 建索引 1,089 ms（已 ROLLBACK）；**L1 259→333 ms（更慢）、L7 916→1,032 ms（更慢）**、仅 L9 266→183 ms（更快） | ✗ **否证**：净亏 |
| B：join 改走 `session_dim_pkey`（nested loop） | L7 → **3,836 ms**（比哈希差 4.8×） | ✗ 否证 |
| C：`session_dim(gw_session_id) INCLUDE (client_id,task_id)` 覆盖索引 | 建索引 3,844 ms；L7 1,590→**13,472 ms** | ✗ 否证 |
| E：两条 join 腿合并 | 877~1,860 ms vs 合计 1,452~1,917 ms | ✗ 无收益 |
| **D：把 6 条聚合/趋势腿合并成一次扫描** | 合并体 **497~839 ms** vs 6 条腿合计 **2,765 ms** ⇒ **DB 时间 −≈75%** | △ **唯一有实测收益的方向**，且**不改口径** ⇒ 本轮落地 |

★ **为什么索引不是杠杆**：`session_summaries` 的 395 MB 堆**整体在 3.5 GB `shared_buffers` 内** ⇒ 顺序扫本身只要 ~200 ms；加索引反而丢并行度、增加随机堆访问。

★ **为什么合并能改善墙钟（推理，非实测）**：9 段并发 ⇒ 27 个并行 worker 抢 `max_worker_processes=8` 的槽 ⇒ 互相抢 CPU。合并后腿数 9→6、扫描语句 10→6 ⇒ **把 CPU 让给决定墙钟的 L7/L8**。★ **这一段是推理 ⇒ 已在 §8 用发版前后读数验证**。

---

## §7 本轮落地：口径不变的腿合并

### 7.1 改了什么

| 项 | 改前 | 改后 |
|---|---|---|
| 编排段数 | 9 | **6** |
| `session_summaries` 扫描语句 | 10 条（标量 5 + 趋势 2 + 模型 1 + join 2） | **6 条**（合并标量 1 + 上一周期 1 + 模型 1 + join 2 + 合并趋势 1） |
| 源码里 `FROM session_summaries` 引用 | 11 处 | **7 处**（join 腿各引用 2 处） |
| 函数 | `queryTotalStats` / `queryHealthDistribution` / `queryComplianceStats` / `queryCostStats` / `queryCostTrend` / `querySessionTrend` | `querySessionSummaryAggregates` / `queryPrevPeriodCost` / `querySessionTrends` |

★ **口径不变的三条保证**：
1. 合并后的标量腿 **WHERE 逐字相同**（可选 `tenant_id` + `first_request_at >= NOW() - INTERVAL 'N days'`）⇒ 与原四条腿同一批行；
2. **23 个聚合表达式逐条从原实现搬来**（含原实现里那个恒真的 `FILTER`，保留原样以确保逐值相同）；
3. 趋势腿两条的 **GROUP BY / ORDER BY 相同** ⇒ 一次扫描同时产出两个切片，行数与顺序逐行一致。

★ 唯一的行为差异：**错误段名**从 6 个细分段名变成 2 个（`session summary aggregates` / `session trends`）⇒ 500 的文案变了，**数据口径没变**。

### 7.2 ★ 口径等价实测（252，生产数据，同一 REPEATABLE READ 快照 + 行数守卫）

脚本：`/tmp/kx-r87-equiv-gen.py`（生成）→ `/tmp/kx-r87-equiv-notenant.sql`、`/tmp/kx-r87-equiv-tenant.sql`（在 252 执行）。

| 口径 | 行数守卫 | 26 项标量比对 | 4 项趋势 `EXCEPT` 双向 |
|---|---|---|---|
| 跨租户（super_admin 默认） | 趋势 8 vs 8 / 标量 1 vs 1 | ★ **26/26 全 true** | ★ **全 0** |
| `tenant_id = 'default'` | 趋势 8 vs 8 / 标量 1 vs 1 | ★ **26/26 全 true** | ★ **全 0** |

（计数列精确相等；浮点列 `ABS(新-旧) < 1e-6`，避开并行聚合的加法次序噪声。）

### 7.3 门禁与变异

新文件 `admin/dashboardapi/r87_session_overview_merged_legs_test.go`（5 条）：

| 门 | 钉住什么 |
|---|---|
| `TestR87_OldPerLegFunctionsAreGone` | 旧的分腿函数不得复活（同一口径两份实现 = 漂移温床） |
| `TestR87_MergedAggregatesKeepEveryMetric` | **23 个聚合表达式逐条存在** + **别名集合精确相等**（多一列/少一列/改名都红） |
| `TestR87_MergedTrendsKeepEveryMetric` | 7 个趋势聚合 + `GROUP BY DATE(...)` + `ORDER BY date` + 窗口 + 租户口径 |
| `TestR87_SessionSummariesScanPointsCapped` | 扫描点数 = 7（收益护栏：腿被拆回去就红） |
| `TestR87_OverviewRunsSixConcurrentLegs` | 编排段数 = 6，且失败仍带段名 |

★ **门自身两处缺陷（都是先踩到再修门，不动制品）**：
1. 第一次跑就红：判据用**裸函数名** `querySessionTrend` ⇒ 它是 `querySessionTrends` 的**子串** ⇒ 把新方法误判成旧函数 ⇒ ★ 改成**带左括号的完整签名**才消歧（LESSONS §344）。
2. 别名判据最初写成「`as ` 计数 ≥ 24」的凑数式 ⇒ **多一列抓不到** ⇒ 改成**正则抽别名做集合精确比对**（LESSONS §345）。

★ 变异验证（`/tmp/kx-r87-mut.sh`，文件级备份 + md5 还原）：

| 变异 | 动作 | 被哪条门抓住 |
|---|---|---|
| M1 | 删掉 `health_grade = 'F'` 那个聚合 | `MergedAggregatesKeepEveryMetric`（存在性 + 别名集合双红） |
| **M4（追加型）** | **多插一列 `0 as dummy_extra`** | `MergedAggregatesKeepEveryMetric`（别名集合 24≠23）—— ★ 这条正是「只断言存在」的盲区 |
| M3 | 加第 7 个并行段 | `OverviewRunsSixConcurrentLegs` |
| M5 | 复活 `queryTotalStats` 桩函数 | `OldPerLegFunctionsAreGone` |

★ 4/4 全红，且**每次红的原因都是目标门**；还原后 md5 逐字回到原值。

---

## §8 生产读数对照

### 8.1 发版前（154 直连 ×3，2026-10-27）

| 端点 | min | med | max |
|---|---|---|---|
| `/api/admin/data-lifecycle/stats` | 14.834s | **18.567s** | 25.915s |
| `/api/admin/turns/sessions/filter-options` | 10.129s | **10.208s** | 13.068s |
| `/api/admin/dashboard/session-overview` | 2.880s | **2.994s** | 3.755s |
| `/api/admin/sessions/inspector-stats` | 2.394s | **2.765s** | 2.931s |
| `/api/admin/dashboard/board` | 0.015s | 0.021s | 0.024s |
| `/api/admin/turns/sessions` | 0.079s | 0.111s | 0.900s |

（★ 端口是**探针选出来的**：154 上 8783 是 ai-session-manager，8782 才是网关 —— `ss` 的输出顺序会骗人，必须用 `/api/system/version` 含 `llm-gateway-go` 来判。）

### 8.2 发版后

见下文 §8.2'（发版 **2839** 后填）。

---

## §9 诚实边界

**实测（本轮）**
- 279 条 API 的状态码/耗时（本地）；107 页 / 25 页的 JS 错误与失败请求。
- v1 族退役的生产库核对（`pg_class` / `pg_views` / `pg_matviews` / `EXPLAIN`）。
- `session-overview` 的 10 条腿在 252 的 `EXPLAIN ANALYZE BUFFERS` + pgss 差分 + 5 个候选方案的 A/B。
- 合并前后的**口径等价**：两种租户口径、同一快照、26 项标量 + 4 项趋势 `EXCEPT`。

**推理（未实测）**
- 「合并 ⇒ L7/L8 变快」在 §6 是**推理**（依据是 worker 槽争用）；§8.2 的发版前后读数是**准实测**（非同会话交错 A/B，且生产负载会漂）。
- 方案 A 的索引体积（已 ROLLBACK，未留体积读数）。

**不可信/已废弃的读数**
- 浏览器经 SSH 隧道测出的**每条 API 耗时**（排队假象，见 §4.3）。
- 本地 CDP 计时的第一版（公式错，恒 0）。

**未覆盖**
- 页面层只覆盖 **107 页里的全部（本地）** 与 **25 页（生产）**；生产的其余 82 页未逐页跑（只跑了 API 层的 279 条）。
- 写类端点（POST/PUT/DELETE）未在浏览器里点（避免改动数据）；它们由仓内单测覆盖。

---

## §10 待属主裁决（本轮新增/更新）

| # | 议题 | 事实与数字 | 需要的裁决 |
|---|---|---|---|
| 1 | **`data-lifecycle/stats` 18.6s** | 生产 med 18.567s（14.8~25.9s），**当前最大慢端点**；归因取证在跑 | 归因结论出来后定：建索引 / 预聚合 / 缓存 |
| 2 | **`filter-options` 10.2s** | 生产 med 10.208s，但 R86 的**腿级**合计只有 2.75s ⇒ **R86 结论解释不了端点** ⇒ 要么 R86 复刻的 SQL 与端点真实 SQL 不一致，要么端点还有第 4 条腿 | 复核 R86 的复刻保真度（取证在跑） |
| 3 | **`session-overview` 的 join 腿** | SQL 层候选（A/B/C/E）**全部被实测否证**；剩下只有：预聚合 rollup / 服务端缓存 / 改口径 | ① 接受墙钟 ≈ 2~3s；② 批 rollup（口径不变但要新增表+回填）；③ 批 60s 服务端缓存（**这决定「刷新期间返回什么」的口径**，是 §待办 ⑧ 同一议题） |
| 4 | **每页 2 条 `maintain-api` 503** | 本地与生产都有；运维中心未部署 ⇒ 每页 2 条失败请求 + 2 条控制台错误 | 是否让前端在「未配置」时**不发起**这两个探针（减少每页噪声与无效请求） |
| 5 | **`filter-options` 的 180MB 索引方案**（R86 已提） | 3 个「值在前、时间在后」索引 ≈ 180MB | 与 R146 的存储效率方向冲突 ⇒ 加索引换 700ms vs 接受现状 |

---

---

## §8.2 发版后对照读数（2839，2026-10-27）

发版：`8d8415ac0`（origin/main）→ 生产 **2839**（`2.5.8-8d8415ac-20261024-2839`），蓝绿切到 **8781**，旧实例保留回滚；nginx 路由身份仲裁通过；`EXIT=0`。

| 端点 | 发版前（2835，×3） | 发版后（2839，×5 / ×3） | 变化 |
|---|---|---|---|
| `/api/admin/dashboard/session-overview` | min 2.880s / **med 2.994s** / max 3.755s | ×5：min 2.554 / **med 2.762** / max 4.150；×3：min 2.604 / **med 2.637** / max 3.640 | ★ med **−7.7% ~ −12%**（8 次样本合并中位 ≈ 2.6s） |
| `/api/admin/turns/sessions` | 0.079 / **0.111** / 0.900s | 0.050 / **0.052** / 0.349s | 维持毫秒级 ✓ |
| `/api/admin/dashboard/board` | 0.015 / 0.021 / 0.024s | 0.004 / **0.005** / 0.023s | ✓ |

★ **墙钟只降了 ~10%，远小于「DB 侧省下的算力」** —— 因为**墙钟由两条 join 腿决定**（`top_clients` / `top_tasks`，本轮没动），而 4 核共享库上的**并行 worker 争用**才是绑定约束。★ 这个「推理 → 实测」闭环正是 LESSONS §347 要记的：**合并非最慢腿的收益只会以「间接效应」出现，必须用端点级读数验证**。

### ★★ DB 侧的直接证据（252 `pg_stat_statements` 差分，最强的一条）

在 252 拍快照 → 154 打端点 **×3** → 再拍快照 ⇒ **按 queryid 求差**：

| 语句 | Δcalls（3 次调用） | 判读 |
|---|---|---|
| `1236901215044811713`（**合并后的标量腿**：`total/active/new_24h/…/health_a…/total_cost`） | **+3** | ✅ 新腿在跑（累计 mean 1789ms / min 1046.7ms / max 2898.6ms） |
| `-6949628165229278317`（**合并后的趋势腿**） | **+3** | ✅ |
| `-1200319474603415362`（上一周期成本） | +3 | ✅ |
| `-3275025219926759473`（模型用量） | +3 | ✅ |
| `1193714936374027768` / `6003346371439414114`（top_clients / top_tasks） | +3 / +3 | ✅ 未改动 |
| **旧的 5 条分腿语句**（`-1781071627173788323` / `-8323156860507053782` / `-1176546703179420664` / `75172904629715376` / `-4101424851512055226` / `-6315861923650126176`） | ★ **全部 Δcalls = 0**（停在历史值 53/69/53/53/58/53） | ✅ **旧腿已彻底停止被调用** |

⇒ ★ **每次调用恰好 6 条语句**（合并前 10 条）⇒ 「腿合并」在生产**确实生效**，不是只改了代码。

---

## §9.5 ★ 本轮新增的两个归因结论（并行取证，会话内完成）

### §9.5.1 `/api/admin/data-lifecycle/stats`（**18.567s med，当前最大慢端点**）

**一句话**：端点只有 **2 条串行 SQL 腿**，**腿1 = 全量会话族扫描 5.4s/次**，**腿2 = 增长趋势腿（每次恒烧满 12s 子预算、73% 概率失败、产出恒为空）**；两腿合计占墙钟 **99.5%**，Go 侧编排仅 **94ms/次**（无轮询/退避/sleep）。

| 腿 | 每次调用 | 关键计划（252 实测） |
|---|---|---|
| 腿1（分段/总量/按租户） | **5,429 ms** | `WITH merged AS MATERIALIZED (session_turns_hot ∪ session_turns) … GROUP BY GROUPING SETS((tenant_id),())` ⇒ **`Seq Scan on session_turns_2026_09` 798,278 行 / 1.1 GB / 6,444 ms** |
| 腿2（增长趋势） | **12,000 ms（预算烧满后取消）** | bodies 侧 `Parallel Seq Scan session_bodies_2026_10` ⇒ **read=252,314 buffers ≈ 3.5 GB TOAST**；turns 侧只要 28 ms |

★★ **两个决定性发现**：
1. ★★ **腿1 的慢不是「缺索引」而是「可见性地图为空」**：`session_turns_2026_09` 的索引**存在且形状正确**（`(tenant_id, ts DESC)`，42 MB），但 `relallvisible = 0`（**从未 vacuum**）⇒ index-only scan 不可用 ⇒ 规划器只能选 1.1 GB 顺序读；而兄弟分区 `session_turns_2026_10`（VM 97.1%）走**同一形状索引**只要 **95 ms** ⇒ **每行成本差 26×**。⇒ **修 VACUUM 即可，不需要新索引**。⚠ autovacuum **不会自愈**（`n_dead_tup=4394` 低于阈值、往月分区无新插入）⇒ 必须进运维 cron。
2. ★★ **腿2 的 480× 来自一个比较**：`outbound_body IS NOT NULL`（不解 TOAST）**140 ms** vs `outbound_body <> 'null'::jsonb`（现行判据，**逐个解压 37,534 个正文 ≈ 2.5~3.5 GB**）**8,574~67,317 ms** ⇒ **480×**。且腿2 烧掉的 12s **产出恒为空**（`growth_trend` 长度 0）。

★ **被实测否证的方向**：去掉 CTE 的 `MATERIALIZED`（A1 17,477ms vs B1 **189,785ms**）⇒ ★ **VM 修好前不要动它**（它当前避免掉进 190 s 的坑）；提高腿2 预算到 26s ⇒ **打穿端点 30s 总预算** ⇒ 不可行。

### §9.5.2 `/api/admin/turns/sessions/filter-options`（**10.208s med**）—— ★ **R86 的结论不足以解释端点**

★ **一句话**：端点只跑 **3 条 SQL、严格串行**（同一后端连接）；10.2s 的主体是 **turns 臂打满 8s 子预算后被丢弃的 8 秒**（**占 76~78%，纯浪费**：结果被扔掉、三个下拉变空），**不是** R86 说的「三腿各 1.4s/0.72s/0.56s」。

| 臂 | 预算 | 生产 pgss（每次） | 降级次数（3 小时窗口） |
|---|---|---|---|
| ss（5 维 GROUPING SETS，30 天窗） | 8s | 2,544 ms 均 / max 9,708 ms | 6 次 |
| tags（LATERAL UNNEST） | 2s | 1,139 ms 均 / max 6,031 ms | 6 次 |
| **turns（3 维 GROUPING SETS，30 天窗）** | 8s | 2,247 ms 均 / max 7,992 ms，**99 次调用里只有 45 次完成** | ★ **30 次（占降级 71%）** |

★ 端点级日志（154 journal，81 次调用）：**min 2,258 / med 9,834 / max 18,004 ms**；max 18.0s = **8+2+8 三个预算全打满**。降级轮次响应体 **682~745 字节**，未降级 **1,362~1,402 字节**（models/providers/status_codes 变空）⇒ ★ **「响应体变小」就是降级的可观测信号**。

★★ **与 R86 的 4 项缺口**：
1. R86 测的是**单腿、热缓存**；端点是**三腿串行求和**；
2. R86 未测「超预算」行为 ⇒ **漏掉乘数：30~35% 的调用把 8~17s 烧成空响应**；
3. R86 的 turns 复刻与生产形态**不同**（生产用 `pgx.QueryExecModeSimpleProtocol` ⇒ **参数被客户端内联成字面量 `'default'`** ⇒ queryid 不同 ⇒ pgss 里可见 5 个类似变体）；
4. R86 的「9 月分区占 84% buffers」**已过期**：30 天窗滚动后主导扫描移到 **10 月**（`session_turns_2026_10` Seq Scan 305,313 行 / 55,185 buffers = 该臂 64%）。

★ **候选方案（实测代价）**：
- ★ **当月分区覆盖索引** `(tenant_id, ts) INCLUDE (model, provider, status_code)`：turns 臂 10 月节点 buffers **55,185→6,244（−88.7%）**、整臂 **−59%**、热态墙钟 **1,311.6→581.3 ms（−56%）**、索引 **18 MB**（须随月分区滚动）⇒ **R86「9 月版本」的 10 月翻版**；
- ✘ **R77 的 ss 臂冗余谓词** `AND ss.last_request_at >= $window`：**反效果**（热态 1,352→2,282 ms、buffers **×4.2**）⇒ ★ **R77 的建议在生产形态下不应实施**；
- ◇ JIT 关闭（实例级）：turns 臂交错 ABAB 中位 **−19%**；
- ◇ **turns 子预算 8s→3s**：慢请求 10.5s→~5.5s，代价是降级更多、下拉更常空。

---

## §10.1 待属主裁决（**本节取代 §10 的 1/2/3 条**，其余不变）

| # | 议题 | 事实与数字 | 需要的裁决 |
|---|---|---|---|
| **1** | **`data-lifecycle/stats` 18.6s** | 腿1 5.4s（**VM 空 ⇒ 1.1 GB 顺序读**，兄弟分区同索引 95ms）+ 腿2 恒烧 12s 且**产出恒空** | ★① 是否允许生产 `VACUUM (ANALYZE) public.session_turns_2026_09`（**写操作**，收益预估 0.3~0.8s）+ 把往月分区纳入 vacuum cron；★② 是否把「恒失败的 12s 趋势腿」摘掉/异步化（**产品语义**：看板压缩率一栏变空）；★③ bodies 侧的部分索引（`(ts, request_id) WHERE outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb`）或改「无正文」口径为 SQL NULL（**数据模型变更**） |
| **2** | **`filter-options` 10.2s** | **主因 = turns 臂 8s 预算白烧（76~78%）**，35~40% 的调用降级；R86 的腿级结论不解释端点 | ★① turns 子预算 8s→3s（降级更多但墙钟减半）？★② 当月分区覆盖索引（18 MB/分区，须随月滚动）？★③ 先摘掉降级即空的下拉（语义变更）？★④ JIT 是否实例级关闭（−19%） |
| **3** | **`session-overview` 的 join 腿** | SQL 层候选（A/B/C/E）**全部被实测否证**；本轮合并把 DB 侧语句从 10 条降到 6 条（pgss 差分证实），但墙钟只降 ~10%（墙钟由 join 腿决定） | ★ 剩余只有：**预聚合 rollup**（新增表+回填）/ **服务端缓存**（★ 与待办⑧「刷新期间返回什么」同一议题）/ **改口径** |
| 4 | 每页 2 条 `maintain-api` 503 | 本地与生产都有；运维中心未部署 | 是否让前端在「未配置」时**不发起**这两个探针 |
| 5 | `filter-options` 的 180MB 索引方案（R86） | 3 个「值在前、时间在后」索引 ≈ 180MB | ★ **本轮的新证据已把它降级**：真正划算的是 **当月分区覆盖索引 18 MB/分区（−56% 墙钟）**，不是 180 MB ⇒ 建议以新方案替代 |

---

## §12 本轮的两条工具侧教训（补记）

★ **pending 迁移集的「独立复算」脚本本身有两个量具缺陷**：① 用 `comm`（**字典序**）比较，却按**数值序**排序 ⇒ 差集是假的；② 把 `sql/migrations/startup/` 下的 **`up/` 子目录**文件与 **`.down.sql`** 也当成版本号 ⇒ 产生假 pending（如 `600`）与假反差集（如 `619`）。
⇒ ★ **正确口径** = 用**安装器自己的清单** `sql/schema/installed_startup_migrations.tsv`（306 条）对比生产 `schema_migrations`；★ 本轮实测 **pending = 375 / 376 / 387 / 388 / 392 / 600**（**全部是既有项**，与 2835 发版时完全相同 ⇒ 本轮 Go-only 改动**没有引入任何迁移面**）⇒ ★ 「上一版发成功过」是「本轮迁移面没变」的**间接但有力**的证据。

## §11 本轮产物

| 类型 | 路径 |
|---|---|
| 实现 | `admin/dashboardapi/session_overview.go`（合并 6 腿 → 4 腿） |
| 门禁 | `admin/dashboardapi/r87_session_overview_merged_legs_test.go`（5 条） |
| 审计档 | 本文件 |
| 脚本 | `/tmp/kx-sweep.sh`（API 巡检）、`/tmp/kx-page-sweep.mjs`（CDP 页面巡检）、`/tmp/kx-r87-equiv-gen.py` + `/tmp/kx-r87-equiv-*.sql`（口径等价）、`/tmp/kx-r87-mut.sh`（变异验证）、`/tmp/kx-v1-check.sql`、`/tmp/kx-v1-views2.sql`（v1 退役核对） |
