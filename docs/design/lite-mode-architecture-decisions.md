# Lite 模式存储架构 - 关键决策记录

**文档类型**: Architecture Decision Record (ADR)  
**创建日期**: 2026-09-08  
**状态**: 已采纳并实施  

---

## 决策概览

本文档记录了 LLM Gateway Lite 模式存储架构的 7 个关键技术决策，包括背景、考虑的方案、最终选择及其理由。

---

## ADR-1: 存储模式选择

### 背景

网关需要支持两种部署场景：
1. **生产集群**：多实例、高并发、需要分布式能力
2. **单机/边缘**：开发环境、小流量生产、资源受限环境

原有架构强依赖 PostgreSQL + Redis，无法在资源受限环境部署。

### 考虑的方案

| 方案 | 优点 | 缺点 | 决策 |
|------|------|------|------|
| **A. 只用 PostgreSQL** | 简单统一 | 无法满足边缘场景 | ❌ 拒绝 |
| **B. 嵌入式数据库（SQLite）** | 零依赖、简单 | 需要文件存储补充 | ✅ 采纳 |
| **C. 纯文件存储** | 最简单 | 无法高效查询 | ❌ 拒绝 |
| **D. 嵌入式 KV（BoltDB）** | 性能好 | 学习曲线、生态弱 | ❌ 拒绝 |

### 最终决策：混合架构（SQLite + 文件系统 + 内存）

**理由**:
- ✅ **SQLite**：成熟稳定，SQL 查询能力强，适合元数据
- ✅ **文件系统**：大对象存储（几百 KB - MB），避免 SQLite BLOB 性能问题
- ✅ **内存**：临时状态（KV、队列），无需持久化

**权衡**:
- ⚠️ 跨介质一致性需要额外保障（通过对账 worker 解决）
- ⚠️ 文件系统性能依赖磁盘（通过异步写入 + gzip 压缩缓解）

---

## ADR-2: 大对象存储策略

### 背景

会话内容（session bodies）是主要的大对象：
- 平均大小：200 KB
- 最大可达：5 MB（长上下文会话）
- 写入频率：每次会话轮次（高频）

### 考虑的方案

| 方案 | 优点 | 缺点 | 决策 |
|------|------|------|------|
| **A. SQLite BLOB** | 统一存储 | BLOB > 100KB 性能急剧下降 | ❌ 拒绝 |
| **B. 单文件存储** | 简单 | 大文件锁竞争、部分读写困难 | ❌ 拒绝 |
| **C. 轮次级文件** | 独立可寻址、无锁竞争 | 文件数量多 | ✅ 采纳 |
| **D. 对象存储（S3）** | 扩展性好 | Lite 模式违背零依赖原则 | ❌ 拒绝 |

### 最终决策：轮次级文件 + 分层目录

**文件布局**:
```
{baseDir}/{tenant_id}/{session_id前2位}/{session_id}/turn_{turnNo}.json.gz
```

**理由**:
- ✅ 每个轮次独立文件，无锁竞争
- ✅ gzip 压缩节省 70% 存储（LLM 文本重复度高）
- ✅ 分层目录避免单目录文件过多（Linux ext4 单目录推荐 < 10000 文件）
- ✅ 按会话删除简单（`rm -rf session_dir`）

**路径安全**:
```go
// 防止路径遍历攻击（审计 P1）
func validPathID(id string) bool {
    return id != "" && id != "." && id != ".." && 
           !strings.ContainsAny(id, `/\`)
}
```

---

## ADR-3: 异步写入架构

### 背景

会话内容写入是热路径：
- Full 模式：PostgreSQL JSONB 写入耗时 100ms
- Lite 模式目标：< 5ms（不阻塞主流程）

### 考虑的方案

| 方案 | 延迟 | 复杂度 | 数据安全 | 决策 |
|------|------|--------|----------|------|
| **A. 同步写入** | 高（100ms+） | 低 | 高 | ❌ 拒绝 |
| **B. Go channel 队列** | 低（1ms） | 中 | 中（内存丢失） | ✅ 采纳 |
| **C. 外部队列（Kafka）** | 低 | 高 | 高 | ❌ 违背零依赖 |
| **D. WAL + 批量刷盘** | 中（10ms） | 高 | 高 | 🔮 未来优化 |

### 最终决策：Go channel + 后台 worker 池

**实现** (`file/async_writer.go`):
```go
type AsyncFileWriter struct {
    workers   int
    queue     chan *writeTask
    wg        sync.WaitGroup
}

// 主流程：提交任务到队列（非阻塞）
func (w *AsyncFileWriter) Write(path string, data []byte) error {
    task := &writeTask{path: path, data: data, done: make(chan error, 1)}
    w.queue <- task      // 队列提交：< 1ms
    return <-task.done   // 等待落盘完成
}

// 后台 worker：gzip + 原子写入
func (w *AsyncFileWriter) worker() {
    for task := range w.queue {
        tmp := task.path + ".tmp"
        os.WriteFile(tmp, task.data, 0644)  // 写临时文件
        os.Rename(tmp, task.path)           // 原子 rename
        task.done <- nil
    }
}
```

**权衡**:
- ✅ 主流程延迟 < 1ms（队列提交）
- ✅ 后台批量写入，充分利用磁盘顺序写
- ⚠️ 进程崩溃会丢失队列中未落盘的数据（通过 meta 后写 + 对账修复）

---

## ADR-4: SQLite 配置与调优

### 背景

SQLite 默认配置面向嵌入式场景，不适合服务器工作负载。

### 关键 PRAGMA 决策

| PRAGMA | 默认值 | Lite 模式 | 理由 |
|--------|--------|----------|------|
| `journal_mode` | DELETE | **WAL** | 并发读，不阻塞写 |
| `synchronous` | FULL | **NORMAL** | WAL 下安全，性能提升 3x |
| `cache_size` | -2000 (2MB) | **-64000 (64MB)** | 减少磁盘 I/O |
| `busy_timeout` | 0 | **5000ms** | 避免 SQLITE_BUSY 错误 |
| `temp_store` | DEFAULT | **MEMORY** | 临时表内存化 |
| `mmap_size` | 0 | **268435456 (256MB)** | 利用内存映射 |

**实现** (`sqlite/schema.go`):
```go
func DefaultPragmas() []Pragma {
    return []Pragma{
        {Name: "journal_mode", Value: "WAL"},
        {Name: "synchronous", Value: "NORMAL"},
        {Name: "cache_size", Value: "-64000"},
        {Name: "busy_timeout", Value: "5000"},
        {Name: "temp_store", Value: "MEMORY"},
        {Name: "mmap_size", Value: "268435456"},
    }
}
```

**WAL 模式原理**:
```
传统模式（DELETE）:
  写操作加排他锁 → 读被阻塞 → 并发差

WAL 模式（Write-Ahead Log）:
  写操作追加 WAL 文件 → 读继续访问主文件 → 并发读不阻塞
  定期 checkpoint 将 WAL 合并回主文件
```

**性能提升**:
- 并发读：无限制（vs DELETE 模式的串行）
- 写入吞吐：提升 3-5x（NORMAL vs FULL）

---

## ADR-5: 跨介质一致性保障

### 背景

Lite 模式写入顺序（`lite_telemetry_sink.go`）：
1. **body 异步落盘**（file）
2. **meta 提交**（SQLite）

中断窗口会导致：
- **孤儿 body**：body 已落盘，meta 缺失
- **缺失 body**：meta 存在，body 写失败

### 考虑的方案

| 方案 | 一致性 | 性能 | 复杂度 | 决策 |
|------|--------|------|--------|------|
| **A. 两阶段提交（2PC）** | 强 | 低 | 高 | ❌ 过度设计 |
| **B. 事务日志 + 回放** | 强 | 中 | 高 | ❌ 复杂度高 |
| **C. 后台对账 + 修复** | 最终 | 高 | 中 | ✅ 采纳 |
| **D. 不处理** | 无 | 高 | 低 | ❌ 不满足审计要求 |

### 最终决策：后台对账 Worker + 双保险删除

**检测原语** (`storage/consistency.go`):
```go
func ReconcileTurnArtifacts(ctx, tenantID, sessionID, bodies, turns) 
    -> *TurnArtifactReport {
    
    // 1. 读取元数据侧 turn 集合
    metaTurns := turns.GetTurnsMeta(...)
    
    // 2. 列出文件侧 turn 集合
    bodyTurns := bodies.ListTurns(...)
    
    // 3. 差集分析
    return &TurnArtifactReport{
        OrphanBodies:  bodyTurns - metaTurns,  // 孤儿文件
        MissingBodies: metaTurns - bodyTurns,  // 缺失内容
    }
}
```

**修复原语** - 双保险机制：
```go
func RepairTurnArtifacts(report, bodies, turns, action) -> *RepairResult {
    for _, turn := range report.OrphanBodies {
        // 保险 1：删除前复检（double-confirm）
        stillOrphan := turnAbsentInMeta(turns, turn)
        if !stillOrphan {
            skip(turn)  // 在途写入已完成，保留
            continue
        }
        
        // 保险 2：mtime 宽限（10 分钟）
        mtime := bodies.TurnFileModTime(turn)
        if now - mtime < 10min {
            skip(turn)  // 可能仍在途，保留
            continue
        }
        
        // 双保险通过，执行删除
        bodies.DeleteTurnFile(turn)
    }
}
```

**运行策略**:
- 默认 **report-only**（只报告不删除）
- 显式启用 `delete_orphan_bodies` 才清理
- 只检查空闲会话（最后活动 > 30 分钟）

---

## ADR-6: 内存状态存储设计

### 背景

Full 模式使用 Redis 存储临时状态（限流计数、会话锁等），Lite 模式需要替代方案。

### 考虑的方案

| 方案 | 性能 | 持久化 | 复杂度 | 决策 |
|------|------|--------|--------|------|
| **A. sync.Map** | 高 | 无 | 低 | ⚠️ 不支持 TTL |
| **B. go-cache** | 高 | 无 | 低 | ✅ 采纳思路 |
| **C. 内嵌 Redis（miniredis）** | 中 | 可选 | 中 | ❌ 过度依赖 |
| **D. SQLite** | 低 | 有 | 中 | ❌ 性能不足 |

### 最终决策：自定义实现 + TTL + 后台清理

**实现** (`memory/state_store.go`):
```go
type MemoryStateStore struct {
    mu    sync.RWMutex
    items map[string]*item
    stopCh chan struct{}  // 停止清理协程
}

type item struct {
    value      interface{}
    expiration time.Time
    hasExpiry  bool
}

// 后台清理协程（30s 扫描一次）
func (s *MemoryStateStore) janitor() {
    ticker := time.NewTicker(30 * time.Second)
    for {
        select {
        case <-ticker.C:
            s.sweep(time.Now())  // 删除过期条目
        case <-s.stopCh:
            return
        }
    }
}

// 读路径：惰性删除 + 双检
func (s *MemoryStateStore) Get(key string) (interface{}, error) {
    s.mu.RLock()
    item := s.items[key]
    if !item.expiredAt(now) {
        s.mu.RUnlock()
        return item.value, nil  // 快路径：未过期直接返回
    }
    s.mu.RUnlock()
    
    // 慢路径：加写锁双检后删除
    s.mu.Lock()
    if s.items[key] == item {  // 指针相等才删除
        delete(s.items, key)
    }
    s.mu.Unlock()
    return nil, ErrNotFound
}
```

**语义对齐 Redis**:
- ✅ `Set(key, value, ttl)` - 支持 TTL
- ✅ `Get(key)` - 过期即不存在
- ✅ `Delete(key)` - 幂等删除
- ⚠️ 无持久化（单机重启可接受）

---

## ADR-7: 模式切换与工厂模式

### 背景

需要在不改变业务代码的前提下切换存储实现。

### 考虑的方案

| 方案 | 侵入性 | 灵活性 | 决策 |
|------|--------|--------|------|
| **A. 编译期条件编译** | 低 | 低 | ❌ 无法运行时切换 |
| **B. 接口 + 工厂模式** | 低 | 高 | ✅ 采纳 |
| **C. 依赖注入框架** | 高 | 高 | ❌ 过度设计 |

### 最终决策：统一接口 + 工厂分派

**架构**:
```go
// 1. 定义统一接口（storage/interfaces.go）
type SessionStore interface {
    CreateSession(ctx, *Session) error
    GetSession(ctx, tenantID, sessionID string) (*Session, error)
    // ...
}

// 2. 两种实现
// - Full: postgresql.PgSessionStore
// - Lite: sqlite.SQLiteSessionStore

// 3. 工厂分派（storage/factory/factory.go）
type StorageFactory struct {
    mode storage.StorageMode
    // ...
}

func (f *StorageFactory) NewSessionStore() storage.SessionStore {
    switch f.mode {
    case storage.StorageModeFull:
        return newPgSessionStore(f.pgPool)
    case storage.StorageModeLite:
        return sqlite.NewSQLiteSessionStore(f.sqlDB)
    }
}

// 4. 业务层透明使用
sessionStore := factory.NewSessionStore()
sessionStore.CreateSession(...)  // 不关心底层实现
```

**依赖方向**（避免循环依赖）:
```
业务层（domains）
    ↓ import
存储抽象（storage/interfaces.go）
    ↑ import
实现包（storage/sqlite, storage/file）
    ↑ import
工厂包（storage/factory）← 独立包，协调各实现
```

**配置切换**:
```yaml
# 配置文件
storage_mode: "lite"  # 或 "full"

# 或环境变量
LLM_GATEWAY_STORAGE_MODE=lite
```

**惰性单例**（避免资源泄漏）:
```go
// FileBodiesStore 持有后台 worker，不能每次新建
func (f *StorageFactory) NewBodiesStore() storage.BodiesStore {
    f.liteMu.Lock()
    defer f.liteMu.Unlock()
    if f.bodiesStore == nil {
        f.bodiesStore = filestore.NewFileBodiesStore(...)  // 首次创建
    }
    return f.bodiesStore  // 复用实例
}

// Close 时统一清理
func (f *StorageFactory) Close() error {
    if f.bodiesStore != nil {
        f.bodiesStore.Close()  // 排空队列，停止 worker
    }
}
```

---

## 决策影响矩阵

| 决策 | 性能 | 可靠性 | 复杂度 | 部署便利性 |
|------|------|--------|--------|-----------|
| ADR-1: 混合架构 | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐☆ | ⭐⭐⭐☆☆ | ⭐⭐⭐⭐⭐ |
| ADR-2: 轮次级文件 | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐☆ |
| ADR-3: 异步写入 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐☆☆ | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐☆ |
| ADR-4: SQLite 调优 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ |
| ADR-5: 对账 Worker | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐☆☆ | ⭐⭐⭐⭐☆ |
| ADR-6: 内存状态 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐☆☆ | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐⭐ |
| ADR-7: 工厂模式 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐☆ | ⭐⭐⭐⭐⭐ |

---

## 待解决的技术债

### 高优先级

- [ ] **SQLite 备份机制**：当前无自动备份，建议增加 `VACUUM INTO` 定期备份
- [ ] **文件归档策略**：老会话文件应归档到冷存储（tar.gz）
- [ ] **监控指标完善**：缺少 SQLite 锁竞争、文件系统 I/O 监控

### 中优先级

- [ ] **在线迁移工具**：Lite → Full 模式迁移需要停机，建议提供在线迁移脚本
- [ ] **压缩算法可选**：gzip 压缩比好但 CPU 密集，可考虑 zstd（更快）
- [ ] **多读副本支持**：SQLite WAL 支持多读进程，可做只读副本分流

### 低优先级

- [ ] **嵌入式 LMDB**：探索 LMDB 替代文件存储（更快的 KV 存储）
- [ ] **混合模式**：SQLite + Redis（单机写 + 分布式缓存）

---

## 参考资料

- [SQLite WAL 模式详解](https://www.sqlite.org/wal.html)
- [Go channel 最佳实践](https://go.dev/blog/pipelines)
- [文件系统性能调优](https://www.kernel.org/doc/html/latest/filesystems/ext4.html)
- [审计 B-#2：跨介质一致性补偿](../../audit/2026-09-05-consistency-reconciliation.md)

---

**维护者**: LLM Gateway Team  
**最后审核**: 2026-09-08  
**下次复审**: 2026-12-08（每季度）
