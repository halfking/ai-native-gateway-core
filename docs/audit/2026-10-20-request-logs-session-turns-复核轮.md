# request_logs → session_turns 双写退役：2026-10-20 复核轮

> 基线（**方法 + 观测值**，不写死终态 SHA —— 上游每几十分钟就被推一次，写死必然过期）：
>
> ```bash
> cd <仓库根> && git fetch origin --prune -q
> git rev-list --left-right --count main...origin/main   # 期望 0  0
> git branch --show-current                              # 期望 main
> git status --porcelain                                 # 期望空
> ```
>
> 本轮收尾时的观测值：本地 `main` 与 `origin/main` 一致、工作树干净。
> **网关在跑的发布产物** = `2.5.8.2756`（`git_sha=b45b3725`）—— 这个 sha **可作证**，
> 因为构建时**工作区干净且 HEAD==origin/main**（版本号类提交不改 Go 树）。
> 环境：本机 PG 17.10（`llm-gateway-pg` 容器，127.0.0.1:54318）、Redis 7（6379）、
> 入口 `http://127.0.0.1:8782/v1`
> 口径：只写实测到的；未验证项显式标注。
> 覆盖范围：本轮所有读数均来自**本机**（§5.11.3 末尾已单列覆盖范围）。

本轮是**复核轮**，不是从零实施轮。2026-10-16/17 两轮已把退役主线落地
（改名、停写门、读源切换、逐文件四族分类）。本轮做三件事：

1. 把仓库与上游的偏离收敛（合并 74 个上游提交，冲突 1 处）；
2. 在**最新 schema**（870–878 已入库）上重新验证「写侧是否真的停写、
   读侧是否真的切到 session 族」；
3. **发现「请求返回 200 但数据没落库」这个静默失效，并定位到根因、修复、加门。**

---

## 0. 结论先行

| 问题 | 判定 |
|---|---|
| v1 宽族是否还在被写？ | **否**。代码三处流量写点全挂 S4 门，真库 `_request_logs*` 实测 0 行 |
| 物化视图是否已切？ | **是**。8 张 MV 定义零 `request_logs` 引用，链经 canonical 视图到 `session_turns` |
| API 读源是否已切？ | **主体已切**（canonical 视图含 session 臂）；3 张中间层视图仍 v1-only（**有意**，有门钉住） |
| **数据存储是否可用？** | **修复前：不可用。** 请求返回 200，但 session 族几乎不落库（正文 null、trace 丢、turn_no 错位）。**根因是本机 PG 的 `shared_buffers=400kB`**，加上 `sessions_v2.write_timeout_ms` 的 spec 默认（500ms）**静默撤销**了一次「提到 2000ms」的修复。两者已修并加门 |
| 效率 | 修 PG 内存后，规划耗时 1195ms → 50ms（**24×**），网关请求延迟 2–8× 改善 |
| **逐页浏览器验证（104 页）** | **103/104 文档级 200**（唯一例外 `/maintain` 503 = 本机未部署该服务）；**0 个 API ≥2000ms**；**0 个 JS 运行时异常**；抓出 **9 处真 5xx**（5 个根因，全部 curl 可复现）—— 已修 4 个 + 1 个是功能未启用（503 预期） |
| **逐页抓出的最重根因** | `prompt_injection_detections` 的**正典形状**（`sql/schema/` + `sql/objects/tables/`）与**历史迁移 315 的形状**不是一回事，而 315 的 `CREATE TABLE IF NOT EXISTS` 在正典库上是**空操作** ⇒ 315 声明的列**从未被创建** ⇒ 读侧恒 42703、**写侧恒失败（审计记录静默丢失）**、364 的视图建不出来 ⇒ **全环境**缺陷（不是本机没升级） |
| **门禁自身的竞态（本轮新发现）** | `TestDataLifecycleTableSizesFieldsAreComplete` 偶发红：判据与端点读**同一个** `pg_total_relation_size`，却跨两次读做**精确相等**，而首行大表正被网关后台 worker 写入 ⇒ **端点是对的，判据的前提是错的**。已按该文件注释**早已声明的 ±1%** 修正并做变异验证（§5.7） |
| **逐页扫描抓到的唯一一条 JS 异常** | `/quality-correlations` 在**无数据**时抛 `TypeError: Cannot read properties of null (reading 'reduce')`：后端 `breakdown` 是 Go **nil slice** ⇒ 序列化成 `null`，而前端 TS 类型声明成**非空数组** ⇒ 类型成了假的安全网。已改后端契约（恒返 `[]`）+ 前端**边界**归一化，3 道新判据全部做过变异验证（§5.8） |
| **★ 模型请求与写入测试（补做步骤）** | `deepseek-v4-flash` **5/5 = 200**（用户清单里其余 6 个在本机**目录中不存在**或无健康凭据）；**读回证据**：5 轮 `turn_no` 1..5、`success=t`、`latency_ms` 1795→28756、正文三列 `jsonb_typeof` **全是 `array`** ⇒ **「数据存储可用」成立**。08:02 起全量 503 的归因是**三个环境原因**（凭据 auth_failed / 代理节点库 0 行 / 上游余额耗尽），**不是仓库缺陷**（§5.10） |
| **★ 累积式多轮（补做）** | 改用**累积对话**（每轮带全部历史）重做：`prompt_tokens` **84 → 92 → 107 → 386 → 1255 严格单调递增**（这才是「多轮」的可验证形态；每轮只发一条消息时它只反映本轮提示词长度）⇒ 5/5 = 200、`turn_no` 1..5 连续、正文三列 `jsonb_typeof` **全是 `array`**、退役族 `_request_logs*` **0 行** ⇒ **4 条判据全 PASS**。过程中抓到**两条量具坑**（BSD sed 不剥 `\n` 标记 ⇒ 请求体被清空；探活一次碰多模型 ⇒ 一个模型的 429 冷却整个凭据）（§5.11） |
| **★ 新发现一：凭据级熔断会连坐兄弟模型** | 一个模型的 429（`EndpointTPMExceeded`）经 goal-retry 连打 3 次 ⇒ `nodehealth` 熔断阈值（付费档 3）⇒ `EffectSetCredentialUnavailable` ⇒ **该凭据名下 10 个绑定全部 `available=FALSE`** + `credentials.availability_state='cooling'` ⇒ 刚 200 过的兄弟模型立刻 `no_candidate`（3 分钟）。是 `reducer.go:434` 的**明确设计**，但代价要记账（L9） |
| **★ 新发现二：探针 chat 腿缺「预算耗尽」豁免** | Responses 腿接受 `status=incomplete` + `max_output_tokens` 作正向证据，**chat 腿没有** ⇒ reasoning 模型（探针预算只有 32 token）在 `content:null` 时被判 `probe_response_inconclusive` ⇒ 永远到不了 `healthy_confirmed`。与「2xx 不等于健康」是刻意规矩冲突，本轮**不改**（L10） |
| **★ L1 订正（上一轮自己的结论前提有误）** | 「7 张分区不带 `_` 前缀 ⇒ 新装/存量形状分叉」**不成立**：那是迁移 860 的**明确设计**（待删标志在父表上），正典基线 schema 造的就是无前缀分区名，862 那个 `_request_logs_2026_10` 只是**合成夹具名** ⇒ 两边形状相同。真正要修的是**按名字枚举分区族的口径**（两处运维脚本 ⇒ 已改成按 `pg_inherits` 走父表，真库差集证明**零回退、只补漏**）。⚠️ 这两个脚本是**仓库级运维资产、不在发布链上** ⇒ 已提交≠已生效，生产机上的拷贝件仍是旧口径（当前**无排程在跑**，详见 §5.9.3.1） |
| **★ D1：两个活指标读冻结族** | `customer_cost_view.active_concurrent`（**5 分钟**窗）与 `model_cost_per_task_view.avg_latency_ms`（7 天窗）读的是**已冻结**的 `_request_logs` ⇒ 前者在**任何环境都恒 0**（冻结族里永远没有「最近 5 分钟」的行）、后者只读到停写那一刻的快照，**且两者都不报任何错**。已用**迁移 883** 切到会话族（5 分钟窗=hot 单腿、7 天窗=hot∪parent；会话族 `api_key_id` 是 text ⇒ 必须 `::text`），canonical/正典/baseline/installer/runner/部署通道/TSV **七处登记齐全**，2 道门（真库行为门 + 5 道契约门）**4 处变异全部转红**（§5.12.2）。**部署后活体直查**：旧读源形状全是 0/NULL，新读源取到真实延迟（`deepseek-v4-flash` 54 行 avg **4046.1ms**、`glm-5.2` 29 行 avg **6395.8ms**），视图内子查询在键匹配时同样取到这些值 ⇒ **读源确实切对了、键空间也对**（§5.12.6） |
| **★ D1 的另一半：成本端点仍 `[]`** | **不是**本轮没修好，而是**聚合链从未接线**：`api_key_model_cost` 的 trigger（`trg_update_api_key_model_cost`）在正典与全部 startup 迁移里**都不存在**，**真库全触发器清点也确认它没挂在任何表上**（函数 `update_api_key_model_cost()` 存在但**无调用方**），该表**0 行**、**无唯一索引** ⇒ 原函数的 `ON CONFLICT` 会报 42P10。⇒ 这两个端点**从功能设计出来那天起**就一直是 `[]`，与退役无关。复活需按序补：写侧 `canonical_id`（现 0/NULL）→ 唯一索引 → 会话族 trigger ⇒ 登记为 **L11**（§5.12.3） |
| **★ D3：列表端点对同一行集扫两遍 → 一遍** | 上一轮记 L3 时说「本地小数据集量不出收益」——**量法选错了**：buffers 确实量不出（三种写法都是 137 hit，第二趟全命中缓存），但**执行次数**是结构性的。已把 COUNT 与各 SUM 合并成**一条**聚合（执行次数 2→1），并保持「聚合失败 ⇒ 退回单独 COUNT、聚合字段回零、页照常出；COUNT 也失败才 500」的旧降级语义。判据用 **pgx QueryTracer 数查询**（必须恰好 1 条非分组聚合扫描）+ 与旧两扫口径**逐值对账**，变异（拆回两查）转红（§5.12.4）。**部署 `2.5.8.2757` 后读回**：200/82ms，`count=324`、`total_requests=324`、`prompt_tokens=13109`、`completion_tokens=11875`、`by_model` 20 项 ⇒ **口径与部署前逐值一致**（§5.12.6） |
| **★ 一条「判据污染度量」的实测发现** | 用来证明「`api_key_model_cost` 从未被写」的证据 `pg_stat_user_tables.n_tup_ins` 被**门自己**污染了：真库门用 `defer tx.Rollback()` 造夹具，而 **PG 计入回滚事务的插入**（本轮受控实验实测：`BEGIN; INSERT; ROLLBACK;` 后计数 +1）⇒ 读数从 0 变成 13，看起来像「表被写过后又删了」。**连跑该门 3 次 ⇒ 计数 13→16（恰好 +3）、行数恒 0** ⇒ 那 13 次插入全是门自己的回滚夹具，该表从未被生产流量写过。⇒ 证据已换成**「触发器在不在 + 现在几行」**（§5.12.2 订正框、§5.12.3 证据链） |

---

## 1. 合并与构建

| 项 | 结果 |
|---|---|
| 合并 | `git merge origin/main`，1 处冲突（`LESSONS.md`）——两侧各追加了**不同**的经验条目（HEAD 侧 §138/§139，upstream 侧 2026-10-19/20 若干条），**两边都保留**（HEAD 在前） |
| 工具链 | `go1.27.2 darwin/arm64`（`~/sdk/go/bin/go`）。⚠️ `AGENTS.md` 写的 Homebrew 路径 `/opt/homebrew/bin/go` **已不存在** |
| 构建 | `CGO_ENABLED=0 go build ./...` → RC0 |
| 迁移 | `scripts/apply-db-revision-sequence.sh` → RC0，ledger 233 → 241 腿，870~878 全部入库 |
| 部署 | `deploy-local.sh deploy` → `VERIFY_PASS=1`，release `2.5.8.2746` |

**续做轮（§5.12 的 D1/D3）的构建与部署**：

| 项 | 结果 |
|---|---|
| 代码提交 | `ddc2f3362`（D1 迁移 883 + D3 合并聚合 + 2 道新门） |
| 迁移 | `scripts/apply-db-revision-sequence.sh` → RC0，`883_session_family_cost_view_metrics.sql` 入账（台账 marker `session-summary-and-integrity-2026-09:883_…`，`content_sha256 = 84a614746d06…`） |
| 部署 | `deploy-local.sh deploy` → **`VERIFY_PASS=1`**，release **`2.5.8.2757`**，版本身份 `2.5.8-ddc2f336-20261020-2757`（**sha 与代码提交对得上**） |
| 部署后再次重放 | 883 的**注释**订正（见 §5.12.2 订正框）使文件内容指纹变化 ⇒ 通道按契约**自动重放**（`content replay (ledger 84a614746d06 -> file c005c02cb438)`）⇒ 台账收敛到 `c005c02cb438`，视图定义体不变（`f|f|t|t`） |
| 门禁 | `gofmt` 干净；`./admin/`（193s）、`./internal/sqlreadguard/`、`./sql/migrations/startup/`、`installer/...` 全绿；`scripts/apply-db-revision-sequence_test.sh` 9 通过 1 跳过 |

**踩坑留档（DSN 指向歧义）**：迁移脚本在 `psql` 不在 PATH 时走 docker 通道，
`docker exec … psql … "postgresql://…@127.0.0.1:5432/llm_gateway"` 看起来像连到了
**本机另一个项目的 PG**（`acc-pg17` 恰好把 5432 映射到宿主）。实际无害：DSN 是在
**容器内部**解析的，127.0.0.1:5432 指的就是 `llm-gateway-pg` 自己；而 `acc-pg17`
里根本没有 `llm_gateway` 库（实测 `database "llm_gateway" does not exist`）。
⇒ 判别动作：**看到端口先问「这个端口是谁的」，再问「这个连接在哪一侧解析」**。

---

## 2. ★ 头号发现：请求返回 200，但 session 族几乎不落库

### 2.1 现象

用任务指定的入口 key（`http://127.0.0.1:8782/v1`）对 `deepseek-v4-flash` /
`glm-5.2` 各发 3 轮真实多轮请求，全部 200；逐轮回查库：

```
 session_id                          | turn_no | model             | success | has_trace | req    | resp   | out
 gw_656b3fae…（首轮，PG 修前）        |    1    | glm-5.2           | t       | t         | null  | null   | null
 gw_656b3fae…                        |    2    | glm-5.2           | t       | f         | null  | null   | null
 gw_656b3fae…                        |    3    | glm-5.2           | t       | t         | null  | null   | null
```

⇒ 正文（`session_bodies`）**三件套全为 JSON null**；`trace_events` 2/3 丢失；
还有一次整轮**一行都没落**（第二次运行：`session_turns_hot` 里查不到那 3 个会话）。

> ⚠ 量具纪律：正文是否落库一律用 `jsonb_typeof(col)='array'` 判，
> **不用 `IS NOT NULL`**（JSON `null` 也满足 `IS NOT NULL` ⇒ 假绿）。

### 2.2 证据链（自下而上）

1. **网关日志**（`~/kaixuan/llm-gateway-go-v2/logs/gateway-8782.log`）：
   ```
   sessionv2mirror: V2 shadow write failed  error="begin tx: context deadline exceeded"
   sessionv2mirror: session_dim upsert failed error="timeout: context deadline exceeded"
   sessionv2mirror: outbox registration commit failed, degrading to in-process backlog
   trace.FlushToPG: request log row not found, retaining Redis trace
   failed to get previous body for submit mode detection  error="query latest bodies: timeout"
   ```
2. **PG 服务端日志**：`canceling statement due to user request`（客户端 deadline
   在语句执行途中取消），被取消的语句包括
   `INSERT INTO session_dim … ON CONFLICT (gw_session_id) DO UPDATE`、
   `INSERT INTO stats_event_inbox …`、以及 `session_dim` 触发器里的
   `UPDATE session_summaries SET gw_project_id = v_ref …`。
3. **不是锁等待**：采样 `pg_stat_activity` 期间无 `pg_blocking_pids`；
   `pg_stat_activity` 空闲时全 idle ⇒ 不是持续性的锁问题。
4. **是「每条语句都慢」**：连 `SELECT count(*) FROM session_turns_hot WHERE 1=0`
   这样的空查询，**规划**都要 1195ms（执行只要 0.03ms）。

### 2.3 根因一（环境）：本机 PG 的 `shared_buffers` 被改成 400 kB

```
shared_buffers | 50 | configuration file | /var/lib/postgresql/data/postgresql.conf:129
```

⇒ **50 × 8 kB = 400 kB**（PG 自己的默认是 128 MB）。该值是**手改**在容器数据目录
里的（`postgresql.conf:129` 写着 `shared_buffers = 400kB  # min 128kB`）。

后果：规划期要读的 catalog 页根本进不了共享缓冲 ⇒ **每条语句的规划 ~1s**。
而写路径的 deadline 是毫秒级的 ⇒ 写不进 ⇒ 静默丢数据。

对照（同一台机、同一张表、同一条语句，只改这个值）：

| `shared_buffers` | `session_turns_hot` 规划 | 执行 | `session_summaries` 规划 | `session_bodies_hot` 规划 |
|---|---|---|---|---|
| 400 kB | **1195 ms** | 0.03 ms | 745 ms | 271 ms |
| 2 GB | **50 ms** | 0.03 ms | 99 ms | 12 ms |

⇒ 差 7–24×，**而执行时间根本没变**。这解释了为什么「EXPLAIN 看执行计划」
查不出问题：执行是快的，慢的是规划。

**处置**：`ALTER SYSTEM SET shared_buffers='2GB'; effective_cache_size='6GB';
work_mem='8MB';` + 重启容器（本机 64 GB 内存，PG 当时只占 329 MiB，无压力）。
生产侧 154/245 的 PG 是 3.5 GB（`docs/database/2026-10-02-db-audit-34pg17-and-optimization.md`
实测），**不受影响**。

**加门**：`scripts/deploy-local-lib.sh` 新增 `dl_pg_check_memory`，
在 PG 探活成功后检查 `SHOW shared_buffers`，低于 128 MB 时告警
（`DL_PG_MIN_SHARED_BUFFERS_REQUIRED=1` 可 fail-closed）。
判据：`scripts/deploy-local-lib-pgmemory_test.sh`（7 组断言）。

> ⇒ **教训**：「部署成功（VERIFY_PASS=1）」与「数据可用」是两件事。
> 本仓的部署验证探的是**进程起来没、端口通不通、版本对不对**，
> 从来没探过「写进去没有」。这道内存门补的是**可观测口径**的缺口。

### 2.4 根因二（代码）：`sessions_v2.write_timeout_ms` 的 spec 默认静默撤销了一次修复

`internal/sessionv2mirror/hook.go` 的注释写着：

> raise the default budget to 2000ms

它把常量 `defaultShadowWriteTimeoutMs` 从 500 提到了 2000。**但那次修复从未生效。**

`settings.GetPlatformInt(key, fallback)` 的解析顺序是
**DB → env → `spec.Default` → 代码里的 fallback**：

```go
// settings/helpers.go
raw, _, err := Global.EffectiveValue(sp.Scope, key, "")   // ← 命中 spec.Default
if err != nil || len(raw) == 0 { return fallback }          // ← 走不到这行
```

而 `settings/spec_sessions_v2.go` 里这条的 `Default: 500` **一直没改**。
⇒ 实际预算一直是 **500 ms**。

500ms 撑不住一次 shadow write 的串行步骤（session_dim 维度写（其触发器还要
UPDATE session_summaries）→ session_turns → session_turn_details → session_bodies →
session_summaries）⇒ 细节层写超时 ⇒
- ① 正文（`session_bodies`）**一行都不落**（写顺序在 details 之后）；
- ② 整笔写失败 ⇒ 走 outbox 异步重放 ⇒ **重放顺序 ≠ 请求顺序** ⇒
  **turn_no 与时间序错位**（实测：第 1 轮拿到 `turn_no=3`）；
- ③ trace 找不到父行 ⇒ `trace_events` 丢失。

**处置**：`spec.Default` 500 → **2000**（与常量对齐）。
**加门**：`settings/session_write_timeout_default_test.go` —— 钉住
「spec.Default == `internal/sessionv2mirror` 的常量」，两边不许再各写各的；
另加一道**方向性**门（值不得低于 2000ms）。
判据从**源码字面量**取常量（不是复制品），常量改名/改值都会红。

### 2.5 根因三（代码）：维度写与轮次写共用同一个 deadline

`runShadowWrite` 里，`session_dim` 维度写与轮次写共用一个 ctx（同一个 deadline）。
维度写是 `INSERT … ON CONFLICT (gw_session_id) DO UPDATE`，其触发器还要去
`UPDATE session_summaries`；这一步慢或多等几把锁，就把**轮次写**的预算吃光。

⇒ 形态正是 LESSONS「同一次判定里的不同判据可以有不同的时间语义，不要因为共用一个
函数就让它们共用同一个 gate」：**维度写是 best-effort 的旁路，轮次写才是判定主体。**
让旁路吃主体的预算 ⇒ 主体失败、旁路却「成功」了。

**处置**：维度写用**自己的** ctx；轮次写的 ctx **在维度写之后才建**。

> ⚠ **第一版修法不够，被自己的判据当场抓住**：光「再开一个 ctx」没用 ——
> 两个 `WithTimeout(bg, timeout)` 的 deadline 是**同一个绝对时刻**（t0+timeout），
> 维度写照样把主体的 deadline 耗光。实测轮次写拿到的剩余预算是 **−0.8 ms**。
> ⇒ 轮次写的预算必须**从「轮次写开始」起算**。
>
> 代价（如实标注）：一次 shadow write 的**总**墙钟上限从 `timeout` 变成
> ≤ 2×timeout。这是有意取舍：上限变松，换主体不再被旁路饿死。
> 调用方是后台 goroutine（`shadowWriteSema` 限流），worker 侧不受影响。

**加门**：`internal/sessionv2mirror/shadow_write_budget_test.go`（行为门：
让维度写把预算耗光，轮次写仍必须拿到**完整**预算）。

### 2.6 修复后的实测

同一条命令、同一批模型、同样 3 轮：

| | 修复前 | 修复后 |
|---|---|---|
| HTTP | 5×200 + 1×503 | **6×200** |
| 单轮延迟 | 4.1 / 9.6 / 7.8 / 30.2 / 56.5 / 7.0 s | **4.1 / 3.3 / 4.8 / 7.2 / 5.3 / 12.6 s** |
| 落库轮次 | 3/6 | **6/6** |
| `trace_events` | 2/6 | **6/6** |
| 正文三件套 = array | 0/6 | **4/6**（另 2 轮是修复前的旧行） |

### 2.7 变异验证

| 变异 | 结果 |
|---|---|
| M1 维度写改回与轮次写共用 ctx | ✅ 红（轮次写剩余预算 ≤ 预算一半） |
| M2 spec.Default 2000 → 500 | ✅ 红（方向性门点名「撑不住一次 shadow write」） |
| M3 常量 2000 → 1500 | ✅ 红（一致性门点名「两边不一致就等于悄悄改了写预算」） |
| M4 PG 内存门：比较恒假 | ✅ 红（400kB 不告警） |
| M5 PG 内存门：MB 换算系数错 | ✅ 红（128MB → 128000kB） |
| M6 PG 内存门：默认改成 fail-closed | ✅ 红（默认必须 warn，不能掐死别人的部署） |
| M7 PG 内存门：判据脚本自身被改坏 | ✅ 红 |

每个变异都**逐字还原并用 md5 核对**（`internal/sessionv2mirror/hook.go`
`a814fa36…`、`settings/spec_sessions_v2.go` `259e6ba2…`，还原后两道门复跑全绿）。

---

## 3. 写侧复核：v1 宽族是否真的零写入

### 3.1 代码侧：三个流量写点全部挂门

| 写点 | 位置 | 门 |
|---|---|---|
| telemetry 主行 INSERT | `domains/hooks/observability/telemetry/client.go:1430` | `logsWrite := requestLogsWriteEnabled()`（:1360），`if logsWrite {` 开启整段（:1418 起） |
| telemetry 正文 UPSERT | `domains/hooks/observability/telemetry/client.go:3119` | `upsertRequestLogBodies` 只被门内调用点 `:534` 触达；`mirrorRequestBodies` 在 `:1216` 门内 |
| 音频计量行 | `domains/streaming/audio_usage_log.go:62` | `if !settings.RequestLogsWriteEnabled() { return }`（:53）——2026-10-13 补挂，「宽表家族最后一个未挂门的流量写点」 |
| admin ingest | `admin/telemetry.go:463` / `:572` | `if requestLogsWriteEnabled() {`（:454） |

### 3.2 数据侧：实测零行（**在真实流量之后仍然零**）

```sql
_request_logs_hot        0 行    _request_logs            0 行
_request_logs_bodies_hot 0 行    session_turns_hot       有行（max ts 随流量前进）
session_bodies_hot       有行    session_turns / session_bodies  有行
```

开关实测值（`settings_kv`）：`storage.request_logs_write_enabled=false`、
`sessions_v2.enabled/l3_read/primary_read/shadow_write=true`、
`storage.session_bodies_native_read=true`、
`storage.admin_logs_native_turns_read` **无记录 → 默认 false**（有意，见 §4.3）。

⇒ **写侧退役成立**：v1 宽族零写入，新流量全部落 session 族。

---

## 4. 读侧复核：视图 / 物化视图 / API 三个层面

### 4.1 物化视图（8 张）：全部已切到 session 族或与 v1 无关

| 物化视图 | 源 |
|---|---|
| `routing_analytics_7d` | `credentials` + `routing_analytics_source` |
| `routing_audit_summary_7d` | `routing_analytics_source` |
| `session_client_stats` / `session_owner_stats` / `session_task_stats` / `session_client_task_matrix` | `session_summaries` |
| `tuning_signals_5m` / `tuning_signals_daily` | `tuning_signals` |

`routing_analytics_source` → `request_logs_with_current_month` → session 臂
⇒ **MV 链已切**。实测库里 8 张 MV 的定义**零** `request_logs` 引用。

### 4.2 视图（13 张触及 request_logs 族）：1 张已切、12 张仍读 v1

| 视图 | 判定 |
|---|---|
| `request_logs_with_current_month` | **已切**（canonical，定义含 `session_turns`） |
| `request_logs_with_current_month_without_customer_id` | 仍读 v1（`_request_logs_hot` ∪ `_request_logs`） |
| `request_logs_with_current_month_without_request_class_due_at` | 仍读 v1（包装上一张） |
| `request_logs_bodies_with_current_month` | 仍读 v1（`_request_logs_bodies_hot` ∪ `_request_logs_bodies`） |
| `request_logs_bodies_progress` | 仍读 v1（v1 正文回填进度；停写后恒 0 —— 对**冻结**族是**正确**读数） |
| `customer_cost_view` | 主表 `api_key_model_cost` + **v1 子查询** `active_concurrent`（5 分钟窗） |
| `model_cost_per_task_view` | 主表 `api_key_model_cost` + **v1 子查询** `avg_latency_ms`（7 天窗） |
| `routing_analytics_source` | 读 canonical 视图 ⇒ 实际已切 |
| `v_continuation_effectiveness` / `v_model_health_dashboard` / `v_node_switch_analysis` / `v_probe_system_health` / `v_timeout_effectiveness` | 仍读 v1 裸母表（`internal/sqlreadguard` 门里登记为 `DEBT(R47)`） |

前 3 张 v1-only 视图是**有意建成**的：`cmd/gateway/request_logs_view_session_arm_pin_test.go`
把它们登记在 `v1OnlyRequestLogsViews`，注释写明「577/734 迁移为规避投影缺失而故意
建成 v1-only 的中间层」。该门从 `pg_get_viewdef` 重算并与代码双向比对
⇒ **视图形状漂移会被当场抓住**。

### 4.3 canonical 视图的真实形状（效率相关）

`request_logs_with_current_month` 是**三臂 UNION**：

```
臂1  session_turns_hot   LEFT JOIN session_turn_details_hot
臂2  session_turns       LEFT JOIN session_turn_details
臂3  (request_logs_with_current_month_without_request_class_due_at v
        LEFT JOIN LATERAL ( _request_logs_hot ∪ _request_logs ) )
      WHERE NOT EXISTS (session_turns_hot …) AND NOT EXISTS (session_turns …)
```

臂3 是「v1 行里扣掉会话族已有的那些」。它的两个 `NOT EXISTS` **没有时间窗**，
所以每次读都要对整个 `session_turns` 族建哈希探针。修复 PG 内存后本地实测
（871 行会话数据，7 天窗）：**15.4ms**。

> ⚠ 这条**不是**本轮新发现：仓内已有裁决记录（`db/v1_freeze_cutoff.go` 头注：
> R36-A1 实测「canonical 视图的反连接 + 租户过滤 COUNT 30s 超时」），
> 且 `db.V1FreezeCutoffSQL` 已被 `admin/tenants.go` 与 `bg/auto_index_refresher.go`
> 采用。**canonical 视图本体仍走反连接**是有意保留的（去重必须逐行恰计一次）。

### 4.4 API 读源

`admin/logs.go` 的列表/详情读源由 `logsSourceFromSQL()` 二选一：

- 默认 → `request_logs_with_current_month rl`（canonical 视图）
- 打开 `storage.admin_logs_native_turns_read` → `db.SessionFamilyTurnsSourceSQL()`

**为什么默认不开**（`admin/logs_turns_source.go` 头注有实测依据）：真库 2,321,464 个
request_id 里有 **641,452 个（27.6%）** 在原生源查不到（无会话头流量：探针/自检按
设计排除、in_progress 占位、标题/摘要生成器回环）。日志列表是**不带会话谓词**的
通用流量读路径 ⇒ 切过去会静默少 27.6% 的行。
⇒ 判据是**谓词形态**而非「镜像补齐没有」：按 request_id 反查 / 全量时间窗聚合
**禁止**迁；带 `gw_session_id` 的会话内读**可以**迁（已迁）。该分类固化为
`admin/session_view_dependency_risk_test.go`。

正文腿（`fetchRequestBodies` / `fetchRequestOutboundBody`）顺序是
**session 族优先 → v1 兜底**（`admin/logs_bodies_session_leg_order_test.go` 钉死）。

---

## 5. 效率

### 5.1 修 PG 内存前后（同一台机、同一批数据）

| 指标 | 修前 | 修后 |
|---|---|---|
| `session_turns_hot` 空查询**规划** | 1195 ms | **50 ms** |
| `session_summaries` 空查询规划 | 745 ms | **99 ms** |
| `session_bodies_hot` 空查询规划 | 271 ms | **12 ms** |
| canonical 视图 COUNT（7 天窗） | — | 15.4 ms |
| 真实请求延迟 | 4.1–56.5 s | **3.3–12.6 s** |

⇒ **本机此前所有的「慢 SQL」读数都是环境问题，不是 SQL 问题。**
这一点必须写进交接：本地量出来的毫秒数在生产（3.5 GB shared_buffers）不成立。

### 5.2 列表端点的三扫（结构性，未改）

`admin/logs.go:listLogs` 对**同一行集**做三次独立扫描：

1. `SELECT COUNT(*) FROM <logsFrom> WHERE …`（:635/:636）
2. `SELECT SUM(prompt_tokens), SUM(completion_tokens), … FROM <logsFrom> WHERE …`（:678/:680）
3. 分页列表本体

⇒ 读一次列表页 = 3× canonical 视图成本。COUNT 与 SUM 可合并为一次聚合。
本轮**未改**（在本地小数据集上无法证明收益，且改动面在热路径上）。

### 5.3 索引侧（本轮随上游入库）

862 给两个分区父表补**无谓词 (ts) 前导索引**；870/873 给 usage_ledger 补覆盖索引；
874 给 `session_turns.client_request_id` 补索引；877 给 `session_turns.search_text`
补 trigram（配合 `?q=` 检索）。

### 5.4 ★ `/api/admin/data-lifecycle/storage/tables` 稳定 500：两个独立缺陷

**现象**（API 全量扫描时发现，随后 3/3 复现）：

```
GET /api/admin/data-lifecycle/storage/tables  → 500, t=15.02s
{"error":{"detail":"查询表大小失败"}}
日志：storage: table sizes query failed error=context deadline exceeded
      admin handler internal error op=查询表大小失败 caller=data_lifecycle_storage.go:724
```

**根因一（代码）：TOAST 列是一条相关子查询，既取错值、又逐行重扫全表。**

原表达式：

```sql
COALESCE(pg_total_relation_size((
  SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid)), 0)
```

- **值恒为 0**：子查询找的是「c 自己是某张表的 TOAST 表」；而本查询限定
  `c.relkind IN ('r','p')`，TOAST 表的 `relkind` 是 `'t'` ⇒ **永远匹配不上**。
  实测：用户 schema 下 451 张 r/p 表**可命中 0 张**，而其中 **435 张确实有 TOAST 表**。
  ⇒ `toast_bytes` 这个字段一直是 0，而 `StorageOverview.vue:186` 照着渲染它。
- **代价极高**：planner 把它落成对 `pg_class` 的**逐行**扫描，且有**两种**形态：
  - (a) `SubPlan` → `Seq Scan on pg_class`，`loops=N`；
  - (b) 半连接拉起 → `Nested Loop` 的内层
    `Seq Scan on pg_class Filter=(... AND (reltoastrelid = c.oid))`。

  实测同一台机、同一批数据，坏/好写法**交错重复**各 3 轮：

  | 写法 | 第 1 轮 | 第 2 轮 | 第 3 轮 | 中位 |
  |---|---|---|---|---|
  | 坏（相关子查询） | 5596 ms | 5791 ms | 6686 ms | **5791 ms** |
  | 好（`c.reltoastrelid`） | 2976 ms | 2677 ms | 3241 ms | **2976 ms** |

  ⇒ **−49%，两组区间不重叠**，差异远超噪声。
- 改法：直接用 `c.reltoastrelid` 这一列（零 join、零子扫描，且是真值；
  无 TOAST 时为 0，`pg_total_relation_size(0)` 返回 NULL，被 `COALESCE` 兜成 0）。

**根因二（代码）：采集预算 == 请求预算 ⇒ 采集永远跑不完，快照永远建不出来。**

`storageCollectTimeout` 原为 15s，与 handler 的请求预算同值。采集的 ctx 是从
`context.WithoutCancel` 派生的（采集**不**挂在请求 ctx 上）⇒ 请求超时**不会**
掐死采集，**真正会掐死它的只有 `storageCollectTimeout`**。于是：冷页缓存下一次
采集要 11–17s ⇒ 采集在 15s 被自己掐掉 ⇒ 快照建不出来 ⇒ 端点 500 ⇒
**每次请求都重试一次、再掐死一次**，没有任何自愈路径。
⇒ 改：`storageCollectTimeout` 15s → **60s**（请求侧预算仍是 15s，用户不等待更久）；
并把两个预算提成具名常量 `storageRequestTimeout` / `storageCollectTimeout`，
用一条门钉住「采集预算 > 请求预算」这个结构前提。

**环境因素（不是缺陷，但必须记账）**：本机 PG 的数据目录挂在 Docker Desktop 的
**virtiofs** 上（`docker inspect`：`bind /Users/niuer/kaixuan/postgres-citus →
/var/lib/postgresql/data`）。于是**每次 `stat()` 是一次到宿主机的 RPC**，
实测 0.2–1.2 ms/次；`pg_total_relation_size` 一次要对 451 张表 stat
主 fork / fsm / vm / toast / 每个索引（≈6000 次）⇒ 光 stat 就要 **4.2s**。
生产（Linux 原生文件系统）没有这层开销，**该开销在生产不成立**。
⇒ 判别动作：本机读到的「秒级 size 查询」先问「`stat()` 一次多少钱」，
再决定是改 SQL 还是改存储层。

**新增判据（4 条，全部做过变异验证）**

| 判据 | 钉住什么 | 变异 |
|---|---|---|
| `TestTableSizesToastBytesReportsRealValue` | 端点给的 `toast_bytes` 与 PG 真值**逐字节相等**且 > 0 | 把表达式改回相关子查询 → 红（`ToastBytes = 0` vs 真值 5709824） |
| `TestTableSizesQueryPlanHasNoCorrelatedSubplan` | 计划里**任何条件**都不得引用 `reltoastrelid` | 同上 → 红（`Filter=(... (reltoastrelid = c.oid))`） |
| `TestStorageCollectBudgetOutlivesRequestBudget` | 采集预算 > 请求预算 | 预算压回 15s → 红 |
| `TestTableSizesCollectSurvivesCallerCancellation` | 调用方 ctx 取消后，采集**仍跑完并写入快照** | 采集改回挂调用方 ctx → 红（90s 后快照仍未出现） |

> ⚠ 计划形状门**换过一次**：第一版钉「计划里有没有 `SubPlan` 节点」，
> 变异时计划恰好落到半连接形态 ⇒ 门**假绿**。现版钉的是**不变量**
> （条件里不得出现 `reltoastrelid`），与 planner 选哪种落地形态无关。

---

## 5.5 ★ 逐页浏览器验证（104 页）：0 个慢 API、0 个 JS 异常，抓出 9 个真 500

> 口径：Maestro 方法论（逐页 + 逐 API + 计时），但**不用 Maestro CLI**
> （本机无 Java/adb），改用**无头 Chromium 脚本**（playwright-core 1.64.1 +
> 本机缓存的 Chrome for Testing 1223）。
> **为什么换载体**：内嵌 Browser 每个标签页常驻一条 SSE（`/api/admin/live-stream`），
> 而 Chromium 对同源 HTTP/1.1 只有 **6 条**连接（整个浏览器共享）⇒ 多标签页时
> 新请求被**客户端取消** ⇒ 服务端看到 `context canceled` ⇒ 页面「完整、无报错、
> 数据恒为空」。这是**工具/环境约束**，不是应用缺陷（同一请求 curl 恒 200）。
> 无头脚本**单页顺序访问** ⇒ 每时刻最多 1 条 SSE ⇒ 争抢消失。
>
> 覆盖范围：85 条直接可访路由（`web/src/router.ts` 里不含 `:`/`*` 的 path，
> 去掉 `/login`、`/forbidden`、`/`）+ **19 条带参路由的具体实例**
> （从真库取真实 id 构造，不造假 id）= **104 页**。
> 取数命令：
>
> ```bash
> cd /tmp/llmgw-retire-20261020/pw
> node sweep.mjs            # 逐页：document 状态 + 每个 /api/ 响应的状态与耗时
>                           #       + console/page error + main 容器文本标记
> # 产物：sweep.tsv（104 行），逐页一行
> ```
>
> 认证：`Authorization: Bearer <admin JWT>` 注入到 context（不落盘、不打印）。

### 5.5.1 结论

| 口径 | 结果 |
|---|---|
| 文档级可达 | **103/104** 返回 200；唯一例外 `/maintain` = 503（本机未部署 maintain 服务，**预期**） |
| 路由改写 | 20 条重定向到规范别名（`/maas/*`→`/tenant/*`、`/customer/*`→`/customer/update-activate`、`/dev/*`→`/`（dev 路由只在 `import.meta.env.DEV` 注册））——**全部有意** |
| **API 耗时 ≥ 2000ms** | **0 页 0 个**（用户验收线「超过 2 秒要定位慢因」全部达标） |
| 页面加载耗时 Top1 | 1940ms（`/dev/waterfall-preview`），**全部 < 2000ms** |
| **JS 运行时异常（pageerror）** | **0 页**（104 页全部为空） |
| console error | 全部页面都有，**内容只有一种**：`503 (Service Unavailable)` —— 即 `/maintain-api/healthz` 与 `/maintain-api/menu/ops`（本机无该服务，前端已优雅降级） |
| **5xx API** | **9 处 / 6 页**（剔除 maintain-api 的 503）—— 见 §5.6 |

### 5.5.2 逐页扫描顺带暴露的真问题：`/request-logs` 与 `/admin/turns` 的「共 0 条 / 加载中…」

内嵌 Browser 里 `/request-logs` 恒显示「共 0 条 / 加载中…」，服务端
`admin listLogs count query failed scope=super_admin error="context canceled"`。
本轮把它查实了：

1. **不是服务端慢**：`GET /api/logs` 用 curl 逐字重放（同一 query 串）恒 **200 / 151–156 条**。
2. **是客户端取消**：服务端 `context canceled` ⇒ Go 的 `http.Server` 在**客户端断开**时
   取消 request ctx。浏览器同源连接池（6 条）被同浏览器其它标签页的 SSE 占满时，
   排队中的请求会被**放弃**⇒ 服务端记 500。
3. **同一端点、同标签页，时好时坏**：`/api/admin/turns/sessions?limit=20`
   在 06:26:34 = **500**（ctx canceled）、06:26:36 = **200 / 39ms**、06:26:40 = **500**。
   ⇒ 与「端点有缺陷」不相容 ⇒ 争抢相关。
4. **前端没有「用 0 冒充没有」**：`RequestLogsView.load()` 有 `catch → error.value`，
   `TurnsListView` 有 `error-banner` + 「加载会话失败，请重试」⇒ **两端视图都做了错误态**，
   「共 0 条 / 加载中…」是**请求还没结束时的中间态**（`loading=true` 时刷新按钮 disabled）。
5. **服务端仍有一处真缺陷**：客户端取消被记成 `500 / error.kind=server_error`
   （`middleware/logging_mw.go` 的 `errorKind(status)` 只看状态码）⇒
   **「客户端跑了」与「服务端坏了」在监控里长得一样**。本轮**未改**（共享中间件，
   改动面大），记为遗留 **L7**。

> 判据纪律：**「端点坏」与「端点被取消」必须用同一请求的两种证据分开**
> ——本轮用的是「curl 逐字重放 200」+「服务端 ctx canceled」两条一起。

---

## 5.6 ★ 逐页扫描抓出的 5 个真 500：形状错配、缺列、空聚合、非法 id

9 处 5xx 里 4 处是同一端点的多次触发 + 2 处 maintain-api（预期）⇒ **5 个真问题**。
**全部用 curl 逐字复现**（⇒ 不是浏览器假象）：

| 端点 | 复现 | 服务端错误原文 | 根因 |
|---|---|---|---|
| `/api/admin/prompt-injection/stats` | 500 | `42P01 relation "prompt_injection_stats_enhanced" does not exist` | 视图建不出来（见下） |
| `/api/admin/prompt-injection/detections?page=1&page_size=20` | 500 | `42703 column "detection_score" does not exist` | 代码查的是**非正典**的列 |
| `/api/admin/sessions/{id}/snapshot` | 500 | `42703 column s.user_tags does not exist` | 该列**不在正典基线**里 |
| `/api/admin/session-analytics/users/{owner}?days=30` | 500 | `can't scan into dest[1] (col: total_requests): cannot scan NULL into *int64` | 空聚合扫进非空标量 |
| `/api/v1/approvals/1` | 500 | `22P02 invalid input syntax for type uuid: "1"` | 非法 uuid 喂给 uuid 列 |
| （`/api/free-discovery/*` ×3） | 503 | `free-discovery is not available (database disabled)` | **功能未启用**，非缺陷 |

### 5.6.1 ★ 最重的一个：正典表形状与「历史迁移 315 的形状」不是一回事

`prompt_injection_detections` 的**正典形状**在
`sql/schema/01-schema.sql:11398` 与 `sql/objects/tables/prompt_injection_detections.sql`：

```
risk_level integer NOT NULL   -- 带 CHECK (risk_level >= 1 AND risk_level <= 10)
rule_id / rule_name / category / matched_pattern / input_sample
categories injection_category[] / canary_token_leaked / approval_id / …
（**没有** detection_score / matched_rules / matched_rules_count / detection_layers）
```

而**历史迁移 315**（`sql/migrations/startup/315_prompt_injection_detection.sql:129`）
声明的是另一套形状（`detection_score INT NOT NULL` + `matched_rules JSONB` +
`matched_rules_count` + `detection_layers` + `risk_level VARCHAR`）。

**致命的一步**：315 的建表语句是 `CREATE TABLE IF NOT EXISTS` ⇒ 在**已有正典表的库上
是空操作** ⇒ 315 的那几列**从未被创建**。于是：

1. **读侧**（`admin/prompt_injection_handler.go`）查 315 的列 ⇒ 恒 `42703` ⇒ 端点 500；
2. **写侧**（`domains/promptinjection/detector.go` 的 `DetectAndLog`）也 INSERT 315 的列
   ⇒ 恒 `42703` ⇒ **每一次注入检测的审计记录都被静默丢弃**
   （失败只落一行 `slog.Warn` ⇒ 检测照常生效/拦截，只是没有留痕
   ⇒ 于是「这批请求没有注入命中」成了假结论）；
3. **视图** `prompt_injection_stats_enhanced`（迁移 364）引用 `d.detection_score`
   ⇒ **在任何正典库上都建不出来** ⇒ 统计端点 42P01。
   就算建得出来，它写的 `((risk_level)::text = 'critical')` 对**整数**列恒不匹配
   ⇒ 那 4 个严重等级分桶**恒为 0**。

⇒ **⇒ 这不是「某个环境没升级」，是**全环境**的形状/代码错配**（本机与新装库同形）。
2026-10-12 的 schema 对齐审计（`docs/audit/2026-10-12-schema-parity-252-vs-local/`）
已把它记为「本地 0 行，可直接 NOT NULL」并给了一份 `02-local-fix-extra.sql`，
但那份修复 SQL **自身也自相矛盾**（补了 `detection_score`，却仍按字符串比 `risk_level`）。

**本轮的修法（以正典形状为准，不发明语义）**：

- 分数落 `risk_level`（正典语义：它就是 1..10 的分值）；
- 等级字符串由**同一套阈值**派生：`≥10 critical / ≥8 high / ≥6 medium / else low`
  —— 阈值**沿用**代码里既有的 `calculateRiskLevel`，不是新口径。
  写侧（Go）与读侧（SQL）共用**同一个**定义
  （`domains/promptinjection.RiskLevelFromScore` / `RiskLevelSQLExpr`），
  否则会出现「按 high 筛出来的行里写着 medium」；
- `matched_rules` ← 命中的最高危规则的 `rule_name`；
  `matched_rules_count` ← `categories` 的元素个数；
  正典表没有的 `rule_id` / `matched_pattern` / `input_sample` **留空而不是编造**；
- 统计端点**不再依赖那个建不出来的视图**，直接在正典表上聚合。

### 5.6.2 其余三个的修法

| 缺陷 | 修法 | 为什么这么修 |
|---|---|---|
| `sessions.user_tags` 缺列 | 加进 `db.go` 的 **ensure 链**（`ensureSessionUserTags`）：列不在位才 `ALTER TABLE … ADD COLUMN IF NOT EXISTS`，并用 `columnsAllPresent` 预探测（列已在位就**不做 DDL**，`ADD COLUMN IF NOT EXISTS` 即便列已存在也要先取 ACCESS EXCLUSIVE，§10.97 同族） | 该列**不在正典基线**里 ⇒ 只有走 installer（`StartupFiles` 含 467）的新装库才有 ⇒ 任何 deploy 路线演进来的库都缺 ⇒ **ensure 链是本仓既有的「环境对齐」通道**（096141ecc 同族） |
| 画像聚合 NULL 扫描 | `COALESCE(SUM(...), 0)` 覆盖 4 个聚合；`MIN/MAX` 时间戳改用可空指针接收后落零值 | 「该 owner 没有会话」是**正常状态**，不该 500；零值时间 = 从未出现，不是 1970 事实 |
| `approvals/{id}` 非法 uuid | 在**错误分类**层把 PG 的 `22P02` 归一为 `ErrNotFound`（⇒ 404），**不做入参校验** | 域层不该知道「id 是 uuid」这种存储细节；也正因如此既有单测用非 uuid 假 id 是合法的，入参校验会打断它们 |

### 5.6.3 顺带修掉的两个「被掩盖的缺陷」

- `detected_at`（timestamptz）扫不进 `*string` ⇒ 改 `*time.Time`
  （JSON 仍是 RFC3339 字符串，前端 `fmtTime` 可解析；列可空时如实给 null）。
  这个缺陷**一直被掩盖**：旧查询在更早的 `42703` 就挂了。
- 计数查询原先靠 `strings.Replace` 把 SELECT 换成 `SELECT COUNT(*)`
  ——**字符串级替换**：SELECT 文本一改（换行/空格/字段顺序）就**静默不生效**
  ⇒ count 查询返回整行集 ⇒ `Scan(&total)` 报 "cannot scan ..."。改为
  **显式 count 查询 + 共享 WHERE**（`promptInjectionDetectionsCountFrom`）。

### 5.6.4 新增判据（6 道，全部做过变异验证）

| 判据 | 层 | 变异（改坏） | 结果 |
|---|---|---|---|
| `TestInsertDetectionSQL_LandsInCanonicalShape` | 写侧（真库，事务+回滚） | INSERT 换回 315 形状 | **红** ✔ |
| `TestInsertDetectionSQL_OldShapeStillFails` | 写侧（真库） | —— | 绿（旧形状必须仍失败） |
| `TestRiskLevelFromScore_MatchesSQLExpr` | Go↔SQL 阈值同源（真库逐档比对） | 阈值漂移 | 可被抓 |
| `TestPromptInjectionQueries_RunOnCanonicalShape` | 读侧（真库逐字跑生产 SQL） | 投影换回 315 形状 / 统计改读视图 | **红** ✔ / **红** ✔ |
| `TestPromptInjectionHandler_NoStringReplaceForCount` | 读侧（静态） | 计数回到字符串替换 | 可被抓 |
| `TestPromptInjectionStats_NoDependencyOnMissingView` | 读侧（静态） | 统计改读视图 | **红** ✔ |
| `TestUserProfileStatsSQL_NullAggregatesAreCoalesced` | 画像（真库逐字跑生产 SQL） | 去掉 COALESCE | **红** ✔ |
| `TestEnsureSessionUserTags_RealDB` / `_IsOnTheBootChain` | ensure（真库 + 静态） | ensure 脱链 | **红** ✔ |
| `TestIsMalformedUUIDInputError` | 错误分类（纯函数，同层） | 恒 false | **红** ✔ |

> **这几道门在写它们的过程中当场抓到了 4 个我自己的错误**：
> `AS c` 把列别名写成了表别名；`FROM` 跑到了子查询**外面**；
> `categories` 绑 JSON 字节（PG 数组字面量解析失败 22P02）；
> 判据自己把 `categories` 扫进 `*string`。
> ⇒ **真库门（让 PG 自己解析）能抓到 mock 抓不到的东西**——这与攻击向量库
> 那次（a56338ee2）同源：**pgxmock 对参数类型不敏感，旧 bug 在 mock 下照样全绿**。

### 5.6.5 修后复验（部署 `2.5.8.2753`，`VERIFY_PASS=1`）

**逐页全量重扫（同一脚本、同一 104 页、同一口径）**：

| 口径 | 修前 | 修后 |
|---|---|---|
| 真 5xx（剔除 maintain-api 503） | **9 处 / 6 页** | **3 处 / 1 页** —— 剩下 3 处全是 `/free-discovery` 的 **503「功能未启用」**（预期） |
| 慢 API ≥2000ms | 0 | **0** |
| 页面加载 ≥2000ms | 0 | **1 页**（`/dashboard` 2004ms；它首屏打 87 个 API，属边界值） |
| 文档级不可达 | 1（`/maintain` 503） | 1（同上，预期） |
| JS 运行时异常 | 0 | **0** |

**逐端点 curl 复验（修前 → 修后）**：

| 端点 | 修前 | 修后 |
|---|---|---|
| `GET /api/admin/prompt-injection/stats` | 500 | **200**（`total_detections:0` 等全零） |
| `GET /api/admin/prompt-injection/detections?page=1&page_size=20` | 500 | **200**（`detections:[]`） |
| `GET /api/admin/sessions/{id}/snapshot` | 500 | **200**（真实数据：`total_turns:1, total_tokens:33`） |
| `GET /api/admin/session-analytics/users/default?days=30` | 500 | **200**（零值） |
| `GET /api/admin/session-analytics/users/<不存在的 owner>` | 500 | **200**（零值，时间戳落零值 `0001-01-01T00:00:00Z`） |
| `GET /api/v1/approvals/1`（非法 id） | 500 | **404** `{"error":"approval not found"}` |
| `GET /api/v1/approvals/<合法 uuid>` | — | **404**（不存在的审批，正确） |

> 产物来自**未提交的工作区** ⇒ `version.json` 里的 `git_sha` 仍是 HEAD
> （`8cec460b`），**不能**用它证明新代码在跑 ⇒ 用**行为变化**证明
> （上表：500 → 200/404，且 snapshot 返回真实轮次数与 token 数）。
> ⇒ 判别动作：**当部署产物来自未提交源码时，「版本号对得上」不是证据**。

---

## 5.7 ★ 判据自身的竞态：`data-lifecycle/storage/tables` 的「字节数对账」在活库上间歇性红

### 5.7.1 症状

`go test ./admin/`（带 `TEST_PG_URL`）在**同一份代码**上出现**第三条**失败：

```
--- FAIL: TestDataLifecycleTableSizesFieldsAreComplete (1.44s)
```

但同一提交**再跑一次就过**（`-v` 全绿），pristine worktree（`8cec460b3`，
不含本轮改动）也**过** ⇒ 不是本轮引入，也不是稳定复现的缺陷 ⇒ 先怀疑**判据本身**。

### 5.7.2 根因：判据与被测对象读的是**同一个函数**，却跨两次读做精确相等

- 端点口径：`admin/data_lifecycle_storage.go` 的 `tableSizesSQL` 里
  `TotalBytes` 取的是 **`pg_total_relation_size(c.oid)`**；
- 判据口径：`admin/data_lifecycle_storage_tables_perf_test.go` 的 live 查询用的**也是**
  `pg_total_relation_size($1::regclass)`。

⇒ 两个值相等的前提是「**两次读之间那张表没变**」——而首行是**全库最大的表**，
本机实测首行为 `public.ursm_node_snapshot_min`（231MB），**网关的 URSM worker
正在批量写它**（`n_tup_ins=49283`、`n_tup_del=0`，实测 5 秒涨 64KB）。
快照采集与随后的 live 查询之间只要落进一次写入，两者就不相等。

**取数命令**（本机 PG 54318；DSN 走 `DATABASE_URL`，**不落盘**）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
"$PSQL" "$DATABASE_URL" -tAc "SELECT c.relname, pg_total_relation_size(c.oid) \
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace \
  WHERE n.nspname='public' AND c.relkind IN ('r','p') ORDER BY 2 DESC LIMIT 5;"
"$PSQL" "$DATABASE_URL" -tAc "SELECT n_tup_ins, n_tup_del, n_live_tup \
  FROM pg_stat_user_tables WHERE relname='ursm_node_snapshot_min';"
```

**口径**：`pg_total_relation_size` = heap + TOAST + indexes；端点与判据口径**完全一致**，
所以差异只可能来自「两次读之间表变了」。
**覆盖范围**：`public` schema 的 `r`/`p` 两类关系；端点 `tableSizesSQL` 的
`WHERE n.nspname NOT IN ('pg_catalog','information_schema')` **还包含非 public schema**
（见 §5.7.3）。

### 5.7.3 第二层竞态：首行可能是**用例自己建的临时 schema 里的表**

端点的 `WHERE` 不排除非 public schema，而 admin 包的真库门普遍用
`dlperf_<test>_<pid>_<nanos>` 这类临时 schema（用例结束 `DROP CASCADE`）。
⇒ 首行可能是一张**即将被删掉**的表，live 查询直接 42P01。

### 5.7.4 判据与它自己声明的不一致

该文件**上方的注释**写的是「字节数一致（**±1%**）」，**实现**却是
`if first.TotalBytes != liveBytes` —— 逐字节精确相等。
⇒ **判据没做到它自己声明的事**（判据纪律：注释不是证据，断言才是）。

### 5.7.5 修法

- 按**它自己声明的 ±1%** 比对（容差取 `max(1%, 64KB)`：小表上 1% 可能小于一个页
  8KB，要给页级漂移留余量）；
- 关系已被删（42P01）或 live 值为 0 时**改判下一行**；
- 保留「**至少有一行成功对账**」的前提断言（否则会退化成「压根没对账」的假绿）。

**端点本身不改**——它返回的就是真实读数，变的是**判据的前提**。

### 5.7.6 变异验证（证明新判据不是假绿）

把生产读数改坏（`queryTableSizes` 里 `t.TotalBytes *= 2`），判据如期变红：

```
--- FAIL: TestDataLifecycleTableSizesFieldsAreComplete (2.13s)
    data_lifecycle_storage_tables_perf_test.go:146: 第 0 行 public.ursm_node_snapshot_min
      的 TotalBytes = 462536704，PG 实时 = 231268352（偏差 231268352 字节 > 容差 2312683）
      —— 响应不是真实读数
```

⇒ 容差 2312683 字节（1%）能抓住 100% 量级的读数错误；活库的写入漂移实测在 KB 级
（64KB/5s），两者差三个数量级 ⇒ 不会把「读数在变」误判成「读数不对」。

---

## 5.8 ★ 逐页扫描抓到的**唯一一条** JS 运行时异常：`/quality-correlations` 在「无数据」时崩

### 5.8.1 症状

合并上游 22 个提交并重新部署后，104 页全量重扫里出现了**唯一一条** JS 运行时异常：

```
[Vue Error] TypeError: Cannot read properties of null (reading 'reduce')
    at ac.fn (http://127.0.0.1:8782/assets/QualityCorrelationsView-Bq54SLjJ.js:1:2961)
    at Wi (http://127.0.0.1:8782/assets/vue-vendor-BSI0bAkk.js:9:3566)
    ...
```

页面**文档级 200、其它 API 全部正常**，只有这一条异常（不崩页，但那一格数据算不出来）。

### 5.8.2 根因：契约两端不一致 —— 后端返 `null`，前端类型说「非空数组」

| 端 | 事实 |
|---|---|
| 后端 | `admin/quality_correlations.go` 的 `var breakdown []QualityCorrelationRow` 是 Go 的 **nil slice** ⇒ JSON 序列化成 **`null`** 而不是 `[]`；而**同一个 handler** 里下方的 `insights := []QualityCorrelationInsight{}` 被**显式**初始化了 ⇒ 同一响应里两个数组字段的空值形态不一致，是疏漏 |
| 前端 | `web/src/api/tuning.ts` 的 `QualityCorrelationResponse.breakdown` 声明为 `QualityCorrelationRow[]`（**非空**）⇒ **类型是假的**；页面于是直接 `resp.value.breakdown.reduce(...)`，null 时当场抛异常。同一页另有 3 处 `resp.breakdown.length` / `resp.insights.length` 同样会炸 |

触发条件是**合法业务状态**，不是异常输入：本地库 `session_turns` 里
`is_auto_request IS TRUE` 的行数为 **0** ⇒ 聚合查询 0 行 ⇒ `breakdown` 为 `null`。

**取数命令**（DSN 走 `DATABASE_URL`，**不落盘**）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# ① 端点实际返回
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8782/api/admin/auto-route/quality-correlations?days=7"
#   ⇒ {"window_days":7,"by":"prompt_length","breakdown":null,"insights":[],"generated_at":...}
# ② 为什么是 null：查询读的是 session 族，而 auto 轮次为 0
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.session_turns WHERE is_auto_request IS TRUE;"
```

**口径**：端点读 `SessionFamilyTurnsSourceSQL()`（session_turns）LEFT JOIN
`SessionBodiesSourceSQL()`（session_bodies_hot），过滤 `is_auto_request = TRUE`
且 `ts >= NOW() - INTERVAL '1 day' * $1`。
**覆盖范围**：本机 PG 54318 的 `public.session_turns` 全表（876 行）——
`is_auto_request IS TRUE` 命中 **0** 行。

### 5.8.3 归属：既有缺陷，**不是**本轮引入

- 合并**没有动过** `admin/quality_correlations.go` 与 `web/src/views/QualityCorrelationsView.vue`
  （`git diff 8cec460b3 c75ec26ee -- <两文件>` 为空）；
- 上一轮（修完 5 个 500 之后）那次 104 页扫描报「0 个 JS 运行时异常」⇒ 当时
  `breakdown` 还不是 `null`（本地库那会儿有 auto 轮次）。

### 5.8.4 修法：后端改契约 + 前端在**边界**归一化（两层都做）

1. **后端**：`breakdown := []QualityCorrelationRow{}`（与同函数里的 `insights` 对齐）
   ⇒ 数组字段恒返 `[]`。
2. **前端**：`normalizeQualityCorrelations()` 放在**写进 `resp` 之前**（边界），
   把 `null`/非数组一律兜成 `[]`。
   - 为什么放在边界而不是模板里：模板继续用 `resp.breakdown`（既有跨层契约门禁
     `QualityCorrelationsView.responsive.test.ts` 钉的就是这个形态），而防御散落在
     4 个模板表达式里**就会漏掉第 5 个**；
   - 为什么前端也要做：前端**不能依赖后端**（同一份视图可能被别的后端实现喂数据）。

### 5.8.5 新增判据（3 道，全部做过变异验证）

| 判据 | 钉的不变量 | 变异 |
|---|---|---|
| `admin/quality_correlations_null_array_test.go::TestQualityCorrelations_ArrayFieldsAreNeverNull` | **数组型字段在响应里必须是 JSON 数组，永远不是 null**（真库 + 真 handler + **解进 map** 才能看见 `null` 与 `[]` 的区别） | 改回 nil slice ⇒ 红：`字段 breakdown 序列化成了 null` |
| `admin/quality_correlations_null_array_test.go::TestQualityCorrelations_BreakdownIsNotDeclaredAsNilSlice` | **写法**：源码里不能出现 `var breakdown []QualityCorrelationRow` | 同上 ⇒ 红：`又把 breakdown 声明成 nil slice 了` |
| `web/src/views/QualityCorrelationsView.nullbreakdown.test.ts` | **页面的承受力**：后端返 null 时必须渲染空态、不得抛异常（**不**断言「后端返 null」这个形态，否则后端修好后这道门变废门） | 去掉边界归一化 ⇒ 红，且报的正是生产同款 `TypeError: Cannot read properties of null (reading 'reduce')` |

> 真库门与静态门分工：真库门钉**输出**，静态门钉**写法** ——
> 把 nil slice 换回去在**有数据**的环境里输出不变，只有静态门能抓住。

### 5.8.6 连带修：行号锚点型登记表

`web/src/components/ui/mobileListUx.spec.ts` 的 `NON_NAVIGABLE` 登记表按
`[文件, 行号, 理由]` 锚定调用点。前端在 `<script setup>` 头部插入 37 行后，
`QualityCorrelationsView.vue` 的 `ResponsiveDataView` 调用点由 **186 → 223** ⇒
登记表必须跟着走（否则门报的是「找不到」而不是「不合格」—— 量具坏了，不是产品坏了）。

已按本仓先例改键并在登记处写明原因与行数（先例：
`internal/billguard/month_key_test.go` 2026-10-13 `:126→:145`、
2026-10-17 `:91→:243`）。

### 5.8.7 修后复验（部署 `2.5.8.2755`，`VERIFY_PASS=1`）

**端点行为变化**（部署产物来自**未提交**源码 ⇒ 版本号不能当证据，用行为变化证明）：

```
修前：{"window_days":7,"by":"prompt_length","breakdown":null,"insights":[],...}
修后：{"window_days":7,"by":"prompt_length","breakdown":[],"insights":[],...}
```

**104 页全量重扫**（同一脚本、同一 104 页、同一口径）：

| 口径 | 合并后（修前） | 修后 |
|---|---|---|
| 文档级不可达 | 1（`/maintain` 503） | 1（同上，预期） |
| **真 5xx**（剔除 maintain-api 503 / free-discovery 503） | 0 | **0** |
| **JS 运行时异常** | **1 页**（`/quality-correlations`） | **0 页** |
| 慢 API ≥2000ms | 0 | **0** |
| 页面加载 ≥2000ms | 1（`/dashboard`） | 1（`/dashboard` 2042ms，首屏 87 个 API，属边界值） |
| console error | 104 页（全为 maintain-api 503 的资源加载失败，预期） | 同上 |

**量具本身的修复**：`sweep.mjs` 写 TSV 时只 `replace(/\t/g,' ')`，**没剔换行** ⇒
JS 堆栈是**多行**的，整表被写坏（解析出来「106 行、route 重复、docStatus 为空」）。
已补 `flat()` 统一把换行也换成空格 ⇒ 本次读数 104 行、可逐行解析。
⇒ 判别动作：**量具产出必须先自检「行数 == 预期」再读数**。

---


| # | 项 | 性质 | 建议 |
|---|---|---|---|
| ~~**L1**~~ | ~~7 张 v1 分区表未带 `_` 待删标志 … 形状分叉~~ ⇒ **2026-10-20 复核轮内已订正，见 §5.9**：这不是缺陷，是迁移 860 的**明确设计**（待删标志在**父表**上，子表名按设计保持不变）；「新装与存量形状分叉」的前提**不成立**（正典基线 schema 造的就是无前缀分区名，862 那个 `_request_logs_2026_10` 只是**合成夹具名**）。**真正要修的是按名字枚举的口径**（已修，见 §5.9.3） | ~~真实不一致~~ → **前提有误** | 不出改名迁移（改名会与 860 的设计、基线 schema、`ensure_request_logs_partition` 的动态前缀脱节）；改为把两处按名字前缀枚举分区族的运维脚本改成**按 `pg_inherits` 走父表** |
| ~~**L2**~~ | ~~`customer_cost_view.active_concurrent`（5 分钟窗）与 `model_cost_per_task_view.avg_latency_ms`（7 天窗）是活指标读冻结族 ⇒ 恒 0 / 恒 NULL~~ ⇒ **2026-10-20 复核轮续做已修读源**（迁移 883 → 切会话族；真库行为门 + 5 道契约门，4 处变异转红；部署 `2.5.8.2757` 后活体直查：旧读源形状全 0/NULL，新读源取到真实延迟 4046.1ms / 6395.8ms，§5.12.6）。⚠ **但端点仍返回 `[]`**：外层聚合表 `api_key_model_cost` 的 trigger **从未接线**（真库全触发器清点确认它没挂在任何表上、该表 0 行、无唯一索引）⇒ 端点空是**新功能缺口**不是退役残留 ⇒ 记为 **L11**（见 §5.12.3） | ~~恒 0 / 恒 NULL~~ → **读源已修**，端点仍空（L11） | 复活聚合链：写侧 `canonical_id` → 唯一索引 → 会话族 trigger（§5.12.3 给了顺序与理由） |
| ~~L3~~ | ~~列表端点 COUNT / SUM / 分页三扫同一行集~~ ⇒ **2026-10-20 复核轮续做已修**：COUNT 与各 SUM 合并成**一条**聚合（同一行集的执行次数 2→1），降级语义保持；判据用 pgx QueryTracer 数查询 + 与旧两扫口径逐值对账（§5.12.4） | ~~效率~~ → **已收口** | 无 |
| L4 | 5 张 `v_*` 视图 + `request_logs_bodies_with_current_month` 仍读 v1 裸母表（`internal/sqlreadguard` 已登记 `DEBT(R47)`） | 已知债 | 逐张判定：历史可读（保留）vs 活指标（必须迁） |
| L5 | `storage.admin_logs_native_turns_read` 默认关闭 ⇒ 列表页继续付 canonical 视图的 v1 臂 + 反连接成本 | 已决策 | 保持关闭（27.6% 缺口），但**成本要显式记账** |
| L6 | 本机 PG 的 `shared_buffers` 原值 400 kB 是**手改在容器数据目录**里的，不在仓库内 | 环境 | 已在本机修好并加部署门；**任何新建的本机 PG 都要按门检查**（门只在本仓的 deploy-local 路径生效） |
| **L7** | **客户端取消被记成 `500 / error.kind=server_error`**（`middleware/logging_mw.go` 的 `errorKind(status)` 只看状态码）⇒ 「客户端跑了」与「服务端坏了」在监控里**长得一样**；且多数 handler 的裸 `writeError(500, …)` **不留任何日志**（只有 `handleTurnsSessions` 等少数几处打了 WARN） | 观测 | 在 `emitRequestLog` 里判 `r.Context().Err() == context.Canceled` ⇒ `error.kind=client_aborted`（不改级别，避免把真 5xx 降级）；并让裸 `writeError(500,…)` 路径也留一行 WARN。**本轮未改**（共享中间件，改动面大且会动全部请求的日志形态） |
| **L8** | `prompt_injection_stats_enhanced`（迁移 364）**在正典库上建不出来**（引用不存在的 `detection_score`）；本轮已让统计端点**不再依赖**它，但**视图本身仍在** ⇒ 任何直接查它的外部消费者（`web-mobile/src/api/promptInjection.ts` 的注释仍这么写）继续 42P01 | 债 | 出一条新迁移用**正典形状**重建该视图（历史迁移字节不可改）；本轮未做（见 §5.6.1 的口径冲突：视图的等级分桶与正典整数 `risk_level` 不兼容，必须同时定口径） |
| **L9** | **一个模型的 429 会冷却整个凭据**：`nodehealth` 熔断（付费档连续 3 次）⇒ `EffectSetCredentialUnavailable` ⇒ `credentials.availability_state='cooling'` + **该凭据名下全部绑定 `available=FALSE`** ⇒ 兄弟模型（哪怕刚 200 过）一起 `no_candidate` 3 分钟。写侧的 `writeModelLevelFailureOnly` 本来是对的（只冷自己），是**熔断路径**把范围放大 | 设计代价 | 本轮**不改**：改成模型级会重新打到同一个已超配额的端点上，更糟。若要改，需先答「凭据级限流 vs 模型级限流怎么区分」——上游 TPM/RPM 限流是**账号级**的，凭据级冷却在这类场景是对的；真正要收窄的是**被误判为模型故障的凭据级限流** |
| **L10** | **探针 chat 腿没有 Responses 腿那个「预算耗尽也算证据」的豁免**：reasoning 模型在探针预算（`ProbePingMaxTokens=32`）内把预算全花在 `reasoning_content` 上、`content:null` ⇒ chat 腿判 `probe_response_inconclusive` ⇒ 永远到不了 `healthy_confirmed` ⇒ 模型不可路由。实测命中 `z-ai/glm-5.3-flash`（凭据 2） | 规则不对称 | 本轮**不改**：「2xx 不等于健康、必须有正向证据」是本仓 2026-10-20 12h 审计轮**刻意**立的规矩（`bg/probe_observation_beat_gate_20261020_test.go` 整篇在钉）。要改必须单独一轮，并同时回答「reasoning 模型的探针预算该给多少」（32 ⇒ 多少）和「`finish_reason=length` + `content:null` 算不算协议可达证据」 |
| **L11** | **auto-route 成本聚合链从未接线**：`api_key_model_cost` 的填充 trigger `trg_update_api_key_model_cost` 在正典与**全部** startup 迁移里都不存在（只在归档 feature doc 里建过，且建在 v1 表上）；**真库全触发器清点确认它没挂在任何表上**（函数 `update_api_key_model_cost()` 存在但**无调用方**）⇒ 该表**0 行**、**无任何索引** ⇒ 原函数里的 `ON CONFLICT (bucket, api_key_id, raw_model)` 会报 42P10。⇒ `/api/admin/auto-route/cost/customer` 与 `/api/admin/auto-route/cost/model` **从功能设计出来那天起就一直返回 `[]`**，与 request_logs 退役**无关**。⚠ **证据口径订正**：本条原先用 `pg_stat_user_tables.n_tup_ins = 0` 当「一行都没写过」的证据，**该读数已被本轮的真库门污染**（PG 计入回滚事务的插入，门用 `defer tx.Rollback()` 造夹具 ⇒ 连跑 3 次门计数 13→16、行数恒 0）⇒ 证据已换成**「触发器在不在 + 现在几行」**（§5.12.2 订正框 / §5.12.3） | 新功能缺口（非退役残留） | 按依赖序复活：① 写侧补 `canonical_id`（会话族现 0/NULL）与 `cost_usd` 的计价来源 → ② `api_key_model_cost (bucket, api_key_id, raw_model)` UNIQUE 索引 → ③ 在 `session_turns_hot` 上挂会话族版聚合 trigger（v1 版的 `outbound_model`/`client_model` 列在会话族不存在，**不能直接复用原函数体**）。**读源切换已完成**（迁移 883）。本轮**不做**（属新功能 + 要动写热路径） |

### 6.1 既有失败的归因（**不是**本轮引入）

`go test ./admin/`（**带 `TEST_PG_URL`**，否则这些真库门会被跳过）在
**pristine worktree**（`git worktree add /tmp/llmgw-pristine2 HEAD`，HEAD = `8cec460b3`，
不含本轮任何改动）上复跑，**逐字相同**地失败：

| 失败 | 消息 | 归因 |
|---|---|---|
| `TestFirstTurnSamplesReturnsRealSamplesAfterChannelGap` | `super_admin got 0 samples (total 0), want 3` | **数据依赖**：本地库没有 first-turn 样本数据。**与本轮改动无关**（pristine 复现一致） |
| `TestFirstTurnSamplesSessionsTitleShapeRealDB` | `public.sessions.title present = true`（✓）+ `super_admin Total = 0, want 3` | 同上（title 断言**过**，失败的是样本数） |

⇒ 这两条**只有**在设了 `TEST_PG_URL` 时才跑；上一轮把它们记成「既有失败」时
**没设**该变量 ⇒ 实际是被**跳过**了。⇒ 判别动作：**「绿」要问是「过」还是「跳过」**；
`t.Skip` 的门在没配环境变量时永远绿。
⇒ 它们与 §5.6 的 5 个 500 同源：**本地 PG 落后于正典 schema/数据**
（`user_tags` 缺列这条已由 ensure 链补上；样本数据与 `prompt_injection` 的
315 形状属另一层，见 §5.6.1 与 L8）。

**第三条（本轮新出现，已修）**：`TestDataLifecycleTableSizesFieldsAreComplete`
在一次全量跑里红了、**同提交再跑就绿**、pristine worktree 也绿 ⇒ 判据自身的竞态，
**不是**本轮引入的产品缺陷。根因与修法见 **§5.7**（判据与端点读同一个
`pg_total_relation_size`，却跨两次读做精确相等，而首行大表正被网关后台 worker 写入）。

---

## 5.9 ★ L1 订正：「7 张分区不带 `_` 前缀」**不是**缺陷，是 860 的设计；真正的问题是**按名字枚举的口径**

### 5.9.1 上一轮写下的结论（**前提有误**）

上一轮在遗留表里记了 L1：「7 张 v1 分区表未带 `_` 待删标志（`pg_inherits` 显示父表
是 `_request_logs`）⇒ **新装与存量环境形状分叉**」，并建议「出一条迁移把存量分区改名」。

**本轮复核后：这条结论的前提不成立。** 决定性证据有三条：

| 证据 | 读数 | 出处 |
|---|---|---|
| 860 的**明确设计** | 「monthly partitions and archive children (`request_logs_2026_10`, `request_logs_archive_2026_08`, `request_logs_default`, …) —— they **inherit the marker through their renamed parents** and are dropped together with them; their index names likewise stay」 | `sql/migrations/startup/860_rename_request_logs_family_pending_delete.sql:18-21` |
| **正典基线 schema 造的就是无前缀分区名** | `CREATE TABLE public.request_logs_2026_07` / `request_logs_2026_08` + `ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_07 …`；860 随后只改**父表** | `deploy/sql/schemas/baseline/01-schema.sql:13002/13150/19569`（`sql/schema/01-schema.sql` 同） |
| **862 的 `_request_logs_2026_10` 只是合成夹具** | 那是 `migration_862_integration_test.go:54` 里的**夹具**建表语句；862 的**生产 SQL 本身是父表派生**的（`CREATE INDEX … ON public._request_logs (ts)`，不硬编码任何子分区名） | `sql/migrations/startup/862_partition_family_leading_ts_idx.sql:60`；全仓 `grep` 无任何生产 DDL 造 `_request_logs_YYYY_MM` |

⇒ **新装环境与存量环境产出的形状完全相同**（父表 `_request_logs` + 子表
`request_logs_2026_*`）⇒ 「形状分叉」不成立，两边**都**是这一形状。
⇒ 而且**改名不是正确的修法**：860 的函数体改写**刻意跳过**了动态前缀
`'request_logs_' || to_char(...)`（它的边界守卫明确写了「dynamic prefixes …
must not be touched」），所以只改存量分区名、**不改函数体**的话，一旦重新开启
v1 写路径，函数又会造出**无前缀**的新分区 ⇒ 分叉原地复活。
⇒ 正确动作是**把口径从「按名字」改成「按父表」**，而不是改名字。

### 5.9.2 但确实有一个**真缺陷**：按名字前缀枚举分区族的口径会漏

| 脚本 | 旧口径 | 漏掉的 |
|---|---|---|
| `scripts/ops/analyze-partition-stats.sh` | `relname ~ '^(credential_model_index\|model_probe_runs\|_request_logs)_(202[0-9]_[0-9]{2}\|hot)$'` | **5 张**：`request_logs_2026_07/08/10/11/default` |
| `scripts/pg-columnar-rotate.sh` | `child.relname ~ '^(_request_logs\|credential_model_index\|usage_ledger\|request_wal)_2026_(0[1-9]\|1[0-2])$'` | **4 张**：`request_logs_2026_07/08/10/11` |

⇒ 这两张表的**统计信息不被刷新**（`last_analyze` 停在旧值）、**列存轮转跳过它们**。
本机实测这 7 张分区合计 2,277,376 bytes、**0 行**（v1 族已冻结）⇒ 当前影响小，
但**是结构性的**：族一旦解冻或迁到别的环境，缺口立刻变成真问题。

### 5.9.3 修法与**差集证明**（真库实测）

两处都改成**按 `pg_inherits` 走父表**（父表名单 = 家族名；月份窗口仍按**子表自己的
后缀**判定，与旧口径等价）。仓库里已有这个写法的先例：
`deploy/sql/schemas/baseline/01-schema.sql:1436` 的
`c.relname ~ ('^' || parent_name || '_\d{4}_\d{2}$')`。

**旧口径 vs 新口径的差集（真库，2026-10-20）**：

| 脚本 | 新口径**补上** | 新口径**漏掉**（回退） |
|---|---|---|
| `analyze-partition-stats` | `request_logs_2026_07` / `_08` / `_10` / `_11` / `_default`（**5**） | **0**（旧口径的 8 张一张不少） |
| `pg-columnar-rotate` | `request_logs_2026_07` / `_08` / `_10` / `_11`（**4**） | **0**（旧口径的 10 张一张不少） |

**取数命令**（DSN 走 `DATABASE_URL`，不落盘）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# 旧口径 vs 新口径的差集（两个 WITH + 双向 EXCEPT 的等价写法）
"$PSQL" "$DATABASE_URL" -f /tmp/anacmp.sql   # analyze 口径
"$PSQL" "$DATABASE_URL" -f /tmp/rotcmp.sql   # rotate 口径
# 分区归属（证明父表是谁）
"$PSQL" "$DATABASE_URL" -tAc "SELECT c.relname, p.relname FROM pg_class c
  JOIN pg_inherits i ON i.inhrelid=c.oid JOIN pg_class p ON p.oid=i.inhparent
  WHERE p.relname ~ '(^|_)request_logs' ORDER BY 1;"
```

**口径**：按 `pg_inherits` 的父子关系枚举分区，家族以**父表名**标识。
**覆盖范围**：`public` schema，`_request_logs` / `_request_logs_bodies` /
`credential_model_index` / `model_probe_runs` / `usage_ledger` / `request_wal`
六个家族的分区子表 + 各自的 `_hot` 表。

### 5.9.3.1 ★ 这条修复的**生效范围**（别把「已提交」当「已生效」）

两个脚本是**仓库级运维资产**，**不在部署物里**：本机部署目录
`~/kaixuan/llm-gateway-go-v2/` 只有 `bin/ logs/ run/ raw-logs/ attachments/ backups/`，
**没有 `scripts/`** ⇒ 改脚本**不需要重新部署网关**（网关二进制里没有它们）。

但**生产机上的脚本副本是旧的**（实测 154）：

```
/opt/llm-gateway-go/build-2515/scripts/ops/analyze-partition-stats.sh   pg_inherits=0   （10-08）
/opt/llm-gateway-go/build-src/scripts/ops/analyze-partition-stats.sh    pg_inherits=0   （08-25）
```

⇒ 若运维**从那两份副本**直接执行，跑的仍是**旧口径**（会漏 ANALYZE / 列存轮转那 4~5 张）。
⇒ **好消息是当前没有排程影响**：245 / 154 / 252 三台机 × 5 类排程源
（用户 crontab、root crontab、`/etc/cron*`、systemd unit/timer、**其它所有用户的
crontab**）**全部零命中**这两个脚本 ⇒ 没有任何定时任务在跑它们。
⇒ **处置建议**：运维改从**仓库 checkout** 执行这两个脚本（或把 154 上那两份副本刷新到
当前 `origin/main`）。**本轮未刷新那两份副本**（它们不在发布链上，属另一会话/运维的资产，
且当前无排程消费者）。

```bash
# 核对「哪台机上有旧副本、有没有排程在跑」
for h in 245 154 252; do
  ssh "$h" 'for u in $(cut -d: -f1 /etc/passwd); do
      crontab -u $u -l 2>/dev/null | grep -q "analyze-partition-stats\|pg-columnar-rotate" && echo "$u"
    done
    grep -rli "analyze-partition-stats\|pg-columnar-rotate" /etc/cron* /etc/systemd/system 2>/dev/null
    for f in $(ls -d /opt/llm-gateway-go/*/scripts/ops/analyze-partition-stats.sh 2>/dev/null); do
      printf "%s pg_inherits=" "$f"; grep -c pg_inherits "$f"
    done'
done
```

### 5.9.4 顺带订正一条**基线 schema 不该动**的判断

`deploy/sql/schemas/baseline/01-schema.sql:201` 的分区名正则里用的是**无前缀**的
`request_logs` / `request_logs_bodies` —— 看着像漏了 `_`，其实**是对的**：
基线 schema 造出来的分区本来就叫 `request_logs_2026_07`（无前缀），
860 是在基线**之后**才改的父表名。⇒ **不动基线 schema**（它是历史快照）。

---

## 5.10 ★ 补做「模型请求与写入测试」步骤：5/5 成功并完整落库 + 之后全量 503 的完整归因

### 5.10.1 可用的模型集（用户给的 7 个里只有 1 个在本机目录中）

> ⚠️ **2026-10-20 补正**：本小节「本机**目录里根本没有这些族**」的说法**不准确**——
> `/v1/models` 的目录**随可用凭据变化**（凭据 1 挂掉后 89 → 77，正好等于凭据 2 的
> provider 2452 的模型数）；用户清单里的模型**其实都在 `provider_models` /
> `credential_model_bindings` 里**，只是**不可路由**。
> 逐个归属（含每个模型不可路由的**真实原因**）见 **§5.11.6**。

`GET /v1/models`（本机）返回 **89** 个模型。用户清单里的族在本机的实际可用性：

| 用户清单里的模型 | 本机结果 | 原因 |
|---|---|---|
| `deepseek-v4-flash` | **200** | 有健康凭据 |
| `glm-5.3-flash` | 503 | 目录里只有 `glm-5.2` / `glm-5.3`，没有 `glm-5.3-flash` |
| `minimax-m2.7` / `grok-4.6` / `claude-sonnet-5-5` / `gpt-5.6-luna` / `mimo-v2.6-flash` | 503 / 400 | 本机**目录里根本没有这些族**（`minimax`/`grok`/`claude`/`gpt-5`/`mimo` 命中数均为 0）；`mimo-v2.6-flash` 是 `400 invalid_model: Model 'mimo-v2.6-flash' is not supported by this gateway` |

⇒ 实际可跑：`deepseek-v4-flash`、`deepseek-flash`、`glm-5.2`（探针 200/200/200，
`glm-5.3` 与 `deepseek-v4.1-flash` 503）。

### 5.10.2 成功的 5 轮与**读回证据**

会话头 `X-Session-Id: gw_add2edb31d14a16e8d027a602a63dd2b`（**必须** `gw_` 前缀，
否则被静默丢弃 ⇒ 多轮退化成 N 次单轮）。

**读回（`session_turns_hot`；正文在 `session_bodies_hot`，晋级有延迟）**：

| turn_no | model | prompt_tokens | completion_tokens | success | latency_ms |
|---|---|---|---|---|---|
| 1 | deepseek-v4-flash | 84 | 26 | t | 1795 |
| 2 | deepseek-v4-flash | 86 | 19 | t | 1799 |
| 3 | deepseek-v4-flash | 98 | 528 | t | 7469 |
| 4 | deepseek-v4-flash | 114 | 1323 | t | 19964 |
| 5 | deepseek-v4-flash | 103 | 2211 | t | 28756 |

**正文是否落库**（量具纪律：用 `jsonb_typeof(col)='array'` 判，**不用** `IS NOT NULL`，
因为 JSON `null` 也满足 `IS NOT NULL`）：

| turn_no | `outbound_body` | `response_delta` | `request_delta` |
|---|---|---|---|
| 1–5（全部 5 行） | **array** | **array** | **array** |

⇒ 5 轮全部落库、轮次序号连续、正文三列**都是数组** ⇒ **「数据存储可用」成立**。

### 5.10.3 一个**量具**坑：每轮只发一条消息 ≠ 多轮

首版脚本每轮只发**一条** user 消息 ⇒ 网关看到的每轮都是独立请求，
`prompt_tokens` 只反映**本轮提示词长度**。读数 `r4=114 > r5=103` 看着像「不单调」，
其实是因为 r5 的中文提示词比 r4 **短** —— 那是**提示词长度**，不是多轮上下文。
⇒ 已改成**累积对话**（每轮带上全部历史 user/assistant），这才是「多轮」的可验证形态
（`prompt_tokens` 必然单调增长）。

### 5.10.4 08:02 起全量 503 的**完整归因**（三个独立环境原因，**都不是仓库缺陷**）

| # | 现象 | 根因（读数） | 性质 |
|---|---|---|---|
| 1 | `no_candidate`（`candidates_count:0`） | 凭据 1 `availability_state='auth_failed'`（`state_updated_at=08:02:36`，`availability_recover_at=08:17:36`）⇒ 被 `autoroute/index.go` 的 `refreshIndexSQL` 里 `COALESCE(cr.availability_state,'ready')='ready'` 谓词滤掉 | **环境/上游**：凭据被判 auth 失败 |
| 2 | `upstream_down`：`provider requires egress proxy but none is available: no available active nodes` | `proxy_nodes` **0 行**、`proxy_subscriptions` **0 行** ⇒ 网关自己的出网代理节点库是空的 | **环境**：代理节点库没配 |
| 3 | 429 `budget_exceeded` | 凭据 3 `quota_state='permanently_exhausted'`、`health_error='responses status 429: {"error":{"message":"Organization balance insufficient","code":"budget_exceeded"}}'`、`lifecycle_status='suspended'` | **上游账单**：账户余额耗尽 |

⇒ 网关的行为是**正确**的：摘除失败候选、给出明确原因（`no_candidate` /
`upstream_down` / `429 budget_exceeded`），而不是把请求路由到已知坏的凭据。
⇒ 判定「这不是产品缺陷」的两条独立证据：
① 同一份二进制在 08:00:15–08:01:15 连续 5 次 200 且完整落库；
② 刷新作业本身**健康**（`auto index refreshed` 每 5 分钟一次，最新桶
`2026-10-20T00:05:00Z`、`credential_rows=7`），`credential_model_index_hot` 里
`deepseek-v4-flash|1` 在位 ⇒ 计划数据在库里，缺的是**凭据与代理节点**。

**取数命令**：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# 三个凭据的当前可用性
"$PSQL" "$DATABASE_URL" -tAc "SELECT id, provider_id, availability_state, health_status,
   quota_state, LEFT(COALESCE(health_error,''),60), availability_recover_at
   FROM public.credentials ORDER BY id;"
# 代理节点库
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.proxy_nodes;"
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.proxy_subscriptions;"
# 候选计划（生产 SQL，逐字）
# autoroute/index.go 的 refreshIndexSQL
```

### 5.10.5 顺带确认的一条**读侧事实**：`session_turns` 不是写入落点

写侧落在 **`session_turns_hot`**（本轮 15 行新数据全在 hot 表），晋级到
`session_turns` 有延迟 ⇒ **用 `session_turns` 直接查「刚发的请求」会查不到**，
必须查 hot 表或按时间窗查父表。本机读数：请求前 `session_turns=876` /
`session_turns_hot=174` / `session_bodies_hot=174` / `sessions=989`；
本轮 15 次请求后 hot 表新增 15 行（`turn_no` 1..5 × 3 会话）。

---

## 5.11 ★ 补做「累积式多轮」测试：`prompt_tokens` 84→1255 单调增长，退役族零新增

### 5.11.1 为什么要重做：§5.10.2 那 5 轮**不是多轮**

§5.10.3 已记：每轮只发**一条** user 消息 ⇒ 网关看到的是 5 个独立请求，
`prompt_tokens` 只反映**本轮提示词长度**（读数 84/86/98/114/103，r4 > r5
是因为 r5 的中文提示词更短，**不是**「不单调」）。

本节用**累积对话**（每轮带上全部历史 user/assistant，与真实聊天客户端一致）
重做，唯一目的是让「多轮」变成**可验证的形态**：`prompt_tokens` 必须单调不减。

### 5.11.2 两条**量具**坑（现象与「产品缺陷」同形，必须先排除量具）

**坑 1：BSD `sed` 不认 LHS 里的 `\n` ⇒ 标记没剥掉 ⇒ 请求体被清空。**

脚本用 `sed 's/\n__HTTP__[0-9]*__T[0-9.]*$//'` 剥 `curl -w` 追加的标记。
macOS 的 **BSD sed 不把 LHS 的 `\n` 当换行**（GNU sed 才当）⇒ 标记留在 payload 里
⇒ `json.loads` 报 `Extra data` ⇒ **history 被悄悄置空** ⇒ 下一轮请求体变成空串
⇒ 服务端回 `400 sanitization_failed: Invalid request body`。

服务端日志（这一条是**服务端自己**说的，但**根因在量具**）：

```
sanitize_middleware: sanitize failed
  error="invalid sanitizable request: JSON object required: unexpected end of JSON input"
```

本地对照（同一份输入，两种剥法）：

```
old=[{"a":1}
__HTTP__200__T1.5]        ← BSD sed：标记没剥掉
new=[{"a":1}]              ← python rfind：剥掉了
```

⇒ 修法：改用 python 按 `rfind("\n__HTTP__")` 剥；并让 **history 更新失败时显式中止**，
不再让空 history 继续跑（空 history ⇒ 请求体退化 ⇒ 现象与服务端 bug 同形）。
⇒ **判别动作：量具的失败可以伪装成服务端的失败。** 看到「服务端说请求体非法」，
先回头验量具的**输出**（本例：落盘的 payload 里还带着 `__HTTP__` 标记）。

**坑 2：探活一次碰多个模型 ⇒ 一个模型的 429 会把整个凭据冷却 ⇒ 后面的模型全部 `no_candidate`。**

详见 §5.11.4。⇒ **判别动作：探活一次只碰一个模型**，否则会把「凭据被冷却」
误读成「这个族没有可用模型」。

### 5.11.3 累积式多轮：**4 条判据全 PASS**

会话头 `X-Session-Id: gw_927eebbc5a24f59b580dd1fb0d678925`（`gw_` 前缀必需）。

**客户端读数**（每轮都带全部历史）：

| round | http | 耗时 | prompt_tokens | completion_tokens |
|---|---|---|---|---|
| 1 | 200 | 1.43s | 84 | 38 |
| 2 | 200 | 1.65s | 92 | 41 |
| 3 | 200 | 7.32s | 107 | 717 |
| 4 | 200 | 16.46s | 386 | 1352 |
| 5 | 200 | 25.13s | **1255** | 2158 |

**读回判据**（独立脚本 `verify-model-writeback.sh`，只读库、不发请求，与发送侧互不污染）：

| 判据 | 结果 |
|---|---|
| **A. 轮次连续** | `turn_no` = 1..5 连续，**5 行**，`success=t` 全绿，`latency_ms` 1247/1443/7123/16226/24951 |
| **B. 多轮生效** | `prompt_tokens` = 84 → 92 → 107 → 386 → **1255**，**严格单调递增** ⇒ 累积对话确实生效 |
| **C. 正文落库** | `session_bodies_hot` 5 行，`outbound_body` / `response_delta` / `request_delta` 的 `jsonb_typeof` **全是 `array`**（口径：用 `jsonb_typeof='array'` 判，**不用** `IS NOT NULL`） |
| **D. 退役族零新增** | `_request_logs` / `_request_logs_bodies` / `_request_logs_archive` / `_request_logs_hot` / `_request_logs_bodies_hot` **全部 0 行**；同族对照 `session_turns_hot=306` / `session_bodies_hot=306` / `session_turns=876` |

⇒ **A+B 证明「多轮是真的」；C 证明「正文落库」；D 证明「退役族没被复活」。**
⇒ 与 §5.10.2 的 5 轮合起来：**非累积 5/5 + 累积 5/5，共 10 轮全 200 且完整落库。**

**取数命令**（口径与覆盖范围一并落账）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
SID=gw_927eebbc5a24f59b580dd1fb0d678925

# A/B：轮次连续 + prompt_tokens 单调不减（写侧落在 hot 表）
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT turn_no, model, prompt_tokens, completion_tokens, success, latency_ms
  FROM public.session_turns_hot WHERE session_id='$SID' ORDER BY turn_no;"

# C：正文是 JSON 数组（**不用** IS NOT NULL —— JSON null 也满足）
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT turn_no, jsonb_typeof(outbound_body), jsonb_typeof(response_delta),
         jsonb_typeof(request_delta)
  FROM public.session_bodies_hot WHERE session_id='$SID' ORDER BY turn_no;"

# D：退役族零新增（覆盖父表 + hot + bodies + archive 四张）
for t in _request_logs _request_logs_bodies _request_logs_archive \
         _request_logs_hot _request_logs_bodies_hot; do
  echo -n "$t: "; "$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.$t;"
done
```

**覆盖范围说明**：本节只覆盖**本机**（`127.0.0.1:8782` + 本机 PG 17.10）、
**凭据 1（provider 2453，sensenova）**、**模型 `deepseek-v4-flash`**。
其它凭据/模型的可用性见 §5.11.6；本节**不**声称其它环境已验证。

### 5.11.4 新发现一：一次 429 会把**整个凭据**冷却 3 分钟，兄弟模型一起连坐

**实测链**（2026-10-20 08:20:45 → 08:24:44）：

1. 08:20:45 探 `deepseek-v4-flash`（我自己的探针带了 `max_tokens:16`）
   → 上游 `token.sensenova.cn` 返回 **200 但内容为空**（reasoning 模型被 16 token 截断）
   → 网关判 `empty_response` ⇒ failover。**该错误不计入连续失败数**
   （`nodehealth/reducer.go` 的 `ErrorKindEmptyResponse` 直接 `break`）。
2. 08:20:54 / 08:20:59 / 08:21:10 探 `deepseek-flash` → 上游 **429
   `RateLimitExceeded.EndpointTPMExceeded`**（"inference exceeds tpm/rpm limit"）
   ⇒ goal-retry 对**同一节点**连打 3 次。
3. 3 次后 `nodehealth` 的 `ConsecutiveFailures`（NodeKey = 租户+provider+**凭据+模型**）
   达到 `circuitFailureThreshold(billing_mode)` = **3**（付费档；免费档 6）
   ⇒ `effectsFor` 触发 `EffectRecordCircuitFailure` + `EffectSetBindingUnavailable`
   + **`EffectSetCredentialUnavailable`**。
4. `EffectSetCredentialUnavailable` → `credential.Writer.SetCredentialUnavailable`
   ⇒ `credentials.availability_state='cooling'`（`availability_recover_at` = +3min）
   **且把该凭据名下全部 10 个绑定置 `available=FALSE`**。
5. ⇒ **刚刚才 200 过的 `deepseek-v4-flash` 立刻变成 `no_candidate`**，直到 08:24:44
   恢复 ticker 把它翻回 `ready`。

**读回证据**（冷却窗口内，cred1 的 10 个绑定）：

```
f|auto_credential_rate_limit|SenseChat-5
f|auto_credential_rate_limit|SenseChat-5-Thinking
f|auto_credential_rate_limit|SenseChat-Turbo
f|auto_rate_limit|deepseek-flash              ← 模型级（writeModelLevelFailureOnly 做的，对）
f|auto_credential_rate_limit|deepseek-v4-flash ← 凭据级（连坐）
f|auto_credential_rate_limit|deepseek-v4.1-flash
f|auto_credential_rate_limit|glm-5.2
f|auto_credential_rate_limit|kimi-k3
f|auto_credential_rate_limit|sensenova-6.8-flash-lite
f|auto_credential_rate_limit|sensenova-u1.5-lite
```

⇒ 注意两条路径**同时**发生：写侧 `WriteOnError` 对 `KindConcurrent` 走
`writeModelLevelFailureOnly`（**只**冷 `deepseek-flash` 自己的绑定，**不碰**
`credentials.availability_state`，`writer.go:518` 明写「✅ DO NOT update
credentials.availability_state」）；而 `nodehealth` 的熔断路径
（`EffectSetCredentialUnavailable`）把范围**放大到整个凭据**。
⇒ **归属**：熔断「连打 N 次 ⇒ 整个凭据不可用」是 `reducer.go:434` 的**明确设计**
（注释：凭据级熔断 + 免费档阈值 6），不是本轮引入的回归；**代价**是
「一个模型的限流会摘掉该凭据名下全部模型」。记为遗留 **L9**，本轮不改
（改成模型级会重新打到同一个已超配额的端点上，反而更糟）。
⇒ **判别动作**：看到「某个模型族全部 `no_candidate`」时，**先查凭据级状态**
（`credentials.availability_state`），再查模型级绑定；两者原因完全不同。

### 5.11.5 新发现二：探针的 chat 腿**没有** Responses 腿那个「预算耗尽也算证据」的豁免

- **Responses 腿有豁免**：`bg/probe_http_validation.go` 的
  `responsesProbeProtocolEvidence` 接受
  `status=incomplete` + `incomplete_details.reason=max_output_tokens` + 非空 `output`
  ⇒ 判为**有正向证据**（注释：「A complete Responses token-budget terminal proves
  the former even if all of its output budget went into reasoning」）。
- **chat 腿没有**：`bg/credential_probe_response_validation.go:147-152` 要求
  `choices[].message.content` 是**非空字符串**（或含文本的 content 数组 / 带名字的 tool_calls）。
  `content: null` + 只有 `reasoning_content` ⇒ **没有证据** ⇒ `probe_response_inconclusive`。
- **实测**（凭据 2 / provider 2452 / NVIDIA NIM）：`z-ai/glm-5.3-flash` 探针
  连续多轮拿到 **HTTP 200**，正文是
  `{"message":{"content":null,"reasoning_content":"The user just said \"ping\"..."}}`、
  `finish_reason:"length"` ⇒ 判 `probe_response_inconclusive`、attempt 递增、
  一直退避 ⇒ 该模型**永远到不了 `healthy_confirmed`** ⇒ 不可路由。
- **探针的 token 预算是 32**（`providercap.ProbePingMaxTokens = 32`，注释里明写
  「a reasoning model can burn the entire budget invisibly」）⇒ 对 reasoning 模型
  而言 32 token 常常**只够写 reasoning**。
- ⇒ 这是**规则不对称**（同一件事，Responses 腿放行、chat 腿定罪），但
  「2xx 不等于健康、必须有正向证据」是本仓 2026-10-20 12h 审计轮**刻意**立的规矩
  （`bg/probe_observation_beat_gate_20261020_test.go` 整篇在钉它）⇒
  **本轮不改**，记为遗留 **L10**（要改需要单独一轮，并且必须同时回答
  「reasoning 模型探针该给多少预算」）。

### 5.11.6 订正 §5.10.1 的说法：用户清单 7 个模型在本机的**逐个**归属

§5.10.1 当时写的是「本机**目录里根本没有这些族**」。这句话**不准确**：
`/v1/models` 的目录是**随可用凭据变化**的（凭据 1 挂掉后目录从 89 个变成
**77 个**，正好等于凭据 2 的 provider 2452 的模型数），而**用户清单里的模型
其实都在 `provider_models` / `credential_model_bindings` 里**——只是**不可路由**。
逐个归属（真库读数）：

| 用户清单模型 | `provider_models` | 凭据 | 绑定 | **不可路由的真实原因** | 性质 |
|---|---|---|---|---|---|
| `deepseek-v4-flash` | id=1（provider 2453） | 1 | available | 凭据 1 曾 `auth_failed`（08:02:36→08:17:36，08:18:05 恢复）⇒ 期间 `no_candidate` | **环境**（已恢复） |
| `glm-5.3-flash` | id=3 `z-ai/glm-5.3-flash`（2452） | 2 | available | 探针 200 但 `content:null` ⇒ `probe_response_inconclusive` ⇒ 永远退避（§5.11.5） | **仓库规则**（L10） |
| `minimax-m2.7` | id=2 `minimaxai/minimax-m2.7`（2452） | 2 | available | **上游 410 Gone**：`The model 'minimaxai/minimax-m2.7' has reached its end of life on 2026-07-27T00:00:00Z and is no longer available` | **上游退役**（不可修） |
| `grok-4.6` | id=5（provider 2454） | 3 | available | 凭据 3 `quota_state='permanently_exhausted'`（429 `Organization balance insufficient`） | **上游账单**（不可修） |
| `claude-sonnet-5-5` | id=6（2454） | 3 | available | 同上 | **上游账单**（不可修） |
| `gpt-5.6-luna` | id=4（2454） | 3 | available | 同上 | **上游账单**（不可修） |
| `mimo-v2.6-flash` | **无行** | — | — | 本机从未收录该模型（`provider_models` 里没有任何 `mimo` 记录） | **环境**（未配置） |

补充读数：凭据 2 的探针失败分布（`node_probe_runs` 全量）——`http_404` **266** 次
（NVIDIA 侧 `Function 'c322f327-…' Not found for account` ⇒ **该账号没部署这些模型**）、
`timeout` 206、`http_500` 138、`http_410` 96（EOL）、`probe_response_inconclusive` **10**、
`http_400` 17、`http_429` 6、`network_error` 3、`http_502` 2
⇒ **凭据 2 不可用的主因是「账号没部署这些模型」，不是探针规则**；
探针规则只影响其中 10 次。

**取数命令**：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# 模型 → provider_models → 凭据 → 绑定可用性
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT pm.id, pm.provider_id, pm.raw_model_name, pm.available, c.id
  FROM provider_models pm JOIN credentials c ON c.provider_id=pm.provider_id
  WHERE pm.raw_model_name ILIKE '%deepseek-v4-flash%' OR pm.raw_model_name ILIKE '%glm-5.3-flash%'
     OR pm.raw_model_name ILIKE '%minimax%' OR pm.raw_model_name ILIKE '%grok-4.6%'
     OR pm.raw_model_name ILIKE '%claude-sonnet-5-5%' OR pm.raw_model_name ILIKE '%gpt-5.6-luna%'
     OR pm.raw_model_name ILIKE '%mimo%';"
# 探针失败分布（按 err_code 计数）
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT direct_err_code, count(*) FROM node_probe_runs WHERE credential_id=2 GROUP BY 1 ORDER BY 2 DESC;"
# 上游 410 的原文
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT id, raw_model_name, direct_http_status, left(direct_err_detail,160)
  FROM node_probe_runs WHERE credential_id=2 AND raw_model_name='minimaxai/minimax-m2.7'
  ORDER BY id DESC LIMIT 1;"
```

---

## 5.12 ★ 续做：从遗留表里挑出「本轮真能收口」的两项（读源 D1 / 扫描次数 D3）

### 5.12.1 怎么挑的：只动**有可观测效果**且**在目标内**的两项

上一轮遗留表里有 6 项（L1/L2/L3/L4/L5/L6/L7/L8）。本轮按三条标准筛：

| 候选 | 在目标内？ | 有可观测效果？ | 本轮处置 |
|---|---|---|---|
| **L2**（两个活指标读冻结族） | ✅「API 读源转到 session 族」 | ✅（恒 0 / 恒 NULL 是错的） | **做**（§5.12.2） |
| **L3**（列表端点同一行集扫两遍） | ✅「优化列表效率，不要表扫描」 | ✅（执行次数 2→1） | **做**（§5.12.4） |
| L4（5 张 `v_*` 视图仍读 v1 裸母表） | ✅ 但**全是历史只读面** | ❌ 活指标不依赖它们 | 留待（L4） |
| L5（`admin_logs_native_turns_read` 默认关闭） | ⚠️ 是**开关决策**不是缺陷 | — | 留待（L5） |
| L7 / L8 | ✅ 但改动面在共享中间件 / 需要新迁移定口径 | — | 留待 |

### 5.12.2 D1：两个**活指标**读的是**冻结族** ⇒ 一个恒 0、一个恒 NULL（且无任何错误信号）

**缺陷本体**（真库读数，非推理）：

| # | 指标 | 窗口 | 原读源 | 后果 |
|---|---|---|---|---|
| 1 | `customer_cost_view.active_concurrent` | **5 分钟** | `_request_logs` | **恒 0**（任何环境都恒 0，见下） |
| 2 | `model_cost_per_task_view.avg_latency_ms` | **7 天** | `_request_logs` | 只能读到**停写那一刻**的快照值（7 天后为 NULL） |

三个决定性读数：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# ① v1 族在退役后是空的（本地）
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public._request_logs;"        # 0
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public._request_logs_hot;"    # 0
# ② 它**从未**被写入过 —— ⚠ 用「触发器 + 行数」判定，**不要**用 n_tup_ins（见下方订正）
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public._request_logs;"        # 0
"$PSQL" "$DATABASE_URL" -tAc "SELECT t.tgname, c.relname FROM pg_trigger t
  JOIN pg_class c ON c.oid=t.tgrelid WHERE NOT t.tgisinternal
    AND c.relname IN ('_request_logs','_request_logs_hot');"                     # 0 行
# ③ 端点现状
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:8782/api/admin/auto-route/cost/customer                      # []
```

> **⚠ 订正（本轮实测发现的一条「判据污染度量」）**：这条证据最初写的是
> `pg_stat_user_tables.n_tup_ins` = 0（当时确实读数 0，因为**门还没写**）。
> 但本轮把真库门加上之后，同一个读数变成了 **13**，而表里**仍然是 0 行**、
> `n_tup_del=0` ⇒ 看起来像「表被写过了、又被删了」。
> ⇒ 根因：**PG 的 `n_tup_ins` 计入「回滚事务里的插入」**（本轮受控实验实测：
> `BEGIN; INSERT; ROLLBACK;` 之后 `n_tup_ins` 确实 +1），而本轮的真库门
> `admin/auto_route_cost_views_session_source_test.go` 正是
> `defer tx.Rollback()` 的**夹具插入**（`api_key_model_cost` 那行）⇒
> **门把自己的度量污染了**。
> ⇒ 决定性复现：连跑该门 3 次，`n_tup_ins` 13 → 16（**恰好 +3**），
> 表行数**始终 0** ⇒ 那 13 次插入**全部是门自己的回滚夹具**，
> 该表**从未被生产流量写过**。
> ⇒ **判别动作：要证明「某表没被写过」，用「触发器在不在 + 现在几行」，
> 不要用 `n_tup_ins`**——后者会被任何跑过夹具的测试污染，且
> 「回滚也算」这个语义不看文档根本想不到。

**为什么「恒 0」是普遍结论而不是「本机没数据」**：`active_concurrent` 的窗口是
**5 分钟**，而 v1 族自 2026-10-13 停写 ⇒ **冻结族里永远没有「最近 5 分钟」的行**
⇒ 这个计数在**任何环境**都恒 0（哪怕那台环境有三年历史数据）。它衡量的是
「此刻有多少个 auto 请求在跑」，恒 0 ⇒ 并发压力看板与由它派生的
`pressure_ratio` / `avg_pressure_1h` 全部失去意义，**且不报任何错**。

**语义映射**（逐列核对，不是机械替换）：

| v1 列 | 会话族对应列 | 依据 |
|---|---|---|
| `outbound_model` | **`raw_model_name`** | `domains/streaming/executors/executor.go` 的 `candidateRawModel(c)` ⇒ 候选的**上游原始模型**；`session_turns.model` 是**客户端**模型，语义不同 |
| `api_key_id` | `api_key_id` | ⚠ **类型不同**：会话族是 **text**（写侧 `APIKeyID string`，落库是十进制文本 `'2'`），`api_key_model_cost.api_key_id` 是 **integer** ⇒ 必须显式 `::text`，否则建视图时报 `operator does not exist: text = integer`（本轮真实踩到） |
| `is_auto_request` / `success` / `ts` / `latency_ms` | 同名列 | 语义一致 |

**两条腿的选型**（各有依据，不是随手挑）：

- `active_concurrent`（5 分钟）= **hot 单腿** `session_turns_hot`
  · **完整**：在线写路径只写 hot（`domains/session/v2/turn_writer.go`），
    晋级延迟远大于 5 分钟 ⇒ 窗口内的行必然在 hot；
  · **无重复**：计数指标不能被数两次（下面 ② 的 UNION ALL 才有那个问题）；
  · **便宜**：本仓先例 —— `db/request_logs_view_schema.go` 的
    `SessionFamilyTurnsHotSourceSQL` 明写「短窗仪表盘用 hot-only，免得父表
    分区扫描与并发 MV REFRESH 把 `/api/logs/top-models` 顶爆 10s 预算
    （2026-10-15 生产 500 实证）」。
- `avg_latency_ms`（7 天）= **hot ∪ parent 两条腿 UNION ALL**
  · 7 天窗跨过 hot 保留期（名义 8h）⇒ 只带 hot 会**整段丢失** 7 天前到 8 小时前；
  · 形态与 `SessionFamilyTurnsSourceSQL` 同款；promote 会排空 hot ⇒ 不会重复；
  · 且它是**均值**，对偶发重复不敏感（计数指标对重复敏感，所以那条债是另一回事）。
  · **不能**复用现成的 `session_turns_with_current_month`：它不投影
    `raw_model_name`（实测 information_schema），而本子查询需要它。

**改法与登记**（迁移 883）：

| 落点 | 内容 |
|---|---|
| `sql/migrations/startup/883_session_family_cost_view_metrics.sql` (+`.down.sql`) | 两条 `CREATE OR REPLACE VIEW`（列名/列序/列类型不变 ⇒ 可直接替换）+ `lock_timeout=60s` + `statement_timeout=5min` + `POST_CONDITION` |
| `sql/objects/views/{customer_cost_view,model_cost_per_task_view}.sql` | 正典定义体同步 |
| `sql/schema/01-schema.sql` **与** `deploy/sql/schemas/baseline/01-schema.sql` | 两份**逐字节相同**（`deploy/sql/verify-migration.sh` 会 diff 它们）⇒ 新装环境与存量环境形状一致 |
| `installer/cmd/llm-gw-installer/main.go` | `go:embed` + startup map 条目 |
| `installer/cmd/llm-gw-installer/embeddata/startup/883_*.sql` | embeddata 副本（与 canonical **SHA 现场一致**：`84a614746d06…`） |
| `installer/internal/dbinit/runner.go` | `StartupFiles` 条目 |
| `scripts/apply-db-revision-sequence.sh` | 部署通道 `files=()` 条目 |
| `sql/schema/installed_startup_migrations.tsv` | **用生成器重建**（`go test -run TestStartupManifest -update`）⇒ 883 @ 位置 278（序号不写死） |

**判据（2 道，都做过变异验证）**：

| 门 | 钉什么 | 变异 → 转红 |
|---|---|---|
| `admin/auto_route_cost_views_session_source_test.go`（**真库 + 造夹具**） | 两个指标真的取到**会话族**的值：造 `api_key_model_cost` 行 + 3 条会话行，断言 `active_concurrent == 1`、`avg_latency_ms == 夹具延迟` | **M1** 改回读 v1 ⇒ `active_concurrent` 变 0；**M2** 把匹配键换成 `model` ⇒ `avg_latency_ms` 变 NULL |
| `sql/migrations/startup/migration_883_..._test.go`（契约门，5 道） | 迁移体不读 v1 + 两条腿的选型 + `::text` + down 可回滚 + canonical/baseline 同步 + embeddata 一致 + **三处登记都在** | **M3** 把 883 从部署通道摘掉 ⇒ 登记门红；**M4** 去掉 `::text` ⇒ 形态门红 |

⇒ 门里还配了**前提断言**（`public._request_logs` 当前确实是空的 ⇒「指标非零」
只可能由会话族贡献）与**阴性侧断言**（非 auto 行、`success IS NULL` 行都**不得**
被计入 ⇒ 防止「为了让门过，把谓词删掉」）。

**⚠ 端点仍然返回 `[]` —— 这不是本轮没修好，是另一件事**（见 §5.12.3）。

### 5.12.3 D2（新遗留 **L11**）：成本聚合链**从未接线**（不是退役造成的）

`api_key_model_cost` 是两张视图的**外层**聚合表。它的填充依赖 trigger
`trg_update_api_key_model_cost`，而：

- 全仓搜 `CREATE TRIGGER ... api_key_model_cost` ⇒ **只在一份归档 feature doc
  里**（`deploy/sql/docs/features/2026-06-15-auto-route-mode-cost-table.sql`，
  建在 `request_logs` 上）⇒ **正典与全部 startup 迁移里都没有它**；
- **真库清点（本轮实测，2026-10-20 部署后）**：
  `SELECT t.tgname, c.relname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
   WHERE NOT t.tgisinternal;`
  ⇒ 全库非内部触发器里**没有** `trg_update_api_key_model_cost`，
  `api_key_model_cost` 身上**也没有**任何触发器；
  退役族上只剩两个**别的**触发器（`trg_session_tenant_binding` 挂
  `_request_logs`、`trg_update_session_summary` 挂 `_request_logs_hot`）。
  ⇒ 函数 `update_api_key_model_cost()` **确实存在于库里**（归档 feature doc 建过），
  但**没有任何触发器调用它** ⇒ 它**一次都没跑过**。
- `SELECT count(*) FROM public.api_key_model_cost;` = **0 行**
  （⚠ **不要**用 `n_tup_ins` 当「有没有被写过」的证据——**本轮的门自己会污染它**，
  见 §5.12.2 的订正框：PG 计入回滚事务的插入，夹具 `defer tx.Rollback()` 会让
  计数只涨不落，连跑 3 次门实测 13→16、行数恒 0）；
- `api_key_model_cost` **一个索引都没有** ⇒ 原函数里的
  `ON CONFLICT (bucket, api_key_id, raw_model)` 会直接报
  `42P10: there is no unique or exclusion constraint matching the ON CONFLICT specification`
  ⇒ 就算把 trigger 挂回去，**第一行就会失败**。

⇒ **结论：这两个成本端点从功能设计出来那天起就一直是 `[]`**，与 request_logs
退役**无关**（不是退役造成的回归）。「端点空」和「读源错」是**两件事**：
本轮修的是后者（读源），前者是**新功能缺口**。

**复活它的前置顺序**（缺一不可，按依赖排）：

1. **写侧**：会话族的 `canonical_id` 实测 **0/NULL**（`session_turns` 与
   `_hot` 都是）⇒ `model_cost_per_task_view` 的分组键会一直是 NULL；
   `cost_usd` 虽有值但本机全为 0（本地凭据无计价）⇒ 成本列要真实，得先确认
   写侧在有计价的环境里会落非零值。
2. **唯一索引**：`api_key_model_cost (bucket, api_key_id, raw_model)` UNIQUE
   （否则 `ON CONFLICT` 无法工作）。
3. **会话族 trigger**：在 `session_turns_hot` 上挂会话族版聚合函数
   （会话族的列名是 `model`/`raw_model_name`，v1 版的 `outbound_model`/
   `client_model` 不存在 ⇒ 不能直接复用原函数体）。
4. **读源切换**：**本轮已完成**（迁移 883）。

⇒ 本轮**不做** 1–3：那是**新功能**（要动写侧 + 新增唯一索引 + 挂在写热路径上的
trigger），不在「退役双写」的目标范围内，且会显著放大本轮的风险面。
⇒ **判别动作**：看到「某个端点返回空」时，**先问「它外层的聚合表有没有在增长」**
（`pg_stat_user_tables.n_tup_ins`），再问「读源对不对」——两者独立，混在一起
就会把「新功能缺口」误当成「退役残留」。

### 5.12.4 D3：列表端点对同一行集扫**两遍** → 扫一遍

上一轮把「列表端点三扫」记成 L3，理由是「**在本地小数据集上无法证明收益**」。
本轮换了量法：**量执行次数，不量墙钟/buffers**。

**为什么旧量法量不出来**（真库 EXPLAIN ANALYZE, BUFFERS，24h 窗，三种写法）：

| 写法 | Buffers | Execution |
|---|---|---|
| A：旧 COUNT | shared hit=137 | 0.30ms |
| B：旧 SUM | shared hit=137 | 0.30ms |
| C：合并后 | shared hit=137 | 0.30ms |

⇒ 本地只有 ~1.2k 行，**第二趟全命中缓存** ⇒ buffers 完全相同、**量不出差别**。
⇒ 但**执行次数**是**结构性**、与数据量无关的事实：同一行集被独立扫了两次。

**改法**（`admin/logs.go` 的 `listLogs`）：COUNT 与各 SUM 合并成**一条**聚合
（`SELECT COUNT(*), COALESCE(SUM(...)), ... FROM <同一 FROM/WHERE>`），
执行次数 2 → 1。

**失败语义必须保持**（旧行为是「SUM 失败 ⇒ 聚合字段回零、页照常出；
COUNT 失败才 500」）：

- 合并查询失败 ⇒ 退回**单独**的 COUNT 查询（count 与列表仍然可用，聚合字段保持零值）；
- COUNT 也失败 ⇒ 500（与旧行为一致）。

**口径不变**：COUNT 与 SUM 用的是同一 FROM/WHERE 与同一组参数
（tenant_admin 路径两者都带 `api_keys` 的 LEFT JOIN；super_admin 路径两者都不带）
⇒ 合并查询里的 `COUNT(*)` 与旧 COUNT 查询**逐字等价**。

**判据**（`admin/logs_single_scan_test.go`，真库 + **pgx QueryTracer** 数查询）：

- 判据 ①：统计「读日志源 + 带聚合 + **无 GROUP BY**」的查询条数 ⇒ 必须 **1**
  （旧写法是 2；`by_model` 的 GROUP BY 不计入）；
- 判据 ②：响应的 `count` 与各 SUM 与**旧两次扫描的参考口径**（测试自己用同一源、
  同一对 from/to 重算）**逐值相等**。
- 变异验证：把 COUNT 拆回独立查询 ⇒ 判据 ① 转红（报 2 次扫描并打印录到的 SQL）；
  恢复后转绿。

**活体读回**（部署后）：`/api/logs?page=1&page_size=3` ⇒ 200，
`count=324`、`aggregate.total_requests=324`、`prompt_tokens=13109`、`by_model` 20 项。

### 5.12.5 本轮新增的一条判据纪律

**夹具里被比较的两个键必须「故意不同」，否则门分不出键漂移。**
第一版真库门把会话行的 `model` 与 `raw_model_name` 设成同一个值 ⇒ 变异 M2
（把匹配键从 `raw_model_name` 换成 `model`）**没让门变红** ⇒ 门是「看着在测、
其实测不出来」的那种。改成两个不同的字符串后，同一个变异立刻转红。
⇒ **判别动作：写夹具时问一句「我要防的那个漂移，会让这两个值不同吗」**；
若不会，夹具就钉不住它。

### 5.12.6 部署后读回（`2.5.8.2757`，`VERIFY_PASS=1`）

**版本身份**（部署脚本自己验的，不是我事后看目录猜的）：

```
[verify] 8782:…/bin/2.5.8.2757: ok (version=2.5.8-ddc2f336-20261020-2757 build_seq=2757)
VERIFY_PASS=1   VERIFY_RELEASE=2.5.8.2757   VERIFY_PORT=8782
```

⇒ 运行中的二进制身份 = `ddc2f336` = 本轮代码提交 `ddc2f3362`（**sha 对得上**，
不是「当前 release 目录里有没有」那种弱证据）。

**D3 的活体读回**（列表端点，合并聚合之后）：

| 读数 | 部署前（老二进制 + 新视图） | 部署后（新二进制） | 判定 |
|---|---|---|---|
| HTTP / 耗时 | 200 | **200 / 82ms** | ✅ |
| `count` | 324 | **324** | ✅ 与旧 COUNT 逐值一致 |
| `aggregate.total_requests` | 324 | **324** | ✅ 与 `count` 自洽 |
| `aggregate.prompt_tokens` | 13109 | **13109** | ✅ 逐值一致 |
| `aggregate.completion_tokens` | — | **11875** | — |
| `aggregate.total_tokens` | — | **24984** | = 13109 + 11875 ✅ |
| `aggregate.by_model` | 20 项 | **20 项** | ✅ |
| `items` | 3 | **3** | ✅ |

⇒ **口径没变**：合并后的单次聚合与旧的两扫（独立 COUNT + 独立 SUM）
**逐值相等**（判据里也用旧口径重算做过对账，§5.12.4）。
⇒ **但「扫一遍」这件事在活体上是读不出来的**（本机 ~1.2k 行，buffers 量不出，
见 §5.12.4）⇒ 它的证据是**结构性**的（QueryTracer 数查询 = 1），
不是**运行时**的。别把「读数一样」当成「少扫了一遍」的证据。

**D1 的活体直查**（真库，逐条形状对照）：

| 形状 | 读数 |
|---|---|
| **旧读源**（v1 冻结族，同一条子查询的形状） | `v1_active_concurrent = 0`、`v1_avg_latency_7d = NULL`、`_request_logs` **0 行** |
| **新读源**（会话族 7 天窗，hot ∪ parent） | `deepseek-v4-flash` 54 行 / **avg 4046.1ms**；`glm-5.2` 29 行 / **avg 6395.8ms** |
| **视图内子查询**（外层行存在且键匹配时） | `deepseek-v4-flash` → **4046.1**、`glm-5.2` → **6395.8**、`grok-4.6` → NULL（该模型 7 天内确无成功行 ⇒ NULL 是**正确**的） |

⇒ 决定性点：**旧读源在这三条上全是 0 / NULL**（冻结族里没有数据），
新读源能取到**真实延迟** ⇒ 读源确实切到了会话族，**且键空间是对的**
（`raw_model_name` 而不是 `model`）。

**⚠ 但 `active_concurrent` 的活体值仍然是 0 —— 这也是正确的**：
会话族里 `is_auto_request` **全是 false**（hot 310 行 / parent 877 行，
auto 行数 **0 / 0**）⇒ 没有任何 auto 路由流量 ⇒ 5 分钟窗里自然没有可计数的行。
`is_auto_request` 来自 **`X-Gw-Is-Auto` 头或 `model="auto"`**
（`domains/streaming/handler.go:7006`）⇒ 本机这轮测试走的是**指定模型直连**，
不是 auto 路由。
⇒ **判别动作：看到 `active_concurrent = 0`，先问「这台机器有没有 auto 流量」
（`count(*) FILTER (WHERE is_auto_request)`），再问「读源对不对」**——
前者为 0 时，读源再对也是 0。**「0」有两种成因，必须分开。**

**端点与台账终态**：

```
/api/admin/auto-route/cost/customer → 200 []      （L11：外层聚合表 0 行）
/api/admin/auto-route/cost/model    → 200 []      （同上）
customer_cost_view 定义体含 _request_logs?          → false
model_cost_per_task_view 定义体含 _request_logs?   → false
customer_cost_view 含 FROM session_turns_hot st?    → true
model_cost_per_task_view 含 FROM session_turns st_par? → true
台账 session-summary-and-integrity-2026-09:883_session_family_cost_view_metrics.sql
     → content_sha256 = 84a614746d06…（与 canonical 副本一致）
退役族 _request_logs 行数 = 0 ；session_turns_hot = 310 ；session_turns = 877
```

**⚠ 端点仍 `[]` 是 L11，不是本轮修坏了**：外层聚合表 `api_key_model_cost`
**0 行**且**无触发器**（§5.12.3 的清点）⇒ 视图的相关子查询**压根不会被执行**
⇒ 两个端点必然返回 `[]`。**「读源已修」与「端点有数」是两件事。**

**本轮最后一个发现（写进判据纪律）**：`n_tup_ins` 的读数被**门自己**污染了
（13 → 连跑 3 次门 → 16），根因是 PG 计入回滚事务的插入。
⇒ 这条已写进 §5.12.2 的订正框与 §5.12.3 的证据链，**证据已换成「触发器 + 行数」**。

## 7. 本轮新增的经验

### 7.1 「执行快」不等于「语句快」：EXPLAIN 的 Execution Time 会骗人

`EXPLAIN ANALYZE` 报 `Execution Time: 0.03ms` 而 `Planning Time: 1195ms`。
只看执行时间 ⇒ 一切正常。
⇒ **判别动作：看慢 SQL 时，`Planning Time` 与 `Execution Time` 必须一起读。**
本轮的全部三个症状（写超时、正文不落、trace 丢）都指向规划，而规划慢的根因是
**共享缓冲太小 ⇒ catalog 页进不去缓存**，跟 SQL 本身无关。

### 7.2 「修好了」的注释不等于「修生效了」：查解析顺序

`hook.go` 的注释写着「raise the default budget to 2000ms」，代码也真的改了常量。
但 `GetPlatformInt` 的解析顺序让 `spec.Default`（500）**先命中**，代码里的
fallback 根本走不到。
⇒ 判别动作：**改「某个值的来源」时，先把它的解析顺序列出来**（DB → env →
spec.Default → 代码 fallback），再确认自己改的那一层**真的在链上**。
⇒ 通用式：**注释与代码一致 ≠ 生效**；生效的是「链上第一个命中的那一层」。

### 7.3 「给旁路单独开一个 ctx」不等于「预算分开了」

第一版修法（维度写用自己的 ctx）**被自己的判据当场抓住**：轮次写拿到的剩余预算是
−0.8ms。因为两个 `WithTimeout(bg, timeout)` 的 deadline 是**同一个绝对时刻**。
⇒ 判别动作：**要让两段各用各的预算，后一段的 deadline 必须从「后一段开始」起算**，
而不是「再开一个同长的 ctx」。
⇒ 通用式：**预算分离 = 计时起点分离**，不只是「换个 ctx 对象」。

### 7.4 部署验证通过 ≠ 数据可用

`VERIFY_PASS=1` 探的是进程/端口/版本。本轮实测：部署全绿、探活全绿、迁移全绿，
但**一行请求数据都没落库**。
⇒ 判别动作：任何「数据可用」的结论，必须有一份**读回**的证据
（`jsonb_typeof` 读正文、读 `turn_no` 连续性、读 `trace_events` 非空），
不能拿「接口 200」当落库证据。

### 7.5 端口会骗人：先问「这个端口是谁的」

迁移脚本的 docker 通道里出现 `postgresql://…@127.0.0.1:5432/…`，而本机 5432
恰好是**另一个项目**（`acc-pg17`）映射出来的。差点误判成「迁移打到别的库去了」。
实际无害——DSN 在**容器内部**解析，127.0.0.1:5432 就是 `llm-gateway-pg` 自己。
⇒ 判别动作：看到端口先问「这个端口是谁的」，再问「这个连接在哪一侧解析」。

### 7.6 「同一张表有两套形状」时，先问「哪一份是正典」

`prompt_injection_detections` 同时存在两套互不兼容的形状声明：
`sql/migrations/startup/315_*.sql` 声明一套（`detection_score` + `matched_rules` …），
`sql/schema/01-schema.sql` 与 `sql/objects/tables/*.sql` 声明另一套（`risk_level integer` …）。

**正典是后者**（基线 schema + 正典对象文件），不是迁移文件。
致命的一步是 315 用的 `CREATE TABLE IF NOT EXISTS`：**在已有正典表的库上是空操作**
⇒ 315 声明的那几列**从未被创建** ⇒ 查它们的代码恒 42703。
⇒ 判别动作：**查列/表时，「正典形状」取自 `sql/schema/01-schema.sql` 与
`sql/objects/tables/`，不取自迁移文件**；迁移文件可能是**从未生效**的声明
（`IF NOT EXISTS` 让它静默变成空操作）。
⇒ 推论：看到 `42703 column ... does not exist` 时，先去正典里核这一列**在不在**，
再去核「是哪条迁移声明的」——后者很可能就是**没生效**的那条。

### 7.7 形状错配会同时毒化读侧与**写侧**，而写侧常被降级成 WARN

同一张表的形状错配让读侧 500（可见），也让**写侧**每次 INSERT 都失败
（`domains/promptinjection/detector.go`）。但写侧失败只落一行 `slog.Warn`
⇒ **检测照常生效/拦截，只是审计记录一条不留**
⇒ 页面/报表上「这批请求没有注入命中」成了**假结论**。
⇒ 判别动作：**凡是「写不进去只记 WARN」的写点，优先怀疑「列/类型对不上」而不是「偶发失败」**；
把这类 WARN 升级为 Error（本轮已改），让「审计链断掉」在告警层可见。

### 7.8 真库门能抓到 mock 抓不到的东西

本轮 6 道新判据里，有 **4 个是我自己的错误**被真库门当场抓住：
`AS c` 把列别名写成了表别名；`FROM` 跑到了子查询**外面**；
`categories` 绑 JSON 字节（PG 数组字面量 22P02）；判据自己把数组扫进 `*string`。
**这四条在 mock 下全绿**（pgxmock 对参数类型不敏感，只认「有一条 INSERT 发生过」）。
⇒ 判别动作：**凡是 SQL 的形状本身是缺陷（列名、数组线格式、类型转换），
判据必须让 PG 自己解析**——把生产用的那条 SQL 提成包级常量，让判据**逐字**跑它，
而不是让判据自己抄一份（抄的那份会与生产漂移，且漂移了没人知道）。

### 7.9 「端点坏」与「端点被取消」要用两条证据分开

同一端点 500 有两种完全不同的成因：**服务端有缺陷** vs **客户端跑了**。
本轮的 `/api/logs`、`/api/admin/turns/sessions` 都被误判过一次。
⇒ 判别动作（两条一起看，缺一不可）：
  1. **同一请求逐字 curl 重放** ⇒ 200 ⇒ 服务端没问题（是客户端取消）；
  2. **服务端日志里 `context canceled`** ⇒ Go 的 `http.Server` 在客户端断开时
     取消 request ctx ⇒ 佐证。
⇒ 补充：**同端点同标签页时好时坏**（本轮 500/200/500）本身就是争抢的指纹 ——
真缺陷是**稳定**的。

### 7.10 工具的连接池上限会伪造出「数据恒为空」

内嵌 Browser 每标签页常驻一条 SSE，而 Chromium 对同源 HTTP/1.1 只有 **6 条**连接
（整个浏览器共享）⇒ 标签页一多，新请求被**客户端放弃** ⇒ 服务端 `context canceled`
⇒ 页面「完整、无报错、数据恒为空」。本轮一开始把它当成应用缺陷查了很久。
⇒ 判别动作：**做页面级验收前先问「这个工具每标签页占几条连接」**；
不满足时用**单页顺序访问的无头脚本**（本轮用的 playwright-core + Chrome for Testing），
它天然没有多标签页争抢。

---

## 8. 量具纪律（本轮复用的四条）

1. **正文是否落库用 `jsonb_typeof(col)='array'` 判**，不用 `IS NOT NULL`
   （JSON `null` 也满足 `IS NOT NULL` ⇒ 假绿）。
2. **正文写入目标是 `session_bodies_hot`**（独立表，`pg_inherits` 继承行数 = 0），
   `count(*) FROM session_bodies`（父表）**不含**它。晋级有 8h 延迟，
   父表最新时间戳停在几小时前**本身不构成停写证据**。
3. **会话头必须 `gw_` / `gt_` / `gs_` 前缀**，否则被 `sanitizeGwSessionHeader`
   静默丢弃 ⇒「多轮」退化成 N 次独立单轮而不报任何错。判别：`prompt_tokens`
   随轮次单调增长（实测 glm-5.2：13 → 15 → 19）。
4. **「同一个数字连续报三次」时先写探针**。本轮 `turn_no` 与时间序错位，
   靠逐轮 `turn_no/prompt_tokens/ts` 三列并排才定位到是**重放顺序**而非写顺序。

---

## 9. §5.11 之后新增的经验（本轮最后一段）

### 9.1 ★ 量具坏了的现象与服务端坏了**同形**

BSD `sed` 不认 LHS 的 `\n` ⇒ 标记没剥掉 ⇒ `json.loads` 失败 ⇒ **history 被悄悄
置空** ⇒ 下一轮请求体变成空串 ⇒ 服务端回 `400 sanitization_failed: Invalid
request body`。服务端日志写的是「请求体非法」，**看起来 100% 像产品缺陷**，
实际根因在量具（macOS 默认 `sed` 是 BSD 版，`\n` 只在 GNU sed 里是换行）。
⇒ **判别动作：看到「服务端说我的请求体非法」，先看落盘的 payload 对不对**——
本例落盘文件里还带着 `__HTTP__200__T1.5` 标记，一眼就能看出。
⇒ 配套纪律：量具的中间变量更新失败时**必须显式中止**，
不能让失败值（空串）继续流进下一轮 —— 「静默清空」比「直接报错」贵得多。

### 9.2 ★ 探活一次只碰**一个**模型

一次探 3 个模型，其中第 2 个撞上上游 429 ⇒ 凭据被冷却 3 分钟 ⇒ 第 3 个模型
（以及第 1 个）全部变成 `no_candidate` ⇒ 读起来像「这个族一个都没了」，
实际是**一个模型的问题被放大成了整个凭据**。
⇒ **判别动作：探活失败时先查 `credentials.availability_state`（凭据级），
再查 `credential_model_bindings.available`（模型级）**；两者的原因与修法完全不同
（见 §5.11.4 与 L9）。

### 9.3 ★ 「目录」不等于「可路由清单」

`/v1/models` 的返回**随可用凭据变化**（本机实测 89 → 77，正好等于另一个凭据的
provider 的模型数）。所以「模型不在目录里」这句话**既不成立也不准确**：
它可能只是**那个凭据当时不可用**。真正的归属要去
`provider_models` → `credentials` → `credential_model_bindings` → 探针/配额状态
逐层查（§5.11.6 的表就是这么列出来的）。
⇒ **推论：写「某模型不存在」之前，先确认目录是**谁**的目录。**

### 9.4 ★ 熔断的连坐范围要与写侧粒度对齐着看

同一个失败事件里，**两条粒度不同的路径同时在写**：
写侧 `WriteOnError` 对 `KindConcurrent` 走 `writeModelLevelFailureOnly`
（只冷自己那一个 (凭据, 模型) 绑定，`writer.go:518` 明写「✅ DO NOT update
credentials.availability_state」）；
熔断侧 `nodehealth` 的 `EffectSetCredentialUnavailable` 却把范围放大到**整个凭据**。
⇒ **判别动作：看到「一个模型的故障连坐了兄弟模型」，不要只看写侧那一条路径**——
`credential_model_bindings.unavailable_reason` 的**前缀**直接告诉你谁写的：
`auto_<kind>` = 模型级，`auto_credential_<kind>` = 凭据级。
本例 10 个绑定里 9 个是 `auto_credential_rate_limit`、1 个是 `auto_rate_limit`，
一眼就能看出放大发生在哪条路径。

### 9.5 ★ 同一件事在不同协议腿上的规则要逐腿核对

「Responses 腿接受 `status=incomplete`+`max_output_tokens` 作正向证据、
chat 腿不接受」——这类**协议腿之间的规则不对称**，靠读单个文件是发现不了的
（两个判据分散在 `bg/probe_http_validation.go` 和
`bg/credential_probe_response_validation.go`）。
⇒ **判别动作：凡是「按证据强弱定生死」的规则，列出它覆盖的**全部**腿**，
逐腿对照「同一现象在这条腿上判什么」**——本例是 Responses 放行、chat 定罪。
⇒ 也提醒：**不要为了让某条腿通过就改判据**——本仓的
「2xx 不等于健康、必须有正向证据」是刻意立的规矩（见 L10 的处理方式：
**记为遗留、单独一轮再议**，不在别人的任务里顺手改掉）。

---

## 10. §5.12 之后新增的经验（本轮续做）

### 10.1 ★ 「量不出来」是对**量法**的判断，不是对**事实**的判断

上一轮把 L3 记成「在本地小数据集上无法证明收益」。这句话本身没错，但它
**掩盖了一个可证的事实**：同一行集被独立扫了两次。
本轮换了量法就出来了 —— **执行次数**是结构性的、与数据量无关；
而 buffers / 墙钟在小数据集上**原理上就分辨不出**（第二趟全命中缓存，
实测三种写法都是 137 hit）。
⇒ **判别动作：说「量不出」之前，先问「我选的量具有没有分辨这件事的能力」**。
量具分辨不出的量（分辨率不足）≠ 事实不存在。
⇒ 推论：找**结构性**的量（执行次数、扫描的行集条数、依赖图形状），
而不是**连续**的量（耗时、buffers、CPU）—— 前者在小环境也能证伪。

### 10.2 ★ 夹具里被比较的两个键必须「故意不同」，否则门分不出键漂移

第一版真库门把会话行的 `model` 与 `raw_model_name` 设成**同一个值** ⇒
变异「把匹配键从 `raw_model_name` 换成 `model`」**没让门变红** ⇒
门「看着在测、其实测不出来」。
改成两个不同的字符串后，同一个变异立刻转红。
⇒ **判别动作：写夹具时问「我要防的那个漂移，会让这两个值不同吗」**；
若不会，夹具就钉不住它。
⇒ 这与「允许侧判据要配前提断言」是同一族：**门必须能分辨「对的」与「错的」**，
而不只是「有值」与「没值」。

### 10.3 ★ 「端点空」与「读源错」是**两件独立的事**

`/api/admin/auto-route/cost/customer` 返回 `[]`，原因有**两层**：
读源错（活指标读冻结族）+ 聚合链从未接线（`n_tup_ins=0`）。
只修读源 ⇒ 端点**仍然** `[]`（因为相关子查询在外层表零行时压根不执行）。
⇒ **判别动作：看到空结果，按这个顺序查**：
① 外层聚合表**有没有在增长** ⇒ 用 **`count(*)` + `max(bucket)`**
（⚠ **不要**用 `pg_stat_user_tables.n_tup_ins` 当「有没有被写」的证据——
它**会计入回滚事务的插入**，被测试夹具污染，见 §10.5）→ 决定「有没有数据」
② 读源对不对（视图/查询的源表）→ 决定「数据取对了吗」
③ 相关子查询在空表上**不执行** ⇒ 要证明读源，必须**造外层行**（夹具），
不能靠「视图建出来了」或「端点不报错」。

### 10.4 ★ 类型要查 `information_schema`，不要靠记忆

会话族 `api_key_id` 是 **text**、v1 是 **bigint**、聚合表是 **integer**
—— 三个值三种类型。凭印象写比较 ⇒ 建视图直接报
`operator does not exist: text = integer`。
⇒ **判别动作：跨族映射列时，逐列查类型**（一张 `information_schema` 查询就够），
并把这个差异**写进迁移注释**（它是下一个人的坑）。

### 10.5 ★ 判据会**污染它自己要用的度量**：先问「这个量会不会被我自己写高」

本轮用来证明「`api_key_model_cost` 从未被生产流量写过」的证据是
`pg_stat_user_tables.n_tup_ins = 0`。加完真库门之后，同一个读数变成 **13**，
而表里**0 行**、`n_tup_del=0` ⇒ 字面读数是「被写进 13 行、又删干净了」。
⇒ 根因（受控实验实测，不是猜的）：**PG 的 `n_tup_ins` 计入回滚事务里的插入**
——`BEGIN; INSERT; ROLLBACK;` 之后计数确实 +1；而本轮的真库门正是
`defer tx.Rollback()` 的夹具插入 ⇒ **门把自己的度量写高了**。
⇒ 决定性复现：连跑该门 3 次 ⇒ 计数 13 → 16（**恰好 +3**）、行数**恒 0**
⇒ 那 13 次插入**全部**是门自己的夹具。
⇒ **判别动作**：
① 用一个量下结论前，先问「**这个量会不会被我自己（的测试/任务）写高**」——
本例若不追问，就会把「测试跑过 13 次」误读成「生产写过 13 行」，
进而把一条正确结论（L11：聚合链从未接线）**整个推翻**；
② **可复现的判据**是「跑 N 次 ⇒ 计数恰好 +N、行数不变」，
这比任何一次读数都有说服力；
③ 判「某表有没有被写过」时，优先用**结构事实**（触发器在不在、现在几行、
`max(bucket)`），而不是**计数器**——计数器会被任何跑过夹具的测试抬高。

### 10.6 ★ 「0」有两种成因：**读源错** 与 **本来就没有这种流量**

`active_concurrent` 修完读源后，活体读数**仍然是 0**。
差点又写一次「修好了但读数还是 0 ⇒ 没修好」。
⇒ 查下去是**第二种成因**：会话族里 `is_auto_request` **全是 false**
（hot 310 行 / parent 877 行，auto 行数 0 / 0）⇒ 本机这轮测试走的是
**指定模型直连**，**压根没有 auto 路由流量** ⇒ 5 分钟窗里没有可计数的行，
**0 是正确的**。
（`is_auto_request` 来自 **`X-Gw-Is-Auto` 头或 `model="auto"`**，
见 `domains/streaming/handler.go:7006`。）
⇒ 7 天窗那个指标（`avg_latency_ms`）**同一时刻**取到了真实值
（4046.1ms / 6395.8ms）⇒ **同一个视图里两个指标一空一有数**，
这个不对称本身就是「读源已通、只是这条腿上没流量」的证据。
⇒ **判别动作：看到 0，先分它是「取错了」还是「没有可取的」**——
查**上游集合的大小**（`count(*) FILTER (WHERE is_auto_request)`、
`count(*) FILTER (WHERE success)`），而不是反复重跑同一个查询。
⇒ 这与 §10.3 是同一族但**方向相反**：那里是「端点空 ≠ 读源错」，
这里是「读数 0 ≠ 没修好」⇒ **合起来一条：任何单个读数都不能同时定两件事**。
