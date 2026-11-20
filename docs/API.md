# MaaS HTTP API 文档

## 概述

MaaS (Model-as-a-Service) HTTP API 提供完整的资源购买、库存管理、使用计量功能。

**Base URL**: `http://localhost:8080/api/v1`

**认证**: 通过 HTTP 头传递
- `X-Tenant-ID`: 租户标识（必需）
- `X-Idempotency-Key`: 幂等性键（推荐用于写操作）
- `X-Trace-ID`: 追踪标识（可选）

---

## 健康检查

### GET /health

检查服务健康状态。

**响应示例**:
```json
{
  "status": "healthy",
  "timestamp": "2026-11-19T10:30:00Z"
}
```

---

## Inventory - 库存管理

### POST /api/v1/inventory/sellable/query

查询可售库存快照。

**请求头**:
```
X-Tenant-ID: ten_example_001
```

**请求体**:
```json
{
  "model_sku": "gpt-4",
  "tenant_id": "ten_example_001"
}
```

**响应示例**:
```json
{
  "resource_snapshot_id": "snap_001",
  "inventory_revision": 123,
  "observed_at": "2026-11-19T10:00:00Z",
  "stale": false,
  "items": [
    {
      "model_sku": "gpt-4",
      "sellable": {
        "amount": "1000000",
        "unit": "token",
        "window": "monthly"
      },
      "unit": "token",
      "capacity_available": true
    }
  ]
}
```

---

### POST /api/v1/inventory/holds

创建库存预留（pending 状态）。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_hold_create_001
```

**请求体**:
```json
{
  "lines": [
    {
      "model_sku": "gpt-4",
      "quantity": {
        "amount": "100000",
        "unit": "token",
        "window": "monthly"
      }
    }
  ],
  "expires_at": "2026-11-19T11:00:00Z",
  "quote_snapshot_id": "qsnap_001",
  "resource_snapshot_id": "snap_001"
}
```

**响应示例** (201 Created):
```json
{
  "hold_id": "hld_1234567890_abc123",
  "status": "pending",
  "fencing_token": "%s",
  "inventory_revision": 124,
  "expires_at": "2026-11-19T11:00:00Z"
}
```

**错误响应** (400 Bad Request - 库存不足):
```json
{
  "error": {
    "code": "MAAS_INSUFFICIENT_INVENTORY",
    "message": "insufficient inventory for gpt-4: requested 100000, available 50000",
    "retryable": true
  }
}
```

---

### POST /api/v1/inventory/holds/:holdID/confirm

确认预留（pending → committed）。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_hold_confirm_001
```

**请求体**:
```json
{
  "fencing_token": "%s",
  "quote_snapshot_id": "qsnap_001"
}
```

**响应示例** (200 OK):
```json
{
  "hold_id": "hld_1234567890_abc123",
  "status": "committed",
  "inventory_revision": 125,
  "committed_at": "2026-11-19T10:05:00Z"
}
```

**错误响应** (409 Conflict - 围栏令牌不匹配):
```json
{
  "error": {
    "code": "MAAS_HOLD_FENCE_MISMATCH",
    "message": "fencing token does not match",
    "retryable": false
  }
}
```

---

### POST /api/v1/inventory/holds/:holdID/release

释放预留（→ aborted）。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_hold_release_001
```

**请求体**:
```json
{
  "fencing_token": "%s"
}
```

**响应示例** (200 OK):
```json
{
  "hold_id": "hld_1234567890_abc123",
  "status": "aborted"
}
```

---

## Commercial - 报价与定价

### POST /api/v1/commercial/quotes/estimate

估算报价（不创建 hold）。

**请求头**:
```
X-Tenant-ID: ten_example_001
```

**请求体**:
```json
{
  "model_sku": "gpt-4",
  "quantity": {
    "amount": "100000",
    "unit": "token"
  },
  "resource_snapshot_id": "snap_001",
  "price_version_id": "prv_2026_11_initial"
}
```

**响应示例** (200 OK):
```json
{
  "quote_id": "quo_1234567890_xyz789",
  "quote_snapshot_id": "qsnap_001",
  "price_version_id": "prv_2026_11_initial",
  "status": "reference",
  "money": {
    "amount_minor": "3000",
    "currency": "USD"
  },
  "hold_created": false,
  "expires_at": "2026-11-19T11:00:00Z"
}
```

---

## Trading - 权益交易

### POST /api/v1/trading/entitlements

激活客户权益。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_activate_001
```

**请求体**:
```json
{
  "hold_id": "hld_1234567890_abc123",
  "legal_signoff_ref": "legal_signature_001",
  "contract_id": "contract_xyz_789"
}
```

**响应示例** (201 Created):
```json
{
  "entitlement_id": "ent_9876543210_def456",
  "hold_id": "hld_1234567890_abc123",
  "status": "active",
  "activated_at": "2026-11-19T10:10:00Z"
}
```

**错误响应** (404 Not Found - Hold 未确认):
```json
{
  "error": {
    "code": "MAAS_RESERVE_NOT_COMMITTED",
    "message": "hold status is pending, expected committed",
    "retryable": false
  }
}
```

---

## Ledger - 使用量账本

### POST /api/v1/ledger/usage/hold

预留使用量配额。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_usage_hold_001
```

**请求体**:
```json
{
  "entitlement_id": "ent_9876543210_def456",
  "request_id": "req_unique_001",
  "quantity": {
    "amount": "10000",
    "unit": "token"
  },
  "model_sku": "gpt-4"
}
```

**响应示例** (201 Created):
```json
{
  "usage_hold_id": "usg_5555555555_ghi789",
  "status": "held",
  "expires_at": "2026-11-19T10:20:00Z"
}
```

**错误响应** (400 Bad Request - 配额超限):
```json
{
  "error": {
    "code": "MAAS_QUOTA_EXCEEDED",
    "message": "insufficient quota for gpt-4",
    "retryable": false
  }
}
```

---

### POST /api/v1/ledger/usage/:usageHoldID/settle

结算实际使用量。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_usage_settle_001
```

**请求体**:
```json
{
  "actual": {
    "amount": "8500",
    "unit": "token"
  },
  "stream_interrupted": false
}
```

**响应示例** (200 OK):
```json
{
  "usage_hold_id": "usg_5555555555_ghi789",
  "status": "settled",
  "settled": {
    "amount": "8500",
    "unit": "token"
  },
  "released": {
    "amount": "1500",
    "unit": "token"
  },
  "entry_id": "entry_1234567890"
}
```

---

### POST /api/v1/ledger/usage/correct

追加使用量修正。

**请求头**:
```
X-Tenant-ID: ten_example_001
X-Idempotency-Key: idem_usage_correct_001
```

**请求体**:
```json
{
  "entry_id": "entry_1234567890",
  "reversal": {
    "amount": "-500",
    "unit": "token"
  },
  "reason": "billing adjustment - duplicate charge"
}
```

**响应示例** (201 Created):
```json
{
  "reversal_id": "rev_9999999999_jkl012",
  "entry_id": "entry_1234567890",
  "status": "posted"
}
```

---

## 错误码

| 错误码 | HTTP状态 | 说明 | 可重试 |
|-------|---------|------|--------|
| `MAAS_INSUFFICIENT_INVENTORY` | 400 | 库存不足 | ✅ |
| `MAAS_QUOTA_EXCEEDED` | 400 | 配额超限 | ❌ |
| `MAAS_HOLD_FENCE_MISMATCH` | 409 | 围栏令牌不匹配 | ❌ |
| `MAAS_HOLD_EXPIRED` | 400 | 预留已过期 | ❌ |
| `MAAS_RESERVE_NOT_COMMITTED` | 404 | 预留未确认 | ❌ |
| `MAAS_USAGE_ALREADY_SETTLED` | 400 | 使用量已结算 | ❌ |
| `MAAS_UNAUTHORIZED_SCOPE` | 403 | 权限不足 | ❌ |
| `MAAS_PRICE_RULE_NOT_FOUND` | 400 | 价格规则未找到 | ❌ |

---

## 完整流程示例

### 1. 查询库存
```bash
curl -X POST http://localhost:8080/api/v1/inventory/sellable/query \
  -H "X-Tenant-ID: ten_example_001" \
  -H "Content-Type: application/json" \
  -d '{"model_sku": "gpt-4"}'
```

### 2. 估算报价
```bash
curl -X POST http://localhost:8080/api/v1/commercial/quotes/estimate \
  -H "X-Tenant-ID: ten_example_001" \
  -H "Content-Type: application/json" \
  -d '{
    "model_sku": "gpt-4",
    "quantity": {"amount": "100000", "unit": "token"},
    "resource_snapshot_id": "snap_001",
    "price_version_id": "prv_2026_11_initial"
  }'
```

### 3. 创建预留
```bash
curl -X POST http://localhost:8080/api/v1/inventory/holds \
  -H "X-Tenant-ID: ten_example_001" \
  -H "X-Idempotency-Key: idem_001" \
  -H "Content-Type: application/json" \
  -d '{
    "lines": [{
      "model_sku": "gpt-4",
      "quantity": {"amount": "100000", "unit": "token", "window": "monthly"}
    }],
    "expires_at": "2026-11-20T00:00:00Z"
  }'
```

### 4. 确认预留
```bash
curl -X POST http://localhost:8080/api/v1/inventory/holds/hld_xxx/confirm \
  -H "X-Tenant-ID: ten_example_001" \
  -H "X-Idempotency-Key: idem_002" \
  -H "Content-Type: application/json" \
  -d '{
    "fencing_token": "%w"
  }'
```

### 5. 激活权益
```bash
curl -X POST http://localhost:8080/api/v1/trading/entitlements \
  -H "X-Tenant-ID: ten_example_001" \
  -H "X-Idempotency-Key: idem_003" \
  -H "Content-Type: application/json" \
  -d '{
    "hold_id": "hld_xxx",
    "contract_id": "contract_001"
  }'
```

### 6. 预留使用量
```bash
curl -X POST http://localhost:8080/api/v1/ledger/usage/hold \
  -H "X-Tenant-ID: ten_example_001" \
  -H "X-Idempotency-Key: idem_004" \
  -H "Content-Type: application/json" \
  -d '{
    "entitlement_id": "ent_xxx",
    "request_id": "req_001",
    "quantity": {"amount": "10000", "unit": "token"},
    "model_sku": "gpt-4"
  }'
```

### 7. 结算使用量
```bash
curl -X POST http://localhost:8080/api/v1/ledger/usage/usg_xxx/settle \
  -H "X-Tenant-ID: ten_example_001" \
  -H "X-Idempotency-Key: idem_005" \
  -H "Content-Type: application/json" \
  -d '{
    "actual": {"amount": "8500", "unit": "token"},
    "stream_interrupted": false
  }'
```

---

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `DATABASE_URL` | `postgres://localhost:5432/maas?sslmode=disable` | PostgreSQL 连接字符串 |
| `PORT` | `8080` | HTTP 服务器端口 |

---

## 运行服务器

```bash
# 设置数据库连接
export DATABASE_URL="postgres://user:pass@localhost:5432/maas?sslmode=disable"

# 运行服务器
go run cmd/maas-api/main.go

# 或编译后运行
go build -o maas-api cmd/maas-api/main.go
./maas-api
```

---

**文档版本**: 1.0  
**最后更新**: 2026-11-19
