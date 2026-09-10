# Lite 模式存储架构 - 快速参考

**用途**: 5 分钟速查，开发者/运维人员快速上手  
**完整文档**: 见 [lite-mode-implementation-status.md](./lite-mode-implementation-status.md)

---

## 一、5 秒理解

```
Full 模式 = PostgreSQL + Redis（生产集群）
Lite 模式 = SQLite + File + Memory（单机/边缘）
```

**适用场景**:
- ✅ 开发环境
- ✅ QPS < 1000 的单机生产
- ✅ 边缘设备/离线环境
- ❌ 多实例集群（用 Full）

---

## 二、快速启动

### 配置切换

```yaml
# config.yaml
storage_mode: "lite"  # 修改这一行

lite_storage:
  sqlite_path: "./data/llm-gateway.db"
  bodies_dir: "./data/session_bodies"
  cache_dir: "./data/cache"
  logs_dir: "./data/request_logs"
```

或环境变量：
```bash
export LLM_GATEWAY_STORAGE_MODE=lite
export LLM_GATEWAY_SQLITE_PATH=/var/lib/llm-gateway/gateway.db
```

### 启动

```bash
# 自动创建数据目录和初始化 SQLite
./llm-gateway -config config.yaml
```

---

## 三、核心组件速查

| 组件 | 实现 | 文件路径 | 用途 |
|------|------|---------|------|
| **会话元数据** | SQLite | `storage/sqlite/session_store.go` | 会话 CRUD + 查询 |
| **轮次元数据** | SQLite | `storage/sqlite/turns_store.go` | 轮次信息索引 |
| **会话内容** | 文件 + gzip | `storage/file/bodies_store.go` | 大对象存储（200KB-5MB） |
| **运行时状态** | 内存 KV | `storage/lite/state_store.go` | 限流计数/锁等 |
| **异步写入** | Go channel | `storage/file/async_writer.go` | 后台写入队列 |
| **一致性对账** | Worker | `storage/consistency.go` | 检测修复数据不一致 |
| **存储工厂** | 工厂模式 | `storage/factory/factory.go` | 模式切换 |

---

## 四、关键代码片段

### 4.1 使用存储接口

```go
// 主程序初始化
storageCfg := loadStorageConfig("config.yaml")
storageRT, _ := initStorageMode(logger, storageCfg)
defer storageRT.Shutdown()

// 创建存储实例（业务层不关心具体实现）
sessionStore := storageRT.factory.NewSessionStore()
bodiesStore := storageRT.factory.NewBodiesStore()

// 写入会话元数据
session := &storage.Session{
    ID:        "sess_123",
    TenantID:  "tenant_1",
    UserID:    "user_456",
    CreatedAt: time.Now(),
}
sessionStore.CreateSession(ctx, session)

// 写入会话内容（异步落盘，主流程 < 1ms）
body := &storage.SessionBody{
    TenantID:  "tenant_1",
    SessionID: "sess_123",
    TurnNo:    1,
    Request:   json.RawMessage(`{"prompt":"..."}`),
    Response:  json.RawMessage(`{"text":"..."}`),
}
bodiesStore.Write(ctx, body)  // 返回时已提交到队列
```

### 4.2 查询会话

```go
// 按 ID 查询
session, err := sessionStore.GetSession(ctx, "tenant_1", "sess_123")

// 分页列表
sessions, err := sessionStore.ListSessions(ctx, "tenant_1", &storage.ListOptions{
    Limit:  50,
    Offset: 0,
})

// 读取会话内容
body, err := bodiesStore.Read(ctx, "tenant_1", "sess_123", 1)  // 读取第 1 轮

// 批量读取
bodies, err := bodiesStore.ReadRange(ctx, "tenant_1", "sess_123", 1, 10)
```

### 4.3 状态存储（KV）

```go
stateStore := storageRT.factory.NewStateStore()

// 设置带 TTL 的键值
stateStore.Set(ctx, "rate_limit:user_123", 100, 60*time.Second)

// 读取
count, err := stateStore.Get(ctx, "rate_limit:user_123")

// 删除
stateStore.Delete(ctx, "rate_limit:user_123")
```

---

## 五、目录结构

```
data/
├── llm-gateway.db              # SQLite 主库
├── llm-gateway.db-shm          # WAL 共享内存
├── llm-gateway.db-wal          # WAL 日志
├── session_bodies/             # 会话内容
│   └── tenant_1/
│       └── se/                 # sessionID 前 2 位
│           └── sess_123/
│               ├── turn_1.json.gz
│               ├── turn_2.json.gz
│               └── turn_3.json.gz
├── cache/                      # 缓存（暂未使用）
└── logs/                       # 请求日志（暂未使用）
```

---

## 六、性能速查

| 操作 | Full 模式 | Lite 模式 | 提升 |
|------|----------|----------|------|
| 会话元数据写 | 50ms | 5ms | **10x** ⬆️ |
| 会话内容写 | 100ms | 1ms | **100x** ⬆️ |
| 会话内容读 | 20ms | 10ms | **2x** ⬆️ |
| 状态更新 | 2ms | 0.001ms | **2000x** ⬆️ |
| 内存占用 | 500MB+ | 200MB | **60%** ⬇️ |

---

## 七、SQLite 配置速查

| PRAGMA | 值 | 说明 |
|--------|---|------|
| `journal_mode` | WAL | 并发读不阻塞写 |
| `synchronous` | NORMAL | 性能与安全平衡 |
| `cache_size` | -64000 | 64MB 内存缓存 |
| `busy_timeout` | 5000 | 锁等待 5 秒 |
| `temp_store` | MEMORY | 临时表内存化 |
| `mmap_size` | 256MB | 内存映射文件 |

手动验证：
```bash
sqlite3 data/llm-gateway.db
> PRAGMA journal_mode;
> PRAGMA cache_size;
> PRAGMA integrity_check;
```

---

## 八、一致性对账配置

```yaml
lite_storage:
  consistency:
    enabled: true                    # 默认开启
    delete_orphan_bodies: false      # 默认只报告不删除（安全）
    interval_hours: 6                # 每 6 小时运行
    idle_threshold_min: 30           # 只检查 30 分钟无活动的会话
    max_sessions_per_run: 100        # 每次最多检查 100 个会话
```

**查看日志**:
```bash
grep "consistency: reconcile session" /var/log/llm-gateway.log | jq
```

**典型输出**:
```json
{
  "level": "info",
  "msg": "consistency: reconcile session",
  "tenant_id": "tenant_1",
  "session_id": "sess_123",
  "turns_with_meta": [1, 2, 3],
  "turns_with_body": [1, 2, 3],
  "consistent": true
}
```

**孤儿文件处理**:
```json
{
  "level": "warn",
  "msg": "consistency: orphan bodies detected",
  "tenant_id": "tenant_1",
  "session_id": "sess_456",
  "orphan_bodies": [5],
  "action": "report_only"
}
```

---

## 九、常见问题 FAQ

### Q1: 如何从 Full 切换到 Lite？

```bash
# 1. 停止服务
systemctl stop llm-gateway

# 2. 修改配置
vim config.yaml  # storage_mode: "lite"

# 3. 启动（自动初始化）
systemctl start llm-gateway
```

### Q2: SQLite 数据库锁定怎么办？

检查并发写入，正常情况下不应该出现：
```bash
# 查看正在写入的会话数
lsof data/llm-gateway.db

# 增加 busy_timeout（默认 5000ms）
sqlite_pragmas:
  busy_timeout_ms: 10000
```

### Q3: 磁盘空间占用太大？

调整保留策略：
```yaml
retention:
  session_bodies_days: 7   # 从 30 天减少到 7 天
```

手动清理：
```bash
# 删除 7 天前的会话文件
find data/session_bodies -type f -mtime +7 -delete
```

### Q4: 如何备份数据？

```bash
# SQLite 在线备份（不停服）
sqlite3 data/llm-gateway.db ".backup data/backup-$(date +%Y%m%d).db"

# 或使用 VACUUM INTO（更快）
sqlite3 data/llm-gateway.db "VACUUM INTO 'data/backup.db'"

# 会话内容备份（直接复制）
tar -czf session_bodies-$(date +%Y%m%d).tar.gz data/session_bodies/
```

### Q5: Lite 模式支持多实例吗？

**不支持**。SQLite 只支持单写入进程。需要多实例请用 Full 模式。

### Q6: 性能瓶颈在哪里？

| 场景 | 瓶颈 | 建议 |
|------|------|------|
| QPS < 500 | 无瓶颈 | 继续使用 Lite |
| QPS 500-1000 | SQLite 写入 | 增加 `cache_size`，启用 `mmap` |
| QPS > 1000 | SQLite 并发限制 | **切换 Full 模式** |
| 磁盘慢 | 文件写入 | 使用 SSD，增加 `async_writers` |

---

## 十、监控指标

### 关键指标

```promql
# SQLite 操作延迟
storage_sqlite_query_duration_seconds{operation="create_session"}

# 文件写入吞吐
storage_file_write_bytes_total

# 异步队列深度
storage_async_writer_queue_depth

# 一致性检查结果
storage_consistency_check_total{result="consistent"}
storage_consistency_check_total{result="orphan_bodies"}
```

### 告警规则

```yaml
- alert: SQLiteLockTimeout
  expr: rate(storage_sqlite_errors_total{error="SQLITE_BUSY"}[5m]) > 0.1
  for: 5m
  
- alert: AsyncWriterQueueFull
  expr: storage_async_writer_queue_depth > 1000
  for: 2m
  
- alert: OrphanBodiesDetected
  expr: storage_consistency_orphan_bodies_total > 100
  for: 1h
```

---

## 十一、故障排查速查表

| 症状 | 可能原因 | 排查命令 | 解决方案 |
|------|---------|---------|---------|
| 启动失败 | 数据目录权限 | `ls -la data/` | `chmod 755 data/` |
| 写入慢 | 磁盘 I/O 慢 | `iostat -x 1` | 换 SSD / 增加 `async_writers` |
| 查询慢 | SQLite 缓存小 | `PRAGMA cache_size;` | 增加 `cache_size_kb` |
| 锁超时 | 并发写入冲突 | 查看日志 `SQLITE_BUSY` | 增加 `busy_timeout_ms` |
| 磁盘满 | 旧数据未清理 | `du -sh data/*` | 调整 `retention` 策略 |
| 孤儿文件多 | 进程频繁崩溃 | 查看对账日志 | 启用 `delete_orphan_bodies` |

---

## 十二、代码位置速查

| 功能 | 文件路径 |
|------|---------|
| **存储接口定义** | `storage/interfaces.go` |
| **SQLite 会话存储** | `storage/sqlite/session_store.go` |
| **SQLite 轮次存储** | `storage/sqlite/turns_store.go` |
| **SQLite Schema** | `storage/sqlite/schema.go` |
| **文件内容存储** | `storage/file/bodies_store.go` |
| **异步写入器** | `storage/file/async_writer.go` |
| **内存状态存储** | `storage/lite/state_store.go` |
| **一致性对账** | `storage/consistency.go` |
| **存储工厂** | `storage/factory/factory.go` |
| **主程序集成** | `cmd/gateway/storage_mode_init.go` |
| **配置加载** | `config/storage.go` |
| **单元测试** | `storage/*/\*_test.go` |

---

## 十三、测试速查

```bash
# 运行所有存储相关测试
go test ./storage/... -v

# 只测试 SQLite
go test ./storage/sqlite -v

# 只测试文件存储
go test ./storage/file -v

# 只测试一致性对账
go test ./storage -run TestReconcile -v

# 集成测试
go test ./cmd/gateway -run TestInitStorageMode -v

# 性能基准测试
go test ./storage/file -bench=. -benchmem
```

---

## 十四、迁移速查

### Lite → Full

```bash
# 1. 导出 SQLite 数据
sqlite3 data/llm-gateway.db .dump > lite_data.sql

# 2. 转换并导入 PostgreSQL
sed 's/AUTOINCREMENT/SERIAL/g' lite_data.sql | psql llm_gateway

# 3. 迁移会话内容到 PG（需要脚本）
# 见 scripts/migrate_lite_to_full.go

# 4. 切换配置
storage_mode: "full"

# 5. 重启
systemctl restart llm-gateway
```

### Full → Lite

```bash
# 1. 导出 PostgreSQL 数据
pg_dump llm_gateway > full_data.sql

# 2. 转换并导入 SQLite（简化版）
# 复杂结构需要手动处理

# 3. 切换配置
storage_mode: "lite"

# 4. 重启
systemctl restart llm-gateway
```

---

## 十五、相关链接

- 📘 [完整实施报告](./lite-mode-implementation-status.md) - 架构概览与组件清单
- 📐 [架构决策记录](./lite-mode-architecture-decisions.md) - 7 个关键技术决策
- 🔧 [部署指南](../deployment/lite-mode-deployment.md) - 生产部署步骤
- 📊 [性能测试报告](../testing/lite-mode-performance.md) - 基准测试结果
- 🐛 [问题跟踪](https://github.com/kaixuan/llm-gateway-go/issues?q=label:lite-mode)

---

## 十六、版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-09-05 | 初始实现（审计 B-#2） |
| v1.1 | 2026-09-08 | 文档完善、测试补充 |

---

**维护**: LLM Gateway Team  
**更新**: 2026-09-08  
**下次复审**: 每月更新
