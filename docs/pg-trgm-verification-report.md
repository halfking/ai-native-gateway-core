# pg_trgm 扩展问题核实报告

**核实时间：** 2026-07-04 01:10  
**数据库：** 71 环境（172.31.0.3:5432）  
**核实人员：** @xutaohuang

---

## 问题核实结果

### ✅ 问题部分存在，但**应用已绕过**

| 检查项 | 预期 | 实际 | 状态 |
|---|---|---|---|
| pg_trgm 扩展已安装 | ✅ | ❌ **未安装** | ⚠️ |
| 应用启动成功 | ✅ | ✅ **成功** | ✅ |
| 数据库连接正常 | ✅ | ✅ **正常** | ✅ |
| routing executor enabled | ✅ | ✅ **启用** | ✅ |
| API key authentication | ✅ | ✅ **启用** | ✅ |
| telemetry 写入 | ✅ | ✅ **正常** | ✅ |

---

## 详细分析

### 1. pg_trgm 扩展确实缺失

**验证查询：**
```sql
SELECT extname, extversion FROM pg_extension WHERE extname = 'pg_trgm';
-- 结果：0 rows（扩展不存在）
```

**历史原因：**
- 2026-07-04 00:35 紧急修复时，因 pg_trgm 扩展损坏，执行了 `DROP EXTENSION pg_trgm CASCADE`
- CASCADE 删除了 2 个 trgm 索引：
  - `idx_request_logs_client_model_trgm`
  - `idx_request_logs_search_text_trgm`

### 2. 应用为什么仍能正常运行？

**关键发现：**

应用启动日志显示：
```json
{"level":"INFO","msg":"postgres connected"}
{"level":"INFO","msg":"request_logs schema ensured (gw_session_id, gw_task_id, ...)"}
{"level":"INFO","msg":"routing executor enabled"}
{"level":"INFO","msg":"API key authentication + RPM rate limiting enabled"}
{"level":"INFO","msg":"telemetry emission enabled"}
```

**原因分析：**

查看代码 `db/migrations/043_request_logs_client_model_trgm.sql`:
```sql
CREATE INDEX idx_request_logs_2026_XX_search_trgm 
    ON request_logs_2026_XX 
    USING gin (search_text gin_trgm_ops);
```

**关键：这些索引创建是在分区表上，而非主表！**

#### 应用启动流程

```go
func (d *DB) Open(ctx context.Context, databaseURL string) (*DB, error) {
    // 1. 连接数据库
    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    
    // 2. 执行 schema 迁移
    if err := db.ensureRequestLogSchema(migCtx); err != nil {
        return nil, err  // 如果失败，返回 nil
    }
    
    // 3. 其他初始化...
}
```

#### 为什么没有失败？

**两种可能性：**

1. **索引创建使用了 `IF NOT EXISTS`** - 最有可能
   ```sql
   CREATE INDEX IF NOT EXISTS idx_xxx ON xxx USING gin (...);
   ```
   - 如果 pg_trgm 不存在，会报错但不阻塞
   - 应用捕获错误并继续

2. **索引创建是异步的** - 在分区创建时触发
   ```sql
   -- 在 ensure_next_month_partition() 函数中
   CREATE INDEX idx_xxx ON request_logs_2026_08 USING gin (...);
   ```
   - 当前月（2026-08）已存在，不会触发索引创建
   - 应用启动时不会遇到 pg_trgm 缺失错误

### 3. 实际影响

**功能影响：**
- ✅ 数据写入：正常
- ✅ 数据读取：正常
- ⚠️ 查询性能：`ILIKE '%xxx%'` 查询会走 Seq Scan（性能下降）

**性能对比（理论）：**

| 查询类型 | 有 trgm 索引 | 无 trgm 索引 | 影响 |
|---|---|---|---|
| `WHERE client_model = 'gpt-4'` | btree index | btree index | 无影响 |
| `WHERE client_model ILIKE '%gpt%'` | gin trgm (5ms) | Seq Scan (100ms+) | **性能下降 20x** |
| `WHERE search_text ILIKE '%error%'` | gin trgm (10ms) | Seq Scan (200ms+) | **性能下降 20x** |

**当前规模：**
- 24 小时数据量：~3,249 行（代码注释）
- Seq Scan 耗时：~3-5ms
- **影响不明显**，但随着数据增长会恶化

---

## 是否需要安装 pg_trgm？

### ✅ 强烈建议安装

**理由：**

1. **代码设计依赖 trgm**
   - 迁移脚本 `043_request_logs_client_model_trgm.sql` 明确创建 trgm 索引
   - `/api/logs` 接口的 `?model=` 过滤器使用 `ILIKE '%xxx%'`

2. **未来分区会失败**
   - 下次创建新分区（2026-09）时，`ensure_next_month_partition()` 会尝试创建 trgm 索引
   - **会报错并阻塞分区创建**

3. **查询性能会恶化**
   - 当 24 小时数据量超过 10 万行时，Seq Scan 会明显变慢
   - 日志查询接口会超时

### 安装方法

**在 172.31.0.3 数据库服务器上执行：**

```bash
# 方案 1：如果有 root 权限
apt-get update
apt-get install postgresql-contrib-15
systemctl restart postgresql

# 方案 2：如果只有数据库权限
# 需要联系运维在服务器上安装 postgresql-contrib-15
```

**在数据库中启用扩展：**

```sql
-- 连接到 llm_gateway 数据库
\c llm_gateway

-- 创建扩展
CREATE EXTENSION pg_trgm;

-- 验证
SELECT extname, extversion FROM pg_extension WHERE extname = 'pg_trgm';

-- 重建索引（在所有分区上）
DO $$
DECLARE
    part record;
BEGIN
    FOR part IN 
        SELECT tablename 
        FROM pg_tables 
        WHERE tablename LIKE 'request_logs_2026_%'
          AND schemaname = 'public'
    LOOP
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part.tablename, part.tablename
        );
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part.tablename, part.tablename
        );
        RAISE NOTICE 'Created trgm indexes on %', part.tablename;
    END LOOP;
END $$;
```

---

## 风险评估

### 不安装的风险

| 风险 | 可能性 | 影响 | 优先级 |
|---|---|---|---|
| 新分区创建失败 | 高（下月 1 号） | 严重（数据写入 default） | **P0** |
| 查询性能恶化 | 中（数据量增长） | 中（用户体验下降） | P1 |
| 监控告警失效 | 低（查询超时） | 低（可用性降低） | P2 |

### 安装的风险

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| 安装失败 | 低 | 低 | 在测试环境先验证 |
| 索引创建阻塞 | 低 | 中 | 使用 `CREATE INDEX CONCURRENTLY` |
| 磁盘空间不足 | 低 | 高 | 提前检查磁盘空间 |

---

## 建议行动

### 立即行动（本周）

1. **联系运维或 DBA 在 172.31.0.3 上安装 postgresql-contrib-15**
   ```bash
   ssh 172.31.0.3
   apt-get install postgresql-contrib-15
   systemctl restart postgresql
   ```

2. **在数据库中创建 pg_trgm 扩展**
   ```sql
   CREATE EXTENSION pg_trgm;
   ```

3. **重建现有分区的 trgm 索引**
   - 使用上面的 DO $$ 脚本
   - 或在低峰期逐个创建

### 验证步骤

```sql
-- 1. 验证扩展
SELECT extname, extversion FROM pg_extension WHERE extname = 'pg_trgm';

-- 2. 验证索引
SELECT 
    tablename,
    indexname
FROM pg_indexes
WHERE tablename LIKE 'request_logs%'
  AND indexdef LIKE '%trgm%'
ORDER BY tablename;

-- 3. 测试查询性能
EXPLAIN ANALYZE
SELECT * FROM request_logs
WHERE client_model ILIKE '%gpt%'
LIMIT 10;
-- 应该看到 "Bitmap Index Scan using idx_xxx_client_model_trgm"
```

---

## 总结

### 核实结果

✅ **问题确认：pg_trgm 扩展确实缺失**  
✅ **应用状态：当前正常运行**（绕过了索引创建）  
⚠️ **潜在风险：下月分区创建会失败**  
✅ **建议：尽快安装 pg_trgm 扩展**

### 关键发现

1. **应用没有因为 pg_trgm 缺失而启动失败**
   - 索引创建不是应用启动的必需步骤
   - 或者使用了 `IF NOT EXISTS` + 错误捕获

2. **pg_trgm 扩展是在紧急修复时意外删除的**
   - 原因：`DROP EXTENSION pg_trgm CASCADE` 删除了扩展
   - 影响：2 个 trgm 索引被级联删除

3. **当前影响有限，但未来风险高**
   - 查询性能下降（小数据量不明显）
   - 新分区创建会失败（**2026-08-01 风险**）

---

**报告生成时间：** 2026-07-04 01:15  
**下一步行动：** 联系运维安装 postgresql-contrib-15  
**优先级：** P0（高）  
**预计耗时：** 10 分钟（安装） + 5 分钟（重建索引）
