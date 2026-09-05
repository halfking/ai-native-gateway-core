# LLM Gateway Phase 2 - 监控与资源管理增强

**项目:** llm-gateway-go  
**阶段:** Phase 2 - Observability & Resource Management  
**基线:** 2026-08-29 审计完成 (commit: 9891b2a12)  
**工作目录:** /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5

---

## 执行模式：主代理 + 子代理并行

你是主协调代理。**不要直接修改代码** — 为每个任务创建独立的子代理（`subagent_type="general-purpose"`）并行执行。

---

## 任务背景

Phase 1 完成了核心功能实现和审计，发现 3 个低风险改进项需要在 Phase 2 实施：

1. ⚠️ Receipts map 无界增长 → 需添加 TTL 清理
2. ⚠️ InMemoryJournalStore 无容量限制 → 需添加 LRU
3. ⚠️ 缺少 Prometheus metrics → 需添加可观测性

**目标:** 完成 P1 高优先级任务（3 个并行任务），质量标准：测试通过、文档完整、推送到 main。

---

## P1 任务（并行执行）

### 任务 1.1: Prometheus Metrics

**目标:** 为 Empty Response 检测和 JournalSnapshot 操作添加 metrics

**新增 Metrics:**
```go
// 1. Empty response 检测
RecordSuccessEmptyResponse(model, providerID, tenantID string)
// Counter: gateway_success_empty_response_total

// 2. JournalSnapshot 存储
RecordJournalSnapshotStored(tenantID string)
// Counter: gateway_journal_snapshot_stored_total

// 3. JournalSnapshot 应用
RecordJournalSnapshotApplied(tenantID string, success bool)
// Counter: gateway_journal_snapshot_applied_total

// 4. JournalSnapshot 去重
RecordJournalSnapshotDeduplicated(tenantID, reason string)
// Counter: gateway_journal_snapshot_deduplicated_total
// reason: "already_completed" | "hash_match"
```

**修改文件:**
- `metrics/interface.go` - 添加 4 个方法到 Recorder 接口
- `metrics/prometheus.go` - 实现 PrometheusRecorder
- `metrics/noop.go` - 实现 NoopRecorder
- `domains/streaming/handler.go` - 调用 RecordSuccessEmptyResponse
- `cmd/gateway/main_dispatch_observation.go` - 调用 Journal metrics

**测试要求:**
- 验证 counter 在事件发生时递增
- 验证 labels 正确
- 边界测试（nil tenant, 空 model）

**工作量:** 2-3 小时

---

### 任务 1.2: Receipts Map TTL 清理

**目标:** 防止 receipts map 无界增长

**实现方案:**
```go
// 1. 添加时间戳
type journalSnapshotReceipt struct {
    hash      [sha256.Size]byte
    createdAt time.Time  // 新增
}

// 2. 添加清理 goroutine
type dispatchJourneyJournalAdapter struct {
    // ... 现有字段
    cleanupTicker *time.Ticker
    stopCleanup   chan struct{}
}

// 3. 启动清理（1 小时间隔，24 小时 TTL）
func (a *dispatchJourneyJournalAdapter) startCleanup(ttl time.Duration) {
    a.cleanupTicker = time.NewTicker(1 * time.Hour)
    a.stopCleanup = make(chan struct{})
    go func() {
        for {
            select {
            case <-a.cleanupTicker.C:
                a.cleanupOldReceipts(ttl)
            case <-a.stopCleanup:
                return
            }
        }
    }()
}

// 4. 清理过期 receipts
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

// 5. 关闭时停止
func (a *dispatchJourneyJournalAdapter) Close() {
    if a.cleanupTicker != nil {
        a.cleanupTicker.Stop()
    }
    if a.stopCleanup != nil {
        close(a.stopCleanup)
    }
}
```

**修改文件:**
- `cmd/gateway/main_dispatch_observation.go`

**测试要求:**
- 测试 TTL 过期后清理
- 测试未过期的不被清理
- 测试并发安全
- 测试 goroutine 正确停止

**配置:** 默认 TTL 24 小时，清理间隔 1 小时

**工作量:** 2-3 小时

---

### 任务 1.3: InMemoryJournalStore LRU

**目标:** 添加容量上限和 LRU 淘汰策略

**实现方案:**
```go
type InMemoryJournalStore struct {
    mu        sync.Mutex
    snapshots map[snapshotKey]dispatch.JournalSnapshot
    capacity  int          // 新增
    lru       []snapshotKey  // 新增：最近在前
}

func NewInMemoryJournalStore(capacity int) *InMemoryJournalStore {
    if capacity <= 0 {
        capacity = 10000  // 默认
    }
    return &InMemoryJournalStore{
        snapshots: make(map[snapshotKey]dispatch.JournalSnapshot),
        capacity:  capacity,
        lru:       make([]snapshotKey, 0, capacity),
    }
}

func (s *InMemoryJournalStore) Store(snap dispatch.JournalSnapshot) {
    s.mu.Lock()
    defer s.mu.Unlock()
    key := snapshotKey{tenantID: snap.TenantID, requestID: snap.RequestID}
    
    // 已存在：更新并移到 LRU 头部
    if _, exists := s.snapshots[key]; exists {
        s.moveToFront(key)
        s.snapshots[key] = detachedSnapshot(snap)
        return
    }
    
    // 达到容量：淘汰 LRU 尾部
    if len(s.snapshots) >= s.capacity {
        oldest := s.lru[len(s.lru)-1]
        delete(s.snapshots, oldest)
        s.lru = s.lru[:len(s.lru)-1]
    }
    
    // 添加到 LRU 头部
    s.snapshots[key] = detachedSnapshot(snap)
    s.lru = append([]snapshotKey{key}, s.lru...)
}

func (s *InMemoryJournalStore) ConsumeSnapshot(ctx context.Context, tenantID, requestID string) (...) {
    s.mu.Lock()
    defer s.mu.Unlock()
    key := snapshotKey{tenantID: tenantID, requestID: requestID}
    snap, ok := s.snapshots[key]
    if !ok {
        return dispatch.JournalSnapshot{}, fmt.Errorf("not found")
    }
    s.moveToFront(key)  // 访问时更新 LRU
    return snap, nil
}

func (s *InMemoryJournalStore) moveToFront(key snapshotKey) {
    for i, k := range s.lru {
        if k == key {
            s.lru = append(s.lru[:i], s.lru[i+1:]...)
            break
        }
    }
    s.lru = append([]snapshotKey{key}, s.lru...)
}
```

**修改文件:**
- `domains/dispatch/journal_consumer.go`
- `cmd/gateway/main.go` - 使用 `NewInMemoryJournalStore(10000)`

**测试要求:**
- 测试容量限制生效
- 测试 LRU 逐出正确（最久未使用的被删除）
- 测试访问更新 LRU 顺序
- 测试并发安全
- 边界测试（capacity=1, capacity=0）

**配置:** 默认容量 10000

**工作量:** 3-4 小时

---

## 执行流程

### 1. 主代理启动（你）

```bash
# 确认环境
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5
git status  # 必须干净
git log --oneline -5  # 确认基线 9891b2a12
```

### 2. 创建 3 个并行子代理

**子代理都使用 `subagent_type="general-purpose"`**

为每个任务创建子代理，传递任务规格：

**子代理 1 - Prometheus Metrics:**
```
你负责为 Empty Response 检测和 JournalSnapshot 操作添加 Prometheus metrics。

工作目录: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5

需要添加 4 个 metrics:
1. RecordSuccessEmptyResponse(model, providerID, tenantID string)
2. RecordJournalSnapshotStored(tenantID string)
3. RecordJournalSnapshotApplied(tenantID string, success bool)
4. RecordJournalSnapshotDeduplicated(tenantID, reason string)

步骤:
1. 在 metrics/interface.go 添加 4 个方法到 Recorder 接口
2. 在 metrics/prometheus.go 实现（创建 CounterVec + Inc）
3. 在 metrics/noop.go 添加空实现
4. 在 domains/streaming/handler.go 调用 RecordSuccessEmptyResponse
5. 在 cmd/gateway/main_dispatch_observation.go 调用 Journal metrics
6. 编写单元测试验证 counter 递增
7. 运行测试: go test ./metrics/... ./domains/streaming/... -v

完成后报告并提交代码。
```

**子代理 2 - Receipts TTL:**
```
你负责为 dispatchJourneyJournalAdapter 的 receipts map 添加 TTL 清理机制。

工作目录: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5

修改文件: cmd/gateway/main_dispatch_observation.go

实现:
1. journalSnapshotReceipt 添加 createdAt time.Time 字段
2. dispatchJourneyJournalAdapter 添加 cleanupTicker 和 stopCleanup
3. 实现 startCleanup(ttl time.Duration) 方法（启动后台 goroutine）
4. 实现 cleanupOldReceipts(ttl time.Duration) 方法（删除过期）
5. 实现 Close() 方法（停止 goroutine）
6. 在构造函数中调用 startCleanup(24 * time.Hour)
7. 编写单元测试

配置: TTL 24 小时，清理间隔 1 小时

完成后报告并提交代码。
```

**子代理 3 - InMemoryStore LRU:**
```
你负责为 InMemoryJournalStore 添加容量上限和 LRU 淘汰策略。

工作目录: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5

修改文件:
- domains/dispatch/journal_consumer.go
- cmd/gateway/main.go

实现:
1. InMemoryJournalStore 添加 capacity int 和 lru []snapshotKey 字段
2. 修改 NewInMemoryJournalStore 接受 capacity 参数（默认 10000）
3. Store 方法实现 LRU 逐出（达到容量时删除最久未使用的）
4. ConsumeSnapshot 访问时更新 LRU（移到头部）
5. 添加 moveToFront 辅助方法
6. 添加 Size() 方法
7. 在 main.go 使用 NewInMemoryJournalStore(10000)
8. 编写单元测试（容量限制、LRU 逐出、并发安全）

完成后报告并提交代码。
```

### 3. 监控子代理进度

等待 3 个子代理完成，如遇到问题提供支持。

### 4. 汇总验证

所有子代理完成后：

```bash
# 运行完整测试套件
go test ./... -count=1

# 代码质量检查
go vet ./...
go build ./cmd/gateway
```

### 5. 统一推送

```bash
# 如果子代理各自提交了，直接推送
git pull --rebase
git push

# 或者统一一个 commit
git add -A
git commit -m "feat(phase2): add metrics, receipts TTL, and store LRU

P1.1: Add Prometheus metrics for empty response and journal operations
- RecordSuccessEmptyResponse, RecordJournalSnapshot* counters

P1.2: Add TTL-based cleanup for receipts map
- Background goroutine cleans up entries older than 24h

P1.3: Add LRU eviction to InMemoryJournalStore
- Capacity limit 10000 with LRU policy

All tests pass. Closes Phase 2 P1 tasks.
See: .handoff/next-phase-tasks.md"

git push
```

### 6. 生成 Phase 2 报告

创建 `.handoff/phase2-execution-report.md`，包含：
- 完成的任务清单
- 测试结果
- 提交记录
- 遇到的问题和解决方案

---

## 成功标准

每个任务必须:
1. ✅ 功能完整实现
2. ✅ 单元测试通过
3. ✅ 完整测试套件通过（`go test ./...`）
4. ✅ go vet 通过
5. ✅ 文档注释完整

Phase 2 整体:
1. ✅ 所有 P1 任务完成（3/3）
2. ✅ 代码推送到 origin/main
3. ✅ 生成执行报告

---

## 环境信息

**项目路径:** /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5  
**Git 分支:** main  
**基线 Commit:** 9891b2a12  
**Go 版本:** 1.21+

**相关文档:**
- 任务清单: `.handoff/next-phase-tasks.md`
- 审计报告: `.handoff/2026-08-29-audit-report.md`

**测试命令:**
```bash
go test ./metrics/... -v
go test ./domains/streaming/... -v
go test ./domains/dispatch/... -v
go test ./... -count=1
go vet ./...
```

---

## 开始执行

**准备好了吗？创建 3 个子代理并行执行 P1 任务！** 🚀

完成后生成 Phase 2 执行报告。
