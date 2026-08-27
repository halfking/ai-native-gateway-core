# Phase 0 漂移对账 SQL 与指标清单（V1 / V2 / body / stats）

> 状态：Phase 0 纯文档交付物，不改任何代码 / 迁移 / installer / 部署配置
> 日期：2026-08-27
> 基线分支：feat/request-fact-phase0
> 上游文档：
> - `docs/04-implementation/plan/2026-08-25-request-session-persistence-final-plan.md`（§5/§6：阶段划分、schema/cutover 约束、fallback/replay 设计）
> - `docs/04-implementation/plan/2026-08-26-request-fact-phase0-contract.md`（已冻结契约）
> - `docs/standards/database-change-and-real-verification.md`（DB 变更与真实验证强制标准）

## 0. 固定 Owner（引用总方案，不可重定义）

| 数据面 | Owner | 说明 |
| --- | --- | --- |
| 请求审计主事实 | telemetry → `request_logs(_hot)`（+ `request_logs_bodies(_hot)`） | V1 主事实，主写失败阻塞业务路径 |
| 会话 V2 | `sessionv2mirror.PersistHook` → `session_turns(_hot)` / `session_bodies` / `sessions` | V2 唯一 owner，shadow write，best-effort |
| 健康状态 | URSM v2（`domains/ursm/v2`） | 不保存 prompt/response/IR |
| 统计事实 | stats EventWriter / InboxConsumer → `stats_event_inbox` / `usage_facts` | body-free 事件，可重放 |
| Redis mirror | 非权威、可丢 | QueueMirror 仅观测 |
| 跨重启执行恢复 | `durable_llm_tasks`（durable PG） | 加密快照 + lease/fencing |
| 请求事实契约 | `internal/requestfact` | 契约唯一 owner |

Phase 0 目标（总方案 §6 Phase 0 第 4/5 条）：建立 V1/V2/body/stats drift SQL、指标和 failure matrix，并做 migration/installer/deploy manifest inventory；**不改变线上行为**。

---

## 1. 漂移对账只读 SQL

### 1.0 口径与前置说明（先读，再执行）

**全部为只读 SELECT。不包含任何 INSERT/UPDATE/DELETE/DDL。** 建议在只读副本执行；在主库执行时限定时间窗口并避开高峰。

**读取面（与代码写入口径一致）：**

| 数据面 | 读视图 / 表 | 定义来源 |
| --- | --- | --- |
| V1 主事实 | `public.request_logs_with_current_month`（= `request_logs_hot` ∪ `request_logs`） | installer embeddata/01-schema.sql、迁移 341/340 |
| V1 正文 | `public.request_logs_bodies_with_current_month`（= `request_logs_bodies_hot` ∪ `request_logs_bodies`） | 迁移 328a/353；573 后为正文唯一 SSOT |
| V2 轮次 | `public.session_turns_with_current_month`（= `session_turns_hot` ∪ `session_turns`） | 迁移 526 |
| V2 正文 | `public.session_bodies`（无 hot 表，直接写分区父表） | 迁移 430；写入点 `domains/session/v2/bodies_writer.go` |
| V2 会话聚合 | `public.sessions` | 迁移 430；聚合点 `session_aggregator.go` |
| 统计事件 | `public.stats_event_inbox` / `stats_event_dedup` | 迁移 536 |
| 统计事实 | `public.usage_facts` | 迁移 537 |
| 对账账本 | `stats_reconciliation_runs` / `stats_reconciliation_diffs` | 迁移 536/546/548 |

**RLS**：上述表/视图启用 `security_invoker` RLS。运维执行需以 superuser 连接，或先 `SET app.bypass_rls = 'true';`（仅只读会话）。

**时间窗口 `<since>`**：统一使用 `ts >= now() - interval '<N> days'`；首次对账建议 N=1（热窗口，视图已含 hot 表），扩大窗口时注意月分区是否已 promote/归档。

**「应镜像到 V2」的口径**（必须与 `internal/sessionv2mirror/hook.go:51-77` 保持一致，否则会把合法缺失误报为漂移）：

1. `gw_session_id` 非空（无会话上下文的请求不镜像）；
2. 终态：`success = TRUE` **或** `request_status IN ('failure','rate_limited')` **或** `error_kind <> ''`（in_progress 占位 INSERT 不镜像）；
3. 排除内部自动请求：`is_auto_request = TRUE` 且（`request_type IN ('title_gen','summary')` 或 `origin_actor IN ('auto-title-generator','auto-summary-generator','session-summary')` 或 `task_type` 为空）；
4. **Feature flag**：`sessions_v2.enabled` 与 `sessions_v2.shadow_write`（settings_kv）必须为 on。**若 flag 为 off，V2 全量“缺失”是合法状态，第 1.1 节 SQL 的 diff 不能视为漂移**。执行对账前先记录两个 flag 的当前值。

**统计终态口径**（与 `domains/stats/event.go:107-141` 一致）：`request_status IN ('success','failure','rate_limited')`；`event_id = <request_id>:request_terminal:0`。

**schema 依赖声明**：以下 SQL 使用的列（`gw_session_id`、`request_status`、`error_kind`、`is_auto_request`、`request_type`、`origin_actor`、`task_type`、`success`、`prompt_tokens`、`completion_tokens`、`total_tokens`、`cost_usd`、`ts`、`tenant_id`、`request_id`）均已核对存在于 `request_logs`/`request_logs_hot`（installer embeddata/01-schema.sql + 迁移 510/561/543）。V2 侧列名核对自迁移 430/431/456/464/513/525/526。若目标库未应用相应迁移，先按第 4 节清单核对账本再解读结果。

### 1.1 V1(request_logs) ↔ V2(session_turns) 行数与缺失漂移

#### SQL-A1：每日/每租户 V1 应镜像行数 vs V2 实际行数

```sql
-- 目的：按天+租户统计「应镜像到 V2 的终态请求」与 session_turns 实际行数的差值。
-- 预期结果形态：day / tenant_id / v1_eligible / v2_matched / missing_in_v2，
--               健康时每行 missing_in_v2 = 0。
-- 漂移判定：missing_in_v2 > 0（V2 缺行）；v2_matched > v1_eligible 不会出现（LEFT JOIN）。
-- 前置：sessions_v2.enabled / sessions_v2.shadow_write 均为 on，否则结果只反映 flag 关闭。
WITH v1 AS (
    SELECT (ts AT TIME ZONE 'UTC')::date AS day,
           tenant_id,
           request_id
    FROM public.request_logs_with_current_month
    WHERE ts >= now() - interval '1 day'
      AND COALESCE(gw_session_id, '') <> ''
      AND (success = TRUE
           OR request_status IN ('failure', 'rate_limited')
           OR COALESCE(error_kind, '') <> '')
      AND NOT (
            is_auto_request = TRUE
            AND (COALESCE(request_type, '') IN ('title_gen', 'summary')
                 OR COALESCE(origin_actor, '') IN ('auto-title-generator', 'auto-summary-generator', 'session-summary')
                 OR COALESCE(task_type, '') = '')
          )
),
v2 AS (
    SELECT request_id, tenant_id
    FROM public.session_turns_with_current_month
    WHERE ts >= now() - interval '2 day'   -- 略宽于 v1 窗口，容忍镜像延迟
)
SELECT v1.day,
       v1.tenant_id,
       count(*)                AS v1_eligible,
       count(v2.request_id)    AS v2_matched,
       count(*) - count(v2.request_id) AS missing_in_v2
FROM v1
LEFT JOIN v2 ON v2.request_id = v1.request_id AND v2.tenant_id = v1.tenant_id
GROUP BY v1.day, v1.tenant_id
ORDER BY v1.day DESC, missing_in_v2 DESC;
```

#### SQL-A2：V2 孤儿行（session_turns 有、V1 无对应行）

```sql
-- 目的：检测 V2 存在但 V1 主事实缺失（或已被数据生命周期清理）的孤儿 turn。
-- 预期结果形态：空结果集。
-- 漂移判定：返回任意行即漂移（V2 不允许先于/脱离 V1 存在；V1 清理未同步 V2 也在此暴露）。
SELECT v2.tenant_id, v2.request_id, v2.session_id, v2.turn_no, v2.ts
FROM public.session_turns_with_current_month v2
LEFT JOIN public.request_logs_with_current_month rl
       ON rl.request_id = v2.request_id AND rl.tenant_id = v2.tenant_id
WHERE v2.ts >= now() - interval '2 day'
  AND rl.request_id IS NULL
LIMIT 200;
```

#### SQL-A3：字段级数值漂移（success / token / cost 投影不一致）

```sql
-- 目的：同一 request_id 在 V1 与 V2 的 success、token、cost 投影不一致。
-- 预期结果形态：空结果集（或仅极少可解释行）。
-- 漂移判定：非空。重点成因：V1 late enrichment（UPDATE 终态补写）后 V2 turn 幂等不可更新，
--           或镜像转换丢字段（hook.go entryToProcessedRequest 的 nil-safe 拷贝遗漏）。
SELECT rl.request_id,
       rl.tenant_id,
       rl.success                       AS v1_success,
       st.success                       AS v2_success,
       rl.prompt_tokens                 AS v1_prompt_tokens,
       st.prompt_tokens                 AS v2_prompt_tokens,
       rl.completion_tokens             AS v1_completion_tokens,
       st.completion_tokens             AS v2_completion_tokens,
       rl.cost_usd                      AS v1_cost_usd,
       st.cost_usd                      AS v2_cost_usd
FROM public.request_logs_with_current_month rl
JOIN public.session_turns_with_current_month st
  ON st.request_id = rl.request_id AND st.tenant_id = rl.tenant_id
WHERE rl.ts >= now() - interval '1 day'
  AND (rl.success IS DISTINCT FROM st.success
       OR (rl.prompt_tokens IS NOT NULL AND COALESCE(st.prompt_tokens, 0) <> rl.prompt_tokens)
       OR (rl.completion_tokens IS NOT NULL AND COALESCE(st.completion_tokens, 0) <> rl.completion_tokens)
       OR (rl.cost_usd IS NOT NULL AND st.cost_usd IS NOT NULL AND st.cost_usd <> rl.cost_usd))
LIMIT 200;
```

#### SQL-A4：session 内 turn_no 连续性

```sql
-- 目的：检测同一会话 turn_no 是否从 1 连续递增（缺 turn / 回填洞）。
-- 预期结果形态：空结果集（或人工确认的合法回填洞）。
-- 漂移判定：turns <> (max-min+1) 或 min <> 1。
-- 注意：backfill/重放场景（source_kind='backfill'）可能合法产生洞，需结合 source_kind 解读。
SELECT tenant_id, session_id,
       count(*)                       AS turns,
       min(turn_no)                   AS min_turn_no,
       max(turn_no)                   AS max_turn_no,
       count(*) FILTER (WHERE source_kind = 'backfill') AS backfill_turns
FROM public.session_turns_with_current_month
WHERE ts >= now() - interval '2 day'
GROUP BY tenant_id, session_id
HAVING count(*) <> (max(turn_no) - min(turn_no) + 1) OR min(turn_no) <> 1
LIMIT 100;
```

#### SQL-A5：sessions 聚合快照 vs turns 实际行数

```sql
-- 目的：sessions.total_turns 快照与 session_turns 实际行数的漂移
--       （aggregate 是事务外 best-effort，见 session_writer_v2.go 独立重试路径）。
-- 预期结果形态：空结果集或个位数可解释行。
-- 漂移判定：actual <> total_turns。
SELECT s.tenant_id, s.session_id, s.total_turns,
       count(t.request_id) AS actual_turn_rows
FROM public.sessions s
LEFT JOIN public.session_turns_with_current_month t
       ON t.session_id = s.session_id AND t.tenant_id = s.tenant_id
WHERE s.updated_at >= now() - interval '2 day'
GROUP BY s.tenant_id, s.session_id, s.total_turns
HAVING count(t.request_id) <> COALESCE(s.total_turns, 0)
LIMIT 100;
```

### 1.2 body 存储缺失漂移

#### SQL-B1：V1 请求行缺 bodies 行（正文侧表缺失）

```sql
-- 目的：终态成功请求应有正文侧表行；检测 request_logs_bodies(_hot) 缺失。
-- 预期结果形态：空或极小（仅「正文未被捕获」的请求类型）。
-- 漂移判定：success 终态请求无 bodies 行（request/response/outbound 全缺），
--           说明 upsertRequestLogBodies 同事务 upsert 失败或被旁路。
SELECT rl.tenant_id, rl.request_id, rl.ts, rl.request_status
FROM public.request_logs_with_current_month rl
LEFT JOIN public.request_logs_bodies_with_current_month b
       ON b.request_id = rl.request_id
WHERE rl.ts >= now() - interval '1 day'
  AND rl.request_status = 'success'
  AND b.request_id IS NULL
LIMIT 200;
```

#### SQL-B2：bodies 行三个正文全 NULL（占位空行）

```sql
-- 目的：bodies 表存在行但 request/response/outbound 全 NULL ——
--       写入路径失败残留或「静默变空」问题（总方案 P1：safeJSONMarshal 失败返回空）。
-- 预期结果形态：空结果集。
-- 漂移判定：非空即异常（正文表按设计只保存正文，全 NULL 行没有存在意义）。
SELECT request_id, ts
FROM public.request_logs_bodies_with_current_month
WHERE ts >= now() - interval '1 day'
  AND request_body IS NULL
  AND response_body IS NULL
  AND outbound_body IS NULL
LIMIT 200;
```

#### SQL-B3：V2 turn 有、session_bodies 缺

```sql
-- 目的：turn 与 bodies 设计为同事务写入（SessionWriterV2.Write），
--       出现「有 turn 无 body」即事务拆分/回滚半写的漂移。
-- 预期结果形态：空结果集。
-- 漂移判定：非空即漂移。
-- 注意：session_bodies 无 hot 表也无 union 视图，直接查父表（RLS 同前）。
SELECT t.tenant_id, t.request_id, t.session_id, t.turn_no, t.ts
FROM public.session_turns_with_current_month t
LEFT JOIN public.session_bodies b
       ON b.request_id = t.request_id AND b.tenant_id = t.tenant_id
WHERE t.ts >= now() - interval '2 day'
  AND b.request_id IS NULL
LIMIT 200;
```

#### SQL-B4：V1↔V2 正文存在性方向漂移

```sql
-- 目的：同一请求的正文在 V1 bodies 与 V2 session_bodies 的存在性应同向
--       （都有或都无）；一侧有一侧无说明某条转换/写入路径丢失。
-- 预期结果形态：空结果集。
-- 漂移判定：非空。常见根因：parseProtocolMessages 解析失败返回 nil（静默变空，
--           总方案 P1 问题）或 V1/V2 写入时序错位（V2 只处理终态）。
SELECT rl.request_id,
       (b.request_body IS NULL)     AS v1_req_body_missing,
       (sb.request_delta IS NULL OR sb.request_delta = 'null'::jsonb) AS v2_req_delta_missing,
       (b.response_body IS NULL)    AS v1_resp_body_missing,
       (sb.response_delta IS NULL OR sb.response_delta = 'null'::jsonb) AS v2_resp_delta_missing
FROM public.request_logs_with_current_month rl
JOIN public.session_turns_with_current_month t
  ON t.request_id = rl.request_id AND t.tenant_id = rl.tenant_id
LEFT JOIN public.request_logs_bodies_with_current_month b
       ON b.request_id = rl.request_id
LEFT JOIN public.session_bodies sb
       ON sb.request_id = rl.request_id AND sb.tenant_id = rl.tenant_id
WHERE rl.ts >= now() - interval '1 day'
  AND ((b.request_body IS NULL)
        <> (sb.request_delta IS NULL OR sb.request_delta = 'null'::jsonb)
       OR (b.response_body IS NULL)
        <> (sb.response_delta IS NULL OR sb.response_delta = 'null'::jsonb))
LIMIT 200;
```

> 注意：V1 侧 `request_bodies_summary` 开关（client.go requestBodiesSummaryEnabled）会把 V1 正文替换为摘要 JSON（非 NULL），只改变形态不改变存在性，因此不影响本条 SQL 的判读。

### 1.3 stats 事件与主事实的漂移

#### SQL-C1：usage_facts vs V1 终态请求（每日/每租户计数对账）

```sql
-- 目的：统计事实层与 V1 主事实的行数对账（与 domains/stats/reconciliation.go 同口径）。
-- 预期结果形态：空结果集（两侧计数相等）。
-- 漂移判定：diff <> 0 —— diff > 0 为「缺事实」（EventWriter 丢事件），diff < 0 为「幻影事实」。
WITH v1 AS (
    SELECT (ts AT TIME ZONE 'UTC')::date AS day, tenant_id, count(*) AS terminal_requests
    FROM public.request_logs_with_current_month
    WHERE ts >= now() - interval '1 day'
      AND request_status IN ('success', 'failure', 'rate_limited')
    GROUP BY 1, 2
),
facts AS (
    SELECT (occurred_at AT TIME ZONE 'UTC')::date AS day, tenant_id, count(*) AS fact_rows
    FROM public.usage_facts
    WHERE occurred_at >= now() - interval '1 day'
    GROUP BY 1, 2
)
SELECT COALESCE(v1.day, f.day)        AS day,
       COALESCE(v1.tenant_id, f.tenant_id) AS tenant_id,
       COALESCE(v1.terminal_requests, 0)   AS v1_terminal_requests,
       COALESCE(f.fact_rows, 0)            AS usage_fact_rows,
       COALESCE(v1.terminal_requests, 0) - COALESCE(f.fact_rows, 0) AS diff
FROM v1
FULL OUTER JOIN facts f ON f.day = v1.day AND f.tenant_id = v1.tenant_id
WHERE COALESCE(v1.terminal_requests, 0) <> COALESCE(f.fact_rows, 0)
ORDER BY day DESC
LIMIT 200;
```

#### SQL-C2：逐请求缺失（usage_facts 缺 V1 终态行）

```sql
-- 目的：定位缺失事实的具体 request_id（比 C1 更细）。
-- 预期结果形态：空结果集。
-- 漂移判定：非空（EventWriter deadLettered / queue 满丢弃 / 未到 terminal）。
SELECT rl.tenant_id, rl.request_id, rl.request_status, rl.ts
FROM public.request_logs_with_current_month rl
LEFT JOIN public.usage_facts uf ON uf.request_id = rl.request_id
WHERE rl.ts >= now() - interval '1 day'
  AND rl.request_status IN ('success', 'failure', 'rate_limited')
  AND uf.request_id IS NULL
LIMIT 200;
```

#### SQL-C3：幻影事实（usage_facts 有、V1 无）

```sql
-- 目的：V1 数据生命周期清理/重放重复可能留下无主事实。
-- 预期结果形态：空结果集。
-- 漂移判定：非空（幻影行会进入 rollup 造成统计虚高）。
SELECT uf.tenant_id, uf.request_id, uf.occurred_at, uf.status
FROM public.usage_facts uf
LEFT JOIN public.request_logs_with_current_month rl
       ON rl.request_id = uf.request_id AND rl.tenant_id = uf.tenant_id
WHERE uf.occurred_at >= now() - interval '1 day'
  AND rl.request_id IS NULL
LIMIT 200;
```

#### SQL-C4：stats_event_inbox 积压 / 重试 / 错误

```sql
-- 目的：inbox 未处理行、高重试、带错误行的分布。
-- 预期结果形态：同步投影模式下（默认），EventWriter 落 inbox 后立即标记 processed_at，
--               pending 应稳定为 0；异步 consumer 模式（LLM_GATEWAY_STATS_INBOX_CONSUMER=1）
--               允许秒级少量 pending。
-- 漂移判定：pending_rows 长时间非零、oldest 超过分钟级、high_attempt_rows/errored_rows > 0。
SELECT (occurred_at AT TIME ZONE 'UTC')::date AS day,
       count(*)                                              AS pending_rows,
       min(occurred_at)                                      AS oldest,
       count(*) FILTER (WHERE process_attempts >= 3)          AS high_attempt_rows,
       count(*) FILTER (WHERE last_error IS NOT NULL)         AS errored_rows
FROM public.stats_event_inbox
WHERE processed_at IS NULL
  AND occurred_at >= now() - interval '7 days'
GROUP BY 1
ORDER BY 1 DESC;
```

#### SQL-C5：stats_event_dedup 与 inbox 一致性

```sql
-- 目的：EventWriter 在同一事务写 dedup + inbox（event_writer.go persist()），
--       dedup 指针存在而 inbox 行缺失即为事务异常/人工干预痕迹。
-- 预期结果形态：空结果集。
-- 漂移判定：非空。
SELECT d.event_id, d.occurred_at
FROM public.stats_event_dedup d
LEFT JOIN public.stats_event_inbox i
       ON i.event_id = d.event_id AND i.occurred_at = d.occurred_at
WHERE d.occurred_at >= now() - interval '1 day'
  AND i.event_id IS NULL
LIMIT 100;
```

#### SQL-C6：对账账本最近运行与未关闭 diff

```sql
-- 目的：直接读取内置 reconciliation 的结论（worker 每 6h 跑一次）。
-- 预期结果形态：status='completed'、error IS NULL；diffs 无 resolution='open' 行。
-- 漂移判定：status<>'completed'、error 非空、或 open diff 存在。
SELECT run_id, started_at, finished_at, status,
       rows_compared, diff_count, rows_repaired, error
FROM public.stats_reconciliation_runs
ORDER BY started_at DESC
LIMIT 10;

SELECT dimension_type, metric, resolution, count(*) AS rows
FROM public.stats_reconciliation_diffs
WHERE created_at >= now() - interval '7 days'
GROUP BY dimension_type, metric, resolution
ORDER BY resolution, rows DESC;
```

#### SQL-C7：usage_facts 修订号口径检查（总方案 P1：revision 未闭环）

```sql
-- 目的：确认生产事实是否出现 revision > 1（correction）。
--       当前写入固定 revision=1（inbox_consumer.go），revision>1 说明出现人工修正，
--       需要核对 rollup/reconciliation 是否都按 latest-revision 口径取数。
-- 预期结果形态：仅 revision=1 一行。
-- 漂移判定：出现 revision>1 的行数 > 0 时，所有下游必须切换 latest-revision 视图口径。
SELECT revision, count(*) AS fact_rows
FROM public.usage_facts
WHERE occurred_at >= now() - interval '30 days'
GROUP BY revision
ORDER BY revision;
```

### 1.4 schema 待核实点（不编造列名的声明）

以下项在本次代码核对中**未能 100% 确认线上真实 schema**，相关 SQL 已按仓库内 schema 定义编写，但执行前需以 `information_schema.columns` 实测（符合 `docs/standards/database-change-and-real-verification.md` §2「禁止以类比代替 schema probe」）：

1. **`request_logs_bodies_hot.tenant_id` 是否存在**：当前 HEAD 代码（`domains/hooks/observability/telemetry/client.go:2180`，merge d2cbaf88b 引入）向该列写入 `tenant_id`；而迁移 601（已注册 installer）DROP 该列，604（未注册）DROP 父表同名列。三个事实源互相矛盾（详见 §4.7）。B 类 SQL 未引用该列，不受影响；**任何按 tenant 维度清理 bodies 的运维 SQL 在核实前禁止执行**。
2. **目标库迁移账本状态**：`public.repository_schema_migrations` 的 scope/version/checksum 需实测（§4.5 的核对 SQL 是只读的）。573/600/601/602 等 DDL 是否已在目标库应用，直接决定 1.1–1.3 节 SQL 引用的视图/列是否存在。
3. **`request_logs.request_body` 等三列**：573 之后的库已 DROP；installer 全新安装路径（embed 基线 + 注册迁移子集，不含 573）仍保留这些列。1.2 节 SQL 一律走 `request_logs_bodies_with_current_month` 视图，不引用主表正文列，两种库形态下均可执行；但视图 `request_logs_bodies_progress`（embed 基线中存在，引用主表正文列）在 573 库上不可用，不要拿它做对账。

---

## 2. 指标清单（按代码现有埋点实际情况）

以下指标名均核对自仓库源码。**「缺口」= 该漂移当前无任何指标覆盖。**

### 2.1 V2 / 镜像路径

| 指标 | 类型 / labels | 定义位置 | 覆盖的漂移 | 状态 |
| --- | --- | --- | --- | --- |
| `llm_gateway_shadow_write_failed_total{kind="session_v2"}` | Counter | `metrics/prometheus.go:334`，递增点 `internal/sessionv2mirror/hook.go:114` | V2 shadow write 失败（V1 成功、V2 丢行） | 已覆盖；告警 `deploy/prometheus/alerts/shadow-write-failures.yaml`（ShadowWriteSessionV2Failing，>10/min 持续 5m） |
| `session_v2_mirror_backlog_pending` | Gauge | `internal/sessionv2mirror/backlog.go:59` | 进程内积压深度（有界 10000，FIFO 淘汰即丢） | 已覆盖；**无对应告警规则（缺口：告警缺失）** |
| `llm_gateway_shadow_write_failed_total{kind="attachment"}` | Counter | `internal/attachmentmirror/hook.go:69` | 附件关系镜像丢行 | 已覆盖（告警同上 yaml） |
| `sessions_v2_write_success_total` / `sessions_v2_write_failure_total` / `sessions_v2_write_latency_seconds` | Counter/Histogram | `metrics/sessions_v2_metrics.go` | V2 写成功/失败/延迟 | **缺口：定义存在但全仓库无递增点（死指标），实际不可用** |
| `sessions_v2_dual_read_diff_total` | Counter | `metrics/sessions_v2_metrics.go` | V1/V2 双读差异 | **缺口：同上，无递增点** |
| `sessions_v2_cache_hit_total` / `sessions_v2_cache_miss_total` | Counter | `metrics/sessions_v2_metrics.go` | V2 缓存命中 | **缺口：同上，无递增点** |

### 2.2 V1 主写与降级路径

| 指标 | 类型 / labels | 定义位置 | 覆盖的漂移 | 状态 |
| --- | --- | --- | --- | --- |
| `llm_gateway_ringbuffer_dropped_total` | Counter | `metrics/prometheus.go:341` | telemetry 降级时内存 RingBuffer 溢出丢行（V1 主事实丢失） | 已覆盖；告警 RingBufferOverflowed（critical，increase>0/1m） |
| `llm_gateway_rawaudit_write_failed_total` | Counter | `metrics/prometheus.go:347` | 本地 raw audit JSONL 写失败 | 已覆盖（告警同上 yaml） |
| `request_wal_events_total` | Counter | `domains/hooks/observability/telemetry/request_logger_prometheus.go:33` | WAL 事件量（旁路观测，非丢失计数） | 已覆盖（仅流量观测） |
| `telemetry_sanitize_events_total` | Counter | `domains/hooks/observability/telemetry/sanitize_prometheus.go:35` | 写前清洗触发（含 body loss 标记） | 已覆盖 |
| **V1 主写失败率**（request_logs INSERT/UPDATE 失败计数） | — | — | 主事务失败直接影响业务 | **缺口：无独立指标，仅 slog.Error；只能从业务 5xx + 日志推断** |
| **request_logs_bodies_hot upsert 失败** | — | — | 正文侧表丢失（client.go:1401 仅 slog.Error，同事务返回错误会连坐主写） | **缺口** |

### 2.3 stats 路径

| 指标 | 类型 / labels | 定义位置 | 覆盖的漂移 | 状态 |
| --- | --- | --- | --- | --- |
| `llm_gateway_stats_reconciliation_runs_total{status}` | CounterVec | `metrics/stats_reconciliation_metrics.go:26`，递增点 `domains/stats/reconciliation.go:132-237` | 对账运行成败 | 已覆盖 |
| `llm_gateway_stats_reconciliation_diffs_total{resolution}` | CounterVec | 同上:38 | open / phantom_open / auto_repaired diff 量 | 已覆盖；**无告警规则（缺口：告警缺失）** |
| `llm_gateway_stats_adjustments_total{action,result}` | CounterVec | 同上:44 | 人工调整动作 | 已覆盖 |
| `llm_gateway_stats_monthly_closed_skipped_total` | Counter | 同上:57 | 封账月跳过 | 已覆盖 |
| `llm_gateway_stats_shadow_comparisons_total{endpoint,result}` | CounterVec | `metrics/stats_shadow_metrics.go:14` | stats 读路径影子对比 | 已覆盖（读路径） |
| **EventWriter 丢失**（`dropped` / `persistFailed` / `deadLettered`） | — | `domains/stats/event_writer.go:29-34`（仅 `Stats()` 方法暴露给测试） | stats 事件在队列满/持续失败后丢失 | **缺口：计数器仅存在于内存原子变量，未注册 Prometheus，无法抓取** |
| **InboxConsumer 处理量/失败/死信** | — | `domains/stats/inbox_consumer.go`（无 metrics 引用） | 异步消费积压与死信 | **缺口：完全无指标** |

### 2.4 durable / 恢复路径（背景参考）

| 指标 | 类型 | 定义位置 | 覆盖 |
| --- | --- | --- | --- |
| `durable_tasks_active` | Gauge | `metrics/durable_metrics.go:28` | 活跃 durable 任务 |
| `durable_recovery_runs_total` | Counter | 同上:37 | 恢复运行 |
| `durable_pending_projections_total` / `durable_pending_projection_errors_total` | Gauge | 同上:47/57 | 待投影/投影错误 |
| `durable_lease_lost_total` | Counter | 同上:66 | lease 丢失 |

### 2.5 指标缺口汇总（Phase 5 前需要补齐的最小集）

1. V1 主写失败计数（telemetry insert/update error）。
2. request_logs_bodies upsert 失败计数。
3. EventWriter dropped/persistFailed/deadLettered 导出到 Prometheus。
4. InboxConsumer 消费/失败/DLQ 指标。
5. `session_v2_mirror_backlog_pending` 与 `llm_gateway_stats_reconciliation_diffs_total{resolution="open"}` 的告警规则。
6. sessions_v2_metrics.go 中 6 个死指标：要么接线，要么删除，避免面板误信恒 0。
7. 每日定时漂移对账（§1 的 A1/C1）落为指标/gauge —— 当前只有人工 SQL。

---

## 3. Failure Matrix（写入路径失败 → 影响 → 检测 → 恢复）

恢复手段引用总方案 §5/§6 的 fallback/replay 设计（outbox、本地归档、durable PG、inbox 重放），**当前代码尚未实现 outbox/本地归档**（Phase 2/3 交付），下表「恢复手段」区分「当前可用」与「Phase 2/3 后可用」。

| # | 失败的写入路径 | 受影响投影 | 检测手段 | 恢复手段 |
| --- | --- | --- | --- | --- |
| F1 | telemetry 主事务：`request_logs_hot` INSERT 失败 | V1 主事实、V2、stats、bodies、usage_ledger 全部下游 | 业务 5xx + slog；`llm_gateway_ringbuffer_dropped_total`（若降级到 RingBuffer 后溢出）；**无直接指标（缺口 2.2）** | 当前：降级 RingBuffer + `request_wal_hot` 恢复路径（`request_logger.go`），重启回放；Phase 2：LocalRequestArchive 本机文件重入归档 |
| F2 | `request_logs_bodies_hot` upsert 失败（与主写同事务） | V1 正文事实（outbound/request/response body） | 同事务回滚导致 F1 症状；单独 slog.Error `persist request_logs_bodies_hot failed`；**无指标（缺口）** | 当前：随 F1 一起走 RingBuffer/重试；Phase 2：归档 envelope 正文重放；§4.7 的 tenant_id schema 矛盾未解决前，此路径可能系统性失败 |
| F3 | `sessionv2mirror` V2 shadow write 失败 | `session_turns(_hot)`、`session_bodies`、`sessions` 聚合 | `llm_gateway_shadow_write_failed_total{kind="session_v2"}`（告警已有）；`session_v2_mirror_backlog_pending` gauge（告警缺失）；SQL-A1/A2 | 当前：进程内 backlog（上限 10000，FIFO 淘汰即永久丢失）+ DrainBacklog 人工重放；重启丢失。Phase 3：durable projection outbox claim/lease/retry/DLQ 重放 |
| F4 | V2 转换静默变空（parseProtocolMessages 返回 nil / safeJSONMarshal 空对象） | `session_bodies.request_delta/response_delta` 空 | SQL-B2/B4；`telemetry_sanitize_events_total`（仅部分场景）；**无专项指标（缺口）** | 当前：无自动恢复（数据已丢）；Phase 1 契约要求核心正文失败进 retry/DLQ、不允许空 JSON 兜底 |
| F5 | V2 后置投影（session_turn_logs / sessions aggregate）失败 | 环节日志、会话聚合快照 | SQL-A5；仅 slog.Warn | 当前：有限重试后放弃；Phase 3：统一进入 post-persist dispatcher + outbox |
| F6 | fallback / WAL replay 只补 V1 绕过 onPersisted hook | V2、stats（重放的请求不产生镜像/事件） | SQL-A1（V2 缺行）+ SQL-C1（stats 缺事实）同现 | 当前：无；Phase 3：replay 统一走 post-persist dispatcher，按 request_id 幂等补 V2+stats |
| F7 | stats EventWriter 队列满 + 同步 fallback 失败 | `stats_event_inbox` / `usage_facts` | SQL-C1/C2/C4；`Stats()` 计数器未导出（缺口） | 当前：内存 deadLettered 后丢弃（不可恢复）；Phase 5：EventWriter durable DLQ + ReplayDLQ 管理入口 |
| F8 | InboxConsumer 处理失败/死信（异步模式） | `usage_facts`（inbox 已有事件未投影） | SQL-C4（pending/last_error）；**无指标（缺口）** | 当前：inbox 行保留（processed_at IS NULL），可人工重置重试；Phase 5：DLQ + replay 指标告警 |
| F9 | stats rollup / reconciliation 错误 | `stats_usage_daily/monthly` 投影 | `llm_gateway_stats_reconciliation_runs_total{status="failed"}`；SQL-C6 | 当前：reconciliation worker 自修复（auto_repaired）+ `stats_adjustments` 人工调整；封账月走 adjustments |
| F10 | SystemMonitor Redis 队列/lease 异常（fallback task ID=0、requeue 非原子等，总方案 P1） | Redis 执行调度元数据；非权威观测 | durable 指标组（2.4）；QueueMirror depth | 当前：durable PG reclaim/fallback 恢复执行（`durable_llm_tasks` 为跨重启恢复源）；Phase 4：稳定 task identity + fencing + DLQ |
| F11 | hot→parent promotion 失败/半完成（`promote_request_logs_hot_to_partition` / `promote_session_turns_hot_to_partition`） | V1/V2 冷分区行 | 视图 union 后 SQL-A1/A2 会暴露双行或缺失；migration 602 后 promote 原子 CTE | 当前：602 原子 promote（已注册）；603/604 修复类迁移未注册（§4.6，禁止执行）；Phase 6：schema truth preflight |
| F12 | attachmentmirror 附件镜像失败 | `request_attachments` 关系镜像 | `llm_gateway_shadow_write_failed_total{kind="attachment"}`（告警已有） | 当前：仅日志；从 V1 `attachments` JSONB 列可回填（401 关系化后以关系表为准，回填需人工 SQL） |

---

## 4. Migration / Installer / Deploy Manifest 清单核对

### 4.0 迁移注册面（三条互不相同的通道）

仓库中「迁移是否注册」有三个独立事实源，核对时必须逐一对齐：

1. **严格迁移账本通道**：`scripts/run-migrations-strict.sh` 递归应用 `sql/migrations/{startup,domain,ursm}` 下全部 `[0-9]*.sql`（排除 `*.down.sql`），记录到 `public.repository_schema_migrations(scope, version, migration_name, checksum)`。**任何放进该目录树的 up 文件都会被执行**（包括 `up/` 子目录）。
2. **installer embed 通道**：`installer/cmd/llm-gw-installer/main.go` 用 `go:embed` 显式列举 `embeddata/startup/` 中的迁移子集（新建库安装路径），**当前只注册 36 个文件（版本 511–602 的子集）**，与通道 1 的集合不相等。
3. **进程内 Go 镜像通道**：`db/db.go` `applyMigrationsOnce` 内联 `ensure*Schema` 函数镜像了部分历史迁移（`gateway migrate` 子命令即走 `db.Open → ApplyMigrations`）。

另有 `deploy/sql/migrations/`（V352 system monitor fallback 队列）与 `sql/migrations/manual|operations|timeout-optimization` 为人工/运维 SQL，不在自动注册面内。

**deploy manifest**：`deploy/` 下未发现逐文件枚举迁移的 manifest；部署侧迁移执行依赖上述通道 1（`scripts/init-minimal-db.sh`、`scripts/local-deploy.sh`、`scripts/migrate-db-kaixuan1.sh` 调用 strict 脚本）与通道 3（服务启动 `db.Open`）。`deploy/DEPLOYMENT_CHECKLIST.md` 中的 `schema_migrations` 操作示例是历史遗留（老 `schema_migrations` 表），与现行 `repository_schema_migrations` 账本不是同一对象。

### 4.1 startup scope 完整清单（`sql/migrations/startup/`，325 个 up 文件）

统计：up 文件 325（顶层 313 + `up/` 子目录 12），down 文件 209，跳过/禁用文件 9（见 4.1.4）。用途一句话按文件名与 up.sql 头部注释归纳。

#### 4.1.1 001–054 与 120（基础表与早期增强，53 个）

| 版本 | 文件 | 用途 |
| --- | --- | --- |
| 001 | 001_users_table.sql | 用户表 |
| 002 | 002_work_types.sql | 工作类型表 |
| 003 | 003_tuning_params.sql | 调优参数表 |
| 004 | 004_tuning_signals.sql | 调优信号表 |
| 005 | 005_tuning_proposals.sql | 调优提案表 |
| 006 | 006_tenants_table.sql | 租户表 |
| 007 | 007_maas_billing.sql | MaaS 计费表 |
| 008 | 008_billing_orders.sql | 计费订单表 |
| 009 | 009_request_logs_tenant_backfill.sql | request_logs 租户回填 |
| 010 | 010_model_probe_runs.sql | 模型探测运行表 |
| 011 | 011_model_probe_state.sql | 模型探测状态表 |
| 012 | 012_model_probe_runs_rls.sql | 探测表 RLS |
| 013 | 013_compression_columns.sql | request_logs 压缩元数据列 |
| 015 | 015_admin_llm_work_types.sql | 管理端工作类型 |
| 016 | 016_outbound_body.sql | outbound_body 列 |
| 017 | 017_quality_fix_mode.sql | 质量修复模式列 |
| 018 | 018_upstream_finish_reason.sql | 上游 finish_reason 列 |
| 019 | 019_passive_probe_state.sql | 被动探测状态 |
| 020 | 020_request_logs_unique_request_id.sql | request_logs 唯一 request_id |
| 021 | 021_tool_registry_and_metatools.sql | 工具注册表 |
| 022 | 022_settings_kv.sql | settings_kv 配置表 |
| 023 | 023_settings_audit.sql | 配置审计表 |
| 024 | 024_tenant_model_policies.sql | 租户模型策略 |
| 026 | 026_supplemental_rls.sql | 补充 RLS |
| 027 | 027_health_source_fast_reprobe.sql | 健康源快速重探测 |
| 028 | 028_tool_registry_extensions.sql | 工具注册扩展列 |
| 029 | 029_seed_tool_registry.sql | 工具注册种子 |
| 030 | 030_tool_registry_enhancements.sql | 工具注册增强 |
| 031 | 031_provider_settings.sql | 供应商设置 |
| 032 | 032_request_wal.sql | request WAL 表 |
| 033 | 033_credential_model_call_history.sql | 凭据-模型调用历史 |
| 034 | 034_concurrency_limit_auto.sql | 并发限制自动化 |
| 035 | 035_routing_recent_success_rate.sql | 路由近期成功率 |
| 036 | 036_fp_slot_limit.sql | FP slot 上限 |
| 037 | 037_candidate_failure_logs.sql | 候选失败日志表 |
| 038 | 038_adaptive_probe_scheduling.sql | 自适应探测调度 |
| 039 | 039_fp_slot_auto_default.sql | FP slot 默认自动化 |
| 040 | 040_fp_slot_auto_reclaim.sql | FP slot 自动回收 |
| 041 | 041_fp_slot_system_settings.sql | FP slot 系统配置 |
| 042 | 042_tool_calls_column.sql | tool_calls 列 |
| 043 | 043_request_logs_client_model_trgm.sql | client_model trgm 索引 |
| 044 | 044_health_source_probe_now.sql | 健康源立即探测 |
| 045 | 045_model_capability_fields.sql | 模型能力字段 |
| 046 | 046_task_route_tiers.sql | 任务路由分层 |
| 047 | 047_apihub_assets.sql | APIHub 资产表 |
| 048 | 048_apihub_relationships.sql | APIHub 关系表 |
| 049 | 049_armor_judgments.sql | Armor 判定表 |
| 050 | 050_agents.sql | agents 表 |
| 051 | 051_agent_relationships.sql | agent 关系表 |
| 052 | 052_supplemental_rls_round49.sql | 第 49 轮补充 RLS |
| 053 | 053_archive_request_logs_column_aware.sql | 列感知的 request_logs 归档 |
| 054 | 054_request_logs_client_request_id.sql | client_request_id 列 |
| 120 | 120_session_audit.sql | 会话审计表 |

#### 4.1.2 2026-07-13 与 291–419（分区/分析/hot 独立化时代，129 个；含 `up/` 子目录 10 个）

| 版本 | 文件 | 用途 |
| --- | --- | --- |
| — | 2026-07-13-multimodal-token-fields-hot.sql | hot 表多模态 token 字段 |
| 291 | 291_recent_success_rate_time_window.sql | 成功率时间窗 |
| 292 | 292_unavailable_recover_at.sql | 不可用恢复时间列 |
| 300 | 300_candidate_failure_logs_per_attempt_latency.sql | 每次尝试延迟列 |
| 301 | 301_model_probe_suspicious_state.sql | 探测可疑状态 |
| 302 | 302_unified_probe_scheduler.sql | 统一探测调度器 |
| 304 | 304_model_health_dashboard.sql | 模型健康看板 |
| 305 | 305_partition_archive_functions.sql | 分区归档函数 |
| 306 | 306_analysis_events.sql | 分析事件表 |
| 307 | 307_auto_route_notify_manual_disabled.sql | 自动路由通知 |
| 308 | 308_probe_dashboard_state_alignment.sql | 探测看板状态对齐 |
| 309 | 309_intent_aggregates.sql | 意图聚合表 |
| 310 | 310_session_summaries.sql | 会话摘要表 |
| 313 | 313_probe_dashboard_followup.sql | 探测看板跟进修复 |
| 314 | 314_probe_health_comprehensive_fix.sql | 探测健康综合修复 |
| 315 | 315_prompt_injection_detection.sql | 提示注入检测表 |
| 316 | 316_output_compliance_monitoring.sql | 输出合规监控表 |
| 317 | 317_partition_credential_model_index.sql | 凭据模型索引分区 |
| 318 | 318_fix_archive_functions.sql | 归档函数修复 |
| 319 | 319_add_missing_ensure_functions.sql | 补齐 ensure 分区函数 |
| 320 | 320_request_logs_upstream_diagnostics.sql | 上游诊断列 |
| 321 | 321_cleanup_stale_in_progress.sql | 清理过期 in_progress |
| 322 | 322_analysis_events_rls.sql | 分析事件 RLS |
| 323 | 323_intent_aggregates_rls.sql | 意图聚合 RLS |
| 324 | 324_credential_state_log.sql | 凭据状态日志表 |
| 325 | 325_request_attachments.sql | 请求附件表 |
| 326 | 326_fix_routable_view_quota_check.sql | 可路由视图配额修复 |
| 328a | 328a_request_logs_bodies_table.sql | **request_logs_bodies 分区表创建** |
| 329 | 329_create_approval_tables.sql | 审批表组 |
| 330 | 330_usage_ledger_partition.sql | usage_ledger 分区化 |
| 331 | 331_remove_archive_tables.sql | 移除旧归档表 |
| 332 | 332_request_wal_default_partition.sql | WAL 默认分区 |
| 333 | 333_partition_routing_decision_log.sql | 路由决策日志分区 |
| 334 | 334_partition_credit_ledger.sql | 积分账本分区 |
| 335 | 335_partition_tool_usage_stats.sql | 工具用量分区 |
| 337 | 337_detach_current_future_partitions.sql | 分离 current/future 分区 |
| 339 | 339_fix_promote_batch_functions.sql | promote 批处理函数修复 |
| 340 | 340_create_partition_query_views.sql | 分区查询视图 |
| 341 | 341_hot_table_independence.sql | **hot 表独立化（后续漂移根因之一）** |
| 342 | 342_create_other_table_views.sql | 其他表查询视图 |
| 343 | 343_fix_routing_decision_log_columnar.sql | 路由日志 columnar 修复 |
| 344 | 344_usage_ledger_hot_independence.sql | usage_ledger hot 独立 |
| 345 | 345_request_wal_hot_independence.sql | request_wal hot 独立 |
| 346 | 346_routing_decision_log_hot_independence.sql | 路由日志 hot 独立 |
| 347 | 347_credential_model_index_hot_independence.sql | 凭据模型索引 hot 独立 |
| 348 | 348_tool_usage_stats_hot_independence.sql | 工具用量 hot 独立 |
| 349 | 349_credit_ledger_hot_independence.sql | 积分账本 hot 独立 |
| 350 | 350_multimodal_token_fields.sql | 多模态 token 字段（父表） |
| 350 | 350_session_analytics_fix.sql | 会话分析修复 |
| 351 | 351_session_analytics_tables.sql | 会话分析表 |
| 353 | 353_request_logs_bodies_hot_independence.sql | **bodies hot 独立表** |
| 354 | 354_credential_model_index_hot_independence.sql | 凭据模型索引 hot 独立（后续轮） |
| 355 | 355_session_analytics_indexes.sql | 会话分析索引 |
| 356 | 356_session_health_columns.sql | 会话健康列 |
| 357 | 357_session_analytics_aggregation_views.sql | 会话分析聚合视图 |
| 358 | 358_session_ownership.sql | 会话归属 |
| 359 | 359_session_intent_evolution.sql | 会话意图演化 |
| 360 | 360_intent_classifier_config.sql | 意图分类器配置 |
| 361 | 361_intent_analysis_adjustments.sql | 意图分析调整 |
| 362 | 362_intent_classification_feedback.sql | 意图分类反馈 |
| 363 | 363_security_detector_config.sql | 安全检测器配置 |
| 364 | 364_prompt_injection_enhanced.sql | 提示注入增强 |
| 365 | 365_output_compliance_policy_enhance.sql | 输出合规策略增强 |
| 366 | 366_model_name_mapping.sql | 模型名映射表 |
| 367 | 367_auto_route_refresh_trigger_noise.sql | 自动路由刷新触发降噪 |
| 371 | 371_product_modules.sql | 产品模块表 |
| 372 | 372_license_modules.sql | 许可模块表 |
| 373 | 373_vibecoding.sql | vibecoding 模块表 |
| 374 | 374_license_devices.sql | 许可设备表 |
| 375 | 375_fault_management.sql | 故障管理表 |
| 375 | 375_offline_requests_status.sql | 离线请求状态列 |
| 376 | 376_offline_activation_code.sql | 离线激活码表 |
| 376 | up/376_autoupdate.sql | 自动更新表 |
| 376 | up/376_gateway_instances_auth.sql | 网关实例认证 |
| 377 | up/377_center_ops.sql | center 运维表 |
| 377 | up/377_instance_heartbeats_partition.sql | 实例心跳分区 |
| 378 | up/378_add_refresh_token.sql | refresh token 列 |
| 379 | up/379_instance_release_status.sql | 实例发布状态 |
| 382 | 382_session_module_executions.sql | 会话模块执行表 |
| 383 | 383_dashboard_access_events.sql | 看板访问事件表 |
| 385 | 385_session_turn_snapshots.sql | 会话轮次快照表 |
| 386 | 386_model_probe_runs_hot_independence.sql | 探测运行 hot 独立 |
| 387 | 387_session_analysis_inference.sql | 会话分析推理表 |
| 388 | 388_billing_cancellation_audit.sql | 计费取消审计 |
| 388 | 388_task_default_routing.sql | 任务默认路由 |
| 388 | up/388_releases_table.sql | releases 表 |
| 389 | 389_maas_credit_consumption_buckets.sql | MaaS 积分消耗桶 |
| 389 | 389_route_incidents.sql | 路由事件表 |
| 389 | up/389_tenant_ops_scope.sql | 租户运维范围 |
| 390 | 390_routing_audit_log.sql | 路由审计日志 |
| 390 | up/390_gateway_instances_center_agent.sql | 实例 center agent |
| 391 | 391_route_incidents_audit_safety.sql | 路由事件审计安全 |
| 391 | 391_state_table_storage_hardening.sql | 状态表存储加固 |
| 392 | 392_candidate_failure_logs_monthly_partition.sql | 候选失败月分区 |
| 393 | 393_request_logs_hot_partial_index.sql | request_logs_hot 部分索引 |
| 394 | 394_nvidia_nim_outbound_model_id.sql | NIM outbound model id |
| 394 | 394_request_stats_minute.sql | 每分钟统计表 |
| 395 | 395_provider_models_canonical_raw_name.sql | 供应商模型 canonical 名 |
| 395b/c | 395b_dedup_provider_models_and_aliases.sql、395c_canonical_raw_name_unprefix.sql | 模型去重与名称去前缀 |
| 396 | 396_model_aliases_canonical_lowercase.sql | 别名小写化 |
| 397 | 397_runtime_logs_lowercase.sql | 运行日志小写化 |
| 398 | 398_model_offers_add_canonical_raw_name.sql | 模型 offers canonical 列 |
| 399 | 399_fix_columnar_promote_on_conflict.sql | columnar promote 冲突修复 |
| 399 | 399_license_trial_consents.sql | 试用许可同意表 |
| 400 | 400_distribution.sql | 发行版表 |
| 400 | 400_runtime_telemetry_preferences.sql | 运行遥测偏好 |
| 401 | 401_request_attachments_relational.sql | **附件关系化表** |
| 402 | 402_restore_missing_341_objects.sql | 恢复 341 遗漏对象 |
| 402 | 402_runtime_metrics.sql | 运行指标表 |
| 403 | 403_runtime_alert_events.sql | 运行告警事件表 |
| 404 | 404_partition_autovacuum_analyze.sql | 分区 autovacuum 调参 |
| 405 | 405_glm52_promote_per_token_to_token_plan.sql | GLM-5.2 计费计划修正 |
| 406 | 406_recent_success_rate_read_hot.sql | 成功率读 hot |
| 407 | 407_credential_rpm_limit.sql | 凭据 RPM 限制 |
| 407 | 407_download_publish_runs.sql | 下载发布运行表 |
| 407 | 407_request_context_attrs.sql | 请求上下文属性列 |
| up/410 | up/410_ip_blocklist.sql | IP 黑名单表 |
| up/411 | up/411_ops_node_registrations.sql | 运维节点注册表 |
| 412 | 412_rca_ts_index.sql | RCA 时间索引 |
| 413 | 413_runtime_alert_events_ts_index.sql | 告警事件时间索引 |
| 414 | 414_drop_model_probe_runs_old.sql | 删除旧探测表 |
| 415 | 415_restore_node_probe_runs.sql | 恢复节点探测表 |
| 416 | 416_reconcile_node_probe_bindings.sql | 节点探测绑定对账 |
| 417 | 417_route_excludes_failed_node_probes.sql | 路由排除失败节点探测 |
| 418 | 418_rearm_node_probes_after_url_fix.sql | URL 修复后重挂探测 |
| 419 | 419_node_probe_runs_complete_fields.sql | 节点探测补全字段 |

#### 4.1.3 420–606（V2 双写、stats、request-fact 相关，143 个，重点区；含 `up/600`）

| 版本 | 文件 | 用途 |
| --- | --- | --- |
| 420 | 420_request_logs_trace_events.sql | trace_events 列 |
| 421 | 421_task_default_routing.sql | 任务默认路由 |
| 422 | 422_models_canonical_complexity.sql | canonical 复杂度列 |
| 423 | 423_approval_routing_rules_add_legacy_columns.sql | 审批规则 legacy 列 |
| 425 | 425_node_probe_runs_sync_request_trigger.sql | 探测同步触发器 |
| 426 | 426_task_type_centroids.sql | 任务类型质心表 |
| 427 | 427_task_type_centroids_model.sql | 质心模型表 |
| 428 | 428_recent_success_rate_probe_filters.sql | 成功率探测过滤 |
| 430 | 430_sessions_v2_schema.sql | **Sessions V2 四表创建（sessions/session_turns/session_bodies/session_turn_logs）** |
| 431 | 431_session_turns_add_attachment_columns.sql | turns 附件列 |
| 431 | 431_task_default_routing_tenant_code.sql | 默认路由租户码 |
| 432 | 432_fix_submit_mode_constraint.sql | submit_mode 约束修复 |
| 432 | 432_route_incident_events_evidence_jsonb.sql | 事件 evidence jsonb |
| 433 | 433_system_metrics_local_ingest.sql | 系统指标本地摄取 |
| 434 | 434_request_stage_events_table.sql | 请求阶段事件表 |
| 435 | 435_provider_quality_tables.sql | 供应商质量表 |
| 441 | 441_session_state_missing_columns.sql | 会话状态补列 |
| 442 | 442_session_summaries_missing_columns.sql | 会话摘要补列 |
| 443 | 443_observability_fields.sql | 可观测字段 |
| 444 | 444_tenant_credit_wallets_pkey.sql | 租户钱包主键 |
| 445 | 445_routing_persistence_hardening.sql | 路由持久化加固 |
| 446 | 446_volcengine_model_aliases.sql | 火山引擎别名 |
| 447 | 447_volcano_glm_outbound_mapping.sql | 火山 GLM outbound 映射 |
| 448 | 448_request_logs_view_routing_attempts.sql | 视图加路由尝试列 |
| 449 | 449_request_logs_hot_trace_events.sql | hot trace_events |
| 450 | 450_request_stage_events_tenant.sql | 阶段事件租户列 |
| 451 | 451_models_canonical_modality_video.sql | canonical 视频模态 |
| 452 | 452_provider_models_modality.sql | 供应商模型模态 |
| 453 | 453_ursm_v2_node_snapshot_min.sql | URSM 快照 min 列 |
| 454 | 454_response_format_anomalies.sql | 响应格式异常表 |
| 455 | 455_request_id_unique_for_hot_tables.sql | **hot 表 UNIQUE(request_id)（幂等冲突目标）** |
| 456 | 456_session_v2_display_columns.sql | V2 展示列（title/summary/attempt_no/tools） |
| 457 | 457_session_v2_owner_filter.sql | V2 owner 过滤策略 |
| 458 | 458_request_logs_canonical_model.sql | canonical_model 列 |
| 459 | 459_request_logs_view_client_perception.sql | 客户感知视图列 |
| 460 | 460_v_routable_credential_models_periodic_exhausted.sql | 可路由视图修复 |
| 461 | 461_request_wal_hot_unique_request_id.sql | WAL hot 唯一键 |
| 462 | 462_model_integrity_events.sql | 模型完整性事件表 |
| 463 | 463_ursm_v2_snapshot_tenant_identity.sql | URSM 快照租户身份 |
| 464 | 464_session_turns_aggregate_claim.sql | turns 聚合声明列 |
| 465 | 465_session_titles_pkey.sql | 会话标题主键 |
| 466 | 466_relax_compression_parent_check.sql | 放宽压缩父检查 |
| 467 | 467_sessions_title_user_tags.sql | 会话标题/用户标签 |
| 468 | 468_v_suspicious_probe_targets_admin_protected.sql | 可疑探测视图保护 |
| 469 | 469_context_window_override.sql | 上下文窗口覆盖表 |
| 470 | 470_cache_metrics.sql | 缓存指标表 |
| 471 | 471_session_summaries_archival.sql | 会话摘要归档列 |
| 472 | 472_cache_metrics_partitions.sql | 缓存指标分区 |
| 473 | 473_partition_precreate_2026_09_10.sql | 预建 2026-09/10 分区 |
| 474 | 474_session_turns_attachment_indexes_and_constraint.sql | turns 附件索引约束 |
| 475 | 475_restore_missing_ensure_partition_functions.sql | 恢复 ensure 分区函数 |
| 476 | 476_session_v2_tenant_unique_keys.sql | **V2 租户唯一键（tenant 维度幂等）** |
| 477 | 477_auto_route_v6_defaults.sql | 自动路由 v6 默认 |
| 478 | 478_auto_route_affinity.sql | 自动路由亲和 |
| 478 | 478_model_reasoning_caps.sql | 模型推理能力表 |
| 478p2 | 478p2_affinity_indexes.sql | 亲和索引 |
| 479 | 479_concurrency_mode.sql | 并发模式列 |
| 480 | 480_model_iq_cost_calibration.sql | 模型 IQ 成本校准 |
| 481 | 481_request_logs_bodies_precreate_2026_07.sql | bodies 预建 2026-07 分区 |
| 482 | 482_provider_profile_metrics_extended_signals.sql | 供应商画像扩展信号 |
| 483 | 483_session_summaries_add_outcome.sql | 摘要 outcome 列 |
| 484 | 484_request_logs_hot_add_status_code.sql | hot status_code |
| 485 | 485_request_logs_add_raw_model_name.sql | raw_model_name 列 |
| 486 | 486_credential_model_bindings_add_probe_revert_at.sql | 绑定探测回退列 |
| 487 | 487_request_logs_add_system_fingerprint.sql | system_fingerprint 列 |
| 488 | 488_request_logs_hot_add_model.sql | hot model 列 |
| 489 | 489_credential_probe_queue_startup_backfill.sql | 凭据探测队列回填 |
| 490 | 490_credential_probe_queue_runtime_columns.sql | 探测队列运行列 |
| 491 | 491_request_logs_queue_timestamps.sql | **T0–T9 队列时间戳列** |
| 491 | 491_work_type_default_routes.sql | 工作类型默认路由 |
| 510 | 510_request_type.sql | request_type 列 |
| 511 | 511_state_transitions_table.sql | 状态迁移表 |
| 513 | 513_schema_unification_and_session_turns_dual_write.sql | **V3.2 统一 + session_turns 双写 T0–T9** |
| 514 | 514_model_probe_watchdog_index.sql | 探测 watchdog 索引 |
| 515 | 515_state_transitions_seq_unique.sql | 状态迁移序号唯一 |
| 516 | 516_durable_llm_tasks.sql | **durable_llm_tasks（跨重启执行恢复源）** |
| 517 | 517_handoff_pending_confirmations.sql | handoff 待确认表 |
| 518 | 518_handoff_pending_proposal_created_at_index.sql | 提案时间索引 |
| 519 | 519_session_title_summary_model_routes.sql | 标题摘要模型路由 |
| 520 | 520_durable_task_settlement_intents.sql | durable 结算意图表 |
| 521 | 521_repair_state_transitions_tenant.sql | 状态迁移租户修复 |
| 522 | 522_request_state_transitions_tenant_contract.sql | 状态迁移租户契约 |
| 523 | 523_credential_model_context_window_override.sql | 凭据窗口覆盖 |
| 524 | 524_cmb_notify_trigger_context_window.sql | CMB 通知触发器 |
| 525 | 525_session_turns_six_dim.sql | **turns 六维列（project/namespace/parent/task_type）** |
| 526 | 526_session_turns_hot.sql | **session_turns_hot 独立表 + 原子 promote + union 视图** |
| 527 | 527_handoff_durable_goal_state.sql | handoff 目标态 |
| 528 | 528_request_logs_bodies_expired_hot_cleanup.sql | bodies 过期清理函数 |
| 529 | 529_repair_shared_pg_sticky_and_bodies_2026_07.sql | 252 库修复（checksum 冻结） |
| 530 | 530_request_journey_contract.sql | 请求旅程契约表 |
| 531 | 531_request_journey_tenant_uniqueness.sql | 旅程租户唯一 |
| 532 | 532_request_logs_final_success.sql | 终态 success 语义修复 |
| 533 | 533_request_wal_bodies_unique_request_id.sql | WAL bodies 唯一键 |
| 534 | 534_handoff_logs_hot_columnar.sql | handoff 日志 columnar |
| 535 | 535_candidate_failure_logs_atomic_promote.sql | 原子 promote 修复 |
| 536 | 536_stats_analytics_foundation.sql | **stats 事件 inbox/dedup/rollup/reconciliation 基础** |
| 537 | 537_usage_facts.sql | **usage_facts 事实表** |
| 538 | 538_node_probe_runs_trigger_kind_unified_queue.sql | 探测触发统一队列 |
| 539 | 539_stats_reconciliation_tenant.sql | 对账租户口径 |
| 540 | 540_stats_event_inbox_consumer.sql | inbox 消费者 schema |
| 541 | 541_candidate_binding_scope_revision.sql | 候选绑定 scope 修订 |
| 542 | 542_request_logs_token_band.sql | token band 列 |
| 543 | 543_request_logs_discard_events.sql | 丢弃审计事件列 |
| 544 | 544_stats_adjustments_alignment.sql | 调整对齐 |
| 545 | 545_stats_reconciliation_phantom_resolution.sql | 幻影解析 |
| 546 | 546_stats_reconciliation_diffs_unique.sql | diff 唯一索引 |
| 547 | 547_session_project_attribution.sql | 会话项目归因 |
| 548 | 548_stats_reconciliation_diffs_identity.sql | diff 身份唯一 |
| 549 | 549_goal_run_ledger.sql | 目标运行账本 |
| 550 | 550_session_title_states_expand.sql | 标题状态扩展 |
| 551 | 551_session_title_states_indexes.sql | 标题状态索引 |
| — | （另一个历史 551 approval_resume_claim 已在 merge 审计中弃用，仅留 `docs/archive/merge-audit/` 副本，见 installer main.go MERGE-AUDIT 注释） | — |
| 552 | 552_request_journey_durable_outbox.sql | **request_journey 观测 outbox** |
| 553 | 553_approval_resume_claim.sql | 审批恢复认领 |
| 554 | 554_goal_runs.sql | goal_runs 表 |
| 555 | 555_goal_run_actions_lease_fencing.sql | 动作 lease/fencing |
| 560 | 560_session_summaries_tenant_uniqueness.sql | 摘要租户唯一 |
| 561 | 561_request_logs_view_origin_actor.sql | 视图 origin actor |
| 562 | 562_fix_request_logs_bodies_partitions_heap.sql | bodies 分区 heap 修复 |
| 563 | 563_session_summary_trigger_on_hot.sql | hot 摘要触发器 |
| 564 | 564_session_summary_backfill_safe.sql | 摘要安全回填 |
| 565 | 565_cost_usd_pricing_backfill.sql | 成本定价回填 |
| 566 | 566_credentials_governor_revision.sql | 凭据治理修订 |
| 567 | 567_session_analysis_metadata.sql | 会话分析元数据 |
| 568 | 568_credential_priority_flag.sql | 凭据优先级 |
| 569 | 569_candidate_binding_scope_revision_canonical.sql | scope 修订 canonical |
| 570 | 570_model_offers_insert_priority_passthrough.sql | offers 插入优先级 |
| 571 | 571_candidate_binding_scope_revision_canonical_priority_hash.sql | priority hash |
| 572 | 572_session_summary_large_token_ratio.sql | 大 token 比率列 |
| 573 | 573_drop_request_logs_body_columns.sql | **主表 DROP 正文三列（bodies 表成唯一 SSOT）** |
| 574/576 | 574_request_logs_customer_id_bigint.sql、576_request_logs_customer_id_bigint.sql | customer_id bigint 化 |
| 575/577 | 575_request_logs_view_customer_id.sql、577_request_logs_view_customer_id.sql | 视图 customer_id |
| 578 | 578_candidate_binding_scope_filter_disabled.sql | scope 过滤禁用 |
| 579 | 579_dashboard_access_events_hot_promote.sql | 看板事件 hot promote |
| 580 | 580_session_module_executions_hot_promote.sql | 模块执行 hot promote |
| 600 | up/600_outbound_body_to_bodies_hot.sql | **outbound_body 迁往 bodies_hot（含给 bodies_hot 加 tenant_id）** |
| 601 | 601_request_logs_bodies_drop_metadata.sql | **DROP bodies_hot.tenant_id** |
| 602 | 602_request_logs_promote_atomic.sql | request_logs 原子 promote |
| 603 | 603_repair_request_logs_schema_consistency.sql | request_logs/hot schema 一致性修复（**待审核，禁止执行**） |
| 604 | 604_repair_request_logs_bodies_tenant_id.sql | DROP bodies 父表 tenant_id（**待审核，禁止执行**） |
| 605 | 605_fix_tool_calls_index_predicate.sql | tool_calls 部分索引谓词修复（**待审核，禁止执行**） |
| 606 | 606_session_summaries_agent_expert_tags.sql | session_summaries 三新列（**待审核，禁止执行**） |

> 版本号历史碰撞（350/360/361/388/389/391/394/400/402/407/431/432/478 等）是已审计的历史事实；`sql/migrations/startup/migration_version_unique_test.go` 强制 492 之后版本号唯一。

#### 4.1.4 跳过/禁用文件（在目录树中但不执行）

`318b_request_logs_archive_heap.sql.skip`、`336_promote_default_to_partition_functions.sql.skip`（及 .bak.skip、down.skip）、`338_fix_routing_decision_log_default_heap.sql.skip`、`341_hot_table_independence.fix.sql.disabled`、`350_request_logs_bodies_hot_independence.sql.skip`、`360_session_module_executions.sql.disabled`、`361_dashboard_access_events.sql.disabled`。

### 4.2 domain scope 完整清单（`sql/migrations/domain/`，48 个 up 文件）

| 版本 | 文件 | 用途 |
| --- | --- | --- |
| 032–035 | session_tenant_binding / bandit_scoring / session_reuse_idx / credential_state_management | 会话租户绑定、bandit 打分、复用索引、凭据状态管理 |
| 130–135 | task_management / credential_plan_type / client_profile / provider_reputation / tool_execution / approval_routing | 任务、凭据套餐、客户端画像、供应商信誉、工具执行、审批路由 |
| 220 | feishu_bot_routing | 飞书机器人路由 |
| 327–328 | credential_plan_type_full / view_add_provider_filter | 套餐全量、视图供应商过滤 |
| 329–331 | model_probe_state_canonicalize / hot_partition_union_views / session_state_and_rotations | 探测状态规范化、hot union 视图、会话状态轮换 |
| 332–335 | credential_view_* / models_canonical_family_fix / cmb_billing_align / remove_plan_billing_check | 凭据视图、canonical 家族、计费对齐 |
| 336–339 | deduplicate_provider_models / routing_health_checks / self_check / self_check_error_types | 模型去重、路由健康、自检 |
| 341/341b | probe_origin_and_node_probe / fix_missing_credential_most_used_model | 探测来源与节点探测 |
| 342–347 | routing_state_capability_foundation / credential_probe_queue / system_probe_runs / self_check_monitor_concurrency / system_probe_run_tokens / probe_queue_lease | 路由状态能力、探测队列、系统探测、并发、token、lease |
| 348–351 | integrity_fingerprint_baseline / integrity_probe_queue_source / model_iq / probe_revert_at | 完整性指纹、探测源、Model IQ、回退 |
| 352–356 | add_grok_4_6 / update_xai_provider / add_latest_models_glm_kimi / add_gemini_3_series / update_provider_catalogs_latest_models | 新模型目录数据 |
| 357–363 | repair_model_aliases_unique / fix_kimi_k3_modality / canonical_metadata_sync / aliases_vendor_prefix / standard_provider_models / standard_binding_placeholders / featured_models_standard | 别名修复、元数据同步、标准模型/绑定 |

### 4.3 其他迁移目录

| 目录 | 内容 | 注册状态 |
| --- | --- | --- |
| `db/migrations/`（12 个文件） | 历史迁移（014/352–363 等），由 `db/db.go` ensure*Schema 函数在进程内镜像应用 | 进程内通道，不走账本 |
| `deploy/sql/migrations/V352__system_monitor_fallback_queue.sql` | SystemMonitor fallback 队列 | 人工/部署脚本 |
| `sql/migrations/manual/`（2 个） | 20260719 火山 GLM-5.2 手工 SQL | 人工 |
| `sql/migrations/operations/` | 2026-08-19 伪成功清理等运维 SQL + 一批与 db/migrations 重叠的历史文件 | 人工 |
| `sql/migrations/timeout-optimization/`、`sql/migrations/test/` | 专项与测试 | 非生产 |
| `sql/migrations/ursm`（根目录 080–083） | URSM key 迁移账本 | strict 脚本 ursm scope |

### 4.4 installer embed 注册清单（通道 2，36 个）

`installer/cmd/llm-gw-installer/main.go` go:embed 显式注册（与 `embeddata/startup/` 一一对应）：

511、515、521、530、531、536、537、539、540、544、545、546、547、548、552、553、554、555、560、561、562、563、564、565、566、567、568、569、570、571、600、601、602（共 33 个 up）+ 544/545/546/547/548/571 的 .down.sql（6 个）。基线 `embeddata/01-schema.sql` 为全量 schema dump（其内含的 `request_logs.request_body/response_body/outbound_body` 列与 573 后的 strict 通道库结构不一致，见 §4.7-R3）。

### 4.5 注册核对矩阵（重点版本段）

| 版本 | startup 树 | strict 脚本会应用 | installer embed | 备注 |
| --- | --- | --- | --- | --- |
| 511–548（列表内 30 个） | 有 | 是 | 是（30 个全注册） | stats/journey/handoff 基础 |
| 549–555、560–572（20 个） | 有 | 是 | 仅 552–555、560–571（12 个） | 549/550/572 未 embed（功能列，依赖进程内通道或 strict） |
| 573–580（12 个） | 有 | 是 | **否** | 573（DROP 正文列）未 embed：installer 全新库与 strict 库 schema 分叉 |
| 600–602 | 有 | 是 | 是 | bodies tenant_id 加/删序列（见 §4.7-R1） |
| 603 | 有 | **是（strict 脚本无白名单，放入即执行）** | **否** | 契约文档明示 pending preflight |
| 604 | 有 | **是（同上）** | **否** | 同上 |
| 605 | 有 | **是（同上）** | **否** | 2026-08-26 新增 |
| 606 | 有 | **是（同上）** | **否** | 2026-08-26/27 新增 |

只读核对 SQL（在目标库执行，验证账本与上表一致）：

```sql
-- 目的：列出目标库已登记的 startup 迁移，与 §4.1 清单和 §4.4 embed 清单三方核对。
-- 漂移判定：账本中存在而 §4.1 没有的版本（人工 SQL 未记账）；§4.1 有、账本无且非 embed 通道的版本（漏应用）。
SELECT version, migration_name, checksum, applied_at
FROM public.repository_schema_migrations
WHERE scope = 'startup'
ORDER BY applied_at DESC, version;
```

### 4.6 待审核，禁止执行（强制结论）

按 `docs/standards/database-change-and-real-verification.md` 与 Phase 0 契约（Non-Goals and Migration Inventory 节）：

- **603_repair_request_logs_schema_consistency**：**待审核，禁止执行**。存在于 startup 树但未在 installer/deploy 注册；契约文档明确 Phase 0 不执行不注册。依赖 573 在目标库成功执行，需先完成真实环境 preflight（影响分析、锁风险、视图核对、回滚演练）。
- **604_repair_request_logs_bodies_tenant_id**：**待审核，禁止执行**。同上；且与 §4.7-R1 的代码矛盾未解决前执行会直接与运行代码冲突。
- **605_fix_tool_calls_index_predicate**：**待审核，禁止执行**。未在 installer 注册；属修复类（JSON 标量触发索引谓词失败），注册前需按标准完成全量验证。
- **606_session_summaries_agent_expert_tags**：**待审核，禁止执行**。未在 installer 注册；属功能新增列，需与写入方代码同变更集提交。

本文档不提供上述四个迁移的任何执行步骤。它们的正式注册、应用顺序与真实环境验证窗口属于总方案 §8 实施前置决策第 7 条，需单独评审。

### 4.7 本次核对发现的 schema/code 漂移风险（只记录，不修复）

- **R1（高）`request_logs_bodies_hot.tenant_id` 三方矛盾**：迁移 600（2026-08-24，已注册）为该列添加并回填；迁移 601（2026-08-25，已注册）DROP 该列；迁移 604（未注册）DROP 父表同列；而当前 HEAD 代码 `domains/hooks/observability/telemetry/client.go:2180-2183`（2026-08-26 merge d2cbaf88b 自 origin/main 引入）仍向该列 INSERT/UPDATE。若目标库已应用 601 而未回滚，主写路径 `upsertRequestLogBodies` 将持续失败（列不存在）并连坐 V1 主事务（F1/F2）。此为总方案 §4「schema/部署契约漂移」的现存实例，必须在实际对账执行前用 `information_schema.columns` 实测确认（§1.4 第 1 条）。
- **R2（中）strict 通道与 installer 通道 schema 分叉**：573（DROP 主表正文列）未注册进 installer，两条安装路径产出的 `request_logs` 列集不同；embed 基线中的 `request_logs_bodies_progress` 视图引用主表正文列，在 573 库上不可查。
- **R3（中）603 描述的 hot/parent 列漂移尚未修复**：603 头注释记录了 25 处差异（10 列缺失 + 类型漂移 + 默认值漂移），在 603 通过 preflight 前，任何对 hot/parent 做显式列清单读写的运维 SQL 都必须以实测 schema 为准。
- **R4（低）`deploy/DEPLOYMENT_CHECKLIST.md` 仍引用旧 `schema_migrations` 表与手工 `\i migrations/...` 流程**，与现行 `repository_schema_migrations` 账本和 strict 脚本不一致，易误导操作者绕过账本。

---

## 5. 与总方案验收标准的对应

- 本文档 §1 满足 Phase 0 第 4 条「V1/V2/body/stats drift SQL」；§2 覆盖「指标」并标注缺口；§3 覆盖「failure matrix」；§4 覆盖「migration/installer/deploy manifest inventory」，603/604 等未注册迁移仅列为待审核（§4.6）。
- 本文档不改变任何线上行为、不执行任何 SQL/迁移/git 写操作，符合 Phase 0 Non-Goals。
