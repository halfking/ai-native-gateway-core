# URSM API 规范

## 概述

本文档定义URSM（统一路由状态管理系统）的所有API接口，包括：
1. 状态更新API（外部调用）
2. 状态查询API（内部使用）
3. 资源管理API（内部使用）

## 1. 状态更新API

### 1.1 请求状态回写

记录单次请求的执行结果，更新节点状态，必要时触发级联更新和探测。

**Endpoint**: `POST /api/routing/record-request`

**Request Body**:
```json
{
  "request_id": "req_123456",
  "credential_id": 42,
  "raw_model": "gpt-4o",
  "session_id": "sess_abc123",
  "success": true,
  "latency_ms": 1234,
  "error_kind": "rate_limit",
  "timestamp": "2026-07-03T10:30:00Z"
}
```

**Request Schema**:
```go
type RecordRequestAPI struct {
    RequestID    string    `json:"request_id" validate:"required"`
    CredentialID int       `json:"credential_id" validate:"required,gt=0"`
    RawModel     string    `json:"raw_model" validate:"required"`
    SessionID    string    `json:"session_id" validate:"required"`
    Success      bool      `json:"success"`
    LatencyMs    int       `json:"latency_ms" validate:"gte=0"`
    ErrorKind    string    `json:"error_kind,omitempty"`
    Timestamp    time.Time `json:"timestamp" validate:"required"`
}
```

**Response**:
```json
{
  "success": true,
  "updates_applied": [
    {"layer": "node", "credential_id": 42, "model": "gpt-4o"},
    {"layer": "model", "credential_id": 42, "model": "gpt-4o", "probe_state": "broken_confirmed"}
  ],
  "probe_triggered": false
}
```

**错误码**:
- `400` - 参数验证失败
- `500` - 内部错误

**逻辑**:
1. 验证请求参数
2. 分类错误（Ignored/Transient/Permanent）
3. 更新Node层状态（历史+连续失败）
4. 永久故障: 连续失败≥2 → 标记Model层broken
5. 临时故障: 连续失败≥3 → 触发探测
6. 原子写入（事务保证）

---

### 1.2 供应商状态修改

修改供应商的enabled/manual_disabled状态，自动级联到所有凭据。

**Endpoint**: `POST /api/routing/update-provider`

**Request Body**:
```json
{
  "provider_id": 5,
  "enabled": false,
  "manual_disabled": true,
  "reason": "供应商维护",
  "actor": "admin@example.com"
}
```

**Request Schema**:
```go
type UpdateProviderAPI struct {
    ProviderID     int    `json:"provider_id" validate:"required,gt=0"`
    Enabled        *bool  `json:"enabled,omitempty"`
    ManualDisabled *bool  `json:"manual_disabled,omitempty"`
    Reason         string `json:"reason" validate:"required,max=500"`
    Actor          string `json:"actor" validate:"required"`
}
```

**Response**:
```json
{
  "success": true,
  "provider_id": 5,
  "affected_credentials": [11, 12, 13],
  "audit_log_id": 9876
}
```

**逻辑**:
1. 加锁（防止并发修改）
2. 验证权限
3. 更新Provider层状态
4. 级联: 禁用Provider → 标记所有Credential为provider_disabled
5. 审计日志
6. 原子写入

---

### 1.3 凭据状态修改

修改单个凭据的availability_state/manual_disabled/quota_state。

**Endpoint**: `POST /api/routing/update-credential`

**Request Body**:
```json
{
  "credential_id": 42,
  "availability_state": "ready",
  "manual_disabled": false,
  "quota_state": "ok",
  "reason": "手动恢复",
  "actor": "admin@example.com"
}
```

**Request Schema**:
```go
type UpdateCredentialAPI struct {
    CredentialID      int     `json:"credential_id" validate:"required,gt=0"`
    AvailabilityState *string `json:"availability_state,omitempty" validate:"omitempty,oneof=ready degraded auth_failed rate_limited unreachable suspended"`
    ManualDisabled    *bool   `json:"manual_disabled,omitempty"`
    QuotaState        *string `json:"quota_state,omitempty" validate:"omitempty,oneof=ok periodic_exhausted balance_exhausted permanently_exhausted"`
    Reason            string  `json:"reason" validate:"required,max=500"`
    Actor             string  `json:"actor" validate:"required"`
}
```

**Response**:
```json
{
  "success": true,
  "credential_id": 42,
  "previous_state": {
    "availability_state": "auth_failed",
    "manual_disabled": true
  },
  "new_state": {
    "availability_state": "ready",
    "manual_disabled": false
  },
  "resources_released": {
    "fp_slots": 3,
    "concurrency_slots": 8
  },
  "audit_log_id": 9877
}
```

**逻辑**:
1. 加锁
2. 验证: Provider必须可用
3. 更新Credential层状态
4. 级联: 禁用Credential → 释放所有资源
5. 审计日志
6. 原子写入

---

### 1.4 探测结果回调（内部）

由探测器调用，记录探测结果。

**函数签名**:
```go
func (m *Manager) RecordProbeResult(ctx context.Context, result ProbeResult) error
```

**ProbeResult Schema**:
```go
type ProbeResult struct {
    CredentialID      int
    ProbeModel        string
    HealthStatus      string  // healthy/warning/degraded/unreachable/error/auth_failed
    AvailabilityState string  // ready/degraded/auth_failed/rate_limited/unreachable
    ProbeState        string  // healthy_confirmed/broken_confirmed/recovering/unknown
    LatencyMs         int
    ErrorMessage      string
    Timestamp         time.Time
    Source            string  // "probe_v2" or "model_probe"
}
```

**逻辑**:
1. 更新Credential层: health_status, availability_state
2. 更新Model层: probe_state
3. 更新Node层: 重置consecutive_failures
4. 原子写入

---

## 2. 状态查询API（内部）

### 2.1 检查节点可用性

**函数签名**:
```go
func (m *Manager) IsAvailable(ctx context.Context, credentialID int, model string) (bool, string)
```

**返回值**:
- `bool`: 是否可用
- `string`: 不可用原因（可用时为空字符串）

**逻辑**: 级联检查四层状态
1. Provider: enabled && !manual_disabled
2. Credential: status=active && availability_state=ready
3. Model: offer_available && binding_available && probe_state!=broken
4. Node: consecutive_failures < 3 && !disabled

---

### 2.2 获取可用节点列表

**函数签名**:
```go
func (m *Manager) GetAvailableNodes(
    ctx context.Context,
    model string,
    sessionID string,
) ([]RouteNode, error)
```

**RouteNode Schema**:
```go
type RouteNode struct {
    CredentialID     int
    RawModel         string
    ProviderID       int
    ProviderName     string
    
    // 状态
    Available        bool
    UnavailableReason string
    HealthStatus     string
    
    // 资源
    FpSlotIndex      int     // 已获取的指纹槽索引
    ConcurrencyHeld  bool    // 是否已获取并发槽
    
    // 历史（用于排序）
    PriceInPer1M     float64
    PriceOutPer1M    float64
    SuccessRate      float64
    P95LatencyMs     int
    CompositeScore   float64
}
```

**逻辑**:
1. 加载所有该模型的候选节点
2. 逐个检查可用性（四层级联）
3. 检查指纹槽（复用/获取/抢占）
4. 检查并发槽（获取/抢占）
5. 成本排序（价格30% + 速度40% + 稳定性30%）
6. 应用策略（tier/billing/sticky）
7. 返回排序后的列表

---

### 2.3 获取节点状态

**函数签名**:
```go
func (m *Manager) GetNodeState(ctx context.Context, credentialID int, model string) (*NodeState, error)
```

**NodeState Schema**:
```go
type NodeState struct {
    CredentialID        int
    RawModel            string
    ConsecutiveFailures int
    SuccessCount        int64
    FailureCount        int64
    LastSuccessAt       *time.Time
    LastFailureAt       *time.Time
    Disabled            bool
    DisabledUntil       *time.Time
    RecoverAt           *time.Time
    LastError           string
    UpdatedAt           time.Time
}
```

---

## 3. 资源管理API（内部）

### 3.1 检查并获取指纹槽

**函数签名**:
```go
func (m *FingerprintSlotManager) CheckAndAcquire(
    ctx context.Context,
    credentialID int,
    sessionID string,
    fpSlotLimit int,
) (slotIndex int, acquired bool, reason string)
```

**返回值**:
- `slotIndex`: 获取的槽索引（-1表示未获取）
- `acquired`: 是否获取成功
- `reason`: 失败原因

**逻辑**:
1. 检查是否已有pin → 复用
2. 尝试获取空闲槽
3. LRU抢占（活跃阈值5分钟）
4. 全部活跃 → 失败

---

### 3.2 释放指纹槽

**函数签名**:
```go
func (m *FingerprintSlotManager) Release(
    ctx context.Context,
    credentialID int,
    slotIndex int,
    sessionID string,
) error
```

**逻辑**:
- 刷新槽TTL（30分钟）
- 保留pin（24小时）
- 不删除槽（允许复用）

---

### 3.3 检查并获取并发槽

**函数签名**:
```go
func (m *ConcurrencySlotManager) CheckAndAcquire(
    ctx context.Context,
    credentialID int,
    sessionID string,
    concurrencyLimit int,
) (acquired bool, reason string)
```

**返回值**:
- `acquired`: 是否获取成功
- `reason`: 失败原因

**逻辑**: Redis Lua脚本原子操作
1. 检查全局计数 < limit
2. INCR全局计数
3. INCR会话计数

---

### 3.4 释放并发槽

**函数签名**:
```go
func (m *ConcurrencySlotManager) Release(
    ctx context.Context,
    credentialID int,
    sessionID string,
) error
```

**逻辑**: Redis Lua脚本原子操作
1. DECR全局计数
2. DECR会话计数
3. 会话计数=0 → DEL会话key

---

### 3.5 释放所有资源

**函数签名**:
```go
func (m *Manager) ReleaseResources(
    ctx context.Context,
    credentialID int,
    sessionID string,
    fpSlotIndex int,
) error
```

**逻辑**:
1. 释放并发槽
2. 释放指纹槽（刷新TTL，保留pin）

---

## 4. 批量写入API（内部）

### 4.1 应用状态更新

**函数签名**:
```go
func (w *BatchWriter) ApplyUpdates(ctx context.Context, updates []StateUpdate) error
```

**StateUpdate Schema**:
```go
type StateUpdate struct {
    Layer             Layer     // Provider/Credential/Model/Node
    Timestamp         time.Time
    Actor             string
    Reason            string
    Source            string    // request/probe/manual
    
    // Provider层
    ProviderID        int
    Enabled           *bool
    ManualDisabled    *bool
    
    // Credential层
    CredentialID      int
    AvailabilityState *string
    HealthStatus      *string
    QuotaState        *string
    
    // Model层
    Model             string
    ProbeState        *string
    OfferAvailable    *bool
    BindingAvailable  *bool
    
    // Node层
    Success           bool
    ErrorKind         string
    LatencyMs         int
    ConsecutiveFailures *int
}

type Layer int

const (
    LayerProvider Layer = iota
    LayerCredential
    LayerModel
    LayerNode
)
```

**逻辑**:
1. 开启DB事务
2. 按层级排序（Provider → Credential → Model → Node）
3. 逐层应用更新
4. 提交事务
5. 异步失效缓存

---

## 5. 错误码定义

```go
const (
    // 4xx 客户端错误
    ErrInvalidRequest         = "invalid_request"           // 400
    ErrCredentialNotFound     = "credential_not_found"      // 404
    ErrProviderNotFound       = "provider_not_found"        // 404
    ErrUnauthorized           = "unauthorized"              // 401
    
    // 5xx 服务器错误
    ErrInternalError          = "internal_error"            // 500
    ErrDatabaseError          = "database_error"            // 500
    ErrCacheError             = "cache_error"               // 500
    
    // 业务错误
    ErrNoAvailableNodes       = "no_available_nodes"
    ErrFpSlotSaturated        = "fp_slot_saturated"
    ErrConcurrencySaturated   = "concurrency_saturated"
    ErrProviderDisabled       = "provider_disabled"
    ErrCredentialUnavailable  = "credential_unavailable"
)
```

---

## 6. 监控指标

所有API都会记录以下Prometheus指标：

```
# 状态更新
ursm_state_update_total{layer,source,success}
ursm_state_update_duration_seconds{layer}

# 资源管理
ursm_fp_slot_acquired_total{credential_id}
ursm_fp_slot_preempted_total{credential_id}
ursm_fp_slot_rejected_total{credential_id,reason}
ursm_conc_slot_acquired_total{credential_id}
ursm_conc_slot_rejected_total{credential_id}

# 路由决策
ursm_routing_decision_total{model}
ursm_routing_decision_duration_seconds
ursm_nodes_filtered_total{reason}

# 缓存
ursm_cache_hit_total{layer,level}  # level=mem/redis/db
ursm_cache_miss_total{layer,level}
```

---

## 7. 审计日志格式

所有状态修改API都会记录审计日志：

```json
{
  "id": 9876,
  "timestamp": "2026-07-03T10:30:00Z",
  "actor": "admin@example.com",
  "action": "provider.update",
  "target_type": "provider",
  "target_id": 5,
  "before": {
    "enabled": true,
    "manual_disabled": false
  },
  "after": {
    "enabled": false,
    "manual_disabled": true
  },
  "reason": "供应商维护",
  "affected_resources": {
    "credentials": [11, 12, 13]
  }
}
```

---

## 8. 使用示例

### 8.1 请求完成后状态回写

```go
// domains/streaming/executors/executor.go

func (e *Executor) Execute(ctx context.Context, req Request) Result {
    // ... 执行请求 ...
    
    // 记录结果
    err := e.ursm.RecordRequest(ctx, ursm.RecordRequestAPI{
        RequestID:    req.ID,
        CredentialID: req.CredentialID,
        RawModel:     req.Model,
        SessionID:    req.SessionID,
        Success:      result.Success,
        LatencyMs:    result.LatencyMs,
        ErrorKind:    string(result.ErrorKind),
        Timestamp:    time.Now(),
    })
    
    if err != nil {
        slog.Warn("failed to record request", "error", err)
    }
    
    return result
}
```

### 8.2 路由决策

```go
// domains/streaming/executors/router.go

func (r *Router) PlanCandidates(model string, sessionID string) []Candidate {
    nodes, err := r.ursm.GetAvailableNodes(ctx, model, sessionID)
    if err != nil {
        return nil
    }
    
    // 转换为旧的Candidate结构
    candidates := make([]Candidate, len(nodes))
    for i, node := range nodes {
        candidates[i] = nodeToCandidate(node)
    }
    
    return candidates
}
```

### 8.3 管理员禁用凭据

```go
// admin/credential_handlers.go

func (h *Handler) handleDisableCredential(w http.ResponseWriter, r *http.Request) {
    credID := extractCredentialID(r)
    
    err := h.ursm.UpdateCredential(ctx, ursm.UpdateCredentialAPI{
        CredentialID:      credID,
        ManualDisabled:    boolPtr(true),
        AvailabilityState: stringPtr("suspended"),
        Reason:            "管理员手动禁用",
        Actor:             getActorFromContext(ctx),
    })
    
    if err != nil {
        writeError(w, err)
        return
    }
    
    writeJSON(w, map[string]any{"success": true})
}
```

---

## 9. API版本控制

当前版本: `v1`

所有API路径前缀: `/api/v1/routing/`

未来版本变更策略:
- 向后兼容的变更: 保持v1
- 破坏性变更: 引入v2, v1保留6个月过渡期

---

## 10. 安全性

### 10.1 认证
所有API需要通过以下方式之一认证：
- Bearer Token (内部服务调用)
- API Key (管理员操作)

### 10.2 授权
- RecordRequest: 仅内部服务可调用
- UpdateProvider/UpdateCredential: 需要superAdmin权限
- RecordProbeResult: 仅探测器可调用

### 10.3 审计
所有状态修改操作都会记录到 `routing_audit_log` 表。
