# JournalSnapshot Authorization 架构设计

**日期:** 2026-08-29  
**状态:** 设计草案（待评审）  
**ADR 依赖:** `docs/adr/2026-08-28-requestjourney-journal-snapshot.md` §Decision point 4  
**相关实现:** `domains/dispatch/observation.go`, `domains/dispatch/pipeline.go`, `cmd/gateway/main_dispatch_observation.go`

---

## §1 背景

### 1.1 ADR 要求（§Decision point 4）

> Consumers must pass the caller's tenant and authorization context. An unauthorized request returns the same not-found-shaped result used by the detail path, avoiding cross-tenant existence leaks.

### 1.2 当前实现状态

**已实现（Push 路径）:**
- `JournalSnapshot` 包含 `CallerTenantID` 和 `CallerAuthorized` 字段（`observation.go:297-315`）
- `Pipeline.emitJournalSnapshot` 在 terminal 时刻从受信任的 dispatch 路径标记这些字段（`pipeline.go:1566-1567`）
- `dispatchJourneyJournalAdapter.ApplyJournalSnapshot` 验证 caller context（`main_dispatch_observation.go:109-121`）:
  - 拒绝 `CallerAuthorized = false` 的 snapshot
  - 拒绝 `CallerTenantID != TenantID` 的 snapshot（cross-tenant）

**未实现（Pull 路径）:**
- `AuthorizedJournalConsumer` 接口已定义（`observation.go:349-362`），但**无生产实现**
- 无外部查询 API（admin API、diagnostic tools）可以调用 `ConsumeSnapshot`
- 测试使用 `InMemoryJournalStore` 验证合约，但生产环境无对应的持久化存储

---

## §2 设计目标

### 2.1 核心目标

1. **Tenant 隔离:** 确保 caller 只能访问其 tenant 的 journal snapshots
2. **不泄露存在性:** 未授权访问返回与"不存在"相同的错误，避免 cross-tenant 探测
3. **统一错误模型:** 使用 `ErrJournalNotFound` 作为唯一的拒绝响应
4. **分层架构:** 授权检查在 consumer interface 层，不渗透到底层存储

### 2.2 非目标

- **细粒度权限:** 当前不区分"读"/"写"权限，只验证 tenant 身份
- **Operator/Admin bypass:** 暂不引入 super-admin 角色（预留扩展点）
- **审计日志:** 授权失败的审计由调用方负责（HTTP handler / gRPC interceptor）

---

## §3 架构设计

### 3.1 整体架构图

```
┌─────────────────────────────────────────────────────────────┐
│                    Caller Context                            │
│  (HTTP Request / gRPC Context / Internal Service Call)       │
│                                                               │
│  callerTenant: string                                        │
│  callerAuthorized: bool (from auth middleware)               │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│           AuthorizedJournalConsumer                          │
│  (Interface: domains/dispatch/observation.go)                │
│                                                               │
│  ConsumeSnapshot(ctx, callerTenant, requestID)               │
│    ↓                                                          │
│    1. Validate callerTenant != ""                            │
│    2. Retrieve snapshot from storage                         │
│    3. Verify snapshot.TenantID == callerTenant               │
│    4. Return snapshot OR ErrJournalNotFound                  │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│              Storage Layer (未实现)                          │
│  (Future: requestjourney.Recorder / PostgreSQL / Redis)      │
│                                                               │
│  - 存储: (tenantID, requestID) → JournalSnapshot             │
│  - 查询: GetByTenantAndRequest(tenant, request)              │
│  - 不执行授权检查（由 Consumer 层负责）                      │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 关键接口

#### 3.2.1 `AuthorizedJournalConsumer` 接口（已定义）

```go
// domains/dispatch/observation.go:349-362

type AuthorizedJournalConsumer interface {
    // ConsumeSnapshot retrieves a journal snapshot for the specified tenant and
    // request ID, verifying that callerTenant matches the snapshot's TenantID.
    //
    // Returns ErrJournalNotFound when:
    //   - The snapshot does not exist
    //   - callerTenant does not match the snapshot's TenantID
    //   - callerTenant is empty and the caller is not a super-admin bypass
    //
    // This not-found-shaped error prevents cross-tenant existence leaks: an
    // unauthorized caller cannot distinguish "does not exist" from "exists but
    // you cannot access it."
    ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error)
}
```

**关键点:**
- `callerTenant` 由调用方提供，来源于认证 middleware 解析的 JWT/API key
- `requestID` 是用户查询的目标 request
- 返回值只有两种情况：成功 `(snapshot, nil)` 或失败 `(zero, ErrJournalNotFound)`

#### 3.2.2 `ErrJournalNotFound` 错误（已定义）

```go
// domains/dispatch/observation.go:10-14

var ErrJournalNotFound = errors.New("journal snapshot not found")
```

**语义:** 统一的"不存在"错误，同时覆盖：
- Snapshot 真的不存在
- Snapshot 存在但 caller 无权访问
- Caller tenant 为空（未认证）
- RequestID 为空（无效查询）

---

## §4 实现方案

### 4.1 方案 A: 基于现有 `InMemoryJournalStore` 扩展（推荐，短期）

**适用场景:** 快速验证架构，测试优先

**实现路径:**
1. **保持接口不变:** `InMemoryJournalStore` 已实现 `AuthorizedJournalConsumer`
2. **生产适配器:** 创建一个桥接 requestjourney.Recorder 的实现
3. **暂不持久化:** Snapshot 仅存在于 Pipeline terminal 回调，不引入新的 DB schema

**代码示例:**

```go
// domains/dispatch/journal_consumer.go

// RecorderBackedJournalConsumer wraps a requestjourney.Recorder to provide
// authorized journal snapshot access. It reconstructs snapshots on-demand from
// the recorder's event projection rather than storing them separately.
type RecorderBackedJournalConsumer struct {
    recorder *requestjourney.Recorder
}

func NewRecorderBackedJournalConsumer(recorder *requestjourney.Recorder) *RecorderBackedJournalConsumer {
    return &RecorderBackedJournalConsumer{recorder: recorder}
}

// ConsumeSnapshot reconstructs a journal snapshot from the recorder's event
// projection, verifying tenant authorization per ADR §Decision point 4.
func (c *RecorderBackedJournalConsumer) ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error) {
    // Step 1: Validate caller
    if callerTenant == "" || requestID == "" {
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    // Step 2: Retrieve events from recorder
    // (Assumes recorder has a GetEvents method; if not, this is a design blocker)
    events, err := c.recorder.GetEvents(ctx, callerTenant, requestID)
    if err != nil || len(events) == 0 {
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    // Step 3: Reconstruct JournalSnapshot from events
    entries := make([]JournalEntry, 0, len(events))
    for _, evt := range events {
        entry, ok := journeyEventToJournalEntry(evt)
        if ok {
            entries = append(entries, entry)
        }
    }
    
    if len(entries) == 0 {
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    // Step 4: Verify tenant match (defense-in-depth)
    // If recorder.GetEvents already filtered by tenant, this is redundant but safe
    if events[0].TenantID != callerTenant {
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    return JournalSnapshot{
        TenantID:   callerTenant,
        RequestID:  requestID,
        Entries:    entries,
        // Truncated/SnapshotVersion fields may not be available from recorder
    }, nil
}

// Helper: reverse translation from requestjourney.JourneyEvent → dispatch.JournalEntry
func journeyEventToJournalEntry(evt requestjourney.JourneyEvent) (JournalEntry, bool) {
    // Map EventType back to NextAction
    // This is the inverse of journalEntryToJourneyEvent in main_dispatch_observation.go:158
    var action NextAction
    switch evt.EventType {
    case requestjourney.EventRequestSucceeded:
        action = NextActionCompleted
    case requestjourney.EventRequestFailed:
        action = NextActionFailed
    case requestjourney.EventRequestCanceled:
        action = NextActionCanceled
    case requestjourney.EventRetryScheduled:
        action = NextActionRetrySameCred
    case requestjourney.EventNodeSwitched:
        action = NextActionSwitchCred
    case requestjourney.EventModelSwitched:
        action = NextActionSwitchModel
    default:
        return JournalEntry{}, false
    }
    
    return JournalEntry{
        Seq:          int(evt.Seq),
        Action:       action,
        ErrorKind:    evt.RetryReason, // May be empty for terminal events
        Model:        evt.Model,
        CredentialID: evt.CredentialID,
    }, true
}
```

**优点:**
- 不引入新的持久化层，依赖现有 requestjourney.Recorder
- 实现简单，快速验证授权逻辑

**缺点:**
- `requestjourney.Recorder` 可能没有 `GetEvents` 方法（需要扩展或查询 Redis）
- 重建 Snapshot 可能丢失 terminal 时刻的元数据（Truncated, SnapshotVersion）
- 性能较低（每次查询都重建）

---

### 4.2 方案 B: 独立的 Snapshot 持久化层（长期）

**适用场景:** 需要高效查询、保留 terminal 元数据

**存储选型:**

#### 选项 1: PostgreSQL 表 `journal_snapshots`

```sql
CREATE TABLE journal_snapshots (
    tenant_id         VARCHAR(255) NOT NULL,
    request_id        VARCHAR(255) NOT NULL,
    snapshot_version  BIGINT NOT NULL,
    entries           JSONB NOT NULL,  -- Array of JournalEntry
    truncated         BOOLEAN NOT NULL DEFAULT FALSE,
    truncated_count   INT NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, request_id, snapshot_version)
);

CREATE INDEX idx_journal_snapshots_tenant_request 
    ON journal_snapshots(tenant_id, request_id);
```

**查询:**
```sql
-- 获取最新的 snapshot
SELECT * FROM journal_snapshots
WHERE tenant_id = $1 AND request_id = $2
ORDER BY snapshot_version DESC
LIMIT 1;
```

#### 选项 2: Redis（扩展现有 requestjourney store）

**Key schema:**
```
journal:snapshot:{tenantID}:{requestID}:latest  → JSON(JournalSnapshot)
journal:snapshot:{tenantID}:{requestID}:{version} → JSON(JournalSnapshot)
```

**TTL:** 与 requestjourney events 相同（7 天或按需配置）

**实现:**

```go
type RedisJournalConsumer struct {
    client *redis.Client
}

func (c *RedisJournalConsumer) ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error) {
    if callerTenant == "" || requestID == "" {
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    key := fmt.Sprintf("journal:snapshot:%s:%s:latest", callerTenant, requestID)
    data, err := c.client.Get(ctx, key).Bytes()
    if err != nil {
        if errors.Is(err, redis.Nil) {
            return JournalSnapshot{}, ErrJournalNotFound
        }
        // Log internal error but return not-found to avoid info leak
        slog.Warn("journal snapshot redis error", "tenant", callerTenant, "request", requestID, "error", err)
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    var snap JournalSnapshot
    if err := json.Unmarshal(data, &snap); err != nil {
        slog.Warn("journal snapshot unmarshal error", "tenant", callerTenant, "request", requestID, "error", err)
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    // Defense-in-depth: verify stored snapshot's TenantID matches caller
    if snap.TenantID != callerTenant {
        slog.Error("journal snapshot tenant mismatch",
            "caller_tenant", callerTenant,
            "snap_tenant", snap.TenantID,
            "request_id", requestID)
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    return snap, nil
}
```

**写入路径（修改 `dispatchJourneyJournalAdapter`）:**

```go
func (a *dispatchJourneyJournalAdapter) ApplyJournalSnapshot(ctx context.Context, snap JournalSnapshot) {
    // ... 现有的授权检查 ...
    
    // 持久化 snapshot 到 Redis（新增）
    if a.redisClient != nil {
        key := fmt.Sprintf("journal:snapshot:%s:%s:latest", snap.TenantID, snap.RequestID)
        data, err := json.Marshal(snap)
        if err != nil {
            slog.Warn("journal snapshot marshal error", "request_id", snap.RequestID, "error", err)
        } else {
            ttl := 7 * 24 * time.Hour
            if err := a.redisClient.Set(ctx, key, data, ttl).Err(); err != nil {
                slog.Warn("journal snapshot redis persist error", "request_id", snap.RequestID, "error", err)
            }
        }
    }
    
    // ... 现有的 recorder.Apply 逻辑 ...
}
```

**优点:**
- 高效查询（单次 Redis GET）
- 保留完整的 terminal 元数据
- 与现有 requestjourney store 共享 Redis 实例

**缺点:**
- 引入新的 Redis key schema
- 需要管理 TTL 和清理策略

---

### 4.3 Caller Context 传递机制

#### 问题: `callerTenant` 从何而来？

**现有架构:**
- `QueuedRequest.TenantID` 是 request 自身的 tenant
- `JournalSnapshot.CallerTenantID` 在 Pipeline terminal 时被设置为 `qr.TenantID`（trusted）

**Pull 查询路径（新增）:**
- HTTP API: `/api/v1/requests/{requestID}/journal` → 需要从 JWT/API key 解析 caller tenant
- Internal service call: 调用方已经过认证，直接传递 tenant

**推荐实现:**

```go
// HTTP Handler 示例
func (h *DiagnosticHandler) GetJournalSnapshot(w http.ResponseWriter, r *http.Request) {
    // Step 1: 从 auth middleware 获取 caller context
    callerTenant := auth.TenantFromContext(r.Context())
    if callerTenant == "" {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }
    
    requestID := chi.URLParam(r, "requestID")
    if requestID == "" {
        http.Error(w, "missing request_id", http.StatusBadRequest)
        return
    }
    
    // Step 2: 调用 AuthorizedJournalConsumer
    snap, err := h.journalConsumer.ConsumeSnapshot(r.Context(), callerTenant, requestID)
    if errors.Is(err, dispatch.ErrJournalNotFound) {
        http.Error(w, "not found", http.StatusNotFound)
        return
    }
    if err != nil {
        slog.Error("journal snapshot query error", "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }
    
    // Step 3: 返回 JSON
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(snap)
}
```

**Context 传递链:**
```
HTTP Request → JWT middleware → Context{tenant: "tenant-a"}
    ↓
Handler extracts tenant from Context
    ↓
journalConsumer.ConsumeSnapshot(ctx, "tenant-a", "request-123")
    ↓
Storage layer filters by tenant-a
    ↓
Verify snap.TenantID == "tenant-a" (defense-in-depth)
```

---

## §5 安全性分析

### 5.1 威胁模型

| 威胁 | 攻击场景 | 缓解措施 |
|------|----------|----------|
| **Cross-tenant 探测** | 攻击者尝试 requestID 暴力枚举以推测其他 tenant 的 requests | 所有失败情况返回相同的 `ErrJournalNotFound` |
| **Timing attack** | 攻击者测量响应时间差异（存在 vs 不存在） | Storage 查询无论成功/失败都返回一致的延迟（可选：添加随机 jitter） |
| **Caller tenant 伪造** | 恶意 caller 伪造 `callerTenant` 参数 | `callerTenant` 必须来自受信任的 auth middleware，不接受用户输入 |
| **Storage layer bypass** | 直接查询底层 Redis/DB 绕过授权 | Storage layer 本身不执行授权，所有访问必须通过 `AuthorizedJournalConsumer` |

### 5.2 Defense-in-Depth

1. **Auth Middleware 层:** 验证 JWT/API key，解析出 tenant
2. **Consumer Interface 层:** 验证 `callerTenant != ""` 和 `snapshot.TenantID == callerTenant`
3. **Storage 层:** 查询时已过滤 `WHERE tenant_id = callerTenant`（SQL）或使用 tenant 前缀 key（Redis）
4. **返回前验证:** 即使 storage 返回了 snapshot，也再次检查 `snap.TenantID == callerTenant`

**示例（Defense-in-Depth 实现）:**

```go
func (c *PostgresJournalConsumer) ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error) {
    // Layer 1: Interface validation
    if callerTenant == "" || requestID == "" {
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    // Layer 2: Storage query (already filtered by tenant)
    var snap JournalSnapshot
    err := c.db.QueryRowContext(ctx,
        "SELECT tenant_id, request_id, entries, truncated, truncated_count, snapshot_version FROM journal_snapshots WHERE tenant_id = $1 AND request_id = $2 ORDER BY snapshot_version DESC LIMIT 1",
        callerTenant, requestID,
    ).Scan(&snap.TenantID, &snap.RequestID, &snap.Entries, &snap.Truncated, &snap.TruncatedCount, &snap.SnapshotVersion)
    
    if err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return JournalSnapshot{}, ErrJournalNotFound
        }
        // Internal error: log but return not-found to avoid info leak
        slog.Warn("journal snapshot db error", "tenant", callerTenant, "request", requestID, "error", err)
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    // Layer 3: Post-retrieval verification (defense-in-depth)
    if snap.TenantID != callerTenant {
        slog.Error("journal snapshot tenant mismatch after retrieval",
            "caller_tenant", callerTenant,
            "snap_tenant", snap.TenantID,
            "request_id", requestID)
        return JournalSnapshot{}, ErrJournalNotFound
    }
    
    return snap, nil
}
```

---

## §6 实现路线图

### Phase 1: 验证架构（1-2 天）

**目标:** 验证授权接口和错误模型

**任务:**
1. ✅ 已完成：`AuthorizedJournalConsumer` 接口定义
2. ✅ 已完成：`InMemoryJournalStore` 实现（测试用）
3. ✅ 已完成：合约测试 `TestJournalSnapshot_Authorization`
4. ⏳ 待完成：评审本设计文档

### Phase 2: 生产实现（2-3 天）

**选择方案 A 或 B:**

**方案 A (推荐，快速):**
- 实现 `RecorderBackedJournalConsumer`
- 扩展 `requestjourney.Recorder` 添加 `GetEvents` 方法（如果不存在）
- 集成到 HTTP handler

**方案 B (完整，慢速):**
- 选择存储（PostgreSQL 或 Redis）
- 修改 `dispatchJourneyJournalAdapter` 添加持久化路径
- 实现对应的 Consumer（`PostgresJournalConsumer` 或 `RedisJournalConsumer`）
- 集成到 HTTP handler

### Phase 3: API 暴露（1 天）

**新增 HTTP endpoint:**
```
GET /api/v1/diagnostics/requests/{requestID}/journal
Authorization: Bearer <jwt>
X-Tenant-ID: <tenant-id>  (optional, fallback from JWT)

Response 200:
{
  "tenant_id": "tenant-a",
  "request_id": "request-123",
  "snapshot_version": 42,
  "entries": [
    {"seq": 1, "action": "retry_same_cred", "error_kind": "rate_limit", ...},
    ...
  ],
  "truncated": false,
  "truncated_count": 0
}

Response 404:
{
  "error": "not found"
}
```

**Auth middleware:**
- 从 JWT 解析 tenant
- 或从 API key 查询 tenant
- 注入到 `r.Context()` 中

### Phase 4: 测试与监控（1 天）

**集成测试:**
- 跨 tenant 访问返回 404
- 合法访问返回正确 snapshot
- 未认证访问返回 401

**监控指标:**
```
journal_snapshot_query_total{tenant, status="success|not_found|error"}
journal_snapshot_query_duration_seconds{tenant}
journal_snapshot_auth_denied_total{reason="empty_tenant|tenant_mismatch"}
```

---

## §7 开放问题

### 7.1 `requestjourney.Recorder` 是否支持反向查询？

**当前状态:** `Recorder.Apply()` 是单向写入（event → Redis），没有暴露 `GetEvents()` 方法

**影响:**
- 如果选择方案 A，需要扩展 `Recorder` 添加查询能力
- 或直接查询 Redis keys（绕过 Recorder 抽象）

**建议:** 与 requestjourney 模块 owner 确认是否可以添加 `GetEvents` 方法

### 7.2 Super-admin 角色是否需要？

**场景:** 运维人员需要查询任意 tenant 的 journal 用于故障排查

**设计选项:**
1. **扩展 `ConsumeSnapshot` 参数:** 添加 `superAdmin bool` 参数，允许绕过 tenant 检查
2. **独立接口:** 创建 `UnauthorizedJournalConsumer` 专门给运维工具使用（需要额外的 auth）
3. **暂不支持:** 运维人员通过直接查询 Redis/DB（绕过 API）

**建议:** 暂不支持 super-admin，等有明确需求时再扩展

### 7.3 是否需要查询历史版本的 snapshot？

**当前设计:** 只查询 latest snapshot（`snapshot_version` 最大的）

**扩展点:**
```go
type AuthorizedJournalConsumer interface {
    ConsumeSnapshot(ctx, callerTenant, requestID string) (JournalSnapshot, error)
    ConsumeSnapshotVersion(ctx, callerTenant, requestID string, version int64) (JournalSnapshot, error) // 新增
}
```

**建议:** 等有明确需求时再添加

---

## §8 决策建议

**推荐实施顺序:**

1. **立即执行:** 评审本设计文档（预计 1 小时）
2. **短期（本周）:** 实现方案 A（`RecorderBackedJournalConsumer`） — 快速验证架构
3. **中期（下周）:** 如果性能不满足，切换到方案 B（Redis 持久化）
4. **长期（下个月）:** 暴露 HTTP API，添加监控

**阻塞点解除:**
- §7.1 需要与 requestjourney owner 确认查询能力
- 如果 `Recorder.GetEvents` 不可行，直接选择方案 B

---

## §9 引用

- **ADR:** `docs/adr/2026-08-28-requestjourney-journal-snapshot.md` §Decision point 4
- **接口定义:** `domains/dispatch/observation.go:349-362`
- **测试合约:** `domains/dispatch/journal_snapshot_contract_test.go:271-344`
- **Push 路径实现:** `cmd/gateway/main_dispatch_observation.go:105-148`
- **Pipeline 集成:** `domains/dispatch/pipeline.go:1534-1575`
