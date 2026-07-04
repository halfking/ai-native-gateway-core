# 71 环境分区修复详细计划

**生成时间**: 2026-07-04 13:15  
**环境**: 71 生产环境 (14.103.174.71)

---

## 📊 当前状态分析

### **关键发现**

1. **服务状态**: ✅ **服务正在运行**
   - 最新写入时间: 2026-07-04 05:13:55 (0.6秒前)
   - 数据持续写入 DEFAULT 分区

2. **当前时间**: 2026-07-04 05:13:56
   - **应该路由到**: `request_logs_2026_07` 分区
   - **实际路由到**: `request_logs_default` (因为 2026_07 未附加)

3. **分区状态**:
   ```
   已附加的分区:
   - request_logs_default (heap, 16MB, 5,190行)
   - request_logs_2026_08 (columnar, 24KB, ~0行)
   
   孤立表（未附加）:
   - request_logs_2026_07 (heap, 674MB, 4,193行) ❌
   - request_wal_2026_07 (heap, 8.6MB, 18,455行) ❌
   - usage_ledger_2026_07 (heap, 6.9MB, 17,391行) ❌
   ```

4. **DEFAULT 分区数据分布**:
   - 6月数据: 5,143 行 (应该在 2026_06 分区，但 2026_06 不存在)
   - 7月数据: 47 行 (应该在 2026_07 分区)
   - **问题**: 6月和7月数据都错误地在 DEFAULT

---

## 🚨 核心问题

1. **缺少 2026-06 分区**
   - 5,143 行 6 月数据在 DEFAULT 中
   - 无处可迁移

2. **2026-07 分区未附加**
   - 孤立表有 4,193 行
   - DEFAULT 又有 47 行新数据
   - 服务持续写入

3. **并发写入冲突**
   - 无法在运行中附加分区（约束冲突）
   - 必须停服才能操作

---

## ✅ 推荐方案：分步骤修复（需要停服）

### **阶段 1: 准备工作（无需停服）**

1. 创建 2026-06 分区（用于接收 6 月数据）
2. 验证分区创建成功
3. 备份关键数据（可选）

### **阶段 2: 停服并修复（预计 15-20 分钟）**

1. **停止网关服务**
2. **清理并迁移数据**:
   - 将 DEFAULT 中的 6 月数据迁移到孤立表 `request_logs_2026_06_temp`
   - 将 DEFAULT 中的 7 月数据迁移到 `request_logs_2026_07`
   - 验证 DEFAULT 清空
3. **附加分区**:
   - 附加 2026-06 为 heap 分区
   - 附加 2026-07 为 heap 分区
4. **验证完整性**
5. **重启服务**

### **阶段 3: 验证（服务恢复后）**

1. 验证新数据写入 2026_07 分区
2. 验证查询跨分区正常
3. 监控 DEFAULT 分区（应该为空或极少数据）

---

## 📋 详细执行步骤

### **步骤 1: 创建 2026-06 分区（不停服）**

```sql
BEGIN;

-- request_logs_2026_06
CREATE TABLE request_logs_2026_06 (LIKE request_logs INCLUDING DEFAULTS) USING heap;
ALTER TABLE request_logs_2026_06 
ADD CONSTRAINT chk_compression_parent_single 
CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL)));
ALTER TABLE request_logs_2026_06 
ADD CONSTRAINT request_logs_strategy_used_check 
CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))));

-- request_wal_2026_06 (已存在，无需创建)

-- usage_ledger_2026_06 (已存在，无需创建)

COMMIT;
```

### **步骤 2: 停止网关服务**

```bash
ssh root@14.103.174.71
systemctl stop llm-gateway-go
# 验证服务已停止
systemctl status llm-gateway-go
```

### **步骤 3: 数据迁移和清理**

```sql
BEGIN;

-- 3.1 将 6 月数据迁移到 2026_06
INSERT INTO request_logs_2026_06 
SELECT * FROM request_logs_default 
WHERE ts >= '2026-06-01' AND ts < '2026-07-01';

DELETE FROM request_logs_default 
WHERE ts >= '2026-06-01' AND ts < '2026-07-01';

-- 3.2 将 7 月数据迁移到孤立的 2026_07
INSERT INTO request_logs_2026_07 
SELECT * FROM request_logs_default 
WHERE ts >= '2026-07-01' AND ts < '2026-08-01';

DELETE FROM request_logs_default 
WHERE ts >= '2026-07-01' AND ts < '2026-08-01';

-- 3.3 验证 DEFAULT 清空
SELECT 'request_logs_default 剩余行数: ' || COUNT(*)::text FROM request_logs_default;

COMMIT;
```

### **步骤 4: 附加分区**

```sql
BEGIN;

-- 4.1 附加 2026-06
ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_06 
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');

-- 4.2 附加 2026-07
ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_07 
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

COMMIT;

-- 验证
SELECT 
    c.relname as partition_name,
    am.amname as storage_type,
    pg_size_pretty(pg_relation_size(c.oid)) as size
FROM pg_class c
JOIN pg_inherits i ON i.inhrelid = c.oid
JOIN pg_class p ON i.inhparent = p.oid
LEFT JOIN pg_am am ON c.relam = am.oid
WHERE p.relname = 'request_logs'
ORDER BY c.relname;
```

### **步骤 5: 重启服务**

```bash
systemctl start llm-gateway-go
systemctl status llm-gateway-go

# 查看日志确认正常
journalctl -u llm-gateway-go -f
```

### **步骤 6: 验证数据正确性**

```sql
-- 6.1 验证新数据写入 2026_07
SELECT 
    'request_logs_2026_07' as partition,
    COUNT(*) as row_count,
    MAX(ts) as latest_ts
FROM request_logs_2026_07;

-- 6.2 验证 DEFAULT 为空
SELECT 
    'request_logs_default' as partition,
    COUNT(*) as row_count
FROM request_logs_default;

-- 6.3 验证总数据量
SELECT 
    'request_logs 总计' as info,
    COUNT(*) as total_rows
FROM request_logs;
```

---

## ⚠️ 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| 停服期间丢失请求 | 高 | 中 | 控制在 15 分钟内 |
| 数据迁移失败 | 低 | 高 | 使用事务，失败自动回滚 |
| 分区附加失败 | 低 | 中 | 验证 DEFAULT 已清空 |
| 服务启动失败 | 极低 | 高 | 准备回滚脚本 |

---

## 📌 关键注意事项

1. **必须停服**: 无法在运行中修复（并发写入冲突）
2. **使用事务**: 所有操作都在事务中，失败自动回滚
3. **保留 heap 格式**: 2026-06 和 2026-07 都保持 heap（不转 columnar）
4. **先处理 request_logs**: 其他表（request_wal, usage_ledger）同理

---

## 🔄 回滚计划

如果修复失败，回滚步骤：

```sql
-- 1. 分离分区
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_06;
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_07;

-- 2. 数据迁回 DEFAULT
INSERT INTO request_logs_default SELECT * FROM request_logs_2026_06;
INSERT INTO request_logs_default SELECT * FROM request_logs_2026_07;

-- 3. 删除分区表
DROP TABLE request_logs_2026_06;
-- request_logs_2026_07 保留为孤立表
```

---

## ✅ 成功标准

- [ ] 所有分区成功附加
- [ ] DEFAULT 分区为空或少于 10 行
- [ ] 新数据写入 2026_07 分区
- [ ] 查询跨分区正常
- [ ] 服务稳定运行

---

**预计停服时间**: 15-20 分钟  
**最佳执行时间**: 业务低峰期  
**需要人工确认**: 是否执行修复
