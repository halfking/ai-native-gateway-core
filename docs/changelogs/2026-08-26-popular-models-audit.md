# 2026-08-26 — Popular Models 三源聚合 + DB 兜底 doc/code drift 审计

> **TL;DR**：审计 commit `c4cf1917f`（"feat(admin): 常用模型列表新增 DB 兜底 +
> telemetry 元信息（cardcredential 闭环第 2 段）"）及其衍生的 CHANGELOG /
> `docs/changelogs/2026-08-26-swimlane-credential-overlay.md`，发现 **8 处
> doc/code drift**：5 处描述了代码里不存在的函数/指标/env，1 处描述了
> 未实现的"DB count ≤ ZSet 候选数 short-circuit"，1 处函数签名错误，
> 1 处行数与代码实际不符。代码本体**功能等价于文档承诺**（fast path +
> SQL fallback），问题在**文档虚报**。本 commit 仅修文档，把未落地的特性
> 显式列入 follow-up。

## 1. 背景

老板在 8/22 ~ 8/26 期间复盘 admin "凭据路由模型" picker 加载慢的问题，
原 commit `c4cf1917f` 由前一会话落地。事后审计发现该 commit 的
commit message 与衍生 doc/CHANGELOG 描述的实现超出了实际代码范围。

## 2. 审计方法

按 rule 09 §2 FACT 三步检查法 + rule 49 §49-1 列名探查流程：

1. **Factuality（事实）**：`rg -n '<claim>' --type go` 全仓零匹配 → 文档虚报。
2. **Alignment（任务）**：原 commit 标题"DB 兜底 + telemetry 元信息"——代码
   落地了 DB 兜底（功能等价），但 telemetry 元信息**未落地**。
3. **Consistency（一致）**：CHANGELOG 与 `docs/changelogs/2026-08-26-swimlane-credential-overlay.md`
   互相引用同一组虚构函数名 → 一并纠正。

## 3. drift 清单（8 项）

| # | 文档声明 | 代码实际（`rg -n '<`>` /`grep -n '<`>`） | 严重度 |
|---|---|---|---|
| 1 | `fetchPopularModels(rdb, db)` 独立函数 | ❌ 0 命中；逻辑内联在 `admin/routing.go:queryPopularModels` | Major |
| 2 | `fetchPopularModelsForTenant(rdb, db, tenantID)` 租户隔离 | ❌ 0 命中；无 tenant scope | Major |
| 3 | DB 兜底 SQL：`SELECT ... FROM request_logs WHERE created_at > now() - interval '24h' GROUP BY model` | ❌ 实际是 `popularModelsHotSQL`：`FROM request_logs_hot rl` + plan-time literal `$1`（7d cutoff）+ LATERAL JOIN `model_aliases`（rule 33 §2.4 MUST-010 plan-time literal） | Major |
| 4 | `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS` env（默认 24h） | ❌ 0 命中；硬编码 `popularModelsHotCutoffWindow = 7*24*time.Hour` | Major |
| 5 | "DB count ≤ ZSet 候选数 short-circuit" | ❌ 0 命中；三源全部无条件 append | Major |
| 6 | `live_stream_tile_overlay_db_lookup` Prometheus metric（success/fail/locked/unknown） | ❌ 0 命中；`admin/live_stream_sse.go:overlaySnapshotTerminalStatuses` 只有 `slog.Debug` + `slog.Info`，无 Prometheus counter | Major |
| 7 | `live_stream_in_progress_db_overlay_count` 启动日志指标 | ❌ 0 命中 | Minor（仅 commit message 提及，未在 CHANGELOG 写入） |
| 8 | 测试 4 类场景 / 181 行 | ⚠️ 实际 7 个 tests / 190 行（`wc -l admin/routing_popular_models_test.go`） | Minor |

## 4. 实际实现（覆盖文档描述的能力）

`admin/routing.go:queryPopularModels(ctx, featuredModels, byCanonical)` 三源聚合：

```
featured (policy source)
  └── 来源：routing_policy.featured_models（DB 一查）
  └── 标签："policy"，count=nil

live (Redis dim queue ZCARD source)
  └── 函数：livePopularModels(ctx, rdb, 5)
  └── 数据：llmgw:live:dim:index:global SMEMBERS → 过滤 llmgw:live:dim:model:* → 各 ZCARD
  └── 标签："live"，count=ZCARD

recent (专用 ZSET source)
  └── 函数：recentlyUsedPopularModels(ctx, rdb, 10)
  └── 数据：ZREVRANGE llmgw:routing:recently_used_models 0..9 WITHSCORES
  └── 写入：RecordRecentlyUsedModel（admin/telemetry.go:persistRequestLog
      success path，e.Success==true 时 ZINCRBY + EXPIRE 7d）
  └── 探测门：isProbe=true / empty / "unknown" 全部跳过
  └── 标签："recent"，count=score

usage (SQL fallback source)
  └── 函数：popularModelsHotSQL（参数化 cutoff $1 = now - 7d，Go 注入）
  └── 数据：FROM request_logs_hot rl + LATERAL JOIN model_aliases
  └── 关键：plan-time literal 而非 NOW() - INTERVAL（rule 33 §2.4 MUST-010）
  └── 标签："usage"，count=COUNT(*)
```

三源通过现有 `add(...)` helper 去重聚合，输出 `[]popularModelEntry`。

并行：`admin/logs.go:listTopModels` 同样从 `request_logs_with_current_month`
切到 `request_logs_hot`（heap 单表，无 columnar 分区扫表），timeout 30s → 10s。

测试 7 项（`admin/routing_popular_models_test.go`，190 行）：
1. `TestPopularModelsHotSQL_TargetsHotTable` — SQL 文本契约（FROM + $1 + 非 NOW()）
2. `TestRecentlyUsedPopularModels_ReadsZSET` — miniredis ZSET round-trip + ZINCRBY
3. `TestRecordRecentlyUsedModel_ProbeGate` — isProbe / empty / "unknown" 全部不写
4. `TestRecordRecentlyUsedModel_NilClientSafe` — nil Redis client 不 panic
5. `TestRecordRecentlyUsedModel_TTLRefreshed` — TTL 滚动到 max
6. `TestLivePopularModels_NilAndEmpty` — live source 空场景
7. `TestListTopModelsSQL_TargetsHotTable` — logs.go 文本契约

## 5. 修复动作（本 commit 范围）

按 rule 11 §1 不扩大修改范围：只修文档，不补未落地特性。

- `CHANGELOG.md` line 14：把 `fetchPopularModels` + tenant scope + 24h env +
  `live_stream_tile_overlay_db_lookup` metric + 4 类场景 / 181 行 全部替换
  为实际描述（`queryPopularModels` 三源聚合 + 7 tests / 190 行），并附加
  审计修正段落。
- `docs/changelogs/2026-08-26-swimlane-credential-overlay.md`：
  - "第 3 段"原文（`fetchPopularModels(rdb, db)` + `fetchPopularModelsForTenant`
    描述）整段替换为实际 `queryPopularModels` 三源聚合描述，并附审计修正段。
  - "变更面"统计：`+181 4 类场景` → `+190 7 个 tests`，`popular models DB fallback`
    → `popular models 三源聚合 (live + recent + usage)`，`popular models tenant scope`
    → `listTopModels 切换 request_logs_hot + 10s timeout`，移除
    `cmd/gateway/main_livestream.go overlay 节流开关`（diff 实际是
    credential label + FIFO eviction，不是 overlay throttling）。
  - "关联"：`fetchPopularModels` → `queryPopularModels`，`4 类场景` → `7 个 tests`。
  - "部署计划"：移除虚构的 "overlay metrics success/locked/unknown 比例
    监测"，改为基于 slog 日志估算（`"live stream: overlaid terminal statuses from DB on snapshot"`）。
  - "已知遗留"：移除 "DB 兜底 env 默认 24h" 段落；增加 "硬编码 7d cutoff
    不是 env 可调" + "未实现租户维度" 两条 follow-up 引用。

未修改代码（`admin/routing.go` / `admin/logs.go` / `admin/telemetry.go` /
`admin/routing_popular_models_test.go` / `cmd/gateway/main.go` / `cmd/gateway/main_livestream.go`），
因为它们**功能正确**，虚报只在文档。

## 6. follow-up（不进本 commit，留给 handoff）

按工作量 / 风险分级：

### 6.1 [P1] `fetchPopularModelsForTenant(rdb, db, tenantID)` 租户隔离
- **风险**：多租户场景下所有 tenant 共享同一 popular models 聚合池，tenant A
  能看到 tenant B 的高频模型（按 `tenant_id` 列在 PG 隔离，违反 rule 19 §1）。
- **改动**：新增 `fetchPopularModelsForTenant(ctx, rdb, db, tenantID, limit int) []popularModelEntry`；
  usage source SQL 加 `WHERE rl.tenant_id = $2`；live source 跳过（Redis dim queue
  是全局索引，租户隔离在 DB 层做）；recent source 加 `ZSCAN` on
  `llmgw:routing:recently_used_models:<tenant_id>`（**需要 tenant 维度键**，当前
  是全局键 — 引入键分片 = `RecordRecentlyUsedModel` 改写写入路径）。
- **测试**：补 4 类测试（tenant A 命中 / tenant B 命中 / 空结果 / tenant 边界）。

### 6.2 [P1] `live_stream_tile_overlay_db_lookup` Prometheus counter
- **风险**：overlay 修复无监控指标，154 上 `success/fail/locked/unknown` 比例只
  能通过 slog 检索估算（P1 §6.2 L2 可观测性缺失）。
- **改动**：`admin/metrics/stream_overlay.go`（新建或合并到现有 stream metrics 文件）
  新增 `prometheus.CounterVec` labels `[outcome] = {success, fail, locked, unknown}`；
  `admin/live_stream_sse.go:overlaySnapshotTerminalStatuses` 三处结局分支
  (`slog.Info` / `slog.Debug`) 改成 `_ counter.WithWithLabelValues(outcome).Inc()`。
- **测试**：`admin/live_stream_sse_test.go` 新增 3 个 outcome label 触发后
  counter value 校验。

### 6.3 [P2] `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS` env
- **风险**：硬编码 7d cutoff 不可调；运营想看 24h / 30d / 90d 窗口时需改代码。
- **改动**：`admin/config/popular_models.go`（新建）读 `os.Getenv("LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS")`，默认 168（7d × 24h）；`popularModelsHotCutoffWindow`
  从 `const` 改成 `var` 并在 init() 里按 env 设值；测试固定 env 重置。
- **测试**：补 env override 测试（24h / 30d / 非法值 fallback）。

### 6.4 [P2] SQL fallback short-circuit
- **风险**：当前三源全部无条件 append，每次 picker 刷新都跑 SQL GROUP BY
  （即使 live+recent 已 ≥ 15 条，limit=20）。高负载下可能 100ms+ 浪费。
- **改动**：`queryPopularModels` 在三源 append 后判 `len(popular) >= limit`
  时直接 `return popular, nil`，跳过 SQL fallback。限制：limit 必须从
  caller 传入（`handleRoutingAvailableModels` 当前未传，需补参数）。
- **测试**：`admin/routing_popular_models_test.go` 新增 4 个 case：
  live+recent ≥ limit / live+recent < limit / SQL fail 时 short-circuit / limit=0 边界。

### 6.5 [P3] 整合测试（无 DB / 集成测试）
- 当前 7 个 unit tests 全部走 miniredis + 文本契约扫描，**未跑真实 PG**。
- 需要加 `tests/integration/popular_models_pg_test.go` 验证：
  - `request_logs_hot` 实表 + LATERAL JOIN 性能（P95 < 50ms）
  - ZINCRBY pipeline 在 Redis 5.x / 7.x 兼容性
  - `routing_policy.featured_models` 与 ZSET 结果的字段对齐

## 7. 关联

- 原始 commit：`c4cf1917f feat(admin): 常用模型列表新增 DB 兜底 + telemetry 元信息`
- 原始 doc：`docs/changelogs/2026-08-26-swimlane-credential-overlay.md`
- 修复 commit：本 commit（`docs(changelog): popular models doc/code drift 审计修正`）
- 修复 doc：本文件
- 关联规则：
  - rule 09 §2 FACT 检查法
  - rule 11 §5 诚实汇报（文档虚报即违反）
  - rule 49 §49-1 列名假设禁止（虚构函数名 = 跨表类比的反面案例）
  - rule 33 §2.4 分区裁剪（plan-time literal vs NOW()）