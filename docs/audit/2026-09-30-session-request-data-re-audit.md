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

### 9.7 第二次自我推翻：视图族不能靠**视图名**判，要靠**视图真实构成**

§9.1 说「710 视图有 session 臂 ⇒ 视图族停写后仍供数」。这条**被我用名字模式
实现成了 `request_logs_with_[a-z_]+`**，然后它就错了。

真库逐个视图查 `pg_get_viewdef`：

| 视图 | 真实构成 |
|---|---|
| `request_logs_with_current_month` | **HAS_SESSION_ARM** |
| `request_logs_with_current_month_without_customer_id` | **V1_ONLY** ← 我判成视图族了 |
| `request_logs_with_current_month_without_request_class_due_at` | **V1_ONLY** ← 我判成视图族了 |
| `request_logs_bodies_with_current_month` | V1_ONLY（bodies 族，本就无 session 臂） |

后两个是 577/734 迁移**故意**建成的 v1-only 中间层 —— `admin/auto_route.go` 的注释
写得很清楚：顶层视图的 session 分支 `provider_id` 投影为 NULL，按 provider 过滤会
静默丢行，所以刻意绕开顶层去读中间层。**这是一个正确的设计取舍，不是迁移遗漏**；
而我的名字匹配把它当成了视图族。

**影响面**：7 个生产文件引用这两个视图（`admin/auto_route.go`、
`admin/auto_route_correlations.go`、`admin/attempt_quality_api.go`、
`admin/analytics_materialized.go`、`admin/board_time_range.go`、
`admin/usage_trend_series.go`、`bg/mv_consistency.go`），它们的停写后果从
「仍供数」变成「**完全停止增长**」。

**修正后的四分表**（白名单化，104→**105** 文件 / 237→**239** 调用点，
增量为并行会话新增的读者，已被 `TestRequestLogsReadInventoryIsComplete` 正常跟踪）：

| 族 | 修正前 | **修正后** |
|---|---:|---:|
| bodies 族（无兜底，硬失败） | 1 | **1** |
| 有 session 臂的视图 | 30 | **27** |
| 基表 + v1-only 视图（完全停止增长） | 44 | **47** |
| 视图+基表都读 | 29 | **30** |

**改法**：族判定不再匹配视图名，而是查一张**白名单**
`requestLogsViewsWithSessionArm`；白名单默认是空的 —— 新增 v1-only 视图会**自动
落进 base 族**（保守、正确的方向），要改成视图族得显式加进来并说明理由。

**白名单由真库门钉住**（`cmd/gateway/request_logs_view_session_arm_pin_test.go`，
`TestRequestLogsViewSessionArmPinIsCurrent`）：从 `pg_get_viewdef` 重算，与白名单
**双向**比对。手工名单会过期、视图定义会被后续迁移改写，两个方向都要抓：

- 视图新增/变为含 session 臂 → 报「未登记」
- 视图变为不含 session 臂 / 被删 → 报「清单陈旧」

变异验证：白名单塞入不存在的视图 → 红（报陈旧）；把有臂视图从白名单删掉 → 红
（报未登记）；还原后绿。真库实测输出：`含 session 臂 1 个，纯 v1 3 个`。

**这一节是 §9.1 的续集，两次都是同一类错**：把「一个名字对应一个语义」当成事实，
而实际语义住在数据库里。**名字是索引，不是定义。** 凡是用正则/名字模式去判定
「读的东西在停写后还在不在长」，都必须由真库定义来裁定。

### 9.8 第三次分类修正：`familyMixed` 把两件本质不同的事混在一起

§9.7 修完视图名问题后，四分表里剩下一个 `mixed` 桶（30 个文件）。它把两类**后果
相反**的文件放在了一起：

- **view + base**（5 个）：视图腿由 session 臂继续供数，基表腿冻结 ⇒ **部分退化**。
- **bodies + 其他**（25 个）：主轮次腿可能照常工作，但**正文腿没有任何 session 兜底**
  ⇒ 表现为「拿得到轮次、拿不到正文」，**不是部分退化**。

「一并读」不等于「部分退化」——这是 §9.7 之后我自己犯的第三类分类错误（前两次：
把视图名当定义、把 bodies 藏进 mixed）。

**拆开后（105 文件）**：

| 族 | 文件数 | 停写之后 |
|---|---:|---|
| `reads_bodies_family` | 1 | 正文无兜底 ⇒ 硬失败 |
| `reads_bodies_plus_other` | **25** | **正文腿硬失败**（轮次腿可能照常） |
| `reads_710_view_only` | 27 | session 臂仍供数 ⇒ 静默少计 |
| `reads_base_tables_only` | 47 | 完全停止增长 |
| `reads_view_and_base` | 5 | 部分退化 |

**族门随即又抓到我自己一处错**：`admin/data_lifecycle.go` 初判
`unaffected_by_stop_write`（理由「生命周期/体量/保留期只读存量」）。逐行核实后，
它的 7 天**增长趋势**里有一条 bodies 腿（:226-241，
`COUNT(DISTINCT request_id) ... outbound_body IS NOT NULL`）。⇒ 停写后 requests
趋势（走 710 视图）继续增长、compressed 趋势冻结，**两条线分叉且无错误信号**。
比「整个端点冻结」更隐蔽：页面照常 200，只有一条线停住。已改判 `silently_frozen`。

**这已经是族门第三次抓到我自己的判定错误**（`integrity_fingerprint_drift`、
`routeincident/store.go`、`data_lifecycle.go`）。这本身就是一条值得记的经验：
**分层的机械判据（族）比人的判断（后果）可靠得多**，因为它可复现、可穷举、
无解释空间；把人的判断挂在一道机械判据下面，错误就变得可见且可回归。

## §9.9 读端分级表的坐标系里没有「控制面」这一维（2026-10-02）

§9.8 的四分表 + 五档分级覆盖的是**响应面**：停写之后，接口返回 200 还是 500、
结果集空不空、冻不冻结。这一整轮分级（105 文件 / 239 调用点）都在这个坐标系里。

本轮在核实子代理产出时撞上一个坐标系外的形状，它让**读端五档全绿也不代表停写
安全**。

### §9.9.1 触发：子代理的一条判定，我不接受也不否定，先去读代码

batch4 报 `domains/hooks/observability/telemetry/client.go` 为
`silently_empty`，理由是 `FindRecentGatewaySession` 读 `request_logs_hot`
无门控，`no rows` 被吞成 `("", nil)`，调用方拿到空串就 `createSession()`，
结论写作「每个请求静默新开 gw_session_id，会话连续性碎裂」。

这条**方向对、量级错**。核实后：

- `FindRecentGatewaySession`（`client.go:593-625`）确实读 `request_logs_hot`、
  确实无门控、确实把空结果吞成 `("", nil)`（:619-621）。
- 但它**不是主路径**。`session_assignment.go:160-173` 先查 Redis 的
  `LastSystemSessionIndex`（`domains/session/last_system_session.go`，TTL 固定
  5 分钟），命中即返回，DB finder 只是兜底——注释原文：「the DB finder ... remains
  the authority」。

准确的失效条件是两条，不是「每个请求」：

1. **无 Redis 部署**：`main.go:968` 的装配在 redis 分支内，无 Redis 时
   `lastSystemSession` 为 nil，`session_assignment.go:160` 的判空直接跳过 ⇒
   DB finder 变成**唯一**路径，完全切断。
2. **Redis 索引 miss**（TTL 到期 / Redis 重启 / device seed 不匹配）⇒ 原本由 DB
   兜底的续对话变成新建会话。

> 判据教训：「grep 零命中」这次是**反过来**用的——不是从零命中反推读点不存在，
> 而是从 `GetRecommendedProbeInterval` 零命中反推消费方不存在（见 §9.9.3）。
> 同一个动作，方向不同，结论强度也不同。

### §9.9.2 真正的洞：停写的门控只管写侧

S4 的门是 `storage.request_logs_write_enabled`，它 gate 的是**写入**。于是有一类
读点天然落在门控之外：**读 v1 去决定「要不要写、写什么、算作哪一轮」**。这类读点
的输出不是 200 也不是 500，而是去改数据库里的另一个状态。

五档里没有它的位置：把 `bg/credential_recovery.go` 填成 `silently_empty` 甚至
**不算错**——它的响应确实是 200、结果集确实是空、无错误信号。它真正丢掉的东西，
在那一列里没有格子可以写。

### §9.9.3 已核实的四条（登记于 `admin/request_logs_control_plane_dependency_test.go`）

**① `bg/credential_recovery.go` —— 本轮最重的一条。**

`lookbackCandidateSQL()`（:1707）选出「36h 内有成功流量」的降级/离线绑定，
结果集**直接驱动一次恢复写入**：`ursmRecoverSink(..., success=true, 0)` 写
URSM v2 的 Recover(30)，外加 `dispatchProbe` 与候选缓存失效。代码注释原文：

> Evidence-backed Recover(30) write into URSM v2. success=true is justified by
> the SQL predicate (a logged success inside the window)

停写后的两段后果，**第二段比第一段危险**：

- ① 停写 36h 后候选集恒空 → `if len(candidates) == 0 { return }`（:1871-1873）
  静默返回，无日志无告警（`recoveryLookbackScans` 计数器照常自增，掩盖了这一点）
  ⇒ 降级凭据只能等自身探针恢复。
- ② **停写后 36h 内仍在用停写前的陈旧成功记录授权恢复写入** ⇒ 失效方向是
  **继续放行**，不是停止。

这与本审计 §8.2 修掉的 `s4_ready` 真空为绿**同一族**：门控的前提消失后仍给出
许可。区别在于这次放行的对象不是切流，是**凭据可用性**——而凭据恢复写入的
`success=true` 是有下游后果的。

**② `domains/hooks/observability/telemetry/client.go`** —— 会话身份
（`FindRecentGatewaySession`，见 §9.9.1）与轮次序号（`lookupTurnNumber`
:3767-3790，按 `gw_session_id` 数 `request_logs` 行）。turn_no 一侧确认在门控外：
`client.go:2113` 注释原文「outbox request-completed 会话事件在门控外照常提交」。
缓解：同处注释写明「the session/v2 aggregator owns the authoritative turn_no
anyway」——属派生计数器，非权威，故不单列为高危。

**③ `domains/providerprofile/adapters.go`** —— 每 credential 的 1h 窗口指标
（TTFT / 错误类型分布 / 429 命中率 / 成功率）经 `bg/provider_profile_workers.go`
的三个 worker 装配进 `AlertEngine`，由 `PGCredentialActor` 驱动**凭据自动禁用与
恢复**。**未核实**：AlertEngine 在「无数据」时是「不告警」（降级）还是「评分掉到
阈值以下 → 禁用凭据」（事故）——这决定它属于哪一档，**必须在生产库复核一次**。

**④ `domains/credentialstate/popularity_tracker.go` —— 判 dormant，且是自我更正。**

结构上完全符合 live：停写后 `refresh` 查 0 行不报错，把 `popularModels` 换成
**空 map**（:114-116，不是保留上一份），于是 `GetProbeInterval` 对所有模型回落到
5 分钟默认（:126-128），相对热门模型的 10 秒是 **30 倍探测衰减**——一个更隐蔽的
形状：不是冻结，是**每个 tick 被抹一次**。

逐行核实后判定为 **dormant**，两条独立理由：

1. `main.go:1535` 由 `LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true` 把关，**默认 false**；
2. 它唯一的输出 `Manager.GetRecommendedProbeInterval` **全仓无生产调用方**（仅定义）。

> 若只做到第 1 层（看到「读 v1 → 决定探针节奏」就登记成 live 高危），这条会造出
> 一个**不存在的风险**——而假的风险会稀释真的风险（§9.9.1 的 credential_recovery
> 就是真的）。**判 live 之前必须先查消费方是否真的被调用。**
> 反过来也成立：`consumer` 存在不等于它在门内——`ursmRecoverSink` 存在且活着，
> 也正因为如此它才危险。

### §9.9.4 落地：两条正交的轴，各配一道自己的门

不把控制面塞进五档当第六档——那会把「响应退化」和「控制流退化」混进同一个枚举，
而它们的判据、危险方向、复核方法都不同。改为独立登记表 + 独立门：

| | 读端轴 | 控制面轴 |
|---|---|---|
| 表 | `requestLogsStopWriteClassification` | `requestLogsControlPlaneReaders` |
| 问的问题 | 这个读点的**输出**长什么样 | 这个读点的输出**决定**了哪个写入/身份 |
| 维度 | 5 档后果 + 4 族机械判据 | live / dormant × gated / ungated |
| 分母 | 全部 105 个读点文件 | **非 admin 子集 52 个**（控制面只可能在请求路径与 worker 侧） |
| 门 | `TestRequestLogsStopWriteNothingLeftUnclassified` | `TestRequestLogsControlPlaneNothingLeftUnreviewed` |
| 常跑守卫 | 证据逐字 + 族一致性 | 证据逐字 + 键必须在清单内 + **live∧ungated 必须写 BlastRadius** |

`BlastRadius` 是这张表的关键约束：它逼每一条「活的、门控外的」读点写清**它具体
授权或改变了哪个写入**。少了这一条，「活」就只是一个形容词——而形容词不可核。

两道门当前**都是红的**，这是真实状态：

- 读端：99/105 未评估。
- 控制面：**48/52 未判定**（已判定 4 条即 §9.9.3 的①②③④）。

### §9.9.5 本条方法论

我这一轮搭的框架（读点清单 + 后果五档 + 族门）有一个盲区，它不是「某几条判错了」，
而是**整张表的坐标系里少了一维**。表现是：所有门都绿，缺陷照样能在生产发生。

可复用的判据：**当一个门控只覆盖系统的一部分时，去找那些「跨越门控边界」的交互。**
S4 的门在写侧，所以要找「读旧 → 写新」的跨界读点；权限门在资源侧，所以要找
「校验资源 A → 访问资源 B」的越界访问。同一形状。门控本身不能证明边界上没有洞，
它只证明边界内侧是对的。

推论：**新增一道门时，先问它守的是哪个坐标系，以及门外还有什么坐标系。**
只补条目不补坐标，缺口会在下一轮换个名字重新出现。

## §9.10 控制面不是一条，是一簇；且最危险的那条是「否定式守卫」（2026-10-02）

§9.9 立了控制面这张表，第一版只登记了 4 条。本轮把 `bg/` 整个 worker 簇逐个打开后
发现：**这不是一条孤例，是一簇**，而且方向不止一种。

### §9.10.1 已判定的 36 条分布

| 判定 | 数量 | 典型 |
|---|---:|---|
| `control_plane_live` | 30 | 凭据可用性、路由亲和、探针节奏、会话摘要、故障事件 |
| `not_control_plane` / `dormant` | 6 | lite SQLite 保留期、统计物化、trace 读取、备选模型列表 |

### §9.10.2 最危险的一条：`discovery/discovery.go`

```sql
UPDATE model_offers
   SET available = FALSE, unavailable_reason = 'auto_discovery_expired'
 WHERE credential_id = $1 AND raw_model_name NOT IN (...)
   AND NOT EXISTS (
       SELECT 1 FROM request_logs rl
        WHERE rl.credential_id = $1
          AND lower(rl.outbound_model) = lower(model_offers.raw_model_name)
          AND rl.success = TRUE
          AND rl.ts > now() - interval '%d hours')
```

它与 §9.9.3 ① 的 `credential_recovery` **同形但方向相反，而且是否定式守卫**：
判据是「v1 里查不到近期成功」。

⇒ 停写后 `NOT EXISTS` **恒真**。无论模型是否真的在用，都会被判为
`auto_discovery_expired` 而**下架**。

这比「恢复变慢」重得多：它不是少做一件事，是**主动禁用仍在正常工作的凭据模型**，
且无异常、无告警（写成功就是成功）。失效方向与 `s4_ready` 真空为绿同族，
但写的是**可用性**，后果更重。

> 一般化：否定式守卫（`NOT EXISTS(证据)`）比肯定式（`EXISTS(证据)`）危险一个量级。
> 肯定式在证据消失时**不做动作**（安全降级）；否定式在证据消失时**做动作**。
> 审计门控依赖时，先问「前提消失后我是停止，还是执行」。

### §9.10.3 方向谱系：同一个类，失效方向至少四种

| 方向 | 代表 | 停写后 |
|---|---|---|
| **继续放行** | `credential_recovery` ① | 36h 内用陈旧证据授权恢复写入 |
| **主动禁用** | `discovery` §9.10.2 | `NOT EXISTS` 恒真 → 误下架可用模型 |
| **退回保守** | `model_probe` | 热度没了 → `next_retry_at` 不再推后 → 探针**变频繁** |
| **静默失效** | `today_success_probe` | 候选集空 → 不再提交探测 → 只能等自身恢复 |

第 3 条尤其反直觉：它不是「少探测」而是「**多探测**」——因为退避加成的输入
（v1 成功流量）消失，`usage` CTE 恒空，热门模型失去 `next_retry_at` 推后。
只看终点（`available=FALSE` 那条 UPDATE）会把它误判成「凭据被误禁用」；
实际上 `reconcileBrokenConfirmedBindings`（:1072）**不读 v1**，它读
`model_probe_state`，v1 的影响在**上游**两跳。

### §9.10.4 进度与不做的事

控制面门：**36/52 已判定，17 条待判**（`s4audit` tag 下持续报出真实清单）。
读端门：99/105 待评估。

**明确没做的事**：剩下那 17 条里有一批是离线工具（`cmd/tools/*`、
`cmd/traffic-replay`、`cmd/scenario_driver`）、测试（`tests/*`）、lite 存储
（`storage/sqlite`）、导出（`domains/sessionforensics`）与视图层
（`db/probe_views_unified.go`）。它们大概率是 `not_control_plane`，
**但我没有批量填**——那样只能拿到一个词宽的 `request_logs` 子串当证据，
而本表的价值恰恰在于「已评估」与「凭印象」可区分。门会继续盯着这 17 条。

## §9.11 控制面轴收口：52/52，31 条是活的（2026-10-02）

`TestRequestLogsControlPlaneNothingLeftUnreviewed` **转绿**。这是本审计第一道
从「全部未评估」走到「全部判定」的轴。

| 判定 | 数量 |
|---|---:|
| `control_plane_live`（输出决定写入或身份，消费方是活的） | **31** |
| `not_control_plane` / `dormant` | 21 |
| **未判定** | **0** |

### §9.11.1 31 条里，只有两条是「写授权」，但它们正好是两个方向

| 文件 | 守卫形态 | 方向 | 停写后果 |
|---|---|---|---|
| `discovery/discovery.go` | `NOT EXISTS(近期成功)` | **主动禁用** | `available=FALSE` 恒真 ⇒ 误下架可用模型 |
| `bg/credential_recovery.go` | `EXISTS(近期成功)` | **继续放行** | 36h 内用陈旧证据授权恢复 |

其余 29 条虽也是 live，但写入的是**派生状态**（探针节奏、亲和度、汇总、事件、
建议、指标），不是凭据可用性这个级别的开关。区分标准是「写的东西被谁消费」：
被路由/凭据选择直接消费的是授权，被报表消费的是派生。

### §9.11.2 收口过程中的两次量具自查

**① 怀疑扫描器把注释当读点 —— 不成立。** `db/db.go` 的 grep 前 4 处
`request_logs` 全在注释里（:77/:181/:224/:2002），一度以为清单分母虚高。
逐行核对后确认扫描器**确实**剔除了它们（`requestLogsReadPattern` 匹配后
再按 `//`/`*`/`/*` 前缀过滤，§8.5 的 40 条注释规则），真实读点在
:3169/:3187/:5871。**分母没有虚高，扫描器无缺陷。**
这条自查的价值在于：它差点变成一条写进报告的假缺陷。

**② 统计脚本给出 4/52 —— 不成立。** 用 Python 正则切分 Go 的 map 字面量
没匹配上（gofmt 后的格式与预期不符），输出 `合计=4`。
改用 Go 直接遍历后是 `TOTAL=52 LIVE=31 NOT_CP=21`。
**量具本身出错时，先怀疑量具，别把它的输出当结论**——这与 §9.10 里
「先量具后结论」是同一条纪律的两个方向。

### §9.11.3 剩余的轴

- **控制面轴：完成（0 待判）。**
- **读端轴：99/105 待评估**，`TestRequestLogsStopWriteNothingLeftUnclassified`
  仍红。4 批子代理产出已收割，但**每条都要过族门**才准写入——族门在本轮已
  抓到我三次判定错误（`integrity_fingerprint_drift`、`routeincident/store.go`、
  `data_lifecycle.go`），这个「先过机械判据再过人的判断」的顺序不能省。

**S4 灰度的前置条件现在是两条，不是零条**：控制面轴已清，读端轴未清，
且两条写授权缺陷（discovery / credential_recovery）尚未修复。

## §9.12 修掉第一条写授权缺陷：让否定式守卫在证据消失时**不动**（2026-10-02）

§9.10.2 记的 `discovery/discovery.go` 已修。修法不是「把证据换成 session 侧」，
而是**让失效方向安全**。

### §9.12.1 为什么不顺手做端口

端口的技术障碍已在 §8 决策 1 的可行性实测里量化：`session_turns.raw_model_name`
0/1,682,828 填充，而 `session_turns.model` 实测 **等于 client_model（1138/1138）**——
本守卫要比的却是**上游名**（`lower(rl.outbound_model) = lower(raw_model_name)`）。
拿 `model` 顶替会在发生模型映射的绑定上系统性误判（`glm-5.2 → glm-5-2-260617`）。

⇒ 补 `RawModelName` 源头字段是独立的一件事，不在这里糊一个近似实现。
**近似实现比不修更危险**：它会让下架判定「看起来在工作」。

### §9.12.2 修法

抽出纯函数 `staleExpiryMayRun(requestLogsWritable bool)`，`expireStaleModels`
在执行任何 `UPDATE model_offers` **之前**用它短路：

```go
if mayExpire, blockedReason := staleExpiryMayRun(settings.RequestLogsWriteEnabled()); !mayExpire {
    slog.Info("discovery: skip stale-model expiry", "reason", blockedReason, ...)
    return
}
```

复用 `settings.RequestLogsWriteEnabled()`（S4 门控的权威读法），不新增第二套判断。
`RequestLogsWriteEnabled()` 在 `settings.Global` 未初始化时回落 `true`，
所以未初始化的进程**保持原行为**，不会因为这次改动突然什么都不下架。

**同函数内第二处写入**（`credential_model_bindings`，`ENABLE_CMB_EXPIRE=1` opt-in）
**完全不读 v1**（守卫是「本轮未发现」），S4 不影响它，本次不动——但要记下来：
它的语义与前者不同，将来若改守卫别混为一谈。

### §9.12.3 护栏本身被变异验证了四次

| 变异 | 结果 |
|---|---|
| 删掉整个护栏块 | **编译器挡住**（`settings` 导入未使用） |
| 保留调用、只删 `return` | **门红**：「护栏分支里没有 return」 |
| 护栏挪到 `UPDATE` 之后 | **门红**：「护栏在第 9 条语句，UPDATE 在第 7 条」 |
| 纯函数恒返回 true | **门红**（表测试） |

**第二条是本轮最重要的一次变异**：只写「调用存在」这条断言时，
「保留调用与日志、只删 `return`」的变体让本文件全部测试**依然全绿**——
护栏形同虚设而门是绿的。所以断言补成三条：**调用存在 ∧ 分支含 return ∧ 位置在 UPDATE 前**。

推论（与 §5.5.5 同族，但更细）：**「调用了守卫」不等于「守卫生效了」。**
判据要钉住**效果**（分支会不会终止、执行顺序对不对），不是钉住**调用**。
调用点是代理指标，效果才是被测性质。

### §9.12.4 这次也踩了两次自己的坑，如实记

1. `hasReturn(ifStmt.Body)` 传 `*ast.BlockStmt` 给 `[]ast.Stmt` 参数 → 编译不过。
   第一轮三个变异全部 `build failed`，**等于一次都没测成**；是「基线必须先绿」
   这条纪律把它逼出来的（`go vet && ... && go test` 的短路让基线没跑到）。
2. 断言只扫 `ifStmt.Cond` → 报「没有调用」。实际写法是
   `if x := f(); cond {`，**调用在 Init 里**。改成 Init + Cond 都扫。
   两者都是「门红了但结论是错的」——门红不等于我的判据对，得看红的原因。

## §9.13 读端轴推进 35/105；分类表补第 6 档，门误伤改具名豁免（2026-10-02）

`TestRequestLogsStopWriteNothingLeftUnclassified` 从 **99/105 未评估** 降到 **70/105**
（本批 29 条）。门仍红，这是真实状态。

### §9.13.1 新增第 6 档：`silently_degraded_content`

batch1 与 batch4 **各自独立**撞上同一堵墙：带 bodies 腿的读点停写后是
「**行还在、某一列变空**」——主腿走 710 视图照常出行，
`LEFT JOIN request_logs_bodies_* … COALESCE(rb.request_body,'')` 的正文腿没有
session 兜底。

- 填 `silently_empty` 不准：结果集没空。
- 填 `silently_frozen` 不准：不是冻结在旧值，是这一列变成空串/NULL/0。
- 硬塞进任何一档，都会让「bodies 腿到底算不算硬失败」被分类表的**沉默**吞掉。

⇒ 单列一档，并给族门加**反向**约束：纯基表族停写后读点整体停止，不存在
「行还在但某列变空」的形状，判该档即错。

### §9.13.2 族门误伤了我自己两次，都不是「门太严」

| 我判的 | 门报的 | 真相 |
|---|---|---|
| `admin/telemetry.go` → `unaffected`（读点在写门内，停写后不执行） | bodies 族不得判 unaffected | **我对**：读点不发生，与「读到空正文」是两种形状 |
| `admin/data_lifecycle_blobs.go` → `unaffected`（读 `pg_column_size`，体量面） | 同上 | **我对**：bodies 族按正则识别，把 `pg_column_size` 也算成「读 bodies」 |

改法不是放宽门，而是**默认拒绝 + 具名豁免**：新增
`bodiesUnaffectedJustification`，缺项或空理由一律判红，放行条件从「门写宽了」
变成「有人写下了为什么，而这段话会被 diff 审到」。

> 第四次记同一件事：**守卫写宽会误伤正确代码。** 这次误伤的是我自己的判定，
> 若不是族门先红，我可能会去「改代码迁就门」——那会把一段正确的读端分析改成错的。

### §9.13.3 子代理说「6 个文件的机械族有误」——核完是它错了

batch1 自报 `admin/model_status.go` 等 5 个文件的族被误标为 `reads_base_tables_only`，
并建议复核。逐个查 `sourceFamilyOf` 的实际返回值：**全部是 `reads_710_view_only`**，
与真实读点一致。子代理把「机械统计里的某个数字」当成了族标签。

**没有照它去「修」分类器**——那会修坏一个没坏的东西，而且它的判据（族与真实
调用点不符）本身站不住。

顺带核实了一件事以免自己犯同样的错：`admin/usage_trend_series.go` 读
`..._without_customer_id`，真库 `pg_get_viewdef` 里 `session_turns` 出现 **0 次**
（纯 v1），而主 710 视图是 4 次；`sourceFamilyOf` 确实把它归入 base 族
（`v1OnlyView` 分支），**分类器是对的**。

### §9.13.4 三个变异，两个被门抓住、一个抓不住（后者是合理的）

| 变异 | 结果 |
|---|---|
| M4 清空 `bodiesUnaffectedJustification` | **门红**（两个文件都报「必须具名登记」） |
| M6 把纯 `familyBase` 文件判成 `silently_degraded_content` | **门红**（反向约束生效） |
| M5 把 `admin/usage_trend_series.go` 改成 `silently_degraded_content` | **未被抓住** |

M5 抓不住**不是门的缺陷**。该文件族是 `familyViewBase`（同时读会话臂视图与
v1-only 视图），混合族的后果取决于**哪条腿主导**——detail 档只读 v1-only 腿会
冻结，而 provider 档读派生表。任何机械规则都会在这里误伤正确判定。
如实记下门的能力边界，不假装它抓到了。

## §9.14 族分类器少了一维：视图的「行级可用」不等于「谓词级可用」（2026-10-02）

batch3 报了一条影响全局的发现，核实后成立，且**推翻了我自己写下的一条族约束**。

### §9.14.1 事实

`710_request_logs_view_session_family_v2.sql` 对 710 视图的 session 臂做了
**30 列 NULL 补位**（`NULL::type AS col`）：

```
affinity_hit api_key_owner_user api_key_prefix application_code attachments
auto_profile client_model client_profile compression_reason due_at gw_task_id id
key_alias model_chosen outbound_msg_count outbound_msg_hashes outbound_token_est
owner_user provider_id provider_model quality_fix_actions request_class
request_type strategy_used stream_chunk_errors stream_chunks_sent test_tab_indent
transform_rule_id virtual_ip virtual_mac
```

于是「710 视图含 session 臂 ⇒ 停写后不会查空」**只在行级成立**。只要读点在
`WHERE / GROUP BY / JOIN` 里用到这几列，就是**行级有、谓词级空**：视图照常返回行，
但按该列过滤的结果集恒为 0 行。

实证（`domains/attachments/handler.go`）：读 `attachments::text`，session 臂该列恒
NULL ⇒ `Scan` 报错 ⇒ 被当成「无附件」⇒ 200 + `attachments: []`。
**附件数据其实还在 `request_attachments` 表里**（`repository.go:183` 已有读法），
丢的只是这条 JSONB 读腿——属可修的读迁移，不需要数据抢救。

### §9.14.2 我原来的族约束在这里是错的

旧规则：`familyView` 不得判 `silently_empty`（理由：真库实测 24h 内 36.55% 的视图行
来自 `session_turns`）。这条规则**会拒绝正确的判定**——`admin/top_problems.go`
（`AND client_model IS NOT NULL`）、`bg/shared_pick.go`（Priority 1 带
`client_model IS NOT NULL`）、`admin/session_analytics_breakdown.go`
（`provider_id` 分组）等，判 `silently_empty` 都对，但会被门判红。

⇒ 门在**惩罚正确代码**。这已经是本审计第四次记「守卫写宽会误伤正确代码」。

### §9.14.3 修法：新增第 6 个族 + 具名论证

`familyViewNullPadded = reads_view_with_null_padded_predicate`（**33 个文件**）。
- 该族**允许** `silently_empty`（谓词级空，结果集真的为空）。
- 该族判 `unaffected` **不直接判红、而是要求具名论证**——因为这一维的自动判定是
  **保守近似**：只看列名是否出现，分不清「用在谓词里」（真触发）与
  「只出现在投影 / URL 路径 / Go 结构体字段 / 已被同表达式非补位列 COALESCE 兜住」
  （过度触发）。实测被标记的 5 个文件里 **3 真 2 假**。

补位列清单由 `TestSessionArmNullPaddedColumnsMatchMigration` **反向校验** migration 710
的声明——不靠人记得更新。清单过期 = 族分类器把「谓词级空」误判成「行级有」= 门开始
拒绝正确判定，所以它必须钉在权威来源上。

### §9.14.4 族门第四次抓到我自己的错，三处改判

| 文件 | 原判 | 改判 | 依据 |
|---|---|---|---|
| `admin/model_status.go` | unaffected | **silently_empty** | :268-269/:296-297 `AND client_model IS NOT NULL`，而 client_model 是补位列 ⇒ 新流量全被滤掉，模型健康度看板**永久空白** |
| `bg/candidate_failure_monitor.go` | unaffected | **silently_degraded_content** | :267 `GROUP BY … provider_id …` ⇒ 新流量全归到 NULL 组，按 provider 的失败率分母静默失真；行与时间窗都正常，坏的是分组键 |
| `bg/stats_minute_rollup_retire.go` | unaffected | **silently_degraded_content** | :27/:106-108 的 NOT EXISTS 保护用 `COALESCE(r.provider_id,0)=m.provider_id` 等补位列 ⇒ 保护失效，本该保留的分钟行被当陈旧退役。**失效方向是多删派生数据** |

第 3 条特别值得记：它的失效方向与本审计已知的四种都不同——不是少读、不是放行、
不是误禁用，而是**过度清理**。同一个「证据源消失」，在不同守卫位置会产出五种方向。

### §9.14.5 两个变异验证

| 变异 | 结果 |
|---|---|
| M7 清空 `nullPaddedUnaffectedJustification` | **红**（2 条具名论证缺失） |
| M8 从补位表删掉 `client_model` | **红**：`迁移里有、表里没有：[client_model]` |

M8 特别重要：它证明补位清单不是写死的常量，而是**真的在和迁移对账**。

## §9.15 读端轴推进 58/105（batch3 的 23 条）（2026-10-02）

未评估从 70 降到 **47**。门仍红。

### §9.15.1 本批暴露的新东西不是 23 条判定，是一条守卫的**方向性错误**

`bg/ledger_reconciliation.go`：`usageCreditSQL`(:261) 是 `request_logs_hot` 与
`credit_ledger_hot` 的 **FULL OUTER JOIN**，而 S4 开关**只门控 request_logs 族、
不门控 credit_ledger**（`settings/key_request_logs_write_enabled.go:3-4` 写明
范围是「request_logs wide family」）。

⇒ 停写后 usage 腿归零、ledger 腿继续增长 ⇒ **每笔新 consume 都变成
`charged=0 vs debited>0` 的假 mismatch**，每轮最多 200 条灌进
`maas_reconciliation_findings`，不报错。

这条的意义超出它本身：**门控的「范围声明」和它实际覆盖的表不一致**时，
停写不是让对账变静默，而是让对账变成**结构性误报机**。
读到 `KeyRequestLogsWriteEnabled` 注释里那句「the S4 stop-write gate for the
request_logs wide family」时，应该顺势问一句：还有哪些表**不在**这个范围里，
却被同一个对账/聚合逻辑引用。

### §9.15.2 归档为 silently_frozen，但方向写在 Note 里

`ledger_reconciliation` 归档 `silently_frozen`——按「无错误信号的持续判定」这个
判据它成立。但它的真实语义既不是冻结也不是空，而是**误报洪水**。
在 Note 里写明方向，是为了让后来者不会把这一档读成「停止更新、无害」。

同类还有 `bg/stats_minute_rollup_retire.go`（§9.14.4 的过度清理）。
**同一个「证据源消失」，在不同守卫位置已经产出五种方向**：
继续放行 / 主动禁用 / 退回保守 / 静默失效 / 过度清理/误报。

### §9.15.3 又一次「族门要求具名论证」

`db/db.go` 被判为 bodies 族（bodies 族按正则识别），但我核了它的两处
`request_logs_bodies` 命中：`:7197` 与 `:7231` 都是 **pg_class.relname 的字符串
名单**（ALTER TABLE SET storage / ANALYZE 分区巡检）——表名出现在正则/数组里，
不是 FROM/JOIN 任何一张表。

与 `attachments_routes.go`（URL 路径字面量）、`data_lifecycle_blobs.go`
（`pg_column_size`）同形。加进 `bodiesUnaffectedJustification`，
默认拒绝、具名放行。

> 这已经是**第三类**触发 bodies 假阳性的语境：SQL 内容读 / 写门内 / 结构面。
> 正则按列名识别表，识别不出「读的是内容还是名字」。**每次都要具名写清是哪一种。**

## §9.16 读端轴推进 80/105（batch4 的 22 条）（2026-10-02）

未评估从 47 降到 **25**（只剩 batch2 的 25 条）。门仍红。

### §9.16.1 族门第七次抓到我：四处改判，两处真触发两处需具名

| 文件 | 原判 | 结论 | 依据 |
|---|---|---|---|
| `bg/stats_minute_rollup.go` | unaffected | **真触发** → degraded_content | `:205/:281 ON CONFLICT (bucket, tenant_id, provider_id, canonical_id)`——补位列在**冲突键**里，取值 `COALESCE(r.provider_id,0)` ⇒ 新流量全落 `provider_id=0` 假桶，事实表与维度表双双归错桶 |
| `admin/model_routing_diagnostic.go` | unaffected | **真触发（部分）** → degraded_content | `:95-99` 的 WHERE 在 710 视图上，是三分支 OR；`client_model` 那臂因补位恒不命中，另两臂（outbound_model / canonical_model）仍有效 ⇒ 不是全空，是「按客户端名查模型」这条路失效 |
| `admin/session_analytics_timeseries.go` | unaffected | **真触发** → degraded_content | `:62 AND %s.provider_id::text = ANY($n)` 的 alias 就是视图别名 ⇒ `NULL::text = ANY(...)` 求值为 NULL（非 true）⇒ **同一面板里按 provider 过滤恒空、不过滤照常有数据**，两种过滤给出矛盾的空/非空 |
| `admin/usage.go` | unaffected | **真假混合** → degraded_content | `:806-810` 的 `provider_id IS NOT NULL` **确实**在 `FROM request_logs_with_current_month rl2` 子查询内 ⇒ 真触发；但 `:334-366 ak.owner_user`、`:456-583 providers.provider_id`、`:767 api_keys.id` 这些列**同名却属于别的表**，那些表没有补位 |

### §9.16.2 `admin/usage.go` 是「机械判定为何只能保守近似」的最好样本

同一个文件里同时存在：
- **真触发**：谓词落在 710 视图上、用的是补位列；
- **假触发**：同名列属于 `api_keys` / `applications` / `providers` / `usage_ledger`——
  **列名相同，表完全不同**，那些表根本没有补位。

文件级的正则匹配看不出「这个 `provider_id` 属于哪张表」。这就是为什么该族
判 `unaffected` 走**具名论证**而不是直接放行：理由必须由读过代码的人写下，
并接受 diff 审阅。自动判据在这里只能**标记嫌疑**，不能**下结论**。

### §9.16.3 两条具名论证（真·假触发各一）

- `internal/collector/gateway_adapters.go`：`:61/:64` 的 `client_model` 都排在
  `COALESCE(NULLIF(outbound_model,''), client_model, …)` 里，而 `outbound_model`
  是 session 臂真值且排**第一位** ⇒ 谓词对有真实 outbound_model 的行照样通过。
- `admin/session_timeline_query.go`：`:32` 只是投影，且同一投影里并列了
  `outbound_model`，消费方取模型名时有非补位列可选；不用任何补位列做谓词。

> 至此 `nullPaddedUnaffectedJustification` 已有 5 条论证，覆盖四种过度触发语境：
> URL/JSON 字面量、**以非补位列打头的 COALESCE**、仅投影、以及（§9.14）
> 同名不同表。**每次都要具名写清是哪一种**——这本身就是这条族维度的能力边界说明。

## §9.17 读端轴收口：105/105，两道覆盖率门全绿（2026-10-02）

batch2 的 25 条写入后，`TestRequestLogsStopWriteNothingLeftUnclassified` **转绿**。
至此本审计两道覆盖率门都是绿的：**读端 105/105、控制面 52/52，未评估 0、未判定 0**。

### §9.17.1 最终分布

| 档位 | 文件数 | 灰度时是否可见 |
|---|---:|---|
| `silently_empty` | **27** | ❌ 静默 |
| `silently_frozen` | **24** | ❌ 静默 |
| `silently_degraded_content` | **23** | ❌ 静默 |
| `unaffected_by_stop_write` | 19 | — 不受影响 |
| `errors_out` | 11 | ✅ 立刻暴露 |
| `validator_dual_read` | 1 | — 刻意对账 |

**105 个文件里 74 个（70.5%）会在停写后继续给出错误答案且不报错。**
只有 11 个会立刻失败——而那 11 个恰恰是**不构成风险**的那批（灰度时一看就知道）。

源族分布（机械判定）：

| 族 | 文件数 |
|---|---:|
| `reads_base_tables_only` | 47 |
| `reads_view_with_null_padded_predicate` | **33** |
| `reads_bodies_plus_other` | 17 |
| `reads_view_and_base` | 5 |
| `reads_710_view_only` | 2 |
| `reads_bodies_family` | 1 |

### §9.17.2 这个分布说明的事

**S4 灰度不可能「跑通了就说明没问题」。** 灰度能观测到的只有那 11 个 `errors_out`；
剩下 74 个的失败形态恰好是「接口 200、字段齐全、值是错的」。

⇒ 灰度方案必须**自带对账**而不是「看接口有没有报错」。可用的对照物有三个：
`cmd/gateway/dual_read_gate.go` 的 S4 门（已修真空为绿）、
`domains/sessionforensics` 的双源比对、
以及本表本身（每条都锚在逐字证据上，可复查）。

### §9.17.3 本轮批次的族门战绩：抓到我 9 次

| 批次 | 触发条数 | 性质 |
|---|---:|---|
| batch1 | 3（`data_lifecycle` / `routeincident` / `integrity_fingerprint_drift`） | 真错 |
| batch3→§9.14 | 3（`model_status` / `candidate_failure_monitor` / `stats_minute_rollup_retire`） | 真错 |
| batch4→§9.16 | 4（`stats_minute_rollup` / `model_routing_diagnostic` / `session_analytics_timeseries` / `usage`） | 真错 |
| batch2→§9.17 | 2（`daily_probe_audit` / `session_management_api`） | 真错 |
| 具名论证 | 9 条 | 假触发，逐条写清机制 |

**9 次真错、9 次假触发。** 两边都需要门：没有族门，那 9 条错判定会一路进到 S4 决策里；
没有具名论证通道，那 9 条假触发会逼着后来者「改代码迁就门」。

### §9.17.4 遗留（不阻塞本次收口，但会阻塞 S4）

1. **`admin/credential_monitor_heatmap.go:295` 引用 `rl.origin_stage`**，而 710/734 的
   canonical 列契约（`db/request_logs_view_schema.go:575-615`）里没有这一列。
   若真库视图确无此列，`exclude_self_test=1` 的查询会**直接 SQL 报错**——
   与停写无关的既存缺陷，待真库确认。
2. **`admin/session_tenant.go` 判 unaffected 依赖一条未实测的假设**：三条 session 腿
   对新 task 是否都及时落行。若某类 task 只在 v1 留痕，权限门仍可能翻转成 404。
3. **两条写授权缺陷**：`discovery` 已修（§9.12），`credential_recovery` 未修
   —— 它的正解要先补 `RawModelName`（§8 决策 1 的可行性实测）。
4. **S4 门控范围声明**与实际覆盖表不一致（§9.15 的 `ledger_reconciliation` 误报机），
   还有哪些表在范围外被同一套对账/聚合引用，未系统排查。

---

## §9.18 视图源越列：读端 105/105 之外的一类缺陷（2026-10-02）

§9.17.4 遗留第 1 条（`admin/credential_monitor_heatmap.go:295` 引用 `rl.origin_stage`）经真库
确认成立并已修。修的过程中暴露出一个**此前 105 条读端分类里没有的缺陷类**，并顺带抓出
第二处同类真缺陷。

### §9.18.1 确认：`origin_stage` 不在视图契约内

| 关系 | `origin_stage` 列数（真库 information_schema） |
|---|---:|
| `request_logs_with_current_month` | **0** |
| `request_logs` / `request_logs_hot` | 1 / 1 |
| `session_turns` / `session_turns_hot` | 1 / 1 |

实跑复现（2026-10-02，库 `llm_gateway`）：

```
SELECT ... COALESCE(rl.origin_stage,'business')='business'
  FROM request_logs_with_current_month rl;
ERROR:  column rl.origin_stage does not exist        -- SQLSTATE 42703
```

去掉该项后同查询正常返回 210,398 行。**不是偶发、不是慢，是这条查询永远执行不了。**
且 `exclude_self_test` 在 `credential_monitor_heatmap.go` 的参数解析里**缺省 true**，
前端 `web/src/api/credential-monitor.ts:548` 又显式下发该参数 ⇒ 裸调用与前端调用走同一条
必错路径。凭据质量热图在线上 500 至少两周（自 R50 于 2026-09-21 引入算起）。

### §9.18.2 根因不是「写错一个列名」，是三层叠加

1. **读面对象与谓词变体没有绑定。** R49（2026-09-20）已识别「物理表谓词对视图必 42703」
   并造出视图变体 `probeTrafficExclusionPredicateView`，还写进了注释；R50（2026-09-21）
   给 admin 热图补臂时把**物理表**那条内联了回去。
2. **调用面守卫的清单是包内清单。** `bg.TestProbeExclusionPredicateCallSitesR50` 的文件
   列表只有 bg 包 7 个文件，admin 不在扫描范围内 ⇒ admin 的回归对整套测试隐形。
   注释里写的「inlined to avoid an admin→bg import」也从来不是真的：admin 已在
   `probe_history.go` / `handler.go` / `candidate_failure_handlers.go` / `probe_stream_sse.go`
   四个文件里 import 了 bg，无循环。
3. **SQL 字面量从未真的发给过 PostgreSQL。** `TestBuildHeatmapSQL_GuardsAgainst42803Regression`
   对 SQL **字符串**做断言，`pgxmock` 匹配查询字符串、从不解析它 ⇒ 「这条 SQL 根本跑不起来」
   整类缺陷在单测里不可见。这与 `admin/sql_literal_validity_test.go` 记录的 `''::jsonb`
   是同一根因，**第三次复发**（2026-07-08 `rl.role` → 2026-08-28 `''::jsonb` → 本次
   `rl.origin_stage`）。

### §9.18.3 第二重缺陷：只补列名会造出「200 但全盲」的热图

原谓词第二臂是 `NOT ('probe' = ANY(rl.quality_flags))`，**没有 COALESCE**。
`'probe' = ANY(NULL)` 求值为 NULL，`NOT NULL` 不是 TRUE，整行被 `WHERE` 丢弃。
而 710 迁移对 session 臂的 30 列做了 NULL 补位，`quality_flags` 正在其中。

7 天窗口实测（`credential_id IS NOT NULL`）：

| 交叉项 | 行数 |
|---|---:|
| session 臂 ∧ `quality_flags IS NULL` | **40,225** |
| session 臂 ∧ `quality_flags IS NOT NULL` | 50 |
| v1 臂 ∧ `quality_flags IS NULL` | 671 |
| v1 臂 ∧ 非 NULL | 418,258 |

⇒ 原谓词会丢掉 **40,225 / 40,275（99.9%）** 的 session 分臂。**只把 `origin_stage`
换成 `origin_actor` 是不够的**：那样热图会返回 200、返回 v1 行，却对整个已迁移的
`session_*` 数据集完全隐形——比 500 更难发现。

### §9.18.4 全仓扫描：8 个命中，7 个证伪

扫「同时出现 `origin_stage` 与视图名」的文件，逐个定 FROM：

| 文件 | 实际数据源 | 判定 |
|---|---|---|
| `admin/credential_monitor_heatmap.go` | `request_logs_with_current_month` | **真缺陷** |
| `admin/routing.go:2696` | `request_logs_hot` | 证伪（有该列） |
| `admin/analytics.go`（7 处调用点） | `routing_analytics_source` / `routing_decision_log` | 证伪 |
| `admin/auto_route.go:493`（3 处落点） | `routing_analytics_source` | 证伪 |
| `bg/mv_consistency.go:230,285` | `routing_analytics_source` | 证伪 |
| `bg/shared_pick.go:88` | 视图，但已用视图变体谓词 | 证伪 |
| `internal/sessionv2mirror/synthetic_session.go` | 仅注释 | 证伪 |
| `db/db.go:3167,3185` | v1 包装链定义，显式暴露 `origin_stage` | 证伪 |

`analytics.go:1012` 的谓词带 `probe` 别名且限定在 `NOT EXISTS` 子查询内，绑定的是
`routing_analytics_source probe`——逐行看出来的，不是 grep 出来的。

### §9.18.5 新抓到的第二处真缺陷：`compression_stats` 的 `token_band`

新写的全仓守卫第一次运行就报出 `admin/compression_stats.go:212`：

```sql
SELECT COALESCE(token_band,'') AS band, COUNT(*) FROM request_logs_with_current_month
WHERE ... GROUP BY token_band
```

真库实测 `token_band` 在视图上 **0 列** ⇒ 必 42703。而错误被
`slog.Warn("compression_stats band query failed")` 吞掉、不中断返回 ⇒
**仪表盘 token 分带聚合长期静默返回空**（属 §9 读端第 1 档 `silently_empty`，
但不在 105 条清单里，因为此前没有任何机制去查）。

**未修，且不擅自修。** 同函数的兄弟查询都读视图，所以正解是把 `token_band` 随迁进
`session_turns` + 710 投影；改成读物理表会丢掉 session 分臂，与 S4 方向相反。
它属于**「物理表独有列未随迁」缺口类，与 §8 决策 1 的 `raw_model_name` 同族**，
修法需要产品语义裁决。已按本仓既有范式登记为具名豁免并写明跟踪位置。

### §9.18.6 改动

| 文件 | 行为 |
|---|---|
| `bg/probe_policy.go` | `probeTrafficExclusionPredicateView` → **导出**为 `ProbeTrafficExclusionPredicateView`（含 4 个 bg 调用点与守卫测试同步改拼写，全仓单一拼写）。修正失实注释：常量有**三个** format 动词，原注释写「pass it twice」，照做会渲染出 `%!s(MISSING)` 混进 SQL。 |
| `admin/credential_monitor_heatmap.go` | 删掉内联三臂，改用 `fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "rl","rl","rl")`；新增 `bg` 导入。 |
| `admin/view_source_columns_contract.go`（新） | 42 个「物理表独有列」清单（真库 information_schema 差集实测），无 build tag 供两道门共用。 |
| `admin/view_source_column_contract_test.go`（新） | 全仓逐字面量 AST 门 + 具名豁免 + 豁免失效自检。 |
| `admin/credential_monitor_heatmap_probe_predicate_test.go`（新） | 源码钉桩门 + 谓词 format 元数门。 |
| `admin/credential_monitor_heatmap_sql_integration_test.go`（新） | 真库执行门 ×3。 |

### §9.18.7 四道门与它们的边界

| 门 | tag | 抓什么 | 抓不到什么 |
|---|---|---|---|
| `TestNoPhysicalOnlyColumnsInViewSourcedSQL` | `!integration` | **同一条字面量**内视图源 + 绑定到视图的越列 | 跨字面量运行时拼接的形状（**正是热图这个 case**） |
| `TestHeatmapProbeExclusionUsesSharedViewPredicate` / `TestViewPredicateFormatArity` | `!integration` | 热图源码里的越列拼写、无 COALESCE 臂、format 元数 | 其它文件的同形缺陷 |
| `TestCredentialHeatmapSQL_ExecutesOnRealDatabase` | `integration` | 拼装后的真实 SQL 能否被 PG 解析执行 | 任何解析期之外的语义问题 |
| `TestHeatmapExcludeSelfTestKeepsSessionBranchRows` | `integration` | 排除谓词的判决**不依赖 `quality_flags` 是否为 NULL** | 探针分类本身准不准 |
| `TestPhysicalOnlyColumnsListMatchesRealDatabase` | `integration` | 硬编码的 42 列清单未过期 | — |

**没有任何一道门能单独替代另一道**：静态门快但看不见运行时拼接，真库门看得见一切但需要
数据库。这与 `sql_literal_validity_test.go` 的结论一致。

### §9.18.8 门自己失败的三次（记录在此，因为都是真错）

1. **per-decl 粒度过粗** → 13 处假阳性。同一个 handler 常带两条独立查询
   （`session_turns_unified.go` 的 `:68` 读 `session_bodies_unified`、`:159` 读视图），
   声明级并集把两者混为一谈。改为逐字面量后降到 4 处。
2. **只剥 Go 注释、没剥 SQL 注释** → `bg/shared_pick.go` 假阳性。`origin_stage` **只**出现
   在一条 SQL 字面量内部的 `--` 行注释里，而那句话正是在解释「origin_stage 不能用」。
3. **判不出列绑定到哪张表** → 剩余 4 处全是假阳性：`rb.outbound_body` 绑
   `LEFT JOIN request_logs_bodies_with_current_month`；`AS task_id` 是**输出别名**
   （源列是视图自己的 `gw_task_id`）；`b2.task_id` 的 `b2` 是 CTE `base` 的别名。
   补上「限定符必须绑定到视图自身别名；无限定列只在字面量只有一张表时才算」的判定后归零。
4. **门自己的注释把门弄红**：`TestHeatmapProbeExclusionUsesSharedViewPredicate` 首跑即红，
   因为修复注释里**引用了**那个坏字面量来解释它为什么错。已改用 `stripGoComments`。

### §9.18.9 变异验证（4 次，全部被门抓住）

| 变异 | 结果 |
|---|---|
| 把 `rl.origin_stage` 重新混入一条视图字面量 | AST 门红，定位 `credential_monitor_heatmap.go:337` |
| 清空 `viewSourcePhysicalOnlyColumnExemptions` | 门红，`compression_stats.go:212` 重新报出 |
| 还原 R50 原始拼写（代码里，非注释） | 源码钉桩门红，两条断言同时命中 |
| 从共享常量抽掉 `COALESCE('probe'=ANY(...), FALSE)` | 真库门红：**9,154** 行（真实 flags）vs **41,730** 行（NULL 补空数组） |

最后一条的数字与本次独立量测互相印证：单独量旧谓词在 7 天窗口的存活行数得 **9,424**，
量级一致（差值来自两次采样时刻不同）。**两把量具不是同一把。**

### §9.18.10 遗留

1. **`compression_stats` 的 `token_band` 未修**，与 `raw_model_name` 同族，等产品语义裁决。
2. **跨字面量运行时拼接的越列仍无静态门**。已知形状：视图引用与谓词分属不同字面量、
   运行时由 `strings.Join`/`fmt.Sprintf` 拼成。静态判定的天花板就在这里；要么接受靠真库门
   覆盖，要么上真正的 SQL 解析器（成本另算）。
3. **`admin/session_tenant.go` 判 unaffected 的未实测假设**（§9.17.4 遗留 2）仍未测。
4. **`credential_recovery` 写授权缺陷未修**，仍阻塞 S4 灰度。

---

## §9.19 两条待办收口：S4 门控影响半径 + session_tenant 权限门假设（2026-10-02）

§9.18 之后剩的两条**不被产品决策阻塞**的待办，本轮做完。结论一条是「我原来的问法问错了」，
一条是「修了一个真的结构性误报机」。

### §9.19.1 S4 门控影响半径：范围声明与实际跨界的对账（已修）

`settings.KeyRequestLogsWriteEnabled` 声明的范围是 **request_logs 宽族**
（`request_logs_hot` 主行 + `request_logs_bodies_hot` 正文）。逐个调用点核实：

| 门调用点 | 同文件/同事务内触及的关系 | 是否越界 |
|---|---|---|
| `admin/telemetry.go:391,454` | `request_logs_hot` / `request_logs_bodies_hot` / 镜像 | 否，范围正确；注释明确「关停后只保留 usage_ledger 计费行」 |
| `domains/hooks/observability/telemetry/client.go:1140,1284,2014,2067` | 同上 + `usage_ledger_hot` | 否，`usage_ledger` 是**有意不归该门管**（计费不受停写影响） |
| `internal/trace/trace.go:473` | `request_logs` / `request_logs_hot` | 否 |
| `discovery/discovery.go:1110` | `request_logs` | 否 |
| **`bg/ledger_reconciliation.go`（不咨询该门）** | `request_logs_hot` × **`credit_ledger_hot`** | **越界** |

真缺陷形态：`usageCreditSQL()` 用 `FULL OUTER JOIN` 比对
**门内**的 `request_logs_hot.credits_charged` 与**族外、且永远不会停写**的
`credit_ledger_hot`（`entry_type='consume'`），而对账器**完全不咨询 S4 门**。

停写一旦生效：

- usage 臂 → 在切换点**冻结**（不再有新 `credits_charged` 行）
- credit 臂 → **继续增长**（计费不归该门管）

⇒ 切换点之后的每个请求都落进 `FULL OUTER JOIN` 的「只有 credit」分支
（`charged=0 / debited>0`），被当成差异写进 `maas_reconciliation_findings`。
**那些不是账务缺陷，就是停写本身**，且数量无上界、不是瞬态。

同一文件的 `balanceChainSQL()` 不受影响 —— 它整段只读 `credit_ledger_hot`
（按 `(created_at, id)` 回放 `balance_after` 链，自洽），不跨族。**所以只挡一项**。

**修法**（沿用本审计已建的 `staleExpiryMayRun` 先例）：

- 新增纯函数 `usageCreditComparability(logsWriteEnabled bool) (bool, string)` +
  稳定原因键 `usageCreditSkipS4StopWrite = "s4_stop_write"`；
- `checkUsageCredit` 在**发查询之前**短路，记录原因、返回 0；
- 新增 `SkippedChecks()`：跳过的检查返回的 0 与「扫了没发现」的 0 在计数上无法区分，
  必须有一条独立通道把它们分开 —— **「扫到 0 个问题」和「扫了 0 个」不是同一句话**。
  `resetSkipped()` 在每轮 `RunOnce` 开头清空，否则会为**真跑过且确实没发现**的一轮
  继续报「已跳过」，那比没有这个功能更坏。

### §9.19.2 我自己的守卫失败了三次（都记在这，因为都是真错）

1. **判据打在错误的 `return` 上。** 结构门最初用「函数体内第一个 `return 0`」判短路。
   变异（删掉 skip 分支的 `return`）后它匹配到了后面查询错误处理里的 `return 0` ⇒
   **门照样绿**。改为用 AST 锁定「调用该谓词的那个 `IfStmt` 的 Body 内是否有 `ReturnStmt`」。
2. **只看 `IfStmt.Cond` 漏掉了实际写法。** 门写成
   `if ok, reason := usageCreditComparability(...); !ok {`，调用在 **Init** 而非 Cond
   ⇒ 门在**正确代码上**报「门不存在」。已同时检查 Init。
3. **测试执行了它声称要验证的那一步。** 跳过列表的重置测试**手工**执行了
   `r.skipped = nil`，所以把 `RunOnce` 里的重置删掉仍然全绿。抽成具名 `resetSkipped()`
   并用 AST 钉住 `RunOnce` 在两个检查**之前**调用它。
4. （附带）`ast.Inspect(nil, …)` 会 panic（`IfStmt.Init` 可为 nil）。修之前，
   变异 A 的「红」其实是**崩溃**而不是断言命中 —— 差点把一次无效的变异验证当成有效证据。

**五道变异全部被正确抓住**：抽掉门 / skip 分支不 return / 门恒返回 true /
删掉 `RunOnce` 的重置 / 把重置挪到检查之后。

### §9.19.3 42 个「物理表独有列」按可修性二分（修正 §9.18 的一处判断）

§9.18 把 `compression_stats` 的 `token_band` 归为「需随迁进 `session_turns`」。**这个判断错了**，
真库差集显示 42 列分成两类：

| 类别 | 数量 | 列 | 修法 |
|---|---:|---|---|
| **A：`session_turns` 已有，只差 710 视图投影** | 5 | `origin_stage`、`token_band`、`client_forwarded_for`、`trace_events`、`upstream_protocol` | **给视图补投影即可，无需回填、无需动写路径** |
| **B：`session_turns` 也没有** | 37 | 见下 | 需迁移 + 历史回填 |

B 类里又有 **23 列已在 `session_turn_details`（733 特征层）**里 —— 该表 61 列、在写
（1,682,905 行，与 `session_turns` 的 1,682,911 同步；`_hot` 1,286 行、同为当前），
且 **`session_turns` 根本没有 `gw_task_id`，任务关联只存在于特征层**。
⇒ 真正缺列的只剩 **19 列**：
`billed_despite_cancellation`、`compression_end_index`、`compression_ratio`、
`compression_start_index`、`continuation_keywords`、`discard_events`、`ir_extensions`、
`is_terminal`、`outbound_body`、`request_depth`、`sanitizer_mutations`、
`session_summary`、`session_title`、`vendor_metadata`，以及 A 类那 5 列
（它们在特征层也没有，但**在 `session_turns` 里有**，仍属纯投影问题）。

**这条结论改变了修法的成本估算**：`token_band` 从「动写路径 + 回填」降级为
「一条 CREATE OR REPLACE VIEW 加 5 个投影」。但**本轮仍不擅自改**：
改 710 视图是共享契约变更（`TestRequestLogsViewSessionArmPinIsCurrent` 等
pin 需要同步），且 A 类里 `origin_stage` 正是本轮刚修掉的越列来源，语义要逐列确认。

### §9.19.4 `assertTaskInTenant`：我原来问错了问题

§9.17.4 遗留 3 问的是「三条 session 腿对新 task 是否都及时落行」。**这个问题本身没有
决策价值** —— 那是个 OR-of-EXISTS，任一腿命中即放行，「三条腿都落」从来不是不变量。
真正该问的是：**有没有 task 五条腿一条都没落**。

30 天窗口实测（`request_logs_hot` 侧，因为 session 侧的任务集就是 `session_turn_details`
本身、按构造自覆盖）：

| 量 | 值 |
|---|---:|
| `request_logs_hot` 的 distinct task | 7 |
| 其中 session 族（details / details_hot / summaries）全未覆盖 | 6 |
| 五条腿全未覆盖 ⇒ 会 404 | **0** |

那 6 个仍被 v1 腿命中，所以门当前不会误拒。**但面向终局有一个真约束**：

`assertTaskInTenant` 的五条腿里有两条是 `request_logs_hot` + `request_logs` ——
**而本项目的终局目标正是删掉这两张表**。v1 退役后，那些「只在 v1 留痕、未回填进
session 族」的历史任务会对**所有人** 404（权限门翻转成阻断所有人）。
⇒ **S4 退出判据必须包含「v1-only 历史已回填进 session 族」这一条**，
否则门会在 v1 真正消失的那一刻静默翻转。

### §9.19.5 遗留（本节新增）

1. **42 列的 A 类（5 列）值得单独做**：纯视图投影，无回填。但改 710 是共享契约变更，
   需同步 pin，且要逐列确认语义（`origin_stage` 刚被本轮修成越列来源，投影它等于
   把那条路重新打开 —— **必须同时把所有视图读方的谓词切到视图变体**）。
2. **19 列 session 族真的没有**，需要迁移 + 回填，或明确裁决「这些列随 v1 一起退役」。
3. `assertTaskInTenant` 依赖两张将被删除的表 ⇒ 写入 S4 退出判据。
4. `credential_recovery` 写授权缺陷未修（阻塞灰度）。

---

## §9.20 把 42 列的迁移成本压到 4 个投影（2026-10-02）

§9.19.3 只回答了「哪些列在 session 族里没有」，没回答「**哪些列真的有人在读**」。
这一轮补上后半问，结论把待决范围缩小了一个数量级。

方法：一次性 AST 扫描器（`/tmp/colscan`，不入仓库），复用 §9.18 那套已验证的
「列绑定到哪张表」判据，对每条 SQL 字面量解析其 `FROM/JOIN` 关系集合，再把每处列
引用归到它绑定的关系。**扫描器的输出只是嫌疑清单** —— 它对多关系字面量会过度归因
（本轮就把 `turn_writer.go` 的 INSERT 列清单误判成视图读取），每一条都要手验。

### §9.20.1 42 列的真实用途分布

| 类别 | 数量 | 结论 |
|---|---:|---|
| **被 SELECT 读、且 `session_turns` 已有该列** | **4** | `origin_stage`、`token_band`、`client_forwarded_for`、`trace_events` ⇒ **纯 710 投影** |
| 被 SELECT 读、但已有别的 session 族落点 | 1 | `outbound_body`：**从不从 `request_logs`/`request_logs_hot` 直读**；所有读取走 `request_logs_bodies*` 或 `session_bodies_unified`，bodies 族已随迁 |
| **只写不读**（INSERT/UPDATE 列清单，零 SELECT） | 5 | `audio_tokens`、`image_tokens`、`video_tokens`、`provider_tokens`、`reasoning_tokens` —— 只见于 `telemetry/client.go:1362`（INSERT）与 `:2150-2153`（UPDATE）及 Go 结构体字段 |
| **全仓无任何 SQL 引用** | 29 | 零迁移成本，可随 v1 退役 |
| **名字撞车，不是真的读 request_logs** | 3 | `cache_hit` → 实为 `dashboard_access_events`；`session_summary` → `approval_requests`；`task_id` → 分布在 `durable_llm_tasks` / `hosted_task_events` / `session_dim` 等十余张任务表，**没有一处从 request_logs 读** |

`api_key_fingerprint`、`upstream_protocol` 等落在「无任何 SQL 引用」一类 ——
它们**存在**（A 类里确实在 `session_turns` 有列），但今天没有任何查询读它们。

### §9.20.2 于是待决范围只剩一句话

> **给 710 视图补 4 个投影：`origin_stage`、`token_band`、`client_forwarded_for`、`trace_events`。**

这 4 列的**数据已经在 `session_turns` 里**（§9.19.3 的 A 类实测），所以：

- **不需要回填**（历史数据已在 session 族里）；
- **不需要动写路径**（`turn_writer.go` 的 INSERT 列清单已含 `token_band` 等）；
- 只需一条 `CREATE OR REPLACE VIEW` + 同步 `TestRequestLogsViewSessionArmPinIsCurrent` 等 pin。

今天唯一的真实消费方是 `admin/compression_stats.go:212` 的 token 分带聚合
（§9.18.5 抓到的那处静默空）。另外 3 列目前无人读，补上是为将来与语义完整性。

### §9.20.3 但补 `origin_stage` 有个硬前提

`origin_stage` 正是 §9.18 修掉的那处线上 500 的来源列。把它投影进视图，等于**把那条
越列路径重新打开**——所有以该视图为源的读方，只要用物理表版谓词，立刻又 42703。

所以补投影与「把所有视图读方切到 `bg.ProbeTrafficExclusionPredicateView`」**必须同批**，
不能拆成两个提交。§9.18 新建的 `TestNoPhysicalOnlyColumnsInViewSourcedSQL` 会在
任何一处遗漏时转红（它按「限定符绑定到视图自身别名」判定，见 §9.18.8 第 3 条）。

### §9.20.4 方法学留记

1. **扫描器输出是嫌疑清单，不是结论。** 本轮它把 `turn_writer.go:366` 的
   `token_band` INSERT 列清单归到了 `session_turns_with_current_month`，
   差点被读成「第五处 42703」。手验 5 处视图读点后确认：全部只用身份列
   （`session_id`/`turn_no`/`request_id`/`tenant_id`/`partition_date`），无越列。
2. **列名撞车是真实噪声源。** `cache_hit` / `session_summary` / `task_id` 三个名字在
   本仓的十几张无关表上都有。**只按列名统计引用量会高估迁移面**——必须先判绑定关系。
3. **「无 SQL 引用」是本轮最有价值的量。** 29/42 无人读，意味着 42 列里真正需要
   迁移的只有 4 个。之前把这 42 列整体当作「迁移面」是高估了。

---

## §9.21 v1 退役爆炸半径：66 个直读方里 39 个不能直接改指视图（2026-10-02）

§9.20 解决了「缺哪几列」。这一轮问的是退役的**另一半**：`request_logs` /
`request_logs_hot` 到底还有多少读方**绕过视图**直读，以及它们能不能改指视图。

### §9.21.1 扫描口径

一次性扫描器（`/tmp/v1scan`、`/tmp/padscan`，均不入仓库）：解析每条 SQL 字面量的
`FROM/JOIN` 关系集合，筛出**含 v1 宽族关系、且同一字面量里没有
`request_logs_with_current_month`** 的（即绕过视图直读 v1 的）。

**口径的精度声明**：这个判据分不出 `SELECT` 与 `UPDATE ... FROM` / `ON CONFLICT`，
所以下面的计数是**上界**——写路径（`admin/telemetry.go`、`db/db.go`、
`telemetry/client.go`）也被计入，因为它们带 `FROM`。请按「量级」而非「精确条数」使用。

### §9.21.2 结果

| 分类 | 文件数 |
|---|---:|
| 绕过视图直读 v1 宽族 | **66** |
| └ 其中**读了 session 臂恒 NULL 的补位列** ⇒ **不可直接改指视图** | **39** |
| └ 其中不读补位列 ⇒ 改指视图无列可用性障碍 | 27 |

读补位列的文件（节选，按主导列）：

| 主导补位列 | 涉及文件数（示例） |
|---|---|
| `client_model` | 25（`admin/logs.go`、`admin/analytics.go`、`bg/model_probe.go`、`bg/credential_recovery.go`…） |
| `id` | 12（`admin/providers.go`、`admin/routing.go`、`admin/swim_lane_init.go`…） |
| `provider_id` | 8（`admin/diagnostics_credential.go`、`admin/provider_diagnose.go`…） |
| `gw_task_id` | 2（`admin/session_tenant.go`、`admin/unified_detail.go`） |
| `outbound_token_est` / `outbound_msg_count` / `outbound_msg_hashes` | 各 1–2（`cmd/gateway/main_v3_wiring.go`、`cmd/compression-bench`） |

### §9.21.3 这不是「39 个缺陷」，是「39 个需要重写的读面」

它们今天**都能正常工作**（读物理 v1，列齐全）。危险在于**改指视图的那一天**：
session 分支的行会从这些列拿到 NULL，而接口照样返回 200 —— 就是 §9.18 那类
「修好了但变全盲」。所以这 39 个是 S4 灰度的工作项清单，判据是：

> 一个 v1 直读方可以安全改指视图，**当且仅当**它读的每一列要么不在 30 列补位清单里，
> 要么它对该列的读取本来就带着回落到 session 侧等价列的 COALESCE。

已确认带等价落地的（migration 710 文档明载的派生映射）：

| 补位列 | session 侧等价 | 710 映射 |
|---|---|---|
| `client_model` | `model` | `outbound_model ← model` |
| `attachments` | `attachment_count` | `has_attachments ← attachment_count` |

其余 28 列**没有已登记的等价映射**。用模糊匹配去找候选列会产出噪声
（`compression_reason` 匹配到 `completion_tokens` 之类），**不作数**——
需要逐列做语义裁决，属于产品决策，不在本轮擅自做。

### §9.21.4 一个反直觉的发现：`id` 明明在 `session_turns` 里，却**不能**进那 4 列投影

真库实测：`session_turns` **有** `id` 列（模糊匹配里是精确命中）。但 v1 的
`request_logs.id` 是**请求行 id**，session 侧的 `id` 是 **turn id** —— 两者不是同一个东西。
视图把它补位成 NULL 很可能是**刻意的**（避免给读方一个语义已变的同名列）。

⇒ **§9.20 的「补 4 个投影」清单不能顺手把 `id` 加进去。** 判据是
「session 侧的列与 v1 侧的是**同一个东西**」，不是「session 侧有这个列」。
`origin_stage` / `token_band` / `client_forwarded_for` / `trace_events` 满足；
`id` 不满足。这条判断建议由你确认。

### §9.21.5 由此得到的 S4 退出判据（三条，缺一不可）

1. **写入面**：v1 写路径全部并入门控（`settings.RequestLogsWriteEnabled`），
   停写稳定期 ≥ 一个 hot retention 窗口（当前 8h，`effectiveWindow` 依此夹逼）。
2. **读面**：本轮点名的 **39 个补位列读方**逐个改为视图读法，且每个都要么
   去掉对补位列的依赖，要么改成带等价落地的 COALESCE。
3. **历史面**：`assertTaskInTenant` 依赖 `request_logs_hot` + `request_logs` 两条 v1 腿
   （§9.19.4），v1 退役后「只在 v1 留痕」的历史任务会对所有人 404 ⇒
   **v1-only 历史必须先回填进 session 族**。

（另：`cmd/gateway/dual_read_validator.go` 是行级对账器，它读补位列
`request_type` ⇒ 停写后两侧不可比，同样需要纳入判据。）

---

## §9.22 把 §9.21 的 39 个读方变成有门的工作项（2026-10-02）

§9.21 的 66/39 来自一次性扫描器（`/tmp`，不入仓库、无回归保护）。数字驱动着
S4 退出判据，**没有门的数字会随代码演进而静默过期**——所以本轮把它落成
`admin/v1_direct_padded_column_reader_test.go`。

### §9.22.1 门的形状

判定复用 §9.18 那套已验证的机制（`goFilesUnder` / `stripSQLLineComments` /
`fromJoinRE` / `findColumnRefs` / `relToRepoRoot`），口径是：

> 一条 SQL 字面量，**FROM/JOIN 集合里含 v1 宽族、且同一字面量里没有 canonical 视图**
> ⇒ 它是「绕过视图直读 v1」的读方。若它还读了 30 列补位集里的任何一列，
> 就进登记表 `v1DirectPaddedColumnReaders`。

**不是禁止。** 这些读方在 v1 存活期间是**正确**的代码，全面禁止会把 S4 之前的
正常迭代也堵死。正确形状是「**默认未登记 = 需要有人拍板**」，与
`request_logs_stop_write_classification_test.go` 的具名论证同一范式。

登记表条目形如 `{cols []string; why string}`：`cols` 是该读方**今天读到的补位列集合**，
`why` 必须非空且会随失败输出打印。登记表由**门自己的输出生成**（不是手抄），
所以登记口径与执行口径不会漂移。

### §9.22.2 门自己漏过一次，被变异验证逼出来

第一版只判「文件在不在登记表里」。给 `admin/analytics.go` 追加一个
`client_model` 过滤条件 ⇒ 门不响。

原因不是实现 bug，是**文件级粒度看不见「已登记读方又多读了一列」**——文件早就在
表里，列集合变了但文件集合没变。⇒ 登记表升级为记录**列集合**，并加两条判定：

1. 已登记读方**新读了**登记集合外的补位列 ⇒ 红（附当前登记集合）。
2. 登记的列**不再被读到**（收缩）⇒ 判为登记项失效，红（要求同步更新）。

**教训与 §9.19.2 同源**：第一版「已覆盖 39 个文件」听起来完整，实际只覆盖了
「文件级」这一个维度。**覆盖率的单位要和风险的单位一致**——这里风险是
「哪些列需要重写」，所以粒度必须是列而不是文件。

（另记一次变异设计错误：最初给 `admin/analytics.go` 加的是又一个 `client_model`，
而它本来就读 `client_model`，列集合没变，门正确地不响——**是我把变异设计坏了，
不是门宽了**。换成它没读的 `gw_task_id` 后门立刻响。）

### §9.22.3 五道变异（全部被正确抓住）

| 变异 | 结果 |
|---|---|
| 已登记读方多读一列（`admin/analytics.go` 加 `gw_task_id`） | 红：`已登记读方新读了补位列 gw_task_id（登记集合为 auto_profile, client_model）` |
| 清空整张登记表 | 红：39 个未登记读方全部报出 |
| 登记项指向不存在的路径 | 红：`已失效：该位置不再触发本门` |
| 在**未**登记文件里新增读方（`admin/body_resolver.go`） | 红：`新增了「绕过…直读 v1 宽族、且读了补位列 client_model」的读方` |
| 登记列集合收缩（多登记一个 `gw_task_id`） | 红：`登记了补位列 gw_task_id，但该读方已不再读它们` |
| **同时**让两条登记失效（多条时必须各自报对文件） | 红，且两条各自报对路径 —— 见 §9.22.6 |

### §9.22.4 交叉印证

`/tmp` 扫描器与仓库内这道门是**两套独立代码路径**，都报出 **39**。
数字一致这一点本身是弱证据（思路同源），但足以支持「39 不是扫描器的假象」这一判断。

### §9.22.5 这道门管不到什么（边界写在门上）

1. **分不出 SELECT 与 `UPDATE...FROM` / `ON CONFLICT`** ⇒ 39 是**上界**，
   登记项里含写路径。表里多数条目是读方，但不要读成 39 条 SELECT。
2. **只覆盖「绕过视图直读 v1」的读方**。已经在读视图的 105 条读端分类由
   `TestRequestLogsStopWriteNothingLeftUnclassified` 覆盖，两道门互补不重叠。
3. **不判断语义等价性**。`client_model` 与 `outbound_model←model` 是否等价、
   `id` 与 session 侧 `id` 是否同一个东西（§9.21.4 已论证不是），
   这些都不是机械判定，仍需人工裁决。

### §9.22.6 自查时发现的第三个自身缺陷：失效报告会把原因报到别的文件头上

写完门后通读一遍，发现失效报告的实现有真 bug：

```go
var stale []string                       // 收的是「原因」字符串
sort.Strings(stale)                      // 排的是【原因】不是【路径】
t.Errorf("登记 %q 已失效：%s", registryOrder()[i], stale[i])   // 却拿路径去配
```

多条失效时 `registryOrder()[i]` 与 `stale[i]` **不相关**——会把 `A` 文件的原因报到
`B` 文件头上。变异 5 当时只有一条失效，所以**侥幸是对的**：我差点把它当成
「变异验证通过」的证据。已改为收集 `(路径, 原因)` 成对再按路径排序，并用变异 6
（**同时**制造两条失效）验证两条各自报对。

**与 §9.19.2 的教训同族**：判据必须钉在**该有的那个节点**上，不能靠「当前恰好对」。
单条样本通过不构成「多组也正确」的证据。

---

## §9.23 第三处跨门边界的检查：36h lookback 恢复扫描（2026-10-02，已修）

§9.19.1 我只查到对账器一处跳界就下了「其余四处都对」的结论。**这次把同族扫描重新
逐个核实，又查出一处**，而且它**反过来修正了我对 `credential_recovery` 的既有判定**。

### §9.23.1 先纠正我自己的旧结论

我此前记录的是「`bg/credential_recovery.go` 的 `EXISTS` 陈旧证据**持续放行**
URSM v2 恢复写入」。重读代码后发现**方向说反了**：

- `lookbackCandidateSQL` 是 **SELECT-only**（注释明写 `cmb.available` 不在此写）；
- 真正被门控的是**候选产生**：扫描产出候选 → `claimLookbackCandidate` 租约并提交探针。

所以停写后的失效方向不是「持续放行」，而是：

> 证据源冻结 → 窗口内不再有新行 → `EXISTS` 恒空 → **36h 之后没有任何绑定再进
> lookback 候选集** → 降级/离线绑定在这条路径上**永久失去恢复机会**，而扫描照常
> 返回空、无任何痕迹。

这是**静默洞**（漏恢复），与 §9.19 的假报机方向相反，但**结构完全同形**：
一个跨门边界的判定，在前提消失后仍然执行，并把「不可判定」读成「判定结果为空」。

### §9.23.2 逐项核实：同文件另外两条恢复路径**不该**被门控

| 路径 | 证据源 | 是否跨门 | 停写后 |
|---|---|---|---|
| `lookbackCandidateSQL`（36h 回看） | `request_logs_hot` ∪ `request_logs` | **是** | 36h 后恒空 ⇒ 静默洞 |
| `expiredCmbRecoverySQL`（:1132 的 `NOT EXISTS`） | `node_probe_state` | 否 | 仍完全可判定 |
| `recoverFreshDegradedSQL`（:1261 的 `NOT EXISTS`） | `node_probe_state` | 否 | 仍完全可判定 |

⇒ **只门控第一条。** 把另外两条一起挡掉就是拿静默洞换静默洞：它们读的是
`credential_model_bindings` / `node_probe_state`，都不在门内，停写后照常工作。
本门把「同文件」当作跳过逐项核实的理由，就会犯 §9.19.1 警告过的那个错。

被这条路径漏掉的、而另外两条又接不住的，正是 `unavailable_reason` **不**属于
`continuous_failure` / `probe!_%` / `auto!_%` 的那批绑定。

### §9.23.3 修法与验证

同 §9.19.1 的形状：纯函数 `lookbackComparability(logsWriteEnabled) (bool, string)`
+ 稳定原因键 `lookbackSkipS4StopWrite = "s4_stop_write"`，在**发查询之前**短路；
新增 `SkippedChecks()` 通道（跳过返回 0，与「扫了没候选」在计数上无法区分）；
新增 `recoveryLookbackTriggers.WithLabelValues("skipped_s4_stop_write")` 指标，
让「不再具备判定能力」在监控上可见而不是无声。

**六道变异全部被正确抓住**：抽掉门 / 门内不 return / 门内不 markSkipped /
resetSkipped 挪到门之后 / resetSkipped 挪进门内 / 误把兄弟恢复路径也门控。

### §9.23.4 门抓到了我自己实现里的一个问题——而根因在门

`resetSkipped()` 我放在 gate **之前**（作为兄弟语句），但断言写的是
「`gate.Body` 内必须调用 `resetSkipped`」⇒ 门红了。

**门是对的，实现也是对的，错的是断言**：重置若放在门**内**，只有「跳过」的轮次才会
清空清单 ⇒ 一个真正执行、真正「没找到候选」的轮次会继承上一轮的「已跳过」判决。
判据已改为「重置在门之前、且不在门内」。

> 这是 §9.19.2 的同族教训的第三种形态：**门红了不要先改实现，要先问「门在验证
> 我要的性质，还是在验证我写的那份实现的形状」**。

### §9.23.5 仍未解决的部分（不因这次门控而消失）

本轮修的只是**门控**：停写期间这条扫描会「停止并明说」，而不是静默返回空。
**证据源本身仍然只在 v1**——`session_turns.raw_model_name` 实测 0/1,682,828 填充，
所以把证据搬到 session 族仍然是 §8 决策 1 的范围问题，**本轮没有解决它**。
两件事的分工要说清楚：**门控防的是「不可判定被读成空」，迁移解决的是「证据从哪来」。**

---

## §9.24 跨门边界的**第三个失效方向**：过滤臂在门内、驱动表在门外（2026-10-02，已修）

§9.19 修了对账器，§9.23 修了 lookback 恢复扫描。两轮都提醒自己「别只按文件名分组」，
但**口径本身还是窄的**：我是沿着「谁咨询了 `RequestLogsWriteEnabled()`」去扫的。
真正中招的检查**不需要咨询门**——凡是把门内表当**判定证据**用的周期任务都算。
按这个更宽的口径重扫 66 个 v1 直读方，筛出带周期驱动的 13 个，得出下面这张表。

### §9.24.1 三个失效方向（同一个结构缺陷，第三种坏法）

| 方向 | 机制 | 实例 | 危险面 |
|---|---|---|---|
| ① **两侧同期比较** | 门内冻结 vs 门外继续增长 ⇒ 差额无上界 | `usageCreditSQL`（§9.19） | 假报机 |
| ② **证据缺失** | 证据源冻结 ⇒ 候选集恒空 ⇒ 什么都不做 | `lookbackCandidateSQL`（§9.23） | 静默洞（漏恢复） |
| ③ **过滤臂在门内、驱动表在门外** | 驱动表继续收新行，过滤谓词恒真 ⇒ **不再过滤** | `auto_route_affinity_worker`（本节） | **过度纳入 / 模型污染** |

方向 ③ 最隐蔽：它**不会让任何东西变空**，只会让本该被剔除的数据混进来，
所以既没有「0 findings」这种信号，也不会触发任何空结果告警。

### §9.24.2 实例：亲和度聚合的合成流量排除臂会静默失效

`bg/auto_route_affinity_worker.go` 的亲和度聚合用两条 `NOT EXISTS` 剔除合成流量
（`goal-*` / `auto-title-generator` / `auto-summary-generator` / `session-summary`）：

- 两条排除臂读 `request_logs_hot` / `request_logs` —— **整体在门内**；
- 驱动表 `auto_route_selections_all` 的写方是
  `domains/hooks/observability/telemetry/selection_writer.go` ——
  **实测 0 处 `RequestLogsWriteEnabled()` 调用，不受门管**。

停写一旦生效：驱动表继续收新行，证据表冻结 ⇒ 两条 `NOT EXISTS` **恒真**
⇒ 合成流量不再被排除，直接进入亲和度聚合 ⇒ 路由模型被污染，**无任何信号**。

### §9.24.3 真库实测把这条缺陷挖得更深

对 131 条合成请求（`session_turns.origin_actor` 命中上述集合）逐条查 v1 覆盖：

| 覆盖来源 | 覆盖数 |
|---|---:|
| `request_logs_hot` | **0** |
| `request_logs`（母表） | **131** |
| 两侧都没有 | 0 |

即：**今天唯一兜住过滤的是 `request_logs` 母表——而那正是本项目要删掉的那张表。**
（热表臂覆盖 0 条：这些合成流量早已被 promote 出 0–7 天的热窗口，所以热表臂
今天就是一条死臂。）

⇒ 停写后新合成流量不再有 v1 记录，母表臂对新数据失效；v1 退役后该臂彻底消失。
**所以这不是「防御性冗余」，它是终局下的承重臂。**

### §9.24.4 修法：纯增量，且必须钉住

保留原两条 v1 臂（双写期行为完全不变——同一 `request_id` 必然同时命中 v1 臂），
**新增第三条指向 `session_turns` 的臂**（session 族是 SSOT，且不受该门管）。
今天它是 no-op，v1 退役后它承重。

> **正因为它今天是 no-op，它最可能被下一个人当冗余删掉。** 本地 564 条 selections
> 上三条臂全是 564 通过——任何人看到「新加的臂什么都没改变」都会清理它。
> 所以配套的门是这次改动的一部分，不是附加品。

`bg/auto_route_affinity_s4_guard_test.go` 两道门 + **四道变异全部被抓住**：
删 session 族臂（最可能的误删）/ session 臂 actor 集合漂移 / 顺手删 v1 母表臂 /
驱动表改写别处（结构前提变了）。

### §9.24.5 顺带一条证伪：名字相同不等于同一个东西

扫描把 `bg/lite_retention_worker.go` 也列进「v1 直读方」，因为它有
`DELETE FROM request_logs`。但它的方言是 `rowid` + `?` 占位符 + 注释里的
「整条语句持 SQLite 写锁」——**那是 SQLite 本地库，不是 PG**，
与 S4 门（PG 侧）无关。**关系名相同不代表同一个存储面**，与 §9.20 的
「列名撞车」是同族噪声。已排除，未改动该文件。

---

## §9.25 剩余「周期 + v1 直读」检查的定性：多数**不该**门控（2026-10-02）

§9.24 的重扫留下 11 个「带周期驱动 + 绕过视图直读 v1」的检查，其中 2 个已修。
本节逐个定性。**结论先说：这 11 个里只有 1 个真该门控，其余 10 个门控会造出新的
静默洞**——所以本节**不动代码**，只交付分类与理由。

### §9.25.1 分诊表

| 检查 | v1 读喂给什么判定 | 停写后的失效 | 该不该门控 |
|---|---|---|---|
| `usageCreditSQL` | 两侧账目比较 | 假差异无上界 | **已修**（§9.19） |
| `lookbackCandidateSQL` | 候选资格（有无成功） | 候选集恒空 ⇒ 漏恢复 | **已修**（§9.23，可见化） |
| 亲和度排除臂 | 合成流量过滤 | 过滤恒真 ⇒ 模型污染 | **已修**（§9.24，补承重臂） |
| **`credential_selfcheck` 的 `last_error_at` 臂** | 「该凭据最近是否报错」 | 错误信号只来自 v1 ⇒ 冻结后**真实失败也检测不到** | **该门控**（方向 ②，尚未做） |
| `credential_selfcheck` 的 `recentUsageModels` | 自检模型范围（3 天成功流量） | 范围冻结为停写前快照 | 不该（陈旧但保守；门控=自检彻底停摆） |
| `today_success_probe` | 今日该探哪些模型（按最近使用排序） | 排序冻结 ⇒ 反复探同一批旧模型 | 不该（同上） |
| `model_tier` Top-N | 探针可打模型的资格集 | 资格集冻结 ⇒ 新模型永远不被探 | 不该（同上；且它正在履行 §9.18 记的「打破自激循环」职责） |
| `model_probe` watchdog usage scan | 同上（热表） | 同上 | 不该 |
| `auto_route_settle_worker` | 结算的 task_type 基线 | 基线陈旧但**结算仍在继续**（驱动表不受门管） | 不该门控；**该加陈旧标记** |
| `auto_index_refresher` | 索引用哪些行建 | 冻结 ⇒ 「索引是当前的」变成**真命题** | **无缺陷** |
| `anomaly_harvester` 的 `actual_tokens` 回填 | 异常行的**富化字段**，检测本身由流事件驱动 | 新异常行缺 `actual_tokens` | 不该（读端富化退化，非误判） |
| `popularity_tracker` | 探针间隔推荐 | — | **dormant**（默认关 + 全仓无生产调用方，§9.17 已判） |
| `lite_retention_worker` | 清理 `request_logs` | — | **不在范围**（SQLite 本地库，§9.24.5） |

### §9.25.2 为什么「不该门控」是这里的多数答案

把前三个已修的门控当模板套到其余 10 个上，会制造 **5 个新的静默洞**：
自检、today_success_probe、model_tier、model_probe 这四条一旦被门控，
停写后**探针与自检这两条「主动发现故障」的能力会整体消失**，
而它们本来不依赖被冻结的证据（真正的探测结果写在 `node_probe_state`，不受门管）。
停写是为了停**日志写入**，不是为了停**故障发现**。

分诊的判据（可复用）：

> 该门控 ⟺ 停写后这条检查会**做出错误结论**（假报机 / 漏判 / 过度纳入）。
> **不该**门控 ⟺ 停写后它只是**基于陈旧输入继续给出保守结论**，
> 或者它服务的**目的在停写后自动变得平凡为真**。
>
> 前者门控是**止错**，后者门控是**止对**。

### §9.25.3 「不该门控」不等于「不用管」

`auto_route_settle_worker` 是最值得记的一条：它的**驱动表 `auto_route_selections`
不受门管、继续收新行**，而它的**基线来自 v1、会冻结** ⇒ 结算继续发生、但用的是
陈旧基线。这比「整条停掉」更难察觉，**正确的处置是加陈旧标记**（让消费者知道
这个基线是停写前的），而不是门控。

这一条本轮**只定性、未实现**——加陈旧标记要改结算结果的消费方（HTTP 响应契约），
属于需要拍板的范围变更。

### §9.25.4 唯一该做而未做的：`credential_selfcheck` 的错误检测臂

它是 §9.25.1 里唯一「停写后会做出错误结论（漏判）」的：

- `recentUsageModels` 臂只是**范围**陈旧 ⇒ 不该门控；
- 但 `last_error_at` 臂是「该凭据最近是否报错」的**唯一信号源**，而它只来自 v1
  ⇒ 停写后**新发生的真实失败不会被检测到**（错误本身会进 `session_turns`，
  但这条查询不去那里读）。

这与 §9.23 的 lookback 同形（方向 ②），修法也同形。但**本轮未做**，因为：
它需要先决定「停写后错误检测的证据源是 `session_turns.success=FALSE` 还是别的」，
而这正是 §8 决策 1（`RawModelName` 同族：证据从哪来）的一部分。**门控只能让它
可见，迁移才能让它正确**——两件事别混。

---

## §9.26 `credential_selfcheck` 错误证据臂：补 `session_turns` 臂（不门控 worker）

§9.25.4 把它列为「唯一该做而未做」，并留了一个前置：先决定停写后错误检测的证据源。
本节给出那个决定并实施。**结论先说：不门控整个 worker，改为给错误证据臂补一条
`session_turns` 臂。**

### §9.26.1 分诊：为什么是「补臂」而不是「门控」

§9.25.2 的判据直接适用：停写后这条 worker 会做出**错误结论**（漏判），
所以是「止错」。但**止错不等于必须门控**——判据问的是「停写后它会不会做出错误
结论」，不是「它有没有跨门读 v1」。

`credential_selfcheck` 里两条 v1 读的性质**不同**：

| v1 读 | 冻结后的表现 | 性质 |
|---|---|---|
| `last_error_at` 错误臂 | 新发生的真实失败**检测不到** | **漏判 ⇒ 止错** |
| `recentUsageModels`（3 天成功流量） | 自检模型范围冻结为停写前快照 | 陈旧但保守 ⇒ **止对** |

若对 worker 整体门控，会把**后者也一起停掉**——而真正的故障发现能力
（`node_probe` / `node_probe_state`）**不依赖被冻结的证据**，结果写在 `node_probe_state`，
不受门管。**门控 worker 等于为了让一条臂止错，把另外两条有效的臂一起关掉。**
所以只动错误臂。

### §9.26.2 证据源的决定：业务失败 = `session_turns.success = FALSE`；探针失败**不可端口**

真库实测（`llm_gateway`，24h 窗口，按凭据去重）：

| 口径 | 凭据数 |
|---|---|
| v1 侧有报错 | 41 |
| session 侧有报错 | 15 |
| 两侧都有 | 15 |
| **只有 v1 有** | **26** |

再按 `origin_stage` 拆那 26 个：**全部是 `node_probe`**。

⇒ 15/15 完全重合：**业务失败在两族都有**，session 臂能 1:1 覆盖。
⇒ 26 个缺口全是探针流量。**探针流量按设计不走 session 写路径，所以它无法被端口。**

这个区分决定了放弃的是什么：放弃的是「探针最近失败 ⇒ 现在去复检」这条
**取证捷径**（self-check 顺路搭车读 v1 的探针失败行），**不是探针能力本身**——
探针系统独立运行，不受 S4 门管，仍会直接发现不健康凭据。
把这条捷径当成能力丢失去门控 worker，是把「降级的捷径」误当成「停摆的能力」。

### §9.26.3 改动：三处接线（`bg/credential_selfcheck.go`）

补一条臂要动**三处**，少任何一处都是**假修复**：

```diff
-		JOIN LATERAL (            -- ① v1 臂：INNER → LEFT
+		LEFT JOIN LATERAL (
+		LEFT JOIN LATERAL (        -- ② 新增 session 臂
+			SELECT MAX(st.ts) AS last_error_at
+			FROM session_turns st
+			WHERE st.credential_id = c.id::text     -- bigint → text 必须显式转换
+			  AND st.ts >= now() - interval '24 hours'
+			  AND (st.success = FALSE OR COALESCE(st.status_code, 0) >= 400)
+		) se ON COALESCE(e.last_error_at, se.last_error_at) IS NOT NULL
-		ORDER BY ... ASC, e.last_error_at DESC, c.id
+		ORDER BY ... ASC,
+		         COALESCE(e.last_error_at, se.last_error_at) DESC, c.id   -- ③ 排序接线
```

- **①** 不改，session 臂永远轮不上（v1 臂的 INNER JOIN 本身就是一个硬过滤）。
  v1 臂自身保留 `ON e.last_error_at IS NOT NULL`：配 `LEFT JOIN` 它的语义是
  「无 v1 失败行时该臂产 NULL」，与 INNER 等价但不改写它的原有行为。
- **③** 容易被漏：PostgreSQL 的 `DESC` 默认 **NULLS FIRST**，停写后唯一合格的
  群体 v1 值为 NULL，若不接 `COALESCE` 会被**全部挤到最前**，
  「最近报错优先」的排序语义反转成「最近没报错的优先」。
- `credential_id` 类型不同（v1 `bigint` / `session_turns` `text`）⇒ 显式 `c.id::text`。

**纯增量**：两条臂同时生效，命中集合是原集合的**超集**，不改变今天的挑选结果。

### §9.26.4 真库证据（不是推断）

1. **可执行性**：`PREPARE` 这条 SQL 通过（列名/类型/运算符全部解析），
   `EXECUTE` 返回 `id=12`。
2. **模拟停写**（把 v1 臂窗口设为 0，等价于 `request_logs_hot` 不再产生新失败行）：

   | 形状 | 停写后选中数 |
   |---|---|
   | 旧形状（v1-only 硬前置） | **0** ← 自检静默 |
   | 新形状（本次改动） | **1** |

   仅靠 session 臂可救的凭据数 = **15**。
   这是「补臂真的承重」的直接证据，不是「看起来做了修复」。
3. **今天是无影响改动**：实态 `session_only = 0` ⇒ 补臂今天不改变任何结果。

### §9.26.5 门：三次返工才写成（`bg/credential_selfcheck_s4_guard_test.go`）

这道门的设计过程本身是本节最有价值的产出：

| 版本 | 写法 | 漏抓的变异 |
|---|---|---|
| v1 | **全文件**子串 `Contains` | 被文件里另外两条 `LEFT JOIN LATERAL`、另外两处 `FROM request_logs_hot rl` 喂饱 |
| v2 | 限定在 `pickDueCredential` **函数体**内 | 仍被同函数内另外两条 LATERAL 喂饱 |
| v3 | **按臂定位**（别名 `e`/`se`/`l`）+ **剥 SQL `--` 注释** | 无 |

- v1/v2 的失败模式是**守卫被无关的同名字符串喂饱**——它看起来在验证，实际什么都没验证。
  这比门不存在更危险，因为它会让人以为这一处已经被守住。
- v3 加「剥注释」的直接原因：这段 SQL 里有一大段解释性中文注释，
  **注释正文本身就含 `COALESCE(e.last_error_at, se.last_error_at)`**。
  不剥注释，删掉真实接线也能被注释满足。
- 定位用**别名**（节点身份）而不是「第几个 LATERAL」（序号）——
  有人调整臂顺序时，序号定位会静默改判。

**门自己的边界也写进了注释**：它是**源码门**，只能证明这条 SQL 的形状没被改回退化
形状；它**不能**证明 SQL 在真库上可执行，也**不能**证明两族覆盖真的重叠。
真库那一半由本节的第 4 条证据承担。**两道门不可互相替代。**

### §9.26.5.1 连带修正：一个**钉字面量**的既有门被本改动打红

`TestPickDueCredentialRotatesLeastRecentlyChecked`（`credential_selfcheck_pick_test.go`）
断言的是 **ORDER BY 那一行的完整字面量**（单行）。本改动把 ORDER BY 拆成两行、
并把错误键换成 `COALESCE(e, se)`，它立刻红了。

**这是一个和 §9.26.5 同族、但方向相反的问题**：钉字面量的守卫会把**纯排版变化**
报成缺陷（假警报），同时对**语义退化不敏感**（真字面量还在，但它守的东西已经变了）。
所以不是「改回单行让它绿」了事，而是改写成按**排序键先后次序**判定：

- 主键必须是 `COALESCE(l.last_at, …) ASC`（最久未检优先）——这条测试真正要守的语义；
- 错误键必须在主键**之后**；
- `c.id` 收尾键必须保留（并列时结果不确定）。

顺带把 SQL 提取辅助（`selfcheckPickSQL` / `orderByClause` / `stripSQLLineComments`）
收敛到这一个文件里、S4 门复用，避免两处各写一份而漂移。
**重写门之后必须重新变异验证**，所以补了 R1–R3 三个变异。

### §9.26.5.2 变异验证 10/10

每次都确认是**断言命中**（输出里出现对应测试名 + 对应测试文件行号），
而不是编译失败或 panic —— **「红」不等于「门在工作」**。

| 变异 | 守的是哪道门 | 结果 |
|---|---|---|
| M1 v1 臂退回 `INNER JOIN` | S4 门 | 红 |
| M2 删掉整条 v1 臂 | S4 门 | 红 |
| M3 硬前置退回 `se.last_error_at IS NOT NULL` | S4 门 | 红 |
| M4 `ORDER BY` 退回 `e.last_error_at DESC` | S4 门 | 红 |
| M5 删掉 `c.id::text` 显式转换 | S4 门 | 红 |
| M6 删掉整条 session 臂 | S4 门 | 红 |
| **M7 注释诱饵**（M4 + 在注释里补同样字样） | S4 门 | 红 |
| R1 轮转主键反转（错误键排到最久未检之前） | 轮转门 | 红 |
| R2 删掉 `c.id` 收尾键 | 轮转门 | 红 |
| R3 最久未检 `ASC`→`DESC` | 轮转门 | 红 |

M7 是**对照实验**：如果它绿，就证明门仍在被注释喂饱。它红，说明 v3 的剥注释
真的生效。

*（本次变异脚本自身错了两版，都是**量具**问题，不是门的问题：
① 判定「是否断言命中」时拿文件**路径**去匹配，而 Go 输出的是**基名**，
7 个变异全部误报成「红但非本门断言」；
② 加上 R1–R3 之后忘记把轮转门所在的另一个测试文件计入白名单，又误报 3 个。
**量具错了，先修量具再下结论**——与「本轮真库数字需先确认写入方身份」是同一条纪律。）*

### §9.26.6 残余风险（本节**没有**解决的）

1. **探针失败信号在停写后不可端口**。26 个只有 v1 有的凭据全是 `node_probe`，
   补臂后这部分**只能靠探针系统自己发现**。若 §8 决策 1（`RawModelName` 补齐）
   后续把探针流量也纳入 session 写路径，这条残余可自然消解；**在此之前它存在**。
2. **今天是 no-op 的承重臂**。实态 `session_only = 0` ⇒ 任何人看到「新加的臂什么都没
   改变」都会清理它。门的错误信息里已写明它的终局作用，**不要因为「看起来没用」删掉**。
3. `recentUsageModels` 臂的范围陈旧问题**依旧存在**（本节明确判定为「不该门控」，
   处置是接受陈旧，不是修复）。

---

## §9.27 读端静默退化量化，并**推翻我此前给用户的两个口径**

本节起因是一个未被处置的缺口：读端存在大量「静默」退化。动手量化后，
**先推翻的是我自己在前几轮给出的两个数字和一条待拍板事项**。

### §9.27.1 effect × sourceFamily 交叉表（首次计算）

先前只报过总数，没做过交叉。登记 74 个读点（此前我口头说的「105」是另一口径，
本表的分母是 74）。列族取 `sourceFamilyOf`：

| effect | 总数 | base_tables_only | bodies_plus_other | 710_view_only | bodies_family | view_with_null_padded_predicate |
|---|---|---|---|---|---|---|
| `errors_out`（响） | 8 | 5 | 3 | 0 | 0 | 0 |
| `silently_empty`（静默） | **15** | **11** | 4 | **0** | 0 | 0 |
| `silently_frozen`（静默） | 17 | 13 | 1 | 1 | 0 | 2 |
| `silently_degraded_content`（静默） | 18 | 0 | 6 | 4 | 1 | 7 |
| `unaffected_by_stop_write` | 15 | 6 | 2 | 3 | 0 | 4 |
| `validator_dual_read` | 1 | 1 | 0 | 0 | 0 | 0 |

**静默合计 50/74；会响的只有 8 个。**

**最有决策价值的一行是 `silently_empty`**：15 个里 **0 个走 710 视图**，
11 个是纯基表直读、4 个混读 bodies。而 `silently_degraded_content` 那一行
**0 个纯基表**——两者的处置路径天然不同：

- `silently_empty`（11+4）：主因是**绕过视图直读 v1** ⇒ 解法是**改指视图**
  （真库实测该视图 24h 内 36.55% 的行来自 `session_turns`，行不会空）；
- `silently_degraded_content`（18）：行照常出，**某一列内容变空** ⇒ 改指视图
  **解决不了**，典型是 bodies 腿没有 session 兜底（§9.18/§9.26 已记）。
  这 18 个里 7 个正是 `reads_view_with_null_padded_predicate` 族——
  **视图读方也可能是静默退化源**，这一点此前被「改指视图」的口号盖住了。

### §9.27.2 推翻口径一：补位集不是 30 列，是 **6 列**

我此前一直说「710 对 session 臂补了 30 列 NULL，39 个读方读了这些列因而
不可直接改指视图」。**这个口径是错的**，错在**只数了 710 的占位，
没数 734 的 details 层已经顶掉了它们**。

生效投影 = `buildSessionProjectionExprs(order, withDetails=true)`，实测：

- **恒 NULL 的只有 6 列**：`id` / `test_col` / `test_tab_indent` /
  `provider_model` / `credits_rate_multiplier` / `client_ip`
- 另有 **30 列**由 migration 734 的 `session_turn_details` 特征层顶掉，
  在 session 分臂**行级有值**（`client_model` 视图内 2,166,306 非空、
  `provider_id` 1,576,453、`attachments` 956,160）

直接后果：待拍板清单里的「30 列补位集其余 28 列逐列裁决」**基本作废**。
真库逐列裁决这 6 列（`request_logs` 母表 2,163,062 行）：

| 列 | v1 非空 | 判读 |
|---|---|---|
| `id` | 2,163,062（100%） | **不可投影**：v1 是请求行 id、session 侧是 turn id（1,515,984 组配对命中 0），只能改读法 |
| `test_col` | 2,163,062（100%） | 视图 session 臂丢空。**但真库未填充的是 `test_tab_indent`**，`test_col` 本身是满的——这列需要单独拍板 |
| `test_tab_indent` | **0** | 死列：v1 也没人填 ⇒ 无可投影 |
| `provider_model` | **0** | 死列（同上）。注意 `domains/stats` 的 `dimension_type='provider_model'` 是**另一个东西**，别撞名 |
| `credits_rate_multiplier` | **0** | 死列（v1 也没人填）。但 `maas/credits_sql.go` 读它并 `COALESCE(...,1.0)` ⇒ 恒按 1x 计费口径，**计费侧影响真实存在** |
| `client_ip` | 403,601（18.7%） | **不是类型问题，是语义问题**（我第一版写的是「类型不一致下的刻意取舍」，**不完整**）。真库按 `request_id` 配对 202,014 行实测：session 侧 `client_ip` 与本表 `client_forwarded_for` 相同 **202,014/202,014**、不同 0；与 v1 `request_logs.client_ip` 相同 **0**；与 v1 `client_forwarded_for` 相同 **202,014/202,014** ⇒ 它是**写在 `client_ip` 名下的转发头副本**，不是对端 IP。直映它等于把 `X-Forwarded-For` 当客户端 IP。视图该列由 740 从 v1 侧 lateral 供真源 inet，**取舍正确** |

⇒ **待拍板从「28 列」收缩到「0 列」**。这 6 列在
`db/request_logs_view_padded_columns.go` 里**早已各有具名裁决**（且
`TestEveryPaddedSessionColumnHasAVerdict` 强制对齐），我上面逐列复核后**全部认同**：

| 列 | 已有裁决 | 我的复核结论 |
|---|---|---|
| `id` | `verdictDifferentThing` | 认同：请求行 id ≠ turn id，只能改读法 |
| `test_col` | `verdictRetireWithV1` | 认同：**全仓无任何 SQL 读它**（命中只在视图列清单与注释里） |
| `test_tab_indent` | `verdictRetireWithV1` | 认同：v1 非空 0，调试遗留列 |
| `provider_model` | `verdictRetireWithV1` | 认同：两侧都没有写方；注意与 `provider_models` 表**撞名** |
| `credits_rate_multiplier` | `verdictNoSessionSource` | 认同：**整个会话族没有「这一行按什么倍率计价」这个事实**，补不出有源投影 |
| `client_ip` | `verdictDifferentThing` | 认同，且我第一版把理由写窄了（见上表） |

⇒ **这一项不需要你拍板**：没有任何一列需要「补投影」的决策。
`credits_rate_multiplier` 剩下的不是投影问题，而是**计费口径问题**
（`maas/credits_sql.go` 恒按 `COALESCE(...,1.0)` 计 1x），
它属于「会话族要不要记录计价倍率」这个更大的问题，见 §8 决策 1。

### §9.27.3 推翻口径二：「710 补 4 个投影」**早已完成**

我上一轮给你的待拍板①（`origin_stage`/`token_band`/`client_forwarded_for`/`trace_events`）
**已经过时**。真库实测：

- 视图现为 **118 列** = 冻结 113 + 738 `credits_rate_multiplier` + 740 `client_ip`
  + **813 `origin_stage`/`token_band`/`client_forwarded_for`**；
- 三列均已投影且有值：`origin_stage` 1,591,290 / `token_band` 158,277 /
  `client_forwarded_for` 227,195（总行 2,333,495）；
- `trace_events` **不在视图里是刻意的**（镜像从不写、近窗非空率 0，
  投影即净数据损失），它仍留在 `admin/view_source_columns_contract.go` 的
  越列清单里，与实况一致。

⇒ 我此前基于 §9.18/§9.20 的「视图缺这 3 列」判断，是**读了当时的结论而没有回真库复核**。
**决策面因此从 3 件缩到 1 件**（见 handoff 末节）。

### §9.27.4 连带修正：一段已经变成假的注释

`admin/credential_monitor_heatmap.go` 里那段「origin_stage 不在视图 113 列契约内、
真库 0 列」的注释，在 813 落地后**两项断言都成了假**。代码本身一直是对的
（用的是共享谓词 `bg.ProbeTrafficExclusionPredicateView`），**错的只是这段描述**。

已改写：保留「谓词必须用视图词汇表」这条**仍然成立**的规则，把已失效的
具体断言标注为已推翻并附真库新数字（118 列 / 1,591,290 有值 /
`trace_events` 才是刻意未投影的那列）。

**这一类风险的通用形态**：注释里的「真库实测」是**带时间戳的断言**，
迁移一动就过期，而读注释的人不会去核它是否还成立。
**它不能靠门来防**——为「注释里的数字」写机械门必然带假阳性；
能做的是**把断言和它的失效条件写在一起**（本次即如此）。

### §9.27.5 本节没有做的

- **没有**给「静默退化」加可见性信号（如响应里带 `stop_write` 陈旧标记）。
  那要改 50 个读点的响应契约，属需拍板的范围变更。
- **没有**动那 15 个 `silently_empty` 读方。§9.21 的登记表门已经在守
  「直读 v1 且命中补位列」的读方集合，但那是**读法**门，不是**退化可见性**门。
- `silently_degraded_content` 的 18 个（尤其 7 个 `view_with_null_padded_predicate`）
  **一行代码没动**——它们是下一块最值得做的地。

---

## §9.28 bodies 腿的端口可行性 + **修掉我自己在 §9.24/§9.26 引入的缺陷**

§9.27 把 `silently_degraded_content` 列为「下一块最值得做的地」。本节先手验那 18 个，
结论分两半：**一半是「不是缺数据，是没去用」；一半查出了我自己的缺陷。**

### §9.27.5 里我说错的一半：bodies 不是「无 session 兜底」

登记表的备注写的是「bodies 腿无 session 兜底」。这句话对**当前 SQL** 成立
（它们只 `LEFT JOIN request_logs_bodies_*`），但作为**数据可得性**的判断是错的。
真库实测 `session_bodies`：

| kind | 行数 | request_delta | response_delta | outbound_body | request_attachments | 时间跨度 |
|---|---|---|---|---|---|---|
| `turn_delta` | 1,683,104 | 1,683,104 | 1,683,104 | 1,683,104 | 1,683,104 | 09-03 → 实时 |
| `final_full` | 85,900 | 0 | 0 | 85,900 | 85,900 | 09-14 → 实时 |

**会话族已经逐轮存下了 request / response / outbound 正文与附件，四个字段 100% 非空。**
`session_bodies_2026_09` 分区 8.6 GB。

可关联性：按 `request_id` 配对，v1 `request_logs_bodies_hot` 的 1,434 行非探针数据里
**1,392 行（97.1%）**能在 session 侧找到同 `request_id`。

**但内容不等价**，这才是端口的真门槛：

| | v1 `request_body` | session `request_delta` |
|---|---|---|
| 形状 | 完整载荷 `{"model":…,"messages":[…],"max_tokens":…}` | **只有 messages 数组** `[{"role":…,"content":…}]` |

配对的 1,392 行里，三列**无一相同**（连转 text 都不同）。
session `request_delta` 含 `model` 键的行数：**0 / 1,683,104**。

**顶层键缺口**（v1 侧 1,434 行）：

| 键 | 出现行数 | 占比 | session 侧 |
|---|---|---|---|
| `model` | 1,434 | 100% | **0** |
| `messages` | 1,414 | 98.7% | 有（但只是数组） |
| `max_tokens` | 1,389 | 96.9% | **无** |
| `stream` | 306 | 21.4% | **无** |
| `tools` / `temperature` / `reasoning` / `tool_choice` / `stream_options` / `reasoning_effort` 等 11 项 | 各 11~20 | ≤1.4% | **无** |

⇒ **10 个 bodies 腿读点不能无损改指**。缺口最大的 `model` 恰好另有来源：
`session_turns.model` **100% 非空**，且与 v1 payload 的 model 在配对行里
**1,344/1,392（96.6%）一致**。所以理论上可以**合成** `{model, messages}`，
但 `max_tokens`（96.9%）、`stream`（21.4%）**在会话族里根本不存在这个事实**。

**结论：这是「会话族的轮次写入器只持久化了 message delta，没持久化完整请求载荷」
这一个根因**，它同时解释了 §9.27 遗留的 `raw_model_name` 填充率 0/1,682,828。
修它要给 session writer 加一列完整载荷（**动热写入路径 + 需历史回填**），
属需拍板的范围变更。**本节不擅自做。**

### §9.28.2 查出的缺陷：**我自己 §9.24/§9.26 的两条 session 臂都只读了一个存储面**

追查上表时撞上一个结构性事实，本轮已在**两处**都踩了第三次：

- 会话族的写方**只写 `session_turns_hot`**（`turn_writer.go:347`）；
- 冷行由 `promote_session_turns_hot_to_partition` 搬到**分区父表**；
- ⇒ **`session_turns`（父表）与 `session_turns_hot` 是两个存储面，边界随 promote 节奏移动。**

本机实测的边界干净得刺眼：

| 面 | 最新 ts |
|---|---|
| `session_turns`（父表，relkind=p） | **2026-10-02 06:06:31** |
| `session_turns_hot`（relkind=r） | 06:07:14 → **14:50:12（实时）** |
| `session_bodies`（父表） | 06:06:31（与上同一时刻） |

**父表落后 hot 约 8.7 小时。** 710 视图用的是 `session_turns_hot UNION ALL
session_turns`（迁移 710 的标准写法），**直读方必须照做**——而我自己的两条臂没做：

| 位置 | 原本读法 | 缺陷 |
|---|---|---|
| `bg/credential_selfcheck.go`（§9.26） | `FROM session_turns` | **对最新轮次盲** |
| `bg/auto_route_affinity_worker.go`（§9.24） | `FROM session_turns` | **漏掉最新合成流量** |

**危害实测**（自检臂，24h 窗口内父表覆盖不到的那段）：

| 形状 | 失败轮次数 |
|---|---|
| 旧形状（只读父表） | **0** |
| 新形状（两面合并） | **761** |

**这条臂存在的意义正是抓最新失败，而单面读法让它对自己的目标完全失明。**
真库差值实验，不是推断。

**修正**（两处都改为两面 UNION，并写明为什么）：顺带把 §9.26.2 的数字改对——
只读父表时测得「两侧都有 15 / 仅 v1 有 26」，**两面合并后是 19 / 22**，
session 侧少计了 4 个。结论不变（业务失败 100% 重合、22 个 v1-only 仍全是 `node_probe`），
但数字当时是错的。

### §9.28.3 为什么前两轮的门没能抓住

我 §9.26 的门验的是「session 臂存在、三处接线正确、类型转换显式」——
**没有一条断言关心它读的是哪个面**。门的形状与缺陷的形状不匹配：
我按「补了一条臂」写门，而缺陷是「那条臂读漏了一半数据」。
**这类缺陷只能靠「按语义钉」发现**：门必须问「这个读法覆盖了全部数据面吗」，
而不是问「这段 SQL 看起来对吗」。

亲和度那条门原本钉 `FROM session_turns st` —— **它把缺陷形状本身钉成了标准**。
已改写为按语义钉两面（并加了一条**否定式**断言专门挡退回单面）。

### §9.28.4 变异 7/7 全部断言命中

| 变异 | 结果 |
|---|---|
| N1 自检臂退回单面读法 | 红 |
| N2 亲和度臂退回单面读法 | 红 |
| **N3 只删 hot 那一行**（语法仍合法、关键字仍在，专门验门不是子串喂饱） | 红 |
| M1 v1 臂退回 `INNER JOIN` | 红 |
| M3 硬前置退回 v1-only | 红 |
| M4 `ORDER BY` 退回 v1-only | 红 |
| M5 删掉 `c.id::text` 转换 | 红 |

### §9.28.5 本节的量具前提（必须一并读）

- 本地库的 promote 节奏由**外部实例**决定，本机无 `cron.job`，
  所以「父表落后 8.7 小时」是**本机环境值**，不是设计承诺。
  但**结构性结论与节奏无关**：写方只写 hot、边界会移动、
  **单面读法必然漏**——漏多少随节奏变，漏不漏不变。
- §9.28 的 97.1% / 96.6% / 0 都是**本机 `llm_gateway` 库**的实测值，
  写入方身份未确认（沿用既有前提）。

---

## §9.29 全仓普查「只读父表」，并修掉一处**线上 100% 失明**的读点

§9.28 修的是我自己引入的两处。本节把范围放到全仓：**还有谁在只读父表？**

### §9.29.1 普查方法：三次量具返工才得到可信清单

这一节的结论完全取决于扫描器，**而扫描器错了三次**——每次都是跑出来才发现：

| 版本 | 写法 | 错在哪 | 报出 |
|---|---|---|---|
| v1 | Python 正则 + 只剥 SQL `--` 注释 | **没剥 Go 的 `//` 注释** ⇒ 大量假阳性 | 60 |
| v2 | 同上，改剥 Go 注释 | 基表名后用 `\b` ⇒ **排除不了视图名**（`session_turns` 是 `session_turns_with_current_month` 的前缀，而后一个字符 `_` 属单词字符，`\b` 不成立） | 18 |
| v3 | `go/ast` 只取**字符串字面量**（注释结构性排除） | v2 的 `\b` 问题仍在 | 4 |
| **v4** | v3 + 显式后随字符检查 + **逐条 SQL 判**（同一条 SQL 读了 `_hot` 孪生则父表那一支合法） | — | **2** |

⇒ 清单从 60 收敛到 2。**每收窄一次都是「门变准了」，不是「问题变少了」。**

**另有两个必须先做的关系事实过滤**（否则会把 20+ 个文件冤枉成缺陷）：

- `session_summaries`：**无 `_hot`、0 个分区** ⇒ 读父表就是对的；
- `sessions`：有 6 个分区但**无 `_hot`** ⇒ 同上。

### §9.29.2 全仓结论：PG 侧只剩 **1 处**真缺陷

| 类别 | 数量 | 说明 |
|---|---|---|
| 走合并视图（`session_turns_with_current_month` / `session_bodies_unified`） | 绝大多数读点 | 真库已核实两个视图**都 UNION 两面**，1,684,512 行、最新到实时 ⇒ **正确** |
| 直读基表但 UNION 两面 | 12 个文件 | 正确（含本轮修的 2 处） |
| 只读 `_hot` | 3 个文件 | **正是写方**（`turn_writer` / `bodies_writer` / `details_writer`）⇒ 正确 |
| SQLite 同名表 | 4 个文件 | `storage/sqlite/*` + `bg/lite_retention_worker.go`，**不同存储面** ⇒ 不在范围 |
| **只读 PG 父表** | **1 处** | `admin/annotation_handler.go:673` ⇒ **真缺陷** |

两批并行手验（18 个文件逐个读 SQL 上下文、给行号与判定 A/B/C/D/E）
**独立得出与我一致的结论**：18 个里只有 `annotation_handler.go:673` 一处。

### §9.29.3 缺陷本体：标注工作台对**当天的新会话 100% 失明**

`admin/annotation_handler.go` 的 `firstTurnFromClause`（被 `countSQL` 与 `dataSQL` 复用）
直读基表 `FROM public.session_turns st WHERE st.turn_no = 1`——**找每个会话的首轮**。

真库实测（2026-10-02 当天窗口）：

| 形状 | 当天有首轮的会话数 |
|---|---|
| 旧形状（只读父表） | **671** |
| 新形状（两面合并） | **2,077** |
| **修法新捞回** | **1,406** |

⇒ **旧读法对当天会话的可见率只有 32.3%**，且分页 `COUNT(*)` 用的是同一条子查询，
所以列表与 total **同时偏小**。另一组口径：首轮只在 `_hot` 的会话 **1,397** 个、
父表 0 命中，**全部是 2026-10-02 当天**新建的（对照：首轮在父表的 832,627 个）。

**已修**：改为两面 UNION，并把 `turn_no` / `partition_date` 一并投影出来
（原先依赖外层的 `st.partition_date` 谓词，UNION 后必须显式带出）。
**刻意不用** `session_turns_with_current_month` 视图——它虽然已含两面，
但 ft 还要按 `partition_date` 二次下推，直读基表两面 UNION 才能保留该谓词下推。

### §9.29.4 门：默认拒绝 + 具名登记，变异 4/4

`admin/session_family_two_surface_test.go`。三种正确写法（视图 / UNION 两面 / 只读 hot）
都放行，**只有「只读父表」是缺陷**；`storage/sqlite` 等有意排除并写明理由。

**判据钉在「一条 SQL」而不是「一个文件」**：一个文件完全可能同时有一条正确的
union 查询和一条漏读的单面查询。反向自检：登记了却不再命中的要报红
（**过期的登记表比没有更坏**）。

**门的返工过程本身就是产出**——三次，每次都是跑出来才发现：

1. 第一版钉「文件里出现过裸父表读」⇒ **门宽了**：`UNION ALL` 的父表那一支
   本来就合法，却把 12 个文件全判红。*每次假阳性都是「门宽了」不是「代码错了」。*
2. 重写时把「前置必须是 FROM/JOIN/INTO/UPDATE」连同辅助函数一起删掉 ⇒ 又宽回去，
   `cmd/gateway/main.go:2659`（`slog.Info` 的**日志文案**里列了表名）与
   `domains/session/v2/test_helpers.go:48`（**表名清单**）变成假阳性。
3. 定稿：**逐条 SQL 判 + 必须作为语句来源出现**，两个条件缺一不可。

**剩余盲区（写在门上）**：SQL 由字符串拼接在运行时组装出来的形状。
本轮两条登记（`db/request_logs_view_schema.go` 的 710 视图体拼装器、
`domains/session/v2/session_aggregator.go` 的表名切片遍历）就是这类，
两面分处不同字面量，逐串判必然误报。**这类形状只能靠真库执行门兜底。**

**变异 4/4 全部断言命中**：

| 变异 | 结果 |
|---|---|
| M1 退回裸父表读法（**真实缺陷形状**） | 红 |
| M2 登记表理由清空 | 红 |
| M3 登记了却不再命中（过期登记） | 红 |
| M4 **另一文件**新增裸父表读（证明不是只盯那一个） | 红 |

### §9.29.5 顺带查出：**另一个关系**的缺陷（本轮未修，需拍板）

`cmd/tools/validate_sessions_v2/loader.go:328` 读
`FROM public.session_bodies_with_current_month b`。真库核实：

```
relkind: session_bodies_with_current_month = i（**索引/约束**，不是视图）
        session_bodies_unified              = v（真正的合并视图）
```

全仓搜索确认：该名字**只作为 UNIQUE 约束名**出现在迁移 614/645
（`ADD CONSTRAINT session_bodies_with_current_month UNIQUE (...)`），
**从没有任何 `CREATE VIEW`**。⇒ 该工具运行时会
`relation "session_bodies_with_current_month" does not exist`。

**未修的理由**：注释明确写着「有意与 legacy `session_bodies_unified` 分开，
后者的列与当月语义不足以作为发布证据」——**改成 `session_bodies_unified`
会改变这个 parity 门的判定口径**，属需要负责人拍板的语义决策，不是我的。

⇒ 这正是「**同名不代表同一个东西**」的又一例：一个**约束名**被当成了**视图名**。

---

## §9.30 「确认存储可用性」：容量体检（目标原文要求的一步，本节才第一次做）

前面十二节都在改读法，**没有一节回答过目标里那句「确认数据的存储可用性」**。
本节做真库体检。结论先行，而且它**改变退役的理由**：

> **纯从容量看，退役 `request_logs` 省下的量很小，会被 session 族的增长在几天到几周内吃掉。**
> ⇒ 退役的正当理由必须是**架构一致性 / 可维护性**，**不是容量**。

### §9.30.1 体积：session 族**已经比要退役的 v1 更大**

| 族 | 表数 | 体积 | 行数 | 时间跨度 |
|---|---|---|---|---|
| `session_*` 族 | 73 | **19,762 MB（19.3 GB）** | 1,683,184 轮 | 2026-09-03 → 10-02 |
| `request_logs*` 族 | 16 | **8,347 MB（8.2 GB）** | 2,163,262 行 | 2026-09-03 → 10-02 |
| 整库 | — | 56 GB | — | — |

⚠️ 第一版我把 v1 族写成 **36 表 / 14 GB**——**错的**：那一版用 `LIKE 'request\_%'`，
把 `request_*` 下的其它表也算进了 v1 族。精确口径是 `LIKE 'request\_logs%'`。
**这是本轮第三次量具返工**（前两次见 §9.29.1），错因都是**用宽 LIKE 代替精确定义**。

### §9.30.2 单位成本：session 是 v1 的 **2.36 倍**

| 口径 | 计算 | 结果 |
|---|---|---|
| v1 | 8,347 MB / 2,163,262 请求 | **3.95 KB/请求** |
| session | 15,343 MB / 1,683,184 轮 | **9.33 KB/轮** |
| 倍数 | | **2.36×** |

（session 侧含 `session_bodies` 的 `request_delta`/`response_delta`/`outbound_body`；
v1 侧的正文在 `request_logs_bodies`，**已含在 8,347 MB 内** ⇒ 同口径。）

**投影**（退役 v1 一次性省 8.2 GB）：

| 日轮次 | session 增长 | 30 天 | 8.2 GB 相当于 |
|---|---|---|---|
| 384,411 | 3.42 GB/天 | 102.7 GB | **2.4 天** |
| 100,000 | 0.89 GB/天 | 26.7 GB | **9.2 天** |
| 20,000 | 0.18 GB/天 | 5.3 GB | **45.8 天** |

⇒ 容量收益高度依赖真实流量量级，而**这个量级本机测不出来**（见 §9.30.4）。

### §9.30.3 覆盖差 29.9%，其中 28.1 个百分点是**探针流量**

按 `request_id` 做两面（父表 ∪ `_hot`）反连接，v1 有而 session 无的请求：

| 类别 | 请求数 | 占 v1 |
|---|---|---|
| **`probe-*` 命名**（探针/自检） | **608,890** | **28.1%** |
| 有会话头但无 `session_turn` | 38,231 | 1.8% |
| 无会话头 | 64 | 0.0% |
| **合计** | **647,185** | **29.9%** |

⇒ 探针流量按设计不进 session 写路径（与 §9.26/§9.28 的发现同源），
**这部分 v1 成本不可迁移**，退役能省的比 8.2 GB 更少。

按等覆盖率折算：session 族单位成本 ≈ 9.33 × 0.701 ≈ **6.5 KB/轮**，
仍是 v1 的 **1.66 倍**。

**一个被证伪的假设（记下来）**：我先查 `origin_stage='probe'` 想确认探针占比，
实测 **0 / 2,163,262** —— v1 侧该列没被这样填充，**这个判据不成立**。
真正的判据是 **`request_id` 的 `probe-*` 命名**。
*假设被数据推翻时要说出来，不要改口径让它看起来成立。*

### §9.30.4 容量规划**不能**用本机的日序列

| 日 | 轮次数 |
|---|---|
| 2026-09-24 | 384,411 |
| 2026-09-25 | 223,319 |
| 2026-09-26 | 138,958 |
| 2026-09-27 | 9,541 |
| 2026-09-28 | 8,484 |
| 2026-09-29 | 4,490 |
| 2026-09-30 | 3,524 |
| 2026-10-01 | 2,452 |
| 2026-10-02 | 2,111 |

**9 天内掉了 99.5%**。这**不是业务信号，是量具信号**：本地 `llm_gateway` 库由
**外部实例**写入（写入方身份未确认），这个断裂本身就说明该序列
**不能用于容量规划**。

⇒ §9.30.2 的三档投影因此只能当**敏感性区间**看，**不能当预测**。
要得到可用的容量结论，必须换一个**写入方已知**的环境（245 / 154 / 252）重测。

### §9.30.5 本节没有做的

- **没有**据此改任何保留期或分区策略：缺一个可信的日流量量级，改了就是拍脑袋。
- **没有**把探针流量迁进 session 写路径：那会改变 §9.26 确立的
  「探针流量按设计不走 session 写路径」这一前提，属需拍板的范围变更。
- **没有**动 `final_full` 开关（`settings/spec_storage.go`）——它上一轮刚被证伪并
  保持默认关，本节的数据不支持重开。

---

## §9.31 修掉 parity 门：它一直在**产出零证据**

§9.29.5 记下「`loader.go:328` 读的是不存在的视图」并说「未修，属语义拍板」。
本节把那句话的两个部分分开处理：先看**证据**，再决定要不要**改口径**。

### §9.31.1 那道门从来没有红过——因为它没有输出

`cmd/tools/validate_sessions_v2` 是「验证 session V2 与 v1 数据一致」的 **parity 门**，
也就是用户那句「**确保数据在更改前后一致**」的执行者。它的 `CanonicalV2BodiesView`
指向 `public.session_bodies_with_current_month`，而该关系在真库：

```
relkind: session_bodies_with_current_month = 'i'  ← 索引/约束，不是可查关系
        session_bodies_unified              = 'v'  ← 真正的合并视图
```

全仓确认它**只作为 UNIQUE 约束名**出现在迁移 614/645
（`ADD CONSTRAINT session_bodies_with_current_month UNIQUE (...)`），
**从无任何 `CREATE VIEW`**。

⇒ **每次运行都 `relation ... does not exist`。**
而同包的 `loader_test.go` 里那个源码契约串**照样绿**——它只证明
「源码里写着这个名字」，不证明「这个名字在库里是个能查的东西」。
**那是一道被自己喂饱的假保证。**

**这比「门红了」更坏**：门红会被人看见，跑不起来只会**安静地没有输出**。

### §9.31.2 改指的依据（真库逐条核实，不是推断）

原注释反对用 `session_bodies_unified`，理由是「column 与 current-month 语义
不足以作为发布证据」。逐条核：

| 反对理由 | 核实结果 |
|---|---|
| 「column 不足」 | **不成立**：装载查询要的十个列（`session_id`/`turn_no`/`tenant_id`/`request_id`/`ts`/`request_delta`/`response_delta`/`outbound_body`/`request_attachments`/`response_attachments`）**全部具备**（外加 `id`/`partition_date`/`kind`） |
| 「current-month 语义不足」 | 定义为 `session_bodies_hot UNION ALL session_bodies`（两个存储面），1,771,097 行、**2026-09-03 → 实时**。它覆盖的是**全保留期而非仅当月**——对一个**完整性/parity**门来说这是**优点** |

⇒ 反对理由中可核实的部分**已被真库推翻**；而原状态是「跑不起来」，
**任何能跑的口径都是改善**。

⚠️ **这确实改变了门的判定口径（当月 → 全保留期），属语义变更，请负责人复核。**
若确实需要「仅当月」，正确做法是**新建一个视图**，而不是继续引用一个不存在的名字。

### §9.31.3 新增真库执行门——因为**源码门看不见运行时形状**

`cmd/tools/validate_sessions_v2/parity_bodies_relation_integration_test.go`
（`-tags=integration` + `TEST_PG_URL`）做三件事：关系存在且 **relkind 是可查关系**、
**十个必需列**齐备、**真跑一次装载查询形状**（参数取自真库真实 tenant/session，不是我编的）。

**门自己返工了一次，是变异验证逼出来的**：第一版把 `"session_bodies_unified"`
**硬编码在门里**，于是——

| 变异 | 第一版 | 修好后 |
|---|---|---|
| 把 `loader.go` 的常量改回那个不存在的名字 | **绿**（漏抓） | **红** |
| 把常量指向真索引名（验 relkind 检查承重） | 红 | 红 |
| 常量指向不存在的第三个名字 / 常量清空 | 红 | 红 |

漏抓的原因和本轮之前每一次一样：**门证明的是「session_bodies_unified 存在」，
而它要回答的是「parity 门用的那个关系存不存在」——这两个不是同一个问题。**
同包引用 `CanonicalV2BodiesView` 的代价是零，收益是**不可能再漂移**。

另：本次变异脚本自身错了两次（M1 改的门里字符串只出现在守卫分支、
**对被测行为是空变异**；M3 一次改了两个变量）。
*门不响时先怀疑变异、再怀疑门——但这次两者都有问题，都得各自修。*

### §9.31.4 为什么这节优先级高于它看起来的样子

用户目标的原话是「**确保数据在更改前后一致**」。负责执行这句话的 parity 门
**一直在产出零证据** ⇒ 前面十三节所有「实测两族一致 / 覆盖 97.1%」的结论
都是**我用一次性 SQL 手查的**，不是**可持续的自动门**。

修好它之后，那类结论才第一次有了一个**会持续运行的守门人**。

---

## §9.32 目标原话「**确保数据在更改前后一致**」的正面回答

§9.31 修好了 parity 门「能不能跑」。本节回答它**没回答**的那个问题：
**跑出来的数据对不对**。并把结论固化成可重跑的门。

### §9.32.1 口径：先拆口径，再判不一致

样本：**762,652 行非探针配对**（按 `request_id` 关联 v1 `request_logs` 与
`session_turns` 父表∪`_hot` 两面）。

⚠️ **必须排除 `probe-%`**：§9.30 实测 v1 有 29.9% 的请求在 session 侧不存在，
其中 **28.1 个百分点是探针流量**，它按设计不走 session 写路径。
不排除就会把「按设计不镜像」判成「数据不一致」。

**第一版口径是错的**，而且是门自己抓出来的：注释写了「先拆口径」，
SQL 却还在用 `IS DISTINCT FROM`（把「一侧 NULL、另一侧有值」也算不一致），
于是测出 `completion_tokens` **79.8% 不一致**。拆开后：

| 字段 | 两侧都有值且**不同** | 某一侧单独记录（**不参与判定**） | 性质 |
|---|---|---|---|
| `success` | **0** | 0 | ✅ 判定级事实，逐行一致 |
| `prompt_tokens` | **0** | — | ✅ |
| `completion_tokens` | **0** | session 有 / v1 空 **608,706** | ✅ v1 侧本就不记录 |
| `latency_ms` | **0** | — | ✅ |
| `upstream_status_code` | **0** | v1 有 / session 空 **45,337** | ✅ session 侧不记录 |
| 模型（原始串） | 81,531 | session 有 / v1 空 **575,302** | 见 §9.32.2 |

⇒ **在两族共有的事实上，两族完全一致。**所有差异都是「某一侧不记录」，
不是「两族记成了不同的东西」。

### §9.32.2 模型：86% 的「不一致」是**命名口径**，不是数据分歧

| 归一化步骤 | 残余「不一致」 | 可解释比例 |
|---|---|---|
| 原始串 | 81,531 | — |
| + 大小写 / 分隔符 | 41,158 | 50.5% |
| + 版本/日期后缀 | 57,509 | 70.5% |
| + 厂商前缀 | 70,750 | 86.8% |
| + 变体后缀 | **2,913** | **96.4%**（占总量 0.38%） |

真实成对写法（全部取自真库）：`MiniMax-M3`↔`minimax-m3`（3.9 万）、
`glm-5-2-260617`↔`glm-5.2`、`nvidia/riva-translate-4b-instruct-v2`↔`riva-…`、
`moonshotai/kimi-k3`↔`kimi-k3`、`claude-opus-4-5`↔`claude-opus-4-5-20251101`。

**残余里仍有一类值得人看**：session 侧有时记的是**被截断的模型名**——
`claude-sonnet-5` vs `sonnet-5`、`grok-4.6` vs `4.6`、`z-ai/glm-5.3-flash` vs `5.3-flash`。
这不是命名风格，是**信息丢失**（`sonnet-5` 单独看是有歧义的）。
另有个别疑似真分歧（`glm-5-2-260617` → `glm-5.1`），量级很小，未定性。

⇒ **不归一化就比 ⇒ 门会被 8 万条命名噪声喂成永远红，而那不是缺陷。**
归一化函数已固化为**不需要数据库的常驻单测**（`model_name_normalize.go` + 其测试），
样本全部来自真库实测，不是编的。

### §9.32.3 归一化门立刻抓到了**我自己**的过度归一化

`TestNormalizeModelNameKeepsGenuineDivergence` 第一版就把
`gpt-4o` vs `gpt-4o-mini` 判成了「相同」——因为变体表里含 `mini`。
**过度归一化比不归一化更坏：它把真缺陷洗成一致。**
已把 `mini`/`pro`/`max` 从变体表移除（它们是**区分真实模型的能力档位**），
并把「真分歧必须仍然不等」单独固化成一条测试。

**归一化门变异 4/4 全部断言命中**：

| 变异 | 结果 |
|---|---|
| M1 归一化完全失效（返回原串） | 红 |
| M2 重新引入 `mini`（过度归一化） | 红 |
| M3 不剥厂商前缀（恒假条件但仍编译） | 红 |
| M4 不剥版本后缀 | 红 |

*M3 第一版写成了 `if false {`，导致 `i` 未使用而**编译失败** ——
崩溃不是证据，那一版变异是无效的，已改成仍能编译的恒假条件。*

### §9.32.4 新增两道门

| 门 | 类型 | 回答什么 |
|---|---|---|
| `dual_write_value_parity_integration_test.go` | 真库（`-tags=integration`） | 两族**值层**是否一致；阈值按**实测值×余量**定，不按「理论上应该 0」 |
| `model_name_normalize_test.go` | **纯单测**（无需 DB） | 归一化既不能漏（同一模型两种写法要判等）也不能过（真分歧要保持不等） |

阈值：`success` **零容忍**（它是判定级事实，所有「该不该复检/告警」都建立在它上面），
其余 0.05%，模型归一化后 2%。**超阈值时门要报的是「去查」，不是「已知问题」**——
所以诊断量（某一侧未记录）单独输出而不参与判定。

### §9.32.5 本节的边界

- 全部数字来自**本机 `llm_gateway` 库**，写入方身份未确认（§9.30.4）。
- 只比了**两侧都有值**的事实；「某一侧未记录」的 60.9 万 / 4.5 万 / 57.5 万
  是**记录口径差**，不是分歧，但它们决定了**退役 v1 后会丢什么**——
  这与 §9.28 的 bodies 腿、`raw_model_name` 0% 填充是同一张账。
- **没有**把这道门接进 CI：**它需要 `TEST_PG_URL`，默认跳过**，
  而跳过不构成证据（门的提示里写明了这一点）。

---

## §9.33 `upstream_status_code` 的 45,337 个 NULL：追到底，**证伪**

§9.32 记录了一条「仅 v1 记录 `upstream_status_code` 45,337 行」。本节把它追到底，
结论是**这不是缺陷，也不是退役 v1 的代价**。追查过程中上一轮口述的
「受影响读方清单」是**错的**，一并更正。

### §9.33.1 先确认 NULL 本身不是异常

| 切片 | 行数 | `upstream_status_code` NULL | 占比 |
|---|---|---|---|
| 视图全量 | 2,333,933 | 2,203,137 | 94.40% |
| 视图非探针 | 969,550 | 838,754 | 86.51% |
| `request_logs`（v1 基表） | 2,163,262 | 1,987,751 | 91.88% |
| `session_turns` | 1,683,184 | 1,573,848 | 93.50% |

**两族基线都是 ~9 成 NULL。** 该列只在需要时才记（基本是错误路径），
所以「NULL 多」不是信号。若据此立一道「非 NULL 率」的门，
它会以 94% 的基线报红——**那是一台假警报机器，不是门**。本节明确不立这道门。

### §9.33.2 真正的洞：v1 有值、视图给 NULL

按 `request_id` 配对（两族 UNION 两面）：

| 量 | 行数 |
|---|---|
| v1 有非 NULL `upstream_status_code` | 176,134 |
| 其中 `request_id` 也存在于 session 族 | 155,282 |
| ├─ session 侧**也有**值 | 109,945（**值与 v1 全等，109,952/109,952 零分歧**） |
| └─ session 侧为 NULL | **45,337** |

实跑抽样（10 个 `request_id`）确认这不是推断：

```
request_id                  | v1_value | view_value | view_success | v1_rowcount
0000c2c0c9783d02d208c53737d2153e |    200 |           <空> | t           | 1
0000cb1b7ce6aaaee61c0a7d39b63e86 |    200 |           <空> | t           | 1
…（余 8 行同形）
```

**机制**：710 视图的 v1 臂带 `NOT EXISTS session_turns/hot` 反连接去重，
`request_id` 已在 session 的行不再由 v1 臂产出，于是这些行只由 session 臂出场，
带的是 session 的 NULL。**视图三臂都投影了这一列**（`pg_get_viewdef` 行 103/248/373），
所以洞不在投影，在取值。

### §9.33.3 成因：是**写方上线时点**，不是活体缺陷

按天看 session 侧的填充率：

| 日期 | session 有值 | session 为 NULL | 填充率 |
|---|---|---|---|
| 2026-09-03 ~ 09-13 | 435 | 47,399 | ≤2.9% |
| 2026-09-14 | 2,516 | 5,540 | 31.2% |
| **2026-09-15 起至今** | 每日 >0 | **0** | **100%** |

干净的**写方部署签名**：session 写方在 2026-09-14/15 才开始记这一列。
45,337 全部落在部署前，**今天的新写入零缺失**。
这批行的构成也印证「无信息价值」：45,323 是 `success=true` + `200`，14 是 `success=false` + `200`。

### §9.33.4 更正：上一轮口述的「受影响读方清单」是错的

上一轮我在会话里说 `upstream_status_code` 有多个真实读方受影响。按
「**同时**引用 710 视图 **且** 使用该列」求交集（非测试 Go 文件），命中只有 2 个：

| 文件 | 判定 |
|---|---|
| `db/request_logs_view_schema.go` | **视图定义本身**，不是读方 |
| `bg/candidate_failure_monitor.go` | `:258` 的 `COUNT(DISTINCT upstream_status_code)` 在 `candidate_failure_logs_with_current_month` 上——**另一个表族**；它唯一读 710 视图的 `:333` 不取该列 |

被上一轮点名、但实查后**不经过 710 视图**的：

| 文件 | 实际读的面 |
|---|---|
| `bg/provider_error_aggregator.go:71,108,131` | `candidate_failure_logs_hot` / `_unified` |
| `admin/candidate_failure_handlers.go` | `candidate_failure_logs` |
| `internal/quality/minute_aggregator.go:36-41` | **`request_logs_hot` 基表**（非视图），且 `:36` 的判据 `upstream_status_code IS NULL AND success` **按设计就是 NULL 容错的**——`success` 存活即可判成功 |

**教训（与 §9.21 同族）**：grep 命中的是「文件里出现过这个列名」，
不是「这个读方从这层视图读这一列」。表名/列名跨族撞车（§9.24 的 `request_logs`
在 SQLite 与 PG 同名）会让扫描器产出高置信度的假受影响方。**必须求交集后再手验。**

### §9.33.5 定性与决策

1. **不修**。修复要么回填 45,337 个 `200`（无信息价值），要么把 v1 臂的反连接
   改成 LEFT JOIN 携带旧值（要重建 2.3M 行视图 + 动所有消费方）。
   代价与收益不成比例——**且一旦退役 v1，反连接本来就该消失，这个洞自动不存在。**
2. **不是退役 v1 的代价**。今天视图已经给了 NULL，退役不会让它更糟。
3. **不立门**。判据若写成「该列非 NULL」，基线 94% ⇒ 必然假阳性。
   真正该守的是「两族在都有值时不得分歧」，**§9.32 的值层门已经守了**，
   本节不重复造门。
4. **记进账本**：这是「某一侧未记录」那张账（§9.32.5 / §9.28 bodies 腿 /
   `raw_model_name` 0%）的一个**已定性条目**——已确认为历史缺口、写方已补、
   视图层不可观测、无消费方。不是待办。

### §9.33.6 本节边界

- 数字全部来自**本机 `llm_gateway`**，写入方身份未确认（§9.30.4）。
  「2026-09-15 起 100%」是**本机观察**，不是设计承诺。
- 只覆盖 `upstream_status_code` 一列。其余列在两族都有值时零分歧（§9.32），
  但**「某一侧未记录」的其他列未逐列追查成因**——其中 6 列已有具名裁决
  （`db/request_logs_view_padded_columns.go`），`raw_model_name` 0% 填充仍待拍板。

---

## §9.34 §9.31/§9.32 两道新门在 CI 里的真实状态：**两道机制同时让它们失效**

§9.32 结尾把「是否接进 CI」列为待拍板，并写明「需要 `TEST_PG_URL`，默认跳过」。
本节不去猜，**把 CI 的门禁 harness 跑一遍**，回答这个问题。
结论：它们在 CI 里**双重失效**，且**不应该**接进那个 harness。

### §9.34.1 发现一：这个包**已经**在 CI 门禁列表里，是 §9.31 自己加的

`scripts/audit/derive-gate-packages.sh` 从「有 integration-only 测试文件」派生包列表。
`parity_bodies_relation_integration_test.go` 是 §9.31（`a3cc27f0f`）加的，
所以 `./cmd/tools/validate_sessions_v2` **已经**在 29 个门禁包中。
⇒ 不是「要不要接进去」，而是「它已经在了，而且状态如何」。

### §9.34.2 发现二：harness **确实**注入 `TEST_PG_URL`，所以不会因缺 env 而跳过

`scripts/audit/run-integration-gate.sh:586` 注入全部 14 个 DSN 名字，
其中包含 `TEST_PG_URL`，且有 `sql/schema/integration_gate_test.go` 的守卫
（`TestGateInjectsEveryDBCredentialName`）保证名单不漂移。
⇒ 「默认跳过」这个说法**只在 `go test` 裸跑时成立**；进 harness 就一定会连库。

### §9.34.3 实测：对着门禁库，两道门**正确地 SKIP 并自陈不构成证据**

按 harness 的完整流程在一次性库里复现（prereqs → HEAD 版基线 → 200 条启动迁移，
442 relations），然后带 `TEST_PG_URL` 跑这两道门：

```
--- SKIP: TestDualWriteValueParity
    dual_write_value_parity_integration_test.go:115:
    库里没有可配对的非探针样本 —— 本门不构成证据
--- SKIP: TestParityGateBodiesRelationExists
    parity_bodies_relation_integration_test.go:108:
    库里没有可用的 session_turns 样本（no rows in result set）——本门不构成证据
```

**这是设计正确**：门在无样本时拒绝成为证据，而不是拿 0/0 真空通过
（对比本会话早前在 `cloneTablesFrozenDDL` 上踩的「夹具丢 DEFAULT ⇒ 断言恒真」）。
但它同时意味着：**门禁库是空的，这两道门在那里永远 SKIP。**

### §9.34.4 失效机制之二：workflow 的 `paths:` 不含 `cmd/**`

`.github/workflows/integration-testcontainers-ci.yml` 的 `paths:` 过滤是
`domains / tests/integration / internal / durable / db / bg / autoupdate / center /
fault / licensing / vibecoding / sql/migrations / sql/schema / installer / scripts/audit /
go.mod / go.sum / 本文件自身`。
**`cmd/**` 不在其中。** ⇒ 我加测试文件的提交**根本不会触发**这个 workflow。

即便触发：harness 的真空判据是 `NPASS==0 → exit 3`，而该包的普通单测会 PASS，
于是走 `NSKIP>0` 的**警告分支**（"green with skips"，非致命）⇒ **CI 绿，而两道门零证据**。

### §9.34.5 决策：**不接**，并且这是类别性错误而非取舍

一个跨 762,652 行配对样本的值层一致性门，**无法在一次性空库上成立**——
那里没有双写样本可比。要让它在 CI 里真跑，只有两条路：

1. 给门禁库**播种**双写样本 —— 那是制造数据来证明数据的门，且播种口径本身
   会成为新的、未经审计的事实来源。
2. 让它对**生产形态**的库跑 —— 那是本机 `llm_gateway` 的用法，不是 CI 的用法。

⇒ 正确结论：**这两道门是本机生产形态库上的核对工具，不是 CI 门禁。**
把它们接进 `integration-gate` 只会制造一个「绿着但什么都没证明」的条目——
正是本会话反复拆除的那类假保证。**保持现状（不接），并把这一事实写清楚。**

### §9.34.6 途中撞到并已定位的**他处**缺陷（不属于本轮范围，未动）

第一次跑 harness 时它在**跑任何测试之前**就死了：
`ERROR: relation "public.candidate_failure_logs_hot" does not exist`（01-schema.sql:18510）。

- 已确证**不是已提交代码的问题**：`d5932d26c`（并行会话，2026-10-02 13:50，已在 origin/main）
  已把那行改回读父表 `candidate_failure_logs`。用 `git show HEAD:sql/schema/01-schema.sql`
  重建库 → **基线干净通过**，200 条迁移 applied=200 failed=0。
- 真正原因是**工作区有一个未提交的并行会话改动**（`M sql/schema/01-schema.sql`）
  把该行改回了 `candidate_failure_logs_hot`。按纪律**未做任何还原**，
  只用 HEAD 版本绕过。
- 同时撞到 `embeddata/startup/` 有 4 个未提交改动（未逐一核实其影响）。

**教训复述**：共享工作区里「跑出来红了」与「代码是红的」是两件事。
判别三件套的第 ③ 项（该路径 `git status` 的时间关系）在这里直接改变了结论方向：
若据红改代码，会把一个**已被修好**的问题重新引入。

### §9.34.7 本节边界

- 门禁库是**本机**容器里的一次性库；CI 用 amd64 镜像，形态可能不同。
- 442 vs harness 记录的 443 relations 差 1，**未追查**（4 个未提交的 embeddata
  改动是候选嫌疑之一）。这不影响本节结论——门在有无样本时行为都已实测。

---

## §9.35 拍板落地：加陈旧基线标记（已上线）+ **撤回我自己在选项描述里的一个说法**

三项待拍板已回：① 不加 `request_payload` 列；② parity 门保持当月；
③ **`auto_route_settle_worker` 的陈旧基线标记要��，改响应契约**。
本节记 ③ 的落地，并更正我在问卷里对 ① 的一句**说错的话**。

### §9.35.1 失效形状：不是「静默用陈旧基线」，是**全量 abandon**

我此前把这个 worker 的失效方向记成「结算继续发生但用陈旧基线」（方向 ③）。
按代码重新推导，**那个描述不准确**：

| 组件 | 停写后 | 后果 |
|---|---|---|
| `loadTaskBaselines` | `percentile_cont` 查 `request_logs_hot` 的 24h 窗口，但热保留 8h ⇒ 8h 后**零行** | 分位数 NULL ⇒ `COALESCE(...,0)` ⇒ **基线塌成 0** |
| `settleBatch` | `LEFT JOIN request_logs_hot` ⇒ `rl.success` 恒 NULL | 走 `p.success == nil` 分支 ⇒ 超过 `settleAbandonAfter`(4h) 即 **abandon** |

⇒ 停写约 8h 后，**每一条新 selection 都会被盖上 `settled_at` 并 abandon**，
`reward` 留 NULL。API 继续返回 200、`settled_at` 有值、`reward: null`
——**与「正常放弃」逐字段同形**。这是方向 ②（静默洞），不是 ③。
affinity rollup 随之停止学习，同样没有任何信号。

### §9.35.2 已上线：`outcome_source` 响应契约块

`admin/auto_route_outcome_freshness.go`，挂在**三个**暴露 reward/success 的响应上：

| 端点 | 为什么必须有 |
|---|---|
| `GET /api/admin/auto-route/audit` | 成功率与路由 KPI 全部是 worker 事后结算的 |
| `GET /api/admin/auto-route/affinity/ranking` | `avg_reward` / `ema_reward` 直接来自 worker 的基线 |
| `GET /api/admin/auto-route/affinity/selections` | 逐条 `reward` / `reward_source` / `settled_at` |

块形态（**加字段，不改任何既有字段**，老消费方不受影响）：

```json
"outcome_source": {
  "available": true,
  "as_of": "2026-10-02T07:41:12Z",
  "age_seconds": 11312,
  "stale": false,
  "stale_after_seconds": 14400,
  "reason": "live"
}
```

`reason` 是闭集：`live` / `no_rows` / `stale` / `absent` / `query_failed`。

三个设计要点：

1. **门槛 = `settleAbandonAfter`(4h)，不是 `baselineWindow`(24h)。**
   结算只需要请求自己那一行（请求后数秒内落 hot），所以**绑定约束是 abandon 视界**，
   不是基线回看窗。超过 4h ⇒ 下一轮 sweep 不可能再从 v1 结算出任何东西。
2. **不门控 worker。** 门控只会让数字不再变化，而 worker 的正确修法是改读会话族，
   不是被消音。这是 §9.24「止错 ≠ 必须门控」的又一次应用。
3. **v1 被退役后端点必须还能答。** `MAX(ts)` 报 `42P01` 时映射成
   `reason:"absent"` 而非 5xx——退役是**预期终态**，不是故障。

**性能**：挂在 3s 超时的 handler 里，实测 `EXPLAIN ANALYZE` 是
`Index Only Scan using idx_request_logs_hot_ts`，执行 **0.275ms**、5 buffers。

### §9.35.3 门与变异（5/5，全部断言命中）

| 门 | 守住什么 |
|---|---|
| `TestQueryOutcomeFreshness` | 8 个分支：live / 边界不陈旧 / 边界+1s 陈旧 / 冻结 72h / 空表 / 表已删 / 其他错误 / 未来时间戳夹到 0 |
| `TestOutcomeSourceStaleAfterMirrorsSettleAbandonAfter` | 从 bg 源码**按值**解析 `settleAbandonAfter`，防止镜像常量漂移 |
| `TestAutoRouteFreshnessMountedOnEveryNamedHandler` | 默认拒绝：三个挂载点缺一即红 |
| `TestOutcomeSourceReasonsAreClosed` | reason 闭集 |
| `TestOutcomeSourceFreshnessKeysAreJSONTagged` | 线上字段名 |

| 变异 | 红在 |
|---|---|
| M1 摘掉 `HandleAffinityRanking` 的挂载 | `:347` |
| M2 镜像常量 4h→8h | `:256` |
| M3 **抽掉 `out.Stale = false`** | `:149`（3 条断言） |
| M4 破坏 42P01 识别 | `:146` |
| M5 边界 `>` 改 `>=` | `:149` |

**M3 是真 bug，被测试抓到的**：`Stale` 在初始化时 pessimistically 置 `true`，
live 分支只写了 `Reason` 忘了把 `Stale` 设回 `false` ⇒ 该字段**恒为 true**，
标记会退化成「永远陈旧」，等于没加。已修。

**写门时自己踩的两个坑**（都记在测试文件的注释里）：

- 接线门第一版只认 `*ast.SelectorExpr`，而 `writeJSONOk` 是**包级函数**
  （`call.Fun` 是 `*ast.Ident`）⇒ 在三个挂载齐全的文件里报告**零挂载**。
  **一个把「在」报成「不在」的门，比没有门更坏**——它训练读者忽略自己。
- 镜像门第一版按**首次出现**的标识符切行，而该名字先出现在 doc comment 里
  ⇒ 把一句散文当 Go 解析。已改成只认 `settleAbandonAfter = <duration>` 声明行。
  **锚点必须钉在声明上，不是钉在名字上。**

### §9.35.4 更正：我在拍板问卷里对「不加列」的说法**说错了一句**

我给该选项写的描述是「**把 10 个读点改成不再依赖该事实**」。
派子代理逐读点核实后，这句话**不成立**，按它做会引入静默失败：

1. **数量不对**：实际是 **22 个文件 / 约 30 个 SQL 读点**，不是 10 个。
2. **响应侧整体不可端口**——这是 §9.28 完全没记的一条。v1 `response_body` 是
   provider 信封 `{"choices":[{"message":{…}}]}`；`session_bodies.response_delta`
   是**裸消息数组**（`BodiesRecord.ResponseDelta []Message`，`bodies_writer.go:200`）。
   仓库自己的 V2 读法可证：`json.Unmarshal(request_delta, &msgs)`，`msgs` 是切片
   （`message_source_v2.go:141-144`）。⇒ **凡是按 `choices[].message.content`
   取值的读点，拿到 delta 会静默返回空串**：
   `no_topic_session.go:140`、`memora_handlers.go:784`、`body_resolver.go:211/230`、
   `history_store.go:82`、`session_compare.go:899`、`session_bodies_batch.go:66`、
   `passive_probe_listener.go:180`。
3. **另有 4 类结构性不可端口**（与键名无关）：
   - `tools` 键（`quality_correlations.go:172`）→ session 侧恒无 ⇒ 分桶塌成单一桶；
   - **整份 JSON 透传**（`session_export.go:224`、`sessionforensics/export.go:48/68`、
     `logs.go:1150/1179`、`unified_detail.go:93`）⇒ 无 Go 侧解析，字段级保真直接丢；
   - **逐行聚合语义**（`quality_correlations.go:183/194`、`compression_sessions.go:142`）
     ⇒ delta 只含**本轮新增**，多图 `images` 分桶、`code_block` 分桶、压缩前消息数
     全部系统性低估；
   - **`system`/`instructions` 顶层键**（`system_prompt_prefix.go:175`）⇒
     Anthropic / Responses 协议取不到系统提示前缀。
4. **可端口的那部分也不是「把 delta 丢进去就行」**：v1 读点是
   `json.Unmarshal(body, &struct{Messages []…})`（顶层对象），而 `request_delta` 是数组，
   **必须显式包一层 `{"messages": <delta>}`**，否则 `len(Messages)==0` 全部返空——
   **静默失败，不是报错**。

**可无损复现的只有 8 处**（且都是请求侧、只依赖 `messages`）：
`auto_title_generator.go:835`、`session_title.go:187`、`logs_summary.go:196`、
`logs_summary.go:258`、`no_topic_session.go:316`、`session_sanitize_matches.go:225`、
`summarizer.go:646/690`。

**另记一处非阻塞语义差**：`summarizer.go:646` 的
`COALESCE(rb.request_body->>'role','user')` 读**顶层** `role`，而 OpenAI chat 载荷
顶层没有 `role` 键 ⇒ v1 实际**恒为 `'user'`**。迁到 delta 后若按末条消息的 `role` 读，
助手轮会返回 `'assistant'` ⇒ 这是**行为变更，不是等价复现**。

### §9.35.5 因此本轮**没有**做的事，和为什么

- **没有**把那 8 个可复现读点改指 session 侧。理由：同一次改动里会有 8 处
  「包一层 `{"messages": …}`」的静默陷阱与 4 类不可端口读点共存，
  **一半改一半不改比全不改更难推理**；且用户拍板的是「保持现状」。
- **没有**新建 bodies 可移植性登记表门。理由：既有的
  `admin/request_logs_stop_write_classification_test.go` 已经在做
  「逐文件 + 逐字证据 + 默认拒绝」这件事，而且**它当前就是红的**
  （**31/105 未评估**，最后一次提交 `7a6356ef0`，与本轮无关）。
  在它还在红的时候另起一套关于同一批读点的登记表，等于制造
  **两套互相漂移的登记表**——这正是本审计反复拆除的那类隐患。
  正确顺序是先把既有那张门收绿，再在它上面加可移植性维度。

⇒ 缺口已**具名记账**于 §9.28 与本节，不作为「待办」冒充已完成。

---

## §9.36 把 S4 停写分级门**收绿**：31/106 → 0/106

§9.35 结尾定的下一步是收绿覆盖门。本节记结果，**并记三个我原本会判错的地方**。

### §9.36.1 结果

`TestRequestLogsStopWriteNothingLeftUnclassified` 由 **31/106 未评估**变为 **0**。
新增 31 条逐点评估（batch6 20 条 + batch7 11 条），另补 2 条 `nullPaddedUnaffectedJustification`
具名论证。八道相关门全绿：

| 门 | 状态 |
|---|---|
| `TestRequestLogsReadInventoryIsComplete` | PASS（106 文件 / 240 调用点双向一致） |
| `TestRequestLogsStopWriteNothingLeftUnclassified` | **PASS（0/106）** |
| `TestRequestLogsStopWriteClassificationEvidenceIsReal` | PASS（31 条证据逐字命中） |
| `TestRequestLogsStopWriteSourceFamilyCoversInventory` | PASS |
| `TestStopWriteEffectAgreesWithSourceFamily` | PASS |
| `TestNoUnregisteredVPaddedColumnReader` / `…PhysicalOnlyColumns…` | PASS |
| `go test -tags s4audit ./admin/` 全量 | **ok 69.6s，零 FAIL** |

**门必须变异验证**，三条各命中不同的门、且都是**断言命中**而非崩溃：

| 变异 | 红在 |
|---|---|
| M1 删掉一条新登记（实际跨了 6 条） | 覆盖门 `coverage_gate_test.go:40`，报「**6**/106 未评估」——**精确数出我删的条数** |
| M2 把 Evidence 改成文件里没有的片段 | 逐字门 `classification_test.go:939` |
| M3 抽掉 `session_turns_tree` 的 null-padded 具名论证 | 族一致门 `classification_test.go:1328` |

*M3 第一版把字符串截断导致**编译失败**——崩溃不是证据，那一版变异无效，
改成按 map 条目边界删除、`go vet` 确认语法完好后才重跑。*

### §9.36.2 三处**我原本会判错**的地方

这批评估由四个子代理并行做，**三处出现两个子代理给出互相矛盾档位**。
它们在**两个方向上**都会错，所以逐条手验不是形式。

#### ① 判「视图读点停写后是否还供数」**必须量近期填充率，不能用全历史均值**

`admin/session_extract.go`：谓词是 `gw_task_id = $1`，而 710 视图的 `gw_task_id`
取自 `d.*`（details 的 LEFT JOIN）。**全历史口径**下 session 臂 94.1% 为 NULL
⇒ 看起来「停写后恒 0 行」⇒ 判 `silently_empty`。
**按天口径**（真库）：

| 日期 | 09-22 | 09-24 | 09-29 | 09-30 | 10-01 | 10-02 |
|---|---|---|---|---|---|---|
| `gw_task_id` 非空 | 0.36% | 5.34% | 4.77% | 37.88% | **97.72%** | **98.51%** |
| `api_key_prefix` 非空 | 33.60% | 37.30% | 99.98% | 100% | 100% | 100% |

⇒ details 写入链在 09-30 前后已修好，**近期行带着这些值**，读点照常命中
⇒ 正确档位是 `unaffected_by_stop_write`。**全历史均值会给出相反的结论。**

⇒ **纪律**：停写后果是**前瞻**问题，量具必须能回答「现在和以后」，
而历史均值被已修好的旧数据主导。

#### ② 同一个量，方向相反的第二处：`provider_id` 在**恶化**

`admin/session_analytics_breakdown.go` 的 provider 分解按 `rl.provider_id` 分组，
而视图里它是 `d.*`。真库按天：

| 日期 | 09-27 | 09-28 | 09-29 | 09-30 | 10-01 | 10-02 |
|---|---|---|---|---|---|---|
| `provider_id` 缺失率 | 22.15% | 46.38% | 47.75% | 51.87% | **68.03%** | 49.19% |

同期的 `outbound_model` 缺失率 **0.00%**、`cost_usd` NULL 率 **0.00%**。
⇒ 停写后**约一半新流量不再计入其真实 provider**（且 `provider_id=0` 不是 NULL，
`COALESCE(…,'unknown')` 收不住它们，会聚成一个退化的 0/'unknown' 桶）
⇒ 正确档位是 `silently_degraded_content`，**不是** `unaffected`。

**这条同时是一条新的运营事实**：会话族的 provider 归属当前只有约一半填得上，
而且**趋势在恶化**——它是退役决策的一个独立输入，不属于停写后果。

#### ③ 我自己犯的测法错误：按 `request_id` 跨存储面猜行来源

我一度判定 `request_logs_with_current_month_without_customer_id`
「70% 的行来自 session 族」，并据此差点把 `admin/usage_trend_series.go`
从 `silently_frozen` 改判成 `unaffected`。**那是错的。**
`pg_get_viewdef` 的真库定义是纯 `request_logs_hot UNION ALL request_logs`，
**不含任何 session 分支**。

错因：我按 `request_id` 把该视图的行与 session 族 LEFT JOIN 数「重合率」，
而**该包装视图没有顶层 710 那层反连接**（顶层用 `NOT EXISTS session_*` 去重），
于是 v1 行的 `request_id` 本来就与 session 行重合 ⇒ 70% 是**假象**。

⇒ **纪律**：判断一个视图有没有 session 臂，量具是 **`pg_get_viewdef` 逐字读**，
不是「按 id 猜行来源」——后者在有去重/无去重的两层视图之间会系统性造假。
（这与 §9.28 的「列名撞车」、§9.24 的「关系名相同不代表同一存储面」同族：
**跨面对齐 id 是最容易被当成「实测」的错误测法**。）

#### 附：子代理提出的 6 个 UNRESOLVED，两个我用真库直接定案

- `request_logs_with_current_month_without_customer_id` 有无 session 臂 → 上面 ③ 已定（**无**）。
- 线上生效的 `recent_success_rate()` 读哪张表 → 查 `pg_proc.prosrc`：
  **直读 `request_logs_hot`、3 小时窗口** ⇒ `admin/credential_success_rate.go` 判
  `silently_empty` 成立（子代理正确）。

### §9.36.3 收口后的分布与灰度含义

31 条新增评估的档位分布：

| 档位 | 条数 | 灰度含义 |
|---|---|---|
| `silently_empty` | 11 | **最危险**：接口 200、字段齐全、数值全零或空列表 |
| `unaffected_by_stop_write` | 8 | 读点在写门内 / 流量由 session 臂供给 / 读体量不读内容 |
| `silently_degraded_content` | 4 | 行还在但某列静默变空（bodies 正文腿、provider 归属） |
| `silently_frozen` | 4 | 冻结为停写前常数 |
| `errors_out` | 3 | 响亮失败，灰度立刻可见（**可接受**） |
| `validator_dual_read` | 1 | §9.35 的新鲜度探针 |

**⇒ 灰度前必须处理的静默档：**70** 条**（`silently_empty` + `silently_degraded_content` + `silently_frozen`）**
不是「门红了」，而是「门绿了但风险已被登记」。这张表现在是**可执行的清单**，
而不是「待评估」。

> ⚠ **本行数字由 `admin/audit_silent_count_consistency_test.go` 从登记表实时计算**
> （`TestAuditDocSilentClaimMatchesRegistry`）。改登记表而不同步改这一行，那道门会红。
>
> **§9.48 订正**：本句原先写「19 条」，那是在**只评估了 31 个新增读点**时的
> 样本外推（当时上表的 11 + 4 + 4），而文档没有标明它是外推值。§9.48 建门时
> 登记表已覆盖 106 个读点文件，**静默档实测 70 条**（见 §9.48.1 的完整分布），
> 相差 3.7 倍。上表那三行是 §9.36 当时的 31 条样本分布，**不是全量**。
>
> 为什么这个订正要紧：这句话被本节自己称作「可执行的清单」，它是排期会直接引用的
> 唯一量化口径。19 与 70 之间的差别不是「数字过时」，是「按三分之一的工作量排期」。

**本节不改变 §9.35 的结论**：登记表绿了不等于可以停写。
`auto_route_settle_worker` 仍会全量 abandon（只是现在看得见），
响应侧 7 个读点仍不可端口，正确修法仍是改读会话族。

### §9.36.4 途中发现、**未修**的两处相邻缺陷（记账，不在本轮范围）

1. **`discovery/discovery.go` 的控制面登记表已过期**：
   `request_logs_control_plane_dependency_test.go:297-309` 仍登记 `Gated: false`，
   Note 描述的是「未加护栏时」的失效形态，而护栏是 `e52687954`（§9.12）后加的。
   **没有任何一道门把 `Gated` 与代码里的实际护栏对照** ⇒ 这条过期不会被自动发现。
2. **`nullPaddedUnaffectedJustification` 已有 4 条是同一个误触发的产物**：
   族分类器用词边界匹配补位集里的 `id`，而命中常来自**别的表**的
   `WHERE id = $1`（`admin/attachments_routes.go` 的 `attachments` 是 HTTP 路径字面量、
   `admin/route_incidents.go` 的 31 处 `id` 全在 Go 代码里）。本批的
   `admin/live_stream_sse.go` 与 `bg/candidate_failure_monitor.go` 是同一形态，
   已在各自登记与论证里点名。**根修应是给补位匹配加表归属**，不是继续逐条写论证。

---

## §9.37 控制面登记表的 `Gated` 字段**从来不被验证** —— 补口 + 三条过期记录

§9.36.4 记了第 1 条相邻缺陷（`discovery/discovery.go` 的 `Gated:false` 与代码矛盾）。
本节把它查到底，发现**不是孤例**，并补上那道一直缺失的门。

### §9.37.1 洞：`Gated` 是个装饰字段

`requestLogsControlPlaneReaders` 每条登记有 `Gated bool`（「该消费方是否被 S4 写门覆盖」）。
本轮核对发现，**在 2026-10-02 之前没有任何一道门验证过它**。
既有那道 `TestRequestLogsControlPlaneKnownEntriesAreReal` 只核三件事：
Evidence 非空、Evidence 在登记文件里逐字存在、`Live && !Gated` 时 BlastRadius 非空。
**`Gated` 自己从不被读。**

### §9.37.2 后果已经发生三次，而且每一次都可完整复原

| 登记条目 | 护栏引入 | 登记表最后改写 | 差 |
|---|---|---|---|
| `discovery/discovery.go` | `e52687954` 12:25 | `af4ef4b32` **12:14** | 晚 11 分 |
| `bg/credential_recovery.go` | `9b8424fd8` 14:00 | `b585c036e` **11:55** | 晚 2h05m |
| `bg/ledger_reconciliation.go` | `dfd4da2f1` 13:35 | `b585c036e` **11:55** | 晚 1h40m |

三次的形状**完全一样**：先写登记（`Gated:false` + 一段描述失效形态的 Note），
后加护栏，**此后门全绿、登记表一个字不动**。
`discovery` 那条的 Note 至今写着「**本表方向最危险的一条**…停写后 NOT EXISTS 恒真
⇒ 主动禁用仍在工作的凭据模型」——**而那件事已经被修掉了**。
这张表当时正在对外说假话，而且没有任何一道门会发现。

**为什么没人发现**：三条都符合既有门的所有判据——
Evidence 仍逐字存在（代码没删那段 SQL）、`Live && !Gated` 时 BlastRadius 仍非空
（三条的 BlastRadius 当时都填着）。**只有 `Gated` 这一个字段是凭记忆写的，
而它是唯一一个不被检查的字段。**

### §9.37.3 补的门：`TestControlPlaneGatedFlagAgreesWithCode`

判据是**默认拒绝 + 具名豁免**：

> 文件（**剥掉 Go 注释与 SQL 注释后**）出现 S4 门控标识符
> ⇒ 该登记必须 ① `Gated: true`，或 ② 在 `gatedFlagExemption` 里具名说明为什么不是。

**为什么不一刀切禁止**——方向不对称：「文件里有护栏」**不能**推出「登记的那个读点被门控」。
一个文件可能有多个读点，护栏只盖住其中一个；护栏也可能在调用方。
一刀切会再次误伤正确代码——而**一个把「在」报成「不在」的门比没有门更坏**。

**为什么不断言反方向**：「`Gated:false` ⇒ 文件里不该有护栏」是**证伪不了的**
（护栏完全可能在调用方、另一个文件、或接口注入）。本门只断言能被证明的那一侧。

配套 `TestGatedFlagExemptionIsNotStale` 查**反方向**：豁免表里若有一条已不再出现
门控标识符（护栏被删了/改名了），它在**掩盖**一件该重判的事——**失效的豁免比没有豁免更坏**。

### §9.37.4 写这道门时**我自己**踩的假阳性面

第一版只剥 Go 注释（`//` 与 `/* */`），结果在 `bg/auto_route_affinity_worker.go` 上误报。
**那一层的注释根本不是 Go 注释**——它藏在 raw string 里的 SQL 注释中：

```
-- settings.KeyRequestLogsWriteEnabled 声明的 request_logs 宽族），而本查询的
```

而那个文件**根本没有 Go 层护栏**。⇒ 第一版的门会**要求为不存在的护栏写豁免**，
**一道逼人写假豁免的门**。已改为三段剥离（Go 行注释 → Go 块注释 → SQL 行注释，
后者**复用包内已有的 `stripSQLLineComments`**，不另起同名正则以免编译冲突）。
修好后该文件正确退出误报清单。

### §9.37.5 逐条裁定结果：3 条需具名豁免，2 条已订正

先按「文件里有护栏标识符」筛出 7 个候选，再逐条追调用链——
**其中 3 条的 `Gated:false` 本来就是对的**：

| 文件 | 文件里有护栏 | 登记的读点被护住吗 | 结论 |
|---|---|---|---|
| `internal/trace/trace.go` | 有（`:473`） | **否**——它在 `FlushToPG` 里，护的是 Redis→PG 的 **UPDATE 写入**；登记的读点在**独立函数** `LoadFromPG`(:601)，调用方 `admin/request_trace.go:148-156` 也无门控 | 豁免 |
| `domains/hooks/observability/telemetry/client.go` | 有（5 处调用点） | **否**——护栏**全在写路径**；登记的两个读点 `FindRecentGatewaySession`(:607) 与 `lookupTurnNumber` 都不在其中，:2122-2125 的注释明写「outbox request-completed 在门控外照常提交」 | 豁免 |
| `bg/auto_route_affinity_worker.go` | **否**（只有 SQL 注释） | — | 修好 §9.37.4 后自动退出 |
| `bg/ledger_reconciliation.go` | 有（`:378`） | **是**——护栏在 `checkUsageCredit` **首行**(:377-384)，SQL 在 :386 才发出 | **订正为 `Gated:true`** |
| `bg/credential_recovery.go` | 有（`:1926`） | **是**——`return` 在 :1933，SQL 在 **:1939** 才发出、同函数体内 | **订正为 `Gated:true`** |
| `discovery/discovery.go` | 有（`:1110`） | **是**——`staleExpiryMayRun` 在 :1091 消费于 :1110 | **订正为 `Gated:true`** |

`client.go` 那条比 `trace.go` 更隐蔽：**护栏与读点在同一文件、同一包**，
只看「文件有没有门」必然误判——所以它必须由人具名承担，不能靠机械判据。

### §9.37.6 三条都清空了 `BlastRadius`，但**这不等于无事**

`BlastRadius` 的定义是「`Live && !Gated` 时必填 —— 它授权/改变的具体写入是什么」。
`Gated` 变 true 后这些写入在停写期间**根本不会发生**，留着旧值等于对外声明一件不存在的事。
三条已清空，并把「若护栏被误删/改坏，退化路径是什么」写进各自的 Note——
**护栏是可被回退的，Note 是那份回退路径的唯一记录。**

**但共同留了一个真缺口**：`SkippedChecks()` 是机器可读的「本轮未执行」通道，
`ledger_reconciliation` 与 `credential_recovery` 都调用了它，
而**全仓 grep 到的消费者只有测试**（`bg/ledger_reconciliation_s4_gate_test.go`、
`bg/credential_recovery_s4_gate_test.go`）——没有 metric、admin 端点或告警。
⇒ 停写期间，**护栏把危险动作停了，但「为什么没动作」只留在 `slog` 里**。
返回值 0 在计数上仍与「扫了没发现差异」不可区分。
**这是下一件该做的**（§9.36.3 清单里 control-plane 三条中的第三条）。

### §9.37.7 门必须变异验证：4/4，各命中不同的门

| 变异 | 红在 |
|---|---|
| M1 `discovery` 改回 `Gated:false`（**复现 `af4ef4b32` 时的历史状态**） | `gated_flag_test.go:139`，指名 `staleExpiryMayRun` |
| M2 抽掉 `internal/trace` 的具名豁免 | 同门（`:131`，行号上移是因豁免被删了 8 行——**两次行号不同恰恰说明两次变异都生效了**） |
| M3 `ledger_reconciliation` 改回 `Gated:false` | 同门（`:139`），指名 `usageCreditComparability` |
| M4 加一条失效豁免（文件无门控标识符） | `TestGatedFlagExemptionIsNotStale`（`:179`） |

*M1 第一版**没注入成功**（gofmt 改了对齐空格数，锚点没匹配上，测试照常 `ok`）——
**没生效的变异不是证据**，按锚点重做后才算数。*

---

## §9.38 `SkippedChecks()` 的可观测出口：把「没跑」从「跑了没发现」里分出来

§9.37.6 留下的缺口。本节把它闭合，并**顺带修掉一个被它暴露出来的陈旧状态 bug**。

### §9.38.1 缺口是可证实的，不是推测

两处 S4 停写门控都在**发出 SQL 之前**短路、都 `return 0`：

| worker | 门控函数 | 短路点 | SQL |
|---|---|---|---|
| `bg/ledger_reconciliation.go` | `usageCreditComparability` | `checkUsageCredit` 首行（`:377`） | `:386` 才发出 |
| `bg/credential_recovery.go` | `lookbackComparability` | `scanLookbackRecoveries`（`:1937`） | `:1950` 才发出 |

**全仓 grep `SkippedChecks` 的消费者 = 2 个 s4_gate 测试，零生产消费者。**
而 `settings.RequestLogsWriteEnabled()` 本身也**没有任何指标**——
S4 停写这个状态在 `/metrics` 上完全不可见。

停写期间的实际后果：`maas_reconciliation_findings` 不增长、lookback 无候选，
运维读到的是「账务无差异 / 无需恢复」，而真相是这两个问题**已经不再可判定**。

**既有的累计计数器救不了这个洞**：`credential_recovery` 已有
`llmgw_recovery_lookback_triggers_total{outcome="skipped_s4_stop_write"}`，
但它是累计的——停写生效后它**停止增长**，而「计数器不再增长」与「worker 卡死」
在告警侧完全同形。要回答的是**当前状态**，所以补的是 gauge 而不是 counter。

### §9.38.2 三个指标，每个都有告警在消费

| 指标 | 类型 | 回答的问题 | 消费它的告警 |
|---|---|---|---|
| `llm_gateway_bg_s4_scan_skipped_last_run{worker,reason}` | gauge | 本轮**没执行**吗 | `BgS4ScanSkipped`（for: 10m） |
| `llm_gateway_bg_s4_scan_last_run_unix{worker}` | gauge | worker 还活着吗 | `BgS4ScanStalled`（> 3600s, for: 15m） |
| `llm_gateway_bg_s4_scan_unregistered_skip_total{worker,reason}` | counter | 指标本身在**谎报**吗 | `BgS4ScanUnregisteredSkipReason` |

**刻意不加**累计型 `..._skip_total`：当前状态已由 gauge 表达，
加一个没有告警消费者的计数器就是 §9.37 说的装饰。`last_run_unix` 若不加
`BgS4ScanStalled`，它自己就成了装饰——所以「新增的每个指标都必须在告警里出现」
被写成契约门（`s4_scan_skip_test.go:61`）。

接线用 **`defer`**：逐个 `return` 手写一遍是「只补了已知路径」的老形状。
`defer` 覆盖所有 return 分支，包括未来新增的。

### §9.38.3 默认拒绝：新跳过源必须登记，否则门红

`reason` 是 gauge 的标签值，**标签空间必须严格等于闭集**。
若新跳过源带着未登记的 reason 上线，它**不会**被写进 `skipped_last_run`——
于是 gauge 停在 0，指标把「跳过了」**谎报成**「跑过了」。
这比没有指标更坏，所以两侧都堵：

- **源码侧**：扫描包内所有**具名结果为 `(comparable bool, reason string)`** 的函数，
  收集其能产出的 reason（含字面量与常量解析），要求全部在闭集内。
  判据是**签名**而非函数名白名单 ⇒ 新增跳过源时门不会静默放过。
  （`bg` 里其余返回 `(bool, string)` 的函数全是**无名**结果，不被扫到，
  也不该被扫到：它们返回错误文案，不是跳过原因键。）
- **运行时侧**：未登记的 reason 落进兜底计数器 + `slog.Error`，
  **不写进闭集 gauge**。默认拒绝，宁可吵闹不可静默谎报。

空串被显式排除：两个 comparability 的成功分支都是 `return true, ""`，
空串表示「本轮可判定、真跑了」，不是原因键。

### §9.38.4 顺带修掉一个真 bug：`resetSkipped()` 早退在它之上

`scanLookbackRecoveries` 的 hook 早退

```go
if r.ursmRecoverSink == nil && r.probeSubmitter == nil { return }
```

**原本位于 `r.resetSkipped()` 之上** ⇒ 「什么都没做的一轮」返回**上一轮**的 skip 列表。
当时是潜在的（hook 构造后不变，首轮起就恒定早退），但**一旦把列表发布到 `/metrics`，
它就变成运维可见的谎报**——这正是 `ledger_reconciliation.go:193-195`
注释里警告的「陈旧 skip 列表是最坏形态」。已把 `resetSkipped()` 上移到早退之前，
并立门钉住顺序（`TestResetSkippedPrecedesEarlyReturnInLookbackScan`）。

**这不是顺手美化**：把一个潜在 bug 接上告警，等于把它从「没人看得见」
升级成「所有人看见错误的值」。

### §9.38.5 我自己这道门先写错了三次——都记在这里

| 症状 | 真因 | 教训 |
|---|---|---|
| GW-00 守卫报 `108 could not be applied builtin len()` | `for _, x := range someString` 迭代的是 **rune 不是行**，`require.NotContains` 在对 int32 调 `len()` ⇒ **这道守卫一行都没真正检查过** | 对字符串 `range` 出的是字节；断言前先确认迭代出的类型是不是你以为的 |
| 闭集门报 `skip reason "" is not registered` | comparability 成功分支的 `return true, ""` 被当成 reason | 判据要先问「这个值的语义是不是我以为的那种」 |
| 「`scanLookbackRecoveries` 不再调用 `resetSkipped`」 | `r.resetSkipped()` 的 `Fun` 是 **`*ast.SelectorExpr`**（带接收者），我只判了 `*ast.Ident` ⇒ 门红在一个**从未存在过**的缺陷上 | §9.35 记过「接线门只认 `*ast.SelectorExpr` 而 `writeJSONOk` 是包级函数」，这次是**反向**：方法是 SelectorExpr、包级函数才是 Ident。两种都要认 |
| 「hook 早退不见了」 | `a == nil && b == nil` 解析成 `BinaryExpr{Op:LAND, X:BinaryExpr{Op:EQL, X:SelectorExpr, Y:nil}}`——选择器在 `be.X`，我查的是 `be.Y` | AST 判据必须拿真实解析结果对照，不能凭印象写 |

前两条尤其要记：**一个恒红或恒「误报不存在缺陷」的门，比没有门更坏**——
它训练读者忽略自己，久了真的坏了也没人看。

### §9.38.6 门必须变异验证：6/6，各命中不同的断言

| 变异 | 红在 | 证明的是 |
|---|---|---|
| M-D 归零循环只写 1 不写 0 | `s4_scan_skip_metrics_test.go:253` | 显式归零承重（§9.35 M3 的回归） |
| M-B `RunOnce` 的 `defer` 降级为普通调用 | 同文件 `:152` | 门认的是 **defer**，不是「出现过这个调用」 |
| M-C 把 `resetSkipped()` 挪回早退之下 | 同文件 `:226` | 顺序被钉住 |
| M-A `lookbackComparability` 返回未登记 reason | 同文件 `:102` | 闭集默认拒绝 |
| M-E 抽掉未登记 reason 的兜底计数器 | 同文件 `:275`（vet 干净 ⇒ 确认是**断言红**而非编译红） | 未登记 reason 不会被静默吞掉 |
| M-F′ 规则数保持 3，只让 `last_run_unix` 失去告警 | `s4_scan_skip_test.go:61` | 覆盖断言承重，**不是**靠 `Len(3)` 蒙对的 |

**两次「变异没生效」被当场识破并重做**——这正是它们没有污染证据的原因：

1. **M-C 第一版**：只加了标记，`resetSkipped()` 的位置**根本没动** ⇒ 门正确地保持绿。
   若就此记「M-C 通过」，就是拿一个没生效的变异当承重证据。改成真的下移后命中 `:226`。
2. **M-E 第一版**：写出多余 `}` ⇒ `log/slog` 变成未使用导入 ⇒ **编译失败**。
   编译红不是断言红。保留日志、只抽掉计数器后，vet 干净、`:275` 命中。

M-F 也先做了一版「删掉整条规则」，报在 `Len(..., 3)` 这个结构断言上——
它只证明规则数变了，没证明「指标失去了消费者」。于是补做 M-F′（规则数不变、
只换掉一个 expr 里的指标名），确认承重的是覆盖断言本身。

### §9.38.7 留在本档范围之外

- **admin 端点没做**。`admin.Handler` 已经持有 `credRecov`（`handler.go:78`），
  零接线可达；但 `LedgerReconciler` 只是 `cmd/gateway/main.go:4838` 的局部变量，
  从未注入 Handler ⇒ 要做就得改 `main.go`（共享工作区里冲突面最大的文件），
  去重复一个 `/metrics` 已经承载的事实。**判据是新字段先问「哪道门会读它」**，
  同理也适用于新端点：没有第二个消费方就不开这个面。
- **响应侧 7 个读点仍不可端口**、**族分类器 `id` 误触发未修**、
  **`auto_route_settle_worker` 的正确修法（改读会话族）未做**——见 §9.36.3 清单。

---

## §9.39 族分类器的 `id` 误触发：给补位匹配加**表归属**

§9.36.4 记的第 2 条相邻缺陷。根因一句话：`np`（谓词级 NULL 补位）只判断
「补位列名在**这个文件里出现过**」，而「出现过」与「用在这个视图上」是两件事。

### §9.39.1 误触发长什么样：列同名，表不同

补位集现在只有 6 列（`id` / `test_col` / `test_tab_indent` / `provider_model` /
`credits_rate_multiplier` / `client_ip`），而 `id` 是最容易被撞上的一个。
逐个打开本轮离开本族的文件，命中全部来自**别的表**或**根本不是 SQL**：

| 文件 | `id` 命中实际属于 | 行号 |
|---|---|---|
| `admin/auto_title_generator.go` | `api_keys` 表（`ak.id`） | :1174-1182 |
| `admin/logs_summary.go` | `api_keys` 表 + 一条 Go 正则字面量里的 `correlation_id` | :303-308, :28 |
| `admin/session_title.go` | `t.id`，而 `t` 是 session_turns 别名（视图别名是 `rl`） | :328 |
| `bg/stats_minute_rollup.go` | `request_stats_rollup_cursor` 表（`WHERE id = 1`） | :94/:115/:131 |
| `domains/routeincident/store.go` | route_incidents 表自身（`SELECT/RETURNING/WHERE id`） | :227/:310/:387 |
| `bg/candidate_failure_monitor.go` | `candidate_failure_logs` 腿 | :397 |
| `admin/session_turns_tree.go` | 只在投影，且被同表达式非补位列 COALESCE 兜住 | :228/:321 |

**这 7 条的档位一条都不需要改**——它们的 `Effect` 登记本来就正确。
被误伤的只是**族**：多了一条「必须具名论证」的负担，
以及一份让人以为「这 11 条都真的碰了补位列」的清单。

### §9.39.2 归属粒度选错两次，两个方向都踩过

这不是「想清楚再写」的事，是**量出来**的：

1. **整文件口径**（原实现）：19 个本族文件里 **8 个**是这么带进来的。
2. **字符串字面量口径**（我第一版）：看起来更精确，实测 `strictOnly = 0`
   看着很美——直到手验 `bg/shared_pick.go` 发现它是**拼接 SQL**：
   `SELECT client_model … FROM request_logs_with_current_model rl WHERE … AND
   client_model IS NOT NULL` 中间夹着 `<ProbeTrafficExclusionPredicateView>` 占位，
   含 `client_model` 的字面量不出现视图名、含视图名的字面量不含 `client_model`。
   逐字面量口径会把这个**真谓词**（该读点唯一产出就是按 client_model 分组取最常用
   模型，而它是补位列 ⇒ 恒 0 行）判成误触发。
   **一个更「精确」的判据反而更危险，因为它悄悄放走了真触发。**
3. **函数作用域 + 包级字面量**（最终）：函数体 ⊇ 单个字面量，所以口径 2 能命中的
   它一定命中；口径 1 的「同文件不同表」被挡住。
   代价是**包级 `const xxxSQL` 形式的读点整段在函数之外**，必须单独作为作用域——
   这一条也是被门抓出来的，不是想到的（见 §9.39.4）。

### §9.39.3 结果：族从 19 收到 12，档位一条没动

| 族 | 改前 | 改后 |
|---|---|---|
| `view_with_null_padded` | 19 | **12** |
| `view_only` | 12 | 16 |
| `bodies_plus_other` | 21 | 24 |
| 其余三族 | 54 | 54 |
| 合计 | 106 | 106 |

留在本族的 12 个里，`bg/shared_pick.go`（拼接 SQL 的真谓词）与
`admin/top_problems.go`（`silently_empty`）都在——**档位与族的组合没有被这次
改动推翻任何一个**。

### §9.39.4 我自己写的那道「方向性」门当场抓到了我的实现缺陷

`TestNullPaddedAttributionNeverLosesALiteralLevelHit` 断言
「函数体 ⊇ 字面量 ⇒ 逐字面量能命中的，函数口径必须也命中」。
它第一次跑就报红两个文件：`domains/sessionforensics/export.go` 与
`domains/streaming/model_alternatives.go`。打开一看：两者的 SQL 都是
**包级 `const`**（`forensicsExportMessagesSQL` :37/:57、`alternativesSQL` :180），
整段在函数之外，函数口径根本看不到。

**没有这道门，这个缺陷会一直绿着**——因为它只表现为「少认了几个文件」，
而少认的方向恰好是本轮要修的方向，看起来像正常的进展。

### §9.39.5 顺带暴露并修掉：一条注释在说用原文、代码在用剥过的

第一版 `sourceFamilyOf` 里我写了注释「np 用的是原始 code（未剥注释）」，
但函数开头已经把 `code` 覆盖成剥过注释的版本了。后果：`admin/logs_summary.go`
靠 `nullPaddedPredicateHit` 的「解析失败退回整文件」fallback 留在本族——
**一个已经离族的误触发被一条 fallback 悄悄请了回来**。

判别它的不是读代码，是**列出每个文件的 `via` 列**：`logs_summary.go` 的 `via` 是空串，
而它在族里——两个判据对不上。`sourceFamilyOf` 现在显式保留 `raw`。
门里也加了「归属命中与族归属必须一致」的一致性断言（bodies 族除外，因为
switch 里 bodies 优先）。

### §9.39.6 失效豁免：删掉 5 条，并承认这是**降低**了门槛

改动让 5 条 `nullPaddedUnaffectedJustification` 失去对象
（`session_timeline_query` / `session_turns_tree` / `session_turns_unified` /
`candidate_failure_monitor` / `gateway_adapters`）。按 §9.37 的纪律
（失效的豁免比没有更坏）已删除，并补上 `TestNullPaddedJustificationIsNotStale`
常驻检查。

**但要如实说：这对这 5 个文件是降低了门槛。** 它们从「`familyViewNullPadded`
+ `unaffected` ⇒ 必须有具名论证」变成了「`familyView` + `unaffected` ⇒ 无要求」。
它们的 unaffected 判断现在只靠族层面的事实（真库实测 24h 内 36.55% 的视图行来自
session_turns ⇒ 纯视图读者停写后仍供数），不再有逐文件的书面论证。

被删的论证里有价值的部分（`candidate_failure_monitor` 的「5 分钟窗口仍由
turn_writer 持续供数所以不是 empty」、`gateway_adapters` 的 COALESCE 顺序分析）
**已抄录进本节**，但它们不再被任何门强制更新——这是本轮引入的**已知弱化**，
列在下方残余风险里。

### §9.39.7 仍未处理的已知不精确

`domains/streaming/model_alternatives.go` 的视图名只出现在**字符串字面量里的
SQL 注释**中（`-- request_logs_with_current_month is a UNION of …`，:230）。
本轮没有改 `vi`（视图族判定）那一维的注释处理——那是另一个维度的语义，
动它会把该文件整个换族，属超出本轮范围。当前它因此留在本族（保守方向，安全）。
本仓已有 `stripSQLLineComments` 可用，但**用它会同时改变 `vi`**，
必须单独评估，不能顺手带进来。

---

## §9.40 `auto_route_settle_worker` 的正确修法：实测**否决**了两个直觉替代方案

§9.35 的结论是「让它可见，正确修法是改读会话族」。本节去做那个修法，
结果发现**这个结论本身过于简单**。本节的价值主要在**证伪**上。

### §9.40.1 方案 A：换读 710 视图 —— 被计划否决，不是被报错否决

最直觉的替代方案是读 `request_logs_with_current_month`（它本来就是 v1 ∪ 会话的并集）。
先查它的定义：真库 `pg_get_viewdef` 显示它引用 8 个关系，**包含 citus 父表 `request_logs`**。
文件顶部那段「DO NOT add `UNION ALL request_logs`」的警告因此**在实质上适用**。

但那条注释描述的报错（`invalid perminfoindex 0 in RTE with relid 0`）在这条查询上
**没有复现**。把 worker 的真实 `settleBatch` LEFT JOIN 原样打到真库上 EXPLAIN，
实测到的是更坏的东西——**计划**：

```
Seq Scan on request_logs_2026_07 / _08 / default …
Index Scan on request_logs_2026_09_ts_idx2 / _10 / _11 …
```

即 citus 父表被展开成 **7 个叶子分区扫描**。这个 worker 每 30 秒跑一次、每次 100 行。

> **一个「没报错但计划烂掉」的替代方案比报错那个更危险**，因为它更容易被接受——
> 报错会被人停下来看一眼，计划不会。所以门里写的是「实测到的**计划**代价」，
> 而不是复述那条报错。

### §9.40.2 方案 B：换读会话族 —— 不是等价替换

真库 2026-09 分区实测（`auto_route_selections_2026_09` 有 1747 行，
注意 `auto_route_selections_hot` 当前**是空的**——本地库没有这类流量，
只能用 9 月分区，这是取样上的妥协）：

| 维度 | v1 | 会话臂 | 判定 |
|---|---|---|---|
| selection 的 `request_id` 覆盖率 | 1746 | 1734（**99.3%**） | 可平移 |
| `success` | 100% | 100% | 可平移 |
| `latency_ms` | 99.3% | 100% | 可平移 |
| `cost_usd` | **3.6%** | **100%** | 会话臂**更好** |
| session 身份 | `gw_session_id` 100% | `session_id` 100%（0 条 `sys:%`） | 可平移 |
| `origin_actor` | **0** | **0** | **两侧都空** |
| `canonical_id` | **32.3%** | **1.9%** | **真退化** |
| `is_auto_request` | 99.9% | **83.0%** | **真退化** |

**两处硬伤**：

1. **`canonical_id` 在会话族里根本没有来源**。`session_turns` / `session_turn_details`
   都没有这一列（查 `information_schema` 确认：会话族里带 `canonical_id` 的表一张都没有）。
   ⇒ `settleBatch` 的 LATERAL `retry_count` 腿**无法平移**。
   **这是架构缺口，不是工程问题**——要修得先决定是回填会话侧还是改写 retry_count 定义，
   而后者直接改动 **reward 语义**。
2. **`is_auto_request` 差 17pp** ⇒ `loadTaskBaselines` 的 cohort 缩水约 17%，
   p95/p75 基线随之改变。

**顺带更正一条可验证的细节**：`origin_actor` 在**两侧都是 0**，
所以 `SQLExcludeSyntheticActors` 对这批行**早就空转**。
我原本把它列成移植的障碍之一（因为会话臂该列 100% NULL），
实测发现 v1 侧同样是 0 ⇒ 它既不是移植引入的新问题，也不构成障碍。
但这意味着「排除合成流量」这个假设在本 worker 上**已经不成立**，应当单独记账
（它影响的是**现在**的基线质量，与停写无关）。

### §9.40.3 取样方向：差点得出相反结论

第一轮测量用的是**今日 hot 分区**，结果是：v1 的 `is_auto_request` 行
**0%** 能在会话臂配到，会话臂里也 **0** 条 auto 行。
按那个数字下结论就是「auto-route 根本不写会话族，方案 B 直接否决」。

**那是错的**，因为今日 hot 里的 auto 行 2172 条中有 **2158 条是 `probe-%`**
（`origin_actor` = `active-probe-worker` / `node-probe-worker`）——
那是探针流量，是另一个群体。会话写方不覆盖它们。

改用**这个 worker 真正关心的总体**（`auto_route_selections` 的分区）重测，
才是 99.3%。**「按下游表取样」而不是「按上游表的某个标签取样」**——
判一个 join 腿能不能平移，取样必须是 join 的另一侧。

### §9.40.4 我自己那道门第一版有假阳性，又太弱

- **假阳性**：第一版对整份源码跑 `(?i)JOIN\s+request_logs\s`，
  命中第 15 行**注释里的散文** `//  3. Join request_logs for success / latency / cost.`
  ⇒ 改成只对 **AST 提取的字符串字面量**（即真正的 SQL）跑判据。
- **太弱**：文档门第一版查「注释里有没有 `canonical_id`」——
  而这个词在文件里出现十几次（SQL 里就有 6 处），**把整段实质文档删光它照样绿**。
  ⇒ 判据钉到只有实质文档才有的**特征句**（`会话族里根本没有这一列`、`叶子分区 Seq Scan`）。

两道都记在这里，因为**一道删掉它所守之物之后仍然通过的判据，就是装饰**。

### §9.40.5 变异 2/2，其中一次「变异没生效」被当场识破

| 变异 | 红在 |
|---|---|
| N1 把 `LEFT JOIN request_logs_hot` 换成 710 视图 | `auto_route_settle_source_gate_test.go:87` |
| N2 删掉 `canonical_id` 缺口的整段记录 | 同文件 `:133`（钉特征句之后才真正承重） |

*N2 第一版只在一行末尾加了标记，而那行本来就不含 `canonical_id` ⇒ 门正确地绿。
**没生效的变异不是证据**（同 §9.38 的 M-C）。重做成真的删掉整段后命中。*

### §9.40.6 本节**没有**改那三个读点，理由

正确修法必须先决定 `canonical_id` 缺口怎么办，而那会改动 reward 语义。
在一个**本地 hot 分区为空、无法验证运行时行为**的环境里改 reward 输入，
是拿「看起来更正确」换「无法验证」。不做。

已落地的是可证的约束：两道门 + 把完整实测评估写进文件注释 + 更正登记表里
那条已被证伪的 Note。**下一步需要一个明确决定，不是一道门能单方面定下来的事。**

---

## §9.41 `retry_count` 的推导对**真实数据**从未成立过 —— 一个活的运行时错误

§9.40 结尾说「下一件需要一个决定」。在去要那个决定之前，先把决定所需的数字量出来——
结果在量 `canonical_id` 可回填性的时候，撞上一个**与停写完全无关**的现存缺陷。

### §9.41.1 缺陷

`settleBatch` 的 LATERAL 腿（修复前）：

```sql
SUM(GREATEST(COALESCE(jsonb_array_length(r2.routing_attempts), 1) - 1, 0))::int AS retry_count
```

它假设 `routing_attempts` 是 JSON **数组**。它不是。写方
`executors.RoutingAttemptsTracker.ToJSONBytes` 产出的是

```go
data := map[string]interface{}{"attempts": attempts}   // ← object，不是 array
```

真库实测（PG 17.10）：

```
SELECT jsonb_array_length(routing_attempts) FROM request_logs_hot
  WHERE routing_attempts IS NOT NULL LIMIT 1;
ERROR:  cannot get array length of a non-array
```

真实样例：`{"attempts": [{"seq": 1, "result": "error", "raw_model": "deepseek-chat", ...}]}`。

**且 `array` 形态在 `request_logs` 里从未存在过**：
340,917 行非空值、最早 2026-09-03，全部是 `object`。
⇒ 这个表达式**从来没有对过真实数据**。

### §9.41.2 失败范围不是一行，是整条查询

LATERAL 在 `settleBatch` 的主查询里。一行抛错 ⇒ **整条语句中止**
⇒ 那一批最多 `settleBatchSize` = **500** 条 selection 全部不结算。
调用方只 `slog.Warn("auto-route settle sweep failed")` 后 `return`，
而下一轮重查的是**同一批**（`WHERE settled_at IS NULL ORDER BY ts LIMIT 500`）
⇒ **反复卡在同一处**，不是一次性丢一批。

**实测影响面**（2026-09 分区，1747 条 selection）：

| 总体 | 条数 | 占比 |
|---|---|---|
| `canonical_id IS NULL` ⇒ LATERAL 的 WHERE 恒假 ⇒ 无匹配、**不报错** | 1183 | 67.7% |
| 有 `canonical_id` 且匹配行**全部** `routing_attempts IS NULL` ⇒ 不报错 | 104 | 6.0% |
| 有 `canonical_id` 且匹配行**带** `routing_attempts` ⇒ **整批中止** | **460** | **26.3%** |

即约 **26% 的 selection 会毒化它所在的整个批次**。

### §9.41.3 顺带量化了一个**方向相反**的偏差（停写关闭的今天就在发生）

`computeSelectionReward` 里：

```go
if p.modelReqsInSes != nil && p.retryCount != nil && *p.modelReqsInSes > 0 {
    in.RetryRatio = float64(*p.retryCount) / float64(*p.modelReqsInSes)
}
```

`canonical_id IS NULL` 的那 **67.7%** ⇒ LATERAL 无匹配 ⇒ `model_reqs = 0`
⇒ 守卫不成立 ⇒ `RetryRatio` 保持 **0** ⇒ `retryScore = 1.0 - 0 = 1.0`（**满分**）。

`retryScore` 权重 **0.10**。所以：

> **三分之二的已结算 selection 在白拿 retry 项的满分**，而「没测到」被当成了
> 「测到完美」。

`ComputeRoutingReward` 的文档注释写着「Unknown inputs resolve to neutral 0.5 rather
than 0, so 'not measured' is never mistaken for 'measured as bad'」——
**这条不变量在 `RetryRatio` 上不成立**：未测得解析成 0，经 `1.0 - ratio` 变成**最好**，
而不是中性。这与本审计反复记录的「把缺失报成在场」是同一族，且**方向是抬高而非压低**。

**这一条不需要等停写**——它是现在线上 reward 分布的一部分。

### §9.41.4 错误的第二处化身：写方注释把 bug 写成了契约

`executors/routing_tracker.go` 的注释原文：

```
// request_logs.routing_attempts is consumed
// arithmetically. bg/auto_route_settle_worker.go derives
//	retry_count = jsonb_array_length(routing_attempts) - 1
```

**那正是消费者的 bug**。把它写进写方注释，等于给下一个「照着注释改」的人发了一份
错误契约。已一并订正为 `len(routing_attempts -> 'attempts') - 1`，
并立门禁止它回来（`TestRoutingAttemptsWriterShapeIsDocumented`）。

### §9.41.5 修法

抽成具名函数 `retryCountPerRowSQL(alias)`（可静态测），按 `jsonb_typeof` 分派：

- `'array'`  ⇒ 整列（防御性；真库 34 万行从未出现该形态，但删掉它会让未来的假想
  情形**静默退化成 0 重试**，而多一个 CASE 分支代价为零）；
- `'object'` ⇒ `-> 'attempts'`（**写方实际产出**，这是修复的主体）；
- 其它 / NULL ⇒ 走外层 `WHERE jsonb_typeof(v.a) = 'array'` 守卫，最坏退化为
  「这行算 0 次重试」而**不是**整条查询中止。

真库验证（同一批数据，旧表达式报错 vs 新表达式）：

```
旧: ERROR:  cannot get array length of a non-array
新: 1002 行 → retry 总数 1669（无报错）
```

### §9.41.6 门：3 道，变异 3/3

| 变异 | 红在 |
|---|---|
| P1 删掉 `-> 'attempts'` 分支 | `auto_route_retry_count_test.go:50` |
| P2 整段退回旧的裸 `jsonb_array_length(整列)` | 同文件 `:50`（4 条缺失断言）+ `:58`（「裸调用」专用判据） |
| P3 把错误契约放回写方注释 | 同文件 `:101` |

**为什么是形状门而不是集成测试**：`auto_route_selections_hot` 当前是空的、
本地 CI 库没有这类流量 ⇒ 一条需要真实行的集成测试在这里**证明不了任何事**
（§9.34：空库上的真库门是绿而无证据）。所以断言**可静态证明**的那一侧，
真实数据形态与实测数字记在本节。

### §9.41.7 仍未修：retry 项「未测得 ⇒ 满分」

§9.41.3 那个方向性偏差**没有**在本轮修。理由：修它等于改 reward 语义
（是让未测得落回中性 0.5，还是干脆把该权重按可用性开关），这与 §9.40 结尾
那个「需要一个明确决定」是**同一个决定**，应一起做，不宜夹带。

已记账为本轮**新发现的现存缺陷**（与停写无关），列在残余风险里。

---

## §9.42 retry 项：去掉 `canonical_id` 收窄 + 显式三态（用户 2026-10-02 拍板「一次做完」）

§9.41 结尾把两件事绑在一起交给用户决定：`(1) retry_count 的数据源`、
`(2) RetryRatio 未测得 ⇒ 满分`。用户选「一次做完」。本节是落地记录。

### §9.42.1 拍板前的测量：成本结构被翻转了

我在 §9.41 结尾写了「选 ① 之前必须先量：这 67.7% 里 `model_reqs` 本来会是多少」。
量完的结果把决定的成本整个翻转：

**去掉 LATERAL 的 `canonical_id` 条件后**（2026-09 分区 1747 条 selection）：

| 组 | 条数 | 带条件 model_reqs | 去掉条件 model_reqs |
|---|---|---|---|
| A：`canonical_id IS NULL` | 1183 | **0.00** | **1.00**（1182/1183 有匹配） |
| B：有 `canonical_id` | 564 | 1.00 | 1.00 |

A 组的分布是 min 0 / 中位 1 / p99 1 / **max 1** ⇒ **会话本就只有 1 个请求**，
所以「混进别的模型的流量」这个担忧在数据上不成立。

**跨模型污染到底有多少**（10 天 auto 流量，11,634 个会话）：

| 会话类型 | 数量 | 占比 |
|---|---|---|
| 单请求 | 11,299 | 97.12% |
| 多请求·同模型 | 334 | 2.87% |
| 多请求·**跨模型** | **1** | **0.01%** |

⇒ **保留该条件：代价 67.7% 测不到信号，收益 0.01%。** 净负收益，移除。

### §9.42.2 三态取代隐式判定

```
measured     model_reqs > 0                ⇒ retryScore = 1 - retry/model_reqs
unmeasured   model_reqs == 0（会话无可数行） ⇒ retryScore = 0.5 中性
unavailable  指针为 nil（LATERAL 无产出）     ⇒ retryScore = 0.5 中性
```

**`unavailable` 必须与 `unmeasured` 分开**：前者是「读不到」（停写后 worker 会
永久处在这个状态），后者是「读到了，确实是 0」。合并成一个就丢掉了停写时唯一
能看见的那个信号。

`autoroute.RewardInput` 新增 `RetryMeasured bool` 而**不是** `-1` 哨兵：
`RetryRatio` 是**比值**，它的 0 是合法的实测值（「测到、零重试」）；
`HealthComponent` 是**分数**，它的 -1 才可安全地保留为哨兵。
**把 0 复用成「未知」正是这个 bug 的成因。**

### §9.42.3 暴露方式：走指标，不走列

`reward_source` 有 CHECK 约束 `IN ('request','session')`（`db/db.go:7764`），
扩展取值需要迁移。所以三态经新指标暴露：

```
llmgw_autoroute_settle_retry_state_total{state="measured|unmeasured|unavailable"}
```

闭集三值，基数恒定。**没有它，运维从 reward 分布上看不出多少样本的 retry 项
是中性、多少是实测**——只能看到分布整体偏移。

### §9.42.4 两个既有测试把**错误语义写成了期望值**

| 测试 | 原期望 | 新期望 | 说明 |
|---|---|---|---|
| `TestComputeRoutingReward_UnknownsAreNeutralNotZero` | 0.775 | **0.725** | 它的注释逐项算的是 `… + 0.10*1`——**测试名叫「UnknownsAreNeutral」，retry 项却按满分算**。它与自己的名字矛盾。 |
| `TestComputeRoutingReward_Ordering` | 依赖 RetryRatio 生效 | 加 `RetryMeasured: true` | 排序断言靠改 `RetryRatio` 让 reward 变动；不标记 measured 时 retry 项是中性，`retried` 会与 `good` 打平。 |

**第一个测试的存在本身就是这个 bug 能活这么久的证据**：一个把错误值钉死的
「回归测试」，比没有测试更危险——它让 bug 看起来是被保护着的。

新增 `TestComputeRoutingReward_RetryIsTriState`，钉住三态的关键性质：
未测得的分数必须**恰好落在**两个实测极值的中点（retry 权重对分数是线性的），
且**不得等于**任一端。

### §9.42.5 门：3 道 + 1 道语义门，变异 4/4

| 变异 | 红在 |
|---|---|
| Q1 把 `canonical_id` 条件加回 LATERAL | `auto_route_retry_state_test.go:42`（两条断言） |
| Q2 把三态退回 `1.0 - ratio` 旧语义 | `affinity_test.go:308` / `:318`（新三态门） |
| Q3 指标不接线 | `auto_route_retry_state_test.go:86` |

① 是**删代码**，所以专门立门钉住它不在——删掉一个「看起来是防御性收窄」的
条件，下一个人很容易觉得它必要而加回来，而没有任何测试能证明它不在了。

### §9.42.6 残余风险（必须写清）

- **本次改动会让线上 reward 分布位移**：约 2/3 样本的 retry 项从 1.0 变成
  0.5（中性）或实测值。方向是**朝正确**，但**与历史 reward 不可比**——
  依赖绝对 reward 阈值的东西（若存在）需要一并复核。
- **0.01% 这个数字对当前流量形态成立**。本数据集被探针流量主导（探针天然
  单请求会话）。若日后 auto 流量中多轮对话占比大幅上升，跨模型比例会变，
  收窄条件可能需要以别的形式加回来——那时应当用 `RetryMeasured` 区分
  「测到 0」与「没测到」，而不是靠匹配不上来隐式表达。
- **未经线上端到端验证**：本地 `auto_route_selections_hot` 为空，只能在真库上
  验证表达式本身，worker 的端到端结算行为未跑过。

---

## §9.43 settleBatch 的数据源：按 S4 写门在 v1 / 会话族之间切换

§9.40 说「停写后本 worker 会全量 abandon，正确修法是改读会话族」，但被 `canonical_id`
挡住了。§9.41/§9.42 把那个阻塞拆掉之后，本节做移植。

### §9.43.1 先验技术可行性（三项都通过）

| 检查 | 结果 |
|---|---|
| 会话族是否有 citus / 列存问题 | **否**。`citus_tables` 里 `request_logs*` 与 `session_turn*` 都不在；`session_turns_hot` 是 heap |
| 移植版 outcome join 的计划 | **干净**：`Nested Loop Left Join` + `Index Scan using idx_session_turns_hot_request`，与现状同形，**没有** §9.40 那个 7 分区展开 |
| LATERAL 需要的列是否齐 | **全齐**：`request_id / session_id / routing_attempts / success / latency_ms / cost_usd / origin_actor / canonical_id / tenant_id / ts` 全部存在；`routing_attempts` 形态与 v1 **一致**（同为 `{"attempts":[...]}`）⇒ §9.41 的 `-> 'attempts'` 修法原样可用 |

LATERAL 唯一需要改的是会话键列名：v1 `gw_session_id` → 会话族 `session_id`。

### §9.43.2 为什么是「按门切换」而不是「直接换」

直接换会让**停写之前**的行为也变：

- 会话臂对同一批 `request_id` 的覆盖率是 **99.3%**（§9.40）⇒ 另外 0.7% 当场失去 outcome；
- `loadTaskBaselines` 的 `is_auto_request` 覆盖率只有 **83.0%** ⇒ 基线 cohort 缩水约 17%。

目标要求「确保数据在更改前后一致」。所以按 `settings.RequestLogsWriteEnabled` 切换：
**写门开着时读 v1（与今天逐字相同），关掉后读会话族。**

这与 §9.35 的「不门控 worker」不矛盾：那条说的是**不要门控 worker 的执行**
（门控只会让数字不再变化）；这里门控的是**读哪个族**，目的是让切换发生前行为不变。

### §9.43.3 三条腿必须同源

outcome join、LATERAL、`loadTaskBaselines` 若各读各的族，p95/p75 基线与被它归一化的
latency 就不在同一批行上算——**那比缺数据更隐蔽，因为数字都有值**。
所以三者由同一个 `settleSourceSpec` 驱动，门专门钉这一点。

### §9.43.4 默认方向

`settleSourceFor` 刻意**默认 v1**：只有明确读到写门关闭才切。
`settings.GetPlatformBool` 在存储未初始化时返回 true（写门=开着），
若代码默认走会话族，就会在**任何**配置下悄悄改源。

### §9.43.5 可观测性

新增 `llmgw_autoroute_settle_source_total{family="v1|session"}`，`init()` 预置两条序列。
没有它，切换的唯一信号是「settle 变慢了」或「reward 分布变了」——都太晚也太含糊；
而且它让「源已切但三条腿没同步切」这种半吊子状态暴露成一条只有两个取值的曲线。

### §9.43.6 门：4 道，变异 2/2

| 变异 | 红在 |
|---|---|
| M1 LATERAL 腿退回硬编码 `FROM request_logs_hot r2`（制造三腿不同源） | `auto_route_settle_source_test.go:74` + `:81`（两条断言） |
| M2 把 `settleSourceFor` 的默认方向反转 | 同文件 `:30` / `:33` / `:36`（三条断言） |

*门自己抓到一个真问题*：`TestSettleLegsAllUseTheSameSource` 首次跑就红，
报 `src.TurnsTable 只出现 2 次`——`loadTaskBaselines` 当时写的是
`currentSettleSource().TurnsTable` 而非命名变量。**三条腿都用了规格，但形态不一致**，
已统一为 `src`。这是「门写得比我想的更有用」的一个例子：它抓的不是缺功能，
是**一致性能腐化**。

另注：M1 第一版把 baseline 腿也硬编码，导致 `src` 未使用 ⇒ **编译红**。
编译红不是断言红，改成只动 LATERAL 腿（`src` 仍被 baseline 用到）后才算数。

### §9.43.7 残余风险

- **停写后的运行时行为未验证**。本地 `auto_route_selections_hot` 为空，
  会话族分支从未在真实 selection 上跑过。真库只验了**计划**与**列齐备性**。
- `is_auto_request` 在会话族 83% ⇒ 停写后基线 cohort 比 v1 期小 17%，
  p95/p75 会与历史不可比。这是**数据事实**（会话侧该标记覆盖更少），不是实现缺陷，
  但运维应知情。
- 三条腿的切换是原子的（同一 SQL 内），但 baseline 与 settle 是两次查询——
  若切换恰好发生在两次之间，一轮的 baseline 来自新族、结果来自旧族。
  影响窗口 < 1 轮，且下一轮即自愈；已记录未处理。

---

## §9.44 会话族分支第一次被执行，以及一个此前无人测量的失效形态

> 本节的两个标题都不是修辞：
> - 「第一次被执行」是事实——`auto_route_settle_worker` 的 `settleBatch` 会话族
>   分支在 §9.43 交付时**从未被任何进程执行过一次**。
> - 「此前无人测量」也是事实——基线 cohort 为空这件事在 §9.35 / §9.40 / §9.43
>   三节里都被当作「降级，不是失败」写在注释里，从未被测量过。

### §9.44.1 起因：一个查起来很顺的怀疑，结果是错的

§9.40 记着一句话：「`canonical_id` 会话族根本没有这列」。§9.43 据此把
`settlePendingSQL` 接到 `src.TurnsTable` 上，其中 outcome join 读 `rl.canonical_id`。
本节开工前的第一个怀疑是：**这个分支一跑就报列不存在**。

真库核对（`pg_attribute`）：

| 列 | `request_logs_hot` | `session_turns_hot` |
|---|---|---|
| `canonical_id` | bigint | **bigint（存在）** |
| 其余 10 个被读列 | 全部存在 | 全部存在 |

⇒ §9.40 那句话指的是 **710 视图的会话臂不投影 canonical_id**，不是这张表本身。
会话族有这一列。**这是一个差点写进新一节审计的假警报**，靠读定义而不是读转述躲掉了。

顺带核到一个也差点误报的东西：`auto_route_selections_hot` 本地 0 行。
`pg_class.relkind = 'r'`（普通表）、`relispartition = false`，而
`auto_route_selections` 是分区父表，hot 内容由 `bg/partition_manager.go` 的
`promote_auto_route_selections_hot_to_partition` 定期搬走 ⇒ hot 为空是**正常的**，
不是「worker 从来没结算过」的证据。若只看行数不看 relkind，这会是一条很难看的
假发现。

### §9.44.2 真正的问题：基线 cohort 会静默塌成中性

`ComputeRoutingRewardWithWeights`：

```go
latencyScore := 0.5
if in.P95BaselineMs > 0 && in.LatencyMs > 0 { … }
costScore := 0.5
if in.P75BaselineCost > 0 && in.CostUSD > 0 { … }
```

两个 `0.5` 是**中性回落**。设计意图正确（没测量就别假装知道），但它与
「测了、结果恰好中性」在输出上逐字相同。而 `sweep` 里唯一的相关分支是：

```go
baselines, err := w.loadTaskBaselines(sweepCtx)
if err != nil { … baselines = map[string]taskBaseline{} }
```

**空 map 不是 error**，所以这条分支永远不会为它触发。`baselines[p.taskType]`
取不到时拿到零值 `{0, 0}` ⇒ 整条 selection 的延迟项与成本项同时塌成 0.5。

§9.43 把基线 cohort 的来源表接到了 `src.TurnsTable` 上。真库实测
（`baselineWindow = 24h`，`is_auto_request = TRUE AND latency_ms IS NOT NULL`）：

| 族 | 24h cohort 行数 |
|---|---|
| v1（`request_logs_hot`） | **2178** |
| 会话族（`session_turns_hot`） | **0** |

⇒ 在当前这份数据上切换会**立刻**得到空 map。没有报错、计数器照常增长、
reward 仍在 [0,1] 内，而 cohort 基线存在的唯一理由（区分快慢 / 贵贱模型）
就此失效。

### §9.44.3 两族都健康时的覆盖率：80.4%，不是 99.3%

上一节的 0 是**本地流量塌陷**造成的（本地 09-27 起总流量掉了约 20 倍，
且 auto 流量在会话族里几乎归零），不能当作生产结论。取两族都健康的窗口重测：

| 口径（2026-09-19 … 09-26） | v1 | 会话族 | 覆盖 |
|---|---|---|---|
| `is_auto_request=TRUE AND latency_ms IS NOT NULL` 行数 | 820332 | 659252 | **80.4%** |
| 不同的 `task_type` 数 | 1 | 1 | 一致 |

⇒ §9.40 记的 83.0% 大致成立，但**那是对 selection request_id 的口径**；
基线查询读的是「窗口内全部 auto 行」，两个总体不同。**引用覆盖率时必须写清
量的是哪个面**（本审计第三次栽在这一类，见 §9.44.6）。

同窗口的 p95/p75：

| task_type | v1 p95 | 会话族 p95 | 偏移 |
|---|---|---|---|
| `probe_triggered` | 3301 | 3803 | **+15.2%** |

⚠ **这个 15.2% 不代表生产**：本地 auto 流量全部是 `probe_triggered`
（`task_type` 只有这一个取值），是合成探针流量而非真实用户流量。生产偏移未知，
只能确定它**不为零**。p75 两侧都是 NULL（探针没有 `cost_usd`）⇒ COALESCE 成 0，
这正是「延迟项与成本项独立塌陷」的实例。

### §9.44.4 「确保数据在更改前后一致」这句话能被证明的部分与不能的部分

**能证明的**：对**同一批数据**，两条源给出**逐位相同**的 reward。

为此先把两条 SQL 抽成纯函数（`bg/auto_route_settle_sql.go`）。抽出来的直接原因
是可测性：`settings.RequestLogsWriteEnabled()` 只有读取器（`GetPlatformBool` 覆盖
settings store），**没有能在测试里翻转的注入口**，所以只要源由全局门决定，
集成测试就只能跑 v1 分支——也就是今天线上已经在跑的那条。

`TestAutoRouteSettleSessionSourceMatchesV1OnIdenticalRows`（testcontainers，
`//go:build integration`）把同一批请求种进两族，只让承载会话身份的列改名
（`gw_session_id` → `session_id`，这正是 `settleSourceSpec` 的全部意义），然后
要求：基线三元组相等、每条 pending 逐字段相等、`computeSelectionReward` 输出相等。

变异验证（把会话键列改成 `tenant_id`）证明这道门承重，报出的差异是实质的：

```
req-b2: model_reqs differs: v1=2 session=0
req-b2: retry_count differs: v1=0 session=NULL
req-b2: REWARD differs: v1=0.7650191571 (session) session=0.7200191571 (request)
req-b2: reward_source differs: v1="session" session="request"
```

注意最后一行：`reward_source` 翻转会改变 affinity rollup 的归因，不只是数值偏移。

**不能证明的**：跨切换的 reward 相等。cohort 的**总体定义就是被退役的那张表**，
换源必然换总体 ⇒ p95/p75 必然变（本地实测 +15.2%，生产未知）。
要让跨切换可比，只能引入一个与被退役表无关的稳定 cohort（例如独立的长期统计表），
**本轮没有实现**。这是 §9.44 留下的未决项，需要单独排期，不该由一道门或一次
文档改写单方面「解决」。

### §9.44.5 门与告警

新增（`bg/auto_route_settle_baseline_metrics.go`，闭集标签 `family ∈ {v1,session}`、
`term ∈ {latency,cost}`，**刻意不带 task_type**——它来自请求内容，基数无上界）：

| 指标 | 含义 | 消费它的告警 |
|---|---|---|
| `llmgw_autoroute_settle_baseline_cohort_rows{family}` | 本轮 cohort 行数（gauge） | `AutoRouteSettleBaselineCohortEmpty` |
| `llmgw_autoroute_settle_baseline_neutral_total{term,family}` | 走了中性回落的 selection 数 | `AutoRouteSettleBaselineNeutralDominant` |
| `llmgw_autoroute_settle_source_total{family}` | §9.43 已有 | `AutoRouteSettleSourceSwitched`（**本轮新增的消费者**） |

`loadTaskBaselines` 的签名从 `(map, error)` 变成 `(map, int, error)`，第二个返回值
与 `count(*)` **同在一次查询里**取回（不为一个数字多付一次每 30 秒的 RTT）。
§9.37 的纪律直接适用：门
`TestSettleBaselineCohortCountIsConsumed` 要求那个返回值真的被 `Set` 进指标，
否则它在事实层面就是装饰。

**告警的 gauge 陈旧性陷阱**：`cohort_rows` 只在**本轮实际使用**的那个族上 `Set`。
源族切换后另一个族的序列会停更并冻结在旧值上——对一个「已停更」的序列断言
`== 0`，读到的是「没在测」，不是「测出来是 0」。所以每条 cohort 规则都同时要求
该族近期确有结算（`increase(source_total{family=...}[15m]) > 0`），
**把「正在被使用」写进条件本身**。变异验证：删掉这个守卫后门变红。

`AutoRouteSettleSourceSwitched` 用 `changes(...[10m]) > 0` 且**不带 `for:`**——
切换一生只发生一次，被抑制就等于没有（这是上一轮 handoff 遗留的第 ② 项）。

### §9.44.6 搬动 SQL 顺手制造了一道假绿（必须记下来）

把两条查询从 `auto_route_settle_worker.go` 搬进 `auto_route_settle_sql.go` 之后，
两道既有门**都只扫 worker 这一个文件**，于是行为分叉：

| 门 | 搬动后的行为 | 是否暴露了搬动 |
|---|---|---|
| `TestSettleLegsAllUseTheSameSource`（数 `src.TurnsTable` ≥ 3） | **红** | 是 |
| `TestAutoRouteSettleWorkerDoesNotUseThe710View`（扫 SQL 字面量找 710 视图） | **绿** | **否** |

第二道门从此对着一个不再含 SQL 的文件断言「没有 710 视图」——
**一道删掉它所守之物之后仍然通过的判据，就是装饰**。只有红的那一道救了场。

修法不是逐个改调用点，而是引入 `settleSQLFiles` 清单，所有扫 SQL 的门共用，
并加 `TestSettleSQLFilesStillCarrySQL` 挡住「清单与实际位置脱节」这种退化
（它要求 SQL 文件里真的还有 `LEFT JOIN` 与 `auto_route_selections_hot`，
而不是只检查文件存在）。**漏登记的后果是判据静默失效，不是报错。**

同一轮的第二个假阳性面：`TestSettleBaselineCohortCountIsConsumed` 的第一版先写了
一条 `strings.Contains(raw, "autoRouteSettleBaselineCohortRows")`。变异验证时它
判为通过——命中的是 `loadTaskBaselines` **文档注释**里的那句
「see autoRouteSettleBaselineCohortRows」，不是任何代码。已删掉该弱判据，只留
要求完整调用形状（标识符 + `WithLabelValues` + `Set(float64(cohortRows))`）的正则。
**子串门被注释喂饱，是本审计最常见的假阳性面。**

### §9.44.7 本节没有做的

- **没有**让基线 cohort 跨切换保持不变。做不到而不引入新表：cohort 的总体定义
  就是被退役的表。见 §9.44.4 的未决项。
- **没有**改会话族的写入侧，让它补上 `is_auto_request`。本地近 24h 会话族
  `is_auto_request=TRUE` 为 0 行，但**本地 auto 流量本身已塌**（09-27 起总量掉
  约 20 倍），分不清是「写方停写」还是「本地没有 auto 流量」。真要判定，需要在
  一台仍在跑 auto 路由的实例上对比两侧同日 auto 行数——本轮没有这样的实例，
  **不做推测**。
- **没有**把 §9.43 的三个读点再往前推。§9.44 只增加可观测性与一条可执行的一致性
  证明，没有改变任何计算路径。
- **没有**把这两道新集成测试接进 CI。它们是 `//go:build integration` 的
  testcontainers 测试，接进 CI 会在一次性空库上退化（§9.34：绿而无证据）。
  `go test -tags=integration ./bg/` 是它们的正确运行方式。

---

## §9.60 §9.31 遗留四条的执行轮：一条做完、一条证伪两条前提、两条卡在同一个根因上

> **节号说明（2026-10-02）**：本节原编号 §9.45，与并行会话的 §9.45 撞号，
> 经两次顺延定稿为 §9.60–§9.63（连续四个：§9.60 本节 / §9.61 migration 816 /
> §9.62 回填范围 / §9.63 不需要任何迁移）。§9.64 是后续新增的一节（816 守卫的
> 假守卫与 817），不属于这组四个。编号跳号是因为同一份文档正在被多个
> 并行会话追加，**靠行内交叉引用认领节号，而不是靠抢下一个整数**。

本轮执行 §9.31 遗留清单里的 1/2/3/4（`raw_model_name` 端口）。结论一句话：
**只有第 1 条是可执行代码工作，做完了；另外三条的根因不在它们各自指向的地方。**

### §9.60.1 `cmd/compression-bench` 的 `id`：做完，但撞上一个更硬的东西

§9.29.5 读面判据第 2 条要求「1 个真补位读方改读法」。这一条是全部 106 个读方里
**唯一一个经限定符归属判定后仍成立**的（§9.29.4），所以它必须先落地。

`id` 的处置：**删掉，不回查**。理由是它在本工具里只当行标识用
（`requestLogRow.ID` → `benchResult.RowID` → 结果表 `row_id`），**不参与任何压缩比
判定**，而 `request_id` 本来就已在结果里、且两族都有同名列。

改动：`cmd/compression-bench/main.go` —— SELECT 去掉 `id`、scan 去掉 `&r.ID`、
`benchResult` 去掉 `RowID`、结果表 DDL 与 CopyFrom 列清单去掉 `row_id`。
`row_id` 在 DDL 里一并去掉而不是留成 NULL 死列；已存在的表走 `CREATE TABLE IF NOT EXISTS`，
旧表保留该列且不再写入，不构成迁移。

### §9.60.2 顺手撞见的真 bug：这个工具**今天根本跑不起来**

改 `id` 之前我先在真库上跑了它那条查询，想确认 `id` 是不是唯一障碍。它不是：

```
ERROR:  column "request_body" does not exist
LINE 5:  COALESCE(outbound_body::text, request_body::text, '{}') AS body ...
```

真库 `request_logs` **没有 `request_body` 这一列**。`information_schema` 逐列核对
（`request_logs`）：`id` / `request_id` / `tenant_id` / `gw_session_id` /
`outbound_body` / `outbound_token_est` / `outbound_msg_count` /
`compression_strategy` / `ts` 全部存在，**只有 `request_body` 不存在**。

它搬到哪儿去了：`request_logs_bodies` 腿（`\d request_logs_bodies` 三列
`request_body` / `outbound_body` / `response_body`，主键 `(request_id, ts)`）。
这与 §9.28 的 bodies 腿是同一件事，但 §9.28 讲的是「10 个读点不能无损改指」，
**没有发现这个工具的查询是硬报错的**——因为它是一条没人跑过的离线 CLI 查询。

⇒ 修法：正文改从 `request_logs_bodies` 腿 join 上来，联键用主键
`(request_id, ts)`。真库验证（近 2 天、October 分区、限 500）：
**500/500 全部联上**，正文 87–176 字节。

顺带**去掉了 `request_body` 兜底而不是改指它**：那是**入站完整载荷**，
而本工具量的是**出站压缩比**（`BytesBefore` 直接取 `OutboundBody` 长度）。
拿入站正文顶替出站正文，会让每个比值都是两种东西的字节数相除——数字照样出得来，
结论是假的。宁可少一批样本。

**端到端证据**（真库实跑，非仅编译通过）：

```
DATABASE_URL=… go run ./cmd/compression-bench --days 2 --max-samples 5 \
  --test-mode mechanical --skip-llm-summary
--- Strategy Distribution ---  noop 5 (100.0%)
=== Summary === loaded rows → 正常输出聚合
```

（`noop` 100% 是数据形状而非缺陷：近 2 天的行都是 87–176 字节的探针流量，
远低于任何压缩触发阈值。）

### §9.60.3 但「改指视图」这条路**并没有因此打通**——挡路的是 bodies 腿

把 `id` 去掉只是解除了**一个**障碍。剩下的那个更大，而且它不是我能顺手解的：

| 事实 | 真库实测 |
|---|---|
| canonical 视图**没有** `outbound_body` 列 | `information_schema` 查 `request_logs_with_current_month` = false |
| v1 `request_logs_bodies.outbound_body` 与 `session_bodies.outbound_body` 在同 `request_id` 上**从不相等** | 配对 20,000 行：内容相同 **0**、**长度**都相同 **0** |

⇒ 正文这条腿在两族之间**不可移植**，不是「换个表名」能解决的。§9.28 已经量过
`request_body` 那一侧（session 只有 message 数组、无 `max_tokens`/`stream`），
本节补上 `outbound_body` 那一侧：**连长度都对不上**，所以 §9.28 的结论
「10 个 bodies 腿读点不能无损改指」对本工具同样成立。

⇒ **S4 读面判据第 2 条的措辞需要更正**：它写的是「1 个真补位读方改视图读法」，
但实测是「去掉 `id` 之后，这个读方仍被 bodies 腿挡住，**改不成**视图读法」。
补位阻塞已解除，**bodies 腿阻塞未解除**，两者是不同的工程量。

### §9.60.4 门：登记表清空，并加一道「必须保持空」的门

`admin/v1_direct_padded_column_reader_test.go`：

- 删掉唯一一条登记（`cmd/compression-bench/main.go` / `id`）。**不改成豁免**——
  本门的设计是「失效即报红」，改成豁免会让登记表退化成永不更新的占位。
  实测它确实先红了一次再被我改绿，红的理由正是「该位置不再触发本门」。
- 新增 `TestNoVPaddedColumnReaderRemains`：断言登记表**必须为空**。
  理由是 §9.32.3 那条纪律——本项目最贵的两个决定都是**不做**，
  而「不做」没有任何编译期信号。空表本身没人看得见，
  「它必须保持空」这件事需要有人守。断言的是计数为零而不是逐个点名文件：
  逐个点名会在有人新增第 2 个读方时给出误导性的通过。

绿：`v1 直读 + 读补位列的读方：0 个（其中已登记 0，未登记 0）`。

**变异 2/2，均为断言命中（非崩溃），且两处行号不同**：

| 变异 | 命中 |
|---|---|
| M1 把 `rl.id` 注回 compression-bench 的 SELECT | `v1_direct_padded_column_reader_test.go:157`「新增了…读补位列 id 的读方」 |
| M2 往登记表塞回一条 compression-bench 条目 | `:109`「登记表有 1 条登记，但应为空」+ `:217`「登记已失效」 |

（按 §9.38 的 M-C 纪律逐条串行：每次注入前先 grep 标记确认**恰好 1 处**，
还原后确认归零再注入下一处。）

### §9.60.5 `trace_events`：覆盖率量到了，而「镜像从不写它」这句话是**错的**

§9.31 遗留第 1 条与 815 的做法一样，先量覆盖率。真库（2026-10-02 17:5x 快照）：

| 窗口 | v1 `request_logs` 非空 | session（hot ∪ 父表）非空 |
|---|---|---|
| 近 1 天 | 36.11%（1,595 / 4,417） | **0** |
| 近 7 天 | 35.95%（207,579 / 577,421） | **0** |
| 近 30 天 | 33.30%（720,630 / 2,163,770） | **0** |

配对（7 天内 v1 有值的行限 50,000）：能在 session 侧按 `request_id` 找到
**45,197** 行，其中 session 侧有值的 **0** 行，`both_differ` **0**
——但这个 0 是**空集上的 0**，不构成「两侧一致」的证据。**投影它就是净数据损失**，
§9.22 / 815 据此不投影的结论**成立且被本轮重新确认**。

**但根因与文档里写的不一样。** 代码注释（`db/request_logs_view_schema.go:606`、
`internal/sessionv2mirror/s1a_fields.go:84`）说的是「镜像从不写它」。逐行读下来，
这句话**不准确**，真实链路是三段：

1. **会话写方一直在写这一列。** `domains/session/v2/turn_writer.go:369` 的 INSERT
   列清单里有 `trace_events`，`:433` 写 `nilIfEmptyJSON(rec.TraceEvents)`。
   ⇒ 不是「不写」，是**恒写 NULL**（`nilIfEmptyJSON` 把空值转成 SQL NULL）。
2. **镜像的源结构体没有这个字段。**
   `internal/sessionv2mirror/s1a_fields.go:24` 的签名是
   `applyStorageS1AFields(req *v2.ProcessedRequest, entry *telemetry.RequestLogEntry)`，
   `:84` 明写「req.TraceEvents：RequestLogEntry 无此字段，保持零值」。
   `telemetry.RequestLogEntry` 缺 `TraceEvents` / `SearchText` / `RequestChecksum` /
   `RawModelName` 四个字段（包注释 `:7` 有列）。
3. **v1 的值由另一条更晚的路径产生。** `internal/trace/trace.go:497`
   `UPDATE request_logs_hot SET trace_events = $1::jsonb WHERE request_id = $2`，
   数据来自 Redis，**在请求完成时**才 flush；而
   `domains/streaming/handler.go:2134` 的注释写明「FlushToPG 可能早于 telemetry
   worker 写入 request_logs」并为此加了退避重试——**两者存在已知竞态**。

⇒ 所以「让镜像写入」这个工作的**实际范围不在 `internal/sessionv2mirror` 侧**：
写方已就位，缺的是①给 `telemetry.RequestLogEntry` 加字段、②把 Redis 里的 trace
载荷接到该字段上。这是**热写路径 + 请求收尾时序**的改动。

成本我量了（供拍板用）：v1 近 7 天 `trace_events` 共 **392 MB**，
平均 1,981 字节 / 行，最大 5,583 字节。放大倍数不是问题——
近 7 天 `session_turns` 202,822 行 / 202,822 个 distinct `request_id`
（**1 turn ≈ 1 request**），194,369 个 session 平均 1.043 轮。
⇒ 逐轮复制在这份数据上**近似 1:1，不放大**。

**本节没有动写路径**，理由见 §9.60.7。

### §9.60.6 `client_ip`：§9.27.2 的裁决**建立在一个没有分辨力的测量上**

§9.31 遗留第 2 条要在「有人顺手把 740 的 client_ip 补上」之前定处置。
动手前我先复核那条裁决的证据（§9.27.2：「真库按 request_id 配对 202,014 行实测：
session 侧 `client_ip` 与 `client_forwarded_for` 相同 **202,014/202,014**、不同 0」）。

我按同样的方法重测，得到**同样的 100%**，然后去看了这些值是什么：

```
cff(client_forwarded_for) 全历史 distinct = 6 个取值：
  127.0.0.1 (334,610) / 172.17.0.1 (59,542) / 172.18.0.1 (9,468)
  172.29.0.1 (140) / ::1 (66) / 172.21.0.1 (35)
client_ip 的 distinct 取值集合与之**完全相同**（inet 形态，带 /32 或 /128）
多跳链路（cff 含逗号）行数：**0**
```

**全库没有一个真实客户端 IP，也没有一条多跳 XFF 链路。** 六个取值全是本机回环
与 Docker 网桥地址。

⇒ **「两列 100% 相同」在这份数据上零分辨力**：当 `client_ip` 存的是**正确的**对端
IP 时，它也必然等于 `client_forwarded_for`——因为在这台机器上，对端**就是**那个
代理/回环。§9.27.2 的测量为真，但**它不能区分**「client_ip 是错名副本」与
「client_ip 恰好等于链路首跳」这两种互斥解释。

而且代码给出了第三种、且更简单的解释：`middleware/origin_mw.go:499-501`

```go
// If a single value came from X-Real-IP but no XFF chain is
// available, persist the single value as the chain too so the
// (single, chain) tuple is never (a, "").
if chain == "" && single != "" { chain = single }
```

⇒ **在没有 XFF 头时，中间件主动把 chain 设成 single**，于是两列**必然**相等。
本机 0 条多跳链路 ⇒ 观测到的 100% 由这条兜底完全解释，
**不需要**「client_ip 是错名副本」这个假设。

⇒ **`verdictDifferentThing` 这条裁决目前没有支撑它的证据**，而它是承重的：
migration 740 让视图 `client_ip` 由 v1 侧 lateral 供真源 inet、session 臂保持
NULL 补位，正是按「两者不是同一个东西」设计的。
**在拿到有真实代理链路的数据（252 / 154）之前，「改名 / 补真源 / 删列」三个选项
都无法负责任地选**——按现有证据选任何一个，都是在给一个零分辨力的测量投票。

#### §9.60.6.1 252 生产库复测（用户拍板「去 252 复测后再定」）⇒ 裁决被推翻

经 `env-injector inject aliyun-edge-252` + SSH 只读查询 `pg-252-pg17`。**近 7 天**：

| 量 | 本机 | **252 生产** |
|---|---:|---:|
| `client_forwarded_for` distinct 取值 | **6** | **181** |
| 多跳链路（含逗号）行数 | **0** | **138** |
| session 侧有值行 | 172,305 | 17,586 |

**分链路形态拆开看**（这是决定性的一刀）：

| 形态 | 行数 | `client_ip == client_forwarded_for` | `== cff 首跳` |
|---|---:|---:|---:|
| 单跳 | 14,236 | **14,236（100%）** | 14,236 |
| **多跳** | **3,350** | **0** | **0** |

多跳样本：

```
client_ip = 172.64.217.81   cff = 2a06:98c0:3600::103, 172.64.217.81
client_ip = 104.23.251.28   cff = 2a06:98c0:3600::103, 104.23.251.28
```

⇒ `client_ip` 是链路的**末跳**（Cloudflare 侧 `X-Real-IP` 解析出的真实客户端），
不是首跳、**更不是转发头副本**。19.1% 的行两列不等，正是「两个列存着两个事实」
的正确表现。本机那 100% 是**回环数据**的假象。

**两族同义性直接配对**（同 `request_id`）：826 行，
`session_turns.client_ip == host(request_logs.client_ip)` **826/826、差异 0**。
**可投影性**：近 30 天 18,870 行全部匹配 IP 形态，`client_ip::inet` **全部转换成功**
⇒ 投影进 inet 型视图列无类型风险。

⇒ **改判 `verdictDifferentThing` → `verdictSameThingNoSource`**
（`db/request_logs_view_padded_columns.go`）。新裁决的含义是
「session 侧有语义相同的列、只是尚未投影」，正解是走 815 式投影补齐。

⇒ **底表的三个选项（改名 / 补真源 / 删列）全部不成立**——这一列存的就是真源对端 IP，
它是正确的。**要做的恰恰是相反方向：把它投影进视图 session 臂**（815 形状，本轮未做）。

> **方法学**：**一次测量能不能区分两个互斥解释，决定了它算不算证据。**
> 本机的 202,014/202,014 是**真的**，但它测的是
> `127.0.0.1` 对 `127.0.0.1`。凡是要用「逐行相同」下结论的，
> 必须同时报出**该列有几种不同取值**与**是否存在多行形态**；
> 只有一个取值的列，任何两列都会 100% 相同。
>
> （**订正 2026-10-02**：本段原先写着「与 §9.42.3 那条同源」，而并行会话的
> §9.42.3 讲的是 `reward_source` 三态指标，与本条无关——是一条**指错的悬空引用**。
> 与其猜一个可能的目标节号，不如让方法学自足。同类的悬空引用在编号撞号期间
> 很容易产生：**写「与 §X 同源」时，X 未必是当初想指的那一节。**）

> 顺带修掉两处会替旧结论背书的文案：
> ① `db/request_logs_view_padded_columns_test.go` 的非空转门原文写着
> 「id 与 client_ip 是本项目**仅有的两个**同名不同物」——后半句已作废，改写；
> ② `admin/request_logs_stop_write_classification_test.go` 里
> `internal/collector/gateway_adapters.go` 那条 Note 复述了「错名副本」，
> 但它的**降级结论不受影响**（锚在「未投影」上而不是「为什么没投影」），已就地订正。

### §9.60.7 `raw_model_name`：**「缺源字段」这个根因本身是错的**

§9.31 遗留第 4 条要求「先定义字段来源」。本节先照 §9.60.5 的写法去看根因，
结果发现**根因描述与事实不符**，而错误的方向是**把成本高估了一个量级**。

`s1a_fields.go` 的包注释（`:7`）写着：「数据源事实（2026-09-14 审计）：
RequestLogEntry 缺 TraceEvents / SearchText / RequestChecksum / RawModelName」，
`:81` 写着「req.RawModelName：RequestLogEntry 无此字段，保持零值」。

**逐字核对 `telemetry.RequestLogEntry`（`client.go:253`）后，这句话是假的**：

```go
ClientModel   *string `json:"client_model,omitempty"`     // :274
OutboundModel *string `json:"outbound_model,omitempty"`   // :275
```

两个字段**都在**，而且是上游/入站模型名这一对。`outbound_model` 是绑定解析后
**实际发往上游**的模型名，`client_model` 是入站请求里的名字。

⇒ 真实根因不是「源结构体没有这个字段」，而是**接线漏了**：值一直流过
`RequestLogEntry`，只是 `applyStorageS1AFields` 没把它落到 `req.RawModelName`。

⇒ **§9.30.2 的整段成本估算要打折**。它写「端口的真正前置是**新增一个有正确来源
的字段**」，并据此把这条从「补齐已有列」升级成一个需拍板的热写入路径变更。
实际上它是**纯搬运**：源字段已存在、写方已就位（`turn_writer.go:369/433` 早就在写
`raw_model_name` 这一列），只差一行映射。**「四张表全空」是真的，但它的成因
是漏接线，不是缺事实。**

这与 §9.60.5 的 `trace_events` **不同**——后者确实要动热写入路径与收尾时序
（v1 的值在请求收尾后才从 Redis flush）。本节把两者混为一谈，是 §9.60.5 结尾那句
「合并成一个工作项」的**反面**：它们共享「值恒空」这个**症状**，不共享根因与成本。

字段来源的定义（承接 §9.30.2，此处钉成一句可施工的话，且已施工）：

> `raw_model_name` = **绑定解析出的上游原始模型名** = `COALESCE(outbound_model, client_model)`。
> **不是** `session_turns.model`——后者等于 `client_model`（§9.12.1 实测 1138/1138），
> 在发生模型映射的绑定上会系统性误判。写路径上它是**已经算出来**的那个值
> （路由候选解析的结果），不是从落库的 model 列反推的——**这就是「近似实现比不修
> 更危险」的具体理由**：用 `model` 顶替会造出一个持续产出看似合理结论的假信号。

### §9.60.8 `raw_model_name` 接线：落地 + 门 + 变异

用户 2026-10-02 拍板「只做 raw_model_name，trace_events 延后」。改动一行：

```go
// internal/sessionv2mirror/s1a_fields.go
req.RawModelName = firstNonEmpty(strVal(entry.OutboundModel), strVal(entry.ClientModel))
```

**为什么必须 COALESCE 而不是只用 outbound_model**：252 近 7 天
`outbound_model` 有 **4,903/29,201 = 16.8%** 为 NULL。此时回落 client_model 才能让
这一列在两族之间**保持同义**；留空会把「同义」变成「一半缺值」，那才是真的不可比。

**为什么这不是「用近似值凑数」**：252 实测两列**确实不同**的行有
**3,227/29,201 = 11.0%**，且差异是真实映射而非噪声：

```
client_model  | outbound_model
minimax-m3    | MiniMax-M3
glm-5.3       | glm-5.3-flash
```

即大小写规范化 + 别名映射。取 `client_model` 去比
`provider_models.raw_model_name`，会在**这 11% 的行**上让
`credential_recovery` 的下架判定系统性误判——这正是 §9.30.2 警告的那个失败形态。
另外 252 上 v1 `request_logs.raw_model_name` 仍为 **0** 非空，与本机一致。

**门**：新增 `internal/sessionv2mirror/s1a_raw_model_name_test.go`，
**行为断言**而非源码扫描——构造 entry、跑 `applyStorageS1AFields`、断言落到的值。
6 个用例覆盖：两值不同取上游 / outbound 为 nil 回落 / outbound 为**空串**也回落 /
两个都空留空 / 只有 outbound / **否定式**「不许拿 canonical_model 顶替」。

> 选行为门而不是源码扫描门的理由：这道门要守的失败形态是**一次接线漏失**，
> 而上一版注释正是用一句关于**源结构体**的话把接线缺陷伪装成结构性事实。
> 源码扫描型门在「文件里有没有这个词」上假阳性率高；
> 这里断言的是「给一个两值不同的 entry，落到 `RawModelName` 上的是哪一个」——
> **这正是这段接线的全部语义**，且天然对「顺序反了」「退回零值」两种变异敏感。

**变异 2/2，均为断言命中（非崩溃），行号不同**：

| 变异 | 命中 |
|---|---|
| M1 把优先级反过来（先 ClientModel） | `s1a_raw_model_name_test.go:88` `RawModelName = "minimax-m3", want "MiniMax-M3"` |
| M2 退回接线前状态（恒零值） | `:88` × 4 条 + `:110` setup 卫语句 |

**未做**：本轮**没有**把 `raw_model_name` 投影进视图（它仍是 NULL 补位列），
也没有历史回填。投影的前置是「写方有值」——现在刚成立，需要一个观察窗口确认
新写入的行确实带值之后再动迁移。

### §9.60.9 本节没有做的

- **`trace_events` 的写路径**（用户拍板延后）。理由见 §9.60.5：真实范围是
  「给 `telemetry.RequestLogEntry` 加字段 + 接 Redis 载荷」，属**热写路径 + 请求
  收尾时序**变更；而镜像只处理**终态 entry**（`hook.go` 的 in-progress 过滤），
  请求处理中只能拿到**部分** trace，与 v1 的「最终 trace」语义不同，
  需要单独设计收尾后回写。
- **没有把 `client_ip` 投影进视图**。裁决已改判为「同义、待投影」（§9.60.6.1），
  但投影是 815 形状的迁移改动，本轮只做裁决更正版。
- ~~**没有把 `raw_model_name` 投影进视图**，也没有回填。见 §9.60.8 末。~~
  **已作废（§9.63）**：**它根本不需要投影**——视图 session 臂一直就是
  `t.raw_model_name AS raw_model_name`（815 proj 第 272 行）。它 0% 的原因只有一个：
  写方恒写 NULL。**写方已修 + 回填已跑 ⇒ 该列在真库上已有 38.423%（近 1 天），
  往返失配 0，且零迁移。**
- **没有把 compression-bench 改指 canonical 视图**。挡路的是 bodies 腿（§9.60.3），
  不是 `id`；在 bodies 腿有结论之前改指只会把一个「跑不起来」的工具换成
  「跑得起来但测的是另一种东西」的工具。
- **没有**给 `trace_events` 的 0 覆盖率补新门：815 的
  `TestMigration815ExcludesTraceEventsAndID` 已经在守「不投影」，
  而「什么时候可以投影」的前置条件是**写方有值**，那只能由真库门守，
  空库上是绿而无证据（§9.34）。

### §9.60.10 遗留（下一轮）

1. ~~**`client_ip` / `raw_model_name` 两个投影**（815 形状的新迁移）。~~
   **两件都已消解**：`client_ip` 由 816 落地（§9.61）；`raw_model_name`
   **压根不需要迁移**，它早已被投影，只是写方恒空——写方已修（§9.60.8）+
   回填已跑（§9.63.1）⇒ 真库已有 38.423%、往返失配 0。
2. **`trace_events` 写路径**（用户已拍板延后）。需先定「写最终 trace」还是
   「写部分 trace」。
3. **bodies 腿的不可移植性**（§9.60.3）：两族 `outbound_body` 配对 20,000 行
   内容相同 0、长度相同 0 ⇒ 10 个 bodies 腿读点的改指需要先决定正文来源。
4. **本机库不适合做「逐行相同」类裁决**。凡此类结论必须标注是否在 252/154 复测过。

---

## §9.61 816：把 `client_ip` 从 NULL 补位改成有源投影（§9.60.6.1 裁决的落地）

§9.60.6.1 把 `client_ip` 的裁决从 `verdictDifferentThing` 改判为
`verdictSameThingNoSource`。本节做那半句——**投影**。

### §9.61.1 740 当年的理由，两半都要改

`db/request_logs_view_schema.go` 上写着：「session_turns.client_ip 为 text
且**未回填**，不能直映」。

- 「**未回填**」**是错的**：本机近 7 天 session 侧非空 172,305/202,774 = **85.0%**；
  252 近 7 天有值 **17,586** 行。
- 「text 不能直映」只说明需要一次**显式转换**，不是不能映。

⇒ 投影口径（与本投影既有的 `application_id` / `api_key_id` / `credential_id`
转换同款）：`CASE WHEN t.client_ip ~ '^[0-9a-fA-F:.]+$' THEN t.client_ip::inet END`。

**守卫不是防御性编程，是承重的**：`session_turns.client_ip` 是 **text**、
无类型约束，一个畸形值会让 `::inet` 抛错并**打挂整条 canonical 视图的每一个读方**。
真库实测守卫行为（**816 的字符类形态**）：

| 输入 | 守卫输出 | 字符类 `[0-9a-fA-F:.]` 合法？ |
|---|---|---|
| `1.2.3.4` / `2a06:98c0:3600::103` / `::1` | 原值透传 | 是 |
| `garbage` | **NULL**（不是报错） | **否** |
| `1.2.3.4, 5.6.7.8`（多跳 XFF 链） | **NULL**（不是报错） | **否**（含逗号） |
| `''` | **NULL** | **否**（长度 0） |

⇒ 这也是本迁移里唯一一个「少写三个字符就可能打挂全站读面」的改动，所以门必须
单独盯住它（§9.61.4）。

> **订正（2026-10-02，§9.64）**：这张表**三个坏值全部是字符类不合法的**
> ——`garbage` 含 `g/r/b`、多跳链含逗号、`''` 长度不足。也就是说它只证明了
> 「守卫挡住了非字符集垃圾」，而我当时把它读成了「守卫已覆盖畸形值」。
>
> **字符类合法但语义非法**的那一类（`192.168.1` / `deadbeef` / `1.2.3.4.5.6` /
> `:::` / `...` / `999.1.1.1`）全部通过这个正则，然后死在 `::inet` 上——真库
> 实测整条视图查询抛 `ERROR: invalid input syntax for type inet`。由 817 修复，
> 守卫判据换成 `pg_input_is_valid(v,'inet')`。
>
> **教训**：一张「行为表」的分母是它**列过**的那几行，不是它**该覆盖**的那一类。
> 挑样本时若专挑自己能过的那类，表就会系统性地给出过宽的结论。

### §9.61.2 列数不变（118 → 118），所以**不需要 DROP**

`client_ip` 在第 115 位（740 追加位），**不是**尾部追加位。但这次改的是
**表达式**，列名 / 类型 / 序号全都没动 ⇒ `CREATE OR REPLACE VIEW` 合法
（它只禁止改已存在列的名字、类型、位置）。

这一点决定了 down 迁移的形态：**不需要 viewdef 正则手术**。815 的 down 必须做
「viewdef 捕获 → regexp 剥离 → 重建」，正是因为它要**删三列**而
`CREATE OR REPLACE` 删不了。816 只换表达式 ⇒ 用同一套 proj 骨架**确定性重建**即可，
没有 pattern 可写错，也没有「剥出列数相等但错列的 SQL」这一类风险。

**用正则手术去做一件能确定性重做的事，是把不确定性买回来。**

零级联也是选 `CREATE OR REPLACE` 的原因之一：815.down 头部记录的那条级联
（`DROP ... CASCADE` 会带走 `v_model_health_dashboard` 与
`v_probe_system_health`，二者只在启动期自愈、**在线回滚窗口内是不存在的**）
在 816 的上/下两侧都不存在。

### §9.61.3 真库往返（最强的一条证据）

本机 `llm_gateway` 实跑 816，然后对**视图**与**底表**逐值比对：

| 量 | 值 |
|---|---|
| 视图近 1 天行数 / 有 client_ip | 8,104 / **2,102（25.94%）** |
| 往返配对行（近 1 天 session 侧） | **1,735** |
| 视图有 client_ip | **1,294** |
| `host(视图.client_ip) == session_turns.client_ip` | **1,294（精确匹配）** |
| **失配** | **0** |

⇒ 视图侧这一列与底表逐值一致，且缺的 441 行正是 session 侧本就为 NULL 的那些。

幂等实测：同一事务内连跑两次，第二次命中
`816: canonical view already projects client_ip (guarded); nothing to do`。

> **顺带记一个我自己写错的测试**：第一次验幂等时我把两遍各包在自己的
> `BEGIN;…ROLLBACK;` 里，于是第一遍的**重建也被回滚**了，第二遍看到的仍是原视图
> ⇒ 报「没触发 no-op」。**改迁移前先确认你的测试没有把被测对象一起回滚掉**——
> 否则你在测一个不存在的缺陷。

### §9.61.4 门：3 道 + 变异 3/3

- `sql/migrations/startup/migration_816_test.go`（新）——守三件没人会主动想起的事：
  ① 有源投影不被悄悄改回 `NULL::inet`；② **守卫不被去掉**；③ down 真的回到补位
  且**不含 `DROP VIEW` 语句**。
- `db/view_schema_v2_contract_test.go` —— `registeredProjectionAppends` 第 2 项
  随投影同体更新；`TestViewV2ProjectionContractSync` **当场抓到了我漏改这一项**。
- `db/request_logs_view_padded_columns_test.go` —— 补位裁决表里 `client_ip`
  条目删除。顺带给那道门**补了第三个出口**：原先只说「挪进
  `registeredProjectionAppends` / 从契约里删掉」，但存在第三种——**列仍在契约里、
  仍在 `registeredProjectionAppends` 里，只是表达式被就地替换**（就是本次）。
  一个只会说「请选 A 或 B」的门会诱导人硬塞一个豁免。

**变异 3/3，均为断言命中（非崩溃）**：

| 变异 | 命中 |
|---|---|
| M1 把投影改回 `NULL::inet` | `migration_816_test.go:41 / :47 / :53` |
| M2 去掉 CASE 守卫（`CASE WHEN true`） | `:53` **单独命中**（与 M1 签名不同） |
| M3 down 迁移不回滚（保持有源投影） | `:77 / :81` |

### §9.61.4b 真库往返门（`TestRequestLogsViewV2EnsureMatchesMigration`）替我抓了 3 处

816 改了 Go 镜像体，而**生产启动走的是 Go 那条路**、不是迁移文件。
`db/view_schema_v2_contract_test.go` 那道真库门（在 scratch 库上重建整条包装链、
逐字节比对 ensure 与迁移链的 viewdef）第一次跑就红了，**三次**，每一处都是真问题：

1. **重放链里没有 816** ⇒ 「ensure 与 710+734+738+740+815 产出不同 viewdef」。
   这正是这道门存在的意义：Go 镜像体与迁移链必须同体，漏一个迁移会在这里现形，
   而不是等到某台机器启动时把视图重建歪。
2. **815 down 必须先 816 down**。816 的 proj 是完整 118 列契约（含 815 那三列），
   单独执行 815 down 会把 816 建的视图直接拆成 115 列**而不报错**——
   「能跑完但结果不是你以为的形态」这一类半吊子状态。
3. **我那条「815 down 确定性」断言在比两个不同起点的产物**。postDown815 采自
   816-down 之后，而第二次 down 接在 815+816 的 re-up 之后 ⇒ 它测的是
   「815 down 对 816 产出的 viewdef 与对 815 产出的 viewdef 结果是否相同」，
   并不是我以为的「同一个 down 跑两次」。修正后拆成两件：
   - `postDown815 == postDown815From816`：**816 的存在不扰动 815 的 down 结果**
     （否则回滚链的产物会取决于「816 有没有跑过」）；
   - 连做两次相同 down 序列，产物逐字节相同：**这才是确定性**。

⇒ 教训与 §9.61.5 同源：**「跑出来红」要先分诊是门错了还是代码错了**，
而这次三处红**全是代码/测试真的错了**，只有第 3 处是「测试在测一个它没打算测的量」。

迁移 3/3 之外，Go 投影的变异也做了：**去掉 CASE 守卫**（能编译的形态）同时打红
`TestViewV2ProjectionContractSync:97`（registered append #2 drifted）与
`TestRequestLogsViewV2EnsureMatchesMigration:423`（ensure 的 client_ip 投影丢了守卫），
两处行号不同。**第一版注入把行尾注释写进了复合字面量里，编译失败——那不是证据**，
改成前置注释后才拿到有效证据。

### §9.61.4c 部署顺序：816 **可以**先于二进制落地（本节先猜错了，实跑证伪）

我一看到「816 改了 Go 镜像体」就推断出一个部署顺序风险：**本机网关跑的是旧镜像
（`kx-llm-gateway-local:2.5.8.2392`），它重启时的 `db.ensure` 会把视图重建回
815 形态、把 816 冲掉**。听起来很像个必须写进 handoff 的坑。

**实跑证伪了它**。`ensureRequestLogsCurrentMonthView`（`db/request_logs_view_schema.go:45`）
**不是无条件重建**，它开头就早退：

```go
if canonicalExists && bodyIsV2 { return nil }
```

其中 `bodyIsV2` = viewdef 含 `session_turns`（且在 details 族在场时含
`session_turn_details`）。**816 的视图本来就满足它** ⇒ 旧二进制重启时 ensure
**直接 no-op**，816 原样存活。

真库复刻该判据（本机，已应用 816）：

| `canonical_exists` | `body_is_v2` | `has_816_projection` |
|---|---|---|
| t | t | t |

⇒ **816 与二进制之间没有强制的先后顺序**；这降低了部署风险（不必卡着同一个发布窗口）。
反过来说，**ensure 也不是应用 816 的机制**——应用 816 的是迁移本身，ensure 只在
视图缺失或形态不对时兜底。

> **这一节值得记的不是结论，是过程**：我先推出一个「听起来很对」的部署坑，
> 去读代码 + 跑真库才发现早退判据在那里。**部署顺序类推断的成本极低、
> 误报的代价却很高**（它会让运维在发布单上写一条并不存在的约束），
> 所以要么别推，要么推完立刻验。

### §9.61.5 这道门自己写错了一次——**子串门被约束注释喂饱**

M0（第一版）判 `down` 里有没有 `DROP VIEW`，用的是子串匹配。而 down 文件的
头部**恰好写着**「级联面：**无**。本迁移不含 `DROP VIEW」——

⇒ 门**第一次跑就红**，报一个**从未存在过**的缺陷。

这与本项目此前记过的两次完全同族（`native_responses_stream` 刻意不出现在某文件里、
S4 标识符出现在 SQL 注释里）。修法也不是退化成「不判了」，而是**先剥 `--`
行注释再判**——因为这类门守的正是「不要写这句话」，而约束说明天然会陈述它。

> **写守卫前先自问**：这条门如果被「正确地加了注释」触发，它会训练人忽略自己吗？
> 会，就先解决自噬。

### §9.61.6 本节没有做的

- **没有投影 `raw_model_name`**。它的写方是本轮才接线的（§9.60.8），需要先观察
  新写入行的非空率——**没有观察窗口就不能投影**，否则就是 815 明令拒绝的
  「投影它等于把数据换成 NULL」。
- **没有动 `trace_events`**（用户拍板延后，理由见 §9.60.5 / §9.60.9）。
- **没有动 `credits_rate_multiplier`**。它是 `verdictNoSessionSource`
  ——会话族**整条链上都没有「这一行按什么倍率计价」这个事实**，不是缺列，
  补不出有源投影。这与 `client_ip` 是两类问题：一个是**有源未投**，一个是**无源**。

---

## §9.45 S4 读面门的一个结构性盲区，以及一个「两个真相源让门测不出差别」的实例

§9.44 末尾记了一条由本轮改动引起的红灯：
`不可归属豁免 "bg/auto_route_settle_worker.go:id" 已失效`。本节把它查到底，
结论比那条红灯大得多。

### §9.45.1 红灯本身：删掉，而不是改写

`admin/v1_direct_padded_column_reader_test.go` 的 `unattributablePaddedRefExemptions`
里那条 `bg/auto_route_settle_worker.go:id`，形状是「一条同时含 v1 关系与派生表的
字面量里，裸 `id` 不可归属」。§9.44 把 settleBatch 的 SQL 搬进
`bg/auto_route_settle_sql.go` 的两个纯函数后，那条字面量被拆成若干片段，
**每一片都不含任何关系名**（关系名是拼进去的 `src.TurnsTable`）⇒ 该文件对这个门
不再产生任何命中 ⇒ 豁免按「失效即报红」的设计报了出来。

处置是**删掉**。两条理由缺一不可：

1. 豁免的语义是「这处不可归属的裸列，我手验过它绑的不是 v1」。形状确实不存在了，
   这句话对今天仍然成立。
2. 删掉之后 `bg/auto_route_settle_sql.go` 对这道门**完全隐形**——不是「判定为干净」，
   是「看不见」。这一点值得单独一节，因为它不是本轮才有的问题。

### §9.45.2 盲区的机制：关系名必须是字面量

`paddedColumnsReadFromV1Direct` 的口径是：从 **SQL 字面量文本**里用 `fromJoinRE`
抽 `FROM`/`JOIN` 后的关系名。`FROM request_logs_hot` 是完整字面量，抽得到；
`FROM ` + logsTable + ` WHERE …` 抽不到——字面量里根本没有关系名。

这不是本项目特有的写法，恰恰是**为了退役而刻意造的**：把表名收进集中式切换层，
改一处就能整体端口。实测有四个这样的层：

| 切换层 | 定义位置 | `days <= 7` 时返回 |
|---|---|---|
| `maas.requestLogsSource` | `maas/usage.go:66` | `request_logs_hot AS r` |
| `admin.requestLogsFromClause` | `admin/usage_credits.go:91` | `request_logs_hot AS r` |
| `admin.logsSourceFromSQL` | `admin/logs_turns_source.go:62` | canonical 视图或会话族（**从不** v1） |
| `admin.boardRequestLogsFromClause` | `admin/board_time_range.go:136` | canonical 视图 |

⇒ **门的设计与退役规划的方向是冲突的**：规划把读法集中化以便可整体切换，
而门只能看见没被集中化的那些。

### §9.45.3 实测：可复现工具与它的三个错误

新工具 `cmd/tools/sql_source_indirection_audit`（**不是门、不进 CI**，理由写在其
包注释里）。全仓 **60 处拼接点 / 36 个文件**，其中解析到 v1 宽族的有 **8 处 / 5 个文件**：

| 位置 | 解析结果 |
|---|---|
| `admin/usage_credits.go:120` | `request_logs_hot AS r` \| `request_logs_with_current_month AS r` |
| `maas/usage.go:106,116,134,170` | 同上 |
| `maas/credit_buckets.go:47` | 同上 |
| `maas/consumption_detail.go:79` | 同上 |
| `cmd/tools/backfill_session_bodies/main.go:96` | `request_logs_bodies` |

**工具的第一版踩了三个方向相反的错，三个都是「输出看起来很正常」型的**：

1. **把「不知道」报成「安全」**。`x := someFunc(...)` 这种绑定没解析，未知标识符被
   原样当作候选表名 ⇒ `logsTable` 变成「名为 logsTable 的非 v1 表」⇒ 归入
   「退役安全」。6 个真 v1 调用点**一个都没报出来**。
2. **变量按包级绑定**。`logsTable` 是函数局部变量，但 `admin` 包里三个互不相干的
   函数各绑一次、返回**三张不同的表**。「首个胜出」让三者都拿到第一个函数的值 ⇒
   输出一份格式正确、数值合理、**结论全错**的报告。
3. **用前缀判 v1**。视图名 `request_logs_with_current_month*` 同样以 `request_logs_`
   开头 ⇒ 6 个**只读视图**的拼接点被报成读 v1，方向完全相反的假阳性。

⇒ 修法：按**包**建环境、变量按**函数作用域**、只认精确表名集合、跨包调用与 struct
字段一律判「不可判定」（共 40 处），**不猜**。

### §9.45.4 这三条修完之后，又撞上「两个真相源让门测不出差别」

`isV1Relation` 一度同时有三层判据：精确表名集合 + 排除 canonical 视图的正则 +
`request_logs_` 前缀兜底。变异验证连做两次，**两次都没能让门变红**：

| 变异 | 门的反应 |
|---|---|
| A：加回前缀兜底 | **仍绿**——正则守卫已经挡住了视图名 |
| B：删掉视图正则 | **仍绿**——精确集合里本来就没有视图名 |

⇒ 行为被**三个真相源同时决定**，任何删掉其中一个的变异都测不出差别。这与本文件族
反复出现的教训是同一条：§9.26 的注释早就写着「门拿错误源与错误派生式互相校验，
所以它一直绿着」。我刚在工具里重建了它。⇒ 收敛到**唯一**真相源（4 元素精确表名
集合，与 `v1DirectTables` 同源），其余删掉。

第三道门（片段检测的 `\s+$` 锚点）也是先绿后红：夹具里只放了一条**孤立**的完整
字面量，而它根本不在 `+` 链里，`ast.Inspect` 不会访问它 ⇒ 锚点在不在都测不出差别。
补上「完整字面量**自己参与拼接**」（条件查询拼接，本项目到处都是）之后，变异才
生效。⇒ **一道门测不出变异，先怀疑夹具没覆盖区分点，而不是怀疑变异没生效。**

### §9.45.5 结论：盲区是真的，当前没有活的漏网

对那 8 处逐个手验是否读**补位列**（现网 6 列：`id` `test_col` `test_tab_indent`
`provider_model` `credits_rate_multiplier` `client_ip`）：

- `admin/usage_credits.go`、`maas/usage.go`、`maas/credit_buckets.go`：**零匹配**。
- `maas/consumption_detail.go`：`maas_settings.id` 与 `p.id` / `c.id` / `mc.id`
  —— 全部限定在别的关系上，且这三行是**从别名读**（`alias.provider_id`）而非读
  `alias.id`。
- `cmd/tools/backfill_session_bodies/main.go:96`：读 `request_body` / `response_body`
  / `request_id`，均非补位列（该文件唯一的 `id` 出现在 flag 描述文本里）。

⇒ **§9.26 的「14 → 1 → 0 收口」结论方向正确，但它是站在一个漏掉拼接式 SQL 的
测量面上得到的。** 补位读点当前确实为 0；不过这个 0 的**支撑面**比看上去窄。

### §9.45.6 建议（属于门的所有者，本节不代为实施）

1. **不要**把 8 处（或 60 处）登记进任何豁免表。§9.37 已记录：先写登记、后加护栏
   的顺序会系统性腐烂，而这 8 处的形状是**条件性**的（`days <= 7` 才读 v1），
   登记表完全无法表达这种条件。
2. 更合适的形状是在**切换层**上加门：四个切换层的返回值是有限集合，让一道门断言
   「这些集合里出现过的每个表名都在允许清单内」。切换层是端口的**单点**，
   守住它等于守住了所有下游读点——这比逐个读点登记更接近退役规划本身的结构。
3. 本节**没有**实施第 2 条：`admin/` 与 `maas/` 的这些文件当前有并行会话的未提交
   改动（`admin/usage_credits.go` 已被改过），代为加门会与他们的工作冲突。
   本节只交付工具、证据与口径。

### §9.45.7 本节没有做的

- **没有**给 admin 的读面门加拼接式 SQL 检测。理由见 §9.45.6.3。
- **没有**把工具接进 CI。它的结论依赖 Go 层表达式解析，冻结成登记表就是下一次
  腐烂的起点（§9.37）；正确用法是按需运行、把输出当证据读。
- **没有**动 `maas.requestLogsSource` 与 `admin.requestLogsFromClause` 的行为。
  它们在 `days <= 7` 时读 v1 是**当前的正确代码**（短窗口要避开分区扫描），
  退役时要改的是那时的取舍，不是现在。

---

## §9.62 `raw_model_name` 的 36h 回填：量出可执行范围，并作废我自己两个测量

§9.60.8 把写路径接上了，但投影仍卡在「要等观察窗口」。本节做一件**不必等部署**
就能做完的事：把回填范围量出来**——它是投影决策的前置，而前置的可行性不该等到
切前一刻才知道。

### §9.62.1 先确认一个我此前没验证过的事实：该列在视图**两侧**都是空的

| 位置 | 非空率（近 1 天） |
|---|---|
| 视图 `request_logs_with_current_month.raw_model_name` | **0 / 8,135 = 0.000%** |
| v1 `request_logs.raw_model_name` | 0 |
| session `session_turns.raw_model_name` | 0（本节之前） |

⇒ **不是「session 臂空、v1 臂有值」**——两侧都空。这条事实改变了投影的安全性论证：

> 投影**只可能把 NULL 变成值，不可能把值变成 NULL**。今天这一列 100% 是 NULL，
> 所以任何投影都是**单调无损**的。这与 815 那三列当时的情形**相反**
> （它们是「v1 有值、session 补 NULL」，投影前必须先证明 session 侧也有值）。
> ⇒ `raw_model_name` **不受 815 那条「先量非空率再投影」的门槛约束**。

### §9.62.2 回填范围：两面全联，**100% 可回填**

| 环境 | 窗口 | session 行 | 按 `request_id` 联上 v1 | `COALESCE(outbound_model, client_model)` 非空 |
|---|---|---:|---:|---:|
| 本机 | 36h | 4,266 | **4,266（100.00%）** | **4,266（100.00%）** |
| 本机 | 7d | 200,638 | 200,638（100.00%） | 200,637（100.00%） |
| **252 生产** | 36h | 6,985 | **6,985（100.00%）** | **6,985（100.00%）** |

⇒ 36h 窗口**零缺口**。且 v1 侧 `request_id` 在该窗口内 1:1（本机 8,006 行 /
8,006 distinct），回填取单值**没有歧义**。

⇒ 剩下唯一的前提仍是 §9.60.8 那条：新代码上线后**新写入**的行由镜像自己带值，
回填只负责「上线前那一段」。两者衔接处无缺口。

### §9.62.3 我在这一节里错了两次，第二次差点写进结论

**错法一：把 `(request_id, ts)` 当成跨族联键。**
第一次测 36h 回填得到 `v1_matched = 0` / `backfillable = 0`，直观的结论是
「v1 侧没有源，回填无从谈起」。**那个 0 是联键错造成的**：

```
request_id   fa211d71…
session ts   2026-10-02 10:08:56.993039+08
v1      ts   2026-10-02 10:08:57.198929+08     delta = +0.206s
```

⇒ 两族对同一请求的 `ts` 是**两次独立写入各自的时钟读数**，实测差 **~150–206ms**，
**且可以为负**（样本 `-1.46s`）。`(request_id, ts)` 在**族内**是主键
（`request_logs_bodies` 的主键就是它），**跨族不是**。去掉 ts 之后同一查询
立刻从 0 变成 2,878。

**错法二：只联了一个存储面。**
改成 `request_id` 后得到 67.46%，差的那 32.54% 里绝大部分是 **10:08 之后**的行
——我只联了 v1 的**父表**，而 `request_logs_hot` 实时在写（前沿 18:18:54，
父表停在 10:08:57，落后 8.2h 的 promote 节奏，§9.28.2 记过）。
两面都联上 ⇒ **100.00%**。

**两个错的方向相反**：一个把可回填说成 0%，一个把 100% 说成 67%。
**都是「只查了一个面」**——一次查错了联键，一次查漏了存储面。

> **这条与我此前记的「两族是两个存储面」是同一条纪律的第三次触发**：
> §9.28.2 是「只读父表 ⇒ 对最新轮次盲」，本节是「只联父表 ⇒ 把最新一段漏掉」。
> 凡跨族计数，**联键用 `request_id`（必要时加 tenant），存储面必须 hot ∪ parent**。

**顺带查了仓里有没有中招的**：所有 `(request_id, ts)` 联接都在 **v1 族内**
（`request_logs` ↔ `request_logs_bodies`、视图自身的 lateral），
**没有一处跨族**；而 `admin/data_lifecycle_blobs.go:109` 的 bodies 联接用的正是
`request_id` 单键。⇒ 这是**测量方法的错**，不是代码缺陷。

### §9.62.3b 回填 SQL 的真库演练（`BEGIN … ROLLBACK`，不留数据）

范围量出来之后，我把回填 SQL 在真库上演练了一遍——用事务回滚，所以**验了正确性
但没有留下任何数据**。结果抓到本节**第三次**「只碰了一个面」，而且这次最危险：

| 版本 | 写入面 | 填到行数 | 覆盖率 | 是否报错 |
|---|---|---:|---:|---|
| v1 | 只 `session_turns_hot` | 1,388 | **32.6%** | **否** |
| v2 | 只 `session_turns`（父表） | 2,872 | 67.4% | 否 |
| **v3** | **两面都写** | **4,260** | **100.00%** | 否 |

⇒ **单面版本会安静地跑完、只覆盖三分之一。** 这与 §9.28.2（只读父表 → 对最新
轮次失明）、§9.62.3 错法二（只联父表 → 67.46%）是**同一条纪律的第三次**，
但这一次它在**写路径**上——前两次在读路径，症状是漏数；这一次症状是**少写**，
且没有任何错误信号。

回填 SQL 的正确形状（两个存储面各一条 UPDATE，源也必须 hot ∪ parent）：

```sql
WITH src AS (
  SELECT request_id, COALESCE(outbound_model, client_model) AS v
    FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '36 hours'
  UNION ALL
  SELECT request_id, COALESCE(outbound_model, client_model) AS v
    FROM request_logs     WHERE ts >= NOW() - INTERVAL '36 hours'
)
UPDATE session_turns     t SET raw_model_name = src.v FROM src
 WHERE src.request_id = t.request_id AND t.raw_model_name IS NULL
   AND t.ts >= NOW() - INTERVAL '36 hours' AND src.v IS NOT NULL;
-- ↑ 父表（分区父表，落到各月分区）
UPDATE session_turns_hot t SET raw_model_name = src.v FROM src   -- ↑ hot
 WHERE src.request_id = t.request_id AND t.raw_model_name IS NULL
   AND t.ts >= NOW() - INTERVAL '36 hours' AND src.v IS NOT NULL;
```

`AND t.raw_model_name IS NULL` 让它**幂等**：新代码上线后自己写的行不会被覆盖，
回填重跑也不会改变已有值。

### §9.62.4 因此，投影的前置条件已经满足（除「新代码已上线」一条）

`raw_model_name` 与 816 的 `client_ip` 相比，多一个门槛少一个前提：

| | `client_ip`（816 已做） | `raw_model_name`（待做） |
|---|---|---|
| 语义是否同义 | 已证（252 配对 826/826） | 同源（同一批 `COALESCE` 口径） |
| 写方是否有值 | 早已有（85.0%） | **待新代码上线** |
| 投影是否单调无损 | 是（当时 0% 有值） | **是**（本节 §9.62.1 实测） |
| 历史覆盖 | 靠镜像自然积累 | 需 36h 回填（**范围已量：100%**） |

⇒ **本节这段推理在 §9.63 被整段推翻**：我当时以为「投影是一个待执行的动作」，
因而把观察窗口当成它的前置。实际上 `raw_model_name` **早就在投影里**
（`t.raw_model_name AS raw_model_name`），它 0% 只是因为写方恒空。
所以**不需要任何迁移，也没有观察窗口这个前置**——回填跑完该列就通了
（38.423%、失配 0）。
留在这一节原文里，是因为「把已投影的列当成待投影」是一个很容易犯的分类错误。

---

## §9.48 灰度清单的那个数是过期的：19 → **70**，且其中 68 在生产代码

§9.36.3 留下一句话：

> **⇒ 灰度前必须先处理的 19 条**（`silently_empty` + `silently_degraded_content` +
> `silently_frozen`）……这张表现在是**可执行的清单**。

本节从登记表直接算，**不是从那句话推的**。

### §9.48.1 全量分布（`requestLogsStopWriteClassification`，106 个读点文件）

| 档位 | 条数 |
|---|---|
| `silently_empty` | 27 |
| `silently_degraded_content` | 22 |
| `silently_frozen` | 21 |
| **静默小计** | **70** |
| `unaffected_by_stop_write` | 24 |
| `errors_out` | 10 |
| `validator_dual_read` | 2 |
| `unclassified` | **0** |

⇒ **19 → 70，差 3.7 倍。** §9.36 那个 19 来自「31 条新增评估」的样本
（上表当时的 11 + 4 + 4），而**文档没有标明它是外推值**。§9.36.3 自己把那张表
称作「可执行的清单」，于是 19 成了排期会直接引用的唯一量化口径。

`unclassified` 为 **0** ⇒ 评估是完整的，70 就是全部工作量，不存在「还有一截没算」。

### §9.48.2 「大部分只是工具」不成立

按路径把静默档切开（判据只用路径这一个客观信号）：

| 面 | 读点总数 | 静默 | 占比 |
|---|---|---|---|
| 生产代码 | 102 | **68** | 67% |
| `cmd/tools/`（离线工具） | 2 | 1 | — |
| `tests/` | 2 | 1 | — |

⇒ 68/70 在生产代码里。**灰度不会因为「受影响的大多是离线工具」而变得廉价。**

### §9.48.3 补三道门，让这个数不能再悄悄过期

`admin/audit_silent_count_consistency_test.go`（新增）：

| 门 | 钉住什么 | 变异 |
|---|---|---|
| `TestAuditDocSilentClaimMatchesRegistry` | 文档那句里的数 == 登记表实时计算值 | 文档 70→69 ⇒ 红（报「差 +1」） |
| `TestAuditDocSilentClaimIsMarkedAsMachineChecked` | 那句话附近必须同时点名三个静默档 | 删掉 `silently_degraded_content` ⇒ 红 |
| `TestStopWriteEffectValuesAreFromTheDeclaredSet` | 档位值必须来自已声明集合；`unclassified` 必须为 0 | 引入未声明档位 ⇒ 红 |

方向上刻意**不**做两件事：

1. **不逐个读点登记。** 那会退化成 §9.37 记录过的那种表（代码演进后条目静默过期）。
   这里只钉**一个**可从代码算出来的数：它是登记表的全量派生量，没有解释空间。
2. **不用裸数字匹配。** 文档里有几十个数字（§9.26 的 14、§9.36 的 31、§9.36.3 的 19、
   批次号……），按数字匹配会钉到别的句子上。门要求一句**固定格式**的话，
   并从那句话取数；格式本身就是契约。

第三道门补的是一个**本节自己踩到的洞**：我在 `countSilentStopWriteEffects` 的注释里
写「逐个列举三个静默档，不要用取补集的写法」，但**这个选择在今天的登记表上
没有任何判据能区分**——补集恰好也等于 70（106 − 10 − 24 − 2）。所以光靠计数门
无法兑现那句话的承诺，必须另加一道门让「新增第五种失效形态」本身会红。
**一个注释里声称的保护，如果没有门兑现它，那条注释是装饰。**

### §9.48.4 三种「按我自己选的规则分类」都被放弃了

本节中途试过两条聚类路径，**两条都因为「结论由我的选择决定」而弃用**：

1. **按 Note 里的机制关键词聚类**（`bodies` / `无时间下界` / `710` / …）。聚类结果
   完全由我自己挑的词表决定，不是结构事实。写完探针后没跑它。
2. **按我自己定义的路径规则切「离线 vs 生产」**。这条最终采用了，但规则只用了
   `cmd/tools/` 与 `_test.go` 两个**客观前缀**，并把规则本身写进表格——读者可以
   不同意某个归类，而不是只能接受一个来路不明的簇。

⇒ 报一个「分成 N 类」之前，先问：**分类轴是谁定的。** 词表是我定的，那结论就是我的
偏好，不是数据的事实。

### §9.48.5 `silently_frozen` 的 21 条：最该先处理，但本节不做

`silently_frozen`（21 条，生产代码 21 条）与其他两档的性质不同：
`silently_empty` 会让接口返回空、`silently_degraded_content` 会让某一列变空，
**而 frozen 永远不红**——查询照常成功、照常有行、照常有形状，只是内容永久停在
停写前一刻，**没有任何错误信号**。

结构上它们是同一类：**没有时间下界的读点**。理论上可以用一个共享原语一次性覆盖
21 条——§9.35 已经为 settle worker 造过一个实例（`outcome_source` 的
`stale`/`absent` 标记），把 MAX(ts) 与 `settleAbandonAfter` 比较。

**本节明确不做这件事**，理由不是工作量：

- 只造原语、不接那 21 个读点，它就是**一个新的装饰面**——§9.37 记录的核心失效
  模式（「没有第二个消费方的字段在事实层面是装饰」）。造一个没人用的 `v1DataHorizon`
  比不造更坏。
- 要真正有价值，必须逐读点接线，而那 21 个文件里有相当一部分正在被并行会话改动。
  本节不代为接线。

⇒ 正确顺序是：**先有人确认这 21 条的处理口径（接线 / 加降级提示 / 接受冻结并在
UI 上标注），再造原语。** 顺序反过来会产出一个漂亮的、没人用的 helper。

### §9.48.6 本节没有做的

- **没有**逐条分析这 70 条的成因。§9.48.4 说明了为什么按关键词聚类不可用；
  要做成因分析需要逐条读 SQL 与消费方，那是另一轮的工作量，也需要决定
  「成因」的粒度。
- **没有**改任何登记项的档位。70 是从登记表**算出来**的，改档位是别人的判断。
- **没有**处理 §9.44 的基线 cohort 未决项（需要拍板 (a) 或 (b)）。

---

## §9.49 三道门同时变红：我的 §9.43 改法撞碎了三个扫描器 —— 以及我用了三轮才查清是自己干的

本节是一次**自我纠错**，且纠错的对象是我自己的判定方法。先说结论，再说方法。

### §9.49.1 事实：`admin` 从 5 个红灯降到 1 个，而那 5 个里有 4 个是我引入的

前三轮（§9.44 / §9.45 / §9.48）我三次写「admin 的 5 个红灯**不是**本轮引入，
已用 worktree 对照核实」。**这个结论是错的。**

正确的归属（逐点 worktree 复核）：

| 提交 | 这 5 个测试的状态 |
|---|---|
| `e154135fa`（**§9.43 的父提交**） | **全部通过** |
| `075760768`（我的 §9.43） | **4 个失败**：`ControlPlaneKnownEntriesAreReal` / `ReadInventoryIsComplete` / `StopWriteClassificationEvidenceIsReal` / `StopWriteSourceFamilyCoversInventory` |
| `dc01a69c5`（我的 §9.44） | 同上 4 个 |
| `b28ad0c98` 之后 | 第 5 个 `TestSessionArmNullPaddedColumnsMatchMigration` 出现（migration 816，**并行会话**） |

⇒ **4 个是我的**，由 §9.43 引入。只有第 5 个是他们��。

### §9.49.2 我的判定方法错在哪：**用自己提交的后代当基线**

§9.44 那轮我做过一次 worktree 对照，基线取的是当时的 `HEAD` = `624a50c3b`。
它确实通过了那 4 个测试，我据此写下「不是本轮引入」。

**但 `624a50c3b` 是我 §9.43 提交 `075760768` 的后代**（`git merge-base
--is-ancestor 075760768 624a50c3b` = YES）。所以那次对照的基线里**已经包含
我的改动**——它测的是「含我的改动的仓库 vs 含我的改动的仓库」，
当然一致。

同一份 worktree 上 `grep -c "LEFT JOIN request_logs_hot rl" bg/auto_route_settle_worker.go`
= **0**：那个字面量在 §9.43 就已经不存在了。这条证据当时就在手边，我没看。

⇒ **对照基线必须早于被怀疑的那次改动。** 「当前 HEAD 不是我 ⇒ 不是我」这个推理
只在 HEAD 落在我改动**之后**时成立；HEAD 一旦被并行会话推进，它就变成了我的
后代，推理方向整个反过来。

更一般的形态：**用 `HEAD` 当基线只在「HEAD 未被我改动污染」时有效**，而这正是
被怀疑的那件事本身。⇒ 基线要显式指定一个**已知的、在我改动之前**的提交。

### §9.49.3 根因：三个独立的器眼，同一个前提

§9.43 把 `LEFT JOIN request_logs_hot rl` 改成 `LEFT JOIN " + src.TurnsTable + " rl`。
三个**各自独立实现**的器眼同时失明：

| 器眼 | 实现 | 前提 |
|---|---|---|
| `requestLogsReadInventory` | 按行正则 `from\s+request_logs(_[a-z_]+)?` | 关系名紧跟 `from` |
| `paddedColumnsReadFromV1Direct` | 从 SQL 字面量抽 `FROM/JOIN` 后的标识符 | 关系名是字面量 |
| `sourceFamilyOf` | 剥注释后匹配族正则 | 同上 |

三个器眼、三种实现，**同一个未被写下来的前提**。这不是三个 bug，是**一个契约
从未被声明**。§9.45 在补位列那道门上撞见过同一件事，当时把它记成「结构性盲区」，
但没有回头检查仓里还有几个器眼共享这个前提——**这就是它只被记了一轮的原因**。

而规划的方向恰恰是**增加间接性**（把表名收进切换层以便整体端口）⇒ 器眼与规划
的方向相反。

### §9.49.4 为什么不用「加条注释让它看见」

`requestLogsReadPattern` 扫的是**原始行文本**，所以在 `auto_route_settle_sql.go`
里加一行注释 `// reads FROM request_logs_hot / session_turns_hot`，
清单会立刻变绿、计数也会对。

**那是伪造测量**：门绿了，测的不是代码里真实存在的东西。§9.45 记过同族事故
（子串门被约束注释喂饱）。⇒ 唯一诚实的做法是让器眼**知道**有间接读点。

### §9.49.5 落地：一张「间接读 v1 读点」登记表 + 三道门

`admin/request_logs_indirect_readers_test.go`（新增）：

```go
var indirectRequestLogsReaders = map[string]indirectReader{…}
```

每条必须写清三件事，缺一即红：

| 字段 | 为什么必填 |
|---|---|
| `Family` | 器眼看不见它，族只能由人判定；不写就会「未归入任何一族」 |
| `ResolvesTo` | 「它读的是 v1」若是断言就不可核；写上表名后可被 `v1DirectTables` 核对 |
| `Reason` | 间接机制说明；空理由的登记等于没有登记 |

失效自检 `TestIndirectRequestLogsReadersAreStillIndirect` 两条：登记的文件**必须
仍然扫不到直接字面量**（否则间接性消失、应移回直接表），以及不得同时出现在
两张表里（否则被算两次）。

三个消费方改为遍历「直接表 ∪ 间接表」（`allKnownRequestLogsReaderFiles()`）：
读点清单的重复计数检查、族分类器、评估进度。

顺带修了一个被低估的偏差：**评估进度此前报「105/105 已评估」**——那个 105
根本没算上间接读点。修后是 **106/106**。一个「全部完成」的数字里藏着一个漏项。

### §9.49.6 顺带订正的两张表

1. **停写分级表**：`bg/auto_route_settle_worker.go` → `bg/auto_route_settle_sql.go`，
   Evidence 改成逐字存在于新文件的 `LEFT JOIN ` + src.TurnsTable + ` rl`，
   Effect 从 `silently_empty` 改判 **`silently_degraded_content`**
   （outcome 腿仍命中，不再 empty；剩下的是基线 cohort 换总体 / 可能塌成空 map /
   0.7% 失去 outcome，见 §9.44）。
2. **控制面表**：给 `controlPlaneVerdict` 加 `EvidenceIn` 字段。语义是
   「**消费方在 A 文件、证明读 v1 的 SQL 在 B 文件**」——§9.43/§9.44 把 SQL 抽成
   纯函数后这是常态。两种错误做法都不可接受（登记里写 A 文件没有的文本 =
   登记说谎；加注释让扫描器看见 = 伪造测量），指路是唯一诚实的选项。
   成员资格判据同步放宽为「文件本身**或其 `EvidenceIn` 指向的文件**是已知读点」。

### §9.49.7 这张新表**不能**发现新的间接读点（必须说清楚）

机器无法判定「一个不含 v1 字面量的文件是不是通过某种 Go 表达式在读 v1」。
本表覆盖的是**已知**的那一条，加上一道失效自检。新的间接读点仍要靠
`cmd/tools/sql_source_indirection_audit` 人工发现后登记（它把这类点报成
`unresolved`，40 处）。

⇒ 所以这张表**不给 CI 用**，也**不是**一道能自证的门。它是一份带自检的已知量清单。

### §9.49.8 一个我没有处理的语义问题（如实记账）

把 settle worker 判成 `silently_degraded_content`，是让那一档**装了它原本没有的
东西**：该档既有语义是「行还在，但某一**列**内容静默变空」（bodies 正文腿、
provider 归属），而这里是「行与 reward 都在，但 reward 的**分项**退化」。

该文件自己的约定是「**应扩档而不是把它塞回去**」。本轮**没有**扩档——扩档会改动
106 条登记的口径与灰度清单的算法，需要单独裁决。⇒ 记账，不偷偷解决。

### §9.49.9 变异验证 3/3

| 变异 | 结果 |
|---|---|
| N1 清空 `Reason` | 红（`:78` 空理由） |
| N2 把同一文件放回直接清单 | 红（`:126` 双重计数） |
| N3 把登记指向 `admin/analytics.go`（可被直接扫到） | 红（`:118` 间接性已消失） |

N1 第一次注入是**编译红**（结构体字面量混写），不算证据；改用整段替换重做。

---

## §9.50 `IntegrityFingerprintDrift`：一个安全检测器会在 S4 停写后**永久关闭**

### §9.50.1 要观测的失效形态

`bg/integrity_fingerprint_drift.go` 的 `tick()` 每轮先做一次短路判定：

```go
_, inProcSeen := telemetry.SystemFingerprintObservedSince()
switch fingerprintScanDecision(inProcSeen, w.probeDone, w.probeEmpty) {
case fingerprintScanSkip:
	w.skippedTicks.Add(1)
	return                       // ← 全量扫描在这里就结束了
case fingerprintScanProbe:
	hasFP, err := w.probeFingerprintTraffic(probeCtx)   // 读 v1
	...
}
```

短路本身是 2026-09-25 审计 round 8（D11）的正确设计：252 上所有指纹列恒空，
全量扫描是纯 no-op（~490k EXPLAIN、128k 行 seq scan），被 30s rolconfig
超时打死 3 次/55min。**问题不在设计，在于它读的那张表正在被退役。**

停写后 v1 不再产生新行 ⇒ 探针恒空 ⇒ `probeDone && probeEmpty` ⇒ 每轮第一行
return ⇒ 7 天窗口的指纹漂移扫描**再也不执行**。

这与 §9.43–§9.49 记的那一整族「读点变空」是**不同性质**的事：那些是显示层少一块
数据，这里是**一个凭据/模型指纹安全检测器自己判定「没流量可扫」而关掉自己**。
凭据被悄悄换掉（上游模型指纹漂移）不会被任何现有信号发现。

### §9.50.2 停写前它**完全不可见**——这是本次改动的根因

| 痕迹 | 建告警前的事实 |
|---|---|
| `skippedTicks` | 全仓 3 处引用**全是 `Add(1)`，没有任何地方读它** |
| Prometheus 指标 | 该文件此前**一个都没有** |
| 日志 | 只有「那一刻」一条 `slog.Info`（`:189`），之后每轮静默 |
| `Stats()` | 只导出 `scannedCycles`（`:131`），`skippedTicks` 连 Stats 都没进 |

⇒ 停写那一刻运维会看到一条 Info，**然后再无任何信号**，直到有人从别处发现漂移
检测一直没跑。§9.38 已在 `ledger_reconciliation` / `credential_recovery` 上修过
同一种形状（`SkippedChecks()` 无出口）；这个文件当时没做。

### §9.50.3 改动：把「自己关掉了」变成可告警的事实

新增 `bg/integrity_fingerprint_drift_metrics.go`，三个指标**刻意不带标签**（GW-00）：

| 指标 | 语义 |
|---|---|
| `llm_gateway_bg_fingerprint_drift_scanned_total` | 真正执行了全量扫描的轮数 |
| `llm_gateway_bg_fingerprint_drift_skipped_total` | 被短路、没扫描的轮数 |
| `llm_gateway_bg_fingerprint_drift_last_scan_unix` | 最近一次真正扫描的时刻 |

`last_scan_unix` 在 `Start()` 里就置为**进程启动时刻**，不是 0。这不是随手写的：
探针**每进程最多跑一次**，「启动后一次都没扫过」是停写后刚重启那一刻的真实形态。
置 0 ⇒ `time() - 0` 恒为巨大值 ⇒ **每个刚起来的进程都立刻告警**；不初始化 ⇒
序列不存在 ⇒ 告警永不响。

`deploy/prometheus/rules/integrity-fingerprint-drift.yml` 两条告警：

- `BgFingerprintDriftNeverScanned`（critical）：`time() - last_scan_unix > 7200`，
  `for: 10m`。**持续状态**，用 `for:` 抑制重启抖动。
- `BgFingerprintDriftSkippedNoScan`（warning）：`increase(skipped[15m]) > 0 and
  increase(scanned[15m]) == 0`，`for: 10m`。**退化中状态**。

两条都需要：只看 last_scan，扫描间隔逼近阈值时迟迟不响；只看 skipped，
「worker 压根没启动」会漏掉（那时 skipped 也不涨）。

### §9.50.4 ★订正：第一版告警文案里的一个**错误断言**

第一版 yml 写的是：

> **逃生口**（为什么它有时会自己恢复）：`telemetry.SystemFingerprintObservedSince()`
> —— 进程内遥测观测到指纹流量时会把全量扫描重新 arm。这条链**不读 v1**，
> 所以恢复与否取决于当前进程有没有真的处理过带指纹的请求。

**这句是错的**，而且是本轮最贵的一个错：它把「停写 + 重启 ⇒ 永久关闭」写成了
「有时会自愈」。据那条错误前提还发了规则。逐行核实后的真实链路是：

```
markSystemFingerprintObserved()                 ← systemFingerprintLastObserved 的唯一写入方
  ↑ 唯一调用点
persistSystemFingerprint()                       client.go:2540，执行 UPDATE request_logs_hot
  ↑ 两个调用点 client.go:1816 / :2468
insertRequestLog / updateRequestLog 的 `if logsWrite { … }` 块内
  ↑ logsWrite := requestLogsWriteEnabled()  →  settings.RequestLogsWriteEnabled()
  ↑ = KeyRequestLogsWriteEnabled = "storage.request_logs_write_enabled"   ← S4 停写键
```

我最初以为「逃生口在写事务里，但 UPDATE 匹配 0 行也返回 nil，仍会 arm」。**这也错**：
`client.go:1839` 的 `}` 关闭 `insertRequestLog` 的 `if logsWrite` 块，而
`persistSystemFingerprint` 在 `:1816`——**在块内**。`updateRequestLog` 同理
（`:2126` 开、`:2471` 关、调用在 `:2468`）。

⇒ **两条腿断在同一个开关上**：探针读 v1 恒空，进程内 arm 也恒不触发。
**停写 + 任意一次重启 = 检测器永久关闭**，没有任何其他信号。
（不停写时它是健壮的：只对本实例没见过、且一次性探针也没见过的流量才跳。）

一个会让运维「先等等看」的告警，比没有这个告警更坏——它把人引向一个不会发生的
结果。已改写为「**逃生口也是关着的**（不要指望它自愈）」，并在文案里留了一条
⚠ 说明本条曾经被写错、订正于 §9.50.4。

**同一处错误断言还有第二个载体**：`tick()` 里那条 `slog.Info` 原本写着
`"… skipping full scan (re-arms when telemetry observes a fingerprint)"`。已改为
如实描述，并附 `rearm=` 字段指向 §9.50.4。

⇒ 教训：**一个未兑现的承诺会在多个载体里复制**（告警文案、日志、注释、文档）。
订正时必须把载体找全，否则下一个读到旧日志的人还会照着那个承诺行动。

正确修法（**本轮未实施**，需要先实测）：探针改读会话族，并把 `inProcSeen` 的 arm
移到门控之外。

### §9.50.5 门（7 道）与变异验证 8/8

| 门 | 位置 | 断言的是哪一侧 |
|---|---|---|
| 指标↔告警一一对应 | `deploy/prometheus/rules/` | 每个注册的指标都有告警消费（反方向不判，见 §9.37） |
| 告警必须说出 stop-write 成因 | 同上 | 文案点名 `fingerprintScanSkip` 与「自己把自己关掉」 |
| **告警不得承诺自愈** | 同上 | 含「逃生口也是关着的」/「不要指望它自愈」，且不含 §9.50.4 撤回的那两句 |
| 计数器与指标同生共死 | `bg/` | 同一基本块内 `<counter>.Add` 与记录器调用**一一相邻**（不预设分支数量） |
| `Start()` 必须播种 last_scan | `bg/` | 恰好一处调用，且在 `Start()` 内 |
| 指标定义文件存在 | `bg/` | 三个指标名都还在（**不是**「有人在读」） |
| **逃生口在停写门内** | `telemetry/` | `markSystemFingerprintObserved` 唯一调用点在 `persistSystemFingerprint` 内；后者两个调用点都在 `if logsWrite` 内 |
| `logsWrite` 就是 S4 键（三跳） | `telemetry/` | `logsWrite := requestLogsWriteEnabled()` ×2 → wrapper 是**单语句纯转发** → 键值 = `storage.request_logs_write_enabled` |

**门自己抓到的三个真 bug**（都是先红后修，不是事后补记）：

1. `hasCall` 只认一级 selector，而 `w.skippedTicks.Add` 是**两级** ⇒ 在三个挂载点
   全齐的真实代码上报「计数器消失」。
2. `hasCall` 会**下探嵌套块**，于是外层 `switch` 节点「认领」了 case 体里的调用，
   位置被归到函数体 ⇒ 报了一个与真实代码无关的红。
3. 位置用「行号」无法区分**相邻两行**的 `Add` / 记录器 ⇒ 改用「所属基本块 + 块内下标」。

**门的三次失败注入记录**：

| 变异 | 注入 | 结果 |
|---|---|---|
| M1 删掉一处 `recordFingerprintDriftSkip()` | 3 处 → 2 处 | 红，`case@…:174#0` 无后继记录器 |
| M2 新增只有计数器、无记录器的跳过分支 | `Add` 2 → 3 | 红，`blk@…:178#0` |
| M3 把记录器挪到计数器**之前**（破坏相邻性） | 顺序对调 | 红，下标 `#0` → **`#1`**（与 M1 可区分） |
| M3b 删掉 `Start()` 里的 `recordFingerprintDriftStart` | 2 → 1 | 红 |
| M4 把 `persistSystemFingerprint` 挪出 `if logsWrite` | 门控外 | 红，`client.go:1863 is NOT inside` |
| M5 wrapper 改成 `if <key> {return true}; return true` | 值被偷换 | **第一次没抓住**，见下 |
| M6 加第二条 arm 路径 | 递归调用 | 红，「exactly one call site」 |
| M7 把 §9.50.4 撤回的那句放回 yml | 文案回退 | 红 |
| M8 把「一直不扫」告警改挂到 counter 上 | expr 替换 | 红，`last_scan_unix` 变成「无人消费的指标」 |

**M5 是本轮唯一逃过的一道门，值得单独记**：第一版 hop-2 判据只数了
`settings.RequestLogsWriteEnabled` 的**引用次数**（== 1）。M5 之后 wrapper 仍引用
该键一次，**返回值却已被偷换**，门照样绿。⇒ 改成断言**函数体形状**：恰好一条
`return settings.RequestLogsWriteEnabled()`、没有第二条语句。重放 M5 后红。

⇒ 这条与 §9.44 的教训同族：**「数量对」不等于「值对」**。

### §9.50.6 一个必须说清楚的边界

这道门断言的是 **AST 层面的词法包含关系 + `logsWrite` 的来源**，运行期由同一个变量
控制；两者合起来构成完整论证。**无法证明的那一侧**（探针在运行期到底返回什么）
由 bg 侧的告警覆盖，不在这里假装。门也**不能**替你决定该不该改：如果有人把
`persistSystemFingerprint` 挪到门控之外（这正是 §9.50.4 给的正确修法之一），
门会红并要求同步改告警文案——那个摩擦是**故意的**。

---

## §9.51 真库实测推翻了 §9.50 的**因果叙述**（同一件事，我连续订正两次）

### §9.51.1 起因：上一轮留的那个「前置实测」

§9.50 我给出的修法是「探针改读会话族（`session_turns` 的 `system_fingerprint`
覆盖**必须先在真库实测**）」，并把它列为下一轮的第 ② 项。本轮去做了。

### §9.51.2 实测（本地真库，2026-10-02）

| 面 | 行数 | `system_fingerprint` 非空 | 时段 |
|---|---|---|---|
| `request_logs`（v1 全表） | 2,164,650 | **0** | 2026-09-03 → 至今 |
| `request_logs_hot` | 3,189 | **0** | 仅今天有数据 |
| `session_turns`（会话族全表） | 1,683,739 | **0** | 2026-09-03 → 至今 |
| `model_integrity_events.context` JSONB | 7,081 | **0** | 2026-09-05 → 至今 |

最后一行的分量最重：integrity 事件走的是 **context JSONB**，
`detector.go:151` 把 `"system_fingerprint": c.SystemFingerprint` 写进去，
而 `c.SystemFingerprint` 与专用列**同源**（都取 `X-System-Fingerprint` 响应头，
`executor_chat.go:1926` / `handler.go:6730`）。⇒ **它是一条独立于专用列的证据路**，
排除了「列存在但写方没接」这种解释。

按天分形态也查了：近 14 天里只有今天有行，且今天 v1 3189 行里非空指纹 0 行
（**不是**「停写后才有 0」——停写前就是 0）。

### §9.51.3 结论：§9.50 的因果是错的

§9.50 写的是「**S4 停写之后 v1 不再产生新行 ⇒ 探针恒空**」。

**错。** 真实链路是：

```
system_fingerprint  ←  上游响应头 X-System-Fingerprint
                          ↑ 上游从来不发
探测针恒空（停写之前就空） ⇒ fingerprintScanSkip ⇒ 检测器自 2026-09-25
（D11 短路上线）起就一直关着 ⇒ 与 S4 无关
```

⇒ 我 §9.50 的告警文案「**最可能的原因就是 S4 停写**」会把运维引向查切换时刻
与 S4 读写门——**而那正是本条排除掉的假设**。

### §9.51.4 S4 停写的真实影响是**前瞻性**的（这部分 §9.50 说对了）

短路的腿 1 今天已经断了。S4 干的是**腿 2**：

`inProcSeen` 的进程内 arm，其唯一写入方 `markSystemFingerprintObserved()` 在
`persistSystemFingerprint()` 内调用，后者位于 `if logsWrite {}` 块内
（client.go:1816 / :2468）⇒ 停写后它一次都不执行。

⇒ **即便上游将来开始发指纹，停写 + 任意一次重启之后检测器也永远不会恢复。**

这才是那条告警真正该守的东西：**不是「现在扫不到」，而是「将来有了数据也扫不到」**。
§9.50 的告警**存在**是对的（检测器确实没在工作、确实没有任何信号），
它的**诊断**错了。

### §9.51.5 修法排序被这次实测**推翻**了一项

| 修法 | §9.50 的说法 | 实测后 |
|---|---|---|
| 探针改读会话族 | 「先实测覆盖」 | **证伪**。会话族 168 万行非空 0，同一上游原因。改过去只会把「因为缺数据而空」伪装成「已修好」 |
| `inProcSeen` 的 arm 移出门控 | 可做 | **仍是唯一的代码修复，且不依赖上游** |
| 真正的长期问题 | 未提 | 这个检测器在**任何**没有指纹数据时都无对象可检。要么让上游发指纹（产品决策），要么承认这项检测能力当前是空的并如实记录——不是靠调大间隔把告警压下去 |

### §9.51.6 门：+1 道，变异 3/3

新增 `TestIntegrityFingerprintDriftAlertNamesTheRealCause`，判三件事：
文案必须点名 `X-System-Fingerprint`（来源）、必须写明「停写之前探针就已经是空的」
（实测事实）、**必须附可复现的查询**（只给结论不给量具，下一个读它的人仍然只能猜）；
并禁用被撤回的因果措辞。

| 变异 | 结果 |
|---|---|
| M9 把因果改回「最可能的原因就是 S4 停写」 | 红（禁用措辞） |
| M10 删掉可复现查询 | 红（"a conclusion with no measuring stick"） |
| M11 把「停写之前」改回「停写之后」 | 红（两处同时命中） |

**一个门自己产生的假阳性，值得单独记**：第一次跑时这道门红了，原因是撤回说明里
**逐字引用**了被撤回的那句话，于是被自己的禁用词检查命中。这是「禁用某个字面串」
这一判据的固有缺陷——**合法的引用（订正记录）与非法的断言在文本上无法区分**。
处理方式是把订正说明改成**转述**而非引用（保留了信息，去掉了歧义），
而不是放宽判据（放宽就等于这道门没有）。

### §9.51.7 这一轮的教训

1. **写「X 停了 ⇒ 因为 Y」之前，先确认 Y 在 Y 之前就已经成立。** 我把「探针读 v1」
   当成了「探针为空的**原因**」，而它只是**载体**。载体被退役不等于读数归零。
2. **「先实测」这条我自己写进 handoff 的建议，救了这一轮。** 若跳过它直接改读会话族，
   会引入一个**看起来已修好、实际更糟**的改动。
3. **一个未兑现的承诺会在多个载体里复制**（§9.50 已记）。本轮又验证了一次：
   同一个错误因果同时存在于告警 YAML、`bg/integrity_fingerprint_drift_metrics.go`
   的文件头注释、以及审计文档 §9.50.1。**三处都要改**，只改一处等于没改。

---

## §9.63 `raw_model_name`：**不需要任何迁移** —— 我把它和 `client_ip` 归错了类

> **订正（2026-10-02）**：本节原标题是「`raw_model_name`：**不需要 817**」，
> 指的是「不需要为它新增一条 817」。而 §9.64 落地时**真的用掉了 817 这个号**
> （给 `client_ip` 换守卫）⇒ 同一句话里的「817」会指两件事。改标题去掉编号。
> **教训**：在一份还在生长的审计文档里，**不要用「下一个会分配的编号」当占位符**——
> 编号会被真实分配，而文档不会跟着改。

§9.60.9 / §9.62.4 都写着「`raw_model_name` 的投影待做，做法与 `client_ip` 的 816 同形」。
本节把它**做出来**了，结论是：**那一列从来不是投影缺口，一个迁移都不需要。**

### §9.63.1 回填落地

已验的两面 SQL 直接跑（本机）：

| 量 | 值 |
|---|---|
| 36h 窗口 session 行 | **4,277** |
| 回填后仍为 NULL | **0** |
| 与源 `COALESCE(outbound_model, client_model)` **逐值精确匹配** | **4,277（失配 0）** |

`AND t.raw_model_name IS NULL` 保证幂等：新代码上线后自己写的行不会被覆盖，
回填重跑也不改变已有值。

### §9.63.2 然后发现：**视图本来就已经投影了这一列**

回填完我去看视图，没有写任何迁移：

| 时点 | 视图近 1 天行数 | `raw_model_name` 有值 | 占比 |
|---|---:|---:|---:|
| 回填前 | 8,135 | **0** | 0.000% |
| **回填后（零迁移）** | 8,154 | **3,133** | **38.423%** |

⇒ session 臂的投影表达式**一直是** `t.raw_model_name AS raw_model_name`
（815 的 proj 字面量里就有，第 272 行）。它 0% 的原因**从头到尾只有一个**：
写方恒写 NULL。**不是「没投影」，是「投影着一个永远为空的值」。**

往返验值（视图 ↔ `session_turns` 两面）：

| 配对 | 视图有值 | 逐值精确匹配 | **失配** |
|---:|---:|---:|---:|
| 3,133 | 3,133 | **3,133** | **0** |

### §9.63.3 我把两列归错了类，代价是差点写一个不必要的迁移

`client_ip` 与 `raw_model_name` 在我此前的对照表里长得一模一样（都是「有源、
待投影、幂等无损」），于是我把它们当成同一类工程量。**它们的真实分类正相反**：

| | `client_ip` | `raw_model_name` |
|---|---|---|
| 视图里的形态 | `NULL::inet`（**补位**） | `t.raw_model_name`（**已投影**） |
| 0% 的原因 | 没投影 | 投影着空值 |
| 需要的工程 | **迁移**（816） | **只改写方**（§9.60.8）+ 回填 |
| 需要等观察窗口吗 | 不需要（写方早已有值 85.0%） | 不需要（投影已就位） |

⇒ **判别它们的不是「这一列有没有源」，而是「视图里那一格写的是什么」。**
我此前没打开 816 的 proj 字面量去看一眼 `t.raw_model_name` 那一行——
而那正是 815 里我已经读过无数遍的一行。**读过的内容在换了个问题之后要用时，
仍然得再读一次。**

⇒ 附带作废一条我自己在 §9.62.1 写下的推论：「投影**只可能把 NULL 变成值**」——
这句话是对的，但它让人以为「投影是一个待执行的动作」。而这里根本没有待执行的投影。

### §9.63.4 本节第四次「只碰了一个面」

写完回填我做校验，得到 **1,278 行失配**。真因：校验 SQL 里

```sql
FROM s LEFT JOIN request_logs v ON v.request_id = s.request_id;   -- ← 只联了父表
```

`request_logs` 父表停在 10:08，而 hot 那 1,388 行没有父表行 ⇒ `v` 为 NULL
⇒ 被 `IS DISTINCT FROM NULL` 判成失配。**加上 36h 窗口限定与 hot 面之后：
4,277 / 4,277、失配 0。**

⇒ 这是本节**第四次**同一个形状（前三次：§9.62.3 联键错、§9.62.3b 只写一个面、
§9.62.3 校验只联父表）。**四次里三次是我自己的查询，一次是回填 SQL 本身。**
它值得被当成一条硬纪律而不是运气：**凡跨两族，联键用 `request_id`，
存储面 hot ∪ parent，读写两侧都要查。**

（另记：`request_id` 在 v1 **全局唯一**——近 7 天 550,801 行 / 550,801 distinct，
36h 窗口 11,422 / 11,422。所以回填取单值没有歧义，这一点是验过的、不是假设。）

---

## §9.52 实施修复：把进程内 arm 移出 S4 停写门

§9.51 证伪了「探针改读会话族」，剩下**唯一成立的代码修复**是不依赖上游的那条：
`inProcSeen` 的进程内 arm 与宽表写入解耦。本轮做了。

### §9.52.1 改了什么

`persistSystemFingerprint` 原本在 `if err == nil` 分支里 arm 进程内检测器。
现在：

```go
// 落列：v1 专用列的写入，门控之内
func persistSystemFingerprint(ctx, tx, entry) error {
	...
	return err          // ← 不再 arm
}

// arm：观测到「本进程处理过一个带指纹的请求」，门控之外
func observeSystemFingerprint(entry *RequestLogEntry) {
	if entry == nil || entry.SystemFingerprint == nil || *entry.SystemFingerprint == "" {
		return
	}
	markSystemFingerprintObserved()
}
```

调用点放在 `insertRequestLog` / `updateRequestLog` 各自的
`logsWrite := requestLogsWriteEnabled()` 之后、`if logsWrite {` 之前。

**为什么落在这个函数而不是上游读头的地方**（`handler.go:6730` /
`executor_chat.go:1926`）：`executor_chat.go` 正在被并行会话大幅改动
（`git status` 显示 190 行改动），在共享工作区里动它是给自己埋雷。而
`insertRequestLog` / `updateRequestLog` 写 `usage_ledger_hot` 的部分**本来就在
门控之外**（S4 停写的契约是「宽表停写、计费照常」），所以这两个函数会**活过 v1
退役**——arm 放在这里不会跟着宽表一起消失。这两点合起来决定了落点。

**诚实记账**：真正的观测点是上游响应头被读出来的地方。把 arm 放在
`domains/streaming` 更"纯"，但那需要一个跨包导出，且会与并行会话的改动正面冲突。
**当前落点是一个正确的停靠点，不是最优雅的**。若日后要移到 `domains/streaming`，
门会提示（它只认「不在门内」这一条不变式，不认具体函数）。

### §9.52.2 门：**反转过一次**，反转过程本身是记录

第一版 `TestFingerprintEscapeHatchIsInsideTheStopWriteGate` 断言的是
「`markSystemFingerprintObserved` 唯一调用点在 `persistSystemFingerprint` 内」
且「`persistSystemFingerprint` 的调用点都在门内」——**那正是缺陷本身**。
修好之后它立刻变红。

⇒ 换成可持久的**不变式**：*arm 调用链上没有任何一环在 `if logsWrite {}` 内*。
这条在重构后依然可判定，而「钉死某个函数名」不会。

文案门同样反转：§9.50 写的 `TestIntegrityFingerprintDriftAlertDoesNotPromiseSelfHealing`
禁止文案说「逃生口也是关着的」；修好之后那句话变假话，门变红，
改名 `TestIntegrityFingerprintDriftAlertTracksTheArmFix`，方向翻过来。

**这个摩擦是故意留的**：文案与代码状态不一致时，最省事的做法是两边都不改。
让其中一边提醒另一边，是本项目里唯一能持续生效的机制。

### §9.52.3 这道门上发生的四次「装饰断言」——本节是本轮最有价值的部分

| # | 我写的断言 | 为什么是装饰 | 怎么发现的 |
|---|---|---|---|
| 1 | 环 3 =「`persistSystemFingerprint` 的调用点不得自我递归」 | 量的是完全不同的事，且几乎恒真 | 读自己的代码时发现它读起来像别的意思 |
| 2 | 同上，且排在环 1/环 2 **之后** | 撤销修复会先触发更严格的环 1/环 2 ⇒ 环 3 永远轮不到 | 造了个只让环 3 该红的变异，结果红在环 2 |
| 3 | 环 3 用 `isCallTo(persist.Body, …)` | `isCallTo` 是**浅**匹配（遇 `*ast.BlockStmt` 停），而 `Body` 自己就是 BlockStmt ⇒ 恒假 | 变异后环 3 仍不红 |
| 4 | 改成 `blockCalls(Body.List[i], …)` | 调用在 `if err == nil { … }` 里面，仍被浅匹配跳过 ⇒ 仍恒假 | 同上，再造一次变异 |

**第 4 次才真正修好**：为「某函数是否调用了 X」另写一个**深**匹配
`blockCallsDeep`。两种问题（*调用归属于哪个基本块* vs *函数是否调用了 X*）
需要两种匹配器，共用一个是错的。

⇒ 这已经是「浅匹配用错地方」在**同一轮内**的第三次（第一次在 bg 侧那道接线门：
一级 selector 认不出两级；第二次：`hasCall` 下探嵌套块；第三次：这里）。
**判据的正确性问题和它量的是不是同一件事，是两个独立的问题。**

### §9.52.4 变异验证（M12–M14）

| 变异 | 结果 |
|---|---|
| M12 两处 arm 都挪进 `if logsWrite` | 红（环 2 计数） |
| M12b 只把 `updateRequestLog` 那处挪进门内（保持 2 个调用点） | 红（环 2 `inGate`，`client.go:2132`） |
| M13/M13b 在 `persistSystemFingerprint` 里 arm（3 个调用点） | 红（环 2 计数先触发） |
| **M14 只在 persist 里 arm（删掉 update 那处 ⇒ 2 个调用点）** | **红（环 3）** ——这条是环 3 可达性的唯一证据 |

M14 是专门为验证环 3 构造的。前三条无论环 3 写得多糟都会红，
**所以它们不能证明环 3 有效**。

### §9.52.5 同一个陷阱在同一轮里踩了两次

§9.51 记录过：撤回说明里**逐字引用**被撤回的句子，会被自己的禁用词检查命中。
§9.52 改文案时**又踩了一次**（「§9.52 撤回了「逃生口也是关着的」」）。

⇒ 处理方式仍是**转述**而非引用。这条纪律的真正内容不是「怎么绕过判据」，
而是：**一份「禁用某字面串」的判据，天然无法区分「非法的断言」与「合法的订正记录」**，
所以文案里不该逐字复述被撤回的句子。

---

## §9.53 为 §9.44 的 cohort 决策取证：**本地库答不了这个问题**

本轮原计划是为 §9.44「基线 cohort 跨切换」的两个选项（(a) 显式重新基线化 /
(b) 引入独立稳定 cohort）收集真库证据。量完之后结论是：**这个量具在本地开发库上
不成立**，必须上 252。下面是量到了什么、以及我中途差点得出的两个错误结论。

### §9.53.1 量到的（本地库，2026-10-02，24h 窗口）

`settleBaselinesSQL` 的 cohort 谓词是 `is_auto_request = TRUE AND latency_ms IS NOT NULL`，
`GROUP BY task_type`，并叠加 `SQLExcludeSyntheticActors`（排除 `goal-%` 与三个
`*generator` actor）。两侧同谓词对照：

| | v1 | 会话族 |
|---|---|---|
| `is_auto_request = TRUE` | 1,932 | **0** |
| `is_auto_request IS NULL` | 1,326 | 0 |
| `is_auto_request = FALSE` | 0 | 1,299 |
| `task_type` 非空 | 1,920 / 1,932（**仅 auto 行**；非 auto 行 0/1,326） | **0** |
| 列是否存在 | — | 3/3 都存在（`is_auto_request` / `task_type` / `canonical_id`） |

跨族按 `request_id` 联（只按 request_id，hot ∪ parent）：

| | v1 行数 | 同 request_id 在会话族 |
|---|---|---|
| auto 行 | 1,938 | **0** |
| 全部行 | 3,266 | 1,301（39.8%） |

### §9.53.2 为什么本地库答不了

v1 那 1,938 条 auto 行的 `origin_actor` 分布：

| origin_actor | 行数 | 有 task_type | 有 `gw_session_id` |
|---|---|---|---|
| `node-probe-worker` | 1,930 | 是 | **0** |
| `auto-title-generator` | 14 | 否 | 14 |
| `active-probe-worker` | 2 | 是 | **0** |

会话族那 1,301 行的 `origin_actor`：`probe-service` 691 / `<null>` 565 /
`node-probe-worker` 36 / `credential-selfcheck-worker` 14
——**全部是系统流量，零业务 auto-route**。

⇒ 本地开发库的「auto 流量」是**探针 worker 发的合成流量**，它们**不带会话键**，
因此按设计不会进 `session_turns`。**这里根本没有业务 auto-route 流量可供建 cohort。**

⇒ 所以 §9.53.1 里那个「cohort 为 0」**不是缺陷信号，是量具失效信号**。
本库的 cohort 无论怎么算都是探针的形态。

### §9.53.3 我中途得出的两个**错误**结论（都由这次测量推翻）

**错误 1：「会话镜像丢了整个路由组，settle cohort 因写方没接线而为空。」**
依据是 `grep entry.IsAutoRequest internal/sessionv2mirror/hook.go` 在某段行区间内
返回 0。**错**：映射在 `s1a_fields.go:64-99`（`applyStorageS1AFields`），
`IsAutoRequest` / `AutoDecision` / `AutoConfidence` / `TaskTypeChosen` /
`RoutingAttempts` / `RoutingSummary` / `CanonicalID` / `CanonicalModel` /
`RawModelName` **一个不缺**。错因：**把行区间限定在了错误的文件上**，
于是「查不到」被读成了「没接线」。

**错误 2：「业务 auto 请求从未被镜像进会话族（1938 条 0 重叠），这是停写前必须
修的阻塞项。」** 错：那 1,930 条 `node-probe-worker` 行 `gw_session_id` **全为 NULL**，
没有会话键就不可能也不应该出现在 `session_turns` 里。**重叠为 0 是设计，不是缺陷。**

⇒ 两次都是**「没查到」被当成了「不存在」**。第二次尤其危险：如果照着它下结论，
就会把一个不存在的阻塞项写进停写前置条件清单。

### §9.53.4 必须在 252 上量的（本地无法替代）

按 §9.53.1 同一组查询在生产库跑，取**近 7 天**（本地只有 1 天有效流量，
且形态是探针）：

1. `SELECT is_auto_request, count(*) FROM request_logs GROUP BY 1` —— 生产上
   auto 行到底有多少、其中多少带 `task_type`。
2. 同谓词在 `session_turns` 上的计数 —— **这是判据 (a)/(b) 的分水岭**：
   若生产会话族里 auto 行**充足**，则 (a) 显式重新基线化即可；
   若为 0 或极少，则 (b)「引入独立稳定 cohort」也**无从建起**（没有数据），
   必须先解决「业务 auto 流量是否被镜像」。
3. 跨族按 `request_id`（hot ∪ parent，不带 ts）测 auto 行的重叠率 ——
   决定停写后 cohort 是否还成立。
4. `origin_actor` 分布：确认生产上是否存在**非探针**的 auto actor，
   以及它们是否落在 `SQLExcludeSyntheticActors` 的排除名单里。

⇒ **在拿到 ①–④ 之前，(a)/(b) 的选择不应被拍板。** 本地证据既不支持 (b)，
也不足以支持 (a)。

---

## §9.54 252 生产库实测：**§9.44 的 (a)/(b) 两个选项都不是正确的杠杆**

经 `env-injector inject aliyun-edge-252` + SSH 只读查询 `pg-252-pg17`
（用户授权；全程只读 SELECT）。**本节推翻 §9.44 提出的问题本身。**

### §9.54.1 回答 §9.53.4 的四条

**① v1 `is_auto_request` 分布（近 7 天，`request_logs` 全表）**

| 值 | 行数 |
|---|---:|
| `TRUE` | 22,406 |
| `NULL` | 10,581 |

按 `origin_stage` 拆：`node_probe` **19,252** / `business` **3,154**。

**② 会话族同谓词（近 7 天）**：`FALSE` 51,521 / **`TRUE` 18**。

⇒ **(b)「引入独立稳定 cohort」判死**：会话族 7 天只有 18 条 auto 行，
无法对任何 task_type 求 p95/p75。

**③ 跨族重叠**（`request_id`，hot ∪ parent）：v1 的 22,406 条 auto 行里
**15 条**在会话族；且这 15 条 `origin_stage = business`。
⇒ 99.5% 的**业务** auto 流量根本没有进入会话族。

**④ `origin_actor` 分布**：v1 auto 行为 `node-probe-worker` 19,221 /
`auto-summary-generator` 1,924 / `auto-title-generator` 1,215 /
`active-probe-worker` 31 / NULL 15。

### §9.54.2 ★真正的缺陷：cohort 与 settlements 的 task_type 几乎**不相交**

这一条**不需要 join**（纯 task_type 分组对比），因此没有取样假象风险：

| task_type | cohort 行数 | 30 天已结算 selection | 判定 |
|---|---:|---:|---|
| `chat` | **3** | 12,864 | p95 over 3 行不是分位数 |
| `creative` | **0** | 4,985 | **NO_BASELINE** |
| `code` | **10** | 3,501 | p95 over 10 行不是分位数 |
| `reasoning` | **0** | 1,269 | **NO_BASELINE** |
| `planning` | **0** | 4 | **NO_BASELINE** |
| `long_context` | 2 | 2 | |
| `probe_triggered` | **19,252** | **0** | cohort 的 99.95% 服务 0 条 selection |

⇒ **6,258 条已结算 selection（29.3%）完全没有基线**，其 latency/cost 两项按
`auto_route_settle_worker.go:615/618` 走**中性 0.5**。
⇒ `chat` / `code` 共 16,365 条（76.6%）被拿去和一个 n=3 / n=10 的「分位数」比较。
⇒ `probe_triggered` 贡献了 cohort 的几乎全部行数、却服务 0 条 selection，
同时把 §9.44 新加的 `llmgw_autoroute_settle_baseline_cohort_rows` 撑成一个
**看起来健康**的数字。

**这是线上正在发生的缺陷，与 v1→会话族的切换无关。**

### §9.54.3 根因：cohort 的总体定义 ≠ 实际被结算的总体

- cohort 谓词：`request_logs.is_auto_request IS TRUE` + `latency_ms IS NOT NULL`
  + `SQLExcludeSyntheticActors`（排除 `goal-%` 与三个 `*generator`/`summary` actor）。
- 被结算的总体：`auto_route_selections` 里 `task_type` 非空的行。

这两者的 task_type 词表几乎不重叠。**`SQLExcludeSyntheticActors` 的排除名单里
没有 `node-probe-worker` / `probe-service`**（`autoroute/shadow_actors.go:63-64`
只列了 `goal-%` 与 `auto-title-generator` / `auto-summary-generator` /
`session-summary`），而 `middleware/origin_mw.go:404` 明确把 `node-probe-worker`
映射到 `origin_stage = node_probe`。⇒ **合成的探针流量被算进了奖励基线。**

⇒ 正确顺序是：**先修 cohort 的总体定义**（改成从真正被结算的总体导出，
或用 `origin_stage = 'business'` 取代手工 actor 名单），**然后**才谈它存在哪个存储族。
换源族修不好一个定义错的总体。

### §9.54.4 我在 252 上差点得出的第三个错误结论（记下来）

按「30 天已结算 selection 有多少能在 v1 找到同 `request_id`」这条查，得到的数是
**21,367 里只有 15 条命中（0.07%）**——看起来像个灾难级的丢失。

**它是取样假象**：252 上 `request_logs` **总共只有 32,987 行，且全部落在 7 天内**
（`count(*) where ts > now()-30d` 与全表相等 ⇒ 无分区历史）。30 天窗口里的绝大多数
selection 本来就落在 v1 的覆盖范围之外。换成两侧都取 7 天窗口重做：
21 条已结算、15 条命中（71%），**仍有 6 条（29%）在 v1 里找不到**。

⇒ 「99.93% 丢失」**不能写进任何结论**。真实数字是「7 天匹配窗口内 6/21 = 29% 缺失」，
而且这个 29% 还需要独立复核（本轮未做完，不在这里给结论）。

⇒ 与 §9.53 同一个错误的第三次变体：**「没查到」被当成「不存在」**，
只是这次发生在**时间窗口**而不是文件范围上。
**取样方向必须先问清楚，再看数字。**

### §9.54.5 结论：§9.44 的问题被推翻

| 选项 | 判定 |
|---|---|
| (a) 显式重新基线化 | **不可行**——会话族 7 天只有 18 条 auto 行 |
| (b) 引入独立稳定 cohort | **不可行且方向错**——它修的是「cohort 换源族不可比」，而实测显示 cohort 在 **v1 里就已经是错的总体**（29.3% 的结算无基线、76.6% 对着 n=3/n=10 比、99.95% 的行服务 0 条结算） |

⇒ **两者都不是杠杆。** 前置条件是一个本轮之前从未出现在任何文档里的问题：
**cohort 的总体定义与被结算总体不对应。**

---

## §9.55 补测 252：订正 §9.54.2，并确认 §9.51 在生产上成立

本节做两件事：① 复核我自己在 §9.54 里标为「未做完、不给结论」的那两项；
② 核实 §9.51（本地库结论）能否外推到 252。**其中 ① 推翻了我自己刚写的 §9.54.2。**

### §9.55.1 §9.51 在 252 上**成立**（三条独立路）

| 面 | 行数 | `system_fingerprint` 非空 | 覆盖时段 |
|---|---:|---:|---|
| `request_logs`（v1） | 32,987 | **0** | 全表仅 7 天 |
| `session_turns`（会话族） | **804,096** | **0** | **全时段** |
| `model_integrity_events.context` JSONB | 3,829 | **0** | 2026-09-07 → 10-02 |

⇒ 告警文案里「上游从不返回 `X-System-Fingerprint`」这个诊断在**生产上是对的**。
会话族那 80.4 万行是唯一有完整历史的那张表，它同样全空。

### §9.55.2 §9.54.1 的 ② ③ **经复核成立**（这次用干净口径）

| 量（近 7 天） | 行数 | 在会话族 |
|---|---:|---:|
| v1 `origin_stage='business'` 的 auto 行 | 3,154 | **15**（0.48%） |
| v1 `origin_stage='node_probe'` 的 auto 行 | 19,252 | **0**（探针本就没有会话键，符合预期） |
| 近 7 天已结算 selection | 21 | **18**（86%） |

⇒ 「业务 auto 流量 99.5% 没进会话族」**不是取样假象**，§9.54.1 ③ 站得住。

**但同时出现一个反向事实**：结算侧会话族覆盖（86%）**高于** v1（71%）。
逐条看那 21 条：15 条两族都有；3 条（09-29/09-30）**只在会话族**、不在 v1；
3 条（09-25/09-26）在两族都没有——它们正好卡在 7 天窗口的边缘。

⇒ §9.54.4 说的「29% 缺失」既不是丢失也不是缺陷，**是窗口边缘 + 拿 v1 当基准**两个
artifact 叠加。真实陈述是：**会话族对结算行的覆盖比 v1 更好。**

### §9.55.3 ★订正 §9.54.2：那些「已结算 selection」是**三周前的历史**

`auto_route_selections` 近 30 天逐日量：

| 日期 | 量 | | 日期 | 量 |
|---|---:|---|---|---:|
| 2026-09-07 | 10,933 | | 2026-09-20 | 8 |
| 2026-09-08 | 11,411 | | 2026-09-25 | 1 |
| 09-09 … 09-14 | **0** | | 2026-09-26 | 2 |
| 2026-09-15 | 9 | | 2026-09-29 | 1 |
| 2026-09-16 | 120 | | 2026-09-30 | 2 |
| 2026-09-17 | 120 | | 2026-10-01 | 14 |
| 2026-09-18 | 3 | | 2026-10-02 | 1 |
| 2026-09-19 | **0** | | | |

⇒ **auto-route 的 selection 产出在 2026-09-15 前后塌掉了**：
从约 11,000/天降到 0–14/天，已稳定近零 **两周半**。

**排除「分区被删」这个解释**：`auto_route_selections` 的分区
`2026_08` / `2026_09` / `2026_10` / `2026_11` / `default` **全部存在**；
真实 `count(*) = 22,625`，最早 09-07、最晚 10-02。
⇒ 是**没产出**，不是**被删掉**。

**这推翻 §9.54.2 的框架**：那张「29.3% 的已结算 selection 无基线」的表，
把**当周**的 cohort 与**三周前两天**的 settlement 放在一起比。
两批东西不在同一个时间轴上，因此它**不是一个当下正在发生的缺陷**，
而是一张**跨期的静态对照**。（task_type 层面的「cohort 词表与 settlement 词表
几乎不重叠」这个事实本身仍然成立，但它的现实含义变了：
现在的 auto 流量几乎全是探针，因为**真正的 auto 路由已经两周半没产出 selection**。）

### §9.55.4 我没有查、也不猜的一件事

**为什么 auto-route 从 09-15 起不再产出 selection**，本轮**没有查**。
可能是探针/部署/开关/决策器异常，证据不足，任何归因都是编的。
**这应当是下一轮的第一件事**，而且它很可能才是 §9.44 整串问题的上游成因：
没有 selection ⇒ 没有需要基线的结算 ⇒ cohort 退化成一个纯探针的统计量。

### §9.55.5 这一节的方法论：一次自我订正的完整链条

1. §9.54.2 用「30 天 settlements vs 7 天 cohort」下了「线上正在发生」的结论；
2. §9.54.4 我自己发现了同类的取样假象，**却只怀疑了 v1 一侧**，
   没有回头质疑「30 天 vs 7 天」这个时间轴错配；
3. §9.55.3 逐日量一出来，错配直接可见（两个 epoch 差三周）。

⇒ **发现一次取样假象之后，必须把它当成一类错误去搜，而不是当成一个孤立事故修掉。**
本节三处（v1 的 7 天保留期、窗口边缘行、30d-vs-7d epoch 错配）都出自同一类。

---

## §9.64 我自己的 816 守卫是个**假守卫**：字符类合法 ≠ 能被 `::inet` 接受

本节是一次**对自己上一轮结论的推翻**。被推翻的是 §9.61.1 那张「守卫行为表」，
以及它蕴含的「816 的守卫已覆盖畸形值」这个判断。

### §9.64.1 缺陷：真库复现，不是推断

816 给 `client_ip` 投影装的守卫是：

```sql
(CASE WHEN t.client_ip ~ '^[0-9a-fA-F:.]+$' THEN t.client_ip::inet END)
```

它**只挡得住非字符集的垃圾**。下面这批值**全部由合法字符集组成**，因此全部通过
这个正则，然后全部死在 `::inet` 上：

| 值 | 为什么字符类合法 | 为什么语义非法 |
|---|---|---|
| `192.168.1` | 只有数字和点 | IPv4 要 4 段 |
| `deadbeef` | 只有 hex 字符 | 不是点分四段 |
| `1.2.3.4.5.6` | 只有数字和点 | 6 段 |
| `:::` | 只有冒号 | 无任何地址 |
| `...` | 只有点 | 纯点 |
| `999.1.1.1` | 只有数字和点 | 段值 > 255 |

本机 `llm_gateway`（PG 17.10）实跑复现：往 `session_turns` 插一行
`client_ip='192.168.1'`，再读 `public.request_logs_with_current_month`：

```
ERROR:  invalid input syntax for type inet: "192.168.1"
```

**整条查询中止**，不是那一行落 NULL。`request_logs_with_current_month` 的每一个
读方都会挂——而 816 的注释恰恰写着「守卫把畸形值落 NULL 而不是报错」。

> 这不是「理论上的防御不足」，是**一行数据就能让整条 canonical 视图不可读**。
> 攻击面是现成的：`client_ip` 来自 `X-Real-IP` 之类的**客户端可自由填写的头**。

### §9.64.2 我上一轮为什么没看出来

§9.61.1 的那张表列了三个「坏值」：`garbage`、`1.2.3.4, 5.6.7.8`（多跳 XFF）、
`''`。它们**全部字符类非法**——`garbage` 含 `g/r/b`、多跳链含逗号、空串长度不足。

⇒ 那张表准确地回答了「非字符集垃圾会被挡住吗」，而我把它读成了「畸形值会被挡住吗」。
**样本是我挑的，挑选标准恰好是「我能过的那一类」。**

这不是偶然失误，是一类可复发的错误：**行为表的分母是它列过的那几行，不是它
该覆盖的那一类。** 写行为表时必须先问「这一类里最难的那个是什么」，并**显式
把最难的放进表里**。如果表里最难的输入是我随手编的、且我确认它能过，那这张表
对更难的输入没有任何证明力。

**附带一个更该记住的点**：`pg_input_is_valid` 这类**完整语义判据**本来就存在，
手写正则近似它是一个可避免的选择。我当时写「守卫 CASE 与既有的
`application_id ~ '^[0-9]+$'` 转换同款」——而那几列的数字列转 bigint，
**字符类与语义恰好等价**（一串数字必然能转），所以那个写法在那里是对的，
我把这个模式**平移**到了一个**两者不等价**的类型上。

> **模式可以照抄，判据不能照抄。** 抄之前问一句：这个近似在目标类型上还成立吗？

### §9.64.3 修法：换判据，不换结构

- 守卫换成 `pg_input_is_valid(t.client_ip, 'inet')`（PG 16+；本仓一致跑 PG 17
  ——`docker/`、`deploy/`、`scripts/` 里 40 处 pg17、13 处 `postgres:17-alpine`）。
- **列序/列数仍是 118 → 118**：`client_ip` 换的是表达式，不是列。
- **不修改已应用的 816**：816 已在本机应用并登记进 `schema_migrations`，改它会让
  「已跑过」与「文件内容」分叉。新迁移是唯一诚实的做法 ⇒ **817**。

真库实测同一批值（7 个语义非法 + 4 个语义合法，共 11 行）：

| 形态 | 结果 |
|---|---|
| **816**（字符类） | `ERROR: invalid input syntax for type inet: "192.168.1"`，**整条查询中止** |
| **817**（语义） | **11 行全部返回**；7 个非法值 → NULL，4 个合法值原值透传 |

> **「修复了」不等于「数据没变盲」**：817 的表里刻意同时放了 4 个**合法**值。
> 一道只测非法值的门，照样能被「守卫退化成一律落 NULL」骗过——那是 §9.18
> 「修好了但变全盲」的同一形状。

### §9.64.4 写方那道门不能替代读方这道门

并行会话同日给 `middleware/origin_mw.go` 的 `resolveClientIP` 补了 `net.ParseIP`。
两者**互补，不冲突**——但它**只保护新写入的行**：库里已有的值、以及任何别的
写入方仍会流到读侧。**读侧的语义判据不能省。**

这也是为什么「写方已加固」不能作为「读方可以不加固」的理由：两道门防的是
**不同时点、不同来源**的同一类事故。

### §9.64.5 我在写 817 时自己引入的回归（已修）

第一版 817 把 816 的 view 链完整性守卫**窄化成了只查顶层视图**：

```sql
-- 816（正确）：三个视图都在
IF to_regclass('..._without_customer_id') IS NULL
   OR to_regclass('..._without_request_class_due_at') IS NULL
   OR to_regclass('...request_logs_with_current_month') IS NULL THEN
-- 817 第一版（错）：只剩顶层
IF to_regclass('...request_logs_with_current_month') IS NULL THEN
```

而 817 的第二个 DO 块**仍然**直接 `regclass` 那两个 wrapper 视图并从其中一个取
列清单 ⇒ 链不全时它崩在 `relation does not exist`。**那不是 no-op，是崩溃**
（680 事故形态）。我为了「让代码更简洁」而收窄的守卫，恰好是防住这次崩溃的那道。

同一轮还改掉三处：两处 `RAISE EXCEPTION 'migration 816: …'` 的复制粘贴、
以及 `COMMENT ON VIEW` 仍描述 816 形态（回滚后视图里留着弱守卫，而注释说
「带 CASE 守卫的 text→inet」——**注释与实际形态分叉**）。

down 侧另有一个更隐蔽的：第一个 DO 块在链不全时 `NOTICE + RETURN`（**没回滚任何
东西**），而 `DELETE FROM schema_migrations WHERE version='817'` 是**无条件**的
⇒ 会删掉 ledger 行，声称「817 没跑过」，而视图里还装着 817 的守卫。已改成
**只在真的回滚了时才删**。

### §9.64.6 门：静态 3 道 + 行为 1 道，变异 4/4

| 门 | 守什么 |
|---|---|
| `TestMigration817UsesSemanticClientIPGuard` | 守卫是 `pg_input_is_valid`；**字符类守卫必须消失**；列序/118 列不变 |
| `TestMigration817KeepsViewChainGuard` | 三个视图都在链守卫里（**守住 §9.64.5 的回归**），up 与 down 都要 |
| `TestMigration817DownRevertsWithWeaknessWarning` | down 退回弱形态**且带着「这是已知弱形态」的警告**；ledger 只在真回滚时删 |
| `TestMigration817HostileClientIPValuesDoNotBreakCanonicalView` | 敌意值真插真读，**带反向证据** |

**变异 4/4，全部为断言命中（非崩溃、非编译失败）**：

| 变异 | 命中 |
|---|---|
| M1 迁移 proj 退回字符类守卫 | `migration_817_test.go:84 / :101 / :121`（三条，红因互不相同） |
| M2 链守卫窄化为只查顶层 | `TestMigration817KeepsViewChainGuard`（缺两个 wrapper 视图） |
| M3 Go 镜像体退回字符类守卫 | `view_schema_v2_contract_test.go:430 / :441 / :518` |
| M4 **只改迁移文件**、不碰 Go | `TestRequestLogsViewV2EnsureMatchesMigration:518` |

M4 值得单独说：它验证了闭环的**方向性**。`registeredProjectionAppends` 那道门
只比「Go 投影 ↔ 手写登记表」，**不读迁移文件**——我若同改两处就会一直绿。
真正抓住 M4 的是真库那道：它**在 scratch 库上重放真实迁移文件**，与 Go `ensure`
的产物逐字节比对。

> **两道门各管一段，缺一段就有个方向是盲的**：登记表那道管「Go ↔ 表」，
> 真库那道管「Go ↔ 迁移文件」。只有它们同时在，锁步漂移才逃不掉。

### §9.64.7 行为门的反向证据

敌意值行为门若只有正向（817 形态下读到 11 行），它可能是个**恒过的空门**——
一个恒过的门比没有门更坏，因为它让人以为这个洞被守着。

⇒ 门内在**同一事务里**跑 817 down 换回弱形态，断言**同一批输入必须报错**，
然后整体 `ROLLBACK`：

- 剥掉 down 文件最外层 `BEGIN;/COMMIT;` 再执行（内层 `COMMIT` 会提交掉外层
  事务 ⇒ 「回滚」根本不发生，测试把真库留在弱形态而报告是绿的）；
- **对剥离结果断言**（必须恰好一层 BEGIN/COMMIT，否则 `t.Fatalf`）——结构变了
  就宁可测试红，也不要让事务保护悄悄消失。

实测：反向证据成立，报的正是 `invalid input syntax for type inet`。测试跑完后
复核真库视图仍是 817 形态、敌意行残留 0。

### §9.64.8 这一节没有做的

- **没有回滚 816**：它已应用，改它会造成「已跑过」与「文件内容」分叉。
- **没有动写方**：`net.ParseIP` 由并行会话落地（§9.64.4 说明它不能替代读侧）。
- **没有给 817 加幂等的「跳过重建」**：up 的第一个 DO 块在已是 817 形态时
  `NOTICE + RETURN`，但**第二个重建块仍会执行**（`CREATE OR REPLACE VIEW` 是
  幂等的、结果逐字节相同，代价是一次取锁）。这是**沿用 816 的既有形态**，
  不是本次引入的回归；改它属于重构，未做。

---

## §9.56 查「auto-route 为什么从 09-15 起不再产出 selection」：定位到当前状态，**归因未成立**

§9.55.4 明确「不猜」。本节把能验的验了，并把验不出来的部分连同**为什么验不出来**一起记账。

### §9.56.1 写入链（代码侧）

`domains/streaming/auto_route.go:666` `recordAutoSelectionFromWire` →
`telemetry.WriteAutoSelection`（`selection_writer.go:140`，异步入队，**队列满即丢弃**）
→ 批量 `INSERT INTO auto_route_selections_hot`（`:321`，38 列）。
失败时 `:221-225` 整批计入 `dropped` 并打 `WARN auto_route_selections batch insert failed`。

⇒ 存在**两条**能造成「产出归零」的机制：
 (i) 决策器不再产生 selection（上游没有 auto 流量/开关关了）；
 (ii) 产生了但在写入侧被丢弃（队列满 / 批量 INSERT 失败）。

### §9.56.2 机制 (ii) 被**排除**（当前进程内证据）

252 上 `llm-gateway-go` 监听 `127.0.0.1:8780`（systemd `llmgo-252-dev.service`）。
该 `/metrics` 端点当前返回：

```
llm_gateway_auto_selections_dropped_total 0
```

且 **`llm_gateway_auto_selections_total` 完全不出现**。它是带标签的 CounterVec
（`task_type` / `affinity_applied` / `explore`），Prometheus 约定下**首次 Inc() 之前
不会导出任何样本**——它缺席即意味着**一次都没有被写过**。

进程启动于 **2026-10-01 05:19:09**（`ps -o lstart=`），已连续运行 1 天 17 小时。
⇒ **当前：一条 selection 都没写出，且丢弃数为 0。**
⇒ 机制 (ii) 不成立；**是机制 (i)：决策器根本没有产出 selection。**

### §9.56.3 但 09-09 那一刻的归因**验不出来**，原因是证据不存在

| 想要的证据 | 实际可得性 |
|---|---|
| 09-08/09-09 的应用日志 | ❌ journal **最早只到 2026-10-02 16:22**（约 7 小时）。`batch insert failed` 计数为 0 ——**但它覆盖不到出事那天，所以不构成任何证据** |
| `auto_selections_total` 的历史值 | ❌ 252 上**没有 Prometheus / Grafana 容器**，无 TSDB 保留 |
| 文件日志 | ❌ `/opt/llm-gateway-go/logs/` 只有 `resource_monitor.log` 与 `shutdown.log`，无覆盖 09-09 的应用日志 |

⇒ **本节不给 09-09 的归因。** §9.55.3 确立的事实（09-07/08 各约 1.1 万，
09-09 起归零）**仍然只有现象、没有原因**。

**要定因需要**（本轮不具备）：覆盖 09-08/09-09 的应用日志，或 Prometheus 侧的
`llm_gateway_auto_selections_total` 历史序列。在此之前，任何「因为部署/开关/探针」
的说法都是编的。

### §9.56.4 ★一个让这件事两周半没被发现的缺口

`llm_gateway_auto_selections_total` **没有任何告警**。§9.44 建的
`BgFingerprintDrift*`、`AutoRouteSettle*` 全都只管**读侧**；
**产出侧「selection 写入量归零」一直没有门**。

⇒ 一个 2.5 周的产出中断，只有在有人去数 `auto_route_selections` 的日分布时
才被发现——而那个数只有专门去查才会看。

⇒ 这与 §9.37「没有告警读的指标是装饰」是**镜像形态**：
这里的 `llm_gateway_auto_selections_total` **有生产者、有指标、无消费者**，
于是它在监控上**根本不存在**。

**本轮不落这个告警**：`deploy/prometheus/rules/` 正被并行会话的
`a0da9066d` 改动（它新增了 `auto-route-settle-baseline_test.yml` 的 promtool
6 场景），而我的合并尚未解封。此时再动同一批文件只会把冲突面扩大。
⇒ 记账为下一轮的**第一件事**，并在此处写明判据要求（见 handoff）。

### §9.64.9 顺带修掉一个**早于本轮**的红灯：写死编号的权威源

`admin` 包里 `TestSessionArmNullPaddedColumnsMatchMigration` 报
「session 臂 NULL 补位表与 **migration 815** 不一致：迁移里有、表里没有
`[client_ip]`」。

用 worktree 回到 `HEAD` 复跑确认：**早于本轮改动**，是 816 那一轮留下的。
根因不在 `client_ip`，在**这道门把 815 当成了「当前权威迁移」**：

| 迁移 | `client_ip` 形态 |
|---|---|
| 815（及更早） | `NULL::inet` 补位 |
| 816 | 有源投影 + 字符类守卫 |
| 817 | 有源投影 + 语义守卫 |

816 把 `client_ip` 移出补位表之后，这道门就该改指 817——但**没人记得**，
于是它红了整整一轮没人处理：816 那轮所有 `db` 包的测试都是绿的，
只有这道**跨包**的 admin 门看得到。

**修法不是把 815 换成 817**（那只是把同一个错误推迟到下一条迁移），
而是**自动发现**：扫 `sql/migrations/startup/`，取**编号最大的、以
`CREATE OR REPLACE VIEW public.request_logs_with_current_month` 重建该视图的
up 迁移**。`.down.sql` 必须排除——down 恢复的是**旧形态**，把它算进
「当前」会让门在回滚方向上完全失准。

> **写死编号是这类门的默认失败模式**：它要求「每次新增一条重建视图的迁移」
> 都记得回来改这里，而改漏的表现**不是**「门忘了新迁移」，是「门拿旧迁移
> 当权威」——一个看起来完全合理的红，指向一个不存在的问题。
> 这与「登记表 + 穷举」是同一族：真相会漂移，而**没登记就不检查**是最安静
> 的一种失败，所以要把权威源**推导出来**而不是**记下来**。

变异验证（M5）：往 817 的 proj 注入一个 `NULL::text AS client_ip_drift_probe`
⇒ 门报「迁移里有、表里没有：`[client_ip_drift_probe]`」，红因即差集本身。
逐字节还原后复跑为绿。

---

## §9.57 补上产出侧的洞：auto-route selection 写入量归零告警

§9.56.4 记账的「下一轮第一件事」。本轮做完了。

### §9.57.1 补的是一个**产出侧**的洞

到 §9.56 为止，全部告警都建在**读侧**（结算、基线、漂移）。产出侧
「selection 写入量归零」一直没有门——而 `llm_gateway_auto_selections_total`
**有生产者、有值、零消费者**。这与 §9.37「没有告警读的指标是装饰」是
**镜像形态**：不是指标没有用，是它在监控上根本不存在。

`selection_metrics.go:14-16` 的注释写着 dropped 计数
「**worth alerting on rather than merely graphing**」——**注释里声称的保护，
如果没有门兑现它，那条注释就是装饰**。两个指标此前都无告警消费。

### §9.57.2 ★核心：`or vector(0)` 不是可选的

`llm_gateway_auto_selections_total` 是 **CounterVec**。Prometheus 约定下，
带标签的 CounterVec 在**首次 `Inc()` 之前不导出任何时间序列**。

于是最自然的写法会**静默失效**：

```promql
sum(increase(llm_gateway_auto_selections_total[2h])) == 0
```

永不产生 selection 时，左边求和得到的是**空向量**（不是 0）；空向量 `== 0`
仍是空向量；空向量不满足任何 alert 的触发条件 ⇒ **告警永远不响**，
而那恰恰是它唯一要抓的场景（§9.56 在 252 观测到的正是「一条序列都没有」）。

⇒ 这条断言**没有靠注释声明，而是被 promtool 场景测试证明**：
场景 A 在「真的没有该指标任何序列」的输入下要求这条规则**触发**。
要在一个空向量上得到 1，表达式里必须存在把空转成 0 的项。

### §9.57.3 场景测试抓出了规则本身的**两个真缺陷**（`check rules` 全都无感）

1. **告警丢掉实例归属。** 表达式原为
   `sum(...) or vector(0) == 0 and on() (up == 1)`。`and on()` 返回**左侧**的
   标签集，而左侧无标签 ⇒ 告警不带 `instance`，多实例部署里无法定位是哪台网关。
   `promtool check rules` **不报错**（语法完全合法）。
2. **修完第 1 条后仍丢 `job`。** 改用 `sum by (instance)` + `0 * max by (instance)`
   保住了 instance，却因为聚合维度里没有 `job` 而把它消掉。
   最终形态是 `by (job, instance)` + `and on(job, instance)`。

⇒ 只有场景测试的 `exp_labels` 比对能发现这类缺陷。这与 §9.44 那次
「注释里承诺的原则而代码没兑现」是同一类：**语法合法 ≠ 语义正确**。

### §9.57.4 我自己的 Go 门在**修复面前误报了两次**

`TestAutoRouteSelectionNotProducedHandlesTheCounterVecTrap` 第一版断言
`strings.Contains(expr, "or vector(0)")`。第二轮把表达式改成
`or (0 * max by (job, instance) (up{...}))` 之后门红了——但那一项起的是
**同一个作用**（无序列的实例得到 0），而且额外保住了标签。

⇒ **门若钉死字面串，就会在一次修复面前误报。** 改成断言**机制**
（正则匹配「把空变成 0 的那一项」与 `sum by (job, instance)`）。
`up == 1` 那条同理：从字面串改成正则。

**这与 §9.52 的「门被反转过一次」是同一个模式的反面**：那次是门在
**缺陷被修好后**才红（正确），这次是门在**缺陷被修好后**仍然红（不正确）。
区别在于判据锚的是**字面串**还是**机制**。

### §9.57.5 交付物

| 文件 | 内容 |
|---|---|
| `deploy/prometheus/rules/auto-route-selection-output.yml`（新） | 2 条告警 + `runbook` + 已知局限 |
| `deploy/prometheus/rules/auto_route_selection_output_test.go`（新） | 3 道 Go 门 |
| `deploy/prometheus/rule_tests/auto-route-selection-output_test.yml`（新） | 6 个 promtool 场景 |

告警：
- `AutoRouteSelectionsNotProduced`（warning, `for: 1h`）——2h 零增量且实例在线。
  描述里要求值班的人**先分清两种含义**（该部署本就不用 auto 路由 vs 真断流）。
- `AutoRouteSelectionsDropped`（warning, `for: 5m`）——30m 内 dropped 有增长。
  队列满**不打日志**，只能由它发现。

### §9.57.6 已知局限（如实登记，且有门守着「必须写下来」）

1. **本告警假设该部署在用 auto-route。** 完全不用 `model="auto"` 的部署会
   **永久**报红。仓库里没有「本部署是否启用 auto 路由」的配置位可依赖 ⇒
   用 warning + `for: 1h`，并在描述第一条就要求排除这个假阳性。
   要彻底消除需要新增部署级开关，**本轮不做**。
2. **252 上没有 Prometheus**（§9.56 实测：无 prometheus/grafana 容器）
   ⇒ 这组告警在 252 **不会生效**，适用环境是有 TSDB 的部署（154 / 本地）。

### §9.57.7 变异验证

| 变异 | 结果 |
|---|---|
| P1 删掉「空转 0」那一项 | 红（场景 A）——**这句注释因此被证明** |
| P2 去掉进程存活守卫 | 红（场景 E） |
| P3 聚合维度退回 `by (instance)` | 红（标签比对）——第二轮修的缺陷 |
| N1 删掉「空转 0」那一项 | 红（Go 门） |
| N2 删掉进程存活守卫 | 红（Go 门） |
| N3 删掉「已知局限」段 | 红（Go 门） |

`promtool check rules` SUCCESS；`promtool test rules` 6 场景 SUCCESS。

---

## §9.58 ★订正 §9.54.3：我把「内部生成器流量」当成了「业务 auto 流量」

§9.54.3 写的是「99.5% 的业务 auto 流量根本没进会话族」，并把它列为停写前的
阻塞项。本节用 252 实测证明**这个前提是错的**。

### §9.58.1 我当时用的筛选条件

§9.54/§9.55 用 `origin_stage = 'business'` 作为「真实业务流量」的代理，量出
3,154 条 auto 行里只有 15 条在会话族。

### §9.58.2 252 实测：这 3,154 条里 99.5% 是**内部生成器**

| origin_actor | origin_stage | request_type | 行数 |
|---|---|---|---:|
| `auto-summary-generator` | `business` | `main` | 1,924 |
| `auto-title-generator` | `business` | `main` | 1,242 |
| `<null>` | `business` | `main` | **15** |

按「`task_type` 是否为空」切分并各自看是否进会话族：

| task_type 为空 | 行数 | 在会话族 |
|---|---:|---:|
| 是 | 3,166 | **0** |
| 否 | **15** | **15（100%）** |

auto 全量按 `origin_stage` × actor 是否属内部名单交叉：

| origin_stage | actor 属内部名单 | 行数 |
|---|---|---:|
| `node_probe` | 否 | 19,408 |
| `business` | **是** | 3,166 |
| `business` | 否 | **15** |

⇒ **真正的业务 auto 流量是 15 条，而且 15 条全部进了会话族。**
那 3,166 条是 auto 标题/摘要生成器，**它们被排除出 `session_turns` 是正确的**
（不是用户轮次）。

⇒ **§9.54.3 的结论反了**：不是「业务 auto 流量没被镜像」，而是
「镜像行为完全正确，是我把内部生成器误认成业务流量」。
「99.5% 没进会话族」这个数字描述的是内部生成器，**它本就不该进去**。

⇒ 顺带这也让 §9.54.2 的 cohort 故事更弱：v1 的 auto 总体是
19,408 探针 + 3,166 内部生成器 + **15 真实业务**——**几乎全是合成流量**。

### §9.58.3 根因：**两份「内部 actor」名单，互不相认**

| 名单 | 位置 | 认不认 `auto-title-generator` / `auto-summary-generator` |
|---|---|---|
| `IsInternalAutoEntry` | `domains/hooks/observability/telemetry/internal_loopback.go:33-38` | **认**（按 actor 名 + `request_type`） |
| `trustedOriginOwners` | `middleware/origin_mw.go:121-131` | **不认**（两串一次都没出现） |
| `systemOwnerFallbackStage` | `middleware/origin_mw.go:401-406` | **不认** |
| `globalAuthStageActorPairs` | `middleware/origin_mw.go:151-166` | **不认** |

⇒ 这两个 actor 未登记为系统 actor ⇒ origin 中间件走普通路径，
把它们的 `origin_stage` 盖成 **`business`**；
而 `IsInternalAutoEntry` 按名字认出它们是内部 loopback，于是排除出 `session_turns`。

⇒ **同一件事（「这个 actor 是不是内部生成器」）有两份真相源，其中一份漏了两个成员。**

⚠️ 这不是新形态：§9.45 的 `sql_source_indirection_audit` 就撞过
「三个真相源让门测不出差别」；`settings` 的 `KeyRequestLogsWriteEnabled`
也是因为「同一个键被五处各写一遍字面量」才被提成常量。

### §9.58.4 后果与修法（需要拍板，本轮不实施）

**后果**：`origin_stage` 在 auto 总体上**不是**可靠的「是否内部」判据。
任何用 `origin_stage = 'business'` 做筛选的查询（包括 §9.54.2 的
cohort 分析、可能还有别的审计脚本）都会把 3,166 条内部生成器混进「业务」。

**修法候选**：
- (a) 把这两个 actor 补进 `trustedOriginOwners` / `globalAuthStageActorPairs`
  ——改的是**既有行的判定口径**，会让 3,166 条历史行的 `origin_stage`
  在重放时变成不同值（若有回填）。**属行为变更。**
- (b) 不动 origin，新增一个「是否内部」的**单一判定函数**
  （把 `IsInternalAutoEntry` 的 actor 名单与 origin 侧合并），
  并让所有筛选方改用它。改动面更大但不碰历史值。
- (c) 只加一道**门**把两份名单的差异钉出来并打印（默认红），
  迫使后续裁决。**本轮不做**：一道常红的门会立刻挂 CI，
  应当先有裁决再落门。

⇒ 我**不代为裁决**。但 §9.54.3 那条「停写前阻塞项」必须**撤回**：
它建立在一个已被证伪的前提上。
