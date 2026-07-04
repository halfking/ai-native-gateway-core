# PostgreSQL 分区表读写规范标准

**文档版本**: 1.0  
**创建日期**: 2026-07-04  
**强制执行**: ✅ 所有新代码必须遵守  
**适用范围**: `request_logs`, `usage_ledger`, 以及所有使用分区表的时序数据

---

## 📋 快速参考

### 写入规范

```go
// ✅ 正确
INSERT INTO request_logs_default (...) VALUES (...);
UPDATE request_logs_default SET ... WHERE ...;
DELETE FROM request_logs_default WHERE ...;

// ❌ 错误
INSERT INTO request_logs (...) VALUES (...);      // 禁止写父表
UPDATE request_logs SET ... WHERE ...;            // 禁止更新父表
DELETE FROM request_logs WHERE ...;               // 禁止删除父表
```

### 查询规范

```go
// ✅ 最近数据（推荐）
SELECT * FROM request_logs_default WHERE ts > now() - interval '7 days';

// ✅ 当月完整数据
SELECT * FROM request_logs_with_current_month WHERE ts >= '2026-07-01';

// ⚠️ 跨月查询
SELECT * FROM request_logs WHERE ts >= '2026-06-01'  -- 父表
UNION ALL
SELECT * FROM request_logs_2026_07;                  -- 当月 DETACHED 分区
```

---

## 1. 写入规范（强制）

### 1.1 INSERT 规范

#### 规则 1.1.1：硬编码 *_default 表名

**强制性**: ✅ 必须遵守  
**适用场景**: 所有新数据插入

```go
// ✅ 正确示例
_, err := tx.Exec(ctx, `
    INSERT INTO request_logs_default (
        request_id, ts, tenant_id, application_id,
        api_key_id, credential_id, success
    ) VALUES (
        $1, now(), $2, $3, $4, $5, $6
    )
    ON CONFLICT (request_id, ts) DO NOTHING
`,
    entry.RequestID,
    entry.TenantID,
    entry.ApplicationID,
    entry.APIKeyID,
    entry.CredentialID,
    entry.Success,
)

// ❌ 错误示例 1：写父表
_, err := tx.Exec(ctx, `
    INSERT INTO request_logs (...)  -- ❌ 禁止
    VALUES (...)
`, ...)

// ❌ 错误示例 2：动态表名（日常代码禁止）
tableName := "request_logs"  // ❌ 禁止
sql := fmt.Sprintf("INSERT INTO %s (...) VALUES (...)", tableName)
```

**例外情况**：
- 批量历史补录工具（使用 `partition_router.go`）

#### 规则 1.1.2：ON CONFLICT 列引用必须带 *_default 前缀

**强制性**: ✅ 必须遵守

```go
// ✅ 正确：所有列引用都带 request_logs_default 前缀
INSERT INTO request_logs_default (
    request_id, ts, prompt_tokens, success
) VALUES ($1, $2, $3, $4)
ON CONFLICT (request_id, ts) DO UPDATE SET
    prompt_tokens = COALESCE(EXCLUDED.prompt_tokens, request_logs_default.prompt_tokens),
    success = COALESCE(EXCLUDED.success, request_logs_default.success)
WHERE request_logs_default.request_id = EXCLUDED.request_id;

// ❌ 错误：列引用使用父表名
ON CONFLICT (request_id, ts) DO UPDATE SET
    prompt_tokens = COALESCE(EXCLUDED.prompt_tokens, request_logs.prompt_tokens)
    --                                                 ^^^^^^^^^^^^ ❌ 错误
```

**检查清单**：
- [ ] `ON CONFLICT` 子句中的所有 `SET` 赋值列引用
- [ ] `COALESCE` 函数中的列引用
- [ ] `WHERE` 子句中的列引用

### 1.2 UPDATE 规范

#### 规则 1.2.1：UPDATE 必须指向 *_default 表

**强制性**: ✅ 必须遵守  
**适用场景**: 流式响应更新、状态修正

```go
// ✅ 正确
_, err := tx.Exec(ctx, `
    UPDATE request_logs_default
    SET prompt_tokens = $2,
        completion_tokens = $3,
        success = $4
    WHERE request_id = $1
`,
    entry.RequestID,
    entry.PromptTokens,
    entry.CompletionTokens,
    entry.Success,
)

// ❌ 错误：UPDATE 父表
_, err := tx.Exec(ctx, `
    UPDATE request_logs  -- ❌ 禁止
    SET prompt_tokens = $2
    WHERE request_id = $1
`, ...)
```

**理由**：
- UPDATE 都是针对刚 INSERT 的记录（几秒钟前）
- 这些记录肯定在 `*_default` 表中
- UPDATE 父表会触发全分区扫描（包括 columnar 分区）

#### 规则 1.2.2：批量 UPDATE 禁止

**强制性**: ✅ 必须遵守

```go
// ❌ 禁止：批量 UPDATE（会锁表）
_, err := tx.Exec(ctx, `
    UPDATE request_logs_default
    SET success = true
    WHERE ts >= $1  -- ❌ 无主键条件
`, startTime)

// ✅ 正确：逐条 UPDATE 或使用 UPSERT
for _, entry := range entries {
    _, err := tx.Exec(ctx, `
        UPDATE request_logs_default
        SET success = $2
        WHERE request_id = $1  -- ✅ 有主键条件
    `, entry.RequestID, entry.Success)
}
```

### 1.3 DELETE 规范

#### 规则 1.3.1：DELETE 必须指向 *_default 表

**强制性**: ✅ 必须遵守

```go
// ✅ 正确
_, err := tx.Exec(ctx, `
    DELETE FROM request_logs_default
    WHERE request_id = $1
`, requestID)

// ❌ 错误：DELETE 父表
_, err := tx.Exec(ctx, `
    DELETE FROM request_logs  -- ❌ 禁止
    WHERE request_id = $1
`, requestID)
```

#### 规则 1.3.2：历史数据删除用 DROP TABLE

**强制性**: ✅ 必须遵守

```sql
-- ✅ 正确：删除整月历史数据
DROP TABLE request_logs_2025_12;

-- ❌ 错误：DELETE 历史分区（columnar 不支持）
DELETE FROM request_logs_2025_12 WHERE ts < '2026-01-01';
```

### 1.4 事务规范

#### 规则 1.4.1：写入必须在事务中

**强制性**: ✅ 必须遵守

```go
// ✅ 正确：使用事务
tx, err := db.Begin(ctx)
if err != nil {
    return err
}
defer tx.Rollback(ctx)

// INSERT usage_ledger
_, err = tx.Exec(ctx, `INSERT INTO usage_ledger_default ...`)
if err != nil {
    return err
}

// INSERT request_logs
_, err = tx.Exec(ctx, `INSERT INTO request_logs_default ...`)
if err != nil {
    return err
}

return tx.Commit(ctx)

// ❌ 错误：无事务保护
_, err = db.Exec(ctx, `INSERT INTO usage_ledger_default ...`)
_, err = db.Exec(ctx, `INSERT INTO request_logs_default ...`)
```

---

## 2. 查询规范（推荐）

### 2.1 查询模式选择

#### 规则 2.1.1：最近数据优先查 *_default

**强制性**: ⚠️ 强烈推荐（性能优化）

```go
// ✅ 最佳实践：判断时间范围，选择最优查询
func (c *Client) QueryRequestLogs(ctx context.Context, startTime time.Time) ([]RequestLog, error) {
    age := time.Since(startTime)
    
    var sql string
    if age < 7*24*time.Hour {
        // 最近 7 天：直接查 default（最快）
        sql = `SELECT * FROM request_logs_default WHERE ts >= $1`
    } else {
        // 超过 7 天：使用 VIEW（完整数据）
        sql = `SELECT * FROM request_logs_with_current_month WHERE ts >= $1`
    }
    
    return c.db.Query(ctx, sql, startTime)
}

// ⚠️ 次优：总是使用 VIEW（性能开销大）
func (c *Client) QueryRequestLogs(ctx context.Context, startTime time.Time) ([]RequestLog, error) {
    sql := `SELECT * FROM request_logs_with_current_month WHERE ts >= $1`
    return c.db.Query(ctx, sql, startTime)
}
```

#### 规则 2.1.2：必须带时间范围条件

**强制性**: ✅ 必须遵守（防止全表扫描）

```go
// ✅ 正确：带时间范围
rows, err := db.Query(ctx, `
    SELECT * FROM request_logs_default
    WHERE ts >= $1 AND ts < $2
    AND tenant_id = $3
`, startTime, endTime, tenantID)

// ❌ 错误：无时间范围（全表扫描）
rows, err := db.Query(ctx, `
    SELECT * FROM request_logs_default
    WHERE tenant_id = $1  -- ❌ 缺少 ts 条件
`, tenantID)
```

**例外情况**：
- 主键查询（`WHERE request_id = $1`）

### 2.2 VIEW 使用规范

#### 规则 2.2.1：当月数据查询使用 VIEW

**强制性**: ✅ 必须遵守（确保数据完整性）

```go
// ✅ 正确：使用 VIEW 查询当月数据
rows, err := db.Query(ctx, `
    SELECT COUNT(*) FROM request_logs_with_current_month
    WHERE ts >= $1
`, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))

// ❌ 错误：直接查父表（缺少 DETACHED 分区数据）
rows, err := db.Query(ctx, `
    SELECT COUNT(*) FROM request_logs
    WHERE ts >= $1
`, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
```

#### 规则 2.2.2：VIEW 定义标准

**强制性**: ✅ 必须遵守

```sql
-- ✅ 正确：VIEW 命名规范
CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs          -- 父表（ATTACHED 分区）
UNION ALL
SELECT * FROM request_logs_2026_07;  -- 当月 DETACHED 分区

-- ❌ 错误：缺少 UNION ALL
CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs;  -- ❌ 缺少当月 DETACHED 分区
```

### 2.3 聚合查询规范

#### 规则 2.3.1：COUNT 查询优化

```go
// ✅ 正确：使用 EXPLAIN ANALYZE 验证查询计划
rows, err := db.Query(ctx, `
    EXPLAIN ANALYZE
    SELECT COUNT(*) FROM request_logs_default
    WHERE ts >= $1
`, startTime)

// 检查是否使用索引扫描
// Seq Scan ❌ → Index Scan ✅
```

#### 规则 2.3.2：分组查询必须带时间范围

```go
// ✅ 正确
rows, err := db.Query(ctx, `
    SELECT tenant_id, COUNT(*) 
    FROM request_logs_default
    WHERE ts >= $1 AND ts < $2  -- ✅ 时间范围
    GROUP BY tenant_id
`, startTime, endTime)

// ❌ 错误
rows, err := db.Query(ctx, `
    SELECT tenant_id, COUNT(*) 
    FROM request_logs_default
    GROUP BY tenant_id  -- ❌ 无时间范围
`, ...)
```

---

## 3. 命名规范

### 3.1 表命名

| 类型 | 规范 | 示例 | 说明 |
|------|------|------|------|
| 父表 | `<table_name>` | `request_logs` | PARTITION BY RANGE(ts) |
| 月度分区 | `<table>_YYYY_MM` | `request_logs_2026_07` | 按月分区 |
| 默认分区 | `<table>_default` | `request_logs_default` | 热数据窗口 |
| 归档分区 | `<table>_archive_YYYY_MM` | `request_logs_archive_2026_06` | 历史归档 |

### 3.2 VIEW 命名

| 类型 | 规范 | 示例 |
|------|------|------|
| 当月完整数据 | `<table>_with_current_month` | `request_logs_with_current_month` |
| 跨年度数据 | `<table>_with_year_YYYY` | `request_logs_with_year_2026` |

### 3.3 索引命名

| 类型 | 规范 | 示例 |
|------|------|------|
| 主键索引 | `<table>_pkey` | `request_logs_2026_07_pkey` |
| 唯一索引 | `<table>_<cols>_idx` | `request_logs_2026_07_request_id_ts_idx` |
| 查询索引 | `<table>_<cols>_idx` | `request_logs_2026_07_tenant_ts_idx` |

---

## 4. 代码审查检查清单

### 4.1 写入代码审查

审查人员检查以下内容：

- [ ] 所有 `INSERT INTO` 指向 `*_default` 表（不是父表）
- [ ] 所有 `UPDATE` 指向 `*_default` 表
- [ ] 所有 `DELETE` 指向 `*_default` 表
- [ ] `ON CONFLICT` 子句中的列引用都带 `*_default` 前缀
- [ ] 写入操作在事务中
- [ ] 无批量 UPDATE（无主键条件）

### 4.2 查询代码审查

- [ ] 最近 7 天数据直接查 `*_default`
- [ ] 当月数据使用 `*_with_current_month` VIEW
- [ ] 所有查询都有 `ts` 范围条件（除非主键查询）
- [ ] 聚合查询带时间范围
- [ ] 无全表扫描（通过 EXPLAIN ANALYZE 验证）

### 4.3 维护脚本审查

- [ ] 每日迁移脚本正确性
- [ ] 月底转换脚本备份机制
- [ ] VIEW 更新脚本月份正确
- [ ] 监控告警配置完整

---

## 5. 测试规范

### 5.1 单元测试

每个写入函数必须包含以下测试：

```go
func TestEmitRequestLogInsert(t *testing.T) {
    // 测试 1：验证写入 default 表
    t.Run("writes to default table", func(t *testing.T) {
        client := NewClient(db)
        entry := &RequestLogEntry{
            RequestID: "test-" + uuid.New().String(),
            TenantID:  "test-tenant",
            Success:   true,
        }
        
        err := client.EmitRequestLogInsert(entry)
        require.NoError(t, err)
        
        // 验证数据在 default 表
        var count int
        err = db.QueryRow(ctx, `
            SELECT COUNT(*) FROM request_logs_default
            WHERE request_id = $1
        `, entry.RequestID).Scan(&count)
        require.NoError(t, err)
        assert.Equal(t, 1, count)
        
        // 验证数据不在当月分区
        err = db.QueryRow(ctx, `
            SELECT COUNT(*) FROM request_logs_2026_07
            WHERE request_id = $1
        `, entry.RequestID).Scan(&count)
        require.NoError(t, err)
        assert.Equal(t, 0, count)
    })
    
    // 测试 2：验证 UPSERT 语义
    t.Run("upsert semantics", func(t *testing.T) {
        // ... UPSERT 测试
    })
}
```

### 5.2 集成测试

```bash
# 集成测试脚本：tests/partition_write_test.sh
./tests/partition_write_test.sh

# 预期输出：
# ✅ 测试通过：数据正确写入 request_logs_default
# ✅ 测试通过：request_logs_2026_07 正确处于 DETACHED 状态
# ✅ 测试通过：UNION 查询包含更多数据
```

### 5.3 性能测试

```bash
# 性能基准测试
go test -bench=BenchmarkPartitionWrite -benchmem ./telemetry

# 预期指标：
# BenchmarkPartitionWrite/insert_default-8    500 req/s    <10ms p99
# BenchmarkPartitionWrite/update_default-8    300 req/s    <15ms p99
```

---

## 6. 监控规范

### 6.1 关键指标

| 指标 | 阈值 | 告警级别 |
|------|------|---------|
| `request_logs_default` 表大小 | > 10GB | P2 - 警告 |
| `request_logs_default` 表大小 | > 20GB | P1 - 严重 |
| 每日迁移任务失败 | 连续 2 次 | P1 - 严重 |
| 月底转换任务失败 | 1 次 | P0 - 紧急 |
| 写入错误率 | > 1% | P1 - 严重 |

### 6.2 监控 SQL

```sql
-- 监控 default 表大小
SELECT 
    pg_size_pretty(pg_total_relation_size('request_logs_default')) AS size,
    COUNT(*) AS rows,
    MIN(ts) AS earliest,
    MAX(ts) AS latest
FROM request_logs_default;

-- 监控数据分布
SELECT 
    CASE 
        WHEN ts >= now() - interval '7 days' THEN '7d'
        WHEN ts >= now() - interval '14 days' THEN '14d'
        WHEN ts >= now() - interval '30 days' THEN '30d'
        ELSE '30d+'
    END AS age_bucket,
    COUNT(*) AS rows
FROM request_logs_default
GROUP BY age_bucket
ORDER BY age_bucket;
```

---

## 7. 违规处理

### 7.1 违规等级

| 等级 | 定义 | 处理 |
|------|------|------|
| P0 - 阻断 | 写父表 / UPDATE 父表 | ❌ 代码审查拒绝 |
| P1 - 严重 | ON CONFLICT 列引用错误 | ⚠️ 代码审查警告，必须修复 |
| P2 - 警告 | 查询无时间范围 | ⚠️ 代码审查建议优化 |

### 7.2 检测工具

```bash
# 静态代码检查（可集成到 CI）
#!/bin/bash
# scripts/check-partition-compliance.sh

violations=0

# 检查是否有写父表的代码
if grep -r "INSERT INTO request_logs\b" --include="*.go" .; then
    echo "❌ 发现写父表代码（应该写 request_logs_default）"
    violations=$((violations+1))
fi

if grep -r "UPDATE request_logs\b" --include="*.go" .; then
    echo "❌ 发现更新父表代码（应该更新 request_logs_default）"
    violations=$((violations+1))
fi

# 检查 ON CONFLICT 列引用
if grep -r "request_logs\\.prompt_tokens" --include="*.go" .; then
    echo "❌ 发现错误的列引用（应该是 request_logs_default.prompt_tokens）"
    violations=$((violations+1))
fi

exit $violations
```

---

## 8. FAQ

### Q1: 为什么不能写父表？
**A**: 父表会根据 `ts` 自动路由到对应分区。如果当月分区是 columnar，UPSERT 会失败。

### Q2: 历史补录怎么办？
**A**: 使用 `partition_router.go` 工具，它会根据 `ts` 年龄动态选择目标表。

### Q3: 查询父表会丢数据吗？
**A**: 会！当月分区 DETACHED 后，父表查询不包含当月数据。必须使用 `*_with_current_month` VIEW。

### Q4: 为什么 default 表要定期清理？
**A**: 防止表无限增长。7 天前的数据迁移到月度分区，减少 default 表大小，提高查询性能。

### Q5: columnar 分区可以 UPDATE 吗？
**A**: 不可以！columnar 是只读存储，不支持 UPDATE/DELETE/ON CONFLICT。

---

## 9. 版本历史

| 版本 | 日期 | 变更 | 作者 |
|------|------|------|------|
| 1.0 | 2026-07-04 | 初始版本 | Infrastructure Team |

---

## 10. 附录

### 附录 A：完整示例代码

```go
// telemetry/client.go 标准写入模式
func (c *Client) EmitRequestLogInsert(entry *RequestLogEntry) error {
    ctx := context.Background()
    
    tx, err := c.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx)
    
    // 1. 插入 usage_ledger_default
    _, err = tx.Exec(ctx, `
        INSERT INTO usage_ledger_default (
            request_id, ts, tenant_id, prompt_tokens, success
        ) VALUES ($1, now(), $2, $3, $4)
        ON CONFLICT (request_id, ts) DO NOTHING
    `, entry.RequestID, entry.TenantID, entry.PromptTokens, entry.Success)
    if err != nil {
        return err
    }
    
    // 2. 插入 request_logs_default
    _, err = tx.Exec(ctx, `
        INSERT INTO request_logs_default (
            request_id, ts, tenant_id, success
        ) VALUES ($1, now(), $2, $3)
        ON CONFLICT (request_id, ts) DO UPDATE SET
            success = COALESCE(EXCLUDED.success, request_logs_default.success)
        WHERE request_logs_default.request_id = EXCLUDED.request_id
    `, entry.RequestID, entry.TenantID, entry.Success)
    if err != nil {
        return err
    }
    
    return tx.Commit(ctx)
}
```

### 附录 B：标准查询 VIEW

```sql
-- 创建标准查询 VIEW
CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_07;

CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger
UNION ALL
SELECT * FROM usage_ledger_2026_07;
```

### 附录 C：维护脚本模板

参见：
- `scripts/migrate-default-to-monthly.sh` - 每日迁移
- `scripts/convert-last-month-to-columnar.sh` - 月底转换
- `scripts/update-monthly-views.sh` - VIEW 更新

---

**文档所有权**: Infrastructure Team  
**联系方式**: infra@example.com  
**最后更新**: 2026-07-04
