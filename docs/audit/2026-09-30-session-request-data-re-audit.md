# 会话请求数据复审 —— request_logs 退役路径现状与本轮修正

> 日期：2026-09-30（§5.6 补于 2026-10-01）
> 触发：目标「request_logs 及相关的表要删除，只使用 session_* 相关的表，请再次审计」
> 规划锚点：`docs/storage/2026-09-20-session-storage-decoupling-plan.md`（解耦 v3，S1~S6 退役门）
> 证据基线：本地真库 `llm-gateway-pg` / `llm_gateway`；
> main @ `630a40dec`（正文会话域部分），§5.6 复核时 main 已推进到 `fc10b91c5`
> （迁移 765，bodies 列存化 —— 正是它把 fallback 查询从 5.3 秒放大到 40 秒）

---

## 1. 结论先行

> ### ✅ 阻断项已解除：历史欠账已回填，`genuine_loss` 归零
>
> **2026-10-01 复核（跨越 765 迁移 + 1,459 行回填 + 时间推移后）仍然成立**：
> 35 天窗口 `genuine_loss` = **0**，最近 24h = **0**，`session_mirror_outbox` **0 行**，
> 视图 `request_id` 重复数 **0**。且 v1 侧缺失的 641,738 个 `request_id`
> 被**完全解释**（603,509 无会话 + 38,229 按设计排除，两条独立 SQL 互相对上，
> 详见 §5.7）。**数据存储可用性成立。**
>
> **同日另修掉三个「端点本来就是坏的」级缺陷**（§5.8）：`/api/admin/session-export`
> 及其取证导出对上的 `''::jsonb` 让整条 SQL 在**解析期**失败（已死约 12 周），
> `quality-correlations` 的 `images`/`code_block` 分桶同病，
> `language` 分桶的正则写法在本库（PG 17.10）上直接报 `2201B` 且被 `continue`
> 静默吞掉。**共同根因是 mock 不解析 SQL**，既有测试全测的是纯函数与鉴权。
> 现已抽出具名 SQL 函数 + 真库执行门，两个历史 bug 放回去都能当场转红。
>
> 真库核对（`llm_gateway`，2026-09-30）发现，在
> `sessions_v2.enabled=true`、`sessions_v2.shadow_write=true` 的前提下，
> 有大量会话只存在于 `request_logs`、从未进入 session 族。
>
> **初版把它当成「仍在持续漏写的活 bug」，这个判断是错的**，同日被数据推翻；
> 随后又按「pre-712 内存 backlog 丢失」定位并**实际回填了欠账**（见 §5.4）。
>
> 订正后的构成（35 天窗口，订正后的分类口径）：
>
> | 构成 | 回填前 | **回填后（现状）** | 判定 |
> |------|-------|------------------|------|
> | 网关内部回环（标题/摘要生成器） | 36,693 | 36,693 | **按设计排除**（`hook.go:102` `IsInternalAutoEntry`）—— 这些会话只含网关自己的元数据，没有任何用户轮次 |
> | 非终态占位行 | 1,536 | 1,536 | **按设计排除**（`hook.go:71`：镜像占位行会永久记成 `success=false/500`） |
> | **`genuine_loss`** | **1,459** | **0** | **已全部补写入 `session_turns`** |
>
> `genuine_loss` 按迁移 712（durable outbox，`2026-09-15 03:18:02+08`）切分：
>
> | 区间 | v1 行数 | `genuine_loss` | 占比 |
> |------|---------|---------------|------|
> | 712 之前 | — | ~1,341 | — |
> | 712 之后 | 684,305 | 118 | 0.017% |
> | 当前进程启动后（9.5h） | 1,222 | 0 | 0% |
> | **回填后（全窗口）** | — | **0** | **0%** |
>
> 根因见 §5.3.1：**712 之前镜像写失败只落进程内存 backlog，重启即丢**。
> 712 之后该通道关闭，当前进程 9.5 小时、1,222 条终态行**零漏写**。
>
> **数据一致性已验证**（回填后实测）：
> - 视图 `request_logs_with_current_month` 按 `request_id` **零重复**；
> - 视图总行数 2,321,329 = 643,377（仅 v1）+ 1,677,952（session 族），
>   与两侧基数精确对账，**无丢行**；
> - 回填行的 `session_turns.ts` 与 v1 `ts` **delta = 0.000 秒**（逐字节保真）。
>
> **对 S4 的结论（订正）**：
> - **S4 停写不再被「活跃漏写」阻断** —— 门控判据
>   `GET /api/admin/sessions/dual-read-drift` 的 `s4_ready` 在当前窗口已满足。
> - 但那 **1,459 行历史欠账永远进不了 `session_turns`**（payload 当时只在内存里）。
>   所以 **734 视图的 v1 冻结分支必须保留到 S5 TTL 燃尽把存量清掉为止**，
>   这也正是本轮把 `session_online.go` / `session_compare.go` /
>   `session_summary_v2.go` 三处原生源迁移**回退**的原因（详见 §5.1.1）。
> - **该欠账已于本轮清零**（§5.4）：扩口径后的
>   `scripts/audit/mirror_outbox_backfill.sql` 把 1,459 行全部补写入
>   `session_turns`，`genuine_loss` 归零，数据一致性实测通过。
>   ⇒ **那 3 处原生源迁移现在具备重新启用的数据前提**，见 §7。

>
> 另：`admin/logs_turns_source.go` 原注释「本机镜像链 2026-09 起全量双写，
> 观察期零漂移」与实测矛盾，本轮已订正为警告。

原计划状态：

| 阶段 | 计划内容 | 现状 | 证据 |
|------|---------|------|------|
| S1/S2 | 宽表 + 拼装视图 + 镜像 outbox | 已完成 | migration 706/707/710/712/713 |
| S3 | 读端分波去视图化 | **会话域为 0**；镜像已补齐，**前置条件现已满足**，可重启 | 见上 + §5.4 |
| S4 | 停写门控 `storage.request_logs_write_enabled` | 写端已落地，**阻断已解除，可开灰度** | 四路同门；`genuine_loss = 0` |
| S5 | TTL 燃尽 | 未开始 | — |
| S6 | DROP 表族 | 未开始 | — |

**最关键的一条**：灰度开关 `storage.admin_logs_native_turns_read`（默认 false）只服务 `admin/logs.go` 两处，而那两处是**通用日志列表/详情，不是会话域**。
→ **22 个会话域重点文件中，受该开关保护的读路径数量为 0。** 会话域全部硬编码表名。

---

## 2. 判据：直读物理表 vs 走视图

这两种形态后果完全不同，必须分开判：

- **直读物理表**（`FROM request_logs` / `request_logs_hot`）→ S4 停写后新数据恒空，**真故障**。
- **走视图**（`request_logs_with_current_month`）→ 734 视图体本身是
  `session_turns_hot ∪ session_turns ∪ (v1 冻结分支 NOT EXISTS 反连接)`，
  session 分支已拼装在内，S4 停写后**仍供数**，属可降级。

真库实测视图体确认（`sql/migrations/startup/734_request_logs_view_details_join.sql:190-223`）。

---

## 3. 读路径清点结果

### P0 —— S4 停写后静默返回错/空数据（9 文件）

| 文件:行 | 表 | 停写后后果 |
|---------|----|-----------|
| `admin/session_tenant.go:57,60` | `request_logs_hot ∪ request_logs` | **跨租户访问门恒 false → 全体租户管理员会话详情 404**。这是权限门不是数据门，运维会误判为租户配置问题 |
| `admin/session_sanitize_matches.go:157` | `request_logs` | 租户解析落空 → 回退 `callerTenant`，跨租户拒绝分支永不触发 |
| `admin/session_extract.go:282,304,324` | `request_logs` | api_key_id→0、tenant_id→`""`（触发 Memora legacy 单租户布局）、预览空 |
| `admin/session_analytics_timeseries.go:180,250,321` | `request_logs` | `fillMissing*` 把停写**伪装成零流量**，趋势图照常渲染 |
| `admin/session_analytics_handler.go:459` | `request_logs` | 详情 timeline 空，但 summary 仍返回 → 同一响应自相矛盾 |
| `admin/session_panorama_handler.go:175` | `request_logs` | 同上，且是上一条的重复实现 |
| `admin/session_detail_v2.go:554,557` | `request_logs_hot ∪ request_logs` | 旧 `gw_session_id` 客户端整体 404 |
| `admin/session_catalog_usage.go:39,44` | `request_logs_hot ∪ request_logs` | 聚合恒 0；`overlayCatalogUsage` 只补 0 值 → **显示对错取决于 Redis 缓存是否命中** |
| `admin/session_management_api.go:293` | `request_logs_hot`（单腿） | Requests 列表静默为空，接口仍 200 |

### P1 —— 走视图，S4 可扛、S6 崩塌

`session_export.go` / `session_title.go` / `session_analytics_breakdown.go` / `session_online.go` /
`session_summary_v2.go` / `session_compare.go` / `no_topic_session.go` / `compression_sessions.go` /
`session_list.go` / `turns_sessions.go` / `session_turns_tree.go` / `session_turns_unified.go` / `unified_detail.go`

S4 停写后 session 分支供数，但 S6 DROP 视图时**同时失效且无开关可回切**。
正文（`request_logs_bodies`）在 S4 后新请求无正文，**S6 后旧行也无等价物** —— 这是唯一必须在 TTL 燃尽前做完决策的一项。

### P0 组里最隐蔽的一条

`session_analytics_timeseries.go`（物理表）与 `session_analytics_breakdown.go`（视图）同属
`/session-analytics` 命名空间，形态却不同。S4 后 breakdown 正常、timeseries 归零，
**同一页面会呈现互相矛盾的面板**。这类「一半对一半错」比全错更难定位。

---

## 4. 本轮已修正

| 项 | 改法 | 状态 |
|----|------|------|
| `session_catalog_usage.go` | 用量聚合改读 `session_turns_hot ∪ session_turns`，键 `session_id` 命中 `idx_session_turns_session` | 已改 + 真库实测 41ms |
| `session_catalog_usage_test.go` | 守卫**方向翻转**：原断言「必须读 request_logs_hot」是反向护栏，会主动阻止迁移 | 已改 + 变异验证有判别力 |
| `session_sanitize_matches.go` | 租户解析先查 session 族，落空回落 v1 | 已改 |
| `session_tenant.go` | **跨租户权限门**：session 族与 v1 族并联 EXISTS，停写前后都至少一条腿供数 | 已改 + 真库 EXPLAIN 验证 |
| `session_extract.go` ×3 | 三处直读物理表 → 走 734 视图 | 已改 |
| `session_export.go` | **既存故障**：`rl.role` 不在视图 115 列契约内，该查询当前即 42703，导出接口整条失败 | 已改 + 真库复现与验证 |

### 4.0 跨租户权限门（`assertTaskInTenant`）为什么排最前

它不是数据问题，是**权限判定问题**。原实现只查 `request_logs_hot ∪ request_logs`，
S4 停写后对所有 task 恒返回 false → `assertSessionAccess` 写 404 →
**全体租户管理员无法访问任何会话详情**。语义本该是「阻断越权」，实际变成「阻断所有人」，
且运维会往租户/权限配置方向排查，不会想到是存储停写。

修法是让两侧并联，任一来源能证明归属即放行：

| 腿 | 索引情况 | 本机规模 |
|----|---------|---------|
| `session_summaries` | **部分索引** `idx_session_summaries_task (tenant_id, gw_task_id) WHERE gw_task_id IS NOT NULL` | 33.3 万行中 1576 行有 task |
| `session_turn_details_hot` | 无 gw_task_id 索引，但表小，顺序扫描可接受 | 1083 行 |
| `request_logs_hot` ∪ `request_logs`（保留） | 原有双腿 | 镜像链启用前历史窗口 |

真库 `EXPLAIN ANALYZE` 实证：命中 `Index Only Scan using idx_session_summaries_task`，
后续三条腿 `never executed`（OR-of-EXISTS 短路），`Buffers: shared hit=3`。

**已知边界**：`session_turn_details` 月分区母表约 167 万行且无 gw_task_id 索引
（733 只建 request/session/ts 三条），故本函数**不含母表腿**。补齐需新增迁移
（部分索引 + 五点同步门禁），登记为 S4 前置项。


### 4.1 守卫方向翻转（值得单独记）

`session_catalog_usage_test.go` 原断言：

```go
if !strings.Contains(catalogUsageSQL, "request_logs_hot") || !strings.Contains(catalogUsageSQL, "FROM request_logs") {
    t.Fatal("usage SQL must read hot and partitioned request_logs")
}
```

这不是「忘了更新」，是**方向相反的错误护栏** —— 它把 S4 退役表钉成硬依赖。
`admin/session_title_test.go:58` 同型（硬编码 `FROM request_logs rl`）。
迁移时若只做字符串替换而不重新设计断言意图，会把退役依赖永久固化。

新守卫改为断言「读 session 族 + 不含 request_logs + 保留 hot/parent 双腿」，
并做了变异验证（改回 request_logs → 测试转红），确认有判别力而非恒绿。

### 4.2 三个「端点本来就是坏的」级发现（与 S4 无关）

迁移 `session_analytics_timeseries.go` 时真库核验发现的既存故障，都不是 S4 引起的：

| 位置 | 事实 | 后果 |
|------|------|------|
| `appendTimeseriesFilters` 三处调用 | 全部传 `alias = ""` | 拼出 `AND .tenant_id = $4` 这类无限定名谓词 → **只要带租户/模型/供应商过滤就是语法错误，端点 500** |
| provider 过滤谓词 `%s.provider` | `request_logs` 物理表**没有 `provider` 列**（只有 `provider_id`），115 列契约同样没有 | 该过滤从未生效过。叠加上面的空 alias，是双重损坏 |
| `HandleCostTrend` 的 `input_cost_usd` / `output_cost_usd` / `cache_creation_tokens` | 三个列在 `request_logs` **和**视图契约上**都不存在** | **成本趋势端点每次调用必然 42703**，此前是坏的，不存在可回退的历史行为 |

第三条尤其重要：它意味着「保持现状」不是一个可选项——现状本来就是坏的。
`input_cost_usd` / `output_cost_usd` 在 session 族契约内无等价列，且 `CostDataPoint`
无 `omitempty`（删字段会改 JSON 形状），故显式置 0 并在代码注释登记；
`total_cost_usd` 与 cache token 仍是真值。

---

### 4.3 第二批修正（P0 收尾）

| 文件 | 改法 |
|------|------|
| `admin/session_timeline_query.go`（新增） | 把 `session_analytics_handler.go` 与 `session_panorama_handler.go` 里**逐列相同的两份 timeline SQL** 收敛成一个 `loadSessionTimelineInTx` |
| `admin/session_panorama_handler.go` | 改调共用 helper |
| `admin/session_analytics_handler.go` | 改调共用 helper |
| `admin/session_management_api.go` | 单腿 `request_logs_hot` → 734 视图（顺带补齐「只见 7 天热窗」的历史遗漏），`EXISTS` 子查询的表限定名同步改为 `rl` |
| `admin/session_detail_v2.go` | `resolveSessionID` 反向臂加 session 族两条腿（`session_turns_hot` ∪ `session_turns`），v1 腿保留，外层 `primary_request_id` 匹配语义不动 |
| `admin/session_analytics_timeseries.go` | 三条趋势查询切 734 视图 + 列名对齐 breakdown；修空 alias；provider 改按 `provider_id` |

**空/空切片契约差异**：两个 timeline 调用点历史上对空结果的 JSON 形态并不一致
（analytics 序列化成 `null`，panorama 序列化成 `[]`）。共用 helper 统一返回 `nil`，
由 panorama 调用点自行还原成 `[]` —— 不这样做就会悄悄改掉其中一条 API 的响应契约。

**P0 已全部清零**。`internal/sqlreadguard` 的 self-cleaning 守卫在本轮四次要求清除
白名单条目（`session_extract.go`、`session_panorama_handler.go`、
`session_analytics_handler.go`、`session_analytics_timeseries.go`），全部已移除 ——
这个守卫反向证明了这四处裸读确实消失了。


### 4.4 `role` 列：真库实证的既存故障

`session_export.go:211` 原本 `SELECT rl.id, rl.role, ...`。真库核验：

```
information_schema 查 role 列 → 0 命中
canonicalColumnOrderV2 → 无 role
SELECT rl.role FROM request_logs_with_current_month
  → ERROR: column rl.role does not exist
```

与 S4 无关，**导出接口当下就是坏的**。改用与 `loadSessionPreviewTurns` 相同的
`work_type`/`request_mode` 规则推导方向，`ExportMessage.role` 的 JSON 契约不变。

---

## 5. 真库验证记录

| 验证项 | 结果 |
|--------|------|
| `session_turns_hot` 8 列（session_id/parent_request_id/prompt/completion/cost_usd/model/tenant_id/ts） | 8/8 存在 |
| `session_turns` 同 8 列 | 存在 |
| `session_turn_details_hot.gw_task_id` | 存在 |
| 新 `catalogUsageSQL` 实跑 2 个真实会话 | 41.4ms，返回 turns/prompt/completion/cost/model |
| hot ∪ parent 是否双持同一 `request_id` | **overlap=0**（promote 是搬运不是复制，UNION ALL 安全） |
| `request_logs_with_current_month` 是否有 `role` | **0**（既存故障坐实） |
| `session_summaries` 是否有 `(tenant_id, gw_task_id)` 索引 | **有**，部分索引，33.3 万行中 1576 行有 task |
| `session_turn_details_hot` / 母表行数 | 1083 / 1,677,243（母表不能裸扫） |
| 新 `assertTaskInTenant` EXPLAIN ANALYZE | `Index Only Scan using idx_session_summaries_task`，其余腿 `never executed`，`Buffers: shared hit=3` |

---

## 5.1 S6 铺路第一步：会话域原生源，以及「不要照搬 wave-1 样板」

**实测结论先说**：S3 wave-1 的 `db.SessionFamilyTurnsSourceSQL()` **不能**用在会话域读路径上。

原因是列契约：`gw_session_id` 在视图和原生源里都是表达式
`(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)`。
按投影名过滤就用不上 `idx_session_turns_session (session_id, turn_no DESC)`。

同一会话（`gw_hz-drill-sess-1`，21 行）本地 `EXPLAIN ANALYZE`：

| 方案 | Execution | Buffers | 走索引 |
|------|-----------|---------|--------|
| 视图 `request_logs_with_current_month` | **188.5ms** | 10204 | 否（`Seq Scan on session_turns_hot` + v1 分支 `NOT EXISTS` 反连接） |
| `SessionFamilyTurnsSourceSQL()`（wave-1 原生源） | 同样退化 | — | 否（表达式过滤） |
| **新增 `SessionFamilyTurnsForSessionSQL()`** | **1.9ms** | ~20 | **是**（`Index Only Scan ... session_id_turn_no_idx`） |

`db/request_logs_view_schema.go` 新增的 `SessionFamilyTurnsForSessionSQL()` 与 wave-1
同源同契约，唯一差别是**把 session 谓词下推进两条腿**：

```sql
... FROM public.session_turns_hot t LEFT JOIN ... WHERE t.session_id = $1
UNION ALL
... FROM public.session_turns t LEFT JOIN ... WHERE t.session_id = $1
```

调用方契约：session id 必须绑在 `$1`，**外层不能再加 `gw_session_id` 谓词**
（那会退回表达式过滤）；租户过滤仍走外层 `rl.tenant_id = $2`。

首个迁移点：`admin/session_online.go` 的 `querySessionTimeline`
（`GET /api/admin/sessions/{id}/timeline`），同步更新了
`session_online_timeline_test.go` 的三条 SQL 断言（保留原「hot ∪ promoted 双腿 +
投影 is_final_success」意图，只把锚点从视图名换成下推谓词）。

**第三个与第四个迁移点**（均带 bodies 联接，故另测同口径）：

| 端点 | 视图（Planning+Execution） | 原生下推（Planning+Execution） | 倍数 |
|------|---------------------------|-----------------------------|------|
| `session_compare.go:185` 对比详情（含 bodies） | 344ms + **3978ms** | 311ms + **8.7ms** | **~13x** |
| `session_summary_v2.go:284` 总结 fallback（含 bodies） | 500ms + **14000ms** | 385ms + **5308ms** | **~2.5x** |

总结 fallback 两者都慢（5–14 秒）：它把 `request_body`/`response_body` 两个大
JSONB 拉进来再按 `ts` 排序，而原生源每行宽约 2987 字节，排序代价高。
**这是该查询的固有问题，与 S4/S6 无关**；正解是先按 ts 取 request_id 再回表取 body，
登记为独立优化项。

### 5.1.1 ⛔ 这些迁移已全部回退（镜像漏写）

上面三个迁移点的性能收益是真实的，但**在同一天的真库核对里被否决**：
仍有一批会话从未进入 session 族（构成见 §1：93.4% 是按设计排除的内部回环，
真正的漏写是 1,411 行，其中 200 条是成功请求）。原生源没有 v1 分支，
切过去会让这些会话的时间线 / 对比 / 总结**返回不完整的内容**——
与本目标「确保数据在更改前后一致」正面冲突。

**已回退**：`admin/session_online.go`、`admin/session_compare.go`、
`admin/session_summary_v2.go` 三处连同其测试断言，全部改回视图。
测试里保留了反向断言（`session_turns` 不得出现在 fallback SQL 中），
让这次教训变成可执行的约束而不是一段注释。

**保留**：`db.SessionFamilyTurnsForSessionSQL()` 与它的守卫测试
（`TestSessionFamilyTurnsForSessionSQLPushesPredicate`）。镜像补齐后，
这三个端点可以直接换上——届时收益仍在（13x / 13x / 2.5x），且不会丢数据。

**方法论教训**：本轮先做了性能测量、后做了完整性核对，顺序反了。
性能数字是真的，数据前提是假的。**任何「换数据源」的迁移，第一步都应该是
证明新数据源对目标对象是完备的**，性能排在其后。

**守卫**：`TestSessionFamilyTurnsForSessionSQLPushesPredicate` 断言谓词出现在**两条腿**
上，且 wave-1 那个免下推变体不被顺手改掉。这类退化是**静默的**——SQL 依然正确，
只是慢 100 倍，不加守卫不会有任何测试变红。已做变异验证（去掉一条腿的谓词 → 转红）。

---

## 5.2 迁移 802：gw_task_id 索引（S4 前置项已闭合）

`assertTaskInTenant` 的 session 族母表腿原先缺席——`session_turn_details` 月分区母表
约 167 万行，733 只建了 request / session / ts 三条索引，EXISTS 判定会退化成全表顺序扫描。

**`sql/migrations/startup/802_session_turn_details_gw_task_id_index.sql`**（+ `.down.sql`）：

```sql
CREATE INDEX IF NOT EXISTS idx_session_turn_details_tenant_gw_task_id
    ON public.session_turn_details (tenant_id, gw_task_id) WHERE gw_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_turn_details_hot_tenant_gw_task_id
    ON public.session_turn_details_hot (tenant_id, gw_task_id) WHERE gw_task_id IS NOT NULL;
```

形态对齐 525/526 已有的 `(tenant_id, <col>) WHERE <col> IS NOT NULL` 部分索引，
也与 `session_summaries.idx_session_summaries_task` 一致，便于四条腿走同一形态。

**五点同步**（`llm-gateway-installer-migration-3way-sync`）全部完成：
embeddata 副本（up + down）、`main.go` 的 `go:embed` 变量与 `embeddedSQLFiles` 映射、
`dbinit.Runner.StartupFiles` 条目（排在 801 之后，位置约束同 801：必须晚于 733 建表）、
`scripts/apply-db-revision-sequence.sh` 升级通道清单、stats parity map 第 5 点。

**顺带补登 801**：parity map 此前只遍历自身、不反向要求全量，801 缺席 → embed 副本
漂移无人发现。补登前已 diff 确认 canonical 与 embeddata 逐字节一致。

**真库实测**：

| 项 | 结果 |
|----|------|
| up 应用 | `CREATE INDEX` ×2 + `COMMENT` ×2，0.35s |
| 索引级联 | 母表 + hot + 两个分区共 4 个（`pg_indexes` 核验） |
| 构建为何快 | 1,677,271 行中仅 94,615 行 `gw_task_id IS NOT NULL`，部分索引筛掉 94% |
| 母表腿 EXPLAIN | `Index Only Scan` 走 `session_turn_details_2026_09_tenant_id_gw_task_id_idx`，`Buffers: shared hit=4` |
| 五腿合起来 EXPLAIN | 首腿短路，`Buffers: shared hit=1 read=2` |
| down | 干净回滚，重复执行为 no-op（NOTICE skipping） |
| up 幂等重放 | 无副作用，索引数稳定在 4 |

**锁的说明**：用 `CREATE INDEX`（非 `CONCURRENTLY`）—— installer's `applySQL` 以
`psql --single-transaction` 执行，`CONCURRENTLY` 不能在事务块内运行。本机规模下
构建在亚秒级，与 525 同量级。若将来母表显著变大，应改为运维侧在线建索引 +
迁移只保留 `IF NOT EXISTS` 存在性断言。


---

## 5.3 根治盲区：把「按会话查」升级为「全量漂移度量」

上一轮指出影子写失败**只落日志不落库**是盲区。但再想一层：这个缺口**本身就能从
数据直接测量**（本轮就是手写 SQL 测出来的）——问题不是不可测，而是没人测。

仓库里其实已有专职校验器 `cmd/gateway/dual_read_validator.go`（连注释都写了
「7 天零漂移 gate」），但它是**按单个会话**查的：必须先知道该查哪个 session
才会去查。**这正是漏写长期没被发现的原因。**

本轮新增 `Summarize(ctx, tenant, windowHours)` 与端点
`GET /api/admin/sessions/dual-read-drift?hours=168`，把单会话诊断提升为
population 级视图，并**强制区分三类**：

| 分类 | 30 天窗口实测 | 性质 |
|------|--------------|------|
| `internal_loopback` | 36,680 行 / 18,280 会话 | **按设计排除**（标题/摘要回环，非用户轮次） |
| `non_terminal` | 1,536 行 / 1,221 会话 | **按设计排除**（非终态占位行） |
| `genuine_loss` | **1,472 行 / 1,430 会话** | **真漏写** —— S4 的阻断判据 |

响应里的 `s4_ready` 字段仅在 `genuine_loss_rows == 0` 时为真。把噪声和缺陷混在
一个数字里，会让 93% 的「按设计排除」淹没真正要修的 1,400 行；反过来把真漏写
混进「按设计排除」，就会在数据仍丢的情况下判定 S4 可开。**这个区分因此写进了
代码常量与守卫测试**（`TestMirrorDriftClassSQLCoversThreeBuckets`）。

⚠️ `genuine_loss` 在两次测量间从 1,411（全量历史）涨到 1,472（30 天窗口），
**漏写仍在持续发生**，不是历史遗留的静态缺口。

### 5.3.1 根因：pre-712 内存 backlog 随重启丢失（前一版结论已被推翻）

> **本节结论在同日被自我推翻过一次。** 初版写的是「存在某条直写
> `request_logs` 但不触发钩子的旁路路径，本轮未能定位到代码行」。
> **该结论不成立**，下述证据把它逐条排除了。留档以免后来者重走。

**订正后的根因**：镜像写失败在迁移 712 之前只落**进程内存 backlog**，
进程重启即丢；712 落地（`2026-09-15 03:18:02+08`）加了 durable outbox 后，
该丢失通道关闭。

按 712 切分 35 天窗口（订正后的分类口径）：

| 区间 | v1 行数 | `genuine_loss` | 占比 |
|------|---------|---------------|------|
| 712 之前（09-03 ~ 09-15） | — | ~1,341 | — |
| 712 之后（09-15 03:18 ~ 09-30） | 684,305 | **118** | **0.017%** |
| 当前进程启动后（09-30 14:45 ~ 现在，9.5h） | 1,222 | **0** | **0%** |

712 之前的日曲线（465 / 130 / 117 / 69 / … / 309 / 168 / 51）与 712 之后
断崖式降到个位数，转折点与迁移时间戳吻合。

**「钩子有旁路」这一假设的排除过程**（每条都是实证，不是推断）：

1. `firePersistedHooks` 全仓**只有两个调用点**：`client.go:1162`
   （`persistRequestLog`，覆盖 PG / lite sink 两条落库路径）与
   `client.go:743`（`ReplayFallback`）。两者都在写成功后调用，
   **不存在「写库成功但不触发钩子」的路径**。
2. 四道 Go 闸门逐条核对**全部放行**了这批行：
   `isTerminalFailure`（`hook.go:991`）对 `failure` / `rate_limited` /
   任意非空 `error_kind` 一律返回 true；`IsProbeSyntheticSession` 要求
   `GwSessionID` 为空（本批非空）；`shadowWriteEnabled()` 实测
   `settings_kv.sessions_v2.shadow_write = true`。
3. 同形态对照实验：形态完全相同（`is_auto_request` / `origin_actor` /
   `work_type` / `task_type` 全 NULL）的终态行里，**539,689 行已镜像、
   1,442 行未镜像**。同形态绝大多数正常 → 排除系统性门禁缺陷，
   指向**零星的、异步的丢失**。
4. durable 通道本身是通的：reaper 启动日志
   `session mirror outbox reaper started (GAP-2 replay…)` 在位；
   实测观察到一条 outbox 登记（`23:34:48`）随后被排空至 0 行。

> ⚠️ **两个被自己推翻的中间结论**
>
> 1. **`session_v2_mirror_outbox_pending` 等三个指标在 `/metrics` 上「消失」——
>    是假线索。** 该端口由 `com.docker.backend` 持有，**不是网关容器**
>    （`llm-gateway-local-8782` 只发布 8782）。差点据此推断「重放器没进生产
>    二进制」，实际重放器启动日志就在那儿。**负向信号必须先确认信号源身份。**
> 2. **「漏写仍在持续发生（1,411 → 1,472）」是窗口假象。** 两次测量跨的是
>    35 天滚动窗口，且 09-24 之前的量级根本没被采到；按天重算后
>    712 之后是 0~30/天、当前进程为 0。

#### 顺带修掉：分类端点自身的判别缺陷（本轮新增）

`mirrorDriftClassSQL` 初版按 `work_type IN ('session_title','session_summary')`
认内部回环，而 Go 侧 `IsInternalAutoEntry`（`internal_loopback.go:23-39`）
认的是 `is_auto_request` + `request_type` / `origin_actor` / `task_type`，
**`work_type` 它根本不读**。后果是双向的：

- `work_type` 未打戳的生成器行 → 被误判 `genuine_loss` → **`s4_ready` 永远为假，
  卡死一个其实干净的切换**；
- 反向更危险：`is_auto_request=TRUE` + `task_type` 非空的**业务轮次**
  若带上 `work_type='session_title'`，会被 SQL 吞进「按设计排除」，
  **把真漏写伪装成正常**。

已把两个排除臂改成对 Go 闸门的逐条转写（`work_type` 从分类表达式中移除），
并新增真库 parity 门 `TestMirrorDriftClassSQL_MatchesIsInternalAutoEntry`：
把同一组行形态同时喂给 SQL 表达式与 `telemetry.IsInternalAutoEntry`，
**13 个子用例逐一对撞**。变异验证（把 SQL 改回 `work_type` 形态）→
集成门报 3 处 loopback 分歧 + 1 处 non_terminal 分歧，单元字符串门同步转红。

订正后的 35 天真实构成：`internal_loopback` 36,693 / `non_terminal` 1,536 /
**`genuine_loss` 1,459**（初版报 1,472，13 行因分类缺陷被错分）。

> ⚠️ **SQL 三值逻辑陷阱（仍然成立）**
>
> 分类条件**必须用 `CASE WHEN`**，不能写 `NOT (col IN (...))`：
> `NULL IN (...)` → NULL，`NOT NULL` → NULL，整行被 WHERE 丢弃。
> 用 `NOT (IN)` 写出来的查询会稳定返回 `genuine_loss = 0`，
> 看起来像「问题已修复」。端点用的是 `CASE WHEN … ELSE 'genuine_loss'`，
> `ELSE` 分支正确兜住 NULL，不受此陷阱影响。

---

## 5.4 清除历史欠账：扩口径回填 + 两个真缺陷

§5.3.1 定位到 1,459 行历史欠账后，本轮**实际把它们补写了**，而不是只登记。
载体是仓库既有的 `scripts/audit/mirror_outbox_backfill.sql` —— 扩口径后
灌入 `session_mirror_outbox`，由网关内 reaper 走与实时 hook 同一桥接重放。

### 5.4.1 选取口径：原口径对欠账命中率是 0

原口径 `is_final_success IS TRUE` 有两处致命错配：

1. `is_final_success` 是「本请求是否抢到**本会话的最终成功轮**」，
   **不是「本请求是否成功」**。失败轮次永远抢不到该标记。实测 pre-712 的
   1,459 行欠账里 `is_final_success = true` 的成员数为 **0** ——
   这条「清账」脚本对真正的欠账命中率是 0，却因为名字像清账而一直没人质疑。
2. 35 天窗口内 `success = true` 的终态行里，**329 行 `is_final_success = false`**，
   是真业务轮次、hook 当时也镜像了，同样落在口径之外。

改为「终态 + 有会话头 + 无 turns」后命中 **1,459 行**，与 §5.3.1 订正后的
`genuine_loss` 计数**精确一致** —— 两条独立路径互为交叉验证。

### 5.4.2 顺带暴露的两个真缺陷（都是「名字对、类型错」）

| 缺陷 | 表现 | 修法 |
|------|------|------|
| `auto_decision` 投影未转型 | 库里 `jsonb`（**215 万行非空**），Go 侧 `AutoDecision *string`。reaper `json.Unmarshal` 遇 object 打在 string 上 → 整行解码失败 → dead-letter。**12 行实测死信** | 投影改 `auto_decision::text AS auto_decision` |
| `ON CONFLICT DO NOTHING` 让死信不可恢复 | 一旦 decode 失败，request_id 永久占位在 outbox，**修复后重跑也灌不进去** | 改 `DO UPDATE ... WHERE status = 'dead'`，只复活死信、不打断在途的 pending/claimed |

**为什么既有契约测试没拦住**：`TestBackfillProjectionKeysMatchEntryTags`
只校验**键名**是真实的 json tag，不校验**类型**兼容。而「键名对、类型错」
恰恰是更糟的失败形态 —— 键名错会静默丢一个字段，类型错会让整行解码中止。
补了 `TestBackfillProjectionCastsNonScalarColumns` 覆盖类型那一半，
做法是把 90 个投影列 × `information_schema.data_type` × Go 字段类型
做全量交叉核对（结论：`auto_decision` 是**唯一**一处不兼容）。

### 5.4.3 执行与验收

以 `\set days 35`（覆盖 pre-712 全跨度）执行一次，随后恢复默认 7：

| 阶段 | `genuine_loss` | outbox |
|------|---------------|--------|
| 回填前 | 1,459 | 0 |
| 首灌后 | 12 | 12 → dead（`auto_decision` 解码失败） |
| 修投影 + 修幂等后重跑 | **0** | 0（全部重放成功，无死信） |

**数据一致性验收**（回填后实测，这是「确保数据在更改前后一致」的直接检验）：

- 视图 `request_logs_with_current_month` 按 `request_id` **重复数 = 0**；
- 视图总行数 **2,321,329** = 643,377（仅 v1 侧）+ 1,677,952（session 族），
  与两侧基数**精确对账**；
- 回填行的 `session_turns.ts` vs v1 `ts` **delta = 0.000 秒**（逐字节保真）。

> ⚠️ 一处差点误判的地方：把 v1 的 35 天行逐条去视图里找，
> 曾报「1,510,845 行丢失」。**这是错的**——视图的 v1 分支只取
> hot + 当月分区，而 pre-mirror 的老 turns 由 session 分支承载，
> 两腿相加才是 2,321,329。**形态上的「差」不等于「丢」**，
> 必须先确认两侧口径再下结论。
>
> 另注：实时镜像行与 v1 行之间存在约 5.5 秒的 `ts` 差（实测样本
> `request_id=40e0d063…`），这是镜像链写自身事件时间的既有属性，
> **与本次回填无关**（回填行 delta 恒为 0）。

---

## 5.5 重新启用 3 处原生源迁移（欠账清零后的数据前提）

§5.4 把 `genuine_loss` 清零后，本节把同日回退的三处迁移重新做了一遍。

| 调用点 | 迁移 | 切换方式 |
|--------|------|----------|
| `admin/session_online.go` `querySessionTimeline` | 已启用 | `db.SessionFamilyTurnsForSessionSQL()`，谓词下推 |
| `admin/session_summary_v2.go` `buildRequestLogsFallbackQuery` | 已启用 | 同上；正文仍取 v1 bodies 视图（`rb` 腿未改） |
| `admin/session_compare.go` `loadCompareData` + 摘要腿 | 已启用 | 同上 |
| `admin/session_online.go` 在线列表 `JOIN … ON rl.id = slr.last_request_id` | **保留视图** | 见下 |

### 5.5.1 保留的那一处是有理由的

在线列表的 JOIN 条件是 `rl.id = slr.last_request_id`，**不是会话谓词**，
`SessionFamilyTurnsForSessionSQL()` 的下推对它无效；且 `last_request_id`
在请求在飞时指向的正是 `in_progress` 占位行——那类行**只存在于 v1**，
原生源永远没有。切过去会让在飞会话的「最后请求」直接查空。
这一处与计划里的「S3 读端去视图化」无冲突：它是 id 联表，不是会话读。

### 5.5.2 切换前后的口径差（全量实测，非抽样）

| 指标 | 值 |
|------|-----|
| 视图有、session 族无的行 | 38,229 |
| 受影响会话 | 19,511 / 848,414 = **2.30%** |
| ├ `internal_loopback` | 36,693（标题/摘要生成器自己的 LLM 调用） |
| └ `non_terminal` | 1,541（`in_progress` 占位） |
| **未被任何设计判据解释** | **0** |

抽样 300 个会话逐个比对：284 个完全一致，16 个有差，**差异全部落在上述两类**。
另抽一个 40 轮会话逐行核对：视图 40 / 原生源 40，`view_only = 0`。

被剔除的两类**都不是用户轮次**。其中 `in_progress` 行此前在时间线上被当作
`success=false` 的**失败**轮次展示（请求其实还在飞）——迁到原生源后不再出现，
终态时才补进来。这是修正展示语义，不是丢数据。

### 5.5.3 守卫：一次「变异后门仍绿」的实录

第一版守卫用正则 `WHERE t\.session_id = \$1` 钉下推，变异验证时**没有转红**——
把外层 `AND rl.gw_session_id = $1` 加回去后，下推谓词仍然存在，正则照过；
`QueryShape` 里的 `[\s\S]*AND rl\.tenant_id` 也被
`… gw_session_id = $1 AND rl.tenant_id = $2` 顺带满足。
**变异后门仍绿 = 守卫没有判别力**，改用 `pgxmock.QueryMatcherFunc`
拿到实际 SQL 做**否定断言**（`strings.Contains(actualSQL, "rl.gw_session_id")`）。

改后两次变异均被抓住：

| 变异 | 结果 |
|------|------|
| 外层加回 `AND rl.gw_session_id = $1` | 转红（defeats the pushdown） |
| 换成无下推的 `SessionFamilyTurnsSourceSQL()` + 外层过滤 | 转红（found 0） |
| 还原 | 转绿 |

> 同族教训：**「断言某物存在」守不住「某物被加进来」**。凡是「不得出现 X」
> 的性能/正确性契约，必须写成对实际 SQL 的否定断言，正则的 `[\s\S]*`
> 会把新加的内容一并吞掉。

### 5.5.3 ⛔ 一个必须写在这里的负面结论：原生源**不是**全量流量的等价替代

本轮继续把 `session_analytics_breakdown.go` 的两条维度聚合迁到原生源，
**随即回退**。真库实测（近 7 天，模型维度）：

| 指标 | 视图 | 原生源 | 差 |
|------|------|--------|-----|
| 请求行数 | 1,224,620 | 767,014 | **−37%** |
| 会话数 | 412,352 | 401,317 | −2.7% |
| 成本 | 338.0710 | 338.0684 | −0.0008% |

行数差 45.7 万，远超 §5.5.2 量化的 2.30%。继续下钻找到真身：

| v1 行的会话头 | 7 天行数 | 其中有 turns | 缺口 |
|--------------|---------|------------|------|
| `gw_session_id IS NULL`（无会话头流量） | 770,034 | 333,029 | **436,005** |
| 真实会话 | 454,152 | 433,543 | 20,609 |

**无会话头流量有 43.6 万行从未进入 session 族。** 原因：这些请求
（探针、自检、未终态）要么被 `IsProbeSyntheticSession` 按设计排除，
要么是 `in_progress` 占位行——`hook.go:71` 不镜像非终态。
它们的成本近似为 0（所以成本口径几乎没变），但**行数、token 数、
延迟均值会大幅变**。

> ⚠️ §5.5.2 的 2.30% 之所以小，是因为那条查询带了
> `gw_session_id IS NOT NULL AND NOT LIKE 'sys:%'` 过滤，
> **把无会话头流量整个排除了**。这个数字只对「会话内读」成立。
>
> **判据**：原生源的等价性只在**会话内读**（有会话头谓词）成立。
> 任何**全量流量聚合**（analytics / dashboard / 时间线统计）迁过去
> 都会掉 30%+，必须单独决策，不能套用本节的结论。

### 5.5.4 `turns_sessions.go` 两处：等价，已迁移

这两处是**由 session_turns 驱动的 enrich JOIN**（`api_key_id` / `key_alias`），
与上面的聚合性质完全不同：

- 驱动腿 `ft` 本身是 `session_turns` 行；
- 视图的 v1 冻结分支按定义只贡献「session_turns 里没有」的 `request_id`，
  **永远匹配不上** `ON rl.request_id = ft.request_id`。

所以换源是**严格等价**的。真库对账（40 个真实会话）：
旧腿 `pairs=18,725 / sessions=22 / keys=1`，新腿**逐项相同**。

#### 迁移过程中踩到并修掉一个运行期才炸的 bug

把原生源 SQL 拼进 `fmt.Sprintf` 的格式串——而那段 SQL 含
`LIKE 'sys:%'` 与 `~ '^[0-9]+$'`，`%` 被当成格式动词。生成的 WHERE 里：

```
%!'(int=2) THEN NULL ELS      ← LIKE 'sys:%' 吃掉了 argIdx
%!'(MISSING) THEN NULL E      ← 第二条腿已无参数可吃
%d(MISSING)                   ← api_key_id 的 $N 也被吞掉
```

后果是 `/api/admin/turns/sessions?api_key_id=` 在运行期 SQL 语法错 **且**
参数绑定错位。**任何「SQL 里包含 X」的正则断言都发现不了**——残渣恰好让
`rl.api_key_id = $2` 不再成立，而这类断言正是在找那个字符串。

修法：SQL 本体用字符串拼接，参数序号单独 `strconv.Itoa` 拼，
**一个字节都不进格式化**。补守卫
`TestTurnsSessionSQLHasNoFormatArtifacts`（直接判生成结果里不得出现
`%!` / `%!` 动词残渣），变异验证：改回 Sprintf → 转红。

> **通用判据**：把「外部产出的 SQL 文本」拼进 `fmt.Sprintf` 的**格式串**
> 是高危动作——SQL 里出现 `%` 就会吃掉参数。永远用拼接，
> 或用 `%s` 整体传参。

### 5.5.5 会话域视图依赖的最终分类（判据 = 谓词形态，不是「镜像补没补齐」）

把 P1 的 13 个会话域文件按**查询的谓词形态**逐个归类，并给出实测依据。
这份分类已固化为守卫 `admin/session_view_dependency_risk_test.go`
（正反两向，变异验证）。

**实测锚点**：视图里 2,321,464 个 `request_id`，有 **641,452 个（27.6%）**
在原生源查不到——无会话头流量（探针/自检按设计排除、`in_progress` 占位）
与标题/摘要生成器回环。

| 类别 | 文件:行 | 判据 | 结论 |
|------|--------|------|------|
| **A 会话内读** | `session_online.go` 时间线 | `gw_session_id = $1` | ✅ 已迁 |
| | `session_summary_v2.go` fallback | `gw_session_id = $1` | ✅ 已迁 |
| | `session_compare.go` ×2 | `gw_session_id = $1` | ✅ 已迁 |
| | `turns_sessions.go` ×2 | 由 `session_turns` 驱动的 enrich JOIN | ✅ 已迁（严格等价） |
| **B request_id 反查** | `unified_detail.go:205` | `WHERE request_id = $1` | ⛔ **禁止迁** |
| | `unified_detail.go:217` | `WHERE client_request_id = $1` | ⛔ **禁止迁** |
| | `session_turns_tree.go:323` | `WHERE parent_request_id = ANY($1)` | ⛔ **禁止迁** |
| | `session_turns_unified.go:159` | `parent_request_id = ANY($2)` | ⛔ **禁止迁** |
| **C 全量时间窗聚合** | `session_analytics_breakdown.go` ×2 | 无会话谓词 | ⛔ 禁止迁（实测 −37%） |
| | `session_list.go:139/166` | `gw_session_id IS NOT NULL` + 时间窗 | ⚠️ 差异为按设计排除类 |
| | `compression_sessions.go:106/136` | 同上 | ⚠️ 差异为按设计排除类 |
| | `no_topic_session.go:535/558` | `where` 为时间窗 | ⛔ 禁止迁 |
| **D 正文腿** | `session_title.go:190`、`no_topic_session.go:145/320`、`session_export.go:228` | bodies 取 v1 | ⚠️ 判定**已翻转**，见下 |
| **E id 联表** | `session_online.go` 在线列表 | `ON rl.id = last_request_id` | ⚠️ 有意保留视图 |

> ⚠️ **D 类的三处在第二轮复核中翻转为 ⛔。** 第一版分类把它们记成
> 「会话内读，只受正文存储决策制约」——**那是错的**。读实际 WHERE 后：
>
> - `session_title.go:190` 用 `sessionLogsWhere`，主谓词是
>   **`gw_task_id = $1`**（不是会话头）；`sessionLogsWhere` 的可选
>   session 过滤是 `COALESCE(NULLIF(TRIM(gw_session_id),''),NULL) IS NOT
>   DISTINCT FROM $N`，默认为空。
> - `no_topic_session.go:145/320` 用 `noTopicLogsWhere` =
>   **`gw_task_id IS NULL AND api_key_prefix = $1`** ——「没有任务」的流量，
>   正是那 43.6 万行无会话头数据。
> - `session_export.go:228` 是 `WHERE rl.gw_session_id = $1`，**这一条确实
>   是会话内读**，但正文与标题两条腿的 v1 依赖仍需与正文存储决策一起处理。
>
> 实测 `gw_task_id` 口径的缺口：**132,955 行里有 37,064（27.9%）在原生源
> 查不到**——与 `request_id` 的 27.6% 同一量级。**按 task 键查与按
> request_id 查一样危险。**
>
> **教训**：分类不能靠「文件在哪个列表里」或「函数名像不像会话查询」，
> 必须打开它真正用的 `WHERE` 构造函数（`sessionLogsWhere` /
> `noTopicLogsWhere` / `buildWhereClause`）看它拼出什么。第三版分类表：

### 5.5.6 分类守卫的三次修正（都是变异验证逼出来的）

`admin/session_view_dependency_risk_test.go` 一共修了三轮，每轮都是
「变异后门仍绿」或「变异后误报」暴露的：

| 轮次 | 问题 | 修法 |
|------|------|------|
| 1 | `no_topic_session.go` 守卫只查**第一处** `FROM ... rl`；变异只改第二处 → **照绿** | 该文件两处查询都不可迁 → 改**文件级**判定（`fileWide`） |
| 2 | `session_title.go` 用**函数名**当 marker，而 `funcSpan` 回溯到 `\nfunc ` 会落到**上一个**函数 → 查错作用域，假阴性 | `funcSpan` 改为**向后**找函数结束；marker 换成 SQL 串（该文件两处查询可由 ` rl` 别名区分） |
| 3 | 正向守卫用**整文件子串**匹配，注释里写了 helper 名 → 照绿 | 检查前 `stripGoComments` |

三处都已重测：把对应调用改回原生源，守卫**均转红**；还原后转绿。
**任何一条守卫在没做变异验证前都不算有判别力**——本轮这三条守卫
在写出来的当天就各假阴性过一次。

**B 类的危害是结构性的**：`unified_detail` 是「统一详情」端点，按任意
`request_id` 查单行。切到原生源后，27.6% 的 request_id 会返回空——
而这些行在视图里明明存在。**这类迁移不能靠抽样验证**，必须全量计数。

> **判据**（与 §5.5.3 的教训合并）：
> - 「镜像欠账已清零」**不是**迁移许可；
> - 唯一的问题是**这条查询的谓词能不能把 session 族收敛到一个子集**；
> - 能（`gw_session_id` / `session_id` / 由 turns 驱动的 JOIN）→ 可迁；
> - 不能（`request_id` / `parent_request_id` / 纯时间窗）→ **禁止迁**，
>   除非先对那 64 万行做出处置决策。

### 5.5.7 第二波 class A 迁移（4 处，已逐条真库对账）

第一波只做了 4 个调用点就宣称「S3 可做部分已做完」——**那是断言不是验证**。
重开后按 §5.5.5 判据把剩余 class A 找出来并迁完：

| 调用点 | 谓词 | 真库对账（同一 session + tenant） |
|--------|------|-------------------------------|
| `session_list.go` 汇总 | `gw_session_id = $1 AND tenant_id = $2` | 1607 / 1607 行，`MIN/MAX ts` **逐字节相同** |
| `session_list.go` 明细列表 | 同上 | 500 / 500 行，按 `ts` 排序的 `request_id` **md5 完全一致** |
| `session_turns_tree.go` 主查询 | `rl.gw_session_id = $1` | 1607 / 1607 行 |
| `session_title.go:321` gw_task_id 解析 | `gw_session_id = $1 AND tenant_id = $2` | 新旧同解出 `auto` |
| `session_export.go:228` 消息流 | `rl.gw_session_id = $1` | 会话内读，正文腿仍 v1（`rb` 未动） |

**正文腿一律不动**：`session_export.go` / `session_compare.go` /
`session_summary_v2.go` 的 `LEFT JOIN request_logs_bodies_with_current_month rb`
保持原样——`session_bodies` 只有增量、无 `final_full` 全量，正文存储决策未落地前
两侧口径必须一致。

`session_turns_tree.go` 的 `body_present` 判定本来就走
`public.session_bodies_unified`（session 侧），不是 v1，故无需改动。

至此会话域 class A 全部迁完（8 个文件），class B/C 全部由守卫钉死。

### 5.5.8 分类补全：第一版表**不完整**，逐条补齐

§5.5.5 的表只覆盖了每个文件的「代表查询」，而实际有多个文件存在**未归类的残留
查询**。按 `grep -n request_logs_with_current_month` 逐行重扫后补齐：

| 文件:行 | 谓词 | 分类 | 处置 |
|--------|------|------|------|
| `session_turns_tree.go:369` | `gw_session_id = $1 LIMIT 1`（取租户） | **A** | ✅ 本轮已迁 |
| `session_turns_tree.go:324` | `parent_request_id = ANY($1)` | **B** | ⛔ 守卫钉死 |
| `no_topic_session.go:535/558` | `noTopicLogsWhere` = `gw_task_id IS NULL` | **C** | ⛔ 守卫钉死 |
| `no_topic_session.go:145/320` | 同上 | **C** | ⛔ 守卫钉死 |
| `session_online.go:112` | `ON rl.id = slr.last_request_id` | **E** | ⚠️ 有意保留视图 |
| `session_list.go:140/167` | `gw_session_id IS NOT NULL + tenant + 时间窗` | **⚠️ 半等价** | 见下，**待决策** |

#### ⚠️ 半等价类的实测数字（此前只有「⚠️」这种含糊标注）

按 `session_list.go:140` 的**确切谓词**在真库对账（`default` 租户，24h 窗口）：

| 源 | 行数 | 会话数 |
|---|---:|---:|
| 视图 | 3,652 | 3,537 |
| 原生源 | 3,562 | 3,457 |
| 差 | **−90（2.5%）** | **−80（2.3%）** |

这与 §5.5.2 报的会话内读口径 2.30% 吻合，**与 C 类的 37% 差一个数量级**。
缺失的 90 行是真实会话内的内部回环与 `in_progress` 占位行。

**未迁，理由**：这两个查询给会话列表供数（`request_count` / `error_count` /
`is_compressed`）。迁过去会让部分会话的计数**变小 2.5%**——虽是按设计排除的行，
但那是**可观测的数字变化**，与用户约束「确保数据在更改前后一致」直接相关。
**留给产品决策，不自行决定。**

---

## 5.6 独立性能债：fallback 端点 40 秒 → 50 毫秒（与 S4/S6 无关）

§6 上一版把这个条目记成「把两个大 JSONB 正文列拉进来再排序，实测 5.3 秒」。
**那个诊断是错的**，而且错在会误导修法的方向上。实测（2026-10-01，真库
`llm-gateway-pg`，会话 `gw_7a19bfa5` 前 20 轮）：

| 形态 | Execution | Buffers |
|------|-----------|---------|
| 旧单查询（115 列 + `LEFT JOIN` bodies） | **40,483 ms** | 8,513,122 |
| phase 1（只取 `request_id` + `ts`） | 41 ms | 1,029 |
| phase 2 批量半连接（全 miss） | 7 ms | ~120 |
| phase 2 批量半连接（有命中） | 501 ms | — |

先说**与文档不符的两处**：不是 5.3 秒，是 40 秒（`gw_f88477b5` 上 35.5 秒）；
而且那 10 轮正文**全是 NULL** —— 40 秒换来 10 行空值。5.3 秒是 765 迁移
（bodies 列存化）之前的数字，本轮被它放大了约 8 倍。

根因不是「排序时拖着大列」，而是 **`LEFT JOIN` 的连接方式**：

```
Nested Loop Left Join                       (actual rows=20 loops=1)
  -> Gather Merge ... 1607 行               Buffers: 1,573
  -> Append                                 Buffers: 6,726,946 hit / 1,785,824 read
       -> ColumnarScan on request_logs_bodies_2026_09
          Rows Removed by Filter: 10000
          Buffers: 6,726,908 hit
```

765 把 `request_logs_bodies_2026_09`（2,216,660 行）转成了 Citus 列存表。
`LEFT JOIN` 让规划器选了**逐轮 ColumnarScan**——每一轮都重扫整个分区，
`request_id` 主键根本没被用上。正文列大不大完全无关：全 NULL 的那批一样慢。

### 5.6.1 三种写法的计划对比（同一批真实轮次键）

| 写法 | 计划 | 耗时 |
|------|------|------|
| `IN (VALUES ...)` | `Index Scan using request_logs_bodies_2026_09_pkey` | 7 ms |
| `IN (SELECT * FROM unnest($1::text[], $2::timestamptz[]))` | 同上 | 7 ms |
| `unnest(...) AS k LEFT JOIN bodies rb` | **`ColumnarScan` 全扫 2,216,660 行** | 10,365 ms |

第三行是**本节最反直觉的一处**：它和前两行读的是同一张视图、同样两个数组参数，
但 `LEFT JOIN` 贴着函数扫描时规划器没法重排，判定全表列存扫描最便宜，于是真的
全扫了一遍。半连接把 20 行的哈希交给它，它就会走主键探针。

**所以「拆两段」不够，「拆成什么形状」才是判据。** 只写单元断言
「SQL 里不得出现 `LEFT JOIN`」没有意义 —— 第一行和第二行长得几乎一样，
必须判**计划**：断言 phase 2 的 EXPLAIN 里不得出现未标记 `never executed`
的 `ColumnarScan on request_logs_bodies`（`TestSessionSummaryV2FallbackBodiesStaysOnIndexPath`）。

### 5.6.2 顺带修掉一个会静默清空正文的缺陷

拆成两段后，phase 1 与 phase 2 要用 `(request_id, ts)` 对齐。这个键**不能**
直接存 `time.Time`：Go 的 `time.Time` `==` 会比较 `loc` 指针，phase 1 扫出来的
ts 带着连接的 Location，phase 2 再扫回来可能带着另一个 —— 同一时刻的两个值
就 `!=`，于是**每一次正文命中都变成 miss，而且不报错**，只是总结正文悄悄全空。

因此键统一归一为 `UnixMicro`（PG `timestamptz` 恰好是微秒精度），
且两段必须走同一个构造函数 `newFallbackTurnKey`。守卫
`TestFallbackTurnKeyIsLocationIndependent` 同时用反射钉住字段类型 ——
把 `tsUnixMicr` 改回 `time.Time` 或降到秒级精度都会转红。

### 5.6.3 等价性：不是「看着一样」，是逐行对撞

`TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows` 把**拆开前的原始查询
逐字保留**为 `legacyFallbackQuery`，对真实会话跑旧/新两条路径，比较**端点真正
吐出的 turns**（TurnNo + 两个解码后的正文），而不是中间行 —— 中间行相等但合并
错了，端点照样坏。

探针**从库里动态发现，不硬编码 session_id**（硬编码会在正文保留窗口或会话 TTL
一动就失效）。形状覆盖三种：

| 探针 | 数量 | 覆盖的路 | 旧查询成本 |
|------|------|---------|-----------|
| 1 轮、**有正文**的会话 | 8 | 唯一能走到「正文真的被取到」这条路的样本 | 各 1 轮 |
| 多轮（5 轮）、正文全缺 | 1 | 连续 miss | 5 轮 |
| `sys:` 前缀 | 1 | 投影 `gw_session_id` 被 CASE 置 NULL，最易错的一类 | 2 轮 |

**实测：19 组子用例全部逐轮比对通过，整门 156 秒。**

> **这个测试自己踩了三次坑，三次都是我自己的设计错，不是代码错**：
>
> 1. 无界分支没设轮次上限，`sys:probe:cred126` 有 53,851 轮 —— 旧查询是逐轮
>    列存扫描，那就是 53,851 × 0.8s ≈ **十几小时**。
> 2. 上限提到 200 轮，整门仍跑满 15 分钟超时，只完成 5 组。真正的成本是
>    **旧查询约 0.8 秒/轮**，与新查询快不快无关。
> 3. 改用「动态发现探针」后，发现阶段又用 60 次单会话往返 × 3s = 180s，
>    **发现阶段**自己成了瓶颈；改成一条集合式查询（1.9s，从 bodies 侧反查）
>    后整门 21 秒可跑完（加入实际比对后 156 秒）。
>
> 教训值得单独立住：**拿「你刚修好的那个慢查询」当测试夹具时，
> 它自己的成本就是测试预算的天花板。** 语义等价只需要几行样本，轮数堆到几百行
> 不会让结论更有说服力，只会让门永远跑不完 —— **一个超时的门和一个没有门是
> 同一种风险**。发现夹具也同理：动态发现是对的，**逐个往返去探**是同一个错误
> 的另一种形态。
>
> 顺带这也解释了为什么要专门去找「有正文的 1 轮会话」：库里大量单轮会话且正文
> 命中，这正是既便宜又能覆盖命中路径的样本。

### 5.6.4 守卫的变异验证（5/5 全部转红）

| 变异 | 转红的门 |
|------|---------|
| M1 phase 1 把正文 `LEFT JOIN` 内联回去 | `...Phase1CarriesNoBodyColumns` |
| M2 phase 2 改回 `unnest + LEFT JOIN` | `...BodiesSQLUsesIndexedSemiJoin` |
| M3 键从微秒降到秒（不同微秒被压成同一键） | `TestFallbackTurnKeyIsLocationIndependent` |
| M4 缺正文时用上一轮的正文顶替 | `TestMergeFallbackTurnsPreservesOrderAndLeftSemantics` |
| M5 phase 2 改回逐轮 `VALUES`（`$3` 起，参数会爆） | `...BodiesSQLUsesIndexedSemiJoin` |

M5 顺带钉住一件事：用两个数组参数而不是 `VALUES` 列表，是为了让 `upToTurn`
为 nil（轮数无上界）时**不会在 32767 轮撞上 65535 参数上限**。

### 5.6.5 顺带发现的既存冗余（未处置，但已实测可安全删除）

`request_logs_bodies_hot` 上有**两个功能重复的索引**，列完全相同：

```
idx_request_logs_bodies_hot_request_id       UNIQUE btree (request_id)
request_logs_bodies_hot_request_id_idx              btree (request_id)
```

**实测（事务内 `DROP INDEX` + `ROLLBACK`，零风险）**：删掉非 UNIQUE 那个之后，
phase 2 的计划不变——月分区仍走 `request_logs_bodies_2026_09_pkey`，hot 腿改走
`request_logs_bodies_hot_ts_idx`（探针键不在 hot 里，规划器选了另一个同样便宜的索引），
`Execution 3.940 ms`，无回退。

**未处置**，两个原因：属迁移 765 的范围（该迁移刚落 origin/main），
且清理需要新开 803 并走五点同步——这两件都该由 765 的收尾一并做，
而不是由本轮审计顺手插进去。

## 5.7 存储可用性复核（2026-10-01，本轮全部改动落地后）

§5.6 改的是查询形态，但既然已经在真库上跑过一轮，把「数据存储可用」这个
**门本身**重新验一遍——跨越了 765 迁移、1,459 行回填、以及时间推移。

| 复核项 | 结果 |
|--------|------|
| 35 天窗口 `genuine_loss` | **0**（36,693 internal_loopback + 1,536 non_terminal） |
| 最近 24h（当前进程窗口） | **0**（83 internal_loopback + 7 non_terminal） |
| `session_mirror_outbox` | **0 行**（无 pending、无 dead-letter） |
| 视图 `request_logs_with_current_month` 按 `request_id` 重复数 | **0** |
| 视图总行数 | 2,321,899 |
| `sessions_v2.enabled` / `shadow_write`（存于 `settings_kv`） | `true` / `true` |

### 5.7.1 最强的一条：缺口被完全解释，零无法归因

「v1 有、session 族没有」的 `request_id` 共 **641,738** 个。拆开：

| 构成 | request_id | 是否需要处置 |
|------|-----------|------------|
| **无 `gw_session_id`**（探针 / 自检 / 在飞占位） | 603,509 | 否——本就不是会话流量 |
| **有 `gw_session_id`**（漂移口径范围内） | 38,229 | 否——见下 |
| └ 其中 `internal_loopback` | 36,693 | 按设计排除 |
| └ 其中 `non_terminal` | 1,536 | 按设计排除 |
| └ 其中 **`genuine_loss`** | **0** | — |

**36,693 + 1,536 = 38,229，与漂移查询独立算出的同一数字逐位相等。**
两条路径用的是完全不同的 SQL（一条按 `gw_session_id` 有无分桶，一条按
hook 三分类），得到同一个数，说明**每一个 v1 侧缺失的 request_id 都有解释，
没有一条无法归因**。这是「数据在更改前后一致」能给出的最强形式。

### 5.7.2 一个必须写清楚的口径陷阱

`session_turns` 侧另有 **167,107** 个 `request_id` 已不在 v1 中。
这不是丢数据：镜像链比 v1 的 TTL 跑得更远，v1 燃尽后 session 侧仍在。
把两个数相加或相减去论证「对账不平」是错的——**它们量的不是同一个集合**：
§5.7.1 量的是「v1 有而 session 没有」，这里量的是「session 有而 v1 没有」。

## 5.8 顺带撞见的第二类缺陷：SQL 只在发往真库时才被解析（2026-10-01）

§5.6 修完性能后去查「还有哪些端点踩在同一个 bodies 坑上」，按外侧行数分类时
撞见了一个**更严重**的问题。三个文件里有 SQL 是 PostgreSQL **根本无法解析**的，
其中两个端点已经这样死了数周，没有任何测试发现。

### 5.8.1 `''::jsonb` 让整条语句在解析期失败

```
ERROR:  invalid input syntax for type json
LINE 9:  COALESCE(rb.request_body, ''::jsonb) AS request_body,
```

PostgreSQL 会在**解析**阶段对常量求值，而 `''` 不是合法 JSON 文档
（空 JSON 文档是 `{}`）。所以这不是「某些行取不到值」，而是**这条 SQL 永远执行不了**。

| 位置 | 影响面 | 引入时间 |
|------|--------|---------|
| `admin/session_export.go:227-228` | 路由 `/api/admin/session-export` 已注册（`cmd/gateway/main.go:6848`）——**整条路径 100% 失败** | 2026-07-08 |
| `domains/sessionforensics/export.go:120-121, 246-247` | 同一段 SQL 的取证导出，两处 | 2026-08-28 |
| `admin/quality_correlations.go:183, 194` | 只在 `images` / `code_block` 两个分桶下炸，其余分桶正常 | 2026-08-28 |

全部已修为 `'{}'::jsonb`（语义逐条核对过：导出侧扫进 `*string` 得到 `"{}"`；
`->'messages' @> ...` 在 `{}` 上返回 NULL 落入 `ELSE`；`::text` 不含反引号落 `no_code`）。

### 5.8.2 为什么两个端点死了一周多都没人发现

**因为 mock 不解析 SQL。** pgxmock 匹配的是查询**字符串**，所以
「引用不存在的列」「非法类型转换」这一整类缺陷在单测里是隐形的：

- `session_export_test.go` 测的是鉴权顺序、别名解析、租户强制——全绿，
  而端点唯一真正执行的那条查询无法解析；
- `quality_correlations_test.go` 测的是纯函数 `bucketIndex()`（对分桶**名字**
  做映射），与 SQL 无关。

更值得记的是：`admin/session_export.go` 的这条查询**已经栽过两次**——
先是不存在的 `rl.role` 列（2026-07，`42703`），修好之后又栽在 `''::jsonb`。
两次都无人察觉。

### 5.8.3 顺带挖出第三个：`language` 分桶的正则在 PG 17 上直接报错

写真库门时把 `buildBreakdownQuery` 的**全部分支**跑了一遍，`language` 分支红了：

```
ERROR: invalid regular expression: invalid escape \ sequence (2201B)
```

原写法 `'\x{4E00}-\x{9FFF}'` 在本库（**PostgreSQL 17.10**）上不被接受。
已改为字面量方括号区间，并逐条实测：

```
'中'~[一-鿿] t   'あ'~[぀-ゟ] t   'ア'~[゠-ヿ] t
'가'~[가-힯] t   'д'~[Ѐ-ӿ] t   'ع'~[؀-ۿ] t   'x'~[一-鿿] f
```

这个缺陷比前两个更难发现：`computeAllInsights` 用 `continue` **吞掉**查询错误，
所以端点不是报错，而是**静默少一个预测因子**——面板看起来完全正常。
修好后该维度从 0 个分桶变为 2 个分桶，这是**行为变化**（原先缺失的数据现在会出现），
属于修复而非回归，但需要知悉。

### 5.8.4 守卫：把 SQL 抽出来，让真库执行**本体**

| 门 | 作用 | 变异验证 |
|----|------|---------|
| `TestSessionExportMessagesSQL_ExecutesOnRealDatabase` | 执行 `sessionExportMessagesSQL()` **本体** | 放回 `''::jsonb` → `22P02` 转红；放回 `rl.role` → `42703` 转红（**两个历史 bug 都被新门当场抓住**） |
| `TestQualityBreakdownQueries_ExecuteOnRealDatabase` | 跑 `buildBreakdownQuery(by)` 的**全部分支** | 退回 `\x{...}` → `2201B` 转红 |
| `TestNoEmptyStringCastToJSONInSQLLiterals` | 全仓类级扫描（剥注释后） | 放回 `''::jsonb` 与 `''::JSONB` 均转红 |
| `TestEmptyStringCastRejectsEmptyStringLiterals` | **守卫自身的模式自检** | 见下 |

两个关键取舍：

1. **SQL 必须抽成具名函数再测**。测试里抄一份副本等于什么都没验——抄的那份
   永远不会随生产代码变。`sessionExportMessagesSQL()` 是为此抽出来的，注释里
   写明「不要内联回去」。
2. **用哨兵 session id 即可**。解析/绑定错误在产出任何行之前就抛，所以门
   廉价（0.5 秒）且不依赖保留窗口或数据分布。

**守卫自己也栽了一次**：第一版禁止所有 `''::<type>`，立刻误报
`credential_models_dto.go` 的 `offerListSQLCompat = `''::text``——**空串是合法的
text 值**，那个 cast 是有意为之。收窄到 `jsonb?` 才既正确又可用。
**会误报的守卫会被关掉，被关掉的守卫比没有守卫更糟**，所以补了模式自检测试
（正例必须匹配、合法写法必须不匹配）——否则一个悄悄失配的正则和一个干净的树
长得一模一样。

### 5.8.5 一次自我推翻：「已修复」这句话本身差点是假的

本节写完初版后，我用 `cp /tmp/xxx.go` 保存副本来做变异验证、事后「还原」。
结果是**我自己的修复被还原了两次**，而且中途还宣布过「已修复，复查 0 剩余」。

| 事故 | 表现 | 根因 |
|------|------|------|
| 第一次丢失 | `domains/sessionforensics/export.go` 的 4 处字面量一处没改 | 替换脚本的缩进模式只匹配了其中两处 |
| 复查失效 | 汇报「复查 0 剩余」 | 复查命令写成 `grep -v _test.go`（未加引号）且外层双引号把 `''` 吃掉，判定式本身失效 |
| 第二次丢失 | `admin/quality_correlations.go` 的字面量与 `language` 正则修复双双消失 | 变异验证的 `cp` 还原覆盖了新改动 |
| 第三次丢失 | sessionforensics 的 SQL 抽取被 `git checkout --` 一并还原 | 用 `git checkout` 当「还原」手段 = 连自己的修复一起回滚 |

**最后是守卫抓出来的，不是眼睛**：`TestNoEmptyStringCastToJSONInSQLLiterals`
在我毫无察觉的情况下转红，一次列出 6 个位置。

由此得到两条纪律：

1. **完成度声明必须由测试判定，不能由手写 grep 判定。** 手写复查命令
   是本轮唯一没有变异验证过的「门」——它自己先失效了。
2. **变异验证不要用 `cp` / `git checkout` 做还原。** 二者都会覆盖掉
   文件上已有的新改动。本轮为此把 `domains/sessionforensics/export.go`
   改坏了两次，最后靠 `git checkout` 恢复到原始状态、再用精确匹配的
   `edit` 重做。精确匹配工具失败时会**报错**，而脚本化改写会**静默破坏**
   ——这个差别在半夜排查时很值钱。

守卫与真库门在这一轮全部按预期工作（4 次变异全部转红），修复重做后复绿。

## 5.9 顺手把同一形状的慢端点也修了，并挖出两个既存缺陷

§5.6 修完 summary 后，去查还有哪些端点踩在同一个 bodies 坑上。按**外侧行数**分类
（单行 `request_id` 反查不受影响，会话级多行才会触发逐轮列存扫描）后，
`session_compare` 是最严重的一个。

### 5.9.1 性能：compare 4,183 ms → 267 ms

| 形态 | Execution |
|------|-----------|
| 旧单查询（12 列 `LEFT JOIN`，500 轮上限） | **4,183 ms** |
| 新 phase 1（轮次元数据，9 → 5 列） | 170 ms |
| 新 phase 2（批量半连接） | 97 ms |

顺带发现原查询 `SELECT` 了 `outbound_msg_count` / `outbound_token_est` /
`provider_id` 三个列并扫进变量，而**没有任何分支读过它们**——已删除。

### 5.9.2 挖出缺陷一：配对键 `(request_id, ts)` 根本配不上

改造时我一度把 compare 的关联键从 `request_id` 收紧成 `(request_id, ts)`——
理由是 summary 那边用的是元组，看起来更严谨。**等价性门当场转红**，
把我带到一个此前没人注意的事实：

```
request_id   session_turns.ts              request_logs_bodies.ts
d5ddec11…    2026-09-11 09:19:57.824743+08  2026-09-11 09:19:49.062849+08
```

**`request_logs_bodies.ts` 是正文写入时间，不是轮次时间。** 全库实测：

| 口径 | 数量 |
|------|------|
| 两表能 join 上的 request_id | 811,128 |
| 其中 ts **相等** | 1,236 |
| ts **不等** | 809,892（**99.85%**） |

所以按 `(request_id, ts)` 配对**几乎永远返回空**。而
`session_compare` / `session_export` / `sessionforensics` / `session_title`
全都只按 `request_id` 配对——那是既有且正确的口径。

**这意味着 `session_summary_v2.go` 的原查询（`AND rb.ts = rl.ts`）长期拿不到正文。**
§5.6 的等价性门是绿的（因为新旧用的是同一个键），它证明了「等价」，
但没有也不可能证明「这个键本身对不对」。

处置：**两种键都保留**，分别对应两个端点各自的既有行为，不在重构里夹带变更。
`querySessionBodiesByRequestID` 与 `querySessionBodiesByRequestIDAndTS` 并存，
注释写清各自理由。**是否把 summary 也改成 `request_id` 口径是一个独立的产品决策**，
改了之后总结正文会从几乎全空变成有内容——影响面大，需你拍板（见 §8）。

### 5.9.3 挖出缺陷二：compare 静默丢掉 17.32% 的轮次

`client_model` 被扫进裸 `string`，而它可空；`rows.Scan` 遇 NULL 报错，
紧跟的 `if err != nil { continue }` 就把**整轮丢掉**。

| 口径 | 数量 |
|------|------|
| `gw_session_id` 非空的轮次 | 964,990 |
| 其中 `client_model IS NULL` | 167,133（**17.32%**） |

这**不是我引入的**——`git show HEAD:admin/session_compare.go` 确认改动前就是这样。
但重构不能原样保留一个已知的数据丢失。已把扫描改为 `*string` + `derefOrEmpty`。

**行为变化**：compare 现在会比以前多给约 17% 的轮次。

### 5.9.4 守卫

| 门 | 变异验证 |
|----|---------|
| `TestSessionCompareSplitMatchesLegacyQuery`（真库，4 组探针） | 键错配时转红——**正是它抓出 §5.9.2** |
| `TestSessionBodyPairingKeysMatchTheirCallers` | 见下 |
| `TestSessionBodiesBatchSQLIsNotALefiJoin` | 退回 `unnest + LEFT JOIN` → 转红 |
| `TestCompareKeepsTurnsWithNullClientModel` | `clientModel` 退回裸 `string` → 转红 |

> 一个值得记的细节：把 compare 的键改回元组的那个变异**根本编译不过**——
> `querySessionBodiesByRequestID` 返回 `map[string]sessionBody`，用
> `fallbackTurnKey` 去索引是类型错误。**map 的键类型才是真正的守卫**，
> 源码文本断言只是防重构时手滑的文档。

## 5.10 合并 S4 批次：39 文件的合并不是「无冲突 = 正确」（2026-10-01）

本会话的工作树基于 `c54ab8dc1`，另一会话的 S4 读端迁移批次是 `38d59b2eb`。
两者 merge-base 是 `c19baebd4`（比双方 HEAD 都老），所以这是一次**真正的三方合并**，
不是快进。`git merge` 报「无冲突」，但那只说明**没有文本冲突**。

### 5.10.1 第一例：文本层无冲突的语义混合体

`admin/session_panorama_handler.go` 合并后编译失败：

```
admin/session_panorama_handler.go:179:2: undefined: rows
```

成因是三方合并把「`38d59b2eb` 把内联 timeline 查询换成
`loadSessionTimelineInTx`」与「`origin/main` 给内联版本补的 `rows.Err()`
处理」拼在了一起 —— 前者删掉了 `rows` 变量，后者还在引用它。
git 看到的是「一边删 27 行、一边加 4 行」，行级不重叠，**因此判定无冲突**。

删掉残留块后仍留一个未使用的 `fmt` import，编译再次报错。**编译通过是最低门槛，
不是正确性证据**：这次是我运气好，残留的是未定义变量而不是一个恰好能编译的
错误表达式。

### 5.10.2 第二例：`warnRowSkip` 被「重构顺手清理」掉了（更危险）

单测 `TestAggReadGuard_MigratedCallersWired` 在合并后转红：

```
admin/session_compare.go lost guard wiring: warnRowSkip no longer referenced
```

追查结果是两层叠加：

1. `38d59b2eb` 的 `session_compare.go` 里 `warnRowSkip` 出现 **0 次**，
   而 `origin/main` 有 3 次 —— S4 批次把 `loadCompareData` 整体改写成原生源时，
   把 R35-N1 的三处跳行留痕一起删了。**这是 S4 批次自身引入的回归**。
2. 三方合并恰好把 `origin/main` 那 3 处补了回来（因为它们落在 S4 未改动的
   上下文行上）。**这是运气，不是设计。**
3. 我随后用 `git checkout stash@{0} -- admin/session_compare.go` 恢复自己的
   两段式拆分版本 —— 而我的 stash 基线是 `c54ab8dc1`，**早于 R35-N1 那批提交**，
   于是刚被合并补回来的 3 处又被我抹掉了。

所以最终结论是：**两边都丢过，只是被合并的运气掩盖了。**

修复方式不是照抄 `origin/main`，而是核对了 `c54ab8dc1..541c766ba` 之间
`session_compare.go` 的**全部**变更 —— 结果正好就是这 4 处（3 处
`warnRowSkip` + 2 处 `rows.Err()`），无其他内容。因此在我的两段式版本上
逐一补回即为正确超集。

同批核对其余 4 个被 checkout 的文件（`quality_correlations.go`、
`session_summary_v2.go`、`session_summary_v2_fallback_test.go`、
`sessionforensics/export.go`）：`c54ab8dc1..541c766ba` 对它们的 diff **全为空**，
说明我的版本没有回退任何 origin/main 的后续修复。

### 5.10.3 排查方法：按「两侧都改过」筛风险区，而不是靠人眼看 diff

合并后 88 个文件有差异，但只有 **5 个**同时相对两个父版本都发生变更
（`session_compare.go`、`session_export.go`、`session_management_api.go`、
`session_panorama_handler.go`、`session_title.go`）—— 这 5 个是必须逐个人工
审的语义混合体候选。筛法：

```bash
comm -12 <(git diff --name-only P1 M | sort) <(git diff --name-only P2 M | sort)
```

另外对 26 个受 `aggregate_read_guard_test.go` 静态守卫的文件做了
「origin/main / S4 / 合并结果」三态计数对撞，确认合并在**其余 20 个文件上
零差异**，守卫接线未被合并破坏。

### 5.10.4 守卫本身在这一轮证明有效

`TestAggReadGuard_MigratedCallersWired` 是纯文本守卫（`strings.Contains`），
按 §5.5.6 的标准它算「弱守卫」。但本轮它是**唯一**发现语义回归的机制 ——
编译全绿、vet 全绿、其余单测全绿，只有它红了。补记一条判据：
**文本守卫在「引用是否存在」这件事上足够可靠**（引用要么在要么不在），
不可靠的只是它无法区分「在正确的位置」与「在注释里」。

## 6. 未完成项（不得写成已完成）

**P0 已全部清零**（9 个文件）。以下为剩余项：

**本轮合并状态（2026-10-01）**：S4 批次 `38d59b2eb` 已合入 `main`
（merge commit `317f28556`）。合并发现并修掉 2 处语义缺陷，详见 §5.10。
需要注意的是，S4 批次**自身**曾回归掉 `session_compare.go` 的 R35-N1
跳行留痕接线，本轮已补回；其余 20 个受静态守卫的文件经三态计数对撞
确认合并零差异。

**本轮引入的口径变化（需知悉，不是缺陷）**：`session_analytics_timeseries.go`
三条趋势查询切到 734 视图后，**跨月的 pre-mirror v1 历史行不再计入**
（视图 v1 分支只覆盖 hot + 当月分区；session 分支仍是全量历史）。
镜像链 2026-09 起全量双写，最多影响约 1 个月旧数据，而这批数据本来就在 TTL 燃尽队列里。
裁决依据：本页 breakdown 面板本就切了视图，两个面板必须自洽。

**索引前置项：已闭合**。见 §5.2 —— 迁移 802 补齐 `session_turn_details.gw_task_id`
部分索引并完成五点同步，`assertTaskInTenant` 的母表腿已加回（现为五条腿）。

**S6（DROP 表族）才会暴露的债**：
- P1 组仍有 9 个文件走视图，S4 可扛但 S6 时**同时失效且无开关可回切**。
  ⚠️ **本轮曾把其中 3 个迁到会话域原生源，一度全部回退**（见 §5.1.1），
  随后在欠账清零后**已重新启用**（见 §5.5、§5.5.7）。当前状态：
  会话内读（class A）**已全部迁完**；`session_online.go`、`session_compare.go`、
  `session_summary_v2.go` 三处**现役于原生源**。
  回退与重新启用的原因是同一个：镜像不完整（那 1,459 行历史欠账永不入
  `session_turns`），而 §5.4 已把欠账补齐、数据前提不再成立。
  其余各点**不能照搬下推模板**（各需单独设计）：
  - `session_title.go:190`、`no_topic_session.go:535/558` 按 **gw_task_id**
    过滤（733 特征层列），不是 session_id，谓词下推形态不同；
  - `compression_sessions.go:106/136/143` 是**跨会话聚合**（COUNT DISTINCT
    gw_session_id），没有单会话键可下推；
  - `turns_sessions.go:441`、`session_analytics_breakdown.go:267/315` 是
    **时间窗/维度聚合**，属于 wave-1 那类查询，用免下推的
    `SessionFamilyTurnsSourceSQL()` 即可；
  - 读 bodies 的那批（`session_export.go`、`session_compare.go` 剩余腿）受制于
    正文存储决策，见下。
- 正文（`session_bodies` 只有增量、无 final_full 全量）是 S6 前必须决策的一项。
- `session_turns_tree.go:18-20` 自陈的双源一致性 TODO，在视图 DROP 时一次性暴露。
- `admin/logs_turns_source_test.go` 断言灰度默认走视图，S4 真正开启时该默认值假设失效。
- ~~**独立性能项**：`session_summary_v2.go` 的 fallback 查询~~ —— **已完成**，
  见 §5.6。原记录的诊断（「大 JSONB 列拖慢排序」）经实测证伪，真因是 `LEFT JOIN`
  导致的逐轮列存扫描；耗时也从记录的 5.3 秒修正为实测 40 秒。
- **仍未处置**：`request_logs_bodies_hot` 上两个功能重复的 `(request_id)`
  索引（§5.6.5），属 765 迁移范围。

**尚未启动**：S5 TTL 燃尽、S6 DROP。

## 7. 建议下一步顺序

> 顺序已按 §5.4（欠账清零）重排：**S4 灰度的两个前置条件现在都成立了**
> ——活跃漏写为 0、历史欠账已补写。

1. **~~重新启用 3 处原生源迁移~~ —— 已完成（§5.5）**。`session_online.go`
   的时间线、`session_summary_v2.go` 的 fallback、`session_compare.go` 的两处
   全部切到 `db.SessionFamilyTurnsForSessionSQL()`，守卫经两次变异验证有判别力。
   在线列表的 `ON rl.id = last_request_id` 一处**有意保留视图**（见 §5.5.1 理由）。
2. **S4 真机灰度**：开 `storage.request_logs_write_enabled=false`，
   用 `GET /api/admin/sessions/dual-read-drift` 盯 `s4_ready`。
   ⚠️ 灰度期间仍**保留 734 视图的 v1 冻结分支**——停写后新数据不再进 v1，
   但该分支仍是当前月热数据、以及在线列表 `last_request_id` 的唯一读路径。
3. **S5 TTL 燃尽**：让存量按 TTL 自然退出，之后视图 v1 分支即可摘除。
4. **（可选）常规化回填**：欠账已清零，`\set days 7` 的默认窗口足够；
   若后续再现欠账，脚本现已具备「复活死信 + 幂等重跑」能力。
5. **决策正文存储**：S6 前必须定 `session_bodies` 是否补全量字段。
6. **S6 DROP**。

## 8. 待你拍板（截至 2026-10-02，共 3 项待决 + 4 项已关闭）

第 6 项因合并自动关闭；第 7 项同日拍板并落地（连同新挖出的缺陷 7，见 §5.11）。

**2026-10-02 追加**：§8.5 列出的三处硬阻塞中，第 ② 条（validator 换源）已修复 ——
见 §8.8。它不但换源，还修了该门在停写后**永久真空为真**的失效形态。
剩余 ①（104 个文件的逐点依赖评估）与 ③（§8 第 3 项口径决定）仍开放。

| # | 事项 | 性质 | 状态 |
|---|------|------|------|
| 1 | **S4 真机灰度** | 运行态关写、不可逆。**2026-10-01 已批准，但 §8.4/§8.5 查出三处硬阻塞**（退出条件未达成、276 个调用点未评估、validator 会被停写关掉）。**未翻开关** | 阻塞 |
| 2 | ~~**`session_list.go:140/167` 半等价类**~~ | **已拍板并落地**：读源迁到 `db.SessionFamilyTurnsSourceSQL()`。门 `TestSessionListNativeSourceDropIsInternalOnly` 跑**生产同一条 SQL** 并钉方向性不变式——原生源少掉的必须是内部调用，出现真业务轮次即报红。实测：原生源会话 13,585、v1 独有 1,381 条、其中含真业务轮次 **0** 条。见 §8.2 | 已关闭 |
| 3 | **全量流量聚合口径** | analytics/dashboard 是否只统计会话流量（产品口径） | 等决定 |
| 4 | **641,452 个无会话头 request_id 的处置** | S6 DROP `request_logs` 的前提。**2026-10-01 已重测（§8.6）：无镜像缺口，94.05% 从来就没有会话头** ⇒ 这是产品口径决策，不是数据质量决策 | 等决定（已备好数字） |
| 5 | **`request_logs_bodies_hot` 重复索引** | **已由迁移 807 落地（2026-10-01）**：删的是同列**非唯一**索引 `request_logs_bodies_hot_request_id_idx`，**保留** `idx_request_logs_bodies_hot_request_id`（UNIQUE，承重 `ON CONFLICT (request_id)`，也是 phase 2 命中热表的路径）。我此前担心的「删掉 phase 2 依赖的索引」不成立——§8.3 的 17~19 秒是 post-807 状态实测，不受本迁移影响 | 已关闭 |
| 6 | ~~**工作区 131 文件陈旧暂存区**~~ | **已作废**：`reset --soft origin/main` 的残留已被本轮合并（`317f28556`）清空；当前工作区仅 14 个文件、全部是本轮有意改动 | 已关闭 |
| 7 | ~~**`session_summary_v2` 的正文配对键**~~ | **已拍板并落地**（`09419da13`）。实测元组键在 v1 源上只命中 0.007%（1/14,546），单键 100%；真库门实测 169 轮里单键救回 168 轮。**同批还修掉一个此前未知的缺陷 7**（fallback turns 腿被 S4 批次改接成同源原生源，见 §5.11） | 已关闭 |

### 8.3 【P0 新发现】`session_compare` / `session_summary_v2` 的 phase 2 实测 17~19 秒

**先说守门机制本身的缺陷**，再说性能问题——顺序不能反，因为前者是后者的成因。

`TestSessionSummaryV2FallbackBodiesStaysOnIndexPath` 守着「phase 2 不得退化成列存
全扫」这条性能契约。它自诞生起就是**恒绿**：

1. **锚点字符串在计划里根本不存在。** 判据找
   `strings.Contains(line, "ColumnarScan on request_logs_bodies")`，而 Citus 列存
   节点的真实行长这样：
   `Custom Scan (ColumnarScan) on request_logs_bodies_2026_09 request_logs_bodies`。
   中间有 `(ColumnarScan) ` 的括号与空格。**用那个子串在它自己的计划输出里 grep，
   命中 0 次。** 一道永远匹配不到任何东西的断言，等于没有断言。
2. **它跑的是裸 `EXPLAIN`，从不执行。** 输出里只有 `cost=`，没有 `actual rows` /
   `Buffers` / `Execution Time`。于是「这条分支实际多贵」这个问题从来没被问过；
   `strings.Contains(line, "never executed")` 那道豁免也就永远不会触发。

**所以契约绿着，而契约已经在事实上失效。** 修正判据（锚点改成真实字面量
`ColumnarScan) on request_logs_bodies` + 改用 `EXPLAIN (ANALYZE, BUFFERS)`）后，
这道门**立刻变红**，`Execution Time: 17,568 ms`。

**实测口径**（真库，本机 `llm-gateway-pg` / PG 17.10 / Citus 列存，2026-10-01）：

- 用**生产代码路径**（pgx 绑定参数 `$1::text[]`，跑 `sessionBodiesByRequestIDSQL`
  逐字复制体）取真实会话 `gw_63798b79` 的 **171 个 `request_id`**：
  连测三次 **19,654 / 16,271 / 17,326 ms**，取到 171 行。
- 计划形态：`Bitmap Index Scan on idx_request_logs_bodies_hot_request_id` 命中热表，
  但 `request_logs_bodies_2026_09`（列存，2,217,555 行）走
  `ColumnarScan` + `Rows Removed by Filter: 2,217,555`。
- `request_id` 落在热表还是月度分区都一样扫；5 个 id 也要 17.8~19.0 秒。
- 加 `ts` 范围谓词**无效**（17.0~17.6 秒）：该会话轮次时间跨 09-10~09-30，
  窗口放宽到 ±48h 后仍覆盖几乎全部分区，剪不掉 chunk。

**受影响的调用方**（`querySessionBodiesByRequestID` 的使用者）：

| 调用方 | 端点 | 现状 |
|---|---|---|
| `admin/session_compare.go:293` | `GET /api/admin/sessions/compare` | 同一条 SQL，同样代价 |
| `admin/session_summary_v2.go` | `GET /api/admin/sessions/{id}/summary` fallback | 同上（本轮改动新接上） |

**文档里的旧数字已过期**：`phase 2 7~501 ms`、`compare 4.2s→267ms`（§5.6、handoff §三）
都是迁移 765 把 2026_09 转列存**之前**的测值。本轮 `TestSessionCompareSplitMatchesLegacyQuery`
跑 4 组探针耗时 70~143 秒，也与「每组几百毫秒」不相容。

### 8.3.1 更严重的连带发现：`session_compare` 的 10s 预算**已经**超时

两个端点本来就有超时，但预算与实测成本的关系是反的：

| 端点 | ctx 预算 | 实测 | 结果（真库，同一会话 171 个 request_id） |
|---|---|---|---|
| `session_compare` | **10 s**（`session_compare.go:150`） | 17~19 s | **10.0 s 即 `context deadline exceeded`，只取到 104/171 行** |
| `session_summary_v2` | 30 s（`session_summary_v2.go:107`） | 17.3 s | 取满 171 行，无错 |

所以 compare 不是「慢」，是**对任何带正文的会话都在失败**。好消息是它不静默：
`querySessionBodiesByRequestID` 返回 `rows.Err()`，所以调用方拿到的是错误而不是
104 行的半份数据。

连接池上限实测 **16**。这才是雪崩的真正机制：单条 19 秒不是问题，**16 个并发
就把池占满 19 秒**，期间进程内所有其他查询（含完全不碰会话数据的端点）都在等连接。

### 8.3.2 本轮处置（2026-10-01 拍板「先只堵雪崩」）

在 `querySessionBodiesByRequestID` 这一个收敛点加**并发闸**：

- 容量 `maxConcurrentBodyFetches = 4`，刻意小于池上限 16，把大部分连接留给
  不碰正文的端点；
- 饱和时**快速失败**返回 `ErrBodyFetchSaturated`，**不排队** —— 排队等于把 17 秒
  的占用原样放大成雪崩，只是晚点发生；
- 两个端点把该错误映射成 **503 + `code: session_body_fetch_saturated`**
  （不是 500：调用方对两者的重试含义不同，503 可退避重试、500 不该重试）。

门 `admin/session_bodies_batch_gate_test.go` 钉的是**行为**不是数值：饱和时
必须在 250ms 内返回错误（实现成排队就会挂死并被门抓住）、释放后可再获取、
ctx 已取消时报 `context.Canceled` 而非饱和错误（否则排障会把「客户端已断开」
读成「服务端忙」）、并发下无数据竞争。

**没有动的**：10s / 30s 这两个预算本身。compare 的 10s 低于实测成本，怎么处理
属于「修法」的一部分，不在「堵雪崩」范围内 —— 现状是失败（不静默），闸保证的是
它**不会**在被拖垮的连接池上变成大面积 500。

**为什么不在本轮直接修**：这不是一行改动。候选方向各自有明确代价，需要拍板：

> ⛔ **本表已于 §8.3.3 被推翻，保留仅为记录当时的判断。** 四个方向全部建立在一个
> 未经检验的前提上——「列存分区上按 `request_id` 取数只能全扫」。实测不成立：
> 该分区有可用的 btree 主键，只是 `= ANY(数组)` 这个写法让规划器不去选它。
> 四行里没有一行命中真正的成本来源（正文列整列解压），改成 unnest 半连接即 8~9 倍。

| 方向 | 代价 / 风险 |
|---|---|
| 按 `request_id` 分区/加 bloom 或 min-max 索引 | 迁移级别改动；列存表加索引的方式与 heap 不同 |
| 拆查询：先按 `(request_id, ts)` 主键在**非列存**期取，其余回落到列存 | 只对「轮次时间落在非列存分区」的场景有效 |
| 接受现状并给端点加超时/熔断 | 把 19 秒变成 504，不是修 |
| 正文改从 `session_bodies_unified` 读 | 与 summary 注释里「两腿口径必须一致」的约束冲突，需重新评估 |

**当前处置**：门已从「恒绿且测不出任何东西」改成「会测、会打印实测成本、
在 P0 修法拍板前显式 `t.Skip` 并指向本条」。**不置红**是为了不把 CI 变成长期噪声，
但每次运行都会打印 `phase 2 实测成本：Execution Time: ... ms`，成本回归看得见。

### 8.3.3 根因定案：`= ANY(数组)` 让规划器放弃主键，改一个词就是 8~9 倍

**先排掉三个看起来很像的解释**，因为它们都会把修法引到错的地方：

| 假设 | 实测 | 结论 |
|---|---|---|
| 绑定参数导致规划器切到通用计划 | 同一 prepared statement 连跑 7 次，第 7 次 Planning 0.033 ms（确为通用计划），2026_09 分区**每一次**都是 ColumnarScan | ❌ 不是 |
| `EXPLAIN ANALYZE` 插桩放大了数字 | 真实生产函数 `querySessionBodiesByRequestID` 连测 **15.85 / 15.88 / 17.43 s**（200 个真实 id，取满 200 行），与 EXPLAIN 的 15.7~16.9 s 同量级 | ❌ 不是 |
| 取样偏差（我犯的） | 首轮取样用 `ORDER BY request_id LIMIT 171`，取到的是字母序最前的一小撮（全是 `0000…`），与真实会话「散布全空间」不符；改按真实会话 `gw_9d8182c6` 取 200 个 id（`baa1b79…`~`513171…`）后结论不变 | ⚠️ 取样确有偏差，但不影响结论 |

**根因**（同一批 200 个真实 `request_id`、同选三个正文列、真库、同分区）：

| 写法 | 2026_09 分区计划 | 实跑墙钟 |
|---|---|---|
| `WHERE rb.request_id = ANY($1::text[])` ← **当前生产** | `ColumnarScan`，`Rows Removed by Filter: 2,217,398` | **15.85 / 15.88 / 17.43 s** |
| `WHERE rb.request_id IN (SELECT unnest($1::text[]))` | **`Index Scan using request_logs_bodies_2026_09_pkey`** | **1.77 / 1.87 / 2.20 s** |

两者返回**完全相同的 200 行**。差别在 `= ANY(数组)` 这个形式本身：它让规划器
放弃主键探针，**即使数组里只有 1 个元素也一样**——实测 N=1/2/4/8/16/32/64
全部 ColumnarScan，而 `= '字面量'`（等值）会走 Index Scan。列存分区上
`request_logs_bodies_2026_09_pkey` 这个 btree 索引**确实存在也确实能用**
（`pg_index` 查得到，`enable_seqscan=off` 可强制走出 Index Scan），只是
`= ANY` 形态下规划器不选它。

**这解释了为什么「改查询形态」那一族方向看起来都该有用、却一直没被采纳**：
§5.6.1 早就测出 `IN (SELECT unnest)` 走索引（当时记作 7 ms），但生产代码用的
是 `= ANY`；注释里那张对照表**少了生产实际用的这一行**，于是「半连接已经是
对的形态」这个印象一直成立，而它其实不对。

**一个必须写下来的反面测量**：`SELECT count(*)` 包住同一个子查询只要 **2.0 s**。
这不是「其实不慢」，而是规划器把三个正文列的投影整个消掉了 —— 它量的不是同一
件事。我差点拿它把一个正确的 P0 撤回成「插桩开销」。**代理量陷阱的又一次实例。**

**代价归因**：`= ANY` 形态下，过滤本身约 2 s，其余约 14 s 花在把三个正文字段
在 2,217,398 行上整列解压（`Chunk Groups Removed by Filter: 0`，即无任何剪枝）。
所以**减少列数没有用**（实测 A≈B），**加时间窗也没有用**（§8.3 已记），
**加并发闸只是止血**。真正要动的是那个谓词写法。

**修法**（2026-10-01 已落地）：`session_bodies_batch.go` 的
`sessionBodiesByRequestIDSQL` 把 `= ANY($1::text[])` 换成
`IN (SELECT unnest($1::text[]))`。**真实生产函数复测**（同一会话 200 个
`request_id`，取满 200 条正文）：

| | 修前 | 修后 |
|---|---|---|
| `querySessionBodiesByRequestID` | 17.431 / 15.851 / 15.880 s | **4.166 / 2.907 / 2.235 s** |
| 真库门 `…BodiesStaysOnIndexPath` 整门耗时 | 17.61 s | **1.46 s** |
| 同一条的 `Execution Time` | 16,799 ms | **33 ms** |
| 2026_09 分区计划 | ColumnarScan | `Index Scan using …_2026_09_pkey` |

**`sessionBodiesByRequestIDAndTSSQL` 没有改，实测它本来就不在问题里**：
它早就是 `IN (... FROM unnest($1,$2) ...)` 形态，同一批 200 个键实测
`Index Scan using …_2026_09_pkey` / **101.302 ms**。拍板时我说的是「两条都改」，
动手前先测，发现第二条没有可改之处 —— 为凑数做一次装饰性改动只会让 diff
看起来比实际变更大。**这是「先量再改」的又一次兑现。**

守形门 `TestSessionBodiesBatchSQLIsNotALefiJoin` 加了三条判据：
禁 `= ANY(`（**`= ANY` 长得就像半连接，仅禁 `JOIN` 放行了那 15~17 秒**）、
必须含 `unnest(`、必须读 bodies 视图。变异验证：谓词退回 `= ANY` → 静态门红、
真库门红（33.13 s，报告 `fell back onto a columnar partition scan`）。

**真库门同时从「显式 Skip 等拍板」翻回红门，并加了一条正向锚点**：
只判「没看到 ColumnarScan」是**单边判据**——分区哪天变回 heap，它就会静默成立
并永远绿着，正是它过去的样子。现在同时要求计划里出现
`Index Scan … request_logs_bodies_2026_09_pkey`，找不到即红（防空转通过）。

### 8.3.4 这道门本身差点成为 R17 事故的第二起（已修）

R17（252 SQL 日志审计第十七轮）记录：一条同族的 19GB 级测量扫描在 4 核共享小机上
**独占 IO 1 小时 32 分**、load 20~25，把生产写入链整体饿死（05:31-07:05 冻结窗的
根因判定就是「主机饱和下的探测饿死」）。纪律 ㊺：共享小机上测量型长查询受负载
预算约束。

而 §8.3 这道门**就是那类语句的生产者**：`EXPLAIN (ANALYZE, BUFFERS)` 在列存分区
上实测 15.7~17.4 秒，而它由 `TEST_PG_URL` 决定打向哪台机器——那正是最容易被
「顺手指到 252/154」的一个环境变量。本轮实测同文件里那道等价性门更重（legacy
约 0.8 秒/轮，轮数上界 60）。

**处置**：`heavyMeasurementAllowed(dsn, optIn)`（`admin/db_measurement_host_guard_test.go`）
默认只放行回环 / 本地 socket，**解析不出主机的一律按非本地处理**（fail closed）；
远端需显式 `LLM_GATEWAY_ALLOW_HEAVY_DB_MEASUREMENT=1`。判据放在 `EXPLAIN` **之前**。

三项变异验证（均验证红、文件逐字节还原）：

1. 把闸从门上摘掉 → `TestHeavyMeasurementGuardIsActuallyWired` 红（Go 不报未使用的
   函数，一个没人调用的防护会永远绿着）；
2. 判定改成恒真放行 → 表驱动红 4 项；
3. 把闸挪到 `EXPLAIN` 之后 → 顺序断言红（`protects nothing in that order`）。

端到端复核：指向 `8.136.114.245` 时 0.00s 即 Skip，**连库都没试**。

### 8.4 S4 灰度前置核查（2026-10-01 10:2x 已批准开灰度，但退出条件未达成）

**本节只做只读核查，没有翻开关。** 翻之前先量了开关自己 spec 写的退出条件
（`settings/spec_storage.go` 的 `storage.request_logs_write_enabled`）：

> 关闭前提：dual_read_validator 对账 **7 天零漂移**（plan §4 S2 退出条件）

实测（252 生产，validator **本体的 scope SQL**，`db.MirrorDriftClassSQL` 同口径）：

| drift_class | 过去 24h 行数 | 会话数 |
|---|---|---|
| `internal_loopback` | 2,105 | 599 |
| **`genuine_loss`** | **341** | **332** |
| `non_terminal` | 6 | 5 |

**按字面口径，退出条件不成立**：24 小时内就有 341 行「本来该镜像却没有」。

**但这 341 行几乎全部是今天的事故，不是系统性缺口**（按小时分布）：

| 时段 | genuine_loss |
|---|---|
| 09-30 20:00 | 2 |
| 10-01 03:00（列存转换窗 02:42–04:13） | **326** |
| 04:00 / 05:00 / 06:00 | 11 / 1 / 1 |
| **07:05 恢复后 → 10:30** | **0** |

恢复后样本量（07:05→10:30，约 3.4 小时）：`request_logs` 2,343 行、
其中**带会话头 1,278 行**、`session_turns` 666 行，**genuine_loss = 0（0/1278）**。

**所以真正的问题不是「能不能开」，是「7 天零漂移从哪天起算」**：
- 字面口径 ⇒ 从现在重新起算，最早 2026-10-08 才够 7 天；
- 剔除已知事故窗（R17 已定性为列存写路径事故，且已如实登记为不可恢复）⇒
  时钟可从 2026-10-01 07:05 起算，但**目前也只有 3.4 小时干净证据，不是 7 天**。

**另有一条必须一起看的负面结论**（§5.5.3，本轮复核仍然成立）：无会话头流量
（探针、自检、未终态）**从未进入 session 族**（7 天 770,034 行）。S4 停写后
session 六表族成为唯一事实源，这批请求将**再无任何记录**。这不是漂移，是覆盖范围
差异 —— 停写不会让它们「漂移」，只会让它们**彻底消失**。

**当前状态**：开关仍为默认 `true`（持续双写）。已核实 252 上写入确实在进行
（`request_logs_hot` 最近 1 小时 616 行、最大 ts = 10:30；父表停在 02:16 只是
promote 未跑），因此停写不是空操作。**待你就「7 天从哪天起算」拍板后再翻。**

### 8.5 S4 停写的真实影响面：276 个调用点、110 个文件，而分类表只覆盖 14 个

§8.4 核的是「漂移够不够干净」。这一节核的是**停写之后谁会读到空**——
因为 `storage.request_logs_write_enabled=false` 的语义是
「request_logs(_hot) 与 bodies 双写停止」（spec 原文）。

**口径（可复现，先钉死再报数）**：

```bash
grep -rniE "from[[:space:]]+request_logs(_[a-z_]+)?\b" --include=*.go . \
  | grep -v "_test\.go" | grep -v "^\./docs"
```

| 指标 | 值 |
|---|---|
| 上述命令命中行数（含注释） | **277** |
| 其中**注释行**（`// … from request_logs …`）非调用点 | **40** |
| **真实生产读调用点** | **237** |
| 涉及文件 | **104** |
| §5.5.5/§5.5.8「视图依赖最终分类」覆盖的文件 | **14**（其中 9 个确实读 request_logs） |

真实调用点读到的表（钉住第三个维度，便于复核）：

| 表 | 调用点 |
|---|---|
| `request_logs_with_current_month` | 98 |
| `request_logs_hot` | 66 |
| `request_logs` | 52 |
| `request_logs_bodies_hot` / `_bodies_with_current_month` / `_without_request_class_due_at` 等 | 21 |

> ⚠️ **本节初版写的是「276 个调用点、110 个文件」——两个数都不对，已更正。**
>
> **更正过程本身值得记：** 我先后用三条命令得到 **119 / 238 / 277** 三个数，
> 却没把口径写进报告，就先把其中一个发了出去。
> - `request_logs(_[a-z_]+)?[[:space:]]`（要求表名后有空白）→ **119**：漏掉行尾写法。
> - 大小写敏感 + 不剔注释 → **238**。
> - 大小写不敏感 + 不剔注释 → **277**。
>
> **差值 39 的真身不是「大小写」，是「注释」**：40 条注释行里 36 条用小写
> `from`、3 条大写、1 条 `From`；而 237 条真实调用点里 **235 条是大写 `FROM`、
> 只有 2 条小写**。我第一版把差值归因成「大小写坑」，那是**错的归因**——
> 照那个归因去写守卫，加个 `-i` 就算解决，而真正要挡的是注释行。
>
> **教训**：数字与它的口径必须同生共死。「237 / 104」只在「命中 − 注释」这条
> 命令下成立，换个正则就不是这个数。

抽查最大的命中源 `admin/data_lifecycle.go`（15 处）确认口径无误：全部是
`COUNT(*)` / `pg_total_relation_size` / 分段统计这类**真读路径**，不是被
INSERT/DELETE 语句误捕。

> ⛔ **§5.5.8 标题写的是「分类补全：第一版表**不完整**，逐条补齐」——
> 这句话不成立。** 补齐后的表仍只覆盖 14 个文件，而实际读 `request_logs*` 的
> 生产文件有 104 个。**S4 停写会让这 237 处全部读到不再增长的数据，
> 而其中没有任何一处被逐点评估过。**

**⛔ 自动逐点分类不可信——这一条必须写下来，免得下一个人拿它当结论。**
我写了个脚本按「语句窗口内是否出现 `gw_session_id` / `request_id =`」自动分 A/B/C/D，
并拿 §5.5.5 里**已人工核定的 14 个点**做交叉验证，结果**判错了 5 个**：

| 文件 | 自动分类 | 既有判定 |
|---|---|---|
| `session_turns_tree.go` | C 全量流量 | **B 禁止迁**（`parent_request_id = ANY`） |
| `session_turns_unified.go` | C 全量流量 | **B 禁止迁** |
| `compression_sessions.go` | A 会话内读 | C 按设计排除 |
| `no_topic_session.go` | B + C | C 禁止迁 + D 正文腿 |
| `unified_detail.go` | B×5 + **C×1** | B（那个 C 是误判） |

**错因**：判据只认 `request_id =`，不认 `parent_request_id = ANY(...)`，
窗口又没兜住谓词，于是把「禁止迁」判成了「无原生源等价物」——**恰好是最严重的一档**
（C 意味着停写后永久落空，B 只是不许迁）。**自动分类的逐点结论一律不采信，
只有「总数 237 / 104」是硬事实。**

（交叉验证里另有 5 个文件自动扫到「无匹配」：`session_compare.go`、`session_list.go`、
`session_online.go`、`turns_sessions.go`、`session_export.go`——它们正是审计标注
**已迁原生源**的那批，迁完就不读 request_logs 了。这是**正向信号**，不是漏扫。）

按谓词分两类（抽样确认）：

**会话内读**（`WHERE gw_session_id = …`）—— 读的数据在 session 族里有对应，
但**这些代码仍写死 `FROM request_logs`**，S4 后会读到空：
`analysis/request_summary.go:130`、`analysis/optimizer.go:195-197`、
`gateway/output_compliance_control.go:84`、`gateway/main_v3_wiring.go:107`、
`hooks/goal/history_store.go:84`（目标/审计钩子用它重建对话全文）。

**全量流量读**（无会话头谓词）—— **没有原生源等价物**，S4 后必然落空：
`routeincident/store.go:704`（按分钟桶的 24h 请求/错误看板）、
`streaming/model_alternatives.go:230`（已改读 `request_logs_hot`，其注释自陈
「every caller cancelled at the client timeout … the feature never returned」）。

**最要命的一条：S4 会关掉它自己的观测手段。**
`cmd/gateway/dual_read_validator.go:203/243` 读的正是 `request_logs_hot` 与
`request_logs`。而 spec 的退出条件恰恰是「dual_read_validator 对账 7 天零漂移」——
**停写之后 validator 再也测不出漂移，「零漂移」这个前提与「持续验证」在设计上
自相矛盾**。灰度期必须保留写入（只读不比对意义不大），或者把 validator 换源，
这是翻开关之前必须先解决的一条，不是事后能补的。

**已落地的防漂移守卫**（`admin/request_logs_read_inventory_test.go`）：

| 项 | 值 |
|---|---|
| 登记文件 | **104** |
| 登记调用点 | **237** |
| 门 | `TestRequestLogsReadInventoryIsComplete` |
| 扫描口径 | 与上文 grep 一致（大小写不敏感、剔注释、排除 `_test.go` 与 `docs/`） |

这道门**只数个数，不下判定**——因为自动分类已被证伪（见上）。它的作用是
「把覆盖面钉住」：分类表最大的毛病不是判错，而是**自称补全却从没人核过它
覆盖了多少**。现在新增一个读 request_logs 的文件、或某个文件读点数变了，
门立即变红，逼一次显式复核。

**两个方向都判红**，只判新增看起来像覆盖、而真实风险是双向漂移：
新增文件（表里没有 ⇒ 有人开始读一张 S4 将停写的表）/ 计数变化 / 表里的文件
不再有调用点（表已陈旧 ⇒ 所有从它推出的数字也陈旧）。

四项变异验证（均验证红、文件逐字节还原）：

| # | 变异 | 结果 |
|---|---|---|
| 1 | 表内文件加一处读 | 红：`table says 1, code has 2` |
| 2 | 表外文件加第一处读 | 红：`1 production file(s) … absent from requestLogsReadInventory` |
| 3 | 表内文件删掉唯一一处读 | 红：`table says 1, code has 0` |
| 4 | **只加一行注释** `// legacy: … from request_logs …` | **绿**（证明注释过滤器不是装饰） |

> 变异 4 是这道门自己的「非恒绿」证据：40 条注释行里 36 条是小写 `from`，
> 若注释过滤器坏了，#2 那类新文件会被误报、而纯注释改动会让表凭空漂移。
> 变异 4 绿 ⇒ 过滤器确实在干活。

**它不能替代逐点评估。** 门保证的是「缺口扩大时会有人知道」，
不是「缺口已被评估」。后者仍是 104 个文件的活。

**结论**：S4 不是「漂移干净了就能翻」。它还缺三件事——
① 104 个文件的依赖分类（**仍未做**，是当前最大的一块未开工工作）；
② validator 换源或明确「灰度期不验漂移、只验可观测性」
（**2026-10-02 已做，见 §8.8**）；
③ §8 第 3 项的全量流量口径决定（决定 routeincident / analytics 这类看板是
保留 v1 冻结分支，还是接受停写后的空）。§8 第 1 项与第 3 项是**同一条链上的
前后两段**，不能分开拍板。

### 8.6 §8 第 4 项重测：641,452 的真实构成 —— **没有镜像缺口，94% 从来就没有会话头**

第 4 项问的是「S6 DROP `request_logs` 前，那 641,452 个在原生源查不到的
`request_id` 怎么办」。本节按当前基线重测（**不引用旧数**），并把构成拆到
**可判定的粒度**。

**口径**（本机 `llm-gateway-pg`，视图跨度 2026-09-03 15:28 → 2026-10-01 10:44）：

```sql
SELECT (CASE WHEN rl.gw_session_id IS NULL OR rl.gw_session_id='' THEN 'no_sess_header'
             ELSE 'has_sess_header' END), (db.MirrorDriftClassSQL), count(DISTINCT rl.request_id)
  FROM request_logs_with_current_month rl
 WHERE rl.request_id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
   AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
 GROUP BY 1,2;
```

| 指标 | 本轮实测 | 审计原记录 | 差 |
|---|---|---|---|
| 视图侧 `request_id` 总数（DISTINCT） | **2,324,470** | 2,321,464 | +3,006（+0.13%） |
| 原生源查不到 | **643,387（27.68%）** | 641,452（27.6%） | +1,935 |

**构成（合计 643,387，逐项闭合）**：

| 会话头 | drift_class | request_ids | 占缺失 |
|---|---|---|---|
| **无会话头** | （见下方告警） | **605,140** | **94.05%** |
| 有会话头 | `internal_loopback` | 36,693 | 5.70% |
| 有会话头 | `non_terminal` | 1,536 | 0.24% |
| 无会话头 | `non_terminal` | 18 | 0.003% |
| **有会话头** | **`genuine_loss`** | **0** | **0%** |

> ⛔ **结论：没有镜像缺口。** 「有会话头、镜像钩子本该写、却没写」这一类在本机是
> **0 行**。整个 64 万缺口由两部分构成：**94.0% 的行根本没有会话头**（与 §5.5.3
> 的结论一致：探针/自检/未终态从未进入 session 族），其余 5.9% 是
> `internal_loopback`（标题/摘要生成器回环）与 `non_terminal`（`in_progress`
> 占位）——**都是按设计排除**。
>
> **所以第 4 项不是数据质量问题，是纯粹的「覆盖范围 / 产品口径」决策**：
> 产品是否接受「S4 停写后，约 27.7% 的请求级记录、其中 94% 无会话头，将彻底
> 不再存在」。答案不是技术问题。

### 8.6.1 一个必须写下来的口径陷阱：`genuine_loss` 在无会话头行上是**假标签**

我第一次跑这条查询时**漏了 `gw_session_id IS NOT NULL`**，得到的画面是
`genuine_loss` 605,130 行（94%）——看起来像一场巨大的镜像丢失，**完全错误**。

原因：`db.MirrorDriftClassSQL` 的 `genuine_loss` 是 **ELSE 兜底分支**。
`dual_read_validator` 的 scope SQL（`cmd/gateway/dual_read_validator.go:196`）
自带 `gw_session_id IS NOT NULL AND gw_session_id <> ''` 过滤，所以在那里
`genuine_loss` 才有「本该被镜像却没被镜像」的语义。**一旦脱离那个过滤，
所有无会话头的行全部落进 ELSE，被贴上 `genuine_loss` 标签。**

⇒ **引用 `MirrorDriftClassSQL` 的任何查询，都必须自己带上会话头过滤**，
否则会得到一个数量级正确、语义完全错误的「镜像丢失」结论。
（本节两个数字相差 605,130 vs 0，就是这条陷阱的全部代价。）

**它同时是 252 的对照**：§8.4 在 252 实测 24h `genuine_loss` = 341 行——那一侧
**带了**会话头过滤，所以那 341 行是真的（且集中在 R17 事故窗）。两个数字不矛盾，
口径不同。

### 8.6.2 这给 §8 第 3 项提供了确切数字

§5.5.3 只给了「−37%」，单位是**行数/7 天/带会话头过滤的查询**。本节给的是
**不同单位**（DISTINCT `request_id` / 全量视图 / 不过滤），两个数不能互换：

- 全量口径：丢弃 `request_logs` = **抹掉 27.68% 的请求级记录**；
- 其中 **94.05% 是无会话头流量**，它们在 session 族里**没有任何对应物**，
  迁原生源救不回来，只能在产品口径上决定「要不要这 27.7%」。

### 8.7 S4 指定的观测工具本身：可用，但默认一次要 ~37 秒，其中 ~25 秒是纯冗余

S4 的方案是「用 `GET /api/admin/sessions/dual-read-drift?hours=168` 盯 `s4_ready`」。
既然整条 S4 路径都押在这个端点上，**它自己跑不跑得动必须先验**，不能等到灰度当天
才发现。本节只读本机容器（生产 252 当前 load 12.36/4 核，不在它上面做测量——
R17 纪律 ㊺：共享小机上测量型长查询正是今天饿死生产写链的那一类）。

**成本曲线**（本机 `llm-gateway-pg`，数据规模与生产同量级：7 天带会话头 387,734 行）：

| 窗口 | V1Rows（带会话头） | `driftBuckets` 本体耗时 |
|---|---|---|
| 1h | 79 | 0.105 s |
| 24h | 2,907 | 0.398 s |
| **7d（S4 默认）** | **387,764** | **11.23 s / 12.54 s** |

**`Summarize` 在默认 168h 窗一共跑 4 条查询**（`cmd/gateway/dual_read_validator.go`）：

| # | 查询 | 7d 实测 |
|---|---|---|
| 1 | 分母 `V1Rows` | 0.90 s |
| 2 | 分类 `GROUP BY drift_class` | 11.23 s |
| 3 | `driftBuckets(work_type)` | 12.54 s |
| 4 | `driftBuckets(request_status)` | 同形未单独测 |

**合计 ≈ 37 秒**（第 4 条按同形推算，**推算不等于实测**，已标明）。

**好消息**：网关 `ReadTimeout: 300s`、`WriteTimeout: 0`（`cmd/gateway/main.go:7257-7258`），
handler 用 `r.Context()` 且**没有自己的超时**。所以 ~37 秒**能活下来，端点不是坏的**。

**但有一个白拿的 2/3 冗余**：第 2/3/4 条查询**内嵌的是同一个
`mirrorDriftScopeSQL` 常量**——那 7 天、387,764 行、每行两次索引探针的
anti-join 扫描**被完整跑了三遍**，只有外层 `GROUP BY` 的键不同。
把它算成一次（CTE / `GROUPING SETS`）再分三组聚合，成本可从 ~37 s 降到 ~12 s。

**为什么现在值得说**：灰度期正是要**反复**调这个端点的时段（盯 `s4_ready`），
37 s × 反复调用，在 4 核共享小机上就是 R17 今天那类负载。稳态下没人碰它，
所以这个冗余一直没暴露。

### 8.2 第 2 项重测：`session_list` 迁原生源的**真实**代价

旧结论「迁过去会让 `request_count`/`error_count`/`is_compressed` 变小 2.5%」是
上一轮的口头数字，本轮按纪律在真库重测（`default` 租户，近 3 天窗口，PG 17.10）。
`loadSessions`（`admin/session_list.go:132-180`）现在读
`request_logs_with_current_month`，对照口径 = `session_turns_hot UNION ALL session_turns`。

| 口径 | v1 视图（现状） | 原生源（迁后） | 差 |
|---|---|---|---|
| 会话数 | 15,088 | 13,660 | **−1,428（−9.46%）** |
| `request_count` 合计 | 15,710 | 14,156 | −1,554（−9.89%） |
| 只在 v1 出现的会话 | 1,428 | — | 会话整条消失 |
| 只在原生源出现的会话 | — | **0** | 无凭空新增 |

**关键：消失的 1,428 条是什么？**

| class | 会话数 | 轮数 |
|---|---|---|
| `internal_loopback` | 1,354 | 1,445 |
| `non_terminal` | 73 | 73 |
| **`genuine_loss`（真业务轮次）** | **0** | **0** |

即：**没有任何一条真实用户对话会从列表里消失**，消失的全是网关自己生成的
标题/摘要 LLM 调用与会话开始时的 `in_progress` 占位。

**共有会话的逐项等价性**（13,659 条）：

| 字段 | 结果 |
|---|---|
| `request_count` | 仅 **6** 条会话不同，合计 +35 轮（0.07%） |
| `error_count` | **逐会话全等**（v1 9,323 = 原生 9,323） |
| `is_compressed` | 两边同为 **1,418** 行有 `compression_strategy` |
| `total`（会话去重计数） | `COUNT(DISTINCT gw_session_id)` 与原生源同口径 |

`error_count` 语义差异是本轮特意验的疑点：v1 写 `request_status = 'failure'`，
原生源只有 `NOT success`。实测 v1 的 `request_status` 分布为
`failure 9,339 / success 6,262 / in_progress 108`，两式在 13,659 条共有会话上
**完全相等**（`in_progress` 那 108 行未被任何一边计入 `error_count`，因为镜像
只写 `success = true`）。

**结论**：这一项的性质已经从「迁过去数字会缩水」变成
「**1,428 条纯内部会话会从用户可见列表里消失**」。按语义这是**修正**而非退化，
但它确实是可见的行为变更，故仍需拍板。`is_compressed` / `error_count` /
`model_used` 三个字段都有可用替身，不是迁移的障碍。

### 8.1 第 7 项：`session_summary_v2` 的正文几乎全是空

§5.9.2 实测：它的查询用 `(request_id, ts)` 配对正文，而 `request_logs_bodies.ts`
是**正文写入时间**，两者 99.85% 不等，所以绝大多数轮次取不到正文。

改成 `request_id` 口径是**一行改动**，但它不是重构，是行为变更：
总结的输入正文会从「几乎全空」变成「有内容」，直接改变 LLM 总结的结果，
也可能改变 token 计费与缓存命中。

- 不改：端点保持现状（但要知道它长期在用空正文做总结）。
- 改：与 `session_compare` / `session_export` / `sessionforensics` /
  `session_title` 四个既有口径对齐，一致性更好。

**建议改**，但需要你确认影响面可接受。改动本身已就位（两个函数并存），
只差把 `queryRequestLogsFallback` 从 `…AndTS` 切到 `…ByRequestID`。


### 8.8 【P0，2026-10-02】S4 停写后，`s4_ready` 会**永久真空为真**——前置判据自己关掉了自己

§8.5 记的是「S4 会关掉它自己的观测手段」：validator 读 `request_logs_hot` /
`request_logs`，而 spec 的退出条件恰恰是「dual_read_validator 对账 7 天零漂移」。
本节把这句话落到具体的失效形态，并已修复。

**根因**（`cmd/gateway/dual_read_validator.go` 的 `Summarize`）：

```go
sum.S4Ready = sum.GenuineLossRows == 0     // 修复前
```

`GenuineLossRows` 的定义是「有会话头、但在 session 族里找不到对应行的 V1 行」。
**停写之后，窗口内 V1 行恒为 0，于是无行可缺，`GenuineLossRows` 恒 0，
`S4Ready` 恒 true** —— 与镜像是否健康无关。

**真库实测**（PG 17.10，用 `Summarize` 的生产原句 `mirrorDriftScopeSQL`，
窗口取在 V1 无行的区间——这正是关停超过一个窗口后的真实形态）：

| | 场景 A：当前态（过去 7 天） | 场景 B：窗口内零 V1 行（= 停写后） |
|---|---|---|
| `V1Rows` | 245,460 | **0** |
| `V1RowsWithoutTurns` | 9,635 | **0** |
| `GenuineLossRows` | 见下 | **0** |
| `S4Ready`（修复前） | 需分类 | **true** ← 真空为绿 |

**为什么这条比一般的「观测失效」严重**：它是**唯一会给出「可以关写」许可的信号**。
它的失效方向是许可而不是告警——关停的那一刻起它就永久报绿，且响应 JSON 与健康态
**逐字节相同**（`s4_ready: true`，其余字段全 0）。没有 `void` 概念，调用方无法把
「没验证」与「验证了、没漂移」区分开。

**同一根因的另外两处，一并处置**：

1. **空扫描判为空**。即使不关停，只要窗口内 V1 流量为 0（静默集群、租户过滤命中
   0 条），`s4_ready` 同样真空为真。「扫到 0 个问题」和「扫了 0 个」必须分开。
2. **`ZeroDrift` 恒假**。`CompareDetail` 是反方向：停写后 v1 冻结、v2 继续增长，
   `OnlyInV2` 必然单调增长 ⇒ `ZeroDrift` 永久为 false。而 spec 的退出条件正是
   「7 天零漂移」，于是**这道门在停写之后永远无法宣告完成**。恒假与恒真同样无用。

**修法**（`cmd/gateway/dual_read_gate.go`，判定抽为纯函数以便无库测试）：

```
ready ⇔ (v1 写入中) ∧ (窗口扫到过 V1 行) ∧ (无真漏写)
```

并把「没能评估」与「评估了、有漂移」分成两个字段：
`S4GateVoid`（附机器可读的 `S4GateVoidReason`：`v1_writes_disabled` /
`no_v1_traffic_in_window`）与 `S4Ready`。把两者都折成 `s4_ready=false` 会把
「不知道」伪装成「不安全」——那是同一个 bug 的镜像版本。
`DualReadDetail` 侧对应新增 `ZeroDriftEvaluable` / `V1WritesEnabled`。

**门控读数必须来自设置，不能靠启发式**：没有采用「比较两侧 `MAX(ts)` 差值」这类
带阈值的推断，而是新增 `settings.KeyRequestLogsWriteEnabled` /
`settings.RequestLogsWriteEnabled()`。顺带收掉一个既有分裂面——该键此前在
**4 个包的 4 处**各写一遍字面量（`admin/telemetry.go`、
`cmd/gateway/lite_telemetry_sink.go`、`domains/hooks/observability/telemetry/client.go`、
`internal/trace/trace.go`），而 `admin` 自己的注释已经点名了这个失效形态
（「门内联两遍…就是分裂入口」），只是那次去重只做了包内。

**顺带修正一道把缺陷钉成「不变式」的门**：`dual_read_validator_pg_test.go` 的
**I5** 原本写的是 `S4Ready == (GenuineLossRows == 0)`——它对缺陷前的实现永远是绿的。
一个把 bug 写成不变式的门，比没有门更坏：它给后来者「这里有覆盖」的错觉。
现在 I5 复算完整契约，并新增 **I5c `void ⇒ ¬ready`**——**数据无关**，不依赖窗口里
恰好有没有漂移行，因此永远会被求值（I6 那条栽过一次「数据依赖 ⇒ 变异仍绿」）。

**变异验证（7/7 转红）**：

| # | 变异 | 结果 |
|---|---|---|
| 1 | 删掉停写判据 | 红 |
| 2 | 删掉空扫描判据 | 红 |
| 3 | `Summarize` 改回直接赋值（绕过 verdict） | 红 |
| 4 | `ZeroDrift` 可评估性判定挪到赋值之后 | 红 |
| 5 | 某生产文件把键改回字面量 | 红 |
| 6 | spec 表给键改名而常量不跟 | 红 |
| 7 | 回落默认改成 `false` | 红 |
| 8 | **真库门在缺陷代码下**（真实 Summarize + 真实 SQL + 真实数据） | 红，并复现原症状 |

真库门的对照输出（同一库、同一窗口、同一段 SQL，只换门控读数）：

```
基线(写入中): v1=2547 noTurns=4 genuine=0 ready=true  void=false reason=""
关停后:      v1=2547 noTurns=4 genuine=0 ready=false void=true  reason="v1_writes_disabled"
```

注意**漂移数字逐位不变**（2547 / 4 / 0）——门控只改变「能不能这么声称」，
不改变「实际扫到了什么」。这条对照本身也是一条断言（真库门里已钉住）：
若哪天为了让门翻转去动 SQL，它会立刻红。

#### 8.8.1 一条必须写下来的教训：守卫写宽会误伤**我自己的正确实现**

`TestSummarizeNoLongerAssignsS4ReadyDirectly` 第一版禁的是 `sum.S4Ready =` 这个
**前缀**——而修好之后的正确代码 `sum.S4Ready = verdict.Ready` 同样以它开头，
于是这道门对自己的正确实现报红。

正确形态是**数赋值点并核对右值**（恰好 1 处、且必须是 `verdict.Ready`），
而不是禁前缀。前者对「顺手简化回直接判定」照红，后者则永远不会绿。

被自己的门拦下这件事本身是好事：它说明这道门在跑，而不是等别人来发现。
但它也说明**「禁某个子串」这种写法在 Go 里几乎没有安全形态**——
赋值语句天然以 `X =` 开头，与被禁的形态同前缀。

#### 8.8.2 扫描基准错了，而守卫立刻替我抓到了

键唯一性守卫第一版从包目录往上走了**两级**，即仓库的**父目录**。它报出 7 处
「违规」，其中包含一个**隔壁 checkout**（`llm-gateway-go-2`）的文件。

这是「守卫在报假警」还是「我基准错了」的典型二选一。判据：**先看它报的路径是否
在本仓库内**——一眼可辨。改成往上一级后，真实违规数从 7 降到 2，且那 2 处正是
白名单里的常量声明与 spec 表。

**教训**：全仓扫描型守卫，基准路径要当成断言的一部分钉住；
第一次跑出来的「违规清单」必须先核对**目录归属**，再决定是改守卫还是改代码。

#### 8.8.3 本轮自己制造并已回退的一次事故（记在这里而不是只记在 handoff）

为格式化自己新写的三个文件，我执行了 `gofmt -w cmd/gateway/` —— **按目录**。
结果是该目录下 **12 个**与本任务无关的文件被一并重排，其中几个是**并行会话的
在途改动**（`main_dispatch_observation.go`、`plugin_lifecycle_init.go` 等）。

暴露它的是提交前的 `git status --porcelain`：多出 12 行我没打算改的文件。
回退方式不是「凭印象挑几个」，而是

```bash
git diff --name-only HEAD -- '*.go'        # 全部改动
# 减去自己打算改的清单 → 其余即附带改动
git checkout HEAD -- <其余>
# 再逐个 git diff --quiet 核已逐字节还原
```

与本项目此前记录的「批量还原事故」是同一类，方向相反：那次是**批量还原**误伤
源文件，这次是**批量格式化**误伤格式。两者都只靠 `git status` 才看得见，
所以「范围写宽的批量命令之后必须跑一次 `git status --porcelain`」不是谨慎，
是必要步骤。

另注：`docs/audit/2026-10-01-rebuild-vs-inplace-feasibility.md` 是另一条工作线
的未跟踪产物，**未纳入本轮提交**。并行检出下「工作区里出现的东西」不等于
「我改的东西」。

## 9. S4 停写影响面：从「104 个文件待评估」到一张可行动的四分表（2026-10-02）

§8.5 的结论是：读 `request_logs` 的生产文件 **104 个、调用点 237 处**，而逐点评估
**一个都没做过**。本节开工，并当场**推翻了本轮自己差点照单全收的一个结论**。

### 9.1 关键前提：710 视图不是 v1 表的别名，它有 session 臂

`request_logs_with_current_month` 的真库定义（`pg_get_viewdef`，非读注释）：

```
= session_turns ∪ session_turns_hot ∪ request_logs ∪ request_logs_hot
```

**停写只冻结 v1 那两条臂，session 那两条臂继续增长**（镜像链不归 S4 管）。
本地库 24h 实测：视图 7,221 行 = session 臂 2,639（36.55%）+ v1 臂 4,582（63.45%）。

> ⚠️ **我差点把 30 个视图族文件全判成「静默变空」，那是系统性错误。**
> 判空的依据是「停写 ⇒ v1 不再增长 ⇒ 查不到」，而它漏掉了 session 臂仍在供数。
> 后果不是某个文件判错，而是**一批实际还能用的读点被误报成最危险档，
> 把真正该处置的 44 个基表读者淹没在噪声里**。

这条错误不是靠「再想一遍」发现的，是靠**一道族门**发现的（§9.4）——它也发现了我
**自己**写在登记里的同款错误（`bg/integrity_fingerprint_drift.go`、`domains/routeincident/store.go`）。

### 9.2 四分表：按「读哪一族表」机械分类（不靠判断）

剥注释后按模式判定（`admin/request_logs_stop_write_classification_test.go` 的
`sourceFamilyOf`）。四族的本质差别是**有无 session 侧兜底**：

| 族 | 文件数 | 停写之后 | 判据 |
|---|---:|---|---|
| **bodies 族** | **26** | **硬失败，无任何退路** | bodies 只有 v1 一份；`session_bodies` 只有增量、无 `final_full` 全量（§6） |
| **710 视图** | **30** | 静默少计（session 臂仍供数） | 视图含 session 臂 |
| **基表 `request_logs`/`_hot`** | **44** | **完全停止增长** | v1 专有，无 session 等价物 |
| 视图+基表都读 | **29**（含 bodies） | 部分退化 | — |

合计 104，**每个文件都落进某一族，0 个未定**。这张表比「逐点判断后果」更可靠，
因为它是**机械判定**：测的是「按代码测出的族」与「按代码测出的族」相等，
没有解释空间，所以不会恒绿。

### 9.3 少记的那部分是什么：99.8% 是探针流量，不是业务

v1 独有的 4,580 行（24h）拆开：

| 构成 | 行数 | 占比 |
|---|---:|---:|
| `task_type='probe_triggered'`（探针/自检流量） | **4,570** | 99.78% |
| `request_status='in_progress'` 非终态占位（按设计不镜像） | **10** | 0.22% |
| **有会话头却真漏写** | **0** | 0% |

所以 S4 的代价**不是丢业务数据**，而是「analytics 里还看不看得见探针流量」。
这正好把 §8 第 3 项（全量流量聚合口径）从「抽象的产品口味问题」变成
「有数字的问题」：**放弃的是 63% 的近期行，其中 99.8% 是探针流量**。

> **口径声明（必须与上表分开读）**：63.45% / 36.55% 这些**幅度**是**本地开发库**
> `llm_gateway` 的 24h 窗口实测，生产库的业务/探针配比可能不同。
> 可移植的是**结构性事实**（视图有 session 臂 ⇒ 视图读者不会查空）与
> **少记部分的构成**（≈99.8% 是探针与占位行）。

### 9.4 两道门，以及它们各自抓到的错

| 门 | 作用 | 抓到的错 |
|---|---|---|
| `TestRequestLogsStopWriteSourceFamilyCoversInventory` | 机械四分表；验证四族合计 == 清单条数 | 剥注释这一步被变异验证承重（去掉后 view 30→26、base 44→41、4 个文件被带偏） |
| `TestStopWriteEffectAgreesWithSourceFamily` | **族 × 档位的合法组合**：710 视图族不得判 `silently_empty` | 抓出我登记里两处同款错误；也拦住了子代理对 30 个视图族文件的系统性误判 |
| `TestRequestLogsStopWriteClassificationEvidenceIsReal` | 每条登记的 `Evidence` 必须在该文件里逐字存在 | 这是「已评估」与「凭印象」的分界 |
| `TestRequestLogsStopWriteNothingLeftUnclassified`（`s4audit` tag） | 未评估必须为 0 | 现在红：6/104 |

**为什么最后一道门单独放 build tag**：它是「把一件事做完」的闸，不是「防止变坏」的
守卫。常红只会挡住所有人，却不会让那 98 个文件被评估；条件 `Skip` 更坏——把未完成
伪装成通过。`s4audit` 给出第三条路：**默认不挡路，显式调用时绝不放过**，进度由
常跑的 `…Progress` 打日志（`6/104 已评估，未评估 98 个`）暴露。

### 9.5 又一次自摆的乌龙，以及它为什么值得记

临时查询先报「有会话头却未镜像 = **10 行**」，数字看着像新 P0。逐行看才发现
**10 行全是 `request_status='in_progress'` + `error_kind` 为空**，即文档记载的
`non_terminal` 按设计排除桶，真值仍为 **0**。

错因：**自造了一条判据**（`gw_session_id IS NOT NULL`），没用
`db.MirrorDriftClassSQL`。而审计 §5.5.6 早就写过「复用诊断口径的分类表达式前，
先确认标签方向」——**同一个坑，当场又踩了一次**。

教训：**当「某个量应该为 0」的查询返回了非 0，第一反应应该是「我的判据对吗」，
而不是「发现了新缺陷」**。特别是当仓库里已经存在一个成熟的分类表达式时。

### 9.6 当前状态与下一步

**已完成**：机械四分表（104/104 落族）、两道新门 + 变异验证、6 个文件的人工核实
分级（含两处自我更正）、少记构成的量化。

**未完成**：98 个文件的后果分级。三份子代理批量评估已产出（admin 50 / domains 13 /
bg 17），但**其中所有视图族判定需按 §9.1 重判**——这正是族门存在的意义。
下一步应按族分批做：**先 44 个基表族 + 26 个 bodies 族**（真正会断的），
视图族可最后做且多数结论会是「少计探针流量」。

⛔ **S4 仍不可开**：§8.5 三处硬阻塞的 ①（逐点依赖评估）刚开工，且本节把
「停写会少记 63% 近期行」这一新事实摆到了台面上——它需要你先就 §8 第 3 项表态。
