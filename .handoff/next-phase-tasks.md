# 下一阶段待执行任务清单

基于 2026-08-29 审计报告，以下是识别的改进项和后续工作。

## 优先级分类

### P1 - 高优先级（建议立即执行）

#### 1. 添加 Prometheus Metrics
**范围:** Empty response 检测 + JournalSnapshot
**文件:**
- `domains/streaming/metrics.go`
- `metrics/interface.go`
- `metrics/prometheus.go`

**Metrics 清单:**
```go
// Empty response detection
SuccessEmptyResponseTotal *prometheus.CounterVec
// labels: model, provider_id, tenant_id

// JournalSnapshot operations
JournalSnapshotStoredTotal *prometheus.CounterVec
// labels: tenant_id

JournalSnapshotAppliedTotal *prometheus.CounterVec
// labels: tenant_id, success

JournalSnapshotDeduplicatedTotal *prometheus.CounterVec
// labels: tenant_id, reason (already_completed, hash_match)
```

**预计工作量:** 2-3 小时

---

#### 2. 添加 Receipts Map TTL/清理机制
**范围:** dispatchJourneyJournalAdapter
**文件:**
- `cmd/gateway/main_dispatch_observation.go`

**实现方案:**
```go
type journalSnapshotReceipt struct {
    hash      [sha256.Size]byte
    createdAt time.Time  // 新增
}

// 后台清理 goroutine
func (a *dispatchJourneyJournalAdapter) startReceiptCleaner(ttl time.Duration) {
    ticker := time.NewTicker(1 * time.Hour)
    go func() {
        for range ticker.C {
            a.cleanupOldReceipts(ttl)
        }
    }()
}

func (a *dispatchJourneyJournalAdapter) cleanupOldReceipts(ttl time.Duration) {
    a.mu.Lock()
    defer a.mu.Unlock()
    
    cutoff := time.Now().Add(-ttl)
    for key, receipt := range a.receipts {
        if receipt.createdAt.Before(cutoff) {
            delete(a.receipts, key)
        }
    }
}
```

**配置:** 默认 TTL 24 小时

**预计工作量:** 2-3 小时

---

#### 3. 添加 InMemoryJournalStore 容量上限
**范围:** domains/dispatch/journal_consumer.go
**文件:**
- `domains/dispatch/journal_consumer.go`

**实现方案:**
```go
type InMemoryJournalStore struct {
    mu       sync.Mutex
    snapshots map[snapshotKey]dispatch.JournalSnapshot
    capacity int  // 新增：最大存储数量
    lru      []snapshotKey  // 新增：LRU 顺序
}

// 使用 LRU 淘汰策略
func (s *InMemoryJournalStore) Store(snap dispatch.JournalSnapshot) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    key := snapshotKey{tenantID: snap.TenantID, requestID: snap.RequestID}
    
    // 如果已存在，移到 LRU 头部
    if _, exists := s.snapshots[key]; exists {
        s.moveToFront(key)
        s.snapshots[key] = snap
        return
    }
    
    // 如果达到容量上限，淘汰最旧的
    if len(s.snapshots) >= s.capacity {
        oldest := s.lru[len(s.lru)-1]
        delete(s.snapshots, oldest)
        s.lru = s.lru[:len(s.lru)-1]
    }
    
    // 添加新 snapshot
    s.snapshots[key] = snap
    s.lru = append([]snapshotKey{key}, s.lru...)
}
```

**配置:** 默认容量 10000 个 snapshots

**预计工作量:** 3-4 小时

---

### P2 - 中优先级（1-2 周内）

#### 4. Admin API 速率限制
**范围:** admin/journal_handlers.go
**文件:**
- `admin/journal_handlers.go`
- `middleware/ratelimit.go` (可能需要新建)

**实现方案:**
```go
// 使用 golang.org/x/time/rate
import "golang.org/x/time/rate"

type JournalSnapshotAPI struct {
    consumer dispatch.JournalSnapshotConsumer
    limiter  *rate.Limiter  // 新增
}

func (api *JournalSnapshotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Rate limiting
    if !api.limiter.Allow() {
        http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
        return
    }
    
    // ... 现有逻辑
}
```

**配置:**
- 全局速率: 100 req/s
- 每租户速率: 10 req/s
- Burst: 20

**预计工作量:** 3-4 小时

---

#### 5. 添加审计日志（Admin API 访问）
**范围:** admin/journal_handlers.go
**文件:**
- `admin/journal_handlers.go`
- `domains/audit/admin_access_log.go` (可能需要新建)

**实现方案:**
```go
// 记录所有 Admin API 访问，特别是跨租户访问
func (api *JournalSnapshotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    authCtx := GetAuthContext(r)
    tenantID, requestID, ok := parseJournalPath(r.URL.Path)
    
    // 审计日志
    if authCtx.Role == "super_admin" && authCtx.TenantID != tenantID {
        slog.Info("admin_cross_tenant_access",
            "admin_user", authCtx.UserID,
            "admin_tenant", authCtx.TenantID,
            "target_tenant", tenantID,
            "request_id", requestID,
            "source_ip", r.RemoteAddr,
        )
    }
    
    // ... 现有逻辑
}
```

**输出:**
- 结构化日志（slog）
- 可选：发送到审计系统

**预计工作量:** 2-3 小时

---

#### 6. 持久化 JournalSnapshotStore (Redis/PostgreSQL)
**范围:** domains/dispatch/journal_consumer.go
**文件:**
- `domains/dispatch/journal_consumer.go`
- `domains/dispatch/redis_journal_store.go` (新建)
- `domains/dispatch/postgres_journal_store.go` (新建)

**实现方案:**
```go
// Redis 实现
type RedisJournalSnapshotStore struct {
    client *redis.Client
    ttl    time.Duration
}

func (s *RedisJournalSnapshotStore) Store(snap dispatch.JournalSnapshot) {
    key := fmt.Sprintf("journal:snapshot:%s:%s", snap.TenantID, snap.RequestID)
    data, _ := json.Marshal(snap)
    s.client.Set(context.Background(), key, data, s.ttl)
}

// PostgreSQL 实现
type PostgresJournalSnapshotStore struct {
    pool *pgxpool.Pool
}

// CREATE TABLE journal_snapshots (
//   tenant_id TEXT NOT NULL,
//   request_id TEXT NOT NULL,
//   snapshot JSONB NOT NULL,
//   created_at TIMESTAMPTZ DEFAULT NOW(),
//   PRIMARY KEY (tenant_id, request_id)
// );
```

**选择:**
- Redis: 快速、TTL 自动过期、适合临时诊断
- PostgreSQL: 持久化、可查询、适合长期留存

**预计工作量:** 4-6 小时

---

### P3 - 低优先级（观测后决定）

#### 7. Empty Response 检测升级为实际失败
**前提:** 观察 `empty_response_body` 质量标志的发生频率

**当前状态:**
- 保守方案：仅观测，不修改 `reqLog.Success`
- 添加 `empty_response_body` 质量标志

**升级条件:**
- 如果发现频繁出现（例如 > 1% 的成功请求）
- 且确认是上游问题，而非正常响应

**升级方案:**
```go
func detectEmptyNonStreamResponse(reqLog *telemetry.RequestLogEntry) bool {
    // ... 现有检测逻辑
    
    if isEmpty {
        // 升级为实际失败
        reqLog.Success = false
        reqLog.Error = "empty_response_body"
        reqLog.ErrorKind = errorsx.KindUpstreamDown
        return true
    }
    return false
}
```

**预计工作量:** 1-2 小时

---

#### 8. 基于 Journal 的自动化故障分析
**范围:** 新模块
**文件:**
- `domains/analysis/failure_pattern.go` (新建)

**功能:**
- 基于 JournalSnapshot 自动识别故障模式
- 聚合相同 error pattern 的请求
- 生成故障报告

**实现思路:**
```go
type FailurePattern struct {
    Pattern     string  // "all_creds_exhausted", "model_unavailable"
    Frequency   int
    AffectedModels []string
    AffectedProviders []string
    ExampleRequests []string
}

func AnalyzeJournals(snapshots []dispatch.JournalSnapshot) []FailurePattern {
    // 分析 journal entries
    // 识别共同模式
    // 聚合统计
}
```

**预计工作量:** 1-2 天

---

#### 9. Admin UI 可视化
**范围:** Web 前端
**文件:**
- `web/src/pages/JournalViewer.tsx` (新建)

**功能:**
- 可视化展示 JournalSnapshot
- Timeline 显示 attempt 序列
- 错误高亮
- 租户过滤

**技术栈:** React + TypeScript

**预计工作量:** 2-3 天

---

## 任务依赖关系

```
并行任务组 A（无依赖，可同时执行）:
├── P1.1: 添加 Prometheus Metrics
├── P1.2: 添加 Receipts Map TTL
└── P1.3: 添加 InMemoryJournalStore 容量上限

并行任务组 B（依赖组 A 完成）:
├── P2.4: Admin API 速率限制
└── P2.5: 添加审计日志

任务 C（可独立进行）:
└── P2.6: 持久化 JournalSnapshotStore

观测任务（需观测数据后决策）:
├── P3.7: Empty Response 检测升级
└── P3.8: 自动化故障分析

UI 任务（可独立进行）:
└── P3.9: Admin UI 可视化
```

---

## 建议执行顺序

### 第一批（本周）:
1. P1.1: Prometheus Metrics
2. P1.2: Receipts Map TTL
3. P1.3: InMemoryJournalStore 容量上限

### 第二批（下周）:
4. P2.4: Admin API 速率限制
5. P2.5: 审计日志
6. P2.6: Redis JournalSnapshotStore

### 第三批（观测后）:
7. P3.7: Empty Response 升级（根据观测数据决定）
8. P3.8: 故障分析（如有需求）
9. P3.9: Admin UI（如有需求）

---

## 总工作量估算

- P1 任务: 7-10 小时（1-2 天）
- P2 任务: 9-13 小时（2-3 天）
- P3 任务: 3-5 天（如全部执行）

**建议:** 先执行 P1 和 P2 任务，P3 任务根据实际需求和观测数据决定。

---

## 环境准备

### 开发环境
- Go 1.21+
- PostgreSQL 14+ (如实现 P2.6)
- Redis 6+ (如实现 P2.6)
- prometheus/client_golang

### 测试环境
- 需要模拟高并发场景（速率限制测试）
- 需要 Redis/PostgreSQL 测试实例

### 监控环境
- Prometheus + Grafana
- 用于观测新 metrics

---

## 成功标准

每个任务完成后应满足:
1. ✅ 单元测试通过
2. ✅ 集成测试通过
3. ✅ go vet 通过
4. ✅ 文档更新
5. ✅ Code review 通过
6. ✅ 推送到 main 分支

---

**文档版本:** 1.0  
**创建日期:** 2026-08-29  
**基于:** .handoff/2026-08-29-audit-report.md
