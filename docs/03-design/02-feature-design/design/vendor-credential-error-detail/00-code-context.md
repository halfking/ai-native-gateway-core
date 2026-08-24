# Vendor/Credential Error Detail — 代码现状分析

## 1. 入口点

- API 路由注册：`admin/handler.go:1097-1106`（cfHandlers 区段）
- 路由模式：`mux.HandleFunc("/api/candidate-failures/<path>", admin(h.cfHandlers.<method>))`
- 前端入口：`web/src/views/provider-detail/` 下的 `<Tab>` 路由（vue-router），目标 Tab 是 `provider-detail/LogsTab.vue:39-60`（最近 24h 的 request_logs 列表，不含 error_detail）

## 2. 现有调用链

```
admin UI LogsTab.vue / 新 ErrorDetailTab.vue
        ↓ GET /api/vendors/credentials/{id}/error-detail?hours=24
admin/handler.go (admin() middleware wraps the new handler)
        ↓ admin/candidate_failure_handlers.go:getCandidateFailuresByCredential (existing)  ← 复用 helper
        ↓ admin/vendor_credential_error_handlers.go (new) — 本 LP 焦点
        ↓ pgxpool.Query → candidate_failure_logs_with_current_month (view)
                       + credentials 表（join 一次性取 label/health_state/state_reason_*）
                       + provider_profile_daily 表（取最近 7 天 total_score）
        ↓ JSON 响应（writeJSON）
```

## 3. 现有数据结构（SSOT）

### 3.1 candidate_failure_logs（partitioned table + hot + view）
位置：`deploy/sql/schemas/baseline/01-schema.sql:4963-4983`（父表）+ `deploy/sql/migrations/V359__candidate_failure_logs_hot_and_partition.sql`（hot + 视图）
关键列：
- `id BIGINT NULL`（startup 392 架构，hot 表 id 无默认值，app 层去重）
- `request_id text`
- `ts timestamptz` ← 必带谓词
- `tenant_id text`
- `credential_id integer NOT NULL`
- `provider_id integer NOT NULL`
- `raw_model_name text`
- `attempt_index integer`
- `error_kind text` ← errorsx.Kind* 字符串
- `error_message text` ← 上游 err.Error() 文案
- `upstream_status_code integer NULL`
- `upstream_response_preview text NULL` ← 截断到 ~512 字节
- `upstream_response_body text NULL`（不暴露给 API）
- `latency_ms integer`
- `per_attempt_latency_ms integer`
- `retryable boolean`
- `diagnosed_error_kind text NULL`
- `context jsonb`
- `session_id text NULL`

### 3.2 credentials（主表）
位置：`deploy/sql/schemas/baseline/01-schema.sql:5842-5923`
关键列（本 LP 关注）：
- `id bigint PRIMARY KEY`
- `provider_id bigint`
- `label text`
- `health_status text` ← `unknown / healthy / warning / unreachable`
- `health_error text`
- `health_latency_ms integer`
- `availability_state text` ← `ready / cooling / rate_limited / auth_failed / unreachable / suspended`
- `state_reason_code text`
- `state_reason_detail text`
- `state_updated_at timestamptz`
- `quota_state text`
- `lifecycle_status text` ← `active / disabled / suspended / retired`
- `circuit_state text`
- `circuit_opened_at timestamptz`
- `consecutive_failures integer`
- `manual_disabled boolean`
- `auto_disabled_at timestamptz`
- `auto_disabled_reason text`
- `balance_usd numeric(14,6)`
- `balance_currency text`

### 3.3 provider_profile_daily（daily quality score）
位置：`domains/providerprofile/pg_profile_source_test.go:32`（schema 推断）
关键列：
- `credential_id bigint`
- `provider_id bigint`
- `profile_date date`
- `total_score numeric`
- `availability_score numeric`
- `stability_score numeric`

### 3.4 视图 `candidate_failure_logs_with_current_month`
位置：`V359__candidate_failure_logs_hot_and_partition.sql:300-309`
- UNION ALL `candidate_failure_logs_hot` ∪ `candidate_failure_logs_2026_08`（当月分区）
- 这是 admin 读端的 canonical view（rule 33 §2.5 + §2.6）

## 4. 现有不变量 / 约束

1. **tenant 隔离**：`candidate_failure_logs` 启用了 RLS，policy = `tenant_id = get_current_tenant()`
   （`V359__*.sql:263-271`）。新查询必须走 `get_current_tenant()` 上下文。
2. **id 可空**：hot 表 id 是普通 bigint 无默认值，新行 id=NULL（startup 392 架构）。
   现有 handler 已用 `*int64`（`candidate_failure_handlers.go:109`）。
3. **error_kind 来源**：由 `bg/candidate_failure_monitor.go` 写入，值域 = `errorsx.Kind*`
   （参见 `errorsx/classify.go`）。
4. **upstream_response_preview 截断**：~512 字节（`bg/candidate_failure_*.go` 的截断逻辑）。
5. **time window 默认 24h**：参照 `parseSinceQuery(r, "since", 24*time.Hour)`（`candidate_failure_handlers.go:322-330`）。
6. **query 超时 5s**：`context.WithTimeout(r.Context(), 5*time.Second)`（同文件 :85）。
7. **gateway credential id** = `<gateway_credential_id>` 而非 `<provider_credential_id>`
   （rule 33 范围内的别名，本 LP 默认）。

## 5. 外部依赖

- **DB pool**：`h.db *pgxpool.Pool`（admin/handler.go:52）—— 已 wired，无需新依赖。
- **pgx / pgxpool**：项目统一使用（rule 33 §2）。
- **writeJSON / writeError / parseIntQuery / parseSinceQuery**：`admin/handler.go` helper，
  新 handler 直接复用 `parseIntQuery` 与 `parseSinceQuery`（`candidate_failure_handlers.go:301-330`）。
- **admin() middleware**：`admin/handler.go:778-782` —— JWT 校验。
- **i18n**：前端 `web/src/i18n/locales/zh-CN/providerDetail.ts`（logs.* 已存在，复用）。

## 6. 可复用点（已存在）

| 复用 | 位置 | 用法 |
|---|---|---|
| `parseIntQuery(r, "limit", 50, 1, 200)` | `candidate_failure_handlers.go:301-317` | LP1 query 参数解析 |
| `parseSinceQuery(r, "since", 24*time.Hour)` | `candidate_failure_handlers.go:322-330` | LP1 时间窗口 |
| `writeJSON / writeError` | `admin/handler.go` | LP1 响应 |
| `admin(mux.HandleFunc)` wrap | `admin/handler.go:778` | LP1 路由 |
| `useFormat()`、`useI18n()`、`t()`/`td()` | `web/src/views/provider-detail/LogsTab.vue:1-15` | LP4 前端 |
| `ProviderLogEntry` 类型 | `web/src/api/providers.ts:807` | LP3 TS 镜像（已有 logs shape） |
| `el-tabs` / `el-table` pattern | `web/src/views/provider-detail/OverviewTab.vue` | LP4 复用 |

## 7. 改动 vs 新增清单

| 类型 | 文件 | 行数 |
|---|---|---|
| **新增** | `admin/vendor_credential_error_handlers.go` | ~150 |
| **新增** | `admin/vendor_credential_error_handlers_test.go` | ~70 |
| **修改** | `admin/handler.go`（注册新路由，~5 行） | ~5 |
| **新增** | `web/src/api/vendor-credential-error.ts` | ~80 |
| **新增** | `web/src/views/provider-detail/ErrorDetailTab.vue` | ~250 |
| **修改** | `web/src/router/index.ts`（注册新路由，~10 行） | ~10 |
| **修改** | `web/src/views/provider-detail/index.vue`（tab 列表，~3 行） | ~3 |

**总新增/修改 ≈ 568 行，4 个 LP 均 ≤ 300 行 ✅**（rule 42 §2.2）
