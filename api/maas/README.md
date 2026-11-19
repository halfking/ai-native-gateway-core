# MaaS API v0.1 — HTTP 传输层文档

> 契约来源：`docs/maas/contracts/openapi.yaml`（契约 v0.1，2026-11-17 冻结）  
> 实现位置：`llm-gateway-go/api/maas/`（Echo v5 传输边界）  
> 领域实现：`llm-gateway-go/domains/{inventory,commercial,trading,ledger}/`

## 总览

MaaS Control Plane API 提供模型即服务的核心交易能力：库存预留 → 报价 → 交易确权 → 权益计量。
HTTP 是**可选的进程边界**（进程内直接调用 Go ports 更高效）；本层按 OpenAPI v0.1 契约逐字映射。

```
租户购买流程（串行门闩）:
I 库存锁 → T 权益激活 → L 计量结算 → R 热路径切流
   无超卖      无白嫖      账平可审计
```

## 基础约定

### 认证与租户

| 头 | 必需 | 说明 |
|---|---|---|
| `X-Tenant-ID` | 是 | 权威租户标识（来自已验证的令牌，**body 中的 tenant_id 不可信**，必须与之相等否则 403） |
| `Idempotency-Key` | 写操作必需 | 8–128 字符；同键重复到达返回首次结果，绝不重复扣减 |
| `X-Trace-ID` / `X-Span-ID` | 可选 | 分布式追踪透传 |
| `X-Scopes` | 可选 | 逗号分隔的权限范围（如 `maas.inventory.reserve,maas.ledger.write`） |

### 错误格式

错误响应为 JSON 信封（兼容 RFC 7807 problem+json 习惯）：

```json
{
  "message": "insufficient inventory for gpt-4: requested 1000000000000, available 999500000",
  "trace_id": "trace_http_001"
}
```

### 状态码映射

| HTTP | 领域错误码 | 场景 | 可重试 |
|---|---|---|---|
| 400 | — | 请求体格式错误 / 缺 Idempotency-Key / 空行项 | ❌ |
| 403 | `MAAS_UNAUTHORIZED_SCOPE` | body tenant_id ≠ 认证租户 | ❌ |
| 409 | `MAAS_SELLABLE_INSUFFICIENT` | 库存不足（防超卖） | ✅ 可重询后重试 |
| 409 | `MAAS_HOLD_FENCE_MISMATCH` | 围栏令牌不匹配 | ❌ |
| 409 | `MAAS_CONFLICT_REVISION` | OCC 版本冲突 | 重读后重试 |
| 422 | `MAAS_HOLD_EXPIRED` | 预留已过期 | 必须重新预留 |
| 422 | `MAAS_RESERVE_NOT_COMMITTED` | hold 非 committed 状态 | ❌ |
| 422 | `MAAS_USAGE_ALREADY_SETTLED` | 用量已结算（防双结） | ❌ |
| 503 | `MAAS_RETRYABLE_UNKNOWN` | 瞬时故障 | ✅ 同幂等键重试 |

---

## 端点

### 1. 查询可售库存 — `POST /v0.1/sellable:query`

只读，无副作用。

```bash
curl -X POST http://localhost:8080/v0.1/sellable:query \
  -H "X-Tenant-ID: ten_001" \
  -H "Content-Type: application/json" \
  -d '{"model_sku": "gpt-4", "dimensions": {"region": "cn-north", "commitment": "hard"}}'
```

响应 200：
```json
{
  "resource_snapshot_id": "snap_test_001",
  "inventory_revision": 1,
  "observed_at": "2026-11-20T03:00:00Z",
  "stale": false,
  "items": [{
    "model_sku": "gpt-4",
    "sellable": {"amount": "1000000000", "unit": "token", "window": "monthly"},
    "unit": "token",
    "capacity_available": true
  }]
}
```

### 2. 创建预留 — `POST /v0.1/holds` → 201

软预留（pending），TTL 内有效。**超卖防护**：事务内 `FOR UPDATE` 锁定快照行，
校验 `requested ≤ Σ sellable − (pending+committed)`，并发请求串行化。

```bash
curl -X POST http://localhost:8080/v0.1/holds \
  -H "X-Tenant-ID: ten_001" \
  -H "Idempotency-Key: idem_purchase_20261120_001" \
  -H "Content-Type: application/json" \
  -d '{
    "lines": [{"model_sku": "gpt-4",
               "quantity": {"amount": "1000", "unit": "token", "window": "monthly"}}],
    "expires_at": "2026-11-20T04:00:00Z"
  }'
```

响应 201：
```json
{
  "hold_id": "hld_1795104672_1a2b3c4d",
  "status": "pending",
  "fencing_token": "0d32089a...59dfa9c5c",
  "inventory_revision": 2,
  "expires_at": "2026-11-20T04:00:00Z"
}
```

> **fencing_token 必须保存**：confirm/release 均需此令牌；不匹配返回 409。

### 3. 确认预留 — `POST /v0.1/holds/{hold_id}/confirm`

pending → committed（不可逆）。仅 committed 的 hold 可激活权益。

```bash
curl -X POST http://localhost:8080/v0.1/holds/hld_xxx/confirm \
  -H "X-Tenant-ID: ten_001" \
  -H "Idempotency-Key: idem_confirm_001" \
  -d '{"fencing_token": "<创建时返回的令牌>"}'
```

响应 200：`{"hold_id": "...", "status": "committed", "inventory_revision": 3, "committed_at": "..."}`

### 4. 释放预留 — `POST /v0.1/holds/{hold_id}/release`

```bash
curl -X POST http://localhost:8080/v0.1/holds/hld_xxx/release \
  -H "X-Tenant-ID: ten_001" -H "Idempotency-Key: idem_release_001" \
  -d '{"fencing_token": "...", "reason": "customer_cancelled"}'
```

响应 200：`{"hold_id": "...", "status": "aborted"}`（已终态时幂等返回当前状态）

### 5. 过期预留 — `POST /v0.1/holds/{hold_id}/expire`

客户端可触发，但**库存时钟做权威判定**：未到 TTL 时返回 `pending + expired=false`。

### 6. 纯估价 — `POST /v0.1/quotes:estimate`

**契约不变式：绝不创建 hold**（`hold_created` 恒为 false，响应后服务端二次强制）。

```bash
curl -X POST http://localhost:8080/v0.1/quotes:estimate \
  -H "X-Tenant-ID: ten_001" \
  -d '{"model_sku": "gpt-4", "quantity": {"amount": "1000000", "unit": "token"},
       "resource_snapshot_id": "snap_test_001", "price_version_id": "prv_2026_11_initial"}'
```

响应 200：
```json
{
  "quote_id": "quo_...",
  "quote_snapshot_id": "qsnap_...",
  "price_version_id": "prv_2026_11_initial",
  "status": "reference",
  "money": {"amount_minor": "3000", "currency": "USD"},
  "hold_created": false
}
```

### 7. 激活权益 — `POST /v0.1/entitlements:activate`

前置条件：hold 必须为 committed；同 hold 重复激活幂等返回同一权益。

```bash
curl -X POST http://localhost:8080/v0.1/entitlements:activate \
  -H "X-Tenant-ID: ten_001" -H "Idempotency-Key: idem_activate_001" \
  -d '{"hold_id": "hld_xxx", "contract_id": "ctr_001"}'
```

响应 200：`{"entitlement_id": "ent_...", "status": "active", "hold_id": "hld_xxx", "activated_at": "..."}`

### 8. 用量预占 — `POST /v0.1/usage-holds` → 201

请求级预占（**不是**对库存 hold 的二次扣减）。**配额门控**：
`maas_check_quota_available(entitlement, model_sku, requested)` 校验
`requested ≤ allocated − (consumed + held)`，事务内原子增加 `held_amount`。

```bash
curl -X POST http://localhost:8080/v0.1/usage-holds \
  -H "X-Tenant-ID: ten_001" -H "Idempotency-Key: idem_usage_001" \
  -d '{"entitlement_id": "ent_xxx", "request_id": "req_abc_001",
       "quantity": {"amount": "10000", "unit": "token"}, "model_sku": "gpt-4"}'
```

响应 201：`{"usage_hold_id": "usg_...", "status": "held", "expires_at": "..."}`  
配额不足：400 `{"message": "insufficient quota for gpt-4"}`（MAAS_QUOTA_EXCEEDED）

### 9. 用量结算 — `POST /v0.1/usage-holds/{usage_hold_id}/settle`

结算实际用量并释放差额。**账平恒等式**：`settled + released = held`（测试验证 800+200=1000）。

```bash
curl -X POST http://localhost:8080/v0.1/usage-holds/usg_xxx/settle \
  -H "X-Tenant-ID: ten_001" -H "Idempotency-Key: idem_settle_001" \
  -d '{"actual": {"amount": "8000", "unit": "token"}, "stream_interrupted": false}'
```

响应 200：
```json
{"usage_hold_id": "usg_xxx", "status": "settled",
 "settled": {"amount": "8000", "unit": "token"},
 "released": {"amount": "2000", "unit": "token"},
 "entry_id": "entry_..."}
```

双结防护：再次结算返回 422 `MAAS_USAGE_ALREADY_SETTLED`。

### 10. 追加修正 — `POST /v0.1/ledger:correct`

**只追加**：冲正不修改已入账分录，生成独立冲正条目供审计。

```bash
curl -X POST http://localhost:8080/v0.1/ledger:correct \
  -H "X-Tenant-ID: ten_001" -H "Idempotency-Key: idem_correct_001" \
  -d '{"entry_id": "entry_xxx", "reversal": {"amount": "-500", "unit": "token"}, "reason": "duplicate_charge"}'
```

响应 200：`{"reversal_id": "rev_...", "entry_id": "entry_xxx", "status": "posted"}`

---

## 可观测性

### Prometheus 指标（gateway-v2 `/metrics` 自动输出）

| 指标 | 标签 | 说明 |
|---|---|---|
| `maas_http_requests_total` | method, route, status | 请求计数（route 为注册模板，未匹配归 `unmatched`，防标签基数攻击） |
| `maas_http_request_duration_seconds` | method, route, status | 延迟分布；成功路径 P99 目标 < 200ms |
| `maas_domain_errors_total` | code | 领域错误码（超卖/配额/围栏不匹配等业务信号） |

打点通过 `Response.Before` 钩子在 WriteHeader 时刻执行，保证错误状态码（404/409/422）
被准确记录（Echo 错误处理器在中间件链返回后才写响应）。

### OTel 追踪

每个请求产生 server span：`MAAS <method> <route>`，属性含
`maas.tenant_id` / `http.route` / `http.method` / `http.status_code`；handler 错误记录为 span event。
请求体不入 span（PII/凭据风险）。全局 provider 由 `internal/observability/tracer.go` 的
`InitTracer` 初始化，未初始化时 no-op 零开销。

### Grafana 建议面板

- **SLI**：`rate(maas_http_requests_total{status=~"2.."}[5m]) / rate(maas_http_requests_total[5m])`
- **延迟**：`histogram_quantile(0.99, rate(maas_http_request_duration_seconds_bucket[5m]))`
- **超卖信号**：`rate(maas_domain_errors_total{code="MAAS_SELLABLE_INSUFFICIENT"}[5m])` 突增 = 供给紧张
- **配额信号**：`rate(maas_domain_errors_total{code="MAAS_QUOTA_EXCEEDED"}[5m])`

---

## 部署

```bash
# 构建并运行（需 PostgreSQL 15，schema 见 migrations/maas_v01_schema.sql +
# maas_pricing_schema.sql + maas_quota_schema.sql）
export DATABASE_URL="postgres://user:pass@localhost:5432/maas?sslmode=disable"
go build -o maas-api ./cmd/maas-api && ./maas-api

# 健康检查
curl http://localhost:8080/health
```

宿主装配（R 泳道）示例：

```go
srv := maasapi.NewServer(
    inventory.NewService(pool),
    commercial.NewService(pool),
    trading.NewService(pool),
    ledger.NewService(pool),
)
e := echo.New()
srv.RegisterRoutes(e.Group("/v0.1"))
```

## 测试

```bash
# HTTP 层集成测试（testcontainers 自动启停 PostgreSQL 容器，需 Docker）
go test ./api/maas/ -timeout 8m

# 覆盖：库存查询 / hold 生命周期 / 幂等性 / 验证错误 / 围栏令牌 /
# 报价不建 hold / 权益全链 / 账本流（账平验证）/ 配额超限 / 错误格式
# + 指标（4）+ 追踪（2）= 16 个测试
```

## 已知限制（v0.2 待办）

- 幂等重放响应的 JSONB 序列化（`internal/maasidem` 已支持重放，HTTP 层已验证同键同 hold_id）
- HoldRelease/Expire 的库存回补（TODO 注释保留，与 HoldConfirm 扣减对称）
- `shared_cap_id` 共享供应商上限的跨 SKU 聚合校验
- 审计事件写 Outbox（`maas_idempotency_log` 已就位，CloudEvents 信封见 contracts/events.go）

---

**文档版本**：v1.0　**更新日期**：2026-11-20　**对应实现**：commit `d8644cf4f` + `84d5ff9e8`
