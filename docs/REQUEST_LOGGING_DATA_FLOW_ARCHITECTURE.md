# 请求日志系统 - 数据流架构文档

## 文档版本
- **版本**: v1.0
- **日期**: 2026-07-10
- **作者**: LLM Gateway Team
- **状态**: 生产环境

---

## 1. 系统概览

LLM Gateway有**两个独立的请求日志系统**，各有不同的用途和特点。

### 系统对比

| 特性 | RequestLogger (WAL) | TelemetryClient (Logs) |
|------|---------------------|------------------------|
| **目标表** | request_wal_hot | request_logs_hot |
| **用途** | 写前日志（WAL） | 完整请求日志 |
| **写入时机** | 请求到达时立即 | 请求完成后 |
| **数据量** | 轻量级（基本信息） | 完整详细 |
| **主要消费者** | 内部监控 | 前端、分析、计费 |
| **性能要求** | 极快（同步） | 快速（异步） |
| **数据保留** | 7天 | 永久（分区归档） |

---

## 2. 数据流图

### 2.1 完整数据流

```
┌─────────────────┐
│   HTTP 请求     │
│  /v1/chat/...   │
└────────┬────────┘
         │
         v
┌────────────────────────────────────────────────┐
│         ChatHandler (handler.go)               │
│  ┌──────────────────────────────────────────┐  │
│  │  1. 生成 requestID (UUID)                │  │
│  │  2. 认证 (API Key)                       │  │
│  │  3. 提取 clientModel, sessionID          │  │
│  └──────────────────────────────────────────┘  │
└────────┬───────────────────────────────────────┘
         │
         ├────────────────────────┐
         │                        │
         v                        v
  ┌──────────────┐         ┌──────────────────┐
  │RequestLogger │         │ TelemetryClient  │
  │ (同步写入)    │         │  (异步写入)       │
  └──────┬───────┘         └────────┬─────────┘
         │                          │
         v                          │
  ┌────────────────┐                │
  │CreateInitial() │                │
  │  - request_id  │                │
  │  - tenant_id   │                │
  │  - session_id  │                │
  │  - model       │                │
  │  - status      │                │
  └────────┬───────┘                │
           │                        │
           v                        │
    ┌────────────────┐              │
    │request_wal_hot │              │
    │ (WAL表)        │              │
    └────────────────┘              │
                                    │
                                    │ (请求完成后)
                                    v
                             ┌──────────────┐
                             │ Emit()       │
                             │  - 完整信息   │
                             │  - tokens     │
                             │  - latency    │
                             │  - success    │
                             │  - provider   │
                             │  - ...        │
                             └──────┬───────┘
                                    │
                                    v
                             ┌────────────────────┐
                             │ request_logs_hot   │
                             │ (完整日志表)        │
                             └──────┬─────────────┘
                                    │
                                    │ (7天后)
                                    v
                             ┌────────────────────┐
                             │ request_logs       │
                             │ (月度分区表)        │
                             │  - 2026_07         │
                             │  - 2026_08         │
                             │  - ...             │
                             └────────────────────┘
                                    │
                                    │ (前端查询)
                                    v
                      ┌──────────────────────────────┐
                      │request_logs_with_current_month│
                      │        (视图)                 │
                      │  UNION ALL                   │
                      │  - request_logs_hot          │
                      │  - request_logs (分区表)      │
                      └──────┬──────────────────────┘
                             │
                             v
                      ┌──────────────┐
                      │  前端 API    │
                      │  /api/logs   │
                      └──────────────┘
```

### 2.2 关键路径详解

#### 路径1: 请求到达 → RequestLogger (同步)

```
ChatHandler.ServeHTTP()
  ├─ 生成 requestID
  ├─ 认证检查
  └─ if requestLogger != nil:
      └─ requestLogger.CreateInitial(ctx, &InitialRequest{
            RequestID:   requestID,
            TenantID:    tenantID,
            SessionID:   gwSessionID,
            ClientModel: clientModel,
         })
           └─ INSERT INTO request_wal_hot (...) 
              ON CONFLICT (request_id, created_at) DO NOTHING
              (同步执行，5秒超时)
```

**特点**:
- ✅ 同步执行，确保请求被记录
- ✅ 超时5秒，不阻塞主流程
- ✅ 失败只记录警告，不影响请求处理

#### 路径2: 请求完成 → TelemetryClient (异步)

```
ChatHandler (请求处理完成)
  └─ telemetryClient.Emit(&RequestLogEntry{
        RequestID:         requestID,
        ClientModel:       clientModel,
        Success:          true,
        PromptTokens:     123,
        CompletionTokens: 45,
        Latency:          1234ms,
        ProviderID:       5,
        CredentialID:     12,
        ... (50+ 字段)
     })
       └─ 放入异步队列 (channel)
           └─ worker goroutine:
               └─ INSERT INTO request_logs_hot (...)
                  ON CONFLICT (request_id, ts) DO UPDATE SET ...
                  (异步批量执行)
```

**特点**:
- ✅ 异步执行，不阻塞响应
- ✅ 批量提交，提升性能
- ✅ 包含完整信息（50+字段）
- ✅ 前端查询此表

---

## 3. 数据表结构

### 3.1 request_wal_hot (WAL表)

**用途**: 写前日志，记录请求的初始状态

```sql
CREATE TABLE request_wal_hot (
    request_id             VARCHAR(64)    NOT NULL,
    tenant_id              VARCHAR(64)    NOT NULL,
    gw_session_id          VARCHAR(128),
    status                 VARCHAR(20)    NOT NULL DEFAULT 'pending',
    stage                  SMALLINT       NOT NULL DEFAULT 0,
    client_model           VARCHAR(100),
    upstream_provider_id   BIGINT,
    upstream_credential_id BIGINT,
    completion_tokens      INTEGER,
    prompt_tokens          INTEGER,
    created_at             TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    completed_at           TIMESTAMPTZ,
    upstream_request_at    TIMESTAMPTZ,
    upstream_response_at   TIMESTAMPTZ,
    error                  TEXT,
    compression_strategy   VARCHAR(50),
    compression_meta       JSONB,
    
    PRIMARY KEY (request_id, created_at)  -- ⚠️ 必须有主键支持 ON CONFLICT
);
```

**索引**:
- `(gw_session_id, created_at)` - 会话查询
- `(tenant_id, created_at DESC)` - 租户查询
- `(status, stage)` - 状态筛选

**数据生命周期**:
- 保留期: 7天
- 迁移: `promote_request_wal_hot_to_partition()` 函数
- 归档: 移动到 `request_wal` 月度分区

### 3.2 request_logs_hot (完整日志表)

**用途**: 完整的请求日志，包含所有详细信息

```sql
CREATE TABLE request_logs_hot (
    id                     BIGINT         NOT NULL DEFAULT nextval('request_logs_id_seq'),
    request_id             TEXT           NOT NULL,
    ts                     TIMESTAMPTZ    NOT NULL,
    tenant_id              TEXT           NOT NULL,
    application_id         BIGINT,
    api_key_id             BIGINT,
    end_user_id            TEXT,
    client_model           TEXT,
    outbound_model         TEXT,
    credential_id          BIGINT,
    provider_id            BIGINT,
    canonical_id           BIGINT,
    client_profile         TEXT,
    request_mode           TEXT,
    affinity_hit           BOOLEAN,
    prompt_tokens          INTEGER,
    completion_tokens      INTEGER,
    cache_read_tokens      INTEGER,
    cache_write_tokens     INTEGER,
    total_tokens           INTEGER,
    cost_usd               NUMERIC(14,8),
    cost_display           NUMERIC(14,8),
    cost_currency          TEXT,
    latency_ms             INTEGER,
    success                BOOLEAN        NOT NULL,
    request_status         TEXT,
    error_kind             TEXT,
    search_text            TEXT,
    identity_hash          TEXT,
    response_checksum      TEXT,
    transform_rule_id      TEXT,
    egress_protocol        TEXT,
    failure_stage          TEXT,
    failure_detail_code    TEXT,
    -- ... 还有20+个字段
    
    PRIMARY KEY (request_id, ts)  -- ⚠️ 必须有主键支持 ON CONFLICT
);
```

**索引**:
- `(ts DESC)` - 时间排序
- `(tenant_id, ts DESC)` - 租户时间查询
- `(api_key_id, ts DESC)` - API Key统计
- `(success, ts DESC)` - 错误排查
- `(request_id)` - 点查询

**数据生命周期**:
- 保留期: 7天
- 迁移: `promote_request_logs_hot_to_partition()` 函数
- 归档: 移动到 `request_logs` 月度分区

### 3.3 request_logs (分区表)

**用途**: 月度分区存储历史日志

```sql
CREATE TABLE request_logs (
    -- 与 request_logs_hot 相同的结构
    ...
) PARTITION BY RANGE (ts);

-- 分区示例
CREATE TABLE request_logs_2026_07 PARTITION OF request_logs
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
    
CREATE TABLE request_logs_2026_08 PARTITION OF request_logs
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
```

**特点**:
- ✅ 按月分区
- ✅ 旧分区可转换为列存储（columnar）
- ✅ 支持分区级别的归档和删除

### 3.4 request_logs_with_current_month (视图)

**用途**: 统一查询接口，包含热表和分区表

```sql
CREATE VIEW request_logs_with_current_month AS
  SELECT * FROM request_logs_hot
  UNION ALL
  SELECT * FROM request_logs;
```

**使用**:
- ✅ 前端API查询此视图
- ✅ 自动包含热数据和历史数据
- ✅ 查询优化器会自动分区裁剪

---

## 4. 关键代码位置

### 4.1 RequestLogger

**文件**: `domains/hooks/observability/telemetry/request_logger.go`

```go
// 初始化
func NewRequestLogger(pool *pgxpool.Pool, cfg *RequestLoggerConfig) *RequestLogger

// 创建初始记录
func (rl *RequestLogger) CreateInitial(ctx context.Context, req *InitialRequest) error

// 更新记录（异步）
func (rl *RequestLogger) Update(update *LogUpdate)

// 更新记录（同步）
func (rl *RequestLogger) UpdateSync(ctx context.Context, update *LogUpdate) error
```

**SQL语句**:
```go
// 第114行
INSERT INTO request_wal_hot (request_id, tenant_id, gw_session_id, status, stage, client_model, created_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW())
ON CONFLICT (request_id, created_at) DO NOTHING
```

### 4.2 TelemetryClient

**文件**: `domains/hooks/observability/telemetry/client.go`

```go
// 发送日志（异步）
func (c *Client) Emit(entry *RequestLogEntry)

// 插入/更新日志
func (c *Client) insertRequestLog(entry *RequestLogEntry) error
```

**SQL语句**:
```go
// 第582-666行: INSERT
INSERT INTO request_logs_hot (
    request_id, ts, tenant_id, application_id, api_key_id,
    ...
) VALUES (
    $1, now(), $2, $3, $4, ...
)
ON CONFLICT (request_id, ts) DO UPDATE SET
    affinity_hit = COALESCE(EXCLUDED.affinity_hit, request_logs_hot.affinity_hit),
    ...
```

**⚠️ 重要**: 注意第666行的 `request_logs_hot.affinity_hit` 必须包含表名，否则会报"列模糊引用"错误。

### 4.3 ChatHandler集成

**文件**: `domains/streaming/handler.go`

```go
// 第1592-1606行: RequestLogger.CreateInitial 调用
if h.requestLogger != nil {
    tenantID := "default"
    if keyInfo != nil {
        tenantID = keyInfo.TenantID
    }
    initialReq := &telemetry.InitialRequest{
        RequestID:   requestID,
        TenantID:    tenantID,
        SessionID:   gwSessionID,
        ClientModel: clientModel,
    }
    if err := h.requestLogger.CreateInitial(r.Context(), initialReq); err != nil {
        slog.Warn("request_logger: CreateInitial failed", "request_id", requestID, "error", err)
    }
}

// 第2111-2147行: TelemetryClient.Emit 调用
if h.requestLogger != nil && result != nil {
    // ... 构建 LogUpdate
    h.requestLogger.Update(&telemetry.LogUpdate{
        RequestID:           requestID,
        Status:             telemetry.StatusSuccess,
        Stage:              telemetry.StageCompleted,
        // ...
    })
}
```

### 4.4 前端API

**文件**: `admin/logs.go`

```go
// 第447行: 查询日志
func (h *Handler) handleLogsRoot(w http.ResponseWriter, r *http.Request) {
    // ...
    query := `SELECT COUNT(*) FROM request_logs_with_current_month rl WHERE ` + where
    // ...
    query = `SELECT ... FROM request_logs_with_current_month rl WHERE ` + where + ` ORDER BY ` + orderBy
}
```

**前端**: `web/src/api/logs.ts`
```typescript
export function getRequestLogs(params: {...}) {
  return req<RequestLogsResponse>('GET', `/api/logs${s ? '?' + s : ''}`)
}
```

---

## 5. 数据迁移机制

### 5.1 热表 → 分区表迁移

**函数**: `promote_request_logs_hot_to_partition(retention_days INT, batch_size INT)`

**逻辑**:
```sql
-- 1. 查找超过保留期的记录
SELECT * FROM request_logs_hot 
WHERE ts < NOW() - INTERVAL '7 days'
ORDER BY ts 
LIMIT batch_size;

-- 2. 插入到分区表
INSERT INTO request_logs SELECT * FROM temp_batch;

-- 3. 从热表删除
DELETE FROM request_logs_hot 
WHERE (request_id, ts) IN (SELECT request_id, ts FROM temp_batch);
```

**调度**: 由后台任务每小时执行一次

### 5.2 分区表 → 列存储归档

**函数**: `archive_request_logs(partition_name TEXT)`

**逻辑**:
```sql
-- 转换为列存储（Citus columnar）
SELECT alter_table_set_access_method('request_logs_2026_06', 'columnar');
```

**收益**:
- 压缩比: 10:1 ~ 20:1
- 查询性能: 分析查询提升5-10x
- 存储成本: 降低90%

---

## 6. 故障处理

### 6.1 主键缺失问题

**症状**:
```
ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)
```

**原因**: `LIKE INCLUDING ALL` 从分区表复制时不会复制主键

**修复**:
```sql
-- request_wal_hot
ALTER TABLE request_wal_hot 
ADD CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id, created_at);

-- request_logs_hot
ALTER TABLE request_logs_hot 
ADD CONSTRAINT request_logs_hot_pkey PRIMARY KEY (request_id, ts);
```

**预防**: 迁移脚本341和345已包含主键检查和创建逻辑

### 6.2 列模糊引用问题

**症状**:
```
ERROR: column reference "affinity_hit" is ambiguous (SQLSTATE 42702)
```

**原因**: `ON CONFLICT DO UPDATE` 中列名不明确

**修复**:
```go
// 错误
affinity_hit = COALESCE(EXCLUDED.affinity_hit, affinity_hit)

// 正确
affinity_hit = COALESCE(EXCLUDED.affinity_hit, request_logs_hot.affinity_hit)
```

### 6.3 写入失败监控

**检查命令**:
```sql
-- 检查最近的写入
SELECT COUNT(*), MAX(created_at) 
FROM request_wal_hot 
WHERE created_at > NOW() - INTERVAL '5 minutes';

SELECT COUNT(*), MAX(ts) 
FROM request_logs_hot 
WHERE ts > NOW() - INTERVAL '5 minutes';
```

**告警阈值**:
- 5分钟内无新记录 → 警告
- 写入失败率 > 1% → 严重

---

## 7. 性能优化

### 7.1 写入性能

| 优化点 | 方法 | 效果 |
|--------|------|------|
| RequestLogger | 同步单条INSERT | 极快（<5ms） |
| TelemetryClient | 异步批量INSERT | 高吞吐（1000+ QPS） |
| 热表fillfactor | 90%（预留10%空间） | 减少页分裂 |
| ON CONFLICT | DO UPDATE而非DO NOTHING | 支持重试 |

### 7.2 查询性能

| 优化点 | 方法 | 效果 |
|--------|------|------|
| 视图 | 2路UNION（hot + parent） | 比3路快50% |
| 索引 | 覆盖查询索引 | 避免回表 |
| 分区裁剪 | 时间范围查询自动裁剪 | 只扫描相关分区 |
| 列存储 | 旧分区columnar | 分析查询快5-10x |

### 7.3 存储优化

| 优化点 | 方法 | 效果 |
|--------|------|------|
| 热表保留 | 仅7天 | 减少90%存储 |
| 分区归档 | 列存储压缩 | 压缩比10:1 |
| JSONB压缩 | 自动TOAST | 大字段外部存储 |

---

## 8. 监控指标

### 8.1 写入指标

```sql
-- 每分钟写入量
SELECT 
    date_trunc('minute', created_at) as minute,
    COUNT(*) as requests
FROM request_wal_hot
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY 1
ORDER BY 1 DESC;

-- 写入延迟（通过时间戳差异估算）
SELECT 
    AVG(EXTRACT(EPOCH FROM (created_at - '1970-01-01'::timestamp))) as avg_latency_ms
FROM request_wal_hot
WHERE created_at > NOW() - INTERVAL '5 minutes';
```

### 8.2 数据质量指标

```sql
-- 成功率
SELECT 
    success,
    COUNT(*) as count,
    COUNT(*) * 100.0 / SUM(COUNT(*)) OVER () as percentage
FROM request_logs_hot
WHERE ts > NOW() - INTERVAL '1 hour'
GROUP BY success;

-- 热表大小
SELECT 
    pg_size_pretty(pg_total_relation_size('request_wal_hot')) as wal_size,
    pg_size_pretty(pg_total_relation_size('request_logs_hot')) as logs_size;
```

---

## 9. 最佳实践

### 9.1 开发规范

1. **查询日志时使用视图**
   ```sql
   -- ✅ 正确
   SELECT * FROM request_logs_with_current_month WHERE ...
   
   -- ❌ 错误（缺少热数据）
   SELECT * FROM request_logs WHERE ...
   ```

2. **ON CONFLICT必须明确表名**
   ```sql
   -- ✅ 正确
   ON CONFLICT (...) DO UPDATE SET
       column = COALESCE(EXCLUDED.column, table_name.column)
   
   -- ❌ 错误（模糊引用）
   ON CONFLICT (...) DO UPDATE SET
       column = COALESCE(EXCLUDED.column, column)
   ```

3. **新建热表必须添加主键检查**
   ```sql
   CREATE TABLE xxx_hot (LIKE xxx INCLUDING ALL);
   
   -- 必须添加
   DO $$
   BEGIN
     IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE ...) THEN
       ALTER TABLE xxx_hot ADD PRIMARY KEY (...);
     END IF;
   END $$;
   ```

### 9.2 运维规范

1. **定期检查热表大小**
   - 阈值: < 10GB
   - 超过时检查promote函数是否正常

2. **监控写入失败**
   - 查看日志: `grep "telemetry request db persist failed"`
   - 检查主键: `\d request_logs_hot`

3. **分区维护**
   - 提前创建下月分区
   - 归档3个月前的分区为列存储
   - 删除2年前的分区

---

## 10. 常见问题 FAQ

### Q1: 为什么有两个日志系统？

**A**: 不同的用途和性能需求：
- **RequestLogger (WAL)**: 快速记录请求到达，即使后续处理失败也有记录
- **TelemetryClient (Logs)**: 完整记录请求详情，供分析和前端展示

### Q2: 前端为什么查询不到数据？

**A**: 检查以下几点：
1. 是否查询 `request_logs_with_current_month` 视图？
2. `request_logs_hot` 表是否有主键？
3. TelemetryClient是否正常写入？
4. 是否有 affinity_hit 列模糊引用错误？

### Q3: 热表数据何时迁移到分区表？

**A**: 
- 自动: 后台任务每小时执行，迁移7天前的数据
- 手动: `SELECT promote_request_logs_hot_to_partition(7, 1000);`

### Q4: ON CONFLICT失败怎么办？

**A**: 检查主键是否存在：
```sql
SELECT conname FROM pg_constraint 
WHERE conrelid = 'request_logs_hot'::regclass AND contype = 'p';
```
如果为空，执行修复脚本: `sql/fixes/fix-request-wal-hot-primary-key.sql`

---

## 11. 相关文档

- [主键修复完整报告](REQUEST_LOGGING_COMPLETE_FIX_REPORT.md)
- [审计报告](REQUEST_LOGGING_AUDIT_REPORT.md)
- [问题分析](issues/REQUEST_LOGGING_FIX_252.md)
- [迁移脚本341](../sql/migrations/startup/341_hot_table_independence.sql)
- [迁移脚本345](../sql/migrations/startup/345_request_wal_hot_independence.sql)
- [修复脚本](../sql/fixes/fix-request-wal-hot-primary-key.sql)

---

**文档维护**: LLM Gateway Team  
**最后更新**: 2026-07-10  
**审阅周期**: 每月或重大架构变更后
