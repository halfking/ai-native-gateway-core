# request_logs → session_turns 双写退役：2026-10-20 复核轮

> 基线：本地 `main` = `d9fc3b58f`（合并 `origin/main` 74 提交后的合并提交）
> 环境：本机 PG 17.10（`llm-gateway-pg` 容器，127.0.0.1:54318）、Redis 7（6379）、
> 入口 `http://127.0.0.1:8782/v1`、部署产物 `2.5.8.2746`
> 口径：只写实测到的；未验证项显式标注。

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

---

## 1. 合并与构建

| 项 | 结果 |
|---|---|
| 合并 | `git merge origin/main`，1 处冲突（`LESSONS.md`）——两侧各追加了**不同**的经验条目（HEAD 侧 §138/§139，upstream 侧 2026-10-19/20 若干条），**两边都保留**（HEAD 在前） |
| 工具链 | `go1.27.2 darwin/arm64`（`~/sdk/go/bin/go`）。⚠️ `AGENTS.md` 写的 Homebrew 路径 `/opt/homebrew/bin/go` **已不存在** |
| 构建 | `CGO_ENABLED=0 go build ./...` → RC0 |
| 迁移 | `scripts/apply-db-revision-sequence.sh` → RC0，ledger 233 → 241 腿，870~878 全部入库 |
| 部署 | `deploy-local.sh deploy` → `VERIFY_PASS=1`，release `2.5.8.2746` |

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


| # | 项 | 性质 | 建议 |
|---|---|---|---|
| **L1** | **7 张 v1 分区表未带 `_` 待删标志**（`request_logs_2026_07/08/10/11/default`、`request_logs_bodies_2026_10/11`）。实测 `pg_inherits` 显示其父表是 `_request_logs` —— **父改名了、子没改**。而迁移 862 的集成测试期望 `public._request_logs_2026_10` ⇒ **新装与存量环境形状分叉** | 真实不一致 | 出一条迁移把存量分区改名 + 补齐 installer 通道；并把「待删对象」的查找口径从「`_` 前缀」改成「按 `pg_inherits` 递归」——前缀口径会漏掉这 7 张，而分区才是真正占空间的部分 |
| **L2** | `customer_cost_view.active_concurrent`（5 分钟窗）与 `model_cost_per_task_view.avg_latency_ms`（7 天窗）是**活指标**读冻结族 ⇒ 恒 0 / 恒 NULL，**无任何错误信号** | 静默失效 | 迁到 session 族等价源，或显式标注「v1 冻结后不可用」 |
| L3 | 列表端点 COUNT / SUM / 分页**三扫同一行集** | 效率 | 合并为一次聚合 |
| L4 | 5 张 `v_*` 视图 + `request_logs_bodies_with_current_month` 仍读 v1 裸母表（`internal/sqlreadguard` 已登记 `DEBT(R47)`） | 已知债 | 逐张判定：历史可读（保留）vs 活指标（必须迁） |
| L5 | `storage.admin_logs_native_turns_read` 默认关闭 ⇒ 列表页继续付 canonical 视图的 v1 臂 + 反连接成本 | 已决策 | 保持关闭（27.6% 缺口），但**成本要显式记账** |
| L6 | 本机 PG 的 `shared_buffers` 原值 400 kB 是**手改在容器数据目录**里的，不在仓库内 | 环境 | 已在本机修好并加部署门；**任何新建的本机 PG 都要按门检查**（门只在本仓的 deploy-local 路径生效） |
| **L7** | **客户端取消被记成 `500 / error.kind=server_error`**（`middleware/logging_mw.go` 的 `errorKind(status)` 只看状态码）⇒ 「客户端跑了」与「服务端坏了」在监控里**长得一样**；且多数 handler 的裸 `writeError(500, …)` **不留任何日志**（只有 `handleTurnsSessions` 等少数几处打了 WARN） | 观测 | 在 `emitRequestLog` 里判 `r.Context().Err() == context.Canceled` ⇒ `error.kind=client_aborted`（不改级别，避免把真 5xx 降级）；并让裸 `writeError(500,…)` 路径也留一行 WARN。**本轮未改**（共享中间件，改动面大且会动全部请求的日志形态） |
| **L8** | `prompt_injection_stats_enhanced`（迁移 364）**在正典库上建不出来**（引用不存在的 `detection_score`）；本轮已让统计端点**不再依赖**它，但**视图本身仍在** ⇒ 任何直接查它的外部消费者（`web-mobile/src/api/promptInjection.ts` 的注释仍这么写）继续 42P01 | 债 | 出一条新迁移用**正典形状**重建该视图（历史迁移字节不可改）；本轮未做（见 §5.6.1 的口径冲突：视图的等级分桶与正典整数 `risk_level` 不兼容，必须同时定口径） |

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

---

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
